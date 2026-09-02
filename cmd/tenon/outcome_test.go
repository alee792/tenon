package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// finalOutcome decodes the last object of a jsonl stream — the one every
// command's stream is promised to end with — and fails when the stream ended
// with nothing at all, which is the exact absence the outcome contract
// exists to abolish.
func finalOutcome(t *testing.T, stream string) struct {
	Outcome      string `json:"outcome"`
	Error        string `json:"error"`
	SourceDigest string `json:"source_digest"`
} {
	t.Helper()
	var final struct {
		Outcome      string `json:"outcome"`
		Error        string `json:"error"`
		SourceDigest string `json:"source_digest"`
	}
	lines := strings.Split(strings.TrimSpace(stream), "\n")
	last := lines[len(lines)-1]
	if strings.TrimSpace(last) == "" {
		t.Fatalf("the jsonl stream ended with nothing; a consumer cannot tell that from a truncated pipe")
	}
	if err := json.Unmarshal([]byte(last), &final); err != nil {
		t.Fatalf("the stream's last line must be one JSON object, got %q: %v", last, err)
	}
	return final
}

// TestEnvironmentFailureEndsTheStreamWithAnError proves the outcome contract
// covers environment failures, not only findings: a run that cannot complete
// for a reason that is not the source's fault ends the jsonl stream with
// {"outcome":"error"} and its own prose, so a consumer reading objects until
// end of stream never has to infer failure from silence. The error outcome
// is deliberately distinct from gate_failed, drift, and blocked: those three
// are findings a loop scores, while an error is a statement about the
// environment that a loop retries or escalates instead.
func TestEnvironmentFailureEndsTheStreamWithAnError(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	ws := t.TempDir()
	missingPins := filepath.Join(t.TempDir(), "no-such-pins.json")

	cases := []struct {
		name string
		args []string
		// want is a fragment of the prose the error object must carry, so
		// the test proves the reason travels in the stream and not only on
		// stderr.
		want string
	}{
		{
			name: "check with an unreadable pin set",
			args: []string{"check", agent, "--harness", "claude", "--pins", missingPins, "--format", "jsonl"},
			want: "no-such-pins.json",
		},
		{
			name: "apply with an unreadable pin set",
			args: []string{"apply", agent, "--harness", "claude", "--workspace", ws, "--pins", missingPins, "--format", "jsonl"},
			want: "no-such-pins.json",
		},
		{
			name: "drift with an unreadable pin set",
			args: []string{"drift", agent, "--harness", "claude", "--workspace", ws, "--pins", missingPins, "--format", "jsonl"},
			want: "no-such-pins.json",
		},
		{
			// An unwritable pin path is the environment failure that happens
			// AFTER the gate has passed: the source is fine, the run still
			// could not finish, and calling that gate_failed would blame the
			// source for the filesystem.
			name: "check writing pins into a directory that does not exist",
			args: []string{"check", agent, "--harness", "claude", "--write-pins", filepath.Join(t.TempDir(), "missing-dir", "pins.json"), "--format", "jsonl"},
			want: "missing-dir",
		},
		{
			// clean's own environment failure: a --workspace that is not a
			// directory is neither blocked (nothing was refused) nor a gate
			// failure (clean has no source at all).
			name: "clean with a workspace that is not a directory",
			args: []string{"clean", "--workspace", filepath.Join(agent, "instructions.md"), "--format", "jsonl"},
			want: "instructions.md",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tc.args, nil, &stdout, &stderr); code != 1 {
				t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
			}
			final := finalOutcome(t, stdout.String())
			if final.Outcome != "error" {
				t.Fatalf("outcome = %q, want error\nstdout: %s", final.Outcome, stdout.String())
			}
			if !strings.Contains(final.Error, tc.want) {
				t.Fatalf("the error outcome must carry the reason (%q), got %q", tc.want, final.Error)
			}
			// The prose stays exactly where it always was.
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("the prose must still be on stderr, got %q", stderr.String())
			}
		})
	}
}

// TestUsageErrorsEmitNoOutcome proves the one deliberate hole in the outcome
// contract: a usage error exits 2 and emits nothing at all. A malformed
// invocation never ran, so there is no outcome to report, and inventing one
// would tell a consumer a run happened.
func TestUsageErrorsEmitNoOutcome(t *testing.T) {
	// --pins without --harness is a usage error only when no harness is
	// supplied at all, and TENON_HARNESS supplies one: the suite must not
	// inherit the operator's shell for a case that is about the absence of a
	// harness.
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", validInstructions)
	cases := [][]string{
		{"check", agent, "--harness", "gpt", "--format", "jsonl"},
		{"check", agent, "--pins", "some.json", "--format", "jsonl"},
		{"clean", "--format", "jsonl"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := run(args, nil, &stdout, &stderr); code != 2 {
			t.Fatalf("%v exit = %d, want 2\nstderr: %s", args, code, stderr.String())
		}
		if stdout.String() != "" {
			t.Fatalf("%v must emit no outcome object on a usage error, got %q", args, stdout.String())
		}
	}
}
