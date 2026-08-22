package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStageCLIJourney stages a tool-free agent through the CLI, then verifies
// the published tree with the same verification the staged entrypoint invokes,
// and proves that verification fails closed once a staged file is corrupted.
func TestStageCLIJourney(t *testing.T) {
	agent := writeAgent(t, "cli-stage-agent", validInstructions)
	out := filepath.Join(t.TempDir(), "staged")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"stage", agent, "--harness", "codex", "--output", out}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("stage exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	artifact := filepath.Join(out, "opt", "tenon", "artifact.json")
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("artifact.json must be published: %v", err)
	}
	for _, rel := range []string{
		filepath.Join("opt", "tenon", "bin", "tenon"),
		filepath.Join("opt", "tenon", "bin", "agent-entrypoint"),
		filepath.Join("workspace", "AGENTS.md"),
		filepath.Join("workspace", ".codex", "config.toml"),
		filepath.Join("opt", "tenon", "agents", "cli-stage-agent", "instructions.md"),
		filepath.Join("home", "tenon"),
	} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Fatalf("staged tree missing %s: %v", rel, err)
		}
	}

	// The verification the entrypoint calls passes on a clean tree.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"stage", "verify", "--artifact", artifact, "--prefix", out}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("verify of a clean tree exit %d\nstderr: %s", code, stderr.String())
	}

	// Refuse to stage into an existing directory.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"stage", agent, "--harness", "codex", "--output", out}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("staging into an existing directory must fail")
	}

	// Corrupt a staged file: verification must fail closed.
	config := filepath.Join(out, "workspace", ".codex", "config.toml")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, append(data, []byte("\n# tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"stage", "verify", "--artifact", artifact, "--prefix", out}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("verification must fail closed after tampering")
	}
	if !strings.Contains(stderr.String(), "stage verify") {
		t.Fatalf("verify failure must be reported on stderr: %s", stderr.String())
	}
}
