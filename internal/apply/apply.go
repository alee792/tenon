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
	"path/filepath"
	"sort"

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

// Driver is the seam between the portable project and one native harness.
// Harness-specific formats stay behind it.
type Driver interface {
	// Harness is the stable harness name: "claude" or "codex".
	Harness() string
	// Generate renders every tenon-owned native file for the project and
	// reports harness-specific warnings on diags, so validate and apply
	// surface identical diagnostics. It must be deterministic for identical
	// source.
	Generate(p *agentproject.Project, diags *diagnostics.List) []GeneratedFile
}

// Record is the durable apply record: schema version, identity, the source
// fingerprint, and the owned state of every generated file. It deliberately
// carries no timestamps so identical applies are byte-identical.
type Record struct {
	Schema      int                  `json:"schema"`
	Agent       string               `json:"agent"`
	Source      string               `json:"source"`
	Harness     string               `json:"harness"`
	Fingerprint string               `json:"fingerprint"`
	Files       map[string]OwnedFile `json:"files"`
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

// Apply writes the driver's generated files into the workspace. Contract
// violations are reported as diagnostics with stable identifiers; the error
// is reserved for environment failures.
func Apply(p *agentproject.Project, workspace string, driver Driver) (*Result, *diagnostics.List, error) {
	diags := &diagnostics.List{}

	ws, err := filepath.Abs(workspace)
	if err != nil {
		return nil, diags, fmt.Errorf("resolving workspace: %w", err)
	}
	if info, err := os.Stat(ws); err != nil || !info.IsDir() {
		diags.Errorf("apply.workspace.missing", ".",
			"the workspace must be an existing directory: %s", workspace)
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
	files := driver.Generate(p, diags)
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

	result := &Result{Fingerprint: p.Fingerprint}
	record := &Record{
		Schema:      1,
		Agent:       p.Name,
		Source:      p.Root,
		Harness:     driver.Harness(),
		Fingerprint: p.Fingerprint,
		Files:       map[string]OwnedFile{},
	}
	for _, f := range files {
		desired := OwnedFile{Hash: hashBytes(f.Content), Executable: f.Executable}
		record.Files[f.Path] = desired
		full := filepath.Join(ws, f.Path)
		current, err := os.ReadFile(full)
		if err == nil && hashBytes(current) == desired.Hash {
			if info, err := os.Stat(full); err == nil && executable(info.Mode()) == desired.Executable {
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
	if hashBytes(current) != recorded.Hash || executable(info.Mode()) != recorded.Executable {
		diags.Errorf("apply.conflict.modified", path,
			"the tenon-owned file was modified since the previous apply; tenon fails closed rather than discard the edit")
		return true
	}
	return false
}

func executable(mode os.FileMode) bool {
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
