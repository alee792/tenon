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

// TestStageUnderGithubStyledPathStagesCleanly proves the build-machine-path
// scan does not false-positive on an entirely ordinary agent location: a
// path under a "github.com/<owner>/" directory, the shape any agent
// checked out from a real GitHub clone naturally has. It once refused to
// stage such an agent: the copied tenon executable's own embedded module
// data legitimately carries "github.com" and its own module's owner
// segment (tenon's own module path is github.com/alee792/tenon, and this
// test binary — copied into the tree by resolveExecutable exactly as the
// real tenon executable would be — carries that same identity), and the
// scan's bare-component matching could not tell that apart from a real
// leak of the agent's own directory ancestry.
func TestStageUnderGithubStyledPathStagesCleanly(t *testing.T) {
	agent := filepath.Join(t.TempDir(), "github.com", "someowner", "leaky-agent")
	writeFile(t, agent, "instructions.md", []byte(validInstructions), 0o644)
	writeFile(t, agent, "go.mod", []byte("module example.com/leaky-agent\n\ngo 1.24\n"), 0o644)
	writeFile(t, agent, "tools/hash_text/tool.go", []byte(goToolFile), 0o644)

	out := filepath.Join(t.TempDir(), "staged")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"stage", agent, "--harness", "claude", "--output", out, "--format", "jsonl"},
		nil, &stdout, &stderr); code != 0 {
		t.Fatalf("stage exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if got := filterDiags(parseDiagLines(t, stdout.String()), "stage.tree.build-path-leaked"); len(got) != 0 {
		t.Fatalf("staging under a github.com/-styled path must not false-positive: %v", got)
	}

	artifact := filepath.Join(out, "opt", "tenon", "artifact.json")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"stage", "verify", "--artifact", artifact, "--prefix", out}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("a staged tree under a github.com/-styled path must verify: exit %d\nstderr: %s", code, stderr.String())
	}
}

// TestStageUnderGithubStyledPathStagesCleanlyWithAPythonClosure is the same
// false-positive proof as TestStageUnderGithubStyledPathStagesCleanly, but
// with a Python closure present. It once produced 211
// stage.tree.build-path-leaked diagnostics from this exact fixture shape:
// the build-machine-path scan routed every file by whether its own bytes
// looked binary, and CPython's standalone interpreter ships roughly four
// thousand ordinary TEXT files (its stdlib and C headers) free to carry
// short, ordinary-looking tokens — "github", "runner" (GitHub Actions'
// own default /home/runner/work path), "project", the agent's own
// directory ancestry — that collide with a real agent's path components by
// pure coincidence, exactly the class of false positive component matching
// exists to avoid for a compiled binary's data. Provenance, not
// looks-binary, is what must route a carried-in payload tree to joined
// matching (see carriedPayload in internal/stage), and this proves the fix
// against the real interpreter tree, not a synthetic stand-in.
func TestStageUnderGithubStyledPathStagesCleanlyWithAPythonClosure(t *testing.T) {
	uv := requireToolchain(t, "uv")
	agent := filepath.Join(t.TempDir(), "github.com", "someowner", "py-agent")
	writeFile(t, agent, "instructions.md", []byte(validInstructions), 0o644)
	writePythonTool(t, agent)
	lockDependencies(t, agent, uv, "lock")

	out := filepath.Join(t.TempDir(), "staged")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"stage", agent, "--harness", "claude", "--output", out, "--format", "jsonl"},
		nil, &stdout, &stderr); code != 0 {
		t.Fatalf("stage exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if got := filterDiags(parseDiagLines(t, stdout.String()), "stage.tree.build-path-leaked"); len(got) != 0 {
		t.Fatalf("staging a python closure under a github.com/-styled path must not false-positive (got %d): %v",
			len(got), got)
	}

	artifact := filepath.Join(out, "opt", "tenon", "artifact.json")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"stage", "verify", "--artifact", artifact, "--prefix", out}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("a staged python-closure tree under a github.com/-styled path must verify: exit %d\nstderr: %s", code, stderr.String())
	}
}

// TestStageUnderGithubStyledPathStagesCleanlyWithATypeScriptClosure is the
// same false-positive proof as
// TestStageUnderGithubStyledPathStagesCleanlyWithAPythonClosure, but with a
// TypeScript closure present: the copied deno executable and the pruned,
// cached-only DENO_DIR (downloaded npm package sources — ordinary text
// free to carry short tokens like "github" or the agent's own directory
// ancestry) must be routed to joined-path matching by carriedPayload, the
// same provenance-based routing the Python closure already needed, not
// left to component matching to false-positive on.
func TestStageUnderGithubStyledPathStagesCleanlyWithATypeScriptClosure(t *testing.T) {
	deno := requireToolchain(t, "deno")
	agent := filepath.Join(t.TempDir(), "github.com", "someowner", "ts-agent")
	writeFile(t, agent, "instructions.md", []byte(validInstructions), 0o644)
	writeTypeScriptTool(t, agent)
	lockDependencies(t, agent, deno, "install", "--entrypoint", "tools/shout_text.ts")

	out := filepath.Join(t.TempDir(), "staged")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"stage", agent, "--harness", "claude", "--output", out, "--format", "jsonl"},
		nil, &stdout, &stderr); code != 0 {
		t.Fatalf("stage exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if got := filterDiags(parseDiagLines(t, stdout.String()), "stage.tree.build-path-leaked"); len(got) != 0 {
		t.Fatalf("staging a typescript closure under a github.com/-styled path must not false-positive (got %d): %v",
			len(got), got)
	}

	artifact := filepath.Join(out, "opt", "tenon", "artifact.json")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"stage", "verify", "--artifact", artifact, "--prefix", out}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("a staged typescript-closure tree under a github.com/-styled path must verify: exit %d\nstderr: %s", code, stderr.String())
	}
}
