// Package apply materializes a validated agent project into tenon-owned
// native files in a selected workspace. Every conflict check happens before
// any mutation: apply refuses to overwrite hand-authored native files or any
// tenon-owned file modified since the previous apply, and reapplying
// identical source is deterministic. Durable state is owner-only and written
// atomically.
package apply

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/diagnostics"
)

// GeneratedFile is one tenon-owned native file to write into the workspace.
type GeneratedFile struct {
	// Path is relative to the workspace root.
	Path    string
	Content []byte
	// Executable files are written mode 0755 instead of 0644. The intent is
	// part of the owned state: a mode-only change is a source change.
	Executable bool
}

// Target identifies where generated files land and how the managed server
// is launched from them. Generated managed-server configuration embeds
// absolute paths, so generation needs both.
type Target struct {
	// Workspace is the absolute workspace directory.
	Workspace string
	// Executable is the absolute resolved tenon executable.
	Executable string
	// IntegrationStore is the absolute base directory of the operator's
	// integration-package store (ADR 0014), used to resolve installed
	// connections (ADR 0016) offline at generation time. Empty means no
	// store is configured: installed connections then fail to resolve with
	// a clear diagnostic rather than a panic or a silent skip.
	IntegrationStore string
	// TenonVersion is the host tenon version installed-connection resolution
	// checks compatibility against. Drivers stay pure by receiving it here
	// rather than reading the single version constant themselves.
	TenonVersion string
}

// Driver is the seam between the portable project and one native harness.
// Harness-specific formats stay behind it.
type Driver interface {
	// Harness is the stable harness name: "claude" or "codex".
	Harness() string
	// Generate renders every tenon-owned native file for the project and
	// reports harness-specific warnings on diags, so validate and apply
	// surface identical diagnostics. It must be deterministic for identical
	// source and target.
	Generate(p *agentproject.Project, target Target, diags *diagnostics.List) []GeneratedFile
}

// Record is the durable apply record: schema version, identity, the source
// fingerprint, and the owned state of every generated file. It deliberately
// carries no timestamps so identical applies are byte-identical.
type Record struct {
	Schema      int    `json:"schema"`
	Agent       string `json:"agent"`
	Source      string `json:"source"`
	Harness     string `json:"harness"`
	Fingerprint string `json:"fingerprint"`
	// GitCommit is the source directory's HEAD commit SHA, recorded only
	// when the source sits inside a git repository with a clean working
	// tree at apply time. It is best-effort: a missing git, a non-repo
	// source, or a dirty tree all leave it empty rather than fail or warn
	// the apply. Its absence from a record written before this field
	// existed decodes to the same empty value, so no schema bump is
	// required.
	GitCommit string               `json:"git_commit,omitempty"`
	Files     map[string]OwnedFile `json:"files"`
}

// OwnedFile is the recorded state of one owned generated file: content hash
// and executable intent.
type OwnedFile struct {
	Hash       string `json:"hash"`
	Executable bool   `json:"executable"`
}

// RecordPath is the owner-only apply record location for one workspace and
// harness.
func RecordPath(workspace, harness string) string {
	return filepath.Join(workspace, ".tenon", "apply-"+harness+".json")
}

// Result reports what apply wrote.
type Result struct {
	Fingerprint string
	// Written lists workspace-relative paths created or refreshed.
	Written []string
	// Removed lists previously owned paths no longer generated.
	Removed []string
}

// Apply is ApplyWithTarget for a target carrying only a workspace and
// executable, kept for callers that need no configured integration store.
func Apply(p *agentproject.Project, workspace, executable string, driver Driver) (*Result, *diagnostics.List, error) {
	return ApplyWithTarget(p, Target{Workspace: workspace, Executable: executable}, driver)
}

// ApplyWithTarget writes the driver's generated files into the workspace,
// launching the managed server in generated configuration from target's
// resolved executable, and threading target's integration-store base and
// tenon version into generation exactly as validate does. Contract
// violations are reported as diagnostics with stable identifiers; the error
// is reserved for environment failures.
func ApplyWithTarget(p *agentproject.Project, target Target, driver Driver) (*Result, *diagnostics.List, error) {
	diags := &diagnostics.List{}

	executable := target.Executable
	// An unresolved executable reaching a driver is a caller bug, not an
	// authored contract violation, so it is never a diagnostic.
	if executable == "" || !filepath.IsAbs(executable) {
		return nil, diags, fmt.Errorf("the tenon executable must be an absolute resolved path: %q", executable)
	}

	ws, err := filepath.Abs(target.Workspace)
	if err != nil {
		return nil, diags, fmt.Errorf("resolving workspace: %w", err)
	}
	if info, err := os.Stat(ws); err != nil || !info.IsDir() {
		diags.Errorf("apply.workspace.missing", ".",
			"the workspace must be an existing directory: %s", target.Workspace)
		return nil, diags, nil
	}

	previous, err := readRecord(RecordPath(ws, driver.Harness()))
	if err != nil {
		diags.Errorf("apply.record.invalid", ".",
			"the existing apply record could not be read and tenon fails closed rather than guess ownership: %v", err)
		return nil, diags, nil
	}

	// Generation precedes the conflict checks so its warnings survive a
	// conflict refusal.
	files := driver.Generate(p, Target{
		Workspace:        ws,
		Executable:       executable,
		IntegrationStore: target.IntegrationStore,
		TenonVersion:     target.TenonVersion,
	}, diags)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	// Every conflict check precedes every write.
	generated := map[string]bool{}
	for _, f := range files {
		generated[f.Path] = true
		checkOwnership(ws, f.Path, previous, diags)
	}
	var stale []string
	if previous != nil {
		for path := range previous.Files {
			if generated[path] {
				continue
			}
			stale = append(stale, path)
			checkOwnership(ws, path, previous, diags)
		}
		sort.Strings(stale)
	}
	if diags.HasErrors() {
		return nil, diags, nil
	}
	if err := ensurePluginData(p, ws); err != nil {
		return nil, diags, err
	}

	result := &Result{Fingerprint: p.Fingerprint}
	record := &Record{
		Schema:      1,
		Agent:       p.Name,
		Source:      p.Root,
		Harness:     driver.Harness(),
		Fingerprint: p.Fingerprint,
		GitCommit:   cleanHeadCommit(p.Root),
		Files:       map[string]OwnedFile{},
	}
	for _, f := range files {
		desired := OwnedFile{Hash: hashBytes(f.Content), Executable: f.Executable}
		record.Files[f.Path] = desired
		full := filepath.Join(ws, f.Path)
		current, err := os.ReadFile(full)
		if err == nil && hashBytes(current) == desired.Hash {
			if info, err := os.Stat(full); err == nil && isExecutable(info.Mode()) == desired.Executable {
				result.Written = append(result.Written, f.Path)
				continue // identical reapply leaves the file untouched
			}
		}
		mode := os.FileMode(0o644)
		if f.Executable {
			mode = 0o755
		}
		if err := writeAtomic(full, f.Content, mode); err != nil {
			return nil, diags, fmt.Errorf("writing %s: %w", f.Path, err)
		}
		result.Written = append(result.Written, f.Path)
	}
	for _, path := range stale {
		if err := os.Remove(filepath.Join(ws, path)); err != nil && !os.IsNotExist(err) {
			return nil, diags, fmt.Errorf("removing stale generated %s: %w", path, err)
		}
		result.Removed = append(result.Removed, path)
		removeEmptyParents(ws, path)
	}

	recordBytes, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, diags, fmt.Errorf("encoding apply record: %w", err)
	}
	recordPath := RecordPath(ws, driver.Harness())
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
		return nil, diags, fmt.Errorf("creating state directory: %w", err)
	}
	if err := writeAtomic(recordPath, append(recordBytes, '\n'), 0o600); err != nil {
		return nil, diags, fmt.Errorf("writing apply record: %w", err)
	}
	return result, diags, nil
}

// ensurePluginData creates the private, persistent data directory of every
// agent-and-plugin identity contributing an accepted MCP server, before any
// native configuration is written (ADR 0010). Existing permissions are
// normalized to owner-only. The directory is deliberately not a tenon-owned
// generated file: it never enters the apply record, so it survives reapply
// and the removal of the server or plugin that introduced it.
func ensurePluginData(p *agentproject.Project, ws string) error {
	seen := map[string]bool{}
	for _, server := range p.PluginServers {
		dir := agentproject.PluginDataDir(ws, p.Name, server.Plugin)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("creating the plugin data directory for %s: %w", server.Plugin, err)
		}
		for _, level := range []string{dir, filepath.Dir(dir), filepath.Dir(filepath.Dir(dir))} {
			if err := os.Chmod(level, 0o700); err != nil {
				return fmt.Errorf("securing the plugin data directory for %s: %w", server.Plugin, err)
			}
		}
	}
	return nil
}

// Verify reports whether the workspace still carries exactly the state the
// last apply of p for harness wrote: the record exists, targets this harness
// and this source fingerprint, and every owned generated file is present and
// unmodified. Every failure names the stale path or fingerprint and directs
// the operator to reapply, so a tenon-owned process opened against drifted
// setup fails closed rather than serving a stale agent.
func Verify(p *agentproject.Project, workspace, harness string) error {
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return fmt.Errorf("resolving workspace: %w", err)
	}
	record, err := readRecord(RecordPath(ws, harness))
	if err != nil {
		return fmt.Errorf("the %s apply record could not be read (%v); run tenon apply", harness, err)
	}
	if record == nil {
		return fmt.Errorf("the workspace %s carries no %s apply record; run tenon apply", ws, harness)
	}
	if record.Harness != harness {
		return fmt.Errorf("the apply record in %s was written for harness %q, not %q; run tenon apply", ws, record.Harness, harness)
	}
	if record.Fingerprint != p.Fingerprint {
		return fmt.Errorf("the applied source fingerprint %s no longer matches the agent source %s; run tenon apply",
			record.Fingerprint, p.Fingerprint)
	}
	if len(record.Files) == 0 {
		return fmt.Errorf("the %s apply record in %s owns no generated files; run tenon apply", harness, ws)
	}
	paths := make([]string, 0, len(record.Files))
	for path := range record.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		owned := record.Files[path]
		full := filepath.Join(ws, path)
		info, err := os.Lstat(full)
		if err != nil {
			return fmt.Errorf("the tenon-owned file %s is missing from %s; run tenon apply", path, ws)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("the tenon-owned file %s is no longer a regular file; run tenon apply", path)
		}
		current, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("the tenon-owned file %s could not be read: %v; run tenon apply", path, err)
		}
		if hashBytes(current) != owned.Hash || isExecutable(info.Mode()) != owned.Executable {
			return fmt.Errorf("the tenon-owned file %s was modified since the last apply; run tenon apply", path)
		}
	}
	return nil
}

// checkOwnership reports a conflict diagnostic when the workspace file at
// path cannot be safely replaced or removed: it exists without a record
// (hand-authored) or its bytes or executable bit differ from the recorded
// owned state (modified since the previous apply).
func checkOwnership(ws, path string, previous *Record, diags *diagnostics.List) bool {
	full := filepath.Join(ws, path)
	info, err := os.Lstat(full)
	if err != nil {
		return false // absent: safe to create, nothing to remove
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		diags.Errorf("apply.conflict.unowned", path,
			"the existing workspace entry is not a regular file and tenon never replaces it")
		return true
	}
	var recorded OwnedFile
	var owned bool
	if previous != nil {
		recorded, owned = previous.Files[path]
	}
	if !owned {
		diags.Errorf("apply.conflict.unowned", path,
			"a hand-authored native file already exists and tenon refuses to overwrite it; move it aside or choose another workspace")
		return true
	}
	current, err := os.ReadFile(full)
	if err != nil {
		diags.Errorf("apply.conflict.unowned", path, "the existing workspace file could not be read: %v", err)
		return true
	}
	if hashBytes(current) != recorded.Hash || isExecutable(info.Mode()) != recorded.Executable {
		diags.Errorf("apply.conflict.modified", path,
			"the tenon-owned file was modified since the previous apply; tenon fails closed rather than discard the edit")
		return true
	}
	return false
}

func isExecutable(mode os.FileMode) bool {
	return mode.Perm()&0o111 != 0
}

// removeEmptyParents removes directories left empty by a stale removal,
// walking the workspace-relative parent chain upward. os.Remove refuses a
// non-empty directory, so the first failure is the stop condition; the
// workspace root ("." relative) and .tenon are never candidates.
func removeEmptyParents(ws, path string) {
	for dir := filepath.Dir(path); dir != "." && dir != ".tenon" && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if os.Remove(filepath.Join(ws, dir)) != nil {
			return
		}
	}
}

func readRecord(path string) (*Record, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Schema != 1 {
		return nil, fmt.Errorf("unsupported apply record schema %d", r.Schema)
	}
	return &r, nil
}

func hashBytes(b []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(b))
}

// cleanHeadCommit returns the HEAD commit SHA for dir, but only when dir sits
// inside a git repository with an empty `git status --porcelain`. Every
// failure to establish that — git not installed, dir outside any repository,
// a dirty tree, a repository with no commits yet — is an ordinary miss
// reported as "", never an error: apply must behave identically for agent
// sources that are not git repositories.
func cleanHeadCommit(dir string) string {
	status, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil || len(strings.TrimSpace(string(status))) != 0 {
		return ""
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(head))
}

// writeAtomic writes content to a same-directory temporary file and renames
// it into place.
func writeAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tenon-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
