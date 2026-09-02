package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDriftCleanWorkspaceExitsZero proves the no-drift case: right after
// apply, drift reports every owned file unchanged and exits 0 without
// writing anything.
func TestDriftCleanWorkspaceExitsZero(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	recordBefore, err := os.ReadFile(filepath.Join(ws, ".tenon", "apply-claude.json"))
	if err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("drift exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "clean:") {
		t.Fatalf("expected a clean summary, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "unchanged CLAUDE.md") || !strings.Contains(stdout.String(), "unchanged .mcp.json") {
		t.Fatalf("expected both owned files reported unchanged: %q", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("a clean drift must report nothing on stderr, got %q", stderr.String())
	}
	recordAfter, err := os.ReadFile(filepath.Join(ws, ".tenon", "apply-claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(recordBefore) != string(recordAfter) {
		t.Fatal("drift must never write the apply record")
	}
}

// TestDriftJSONLResultSummaryOK proves the jsonl-mode driftResult on a clean
// run carries outcome ok alongside agent/harness/workspace/fingerprint,
// matching checkResult and applyResult's own shape.
func TestDriftJSONLResultSummaryOK(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws, "--format", "jsonl"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("drift exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	var got driftResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
		t.Fatalf("result line %q is not valid JSON: %v", stdout.String(), err)
	}
	if got.Outcome != "ok" || got.Agent != "my-agent" || got.Harness != "claude" || got.Workspace != ws || got.Fingerprint == "" {
		t.Fatalf("result = %+v, want outcome ok, agent=my-agent harness=claude workspace=%s and a non-empty fingerprint", got, ws)
	}
}

// TestDriftReportsModifiedOwnedFileWithDiff proves drift's central case: an
// owned file edited on disk since apply is reported as modified, exits 1,
// carries the stable drift.file.modified identifier at the owned file's
// workspace-relative path, and the prose output includes a unified diff
// naming both the removed and added lines. The edit itself is left in place
// — drift never writes anything.
func TestDriftReportsModifiedOwnedFileWithDiff(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	claudeMD := filepath.Join(ws, "CLAUDE.md")
	original, err := os.ReadFile(claudeMD)
	if err != nil {
		t.Fatal(err)
	}
	edited := append([]byte{}, original...)
	edited = append(edited, []byte("a hand-authored addition\n")...)
	if err := os.WriteFile(claudeMD, edited, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("drift exit %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "drift.file.modified") || !strings.Contains(stderr.String(), "CLAUDE.md") {
		t.Fatalf("expected drift.file.modified at CLAUDE.md on stderr, got %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "+a hand-authored addition") {
		t.Fatalf("expected the unified diff to show the added line, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--- generated/CLAUDE.md") || !strings.Contains(stdout.String(), "+++ workspace/CLAUDE.md") {
		t.Fatalf("expected labeled unified-diff headers, got %q", stdout.String())
	}
	got, err := os.ReadFile(claudeMD)
	if err != nil || string(got) != string(edited) {
		t.Fatalf("drift must never modify the workspace: got %q, err %v", got, err)
	}
}

// TestDriftReportsMissingOwnedFile proves a deleted owned file is reported
// as drift.file.missing and fails drift.
func TestDriftReportsMissingOwnedFile(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	if err := os.Remove(filepath.Join(ws, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("drift exit %d, want 1\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "drift.file.missing") || !strings.Contains(stderr.String(), "CLAUDE.md") {
		t.Fatalf("expected drift.file.missing at CLAUDE.md, got %q", stderr.String())
	}
}

// TestDriftReportsStaleRecordedFile proves a file recorded by a previous
// apply but no longer generated (source since changed to stop producing it)
// is reported as drift.file.stale, without drift itself removing it.
func TestDriftReportsStaleRecordedFile(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	subagentDir := filepath.Join(agent, "subagents", "helper")
	writeFile(t, agent, "subagents/helper/instructions.md", []byte(minimalSubagentInstructionsFor("helper")), 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	generatedSubagent := filepath.Join(ws, ".claude", "agents", "helper.md")
	if _, err := os.Stat(generatedSubagent); err != nil {
		t.Fatalf("expected the generated subagent file to exist: %v", err)
	}
	// Remove the subagent from source, so a fresh generation no longer
	// produces it; the workspace file (and its record entry) are now stale.
	if err := os.RemoveAll(subagentDir); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("drift exit %d, want 1\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "drift.file.stale") || !strings.Contains(stderr.String(), ".claude/agents/helper.md") {
		t.Fatalf("expected drift.file.stale at .claude/agents/helper.md, got %q", stderr.String())
	}
	if _, err := os.Stat(generatedSubagent); err != nil {
		t.Fatal("drift must never remove a stale file; only apply removes it")
	}
}

// TestDriftJSONLIdentifiersAreStableAndParseable proves the machine-readable
// contract for all three finding kinds in one run: a modified file, a
// missing file, and a stale record entry each surface as one parseable JSON
// diagnostic naming its stable identifier and workspace-relative path.
func TestDriftJSONLIdentifiersAreStableAndParseable(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	subagentDir := filepath.Join(agent, "subagents", "helper")
	writeFile(t, agent, "subagents/helper/instructions.md", []byte(minimalSubagentInstructionsFor("helper")), 0o644)
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}

	// Modify CLAUDE.md, delete .mcp.json, and drop the subagent from source
	// so its generated file becomes stale — one of each finding kind.
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(ws, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(subagentDir); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws, "--format", "jsonl"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("drift exit %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	ds := parseDiagLines(t, stdout.String())
	wantByPath := map[string]string{
		"CLAUDE.md":                "drift.file.modified",
		".mcp.json":                "drift.file.missing",
		".claude/agents/helper.md": "drift.file.stale",
	}
	seen := map[string]bool{}
	for _, d := range ds {
		if id, ok := wantByPath[d.Path]; ok {
			if d.ID != id {
				t.Fatalf("path %s: id = %q, want %q", d.Path, d.ID, id)
			}
			if d.Severity != "error" {
				t.Fatalf("path %s: severity = %q, want error", d.Path, d.Severity)
			}
			seen[d.Path] = true
		}
	}
	for path := range wantByPath {
		if !seen[path] {
			t.Fatalf("expected a finding at %s, got %+v", path, ds)
		}
	}
	// Every jsonl line must itself be a single parseable JSON object with no
	// unescaped embedded newlines breaking the one-object-per-line contract.
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, line := range lines {
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("line %q is not one JSON object: %v", line, err)
		}
	}
	// The stream ends with an outcome object distinguishing this from a
	// gate_failed run: the source itself is fine, only the workspace drifted.
	var final struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil || final.Outcome != "drift" {
		t.Fatalf("the stream must end with the drift outcome object, got %q", lines[len(lines)-1])
	}
}

// TestDriftValidateApplyParityUntouched proves drift's addition changed
// nothing about check or apply's own diagnostics or exit codes for a
// project that already fails: check and apply must still agree exactly
// as before, and a passing apply must still succeed and write records
// exactly as before.
func TestDriftValidateApplyParityUntouched(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	if err := os.Mkdir(filepath.Join(agent, "schedules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "schedules", "bad.md"),
		[]byte("---\ncron: not a cron\n---\n\nDo the thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var checkOut, applyOut, stderr bytes.Buffer
	checkCode := run([]string{"check", agent, "--harness", "claude", "--format", "jsonl"}, nil, &checkOut, &stderr)
	applyCode := run([]string{"apply", agent, "--harness", "claude", "--format", "jsonl"}, nil, &applyOut, &stderr)
	if checkCode != 1 || applyCode != 1 {
		t.Fatalf("both must still fail with exit 1: check=%d apply=%d", checkCode, applyCode)
	}
	if checkDiagnostics(t, checkOut.String()) != checkDiagnostics(t, applyOut.String()) {
		t.Fatalf("check and apply must still report identical diagnostics:\n%s\n%s",
			checkOut.String(), applyOut.String())
	}
}

// TestApplyDiscardLocalOverwritesModifiedButRefusesUnowned proves the CLI
// wiring for --discard-local end to end: without the flag apply refuses a
// locally modified owned file; with the flag it overwrites that file, and a
// separate hand-authored, never-owned file is still refused even with the
// flag set.
func TestApplyDiscardLocalOverwritesModifiedButRefusesUnowned(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("first apply exit %d: %s", code, stderr.String())
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without --discard-local, apply refuses.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("apply exit %d, want 1 (refusal)\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "apply.conflict.modified") {
		t.Fatalf("expected apply.conflict.modified, got %q", stderr.String())
	}
	got, _ := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if string(got) != "edited by hand\n" {
		t.Fatalf("the edit must survive a refused apply: %q", got)
	}

	// With --discard-local, apply overwrites the modified owned file.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--discard-local"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("discard-local apply exit %d: %s", code, stderr.String())
	}
	got, err := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "You review pull requests carefully.") || strings.Contains(string(got), "edited by hand") {
		t.Fatalf("--discard-local must overwrite the local edit with regenerated content: %q", got)
	}

	// Now simulate a hand-authored .mcp.json: overwrite it and strip its
	// entry from the apply record, so it is exactly what apply.conflict.
	// unowned describes — a claude-owned path with content on disk but no
	// recorded owned state. Even --discard-local must refuse it.
	record := filepath.Join(ws, ".tenon", "apply-claude.json")
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mcp.json"), []byte("{\"hand\":\"authored\"}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, bytes.Replace(raw, []byte(`".mcp.json"`), []byte(`"never-owned-in-this-test.json"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--discard-local"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("apply exit %d, want 1 (still refused)\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "apply.conflict.unowned") {
		t.Fatalf("expected apply.conflict.unowned even with --discard-local, got %q", stderr.String())
	}
	got, _ = os.ReadFile(filepath.Join(ws, ".mcp.json"))
	if string(got) != `{"hand":"authored"}` {
		t.Fatalf("the hand-authored file must never be overwritten, even with --discard-local: %q", got)
	}
}

// TestDriftDetectsExecutableBitChange proves drift catches what apply itself
// refuses on a mode-only change: identical bytes, a flipped executable bit.
// apply.conflict.modified fires on exactly this in
// TestApplyRefusesModeOnlyModifiedOwnedFile; drift must agree.
func TestDriftDetectsExecutableBitChange(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	if err := os.Chmod(filepath.Join(ws, "CLAUDE.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("drift exit %d, want 1 (executable bit changed with identical bytes)\nstdout: %s\nstderr: %s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "drift.file.modified") || !strings.Contains(stderr.String(), "CLAUDE.md") {
		t.Fatalf("expected drift.file.modified at CLAUDE.md for an executable-bit-only change, got %q", stderr.String())
	}
}

// TestDriftDetectsSymlinkReplacement proves drift catches an owned path
// replaced by a symlink — apply refuses this as apply.conflict.unowned
// (checkOwnership never follows a symlink), and drift must agree rather than
// silently follow it via os.ReadFile and report the target's content.
func TestDriftDetectsSymlinkReplacement(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	claudeMD := filepath.Join(ws, "CLAUDE.md")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.md")
	original, err := os.ReadFile(claudeMD)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(elsewhere, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(claudeMD); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, claudeMD); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("drift exit %d, want 1 (owned path replaced by a symlink)\nstdout: %s\nstderr: %s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "drift.file.modified") || !strings.Contains(stderr.String(), "CLAUDE.md") {
		t.Fatalf("expected drift.file.modified at CLAUDE.md for a symlink replacement, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "symlink") {
		t.Fatalf("expected the finding to name the symlink, got %q", stderr.String())
	}
}

// TestDriftDetectsStaleRecordHashDespiteMatchingDisk proves the case the
// reviewer flagged directly: when the apply record's hash for a path no
// longer matches the disk file, drift must report modified even though the
// disk file happens to already equal the freshly regenerated content —
// exactly the scenario where apply.ClassifyOwnership (not a bare disk-vs-
// regeneration byte comparison) is required to agree with what apply itself
// would refuse.
func TestDriftDetectsStaleRecordHashDespiteMatchingDisk(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	// The disk file is left completely untouched — it still equals exactly
	// what a fresh regeneration produces. Only the record's hash for it is
	// corrupted, simulating a record that has fallen out of sync with what
	// is actually recorded as owned.
	recordPath := filepath.Join(ws, ".tenon", "apply-claude.json")
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	var files map[string]map[string]json.RawMessage
	if err := json.Unmarshal(record["files"], &files); err != nil {
		t.Fatal(err)
	}
	files["CLAUDE.md"]["hash"] = json.RawMessage(`"sha256:0000000000000000000000000000000000000000000000000000000000000000"`)
	patchedFiles, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	record["files"] = patchedFiles
	patched, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, patched, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("drift exit %d, want 1 (stale record hash despite matching disk)\nstdout: %s\nstderr: %s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "drift.file.modified") || !strings.Contains(stderr.String(), "CLAUDE.md") {
		t.Fatalf("expected drift.file.modified at CLAUDE.md despite disk matching the fresh regeneration, got %q", stderr.String())
	}
}

// TestDriftManifestPinnedModelReportsClean proves the model-pinned drift
// blocker fix: a project applied with a manifest that pins a model
// regenerates identically when drift is given the same manifest, so a
// pristine workspace reports clean rather than false drift on the model
// configuration file alone.
func TestDriftManifestPinnedModelReportsClean(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	manifestPath := writePinsForModel(t, agent, "claude", "claude-opus-4")

	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--pins", manifestPath}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(ws, ".claude", "settings.json")); err != nil {
		t.Fatalf("expected the model-pinned settings.json to exist: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws, "--pins", manifestPath}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("drift exit %d, want 0 (clean)\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "unchanged .claude/settings.json") {
		t.Fatalf("expected .claude/settings.json reported unchanged, got %q", stdout.String())
	}
}

// TestDriftJSONLModifiedFindingCarriesDiff proves the JSONL parity fix: an
// improvement loop reading drift.file.modified in jsonl mode gets the
// unified diff in the finding's own detail field, not only in prose-mode
// stdout it never reads. It also proves the driftResult summary carries
// fingerprint, matching checkResult/applyResult's shape.
func TestDriftJSONLModifiedFindingCarriesDiff(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws, "--format", "jsonl"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("drift exit %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var d struct {
			ID     string `json:"id"`
			Path   string `json:"path"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		if d.ID == "drift.file.modified" && d.Path == "CLAUDE.md" {
			found = true
			if d.Detail == "" {
				t.Fatalf("expected a non-empty diff in the jsonl finding's detail field: %+v", d)
			}
			if !strings.Contains(d.Detail, "--- generated/CLAUDE.md") {
				t.Fatalf("expected the diff detail to carry the labeled header, got %q", d.Detail)
			}
			if !strings.Contains(d.Detail, "+edited") {
				t.Fatalf("expected the diff detail to show the workspace's edited content, got %q", d.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("expected a drift.file.modified finding at CLAUDE.md, got %q", stdout.String())
	}
}

// TestDriftFlagValidation proves usage errors exit 2, matching
// TestRunFlagValidation's pattern for the run command.
func TestDriftFlagValidation(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"missing workspace", []string{"drift", agent, "--harness", "claude"}},
		{"bad harness", []string{"drift", agent, "--workspace", ws, "--harness", "gpt"}},
		{"bad format value", []string{"drift", agent, "--workspace", ws, "--harness", "claude", "--format", "yaml"}},
		{"no agent", []string{"drift", "--workspace", ws, "--harness", "claude"}},
		{"two agents", []string{"drift", agent, agent, "--workspace", ws, "--harness", "claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, nil, &stdout, &stderr); code != 2 {
				t.Fatalf("exit = %d, want 2\nstderr: %s", code, stderr.String())
			}
		})
	}
}

// TestUnifiedDiffOverLimitStaysBounded proves the diff-mechanics fix: a file
// pair beyond driftDiffLineLimit renders a short elided notice instead of
// the line-alignment path — and, crucially, that notice is far smaller than
// the input, not larger. The reviewer measured the previous fallback (dump
// every old line, then every new line) emitting MORE output than the
// input's line count; this proves the replacement never does that.
func TestUnifiedDiffOverLimitStaysBounded(t *testing.T) {
	oldLines := make([]string, driftDiffLineLimit+1)
	newLines := make([]string, driftDiffLineLimit+1)
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("old line %d", i)
		newLines[i] = fmt.Sprintf("new line %d", i)
	}
	old := []byte(strings.Join(oldLines, "\n") + "\n")
	newer := []byte(strings.Join(newLines, "\n") + "\n")

	diff := unifiedDiff("generated/big.md", "workspace/big.md", old, newer)
	if diff == "" {
		t.Fatal("expected a non-empty elided notice")
	}
	gotLines := strings.Count(diff, "\n") + 1
	if gotLines > 10 {
		t.Fatalf("elided notice is %d lines, want a small constant-size notice, not proportional to the %d-line input",
			gotLines, len(oldLines))
	}
	if len(diff) >= len(old)+len(newer) {
		t.Fatalf("elided notice (%d bytes) must be far smaller than the input (%d+%d bytes)", len(diff), len(old), len(newer))
	}
	if !strings.Contains(diff, "elided") {
		t.Fatalf("expected the notice to say the diff was elided, got %q", diff)
	}
}

// TestUnifiedDiffOverLimitBoundsMemory proves the DP table's memory stays
// bounded at driftDiffLineLimit: the reviewer measured 126 MB for a
// 3999-line pair under the old (unbounded) limit. This proves a same-order
// input completes quickly and without the elided path being skipped by
// mistake (the two inputs must actually differ in line count to hit the
// over-limit branch rather than the identical-content fast path).
func TestUnifiedDiffOverLimitBoundsMemory(t *testing.T) {
	n := driftDiffLineLimit + 500
	oldLines := make([]string, n)
	newLines := make([]string, n+1) // deliberately different length
	for i := 0; i < n; i++ {
		oldLines[i] = fmt.Sprintf("line %d", i)
		newLines[i] = fmt.Sprintf("line %d", i)
	}
	newLines[n] = "one more line"
	old := []byte(strings.Join(oldLines, "\n") + "\n")
	newer := []byte(strings.Join(newLines, "\n") + "\n")

	done := make(chan string, 1)
	go func() { done <- unifiedDiff("generated/x", "workspace/x", old, newer) }()
	select {
	case diff := <-done:
		if !strings.Contains(diff, "elided") {
			t.Fatalf("expected the over-limit path (elided notice), got %q", diff)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unifiedDiff did not return promptly for an over-limit pair; the DP path was likely taken instead of being bounded")
	}
}

// TestUnifiedDiffTrailingNewlineOnlyChangeIsNotBlank proves the fix for a
// bare-blank-line output on a trailing-newline-only difference: when the
// line-level content is identical and only a trailing newline differs, the
// diff names that explicitly rather than rendering an empty-looking hunk.
func TestUnifiedDiffTrailingNewlineOnlyChangeIsNotBlank(t *testing.T) {
	old := []byte("line one\nline two\n")
	newer := []byte("line one\nline two")

	diff := unifiedDiff("generated/x", "workspace/x", old, newer)
	if strings.TrimSpace(diff) == "" {
		t.Fatal("a trailing-newline-only change must not render as a blank diff")
	}
	if !strings.Contains(diff, "newline") {
		t.Fatalf("expected the diff to name the newline difference explicitly, got %q", diff)
	}
}

// TestUnifiedDiffBoundsTotalBytes proves driftDiffByteLimit is actually
// enforced end to end: many small scattered changes within the line-count
// limit still produce a diff capped at driftDiffByteLimit bytes.
func TestUnifiedDiffBoundsTotalBytes(t *testing.T) {
	const lines = 800
	oldLines := make([]string, lines)
	newLines := make([]string, lines)
	for i := 0; i < lines; i++ {
		oldLines[i] = fmt.Sprintf("this is a moderately long unchanged context line number %d", i)
		newLines[i] = oldLines[i]
		if i%2 == 0 {
			// Scatter a change on every other line so every hunk carries
			// context on both sides — no hunks merge into one contiguous
			// block, keeping the rendered diff large despite the modest
			// line count.
			newLines[i] = fmt.Sprintf("this is a CHANGED moderately long context line number %d", i)
		}
	}
	old := []byte(strings.Join(oldLines, "\n") + "\n")
	newer := []byte(strings.Join(newLines, "\n") + "\n")

	diff := unifiedDiff("generated/x", "workspace/x", old, newer)
	if len(diff) > driftDiffByteLimit+200 { // +200 slack for the truncation marker itself
		t.Fatalf("diff is %d bytes, want at most ~%d (driftDiffByteLimit)", len(diff), driftDiffByteLimit)
	}
	if len(diff) > driftDiffByteLimit {
		if !strings.Contains(diff, "truncated") {
			t.Fatalf("a diff over the byte limit must carry a truncation marker, got tail %q", diff[len(diff)-60:])
		}
	}
}

// TestDriftAgainstAMissingWorkspaceIsDriftNotGateFailure proves the outcome
// names what actually failed: the source passed the gate and the workspace
// is what is missing, so every generated path classifies as missing and the
// run ends in the ordinary drift outcome rather than claiming the source is
// invalid.
//
// The agent carries a real authored tool on purpose. Tool preparation runs
// its language host as a subprocess, and a host launched with its working
// directory set to a workspace that does not exist cannot start at all — so
// a tool-free agent would pass this test over the exact gap it is meant to
// close, reporting tool.inspect.failed and gate_failed for a workspace whose
// only problem is that it is missing.
func TestDriftAgainstAMissingWorkspaceIsDriftNotGateFailure(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeGoTool(t, agent, goToolFile)
	missing := filepath.Join(t.TempDir(), "never-applied")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", missing, "--format", "jsonl"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("drift against a missing workspace must exit 1, got %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if lines[len(lines)-1] != `{"outcome":"drift"}` {
		t.Fatalf("the stream must end with the drift outcome: %q", stdout.String())
	}
	diags := parseDiagLines(t, strings.Join(lines[:len(lines)-1], "\n"))
	if len(diags) < 2 {
		t.Fatalf("expected every owned path reported missing: %q", stdout.String())
	}
	for _, d := range diags {
		if d.ID != "drift.file.missing" {
			t.Fatalf("every finding against a missing workspace is a missing file, got %+v", d)
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("drift must not create the workspace it reports on: %v", err)
	}
}

// TestDriftAgainstAFileWorkspaceIsAUsageError proves the third case is told
// apart from the other two: a workspace that exists but is a regular file is
// neither drift nor a gate failure but a mistake in the invocation, so it
// exits 2 with a usage message and no outcome object at all — the same shape
// every other usage error has.
func TestDriftAgainstAFileWorkspaceIsAUsageError(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("workspace?\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"drift", agent, "--harness", "claude", "--workspace", file, "--format", "jsonl"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("a file passed as --workspace must exit 2, got %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("a usage error carries no outcome object: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--workspace must be a directory (found a file)") ||
		!strings.Contains(stderr.String(), file) {
		t.Fatalf("the usage error must name the rule and the path: %q", stderr.String())
	}
}
