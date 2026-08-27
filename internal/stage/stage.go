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
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

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

	// Refuse a Python- or TypeScript-bearing agent before any mutation: their
	// execution closures do not land until ADR 0021's per-language work, and a
	// tree staged for them today would verify while unable to serve. Go tools
	// stage today; apply/validate/serve keep working for every language
	// locally, only staging refuses.
	for _, lang := range []string{toolruntime.Python, toolruntime.TypeScript} {
		if projectHasLanguage(p, lang) {
			diags.Errorf("stage.tools.runtime-unsupported", "tools",
				"%s tools cannot be staged yet: Go tools stage today, while Python and TypeScript execution closures land with ADR 0021's per-language work (docs/adr/0021-execute-authored-tools-from-a-self-contained-closure.md)",
				languageLabel(lang))
		}
	}
	if diags.HasErrors() {
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

	languages, closureDir, err := prepareClosure(ctx, p, diags)
	if err != nil {
		return nil, diags, err
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
	genErr := generateIntegration(p, opts.Driver, finalAgentSource, closureRootFinal, tmp, diags)
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
			Note: "The prepared tool execution cache is staged whole. Build " +
				"toolchain executables (go, deno, uv) are NOT copied: Go tools " +
				"stage a self-contained host binary, while TypeScript and Python " +
				"still require their interpreter on the base image PATH. The " +
				"closure is not further minimized in this slice.",
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

// projectHasLanguage reports whether any of p's discovered tools are written
// in lang.
func projectHasLanguage(p *agentproject.Project, lang string) bool {
	for _, t := range p.Tools {
		if t.Language == lang {
			return true
		}
	}
	return false
}

// languageLabel renders a toolruntime language constant for prose.
func languageLabel(lang string) string {
	switch lang {
	case toolruntime.Python:
		return "Python"
	case toolruntime.TypeScript:
		return "TypeScript"
	case toolruntime.Go:
		return "Go"
	default:
		return lang
	}
}

// harnessPlaceholderNote states plainly that the native harness runtime is not
// bundled in this slice: the published tenon harness images do not exist yet,
// so /opt/tenon/harness is an empty placeholder and the entrypoint expects the
// harness on PATH or in the base image.
const harnessPlaceholderNote = "the native harness runtime is NOT bundled in this slice; " +
	"/opt/tenon/harness is an empty placeholder and the entrypoint expects the harness executable on PATH or in the base image"

// prepareClosure prepares the agent's authored tools into a throwaway staging
// cache and returns the languages present and the prepared cache directory to
// carry into the tree. A tool-free agent prepares nothing and returns "".
func prepareClosure(ctx context.Context, p *agentproject.Project, diags *diagnostics.List) (languages []string, closureDir string, err error) {
	if len(p.Tools) == 0 {
		return nil, "", nil
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
		return nil, "", fmt.Errorf("creating the staging cache: %w", err)
	}
	defer os.RemoveAll(cacheRoot)
	workspace, err := os.MkdirTemp("", "tenon-stage-ws-")
	if err != nil {
		return nil, "", fmt.Errorf("creating the staging workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	cfg := toolruntime.Config{
		Source:      p.Root,
		Workspace:   workspace,
		Fingerprint: p.Fingerprint,
		Tools:       p.Tools,
		CacheRoot:   cacheRoot,
	}
	if err := toolruntime.Prepare(ctx, cfg); err != nil {
		diags.Errorf("stage.tool.prepare.failed", "tools", "%s", diagnostics.Bound(err.Error(), 512))
		return nil, "", nil
	}

	// Copy the prepared cache out of the throwaway root before its deferred
	// removal, into a staging-owned directory the caller then places in the
	// tree.
	prepared := cfg.CacheDir()
	carry, err := os.MkdirTemp("", "tenon-stage-closure-")
	if err != nil {
		return nil, "", fmt.Errorf("creating the closure directory: %w", err)
	}
	dest := filepath.Join(carry, filepath.Base(prepared))
	if err := copyTree(prepared, dest); err != nil {
		os.RemoveAll(carry)
		return nil, "", fmt.Errorf("carrying the tool runtime closure: %w", err)
	}
	return languages, dest, nil
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
