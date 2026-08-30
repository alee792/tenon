// Package stage prepares one complete, runnable agent filesystem tree at the
// canonical paths of ADR 0012 for a downstream OCI builder to copy onto a
// documented compatible base image. Staging re-prepares and re-generates the
// agent as if its immutable source lived at /opt/tenon/agents/<name> and its
// workspace at /workspace, so every embedded reference inside the tree uses
// those final runtime paths rather than the physical build directory.
//
// The tree is built under a temporary sibling of the requested output and
// published with a single rename, only after the artifact manifest is
// complete: a failure before that rename leaves no output directory behind.
// Preparation never mutates authored source, never reads a credential from the
// environment, and never contacts a model or a registry. The canonical staged
// paths are an exact published contract; the artifact manifest is tenon's own
// bookkeeping and is schema-versioned.
package stage

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/toolruntime"
	"github.com/alee792/tenon/internal/version"
)

// SchemaVersion is the artifact manifest schema. It is tenon's own
// bookkeeping, not a published author-facing schema, and rises when the
// manifest shape changes incompatibly.
const SchemaVersion = 1

// Canonical final runtime paths. These are the exact published contract of
// ADR 0012: downstream Dockerfiles COPY the tree by them, and every reference
// generated inside the tree embeds them, never the physical build directory.
const (
	finalOptRoot     = "/opt/tenon"
	finalArtifact    = "/opt/tenon/artifact.json"
	finalTenonBin    = "/opt/tenon/bin/tenon"
	finalEntrypoint  = "/opt/tenon/bin/agent-entrypoint"
	finalHarnessRoot = "/opt/tenon/harness"
	finalRuntimes    = "/opt/tenon/runtimes"
	finalAgentsRoot  = "/opt/tenon/agents"
	finalWorkspace   = "/workspace"
	finalHome        = "/home/tenon"
)

// Runtime identity of ADR 0012: the staged tree is designed to run as this
// non-root user, whose home is the writable /home/tenon. Ownership is recorded
// as intent in the manifest; staging never chowns (it runs unprivileged and
// the downstream builder applies ownership via COPY --chown).
const (
	rootUID    = 0
	rootGID    = 0
	runtimeUID = 65532
	runtimeGID = 65532
)

// Options configures one staging run.
type Options struct {
	// AgentDir is the authored agent source directory to stage.
	AgentDir string
	// Harness is the selected native harness, "claude" or "codex".
	Harness string
	// Output is the directory to publish. It must not already exist; the tree
	// is built in a temporary sibling and renamed into place last.
	Output string
	// Executable is the absolute, resolved tenon executable to copy into the
	// tree (resolveExecutable's result). Staging copies exactly this binary.
	Executable string
	// Driver is the native harness driver whose generated integration is
	// re-rendered for the final paths.
	Driver apply.Driver
}

// Result reports a completed staging run.
type Result struct {
	// Agent is the normalized agent name.
	Agent string
	// Output is the published directory.
	Output string
	// Fingerprint is the agent source fingerprint recorded in the manifest.
	Fingerprint string
	// RuntimeLanguages are the tool languages whose execution closure was
	// staged, empty for a tool-free agent.
	RuntimeLanguages []string
}

// Stage prepares and publishes the staged tree. Contract violations in the
// agent are reported on the returned diagnostics list and leave nothing
// behind; the error is reserved for environment failures. ctx bounds tool
// preparation.
func Stage(ctx context.Context, opts Options) (*Result, *diagnostics.List, error) {
	if opts.Executable == "" || !filepath.IsAbs(opts.Executable) {
		return nil, &diagnostics.List{}, fmt.Errorf("the tenon executable must be an absolute resolved path: %q", opts.Executable)
	}

	// Validate the agent completely before writing anything: a diagnostic
	// error must leave no output directory.
	p, diags, err := agentproject.Load(opts.AgentDir)
	if err != nil {
		return nil, diags, err
	}
	if p == nil || diags.HasErrors() {
		return nil, diags, nil
	}

	absOutput, err := filepath.Abs(opts.Output)
	if err != nil {
		return nil, diags, fmt.Errorf("resolving output: %w", err)
	}
	if _, err := os.Lstat(absOutput); err == nil {
		diags.Errorf("stage.output.exists", ".",
			"the output directory already exists and tenon refuses to overwrite it: %s", opts.Output)
		return nil, diags, nil
	} else if !os.IsNotExist(err) {
		return nil, diags, fmt.Errorf("inspecting output: %w", err)
	}

	// Build in a temporary sibling so publication is one rename. Any failure
	// before the rename removes the temp and leaves no output directory.
	parent := filepath.Dir(absOutput)
	tmp, err := os.MkdirTemp(parent, ".tenon-stage-"+p.Name+"-*")
	if err != nil {
		return nil, diags, fmt.Errorf("creating the staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(tmp)
		}
	}()

	// Prove preparation never mutates authored source: hash before and after.
	before, err := hashSource(p.Root)
	if err != nil {
		return nil, diags, fmt.Errorf("reading agent source: %w", err)
	}

	languages, closureDir, prepRoots, err := prepareClosure(ctx, p, diags)
	if err != nil {
		return nil, diags, err
	}
	if closureDir != "" {
		// prepareClosure carries the prepared closure out into its own
		// throwaway directory (closureDir's parent) so the later copyTree
		// into the staged tree can read it after the prepare-time cache
		// itself is gone. Once Stage is done with closureDir — on every
		// return path, success or failure — that carried copy is a second,
		// unpublished copy of the whole closure (tens to well over a
		// hundred megabytes with a Python interpreter and its dependencies)
		// with nothing left to clean it up; removing it only on the
		// copy-failure path inside prepareClosure left it on disk after
		// every successful stage.
		defer os.RemoveAll(filepath.Dir(closureDir))
	}
	if diags.HasErrors() {
		return nil, diags, nil
	}

	after, err := hashSource(p.Root)
	if err != nil {
		return nil, diags, fmt.Errorf("reading agent source: %w", err)
	}
	if before != after {
		return nil, diags, fmt.Errorf("preparation mutated authored source at %s; staging fails closed", p.Root)
	}

	finalAgentSource := finalAgentsRoot + "/" + p.Name

	// The final canonical directory the tool runtime closure will be staged
	// under, or "" for a tool-free agent. Computed here, before the tree
	// exists, so both the regenerated apply record (which names it) and the
	// later copy step (which populates it) agree on the identical path.
	var closureRootFinal string
	if closureDir != "" {
		closureRootFinal = finalRuntimes + "/tools"
	}

	// Directories that structure the tree. Ownership is recorded as intent;
	// /opt is root-owned and read-only at runtime, /workspace and /home/tenon
	// are owned by the non-root runtime identity.
	dirs := []DirEntry{
		{Path: finalOptRoot, Mode: octal(0o755), Owner: Owner{rootUID, rootGID}},
		{Path: finalOptRoot + "/bin", Mode: octal(0o755), Owner: Owner{rootUID, rootGID}},
		{Path: finalHarnessRoot, Mode: octal(0o755), Owner: Owner{rootUID, rootGID}},
		{Path: finalRuntimes, Mode: octal(0o755), Owner: Owner{rootUID, rootGID}},
		{Path: finalAgentsRoot, Mode: octal(0o755), Owner: Owner{rootUID, rootGID}},
		{Path: finalWorkspace, Mode: octal(0o755), Owner: Owner{runtimeUID, runtimeGID}},
		{Path: finalHome, Mode: octal(0o700), Owner: Owner{runtimeUID, runtimeGID}},
	}
	for _, d := range dirs {
		mode := fs.FileMode(0o755)
		if d.Path == finalHome {
			mode = 0o700
		}
		if err := os.MkdirAll(physical(tmp, d.Path), mode); err != nil {
			return nil, diags, fmt.Errorf("creating %s: %w", d.Path, err)
		}
		// MkdirAll honors the umask; force the recorded mode.
		if err := os.Chmod(physical(tmp, d.Path), mode); err != nil {
			return nil, diags, fmt.Errorf("securing %s: %w", d.Path, err)
		}
	}

	// Step: generate the native integration for the final paths, writing the
	// physical files under <tmp>/workspace while embedding /opt and /workspace.
	// renderProject carries every plugin-reference server re-anchored as
	// Vendored (see reAnchorReferencedServers) so the rendered configuration
	// points PLUGIN_ROOT and any plugin-relative command inside the staged
	// tree rather than at the operator's cache.
	renderProject := *p
	renderProject.PluginServers = reAnchorReferencedServers(p.PluginServers, p.PluginReferences)
	genErr := generateIntegration(&renderProject, opts.Driver, finalAgentSource, closureRootFinal, tmp, diags)
	if genErr != nil {
		return nil, diags, genErr
	}
	if diags.HasErrors() {
		return nil, diags, nil
	}

	// Step: copy the running tenon executable.
	if err := copyFile(opts.Executable, physical(tmp, finalTenonBin), 0o755); err != nil {
		return nil, diags, fmt.Errorf("copying the tenon executable: %w", err)
	}

	// Step: write the fail-closed entrypoint.
	if err := os.WriteFile(physical(tmp, finalEntrypoint), entrypointScript(opts.Harness), 0o755); err != nil {
		return nil, diags, fmt.Errorf("writing the entrypoint: %w", err)
	}
	if err := os.Chmod(physical(tmp, finalEntrypoint), 0o755); err != nil {
		return nil, diags, fmt.Errorf("securing the entrypoint: %w", err)
	}

	// Step: copy immutable agent source byte-for-byte (regular files and dirs
	// only; symlinks are rejected). This can fail mid-stage; the deferred
	// cleanup then removes the whole temporary tree.
	if err := copySource(p.Root, physical(tmp, finalAgentSource)); err != nil {
		return nil, diags, err
	}

	// Step: materialize every resolved plugin reference's cached tree into
	// the staged filesystem at plugins/<name>/ (issue #58): copySource above
	// carries the plugins/<name>.md reference file itself (harmless
	// provenance) but not the plugin content it resolves to, which lives in
	// the operator's plugin cache rather than the agent source tree. Each
	// reference is re-resolved against the cache here — immediately before
	// copying, not trusting the root Load captured earlier — so a cache
	// pruned or tampered with between Load and staging fails the stage
	// closed with the same diagnostic Load itself would raise, rather than
	// copying stale, missing, or corrupted bytes. The copy reuses copyTree:
	// regular files only, executable bits preserved, any symlink rejected
	// (defensive; the cache's own Fetch already refuses to store one). Each
	// materialized directory's final path is collected for the build-machine
	// path scan below, which must treat these arbitrary third-party bytes as
	// carried-in payload rather than tenon-rendered text.
	//
	// The re-verification and the copy do not run under one lock over the
	// cache — tenon holds no such lock — so a cache entry mutated in the
	// narrow window between them is not prevented. It is caught instead: the
	// staged agent source is re-loaded and fingerprint-checked below, before
	// anything is published, so a tree carrying bytes other than the ones
	// Load fingerprinted fails the stage closed rather than reaching an
	// output directory.
	//
	// A reference that already arrived materialized (its content sits beside
	// it in the authored tree, so Load resolved it from there rather than
	// from the cache) needs nothing here: copySource already carried those
	// bytes, and they stay ordinary authored source for the scan.
	var materializedPlugins []string
	for _, ref := range p.PluginReferences {
		if ref.Materialized {
			continue
		}
		root, err := agentproject.ResolvePluginReferenceRoot(ref)
		if err != nil {
			diags.Errorf("plugin.reference.unresolved", ref.SourcePath, "%s", diagnostics.Bound(err.Error(), 512))
			continue
		}
		final := finalAgentSource + "/plugins/" + ref.Name
		if err := copyTree(root, physical(tmp, final)); err != nil {
			return nil, diags, fmt.Errorf("staging the resolved plugin reference %q: %w", ref.Name, err)
		}
		materializedPlugins = append(materializedPlugins, strings.TrimPrefix(final, "/"))
	}
	if diags.HasErrors() {
		return nil, diags, nil
	}

	// Step: prove the staged agent source reproduces the fingerprint the
	// artifact manifest is about to record, by re-loading it exactly as the
	// container will (issue #58 review). This is the production-side proof of
	// the whole materialization: the staged tree is the first place a
	// materialized reference is ever loaded as one, and its components must
	// fingerprint identically to the cache-resolved load this stage was
	// planned from. It also closes the unlocked window above — a cache entry
	// mutated between its re-verification and the copy lands here as a
	// mismatch, before publication, rather than as a tree that verifies only
	// at container open.
	//
	// The re-load is deterministic whatever the process-global plugin cache
	// holds: every reference in the staged tree now has its content beside
	// it, and materialized content wins over the cache without consulting it
	// (agentproject's materialized-reference precedence), so a real CLI stage
	// with a configured cache and this same stage run offline read the same
	// bytes. The cost is one extra project load per stage, over a tree that
	// is already hot in the page cache.
	stagedSource := physical(tmp, finalAgentSource)
	restaged, restagedDiags, err := agentproject.Load(stagedSource)
	switch {
	case err != nil:
		diags.Errorf("stage.tree.fingerprint-mismatch", strings.TrimPrefix(finalAgentSource, "/"),
			"the staged agent source could not be re-loaded to prove it reproduces the recorded fingerprint: %s",
			diagnostics.Bound(err.Error(), 256))
		return nil, diags, nil
	case restaged == nil || restagedDiags.HasErrors():
		detail := "no diagnostics were reported"
		if all := restagedDiags.All(); len(all) > 0 {
			detail = diagnostics.Bound(all[0].String(), 256)
		}
		diags.Errorf("stage.tree.fingerprint-mismatch", strings.TrimPrefix(finalAgentSource, "/"),
			"the staged agent source no longer validates on its own: %s", detail)
		return nil, diags, nil
	case restaged.Fingerprint != p.Fingerprint:
		diags.Errorf("stage.tree.fingerprint-mismatch", strings.TrimPrefix(finalAgentSource, "/"),
			"the staged agent source fingerprints to %s, not the %s this stage was planned from; staging fails closed rather than publish a tree whose content is not the content that was loaded",
			restaged.Fingerprint, p.Fingerprint)
		return nil, diags, nil
	}
	// The plugin cache base is deliberately not added as a needle to the
	// build-machine-path scan below: a plugin reference's cache tree path is
	// keyed by its pinned rev, and that same rev legitimately appears as
	// plain text in the authored plugins/<name>.md reference file (the
	// "rev:" field) — a real value, not a leak. Every reference server was
	// re-anchored as Vendored above,
	// so PLUGIN_ROOT and any plugin-relative command already render under
	// the staged path rather than the cache's; the negative property that no
	// staged file embeds the cache *base* directory is proven directly by
	// the staging tests instead (issue #58), which is a check this
	// component/rev-shaped needle set cannot make safely.

	// Step: carry the tool execution closure into /opt/tenon/runtimes.
	runtimeInfo := RuntimeInfo{Bundled: false, Minimized: false}
	if closureDir != "" {
		dest := closureRootFinal + "/" + filepath.Base(closureDir)
		physDest := physical(tmp, dest)
		if err := copyTree(closureDir, physDest); err != nil {
			return nil, diags, fmt.Errorf("staging the tool runtime closure: %w", err)
		}
		runtimeInfo = RuntimeInfo{
			Bundled:     true,
			Languages:   languages,
			ClosurePath: dest,
			Minimized:   false,
			Note: "The prepared tool execution cache is staged whole: Go tools " +
				"stage a self-contained host binary and the go toolchain is NOT " +
				"copied. The closure is not further minimized in this slice.",
		}
		if identity, ok, err := pythonInterpreterIdentity(physDest); err != nil {
			return nil, diags, fmt.Errorf("recording the python interpreter identity: %w", err)
		} else if ok {
			runtimeInfo.Interpreters = map[string]string{toolruntime.Python: identity}
		}
	} else {
		runtimeInfo.Note = "the agent declares no authored tools, so no language runtime is staged"
	}

	// Step: build and write the artifact manifest last, over everything staged
	// so far, then publish with one rename.
	artifact := &Artifact{
		SchemaVersion:     SchemaVersion,
		TenonVersion:      version.Version,
		Harness:           HarnessInfo{Name: opts.Harness, Bundled: false, Note: harnessPlaceholderNote},
		Agent:             p.Name,
		SourceFingerprint: p.Fingerprint,
		Platform:          Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Layout: map[string]string{
			"opt":          finalOptRoot,
			"artifact":     finalArtifact,
			"tenon":        finalTenonBin,
			"entrypoint":   finalEntrypoint,
			"harness":      finalHarnessRoot,
			"runtimes":     finalRuntimes,
			"agent_source": finalAgentSource,
			"workspace":    finalWorkspace,
			"home":         finalHome,
		},
		Runtime: runtimeInfo,
		Dirs:    dirs,
	}
	if err := artifact.collectFiles(tmp); err != nil {
		return nil, diags, fmt.Errorf("recording staged files: %w", err)
	}
	artifact.sort()

	manifest, err := artifact.marshal()
	if err != nil {
		return nil, diags, err
	}
	if err := os.WriteFile(physical(tmp, finalArtifact), manifest, 0o644); err != nil {
		return nil, diags, fmt.Errorf("writing the artifact manifest: %w", err)
	}
	if err := os.Chmod(physical(tmp, finalArtifact), 0o644); err != nil {
		return nil, diags, fmt.Errorf("securing the artifact manifest: %w", err)
	}

	// Step: fail closed if any build-machine path survives anywhere in the
	// staged tree, the artifact manifest included (ADR 0021's normalize-
	// then-prove-relocatability step, widened from the prototype's
	// generated-configuration-only rejectBuildPaths scan to the whole
	// tree): the authored agent source directory and every throwaway root
	// staging or tool preparation created must leave no trace once the
	// tree is complete, whatever wrote it and however it was embedded — a
	// rewritten go.mod (ADR 0021's own named example) proved a
	// joined-absolute-string check insufficient on its own for text, so
	// text is checked component by component; a compiled binary's own
	// data is checked only for the fuller joined-path forms instead, since
	// bare-component matching against it produces false positives no
	// author-chosen path can dodge (see buildMachineJoinedNeedles).
	if err := rejectBuildMachinePaths(tmp, closureRootFinal, materializedPlugins,
		buildMachineNeedles(p.Root, tmp, prepRoots),
		buildMachineJoinedNeedles(p.Root, tmp, prepRoots),
		diags); err != nil {
		return nil, diags, fmt.Errorf("scanning the staged tree for build-machine paths: %w", err)
	}
	if diags.HasErrors() {
		return nil, diags, nil
	}

	if err := os.Rename(tmp, absOutput); err != nil {
		return nil, diags, fmt.Errorf("publishing the staged tree: %w", err)
	}
	published = true

	return &Result{
		Agent:            p.Name,
		Output:           absOutput,
		Fingerprint:      p.Fingerprint,
		RuntimeLanguages: languages,
	}, diags, nil
}

// harnessPlaceholderNote states plainly that the native harness runtime is not
// bundled in this slice: the published tenon harness images do not exist yet,
// so /opt/tenon/harness is an empty placeholder and the entrypoint expects the
// harness on PATH or in the base image.
const harnessPlaceholderNote = "the native harness runtime is NOT bundled in this slice; " +
	"/opt/tenon/harness is an empty placeholder and the entrypoint expects the harness executable on PATH or in the base image"

// prepareClosure prepares the agent's authored tools into a throwaway staging
// cache and returns the languages present, the prepared cache directory to
// carry into the tree, and every throwaway root it created (for the
// build-machine-path scan Stage runs before publishing: preparation may
// write these paths into the tree it's building, so the scan needs their
// exact values, even though they are removed again before Stage returns).
// A tool-free agent prepares nothing and returns no closure and no roots.
func prepareClosure(ctx context.Context, p *agentproject.Project, diags *diagnostics.List) (languages []string, closureDir string, prepRoots []string, err error) {
	if len(p.Tools) == 0 {
		return nil, "", nil, nil
	}
	langSet := map[string]bool{}
	for _, t := range p.Tools {
		langSet[t.Language] = true
	}
	for _, l := range []string{toolruntime.TypeScript, toolruntime.Python, toolruntime.Go} {
		if langSet[l] {
			languages = append(languages, l)
		}
	}

	cacheRoot, err := os.MkdirTemp("", "tenon-stage-cache-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("creating the staging cache: %w", err)
	}
	defer os.RemoveAll(cacheRoot)
	workspace, err := os.MkdirTemp("", "tenon-stage-ws-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("creating the staging workspace: %w", err)
	}
	defer os.RemoveAll(workspace)
	prepRoots = []string{cacheRoot, workspace}
	// The shared runtime cache (issue #38) is the one external location a
	// closure is now allowed to be built from — an un-rewritten
	// _sysconfigdata_*.py would otherwise bake in an operator's home
	// directory path this scan would not otherwise recognize as dangerous.
	sharedRoots, err := toolruntime.SharedRuntimeRoots()
	if err != nil {
		return nil, "", nil, fmt.Errorf("resolving the shared runtime cache roots: %w", err)
	}
	prepRoots = append(prepRoots, sharedRoots...)

	cfg := toolruntime.Config{
		Source:      p.Root,
		Workspace:   workspace,
		Fingerprint: p.Fingerprint,
		Tools:       p.Tools,
		CacheRoot:   cacheRoot,
	}
	if err := toolruntime.Prepare(ctx, cfg); err != nil {
		diags.Errorf("stage.tool.prepare.failed", "tools", "%s", diagnostics.Bound(err.Error(), 512))
		return nil, "", nil, nil
	}

	// Copy the prepared cache out of the throwaway root before its deferred
	// removal, into a staging-owned directory the caller then places in the
	// tree.
	prepared := cfg.CacheDir()

	// A Python closure's standalone interpreter bakes its own install
	// directory into one file, _sysconfigdata_*.py (ADR 0021): preparation
	// wrote the throwaway prepare-time path, which staging now rewrites, in
	// place, to the final canonical path the closure will actually occupy in
	// the published tree — the same "normalize before proving
	// relocatability" step ADR 0021 requires, and computable here because
	// the final closure root is a function of prepared's own basename
	// (the source fingerprint plus host digest), not of anything Stage
	// builds later.
	if langSet[toolruntime.Python] {
		finalClosureRoot := finalRuntimes + "/tools/" + filepath.Base(prepared)
		if err := toolruntime.RewritePythonSysconfigData(prepared, prepared, finalClosureRoot); err != nil {
			return nil, "", nil, fmt.Errorf("normalizing the python closure for staging: %w", err)
		}
	}

	// A TypeScript closure's DENO_DIR carries derived, path-keyed caches
	// (type-check/codegen output) staging's own build-machine-path scan
	// refuses to publish (ADR 0021): pruning them is staging-only
	// normalization, not something a local `tenon apply`'s persistent
	// workspace cache needs — see PruneDenoDirClosureCache. This must run
	// after toolruntime.Prepare above has returned: Prepare's own
	// inspection launch is itself a `deno run` that repopulates whatever
	// derived caches it needs.
	if langSet[toolruntime.TypeScript] {
		if err := toolruntime.PruneDenoDirClosureCache(prepared); err != nil {
			return nil, "", nil, fmt.Errorf("normalizing the typescript closure for staging: %w", err)
		}
	}

	carry, err := os.MkdirTemp("", "tenon-stage-closure-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("creating the closure directory: %w", err)
	}
	prepRoots = append(prepRoots, carry)
	dest := filepath.Join(carry, filepath.Base(prepared))
	if err := copyTree(prepared, dest); err != nil {
		os.RemoveAll(carry)
		return nil, "", nil, fmt.Errorf("carrying the tool runtime closure: %w", err)
	}
	return languages, dest, prepRoots, nil
}

// pythonInterpreterIdentity reads the interpreter identity a staged Python
// closure at closureDir carries (for example
// "cpython-3.11.13-linux-x86_64-gnu"), from the single directory name
// `uv python install` gives it under closureDir/cpython. ok is false when
// closureDir carries no cpython/ subdirectory at all — every other language's
// closure, and any closure staged before Python tools existed.
func pythonInterpreterIdentity(closureDir string) (identity string, ok bool, err error) {
	entries, rerr := os.ReadDir(filepath.Join(closureDir, "cpython"))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return "", false, nil
		}
		return "", false, rerr
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "cpython-") {
			return e.Name(), true, nil
		}
	}
	return "", false, fmt.Errorf("the staged python closure at %s carries no installed interpreter directory", closureDir)
}

// maxBuildPathScanFileBytes bounds one file the build-machine-path scan
// reads into memory. It matches toolruntime's own bound on a runtime
// executable copied whole into a closure (maxClosureExecutableBytes): the
// scan must read every legitimately staged file, deno's own ~150MB
// executable among them, whole. A file that exceeds it is an environment
// surprise, not an authored contract violation, so it fails as an error
// rather than a diagnostic.
const maxBuildPathScanFileBytes = 256 << 20

// minBuildPathComponentLen excludes short path segments from the
// build-machine-path scan. A compiled binary's own data is not text: any
// short alphanumeric run (a two- or three-character sequence, a Go test
// harness's own "001"-style TempDir counters among them) has a real chance
// of appearing somewhere in a nontrivial binary's bytes by pure coincidence,
// while every realistic build-machine identifier this scan must catch — a
// human-chosen project or session directory name, a `os.MkdirTemp` random
// suffix — safely clears this bound.
const minBuildPathComponentLen = 6

// buildPathSafeComponents lists path segments expected to appear throughout
// legitimate staged content — tenon's own canonical vocabulary, present in
// every generated harness configuration and the artifact manifest, and the
// most common generic filesystem directory names — so the build-machine-
// path scan does not misfire on them. Every other segment of an authored
// agent source path or a preparation temp root is specific enough (a
// human-chosen project or session directory name, a randomly generated
// mkdtemp suffix) that its appearance anywhere in the staged tree names a
// leak rather than a coincidence.
var buildPathSafeComponents = map[string]bool{
	"tmp": true, "var": true, "private": true, "home": true, "users": true,
	"root": true, "usr": true, "local": true, "opt": true, "go": true,
	"src": true, "pkg": true, "bin": true, "lib": true, "etc": true,
	"mnt": true, "data": true, "cache": true,
	"tenon": true, "agent": true, "agents": true, "workspace": true,
	"runtimes": true, "tools": true, "harness": true, "claude": true, "codex": true,
	// "python" is now also a shared-runtime-cache directory name
	// (toolruntime.SharedRuntimeRoots, issue #38) as well as ordinary
	// content text throughout a Python-tool agent's own generated files
	// (pyproject.toml, artifact.json's language field, .mcp.json) —
	// tenon's own vocabulary, not an operator- or machine-specific detail.
	"python": true,
}

// buildPathComponents splits dir into the path segments a build-machine-path
// scan should treat as dangerous. skipLast drops the directory's own final
// segment, for a caller whose basename is itself an expected, intentionally
// published identity (the agent's own directory name, embedded on purpose
// in the tree's canonical /opt/tenon/agents/<name> path) rather than a
// build-machine detail.
func buildPathComponents(dir string, skipLast bool) []string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(dir)), "/")
	if skipLast && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	var out []string
	for _, part := range parts {
		if len(part) < minBuildPathComponentLen {
			continue
		}
		if buildPathSafeComponents[strings.ToLower(part)] {
			continue
		}
		out = append(out, part)
	}
	return out
}

// buildMachineNeedles collects every dangerous path component the
// build-machine-path scan should search for in a text file: every segment
// of the authored agent source directory except its own final segment (the
// agent name, expected in canonical output), plus every segment of every
// throwaway preparation root (the staging build directory and whatever
// roots tool preparation used) — none of which may ever legitimately
// survive inside the published tree.
//
// This component-by-component form is deliberately not used against a
// binary file — see buildMachineJoinedNeedles and looksBinary.
func buildMachineNeedles(agentRoot, stageTmp string, prepRoots []string) [][]byte {
	seen := map[string]bool{}
	var needles [][]byte
	add := func(dir string, skipLast bool) {
		for _, c := range buildPathComponents(dir, skipLast) {
			if seen[c] {
				continue
			}
			seen[c] = true
			needles = append(needles, []byte(c))
		}
	}
	add(agentRoot, true)
	add(stageTmp, false)
	for _, r := range prepRoots {
		add(r, false)
	}
	return needles
}

// buildMachineJoinedNeedles collects the joined-path forms the
// build-machine-path scan searches a binary file for: each dangerous
// directory's own full path, unsplit, plus the single-level relative
// reference ("../" + its own final segment) the same shape a fixed-name
// build-source copy would take if it ever pointed at that directory
// directly instead of its own safe constant name (see
// toolruntime.copyGoModuleSource). Unlike buildMachineNeedles, these are
// not filtered component by component: a real leak embeds a directory as
// one contiguous run, and checking for that contiguous run is what makes
// this safe to run against a binary's own data (see looksBinary) — a
// compiled binary legitimately carries countless short, ordinary-looking
// tokens (Go standard library package names, a module's own domain-style
// import prefix and its owner segment) that collide with common directory
// names by pure chance when matched component by component, but essentially
// never reproduce a specific multi-segment directory path by coincidence.
func buildMachineJoinedNeedles(agentRoot, stageTmp string, prepRoots []string) [][]byte {
	seen := map[string]bool{}
	var needles [][]byte
	add := func(needle string) {
		if needle == "" || seen[needle] {
			return
		}
		seen[needle] = true
		needles = append(needles, []byte(needle))
	}
	for _, dir := range append([]string{agentRoot, stageTmp}, prepRoots...) {
		if dir == "" {
			continue
		}
		clean := filepath.Clean(dir)
		add(clean)
		base := filepath.Base(clean)
		if len(base) >= minBuildPathComponentLen && !buildPathSafeComponents[strings.ToLower(base)] {
			add(".." + string(filepath.Separator) + base)
		}
	}
	return needles
}

// looksBinary reports whether content is binary rather than text, by the
// same signal most "file"-style tools use: a NUL byte, or bytes that are
// not valid UTF-8. rejectBuildMachinePaths uses this only as a defensive
// fallback within a file it has otherwise classified as tenon-generated
// text (see carriedPayload): a compiled binary's own data legitimately
// contains short generic-looking tokens a text file would not, so a
// surprise binary landing in a "generated" position is still joined-matched
// rather than component-matched.
func looksBinary(content []byte) bool {
	if bytes.IndexByte(content, 0) >= 0 {
		return true
	}
	return !utf8.Valid(content)
}

// carriedPayload reports whether the staged file at rel (slash-separated,
// relative to the staged tree root) is a runtime payload tenon carries in
// from elsewhere — a compiled binary, or a third-party interpreter and
// dependency tree — as opposed to text tenon itself generates or renders,
// or the author's own copied source. The build-machine-path scan routes by
// this provenance axis, not by whether a file's own bytes happen to look
// binary: CPython's standalone interpreter alone ships roughly four
// thousand ordinary TEXT files (its stdlib and C headers), each free to
// carry short, ordinary-looking tokens ("github", "runner", "project", an
// organization or CI runner's own directory name) that collide with a real
// agent's path components by pure coincidence, exactly the false-positive
// class component matching exists to avoid for a compiled binary's data —
// so a carried-in tree is joined-matched wholesale regardless of whether
// any one file inside it is text or binary. closureRootFinal is the
// closure's own final canonical root (finalRuntimes+"/tools"), or "" for a
// tool-free agent. materializedPlugins are the staged tree roots (relative,
// slash-separated) this stage materialized a plugin reference's pinned
// content into: third-party bytes copied in from the plugin cache, in the
// same class as the interpreter tree — a plugin's own README naming a
// directory the build machine happens to share is a coincidence, not a leak
// (issue #58 review). Only the directories this stage copied are exempt; a
// vendored plugin directory the author committed is authored source and
// stays component-matched.
func carriedPayload(rel, closureRootFinal string, materializedPlugins []string) bool {
	if rel == strings.TrimPrefix(finalTenonBin, "/") {
		return true
	}
	for _, root := range materializedPlugins {
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	if closureRootFinal == "" {
		return false
	}
	root := strings.TrimPrefix(closureRootFinal, "/") + "/"
	withinClosureRoot, ok := strings.CutPrefix(rel, root)
	if !ok {
		return false
	}
	// withinClosureRoot is "<hash>/<rest>": one language closure sits
	// directly under the closure root, named by source fingerprint plus
	// host digest (toolruntime.Config.CacheDir). "<rest>" names the payload
	// within it.
	_, rest, ok := strings.Cut(withinClosureRoot, "/")
	if !ok {
		return false
	}
	switch {
	case rest == "cpython" || strings.HasPrefix(rest, "cpython/"):
		// The pinned standalone CPython interpreter tree: stdlib, C
		// headers, the compiled interpreter binary and shared library.
		return true
	case rest == "site" || strings.HasPrefix(rest, "site/"):
		// The project's own locked third-party dependencies, installed
		// flat with no venv (ADR 0021) — carried in, not authored or
		// rendered by tenon.
		return true
	case rest == "go/host":
		// The compiled Go tool host binary. go.mod and main.go beside it
		// are tenon-rendered text and stay component-matched.
		return true
	case rest == "deno" || strings.HasPrefix(rest, "deno/"):
		// The copied deno executable itself.
		return true
	case rest == "deno-dir" || strings.HasPrefix(rest, "deno-dir/"):
		// The pruned, cached-only DENO_DIR: downloaded npm package
		// sources (arbitrary third-party text), carried in rather than
		// authored or rendered by tenon — the same false-positive class
		// CPython's stdlib text files exist to avoid.
		return true
	}
	return false
}

// rejectBuildMachinePaths fails closed if any regular file inside root
// embeds a dangerous build-machine path: preparation may write
// machine-local paths, but publication may not carry them (ADR 0021). Each
// file is routed by carriedPayload: a carried-in runtime payload (the
// interpreter tree, the dependency directory, a compiled host binary, the
// tenon executable) is checked only for the fuller joined-path forms
// (joinedNeedles) — bare-component matching against payload data produces
// the class of false positive an agent living under, say, a
// `github.com/<org>/` path, or CPython's own thousands of ordinary stdlib
// text files, triggers by pure coincidence; text tenon itself generates or
// renders — the workspace integration, the apply record, a closure's own
// go.mod/main.go, artifact.json, the copied agent source — is checked
// component by component (componentNeedles), the same "a rewritten go.mod
// proved a joined-absolute-string check insufficient on its own for text"
// case ADR 0021 names, with looksBinary as a defensive fallback to joined
// matching for the rare case a "generated" position turns out to hold
// binary content after all. Every match is reported as a diagnostic naming
// the offending staged path and the exact leaked text, so a caller sees
// every leak in one run rather than stopping at the first.
func rejectBuildMachinePaths(root, closureRootFinal string, materializedPlugins []string, componentNeedles, joinedNeedles [][]byte, diags *diagnostics.List) error {
	if len(componentNeedles) == 0 && len(joinedNeedles) == 0 {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxBuildPathScanFileBytes {
			return fmt.Errorf("the staged file %s exceeds the build-path scan's size bound", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		needles := joinedNeedles
		if !carriedPayload(relSlash, closureRootFinal, materializedPlugins) {
			needles = componentNeedles
			if looksBinary(content) {
				needles = joinedNeedles
			}
		}
		return matchNeedles(relSlash, content, needles, diags)
	})
}

// matchNeedles reports every needle found in content as a
// stage.tree.build-path-leaked diagnostic naming relSlash.
func matchNeedles(relSlash string, content []byte, needles [][]byte, diags *diagnostics.List) error {
	for _, needle := range needles {
		if bytes.Contains(content, needle) {
			diags.Errorf("stage.tree.build-path-leaked", relSlash,
				"the staged file embeds the build-machine path %q; staging fails closed rather than publish a tree that verifies but carries preparation-machine detail",
				string(needle))
		}
	}
	return nil
}

// physical maps a canonical final path to its physical location under root.
func physical(root, finalPath string) string {
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(finalPath, "/")))
}

// octal renders a file mode as a four-digit octal string for the manifest.
func octal(mode fs.FileMode) string {
	return fmt.Sprintf("%04o", mode.Perm())
}

// hashSource hashes every regular file in dir (skipping symlinks and the
// workspace-state .tenon directory) into one stable digest, so preparation's
// effect on authored source can be checked. It does not fail on a symlink: the
// byte-for-byte copy is the authority that rejects one.
func hashSource(dir string) (string, error) {
	type entry struct {
		rel  string
		hash string
	}
	var entries []entry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if rel == ".tenon" && d.IsDir() {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		h, herr := hashFile(path)
		if herr != nil {
			return herr
		}
		entries = append(entries, entry{rel: filepath.ToSlash(rel), hash: h})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\x00%s\n", e.rel, e.hash)
	}
	return sha256Hex([]byte(b.String())), nil
}
