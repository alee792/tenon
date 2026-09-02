package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/apply"
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
	if code := run([]string{"clean", "--workspace", ws, "--harness", "claude", "--format", "jsonl"}, nil, &stdout, &stderr); code != 0 {
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
	code := run([]string{"clean", "--workspace", ws, "--harness", "claude", "--format", "jsonl"}, nil, &stdout, &stderr)
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

// blockedLines parses a jsonl clean stream into its blocked events and
// asserts the stream ends with the blocked outcome object.
func blockedLines(t *testing.T, out string) map[string]string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != `{"outcome":"blocked"}` {
		t.Fatalf("a refused clean must end with the blocked outcome object: %q", out)
	}
	blocked := map[string]string{}
	for _, line := range lines[:len(lines)-1] {
		var ev struct {
			Blocked string `json:"blocked"`
			Reason  string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not valid blocked jsonl: %v", line, err)
		}
		if ev.Blocked != "" {
			blocked[ev.Blocked] = ev.Reason
		}
	}
	return blocked
}

// TestCleanRefusesPathsReachedThroughASymlinkedParent proves clean never
// follows a symlinked parent directory out of the workspace: the record
// names an ordinary workspace-relative path and the leaf at the end of it is
// byte-identical to what apply wrote, so leaf-only classification would
// happily remove it — through the symlink, in a directory the workspace does
// not contain. The whole clean is refused, and nothing anywhere is removed.
func TestCleanRefusesPathsReachedThroughASymlinkedParent(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeFile(t, agent, "skills/echo/SKILL.md", []byte(echoSkillMD), 0o644)
	ws := t.TempDir()
	outside := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	// The directory the record's paths run through is moved out of the
	// workspace and replaced by a symlink to where it now lives: every
	// recorded path still resolves, and every one of them resolves outside.
	stolen := filepath.Join(outside, "claude")
	if err := os.Rename(filepath.Join(ws, ".claude"), stolen); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stolen, filepath.Join(ws, ".claude")); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(stolen, "skills", "echo", "SKILL.md")
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the outside file must exist before the clean: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws, "--harness", "claude", "--format", "jsonl"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("clean through a symlinked parent must be refused, got exit %d\nstdout: %s", code, stdout.String())
	}
	blocked := blockedLines(t, stdout.String())
	if blocked[".claude/skills/echo/SKILL.md"] != "symlink-parent" {
		t.Fatalf("the symlinked-parent path must be blocked as such: %v", blocked)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("nothing outside the workspace may be touched: %v", err)
	}
	// All-or-nothing: the paths that classified clean are still there too.
	if _, err := os.Stat(filepath.Join(ws, "CLAUDE.md")); err != nil {
		t.Fatalf("a refused clean removes nothing at all: %v", err)
	}
	if _, err := os.Stat(apply.RecordPath(ws, "claude")); err != nil {
		t.Fatalf("a refused clean keeps the record: %v", err)
	}
}

// TestCleanRefusesRecordedPathsThatEscapeTheWorkspace proves a corrupted or
// hand-edited record cannot aim clean at a file the workspace does not
// contain: a "../" entry is refused outright, the file it names survives, and
// the workspace's own parent is never a removal candidate.
func TestCleanRefusesRecordedPathsThatEscapeTheWorkspace(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	parent := t.TempDir()
	ws := filepath.Join(parent, "workspace")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	victimDir := filepath.Join(parent, "victim")
	if err := os.Mkdir(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(victimDir, "secret.txt")
	secretContent := []byte("do not delete me\n")
	if err := os.WriteFile(secret, secretContent, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	addRecordedPath(t, ws, "claude", "../victim/secret.txt", secretContent)

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws, "--harness", "claude", "--format", "jsonl"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("a record path escaping the workspace must be refused, got exit %d\nstdout: %s", code, stdout.String())
	}
	if blocked := blockedLines(t, stdout.String()); blocked["../victim/secret.txt"] != "escapes-workspace" {
		t.Fatalf("the escaping path must be blocked as such: %v", blocked)
	}
	if got, err := os.ReadFile(secret); err != nil || string(got) != string(secretContent) {
		t.Fatalf("the file outside the workspace must be untouched: got %q err %v", got, err)
	}
	if _, err := os.Stat(victimDir); err != nil {
		t.Fatalf("the directory outside the workspace must be untouched: %v", err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("the workspace's parent must never be a removal candidate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "CLAUDE.md")); err != nil {
		t.Fatalf("a refused clean removes nothing at all: %v", err)
	}
}

// TestCleanPrunesEmptyParentsButNeverTheWorkspace proves the pruning bound:
// the directories a removal empties are pruned, and the walk stops at the
// workspace itself rather than climbing past it.
func TestCleanPrunesEmptyParentsButNeverTheWorkspace(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeFile(t, agent, "skills/echo/SKILL.md", []byte(echoSkillMD), 0o644)
	parent := t.TempDir()
	ws := filepath.Join(parent, "workspace")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws, "--harness", "claude"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("clean exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(ws, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("the emptied directory must be pruned: %v", err)
	}
	info, err := os.Stat(ws)
	if err != nil || !info.IsDir() {
		t.Fatalf("the workspace itself must survive the prune: %v", err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("the workspace's parent must survive the prune: %v", err)
	}
}

// TestCleanBlockedInOneHarnessRemovesNothingAnywhere proves the
// all-or-nothing refusal spans harnesses: one blocked path in one record
// refuses the whole clean, including the harness that classified entirely
// clean.
func TestCleanBlockedInOneHarnessRemovesNothingAnywhere(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	for _, harness := range []string{"claude", "codex"} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{"apply", agent, "--harness", harness, "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("apply %s exit %d: %s", harness, code, stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("hand edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws, "--format", "jsonl"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("a blocked path must refuse the whole clean, got exit %d\nstdout: %s", code, stdout.String())
	}
	if blocked := blockedLines(t, stdout.String()); blocked["CLAUDE.md"] != "modified" {
		t.Fatalf("the modified path must be blocked: %v", blocked)
	}
	if _, err := os.Stat(filepath.Join(ws, "AGENTS.md")); err != nil {
		t.Fatalf("the other harness's files must survive a refused clean: %v", err)
	}
	for _, harness := range []string{"claude", "codex"} {
		if _, err := os.Stat(apply.RecordPath(ws, harness)); err != nil {
			t.Fatalf("a refused clean keeps every record: %v", err)
		}
	}
}

// TestCleanDropsARecordOwningNoFiles proves clean's promise to drop the
// record is not conditional on there being files to remove: a record whose
// owned set is empty is removed, and .tenon with it.
func TestCleanDropsARecordOwningNoFiles(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	emptyRecordFiles(t, ws, "claude")

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws, "--harness", "claude"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("clean exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(apply.RecordPath(ws, "claude")); !os.IsNotExist(err) {
		t.Fatalf("a record owning nothing must still be dropped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".tenon")); !os.IsNotExist(err) {
		t.Fatalf(".tenon must go with its last record: %v", err)
	}
}

// TestCleanIgnoresRecordsNamingAnUnknownHarness proves a file in .tenon that
// merely looks like a record is reported and left alone rather than driving
// removals: --harness is validated strictly, and a discovered name gets the
// same standard.
func TestCleanIgnoresRecordsNamingAnUnknownHarness(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	strayContent := []byte("not a real record\n")
	stray := filepath.Join(ws, ".tenon", "apply-bogus.json")
	if err := os.WriteFile(stray, strayContent, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"clean", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("clean exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "ignoring unrecognized record: .tenon/apply-bogus.json") {
		t.Fatalf("clean must say which record it ignored: %q", stdout.String())
	}
	if got, err := os.ReadFile(stray); err != nil || string(got) != string(strayContent) {
		t.Fatalf("the unrecognized file must be left alone: got %q err %v", got, err)
	}
}

// TestRemovableNowRefusesWhatChangedUnderneath proves the re-check clean runs
// immediately before each removal — the guard against a workspace that
// changes between the plan pass and the removal pass, which no in-process
// test can interleave for real.
func TestRemovableNowRefusesWhatChangedUnderneath(t *testing.T) {
	ws := t.TempDir()
	content := []byte("generated\n")
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "sub", "a.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	record := &apply.Record{Files: map[string]apply.OwnedFile{
		"sub/a.txt":            {Hash: hashForTest(content)},
		"../victim/secret.txt": {Hash: hashForTest(content)},
	}}

	if reason := removableNow(ws, "sub/a.txt", record, false); reason != "" {
		t.Fatalf("an unchanged owned file is removable, got %q", reason)
	}
	if reason := removableNow(ws, "sub/missing.txt", record, false); reason != "" {
		t.Fatalf("an already-removed file is not a block, got %q", reason)
	}
	if reason := removableNow(ws, "../victim/secret.txt", record, false); reason != "escapes-workspace" {
		t.Fatalf("an escaping path must block, got %q", reason)
	}
	if err := os.WriteFile(filepath.Join(ws, "sub", "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if reason := removableNow(ws, "sub/a.txt", record, false); reason != "modified" {
		t.Fatalf("a file changed underneath must block, got %q", reason)
	}
	if reason := removableNow(ws, "sub/a.txt", record, true); reason != "" {
		t.Fatalf("--force widens exactly the modified refusal, got %q", reason)
	}
	// Containment is not force-overridable: --force widens what tenon removes
	// inside the workspace, never where it removes.
	if reason := removableNow(ws, "../victim/secret.txt", record, true); reason != "escapes-workspace" {
		t.Fatalf("--force must not override containment, got %q", reason)
	}
}

// hashForTest hashes content the way the apply record does.
func hashForTest(content []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(content))
}

// addRecordedPath rewrites the workspace's apply record to also own path,
// hashed as content — the corrupted or hand-edited record clean must refuse
// to act on verbatim.
func addRecordedPath(t *testing.T, ws, harness, path string, content []byte) {
	t.Helper()
	recordPath := apply.RecordPath(ws, harness)
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	files, _ := record["files"].(map[string]any)
	if files == nil {
		t.Fatalf("record has no files map: %s", raw)
	}
	files[path] = map[string]any{"hash": hashForTest(content), "executable": false}
	rewritten, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, append(rewritten, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// emptyRecordFiles rewrites the workspace's apply record to own no files at
// all, the state a clean of a record whose files were all removed leaves.
func emptyRecordFiles(t *testing.T, ws, harness string) {
	t.Helper()
	recordPath := apply.RecordPath(ws, harness)
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record["files"] = map[string]any{}
	rewritten, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, append(rewritten, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
