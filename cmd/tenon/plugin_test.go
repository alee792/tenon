package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/pluginref"
)

// isolatedPluginCache points every plugin-reference cache resolution at a
// fresh per-test directory (via XDG_STATE_HOME, which both
// internal/pluginref.DefaultBase and internal/integration.DefaultBase key
// off on non-darwin), and returns the resolved base for direct pre-seeding.
func isolatedPluginCache(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base, err := pluginref.DefaultBase()
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func writePluginReferenceFile(t *testing.T, agent, name, source, rev string) {
	t.Helper()
	dir := filepath.Join(agent, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nsource: " + source + "\nrev: " + rev + "\n---\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newLocalGitFixture creates a local git repo with one commit and returns
// its path and commit SHA, for seeding the plugin cache directly (bypassing
// the CLI's https-only source grammar, which local fixtures never satisfy).
func newLocalGitFixture(t *testing.T, files map[string]string) (repo, rev string) {
	t.Helper()
	repo = t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=tenon-test", "GIT_AUTHOR_EMAIL=tenon-test@example.invalid",
		"GIT_COMMITTER_NAME=tenon-test", "GIT_COMMITTER_EMAIL=tenon-test@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	for path, content := range files {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-m", "fixture")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return repo, strings.TrimSpace(string(out))
}

func validPluginTreeFiles(name string) map[string]string {
	return map[string]string{
		"plugin.json":           `{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "` + name + `"}`,
		"skills/greet/SKILL.md": "---\nname: greet\ndescription: Greets.\n---\n\nSay hello.\n",
	}
}

// TestPluginFetchRejectsNonHTTPSSource proves the closed authored grammar:
// fetch never even reaches the cache for a reference whose declared source
// is not an absolute HTTPS URL.
func TestPluginFetchRejectsNonHTTPSSource(t *testing.T) {
	isolatedPluginCache(t)
	agent := writeAgent(t, "agent", validInstructions)
	rev := strings.Repeat("a", 40)
	writePluginReferenceFile(t, agent, "obs", "git@github.com:acme/observability-plugin.git", rev)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"plugin", "fetch", agent}, nil, &stdout, &stderr); code == 0 {
		t.Fatalf("expected a nonzero exit for a non-https source; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

// TestApplyFailsClosedOnUnresolvedPluginReference proves apply and validate
// stay fully offline: an uncached reference fails, naming `tenon plugin
// fetch`, before any generation.
func TestApplyFailsClosedOnUnresolvedPluginReference(t *testing.T) {
	isolatedPluginCache(t)
	agent := writeAgent(t, "agent", validInstructions)
	rev := strings.Repeat("a", 40)
	writePluginReferenceFile(t, agent, "obs", "https://github.com/acme/observability-plugin", rev)

	var stdout, stderr bytes.Buffer
	code := run([]string{"validate", agent, "--harness", "claude"}, nil, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected validate to fail on an unresolved plugin reference")
	}
	if !strings.Contains(stderr.String(), "tenon plugin fetch") {
		t.Fatalf("expected the failure to name `tenon plugin fetch`, got: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", agent, "--harness", "claude"}, nil, &stdout, &stderr); code == 0 {
		t.Fatalf("expected apply to fail on an unresolved plugin reference")
	}
	if _, err := os.Stat(filepath.Join(agent, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("apply must not mutate the workspace when a plugin reference is unresolved")
	}
}

// TestPluginFetchStatusUpdateHappyPath pre-seeds the plugin cache directly
// (a local git fixture, since the CLI's source grammar is https-only and no
// test may reach a real network) at two revisions, then exercises fetch's
// already-cached idempotence, status's resolved report, and update's
// diff-then-rewrite, entirely offline.
func TestPluginFetchStatusUpdateHappyPath(t *testing.T) {
	base := isolatedPluginCache(t)
	repo, oldRev := newLocalGitFixture(t, validPluginTreeFiles("observability"))
	if err := os.WriteFile(filepath.Join(repo, "skills", "greet", "SKILL.md"),
		[]byte("---\nname: greet\ndescription: Greets more.\n---\n\nSay hello warmly.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "commit", "-am", "second")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=tenon-test", "GIT_AUTHOR_EMAIL=tenon-test@example.invalid",
		"GIT_COMMITTER_NAME=tenon-test", "GIT_COMMITTER_EMAIL=tenon-test@example.invalid", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	newRev := strings.TrimSpace(string(out))

	cache := pluginref.NewCache(base)
	if _, err := cache.Fetch(repo, oldRev); err != nil {
		t.Fatalf("seeding old rev: %v", err)
	}
	if _, err := cache.Fetch(repo, newRev); err != nil {
		t.Fatalf("seeding new rev: %v", err)
	}

	agent := writeAgent(t, "agent", validInstructions)
	writePluginReferenceFile(t, agent, "obs", "https://github.com/acme/observability-plugin", oldRev)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"plugin", "fetch", agent}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("plugin fetch failed: %d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "already-cached") {
		t.Fatalf("expected an already-cached report, got: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plugin", "status", agent}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("plugin status failed: %d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "resolved") {
		t.Fatalf("expected a resolved status line, got: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plugin", "update", agent, "obs", "--rev", newRev}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("plugin update failed: %d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "~ skills/greet/SKILL.md") {
		t.Fatalf("expected the diff to report the changed skill file, got: %s", stdout.String())
	}
	rewritten, err := os.ReadFile(filepath.Join(agent, "plugins", "obs.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rewritten), "rev: "+newRev) {
		t.Fatalf("expected the reference file to be rewritten to the new rev, got: %s", rewritten)
	}
	if !strings.Contains(string(rewritten), "source: https://github.com/acme/observability-plugin") {
		t.Fatalf("expected the source field to survive the rewrite unchanged, got: %s", rewritten)
	}

	// The rewritten reference now resolves, and apply succeeds offline.
	if code := run([]string{"apply", agent, "--harness", "claude"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply failed after a successful plugin update: %d\nstderr=%s", code, stderr.String())
	}
}
