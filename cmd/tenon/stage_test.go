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

// TestStageRefusesPythonAndTypeScriptButOtherCommandsStillWork proves ADR
// 0021's staging refusal end to end through the CLI: `tenon stage` reports
// the stable stage.tools.runtime-unsupported diagnostic and writes no output
// directory for a Python- or TypeScript-bearing agent, while `tenon
// validate` and `tenon apply` keep working locally for the very same agent —
// only staging refuses.
func TestStageRefusesPythonAndTypeScriptButOtherCommandsStillWork(t *testing.T) {
	cases := []struct {
		name     string
		language string
		setup    func(t *testing.T, agent string)
	}{
		{
			name:     "python",
			language: "Python",
			setup: func(t *testing.T, agent string) {
				writeFile(t, agent, "pyproject.toml", []byte("[project]\nname = \"x\"\n"), 0o644)
				writeFile(t, agent, "uv.lock", []byte(""), 0o644)
				writeFile(t, agent, "tools/count_words.py", []byte(pythonToolFile), 0o644)
			},
		},
		{
			name:     "typescript",
			language: "TypeScript",
			setup: func(t *testing.T, agent string) {
				writeFile(t, agent, "deno.json", []byte("{}\n"), 0o644)
				writeFile(t, agent, "deno.lock", []byte(`{"version":"4"}`+"\n"), 0o644)
				writeFile(t, agent, "tools/shout_text.ts", []byte(typescriptToolFile), 0o644)
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agent := writeAgent(t, c.name+"-tool-agent", validInstructions)
			c.setup(t, agent)
			out := filepath.Join(t.TempDir(), "staged")

			var stdout, stderr bytes.Buffer
			code := run([]string{"stage", agent, "--harness", "claude", "--output", out, "--diagnostics", "jsonl"},
				nil, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("staging a %s-bearing agent must fail: %q", c.language, stdout.String())
			}
			got := filterDiags(parseDiagLines(t, stdout.String()), "stage.tools.runtime-unsupported")
			if len(got) != 1 {
				t.Fatalf("expected exactly one stage.tools.runtime-unsupported, got %q", stdout.String())
			}
			if !strings.Contains(got[0].Rule, c.language) {
				t.Fatalf("the diagnostic must name %s: %q", c.language, got[0].Rule)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("a refused stage must leave no output directory")
			}

			// Only staging refuses: validate never carries staging's own
			// diagnostic for the identical agent, whatever it otherwise
			// reports (a missing local deno/uv toolchain is this
			// environment's business, not staging's refusal).
			stdout.Reset()
			stderr.Reset()
			run([]string{"validate", agent, "--harness", "claude", "--diagnostics", "jsonl"}, nil, &stdout, &stderr)
			if got := filterDiags(parseDiagLines(t, stdout.String()), "stage.tools.runtime-unsupported"); len(got) != 0 {
				t.Fatalf("validate must never report stage's own diagnostic: %v", got)
			}
		})
	}
}
