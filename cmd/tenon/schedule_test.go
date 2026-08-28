package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeScheduleAgent writes an agent with one schedule NAME.md carrying expr.
func writeScheduleAgent(t *testing.T, name, expr string) string {
	t.Helper()
	agent := writeAgent(t, "sched-agent", validInstructions)
	full := filepath.Join(agent, "schedules", filepath.FromSlash(name)+".md")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ncron: \"" + expr + "\"\n---\n\nDo the work.\n"
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return agent
}

func TestScheduleRequiresSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"schedule"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "a subcommand is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestScheduleTriggerRequiresInputID(t *testing.T) {
	agent := writeScheduleAgent(t, "daily", "0 9 * * *")
	var stdout, stderr bytes.Buffer
	code := run([]string{"schedule", "trigger", agent, "daily", "--workspace", t.TempDir(), "--harness", "claude"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--input-id is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestScheduleTriggerUnknownScheduleFails(t *testing.T) {
	agent := writeScheduleAgent(t, "daily", "0 9 * * *")
	var stdout, stderr bytes.Buffer
	code := run([]string{"schedule", "trigger", agent, "missing", "--workspace", t.TempDir(), "--harness", "claude", "--input-id", "occ-1"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no schedule named") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestScheduleTriggerFailsClosedOnStaleSetup(t *testing.T) {
	agent := writeScheduleAgent(t, "daily", "0 9 * * *")
	// A workspace that was never applied is stale: trigger must fail closed
	// before opening any harness.
	var stdout, stderr bytes.Buffer
	code := run([]string{"schedule", "trigger", agent, "daily", "--workspace", t.TempDir(), "--harness", "claude", "--input-id", "occ-1"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "run tenon apply") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestScheduleRunRejectsMaxActiveOutOfRange(t *testing.T) {
	agent := writeScheduleAgent(t, "daily", "0 9 * * *")
	var stdout, stderr bytes.Buffer
	code := run([]string{"schedule", "run", agent, "--workspace", t.TempDir(), "--harness", "claude", "--max-active-turns", "0"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--max-active-turns must be between") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
