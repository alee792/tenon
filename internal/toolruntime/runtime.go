package toolruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"time"
	"unicode/utf8"
)

// staleCache is the one message every missing, partial, or superseded cache
// produces. A harness that starts a server against setup nobody prepared must
// fail closed with an instruction, not serve half a tool surface.
var staleCache = errors.New("tool runtime is missing or changed; run tenon apply")

// toolNamePattern is the exposed tool-name grammar, identical to the managed
// boundary's. A host may not report a name outside it.
var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Runtime is one open tool runtime: one live host per authored language, the
// merged catalog, and the route from tool name to the host that serves it.
type Runtime struct {
	hosts       []*host
	routes      map[string]*host
	definitions []Definition
}

// Open launches one host per authored language against an already prepared
// cache and merges their catalogs. It never prepares: a cache that is missing,
// partial, or written by another tenon fails closed.
func Open(cfg Config) (*Runtime, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	dir := cfg.CacheDir()
	if err := verifyCache(cfg, dir); err != nil {
		return nil, err
	}
	executables, err := readExecutables(dir, cfg.languages())
	if err != nil {
		return nil, err
	}
	return launch(cfg, dir, executables)
}

// launch starts every present language host, reads each catalog once, and
// cross-checks the merged surface against discovery.
func launch(cfg Config, dir string, executables map[string]string) (*Runtime, error) {
	rt := &Runtime{routes: map[string]*host{}}
	expected := cfg.expected()
	for _, language := range cfg.languages() {
		cmd, err := hostCommand(cfg, dir, language, executables)
		if err != nil {
			rt.Close()
			return nil, err
		}
		h, err := startHost(language, cmd)
		if err != nil {
			rt.Close()
			return nil, inspectFailure(language, "the %s language host could not be started", language)
		}
		rt.hosts = append(rt.hosts, h)

		reported, err := h.catalog(InspectTimeout)
		if err != nil {
			rt.Close()
			return nil, inspectFailure(language, "the %s tool catalog could not be read: %s", language, bound(err.Error()))
		}
		if err := validateCatalog(language, expected[language], reported); err != nil {
			rt.Close()
			return nil, err
		}
		if err := rt.adopt(h, reported); err != nil {
			rt.Close()
			return nil, err
		}
	}
	sort.Slice(rt.definitions, func(i, j int) bool { return rt.definitions[i].Name < rt.definitions[j].Name })
	return rt, nil
}

// adopt merges one host's catalog into the runtime, routing each tool name to
// the host that serves it. A name two languages both claim is refused: one
// name must mean one tool, and silently preferring a language would make the
// served surface depend on discovery order.
func (r *Runtime) adopt(h *host, reported []Definition) error {
	for _, definition := range reported {
		if previous, duplicate := r.routes[definition.Name]; duplicate {
			return inspectFailure(h.language,
				"the tool name %q is declared by both the %s and %s language hosts",
				definition.Name, previous.language, h.language)
		}
		r.routes[definition.Name] = h
		r.definitions = append(r.definitions, definition)
	}
	return nil
}

// validateCatalog holds one host's reported catalog to the same contract the
// managed boundary publishes: a portable name, a bounded description, and two
// bounded JSON Schemas that describe objects. It also checks the catalog
// against what discovery found, so a host can neither hide an authored tool
// nor invent one.
func validateCatalog(language string, expected map[string]bool, reported []Definition) error {
	if len(reported) != len(expected) {
		return inspectFailure(language,
			"the %s language host reported %d tools; discovery found %d under tools/",
			language, len(reported), len(expected))
	}
	seen := map[string]bool{}
	for _, definition := range reported {
		if !toolNamePattern.MatchString(definition.Name) {
			return inspectFailure(language,
				"the %s language host reported a tool name outside the portable grammar", language)
		}
		if !expected[definition.Name] {
			return inspectFailure(language,
				"the %s language host reported the tool %q, which discovery did not find under tools/",
				language, definition.Name)
		}
		if seen[definition.Name] {
			return inspectFailure(language,
				"the %s language host reported the tool %q twice", language, definition.Name)
		}
		seen[definition.Name] = true
		if definition.Description == "" || len(definition.Description) > MaxDescriptionBytes ||
			!utf8.ValidString(definition.Description) {
			return inspectFailure(language,
				"the tool %q must report a non-empty UTF-8 description of at most %d bytes",
				definition.Name, MaxDescriptionBytes)
		}
		for surface, schema := range map[string]json.RawMessage{
			"input":  definition.InputSchema,
			"output": definition.OutputSchema,
		} {
			if err := validateSchema(schema); err != nil {
				return inspectFailure(language, "the tool %q %s schema %s", definition.Name, surface, err)
			}
		}
	}
	return nil
}

// validateSchema bounds one reported JSON Schema and requires it to describe
// an object: a tool's arguments and result are named fields, so a harness can
// render them.
func validateSchema(schema json.RawMessage) error {
	if len(schema) == 0 {
		return errors.New("is missing")
	}
	if len(schema) > MaxSchemaBytes {
		return fmt.Errorf("may contain at most %d bytes", MaxSchemaBytes)
	}
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return errors.New("must be one JSON object")
	}
	if decoded["type"] != "object" {
		return errors.New(`must carry the top-level "type": "object"`)
	}
	return nil
}

// Definitions returns the merged catalog, sorted by name. The managed boundary
// publishes it verbatim.
func (r *Runtime) Definitions() []Definition {
	out := make([]Definition, len(r.definitions))
	copy(out, r.definitions)
	return out
}

// Call runs one authored tool. The deadline is tenon's: an overrun terminates
// the language host rather than leaving an uninterruptible call running behind
// the boundary.
func (r *Runtime) Call(name string, arguments json.RawMessage, deadline time.Duration) (json.RawMessage, error) {
	h, known := r.routes[name]
	if !known {
		return nil, fmt.Errorf("no authored tool named %q is served", name)
	}
	return h.invoke(name, arguments, deadline)
}

// Close ends every host session. Hosts stay alive for the whole server
// session, so this runs once, when the session ends.
func (r *Runtime) Close() {
	for _, h := range r.hosts {
		h.close()
	}
}

// hostCommand builds one language host's launch command. Serving never
// depends on PATH: the executables were resolved once at preparation and
// recorded absolutely.
func hostCommand(cfg Config, dir, language string, executables map[string]string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	switch language {
	case TypeScript:
		cmd = exec.Command(executables["deno"], "run", "--quiet", "--cached-only", "--frozen",
			"--config", filepath.Join(cfg.Source, "deno.json"),
			"--allow-read="+cfg.Source+","+cfg.Workspace,
			filepath.Join(dir, "typescript.ts"), cfg.Source)
		cmd.Env = hostEnv("DENO_DIR=" + filepath.Join(dir, "deno-dir"))
	case Python:
		cmd = exec.Command(executables["uv"], "run", "--locked", "--no-sync", "--project", cfg.Source,
			"python", filepath.Join(dir, "python.py"), cfg.Source)
		cmd.Env = hostEnv(
			"UV_PROJECT_ENVIRONMENT="+filepath.Join(dir, "python-venv"),
			"PYTHONDONTWRITEBYTECODE=1")
	case Go:
		cmd = exec.Command(filepath.Join(dir, "go", "host"))
		cmd.Env = hostEnv()
	default:
		return nil, inspectFailure(language, "%q is not an authored tool language", language)
	}
	// Tools run against the workspace, not against the agent source: source
	// is where tools are discovered, the workspace is where work happens.
	cmd.Dir = cfg.Workspace
	return cmd, nil
}

// verifyCache proves the prepared cache is exactly this tenon's hosts for this
// source. The directory name already carries the source fingerprint and the
// host digest; this checks that the files inside are the ones tenon wrote.
func verifyCache(cfg Config, dir string) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return staleCache
	}
	for _, language := range cfg.languages() {
		switch language {
		case TypeScript:
			if !sameFile(filepath.Join(dir, "typescript.ts"), typescriptHost) {
				return staleCache
			}
		case Python:
			if !sameFile(filepath.Join(dir, "python.py"), pythonHost) {
				return staleCache
			}
		case Go:
			main, _, err := renderGoHost(cfg, filepath.Join(dir, "go"))
			if err != nil {
				return staleCache
			}
			// The rendered main.go is the embedded host for this project;
			// go.mod is left to the toolchain, which rewrites it canonically
			// during tidy.
			if !sameFile(filepath.Join(dir, "go", "main.go"), main) {
				return staleCache
			}
			if _, err := os.Stat(filepath.Join(dir, "go", "go.mod")); err != nil {
				return staleCache
			}
			info, err := os.Stat(filepath.Join(dir, "go", "host"))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return staleCache
			}
		}
	}
	return nil
}

func sameFile(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return string(got) == string(want)
}
