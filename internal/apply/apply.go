// Package apply materializes a validated agent project into tenon-owned
// native files in a selected workspace. Every conflict check happens before
// any mutation: apply refuses to overwrite hand-authored native files or any
// tenon-owned file modified since the previous apply, and reapplying
// identical source is deterministic. Durable state is owner-only and written
// atomically. Target.DiscardLocal lets a caller explicitly opt an owned file
// modified since the previous apply back into being overwritten; it never
// weakens the hand-authored-file refusal. Drift reporting (regenerating in
// memory and diffing against the workspace and the apply record, without
// writing anything) lives in cmd/tenon, built on this package's exported
// Driver, Target, and ReadRecord.
package apply

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	// ManifestIdentity is the supplied agent manifest's stable identity
	// (manifest.Manifest.Identity), recorded in the apply record as a
	// provenance join key so observation made outside tenon can be joined to
	// the exact pinned closure that produced it. Empty when no manifest was
	// supplied; it is never rendered into model-facing content, so it is
	// deliberately NOT forwarded into the Target a driver's Generate receives
	// (see ApplyWithTarget).
	ManifestIdentity string
	// Model is the pinned model (manifest.HarnessPins.Model) for the selected
	// harness, or "" when no manifest was supplied or that manifest pins no
	// model (ADR 0020). Unlike ManifestIdentity, Model IS forwarded into the
	// Target a driver's Generate receives: it is native configuration the
	// harness reads at launch (a `model` key in .codex/config.toml or
	// .claude/settings.json), not model-facing instructions content, so the
	// no-leak rule that keeps provenance out of Generate does not apply to
	// it. A driver emits Model into its owned native configuration file and
	// nowhere else.
	Model string
	// DiscardLocal, when true, lets apply overwrite a tenon-owned file that
	// was modified on disk since the previous apply (apply.conflict.modified)
	// instead of refusing. It never weakens apply.conflict.unowned: a
	// hand-authored file that was never recorded as owned is always refused,
	// discard or not. It is a caller policy choice, not generated content, so
	// (like ManifestIdentity) it is deliberately NOT forwarded into the
	// Target a driver's Generate receives.
	DiscardLocal bool
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
	GitCommit string `json:"git_commit,omitempty"`
	// Manifest is the supplied manifest's identity, a provenance join key
	// present only when a manifest was supplied to apply. It is omitted
	// otherwise so an unsupplied manifest leaves the record byte-identical.
	Manifest string               `json:"manifest,omitempty"`
	Files    map[string]OwnedFile `json:"files"`
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

// ReadRecord reads the apply record for one workspace and harness, returning
// (nil, nil) when no apply has ever been recorded there. It performs no
// mutation and no further validation beyond decoding — callers that need
// Verify's stronger workspace-matches-record guarantee should call Verify
// instead; ReadRecord is for read-only reporting (drift) that must work even
// against a workspace Verify would refuse.
func ReadRecord(workspace, harness string) (*Record, error) {
	return readRecord(RecordPath(workspace, harness))
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
		Model:            target.Model,
	}, diags)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	// Every conflict check precedes every write.
	generated := map[string]bool{}
	for _, f := range files {
		generated[f.Path] = true
		checkOwnership(ws, f.Path, previous, target.DiscardLocal, diags)
	}
	var stale []string
	if previous != nil {
		for path := range previous.Files {
			if generated[path] {
				continue
			}
			stale = append(stale, path)
			checkOwnership(ws, path, previous, target.DiscardLocal, diags)
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
		GitCommit:   CleanHeadCommit(p.Root),
		Manifest:    target.ManifestIdentity,
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

// OwnershipKind classifies one workspace path's tenon-ownership standing
// against the previous apply record — the exact rule checkOwnership
// enforces before any mutation, exported so read-only reporting (drift) can
// classify a path identically without duplicating (and risking diverging
// from) that rule.
type OwnershipKind int

const (
	// OwnershipAbsent: nothing exists at path — safe to create, nothing to
	// remove.
	OwnershipAbsent OwnershipKind = iota
	// OwnershipClean: a regular file at path matches the recorded owned
	// state (content hash and executable bit) exactly.
	OwnershipClean
	// OwnershipNonRegular: a symlink or other non-regular entry exists at
	// path. Refused (apply.conflict.unowned) regardless of any record.
	OwnershipNonRegular
	// OwnershipUnowned: a regular file exists at path but was never
	// recorded as tenon-owned. Refused (apply.conflict.unowned).
	OwnershipUnowned
	// OwnershipModified: path was recorded as owned, but the file's bytes
	// or executable bit no longer match the recorded state. Refused
	// (apply.conflict.modified) unless the caller has opted into
	// Target.DiscardLocal.
	OwnershipModified
)

// ClassifyOwnership reports path's ownership standing in ws against
// previous. readErr is non-nil only when path is a recorded, regular file
// that could not be read; the returned kind is OwnershipUnowned in that
// case, matching checkOwnership's fail-closed treatment of an unreadable
// recorded file (an unreadable file can never be proven safe to replace).
func ClassifyOwnership(ws, path string, previous *Record) (kind OwnershipKind, readErr error) {
	full := filepath.Join(ws, path)
	info, err := os.Lstat(full)
	if err != nil {
		return OwnershipAbsent, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return OwnershipNonRegular, nil
	}
	var recorded OwnedFile
	var owned bool
	if previous != nil {
		recorded, owned = previous.Files[path]
	}
	if !owned {
		return OwnershipUnowned, nil
	}
	current, err := os.ReadFile(full)
	if err != nil {
		return OwnershipUnowned, err
	}
	if hashBytes(current) != recorded.Hash || isExecutable(info.Mode()) != recorded.Executable {
		return OwnershipModified, nil
	}
	return OwnershipClean, nil
}

// checkOwnership reports a conflict diagnostic when the workspace file at
// path cannot be safely replaced or removed: it exists without a record
// (hand-authored) or its bytes or executable bit differ from the recorded
// owned state (modified since the previous apply). When discardLocal is
// true, a modified-since-apply owned file is reported as no conflict at all
// so the caller's normal write (or stale removal) proceeds over it; a
// hand-authored, never-recorded file is refused regardless — discardLocal
// only ever widens what a previous tenon apply already owned, never what a
// person authored by hand.
func checkOwnership(ws, path string, previous *Record, discardLocal bool, diags *diagnostics.List) bool {
	kind, err := ClassifyOwnership(ws, path, previous)
	switch kind {
	case OwnershipAbsent, OwnershipClean:
		return false
	case OwnershipNonRegular:
		diags.Errorf("apply.conflict.unowned", path,
			"the existing workspace entry is not a regular file and tenon never replaces it")
		return true
	case OwnershipUnowned:
		if err != nil {
			diags.Errorf("apply.conflict.unowned", path, "the existing workspace file could not be read: %v", err)
			return true
		}
		diags.Errorf("apply.conflict.unowned", path,
			"a hand-authored native file already exists and tenon refuses to overwrite it; move it aside or choose another workspace")
		return true
	case OwnershipModified:
		if discardLocal {
			return false // caller opted in: the local edit is discarded, never adopted
		}
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

// gitQueryBudget bounds each best-effort git query in CleanHeadCommit: a
// stale lock, a prompting credential helper, or a hung filesystem must not
// block apply indefinitely.
const gitQueryBudget = 5 * time.Second

// CleanHeadCommit returns the HEAD commit SHA for dir, but only when dir
// sits inside a git repository whose dir subtree (never the rest of a
// larger repository dir may be part of) reports an empty
// `git status --porcelain`. Every failure to establish that — git not
// installed, dir outside any repository, a dirty dir subtree, a repository
// with no commits yet, or a query that exceeds gitQueryBudget — is an
// ordinary miss reported as "", never an error: apply must behave
// identically for agent sources that are not git repositories.
func CleanHeadCommit(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitQueryBudget)
	defer cancel()
	status, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain", "--", ".").Output()
	if err != nil || len(strings.TrimSpace(string(status))) != 0 {
		return ""
	}
	head, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
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
