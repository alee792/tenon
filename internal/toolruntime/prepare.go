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

// pythonRequiresPattern and pythonVersionNumberPattern read the minor Python
// version out of the agent's own pyproject.toml, when no .python-version
// file pins one more precisely. pythonInterpreterDirPattern recognizes the
// directory name `uv python install` creates for one installed interpreter
// (for example "cpython-3.11.13-linux-x86_64-gnu"), from which the exact
// patch and platform the closure actually carries is read back rather than
// assumed.
var (
	pythonRequiresPattern       = regexp.MustCompile(`(?m)^\s*requires-python\s*=\s*"([^"]*)"`)
	pythonVersionNumberPattern  = regexp.MustCompile(`\d+\.\d+`)
	pythonInterpreterDirPattern = regexp.MustCompile(`^cpython-(\d+\.\d+\.\d+)-(.+)$`)
)

// environmentAllowlist is every inherited variable a language toolchain may
// see. Anything else is dropped. The CA entries let a toolchain find the
// operator's certificate authority in a proxied or custom-CA environment:
// SSL_CERT_FILE/SSL_CERT_DIR for the OpenSSL-based tools, and DENO_CERT for
// Deno, which reads its CA from that variable rather than the SSL_CERT_* pair.
var environmentAllowlist = []string{
	"PATH", "HOME", "TMPDIR", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR", "DENO_CERT", "GOROOT", "GOPATH",
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
		return prepareClosurePython(ctx, cfg, dir, uv)

	case Go:
		goDir := filepath.Join(dir, "go")
		if err := os.MkdirAll(goDir, 0o700); err != nil {
			return prepareFailure(language, "the go host directory could not be created")
		}
		// Build against a copy of the agent's whole module at a directory
		// name that is fixed regardless of where the agent source
		// physically lives: the generated go.mod names its replace target
		// relative to goDir, and a fixed sibling name makes that target a
		// machine-independent constant ("../agent-source") rather than a
		// path shaped by this machine's directory layout. This matters at
		// build time, not just after: `go build -trimpath` does not scrub
		// a module's replace target the way it scrubs recorded source-file
		// paths, so the built host binary's own embedded module info (read
		// by `go version -m` and runtime/debug.ReadBuildInfo) carries
		// whatever the compiler saw, verbatim.
		//
		// The copy is the whole module, not only tools/: a Go tool may
		// import any package inside its own module
		// (docs/product-spec.md constrains only tools/'s own shape, not
		// what a tool imports), so building against anything narrower
		// silently breaks an import that compiles fine against the real
		// agent source.
		sourceCopy := filepath.Join(dir, "agent-source")
		if err := copyGoModuleSource(cfg, sourceCopy); err != nil {
			return err
		}
		main, mod, err := renderGoHost(cfg, goDir, sourceCopy)
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

// prepareClosurePython installs a pinned standalone CPython into dir/cpython
// and lays the project's own `uv export --locked` dependencies flat into
// dir/site (ADR 0021). No venv is created — no pyvenv.cfg, no activation
// scripts, no interpreter symlink — so launch (hostCommand) execs the
// installed interpreter directly with the dependency directory added as a
// site directory, and uv never runs at serve time.
func prepareClosurePython(ctx context.Context, cfg Config, dir, uv string) error {
	spec, err := pythonVersionSpec(cfg.Source)
	if err != nil {
		return err
	}
	cpythonRoot := filepath.Join(dir, "cpython")
	if err := os.MkdirAll(cpythonRoot, 0o700); err != nil {
		return prepareFailure(Python, "the python interpreter directory could not be created")
	}
	env := hostEnv("PYTHONDONTWRITEBYTECODE=1")
	// The exact patch is not chosen here: it is whatever this pinned uv
	// release resolves the requested minor version to, the same fetch
	// digest-pinned by uv itself, so two prepares against the same uv
	// version agree. The installed directory name, read back below, names
	// the exact patch and platform the closure actually carries.
	if err := run(ctx, Python, "uv python install", exec.CommandContext(ctx, uv,
		"python", "install", "--no-bin", "--install-dir", cpythonRoot, spec), dir, env); err != nil {
		return err
	}

	requirements := filepath.Join(dir, "requirements.txt")
	if err := run(ctx, Python, "uv export", exec.CommandContext(ctx, uv,
		"export", "--locked", "--no-dev", "--no-emit-project", "--format", "requirements.txt",
		"--project", cfg.Source, "-o", requirements), dir, env); err != nil {
		return err
	}

	siteDir := filepath.Join(dir, "site")
	if err := os.MkdirAll(siteDir, 0o700); err != nil {
		return prepareFailure(Python, "the python dependency directory could not be created")
	}
	interpBin, _, _, err := pythonClosureLayout(dir)
	if err != nil {
		return prepareFailure(Python, "the installed python interpreter could not be found after `uv python install`")
	}
	if err := run(ctx, Python, "uv pip install", exec.CommandContext(ctx, uv,
		"pip", "install", "--python", interpBin, "--target", siteDir,
		"--require-hashes", "--no-deps", "-r", requirements), dir, env); err != nil {
		return err
	}

	// requirements.txt is a preparation intermediate: uv annotates it with the
	// exact command line that produced it, including this machine's own
	// throwaway preparation paths, so it never survives into the closure.
	if err := os.Remove(requirements); err != nil {
		return prepareFailure(Python, "the intermediate requirements file could not be removed")
	}

	return normalizePythonClosure(filepath.Dir(filepath.Dir(interpBin)), siteDir)
}

// pythonVersionSpec resolves the Python version to install: a `.python-version`
// file names it exactly; otherwise the minor version is read from
// pyproject.toml's `requires-python` constraint, and the exact patch is left
// to `uv python install`'s own deterministic resolution for the pinned uv
// release (see prepareClosurePython).
func pythonVersionSpec(source string) (string, error) {
	if raw, err := os.ReadFile(filepath.Join(source, ".python-version")); err == nil {
		if v := strings.TrimSpace(string(raw)); v != "" {
			return v, nil
		}
	}
	raw, err := os.ReadFile(filepath.Join(source, "pyproject.toml"))
	if err != nil {
		return "", prepareFailure(Python, "python tools require a readable pyproject.toml at the agent root")
	}
	match := pythonRequiresPattern.FindSubmatch(raw)
	if match == nil {
		return "", prepareFailure(Python,
			"python tools require a requires-python constraint in pyproject.toml, or a .python-version file")
	}
	version := pythonVersionNumberPattern.FindString(string(match[1]))
	if version == "" {
		return "", prepareFailure(Python, "the requires-python constraint %q names no version number", string(match[1]))
	}
	return version, nil
}

// pythonClosureLayout resolves the installed interpreter binary, the
// closure's dependency (site) directory, and the interpreter's own identity
// (for example "cpython-3.11.13-linux-x86_64-gnu") from an already prepared
// Python closure at dir. It reads dir/cpython back from disk rather than
// re-deriving anything from cfg, so it agrees with whatever
// prepareClosurePython actually installed, and is shared by preparation,
// launch (hostCommand), and cache verification (verifyCache).
func pythonClosureLayout(dir string) (interpBin, siteDir, identity string, err error) {
	cpythonRoot := filepath.Join(dir, "cpython")
	entries, rerr := os.ReadDir(cpythonRoot)
	if rerr != nil {
		return "", "", "", staleCache
	}
	var found string
	for _, e := range entries {
		if e.IsDir() && pythonInterpreterDirPattern.MatchString(e.Name()) {
			if found != "" {
				return "", "", "", staleCache
			}
			found = e.Name()
		}
	}
	if found == "" {
		return "", "", "", staleCache
	}
	match := pythonInterpreterDirPattern.FindStringSubmatch(found)
	parts := strings.SplitN(match[1], ".", 3)
	bin := filepath.Join(cpythonRoot, found, "bin", "python"+parts[0]+"."+parts[1])
	info, serr := os.Stat(bin)
	if serr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", "", staleCache
	}
	site := filepath.Join(dir, "site")
	if info, serr := os.Stat(site); serr != nil || !info.IsDir() {
		return "", "", "", staleCache
	}
	return bin, site, found, nil
}

// normalizePythonClosure removes everything the closure does not need to
// launch: the interpreter's own convenience symlinks (never referenced —
// launch execs the versioned interpreter binary directly, per
// pythonClosureLayout), the terminfo and man-page trees, and any
// console-script shims a dependency install placed under the site
// directory's own bin/. copyTree refuses a symlink outright, so the closure
// must already be symlink-free before it can ever be staged; local
// apply/serve normalizes exactly the same way, per ADR 0021's "one
// contract, not a staging mode".
func normalizePythonClosure(interpDir, siteDir string) error {
	for _, p := range []string{
		filepath.Join(interpDir, "share", "terminfo"),
		filepath.Join(interpDir, "share", "man"),
		filepath.Join(siteDir, "bin"),
	} {
		if err := os.RemoveAll(p); err != nil {
			return prepareFailure(Python, "the python closure could not be normalized: %v", err)
		}
	}
	for _, root := range []string{interpDir, siteDir} {
		if err := removePythonClosureSymlinks(root); err != nil {
			return prepareFailure(Python, "the python closure could not be normalized: %v", err)
		}
	}
	return nil
}

// removePythonClosureSymlinks deletes every symlink under root. None of
// CPython's own internal symlinks (bin/python, bin/python3, python3-config,
// pydoc3, 2to3, idle3, the lib/libpython*.so link, the pkgconfig links) are
// referenced anywhere on tenon's launch path, so none needs to be
// materialized in its place.
func removePythonClosureSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return os.Remove(path)
		}
		return nil
	})
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
// dependency to it and writes nothing into it. goDir is the directory the
// rendered go.mod will be written into and moduleDir is the directory that
// holds (or will hold) the replace target's own go.mod: the go.mod replace
// directive names moduleDir relative to goDir rather than as an absolute
// path. Callers pass a moduleDir whose own path is machine-independent (a
// fixed sibling name under the same cache directory as goDir, never
// cfg.Source directly) so the relative target renders as a constant like
// "../agent-source" — a real absolute path would still leak even relative to
// goDir, and `go build -trimpath` does not scrub a module's replace target
// the way it scrubs recorded source-file paths, so whatever the compiler
// sees here is exactly what survives in the built host binary's own
// embedded module info (read by `go version -m` and
// runtime/debug.ReadBuildInfo).
func renderGoHost(cfg Config, goDir, moduleDir string) ([]byte, []byte, error) {
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

	sourceDir := moduleDir
	if rel, relErr := filepath.Rel(goDir, moduleDir); relErr == nil {
		sourceDir = rel
	}

	data := goHostData{
		HostModule: hostModule,
		Module:     module,
		GoVersion:  version,
		SourceDir:  sourceDir,
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

// maxGoModuleCopyBytes bounds the whole-module copy copyGoModuleSource
// makes to build the Go host offline. It mirrors the tool-source
// inventory's own aggregate ceiling (ADR 0013, agentproject.
// MaxToolInventoryBytes): a Go tool may import any package within its own
// module, not only its own tools/ directory, so preparation copies the
// whole module rather than assume the shape of what it imports, and this
// bounds that wider copy the same hard way discovery already bounds the
// narrower tool inventory.
const maxGoModuleCopyBytes = 64 << 20

// copyGoModuleSource copies the agent's whole module — everything under
// cfg.Source except the workspace-state .tenon directory — into dest, a
// directory whose name never varies with the agent source's physical
// location (see prepareLanguage's Go case). A Go tool may import any
// package inside its own module (docs/product-spec.md constrains only
// tools/'s own shape, not what a tool imports), so building against
// anything narrower than the whole module silently breaks an import that
// compiles fine against the real agent source — copying only tools/ and
// the two dependency files once did exactly that. Symlinks are refused,
// matching every other copy in tenon, and the aggregate copy is bounded so
// an unexpectedly large agent source fails closed rather than stalls
// preparation.
func copyGoModuleSource(cfg Config, dest string) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return prepareFailure(Go, "the go build source directory could not be created")
	}
	var total int64
	return filepath.WalkDir(cfg.Source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(cfg.Source, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if rel == ".tenon" && d.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return prepareFailure(Go, "the go build source copy refuses a symlink at %q", filepath.ToSlash(rel))
		}
		if !d.Type().IsRegular() {
			return prepareFailure(Go, "the go build source copy refuses a non-regular entry at %q", filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > maxGoModuleCopyBytes {
			return prepareFailure(Go, "the agent module exceeds the %d byte bound the go build source copy allows", maxGoModuleCopyBytes)
		}
		return copyRegularFile(path, target)
	})
}

// copyRegularFile copies one regular file's bytes and mode, refusing a
// non-regular source (a symlink included): tool source was already proven
// symlink-free at discovery, and this copy stays that strict rather than
// trust the earlier proof silently.
func copyRegularFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, content, info.Mode().Perm())
}

// executables is the recorded absolute path of every toolchain executable
// serving needs. Serving never consults PATH: the operator's PATH at apply
// time is what was validated, and a harness may start the server with another.
// Python carries no entry: `uv` is a preparation-time tool only — the launch
// command execs the closure's own interpreter directly (ADR 0021) — so
// serving never stats it.
type executables struct {
	Deno string `json:"deno,omitempty"`
}

func executablesPath(dir string) string { return filepath.Join(dir, "executables.json") }

func writeExecutables(dir string, resolved map[string]string) error {
	content, err := json.MarshalIndent(executables{Deno: resolved["deno"]}, "", "  ")
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

	resolved := map[string]string{"deno": recorded.Deno}
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
