package toolruntime

// Preparation runs once per apply, before anything in the workspace is
// mutated, and again — against a throwaway cache — for every validate, so both
// commands report the same failures. It writes tenon's own hosts into the
// cache, drives each language's native toolchain against the author's own
// lockfiles, records the absolute executables serving will need, and then
// inspects the result by starting each host once and reading its catalog.
//
// Subprocesses run with a minimal environment allowlist. A toolchain that
// inherited the operator's whole environment would make apply depend on
// ambient state tenon neither validated nor recorded.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// hostModule is the generated Go host's module path. It is never published
// and never fetched: it exists only to give the rendered host a module of its
// own so the authored agent module keeps its go.mod and go.sum untouched.
const hostModule = "tenon.local/toolhost"

// goModulePattern and goVersionPattern read the authored module path and go
// directive out of the agent's own go.mod. The generated host module requires
// and replaces that module, so its own build never touches authored files.
var (
	goModulePattern  = regexp.MustCompile(`(?m)^\s*module\s+(?:"([^"]+)"|(\S+))`)
	goVersionPattern = regexp.MustCompile(`(?m)^\s*go\s+(\d+\.\d+(?:\.\d+)?)`)
)

// environmentAllowlist is every inherited variable a language toolchain may
// see. Anything else is dropped.
var environmentAllowlist = []string{
	"PATH", "HOME", "TMPDIR", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR", "GOROOT", "GOPATH",
}

// Prepare materializes and inspects the tool runtime for cfg. It is safe to
// run repeatedly: the cache is keyed by source fingerprint and host
// implementation, and every step overwrites its own output. ctx bounds the
// whole preparation, including every toolchain subprocess.
func Prepare(ctx context.Context, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	dir := cfg.CacheDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return prepareFailure("", "the tool cache directory could not be created")
	}

	executables := map[string]string{}
	for _, language := range cfg.languages() {
		if err := prepareLanguage(ctx, cfg, dir, language, executables); err != nil {
			return err
		}
	}
	if err := writeExecutables(dir, executables); err != nil {
		return err
	}

	// Inspection is the proof that preparation produced a real tool surface:
	// every host starts, reports a catalog tenon can validate, and stops.
	rt, err := Open(cfg)
	if err != nil {
		return err
	}
	rt.Close()
	return nil
}

func prepareLanguage(ctx context.Context, cfg Config, dir, language string, executables map[string]string) error {
	switch language {
	case TypeScript:
		hostPath := filepath.Join(dir, "typescript.ts")
		if err := writeCacheFile(hostPath, typescriptHost, 0o600); err != nil {
			return err
		}
		deno, err := resolveExecutable(language, "deno")
		if err != nil {
			return err
		}
		executables["deno"] = deno
		// Type-checking the host together with every authored tool proves
		// the tools compile and populates the cache the frozen, cache-only
		// serving run depends on.
		args := []string{"check", "--config", filepath.Join(cfg.Source, "deno.json"), "--frozen", hostPath}
		for _, tool := range cfg.Tools {
			if tool.Language == TypeScript {
				args = append(args, filepath.Join(cfg.Source, filepath.FromSlash(tool.SourcePath)))
			}
		}
		return run(ctx, language, "deno check", exec.CommandContext(ctx, deno, args...),
			cfg.Source, hostEnv("DENO_DIR="+filepath.Join(dir, "deno-dir")))

	case Python:
		if err := writeCacheFile(filepath.Join(dir, "python.py"), pythonHost, 0o600); err != nil {
			return err
		}
		uv, err := resolveExecutable(language, "uv")
		if err != nil {
			return err
		}
		executables["uv"] = uv
		return run(ctx, language, "uv sync",
			exec.CommandContext(ctx, uv, "sync", "--locked", "--project", cfg.Source),
			cfg.Source, hostEnv(
				"UV_PROJECT_ENVIRONMENT="+filepath.Join(dir, "python-venv"),
				"PYTHONDONTWRITEBYTECODE=1"))

	case Go:
		goDir := filepath.Join(dir, "go")
		if err := os.MkdirAll(goDir, 0o700); err != nil {
			return prepareFailure(language, "the go host directory could not be created")
		}
		main, mod, err := renderGoHost(cfg)
		if err != nil {
			return err
		}
		if err := writeCacheFile(filepath.Join(goDir, "main.go"), main, 0o600); err != nil {
			return err
		}
		if err := writeCacheFile(filepath.Join(goDir, "go.mod"), mod, 0o600); err != nil {
			return err
		}
		toolchain, err := resolveExecutable(language, "go")
		if err != nil {
			return err
		}
		env := hostEnv("GOTOOLCHAIN=local")
		if err := run(ctx, language, "go mod tidy",
			exec.CommandContext(ctx, toolchain, "mod", "tidy"), goDir, env); err != nil {
			return err
		}
		return run(ctx, language, "go build", exec.CommandContext(ctx, toolchain, "build",
			"-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=",
			"-o", filepath.Join(goDir, "host"), "."), goDir, env)
	}
	return prepareFailure(language, "%q is not an authored tool language", language)
}

// goHostData is the rendered Go host's template input.
type goHostData struct {
	HostModule string
	Module     string
	GoVersion  string
	SourceDir  string
	Imports    []goHostImport
	Tools      []goHostImport
}

type goHostImport struct {
	Alias string
	Path  string
	Name  string
}

// renderGoHost renders the generated host's main.go and go.mod for cfg. The
// authored module supplies its own path and go directive; tenon adds no
// dependency to it and writes nothing into it.
func renderGoHost(cfg Config) ([]byte, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(cfg.Source, "go.mod"))
	if err != nil {
		return nil, nil, prepareFailure(Go, "go tools require a readable go.mod at the agent root")
	}
	moduleMatch := goModulePattern.FindSubmatch(raw)
	if moduleMatch == nil {
		return nil, nil, prepareFailure(Go, "go tools require a module path in the agent root go.mod")
	}
	module := string(moduleMatch[1])
	if module == "" {
		module = string(moduleMatch[2])
	}
	version := "1.21"
	if versionMatch := goVersionPattern.FindSubmatch(raw); versionMatch != nil {
		version = string(versionMatch[1])
	}

	data := goHostData{
		HostModule: hostModule,
		Module:     module,
		GoVersion:  version,
		SourceDir:  cfg.Source,
	}
	for i, tool := range cfg.goToolDirs() {
		entry := goHostImport{
			Alias: fmt.Sprintf("tool%d", i),
			Path:  module + "/tools/" + tool[0],
			Name:  tool[1],
		}
		data.Imports = append(data.Imports, entry)
		data.Tools = append(data.Tools, entry)
	}

	main, err := renderTemplate("main.go", goHostMain, data)
	if err != nil {
		return nil, nil, err
	}
	mod, err := renderTemplate("go.mod", goHostMod, data)
	if err != nil {
		return nil, nil, err
	}
	return main, mod, nil
}

func renderTemplate(name, text string, data goHostData) ([]byte, error) {
	parsed, err := template.New(name).Parse(text)
	if err != nil {
		return nil, prepareFailure(Go, "the go host %s template is invalid", name)
	}
	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, data); err != nil {
		return nil, prepareFailure(Go, "the go host %s could not be rendered", name)
	}
	return rendered.Bytes(), nil
}

// run executes one toolchain step. Its stdout and stderr are captured and
// discarded: a tenon diagnostic never carries raw process output, so the
// message names the step and points the author at running the toolchain
// directly.
func run(ctx context.Context, language, step string, cmd *exec.Cmd, dir string, env []string) error {
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = nil
	var sink ring
	cmd.Stdout = &sink
	cmd.Stderr = &sink
	err := cmd.Run()
	if ctx.Err() != nil {
		return prepareFailure(language, "preparing %s tools exceeded the time available", language)
	}
	if err != nil {
		return prepareFailure(language,
			"preparing %s tools failed at %q; run it against the agent source to see the toolchain's own output",
			language, step)
	}
	return nil
}

// resolveExecutable finds one toolchain executable on PATH once, at
// preparation, and returns its absolute path.
func resolveExecutable(language, name string) (string, error) {
	found, err := exec.LookPath(name)
	if err != nil {
		return "", prepareFailure(language, "%s tools require %s on PATH; none was found", language, name)
	}
	absolute, err := filepath.Abs(found)
	if err != nil {
		return "", prepareFailure(language, "the %s executable path could not be resolved", name)
	}
	return absolute, nil
}

func writeCacheFile(path string, content []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, content, mode); err != nil {
		return prepareFailure("", "the tool cache file %s could not be written", filepath.Base(path))
	}
	return nil
}

// executables is the recorded absolute path of every toolchain executable
// serving needs. Serving never consults PATH: the operator's PATH at apply
// time is what was validated, and a harness may start the server with another.
type executables struct {
	Deno string `json:"deno,omitempty"`
	UV   string `json:"uv,omitempty"`
}

func executablesPath(dir string) string { return filepath.Join(dir, "executables.json") }

func writeExecutables(dir string, resolved map[string]string) error {
	content, err := json.MarshalIndent(executables{Deno: resolved["deno"], UV: resolved["uv"]}, "", "  ")
	if err != nil {
		return prepareFailure("", "the resolved tool executables could not be encoded")
	}
	return writeCacheFile(executablesPath(dir), append(content, '\n'), 0o600)
}

// readExecutables reads the recorded executables strictly: an unknown field, a
// relative path, or an entry that is not a regular executable file fails
// closed the same way a missing cache does.
func readExecutables(dir string, languages []string) (map[string]string, error) {
	needed := map[string]bool{}
	for _, language := range languages {
		switch language {
		case TypeScript:
			needed["deno"] = true
		case Python:
			needed["uv"] = true
		}
	}
	if len(needed) == 0 {
		return map[string]string{}, nil
	}

	raw, err := os.ReadFile(executablesPath(dir))
	if err != nil {
		return nil, staleCache
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var recorded executables
	if err := decoder.Decode(&recorded); err != nil {
		return nil, staleCache
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, staleCache
	}

	resolved := map[string]string{"deno": recorded.Deno, "uv": recorded.UV}
	for name := range needed {
		path := resolved[name]
		if !filepath.IsAbs(path) {
			return nil, staleCache
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return nil, staleCache
		}
	}
	return resolved, nil
}

// hostEnv builds a subprocess environment from the allowlist plus the caller's
// explicit additions, sorted so identical preparation is identical.
func hostEnv(extra ...string) []string {
	var env []string
	for _, key := range environmentAllowlist {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "LC_") {
			env = append(env, entry)
		}
	}
	sort.Strings(env)
	return append(env, extra...)
}
