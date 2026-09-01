package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCleanIsTheInverseOfApply proves clean's core promise: after applying
// one harness into a fresh workspace, clean --harness removes exactly the
// tenon-owned files apply wrote and the record itself, leaving a
// hand-authored file the user placed there untouched.
func TestCleanIsTheInverseOfApply(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(ws, "CLAUDE.md")); err != nil {
		t.Fatalf("apply must have written CLAUDE.md: %v", err)
	}

	handAuthored := filepath.Join(ws, "notes.txt")
	if err := os.WriteFile(handAuthored, []byte("my own notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws, "--harness", "claude"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("clean exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "removed CLAUDE.md") {
		t.Fatalf("clean must report the removed file: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(ws, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("CLAUDE.md must be gone after clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf(".mcp.json must be gone after clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".tenon")); !os.IsNotExist(err) {
		t.Fatalf(".tenon must be gone once its only record is cleaned: %v", err)
	}
	if data, err := os.ReadFile(handAuthored); err != nil || string(data) != "my own notes\n" {
		t.Fatalf("a hand-authored file must survive clean: data=%q err=%v", data, err)
	}
}

// TestCleanScopesToOneHarnessThenBare proves the harness-removal story:
// applying a second harness leaves the first one's files behind until it is
// separately cleaned, and a bare clean (no --harness) then removes
// everything that remains.
func TestCleanScopesToOneHarnessThenBare(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("claude apply exit %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", agent, "--harness", "codex", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("codex apply exit %d: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws, "--harness", "codex"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("clean codex exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(ws, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("codex-owned AGENTS.md must be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".tenon", "apply-codex.json")); !os.IsNotExist(err) {
		t.Fatalf("codex apply record must be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "CLAUDE.md")); err != nil {
		t.Fatalf("claude-owned CLAUDE.md must survive a codex-scoped clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".tenon", "apply-claude.json")); err != nil {
		t.Fatalf("claude apply record must survive a codex-scoped clean: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("bare clean exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(ws, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("claude-owned CLAUDE.md must be gone after bare clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".tenon")); !os.IsNotExist(err) {
		t.Fatalf(".tenon must be gone once every record is cleaned: %v", err)
	}
}

// TestCleanRefusesModifiedFilesUnlessForced proves the all-or-nothing
// refusal: a single owned file modified since apply blocks the whole clean
// (nothing else is removed either), and --force removes it.
func TestCleanRefusesModifiedFilesUnlessForced(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("hand edited after apply\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"clean", "--workspace", ws, "--harness", "claude"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("clean on a modified file must exit 1, got %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "modified since apply: CLAUDE.md") {
		t.Fatalf("clean must report the modified file: %q", stdout.String())
	}
	if data, err := os.ReadFile(filepath.Join(ws, "CLAUDE.md")); err != nil || string(data) != "hand edited after apply\n" {
		t.Fatalf("the modified file must survive a refused clean: data=%q err=%v", data, err)
	}
	// Nothing else is removed either: the record and every other owned
	// file (.mcp.json) must still be present.
	if _, err := os.Stat(filepath.Join(ws, ".mcp.json")); err != nil {
		t.Fatalf("a refused clean must remove nothing at all: .mcp.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".tenon", "apply-claude.json")); err != nil {
		t.Fatalf("a refused clean must leave the record in place: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws, "--harness", "claude", "--force"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("clean --force exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(ws, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("clean --force must remove the modified file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".tenon")); !os.IsNotExist(err) {
		t.Fatalf(".tenon must be gone after clean --force removes the only record: %v", err)
	}
}

// TestCleanOnUnappliedWorkspaceIsANoOp proves the uninstall idempotency
// property: an empty or never-applied workspace succeeds trivially.
func TestCleanOnUnappliedWorkspaceIsANoOp(t *testing.T) {
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"clean", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("clean on an unapplied workspace exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "nothing to clean") {
		t.Fatalf("clean must report nothing to clean: %q", stdout.String())
	}
}

// TestCleanJSONLShapes proves the jsonl-mode ok and blocked shapes: one
// object per removed/blocked path, then a final outcome line.
func TestCleanJSONLShapes(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws, "--harness", "claude", "--diagnostics", "jsonl"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("clean exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least one removed line and a final outcome line: %q", stdout.String())
	}
	var sawClaudeMD bool
	for _, line := range lines[:len(lines)-1] {
		var ev struct {
			Removed string `json:"removed"`
			Harness string `json:"harness"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not valid removed jsonl: %v", line, err)
		}
		if ev.Harness != "claude" {
			t.Fatalf("removed event must name the harness: %q", line)
		}
		if ev.Removed == "CLAUDE.md" {
			sawClaudeMD = true
		}
	}
	if !sawClaudeMD {
		t.Fatalf("expected a removed event naming CLAUDE.md: %q", stdout.String())
	}
	var final struct {
		Outcome string `json:"outcome"`
		Removed int    `json:"removed"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil {
		t.Fatalf("final line is not valid jsonl: %v", err)
	}
	if final.Outcome != "ok" || final.Removed != len(lines)-1 {
		t.Fatalf("final outcome line = %+v, want ok with removed=%d", final, len(lines)-1)
	}

	// Reapply, modify a file, and clean again to exercise the blocked shape.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("reapply exit %d: %s", code, stderr.String())
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("hand edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"clean", "--workspace", ws, "--harness", "claude", "--diagnostics", "jsonl"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("blocked clean must exit 1, got %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	blockedLines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(blockedLines) < 2 {
		t.Fatalf("expected at least one blocked line and a final outcome line: %q", stdout.String())
	}
	var sawBlockedClaudeMD bool
	for _, line := range blockedLines[:len(blockedLines)-1] {
		var ev struct {
			Blocked string `json:"blocked"`
			Reason  string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not valid blocked jsonl: %v", line, err)
		}
		if ev.Blocked == "CLAUDE.md" {
			if ev.Reason != "modified" {
				t.Fatalf("CLAUDE.md must be blocked as modified, got %q", ev.Reason)
			}
			sawBlockedClaudeMD = true
		}
	}
	if !sawBlockedClaudeMD {
		t.Fatalf("expected a blocked event naming CLAUDE.md: %q", stdout.String())
	}
	var finalBlocked struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal([]byte(blockedLines[len(blockedLines)-1]), &finalBlocked); err != nil {
		t.Fatalf("final blocked line is not valid jsonl: %v", err)
	}
	if finalBlocked.Outcome != "blocked" {
		t.Fatalf("final outcome line = %+v, want blocked", finalBlocked)
	}
}
