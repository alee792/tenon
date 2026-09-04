package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acpharness "github.com/alee792/tenon/internal/harness/acp"
)

// TestMain lets this test binary serve as a fake ACP agent when a test points
// --acp-command at it, so run and schedule are proven end to end over the real
// wire path without a model.
func TestMain(m *testing.M) {
	if opts, ok := acpharness.FakeFromEnv(); ok {
		if err := acpharness.RunFake(os.Stdin, os.Stdout, os.Stderr, opts); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// useFakeACPAgent scripts the fake agent through the environment the driver's
// subprocess inherits. Tests using it must not run in parallel.
func useFakeACPAgent(t *testing.T, env map[string]string) {
	t.Helper()
	t.Setenv(acpharness.FakeEnv, "1")
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// TestDriverFlagValidation proves the driver flags are checked before any
// work, on every dispatching command.
func TestDriverFlagValidation(t *testing.T) {
	agent := writeScheduleAgent(t, "daily", "0 9 * * *")
	ws := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"run bad driver", []string{"run", agent, "--workspace", ws, "--harness", "claude", "--driver", "sdk"}},
		{"run acp-command without acp", []string{"run", agent, "--workspace", ws, "--harness", "claude", "--acp-command", "x"}},
		{"run permissions without acp", []string{"run", agent, "--workspace", ws, "--harness", "claude", "--permissions", "allow"}},
		{"run empty permissions", []string{"run", agent, "--workspace", ws, "--harness", "claude", "--driver", "acp", "--permissions", ""}},
		{"trigger bad driver", []string{"schedule", "trigger", agent, "daily", "--workspace", ws, "--harness", "claude", "--input-id", "o1", "--driver", "sdk"}},
		{"schedule run bad driver", []string{"schedule", "run", agent, "--workspace", ws, "--harness", "claude", "--driver", "sdk"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, nil, &stdout, &stderr); code != 2 {
				t.Fatalf("exit = %d, want 2\nstderr: %s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "--driver") && !strings.Contains(stderr.String(), "--permissions") && !strings.Contains(stderr.String(), "--acp-command") {
				t.Fatalf("stderr must name the flag: %s", stderr.String())
			}
		})
	}
}

// TestRunACPMissingPolicyFileIsAnError proves a --permissions path that does
// not read ends the stream with a run.completed error naming the policy,
// before any agent is launched.
func TestRunACPMissingPolicyFileIsAnError(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", agent, "--workspace", ws, "--harness", "claude", "--driver", "acp", "--permissions", filepath.Join(ws, "missing.json")}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", code, stderr.String())
	}
	last := lastRunEvent(t, stdout.String())
	if last.Type != "run.completed" || last.Outcome != "error" || !strings.Contains(last.Error, "permissions policy") {
		t.Fatalf("terminator = %+v", last)
	}
}

// applyClaude applies agent into a fresh workspace and returns it.
func applyClaude(t *testing.T, agent string) string {
	t.Helper()
	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d\nstderr: %s", code, stderr.String())
	}
	return ws
}

// TestRunACPDispatchesATurn proves the whole journey under --driver acp: an
// applied workspace, one JSONL input, the fake agent launched from
// --acp-command, and the ordinary wire stream — session started, output
// deltas, a completed turn, and a run.completed ok — with a policy file
// answering the agent's permission request.
func TestRunACPDispatchesATurn(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := applyClaude(t, agent)
	policy := filepath.Join(t.TempDir(), "permissions.json")
	if err := os.WriteFile(policy, []byte(`{"default":"deny","rules":[{"kind":"execute","title":"git *","action":"allow"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	useFakeACPAgent(t, map[string]string{
		acpharness.FakeEnvPermission: `{"toolCallId":"t1","kind":"execute","title":"git status","status":"pending"}`,
	})

	stdin := strings.NewReader(`{"input_id":"x-1","text":"hello"}` + "\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", agent, "--workspace", ws, "--harness", "claude", "--driver", "acp", "--acp-command", os.Args[0], "--permissions", policy}, stdin, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit = %d\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	var types []string
	var text strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var e struct {
			Type      string `json:"type"`
			Harness   string `json:"harness"`
			SessionID string `json:"session_id"`
			Delta     string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad wire line %q: %v", line, err)
		}
		if e.Harness != "claude" {
			t.Fatalf("every event carries the applied harness, got %+v", e)
		}
		types = append(types, e.Type)
		text.WriteString(e.Delta)
		if e.Type == "session.started" && e.SessionID != "fake-session" {
			t.Fatalf("session id = %q", e.SessionID)
		}
	}
	joined := strings.Join(types, " ")
	for _, want := range []string{"input.accepted", "session.started", "agent.output.delta", "turn.completed", "run.completed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stream lacks %s: %s", want, joined)
		}
	}
	if !strings.Contains(text.String(), "hello ") || !strings.Contains(text.String(), "decision:allow-once") || !strings.Contains(text.String(), "world") {
		t.Fatalf("deltas = %q", text.String())
	}
	if strings.Contains(stdout.String(), "SECRET-STDERR-LOG") || strings.Contains(stderr.String(), "SECRET-STDERR-LOG") {
		t.Fatal("the agent's stderr must be swallowed")
	}
	last := lastRunEvent(t, stdout.String())
	if last.Outcome != "ok" || last.Turns == nil || last.Turns.Completed != 1 {
		t.Fatalf("terminator = %+v", last)
	}
}

// TestScheduleTriggerACPRunsFresh proves a schedule occurrence dispatches
// through the acp driver as a fresh task session and reports completion.
func TestScheduleTriggerACPRunsFresh(t *testing.T) {
	agent := writeScheduleAgent(t, "daily", "0 9 * * *")
	ws := applyClaude(t, agent)
	useFakeACPAgent(t, nil)
	var stdout, stderr bytes.Buffer
	code := run([]string{"schedule", "trigger", agent, "daily", "--workspace", ws, "--harness", "claude", "--input-id", "occ-1", "--driver", "acp", "--acp-command", os.Args[0], "--permissions", "allow"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("trigger exit = %d\nstderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "completed") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
