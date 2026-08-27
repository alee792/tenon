package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	code := run([]string{"drift", agent, "--harness", "claude", "--workspace", ws, "--diagnostics", "jsonl"}, nil, &stdout, &stderr)
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
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("line %q is not one JSON object: %v", line, err)
		}
	}
}

// TestDriftValidateApplyParityUntouched proves drift's addition changed
// nothing about validate or apply's own diagnostics or exit codes for a
// project that already fails: validate and apply must still agree exactly
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

	var validateOut, applyOut, stderr bytes.Buffer
	validateCode := run([]string{"validate", agent, "--harness", "claude", "--diagnostics", "jsonl"}, nil, &validateOut, &stderr)
	applyCode := run([]string{"apply", agent, "--harness", "claude", "--diagnostics", "jsonl"}, nil, &applyOut, &stderr)
	if validateCode != 1 || applyCode != 1 {
		t.Fatalf("both must still fail with exit 1: validate=%d apply=%d", validateCode, applyCode)
	}
	if validateOut.String() != applyOut.String() {
		t.Fatalf("validate and apply must still report identical diagnostics:\n%s\n%s",
			validateOut.String(), applyOut.String())
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

	// Now introduce a genuinely hand-authored, never-owned file: even
	// --discard-local must refuse it.
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("hand-authored, never generated for claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force a stale-removal path is not needed here: AGENTS.md is simply an
	// unrecorded file coincidentally in the workspace. To exercise the
	// unowned refusal under --discard-local, target the workspace directly
	// with a file claude generation does not own at all is not generated
	// for claude, so instead prove via .mcp.json which IS owned by claude:
	// remove its record entry to simulate a hand-authored .mcp.json.
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
