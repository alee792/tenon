package toolruntime

// Preparation runs once per apply, before anything in the workspace is
// mutated, and again — against a throwaway cache — for every validate, so both
// commands report the same failures. It writes tenon's own hosts into the
// cache, drives each language's native toolchain against the author's own
// lockfiles, and then inspects the result by starting each host once and
// reading its catalog.
//
// Subprocesses run with a minimal environment allowlist. A toolchain that
// inherited the operator's whole environment would make apply depend on
// ambient state tenon neither validated nor recorded.

import (
	"bytes"
	"context"
	"fmt"
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
// Deno, which reads its CA from that variable rather than the SSL_CERT_*
// pair. The proxy entries are the other half of "a proxied environment":
// the CA lets a toolchain trust the proxy's certificate, but without the
// proxy variables themselves a mandatory-proxy environment still can't
// reach the network at all — uv (python install's own fetch, deno's module
// resolution) and go all honor the standard HTTP_PROXY/HTTPS_PROXY/NO_PROXY
// trio, and both the upper- and lower-case forms are read in practice
// (curl-style tooling conventionally prefers lower-case for all but
// HTTPS_PROXY, to avoid the legacy CGI HTTP_PROXY footgun), so both are
// allowed through.
var environmentAllowlist = []string{
	"PATH", "HOME", "TMPDIR", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR", "DENO_CERT", "GOROOT", "GOPATH",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
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

	for _, language := range cfg.languages() {
		if err := prepareLanguage(ctx, cfg, dir, language); err != nil {
			return err
		}
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

// PruneDenoDirClosureCache discards a prepared TypeScript closure's
// derived, path-keyed DENO_DIR caches (see pruneDenoCache), leaving only
// the actually downloaded package cache a `--cached-only` run needs. It is
// exported for staging (internal/stage), not called by Prepare itself: a
// local `tenon apply`'s persistent workspace cache is never scanned for a
// leaked build-machine path (that scan is staging's own, ADR 0021) and
// verifyCache never checks DENO_DIR's pruning state, so nothing about local
// correctness needs it pruned — only staging does, for the same reason
// Python's closure needs rewritePythonSysconfigData before it is carried
// into a staged tree. Pruning DENO_DIR unconditionally inside Prepare once
// discarded a local apply's warm cache on every single run, forcing an
// otherwise avoidable TypeScript recompilation at the next real launch.
// Callers must run this after the source Prepare call has returned (its own
// inspection launch is itself a `deno run` that repopulates whatever
// derived caches it needs, keyed to preparation's own paths — pruning
// before that launch runs leaves it undone, the discovery that first
// surfaced this ordering requirement).
func PruneDenoDirClosureCache(closureDir string) error {
	return pruneDenoCache(filepath.Join(closureDir, "deno-dir"))
}

func prepareLanguage(ctx context.Context, cfg Config, dir, language string) error {
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
		denoDir := filepath.Join(dir, "deno-dir")
		// Type-checking the host together with every authored tool proves
		// the tools compile and populates the cache the frozen, cache-only
		// serving run depends on.
		args := []string{"check", "--config", filepath.Join(cfg.Source, "deno.json"), "--frozen", hostPath}
		for _, tool := range cfg.Tools {
			if tool.Language == TypeScript {
				args = append(args, filepath.Join(cfg.Source, filepath.FromSlash(tool.SourcePath)))
			}
		}
		if err := run(ctx, language, "deno check", exec.CommandContext(ctx, deno, args...),
			cfg.Source, hostEnv("DENO_DIR="+denoDir)); err != nil {
			return err
		}
		// Linking deno into the closure now, ahead of Prepare's inspection
		// launch below, is what lets hostCommand exec it. DENO_DIR is left
		// unpruned here: only staging needs it pruned, and only after
		// Prepare's own inspection launch has run (see
		// PruneDenoDirClosureCache). The shared deno runtime cache
		// (shareddeno.go) means this copies the executable at most once per
		// machine, not once per agent.
		_, sharedDeno, err := ensureSharedDeno(ctx, deno)
		if err != nil {
			return err
		}
		return hardlinkFile(sharedDeno, filepath.Join(dir, "deno", "bin", "deno"))

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

// maxClosureExecutableBytes bounds a toolchain executable copied whole into
// the closure (today, only deno). Large enough for a real toolchain binary,
// small enough that a surprising file fails closed rather than exhausts
// staging disk space.
const maxClosureExecutableBytes = 256 << 20

// denoCacheEntryPrefixes are DENO_DIR entries `deno check`/`deno run` derive
// from the source files' own absolute paths at preparation time: type-check
// and code-generation caches keyed by preparation-time specifiers, which
// relocation invalidates regardless (ADR 0021: preparation may write
// machine-local paths, but publication may not carry them). None of them
// are required to launch: deno regenerates them locally, with no network,
// the first time a `--cached-only` run needs them again. Matched by prefix
// rather than a fixed Deno-release file list: Deno 2.9's on-disk cache
// format (flat "*_v2" files, some with SQLite "-shm"/"-wal" siblings) is
// already a different shape than an older Deno release produced (the
// directory-and-registry.json-per-module shape the prototype this ports
// from was built against), and a future release changing the shape again
// should still be caught by name rather than silently kept.
var denoCacheEntryPrefixes = []string{
	"check_cache", "dep_analysis_cache", "fast_check_cache", "node_analysis_cache", "v8_code_cache",
	"gen", "registry.json",
}

// pruneDenoCache discards derived, path-keyed entries from a prepared
// DENO_DIR, keeping only the actually downloaded package cache ("npm/", and
// any future remote-module cache) a `--cached-only` run needs.
// node_compat_bin, created lazily only for `node:` specifier imports,
// carries a symlink back to the build-time deno executable; os.RemoveAll
// discards the directory entry without following it, so no separate
// symlink-tolerant removal is needed. The final walk is defense in depth,
// the same proof normalizePythonClosure applies to the Python closure:
// copyTree refuses a symlink at staging time regardless, but this fails
// closed here with a diagnostic that names the exact survivor.
func pruneDenoCache(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return prepareFailure(TypeScript, "the prepared deno cache is missing")
	}
	for _, entry := range entries {
		name := entry.Name()
		prune := name == "node_compat_bin"
		for _, prefix := range denoCacheEntryPrefixes {
			if strings.HasPrefix(name, prefix) {
				prune = true
				break
			}
		}
		if !prune {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return prepareFailure(TypeScript, "the deno build cache could not be discarded")
		}
	}
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return prepareFailure(TypeScript, "the deno cache could not be inspected after pruning")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return prepareFailure(TypeScript, "a symlink survived deno cache pruning at %q", filepath.Base(path))
		}
		return nil
	})
}

// prepareClosurePython links a pinned standalone CPython (installed at
// most once per machine, per resolved identity, by
// ensureSharedPythonInterpreter — see sharedpython.go) into dir/cpython,
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
	// The exact patch isn't chosen here: it's whatever this pinned uv
	// release resolves the requested minor version to, the same fetch
	// digest-pinned by uv itself, so two prepares against the same uv
	// version agree — identity names the exact patch and platform the
	// closure actually carries.
	identity, err := ensureSharedPythonInterpreter(ctx, uv, spec)
	if err != nil {
		return err
	}
	sharedRoot, err := sharedRuntimeRoot("python")
	if err != nil {
		return err
	}
	sharedIdentityDir := filepath.Join(sharedRoot, identity)
	perAgentIdentityDir := filepath.Join(dir, "cpython", identity)
	// Every file except _sysconfigdata_*.py is a plain hardlink: the shared
	// store was already normalized once, so the per-agent closure inherits
	// "already symlink-free" for free. sysconfigdata bakes in its own
	// install directory (see RewritePythonSysconfigData's doc), so it must
	// carry per-agent-rewritten content, never a link to the shared store's
	// own path — hardlinkTree skips it here, and it's populated by the two
	// calls immediately below instead.
	if err := hardlinkTree(sharedIdentityDir, perAgentIdentityDir, isSysconfigdataFile); err != nil {
		return prepareFailure(Python, "the python interpreter could not be linked into the closure: %v", err)
	}
	if err := copySysconfigdataFiles(sharedIdentityDir, perAgentIdentityDir); err != nil {
		return prepareFailure(Python, "the python interpreter's sysconfig data could not be copied: %v", err)
	}
	if err := RewritePythonSysconfigData(perAgentIdentityDir, sharedIdentityDir, perAgentIdentityDir); err != nil {
		return prepareFailure(Python, "the python interpreter's sysconfig data could not be localized: %v", err)
	}

	env := hostEnv("PYTHONDONTWRITEBYTECODE=1")
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
	binName, err := pythonBinaryName(identity)
	if err != nil {
		return prepareFailure(Python, "%v", err)
	}
	interpBin := filepath.Join(perAgentIdentityDir, "bin", binName)
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

	return normalizeSiteClosure(siteDir)
}

// ResolvePythonVersionSpec resolves the Python version specification a
// project's own pin names — a `.python-version` file's exact pin, or the
// floor of pyproject.toml's `requires-python` range — without installing or
// fetching anything. It is exported so the agent manifest's tool-runtime
// resolution (cmd/tenon) can pin what preparation will install without
// duplicating this parsing: manifest resolution runs before tool
// preparation (ADR: a supplied manifest is verified before any workspace
// mutation), so it cannot yet read the exact patch and ABI
// `uv python install` will actually resolve to — see prepareClosurePython
// and pythonClosureLayout for that identity, which is what the staged
// artifact manifest carries once preparation has actually run.
func ResolvePythonVersionSpec(source string) (string, error) {
	return pythonVersionSpec(source)
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

// removePythonClosureSymlinks deletes every symlink under root, whatever its
// name or position, PROVIDED its target resolves inside one of
// closureRoots: none of CPython's own internal symlinks known today (
// bin/python, bin/python3, python3-config, pydoc3, 2to3, idle3, the
// lib/libpython*.so link, the pkgconfig links, uv 0.12.6's own
// minor-version directory link) are referenced anywhere on tenon's launch
// path, so none needs to be materialized in its place — but a symlink this
// closure did not expect, pointing somewhere outside itself, is exactly the
// kind of thing a future, further uv layout change could introduce and
// that blind deletion should not paper over: it fails closed instead,
// naming the offending path and its target, rather than guess whether
// deleting it is safe.
func removePythonClosureSymlinks(root string, closureRoots []string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, rerr := os.Readlink(path)
		if rerr != nil {
			return fmt.Errorf("reading the symlink %s: %w", path, rerr)
		}
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(path), resolved)
		}
		resolved = filepath.Clean(resolved)
		inside := false
		for _, closureRoot := range closureRoots {
			if resolved == closureRoot || strings.HasPrefix(resolved, closureRoot+string(filepath.Separator)) {
				inside = true
				break
			}
		}
		if !inside {
			return fmt.Errorf("the symlink %s targets %q, outside the python closure; refusing to guess whether removing it is safe",
				path, target)
		}
		return os.Remove(path)
	})
}

// assertPythonClosureHasNoSymlinks fails if any symlink survives under root.
func assertPythonClosureHasNoSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("a symlink survived normalization at %s", path)
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
	// os.WriteFile only applies mode when it creates the file; a re-run
	// overwriting a path some earlier partial or tampered run left at a
	// different mode (the executable bit on a copied closure runtime,
	// concretely) must still land at exactly mode, not silently inherit
	// whatever was already there.
	if err := os.Chmod(path, mode); err != nil {
		return prepareFailure("", "the tool cache file %s could not be secured", filepath.Base(path))
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
