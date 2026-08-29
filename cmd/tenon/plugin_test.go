package main

import (
	"bytes"
	"encoding/json"
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

// rewriteCachedSource patches a cache entry's already-written state.json to
// record source, in place, leaving its digest and every other field exactly
// as the real Fetch that produced them left them. Test-only file surgery:
// it exists so a test can seed the cache from a local fixture path (the
// only source no test may reach a real network to avoid) while still
// exercising the CLI's real declared-https-source cache lookup, now that a
// cached hit is keyed on (rev, source) together (finding 8).
func rewriteCachedSource(t *testing.T, base, rev, source string) {
	t.Helper()
	statePath := filepath.Join(base, rev, "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state pluginref.State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.Source = source
	rewritten, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, append(rewritten, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
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

	declaredSource := "https://github.com/acme/observability-plugin"
	cache := pluginref.NewCache(base)
	if _, err := cache.Fetch(repo, oldRev); err != nil {
		t.Fatalf("seeding old rev: %v", err)
	}
	if _, err := cache.Fetch(repo, newRev); err != nil {
		t.Fatalf("seeding new rev: %v", err)
	}
	// Seeding above fetches from the local fixture path directly (no test
	// may reach a real network), so the cache records that path as each
	// entry's source. The reference file below must declare an https URL
	// (ADR 0026's closed authored grammar), which the CLI then passes back
	// to Cache.Fetch as the requested source on every subsequent command.
	// Patch the recorded source to match it, so a cached hit is recognized
	// as one (finding 8: a cache hit is keyed on (rev, source) together,
	// not rev alone) without requiring an actual re-fetch from a URL no
	// test can reach.
	rewriteCachedSource(t, base, oldRev, declaredSource)
	rewriteCachedSource(t, base, newRev, declaredSource)

	agent := writeAgent(t, "agent", validInstructions)
	writePluginReferenceFile(t, agent, "obs", declaredSource, oldRev)

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

// TestReplaceFrontmatterRevPreservesCRLF proves the rev-line rewrite
// preserves a CRLF line ending rather than collapsing it to a bare LF
// (finding 11): "." in Go's regexp excludes \n but not \r, so a naive
// ".*$" value class would consume the trailing \r along with the rev.
func TestReplaceFrontmatterRevPreservesCRLF(t *testing.T) {
	raw := []byte("---\r\nsource: https://github.com/acme/observability-plugin\r\nrev: " +
		strings.Repeat("a", 40) + "\r\n---\r\nbody\r\n")
	newRev := strings.Repeat("b", 40)
	out, err := replaceFrontmatterRev(raw, newRev)
	if err != nil {
		t.Fatalf("replaceFrontmatterRev: %v", err)
	}
	want := "---\r\nsource: https://github.com/acme/observability-plugin\r\nrev: " +
		newRev + "\r\n---\r\nbody\r\n"
	if string(out) != want {
		t.Fatalf("replaceFrontmatterRev(CRLF) = %q, want %q", out, want)
	}
}

// TestPluginFetchMalformedNamedRefLeadsWithParseDiagnostic proves a
// plugins/<name>.md that exists but fails to parse is reported by its own
// parse diagnostic, not the generic "no plugin reference named ... was
// found" headline a typo'd or genuinely absent name gets (finding 9: those
// are different failures an operator needs to tell apart).
func TestPluginFetchMalformedNamedRefLeadsWithParseDiagnostic(t *testing.T) {
	isolatedPluginCache(t)
	agent := writeAgent(t, "agent", validInstructions)
	dir := filepath.Join(agent, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing the required "rev" field entirely.
	if err := os.WriteFile(filepath.Join(dir, "obs.md"), []byte("---\nsource: https://github.com/acme/observability-plugin\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"plugin", "fetch", agent, "obs"}, nil, &stdout, &stderr); code == 0 {
		t.Fatalf("expected a nonzero exit for a malformed named reference")
	}
	if strings.Contains(stderr.String(), "no plugin reference named") {
		t.Fatalf("expected the parse diagnostic to lead, not the not-found headline: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "plugin.reference.rev.invalid") {
		t.Fatalf("expected the rev.invalid parse diagnostic, got: %s", stderr.String())
	}

	// A genuinely absent name still gets the not-found headline.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plugin", "fetch", agent, "nonexistent"}, nil, &stdout, &stderr); code == 0 {
		t.Fatalf("expected a nonzero exit for an absent reference name")
	}
	if !strings.Contains(stderr.String(), `no plugin reference named "nonexistent" was found`) {
		t.Fatalf("expected the not-found headline for a genuinely absent name, got: %s", stderr.String())
	}
}

// TestPluginUpdateMalformedNamedRefLeadsWithParseDiagnostic is the update
// counterpart of the fetch test above.
func TestPluginUpdateMalformedNamedRefLeadsWithParseDiagnostic(t *testing.T) {
	isolatedPluginCache(t)
	agent := writeAgent(t, "agent", validInstructions)
	dir := filepath.Join(agent, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "obs.md"), []byte("---\nsource: not-a-url\nrev: "+strings.Repeat("a", 40)+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	rev := strings.Repeat("b", 40)
	if code := run([]string{"plugin", "update", agent, "obs", "--rev", rev}, nil, &stdout, &stderr); code == 0 {
		t.Fatalf("expected a nonzero exit for a malformed named reference")
	}
	if strings.Contains(stderr.String(), "no plugin reference named") {
		t.Fatalf("expected the parse diagnostic to lead, not the not-found headline: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "plugin.reference.source.invalid") {
		t.Fatalf("expected the source.invalid parse diagnostic, got: %s", stderr.String())
	}
}

// TestPluginUpdateWithUncachedOldRevProceeds proves `plugin update` no
// longer dead-ends when the currently pinned rev was never fetched (or is
// no longer cached): it prints that the diff is unavailable and still
// rewrites the reference to the new, successfully fetched rev (finding 4).
func TestPluginUpdateWithUncachedOldRevProceeds(t *testing.T) {
	base := isolatedPluginCache(t)
	repo, newRev := newLocalGitFixture(t, validPluginTreeFiles("observability"))
	declaredSource := "https://github.com/acme/observability-plugin"

	cache := pluginref.NewCache(base)
	if _, err := cache.Fetch(repo, newRev); err != nil {
		t.Fatalf("seeding new rev: %v", err)
	}
	rewriteCachedSource(t, base, newRev, declaredSource)

	agent := writeAgent(t, "agent", validInstructions)
	// The pinned old rev is well-formed but was never fetched into any
	// cache; only newRev above is cached.
	oldRev := strings.Repeat("c", 40)
	writePluginReferenceFile(t, agent, "obs", declaredSource, oldRev)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"plugin", "update", agent, "obs", "--rev", newRev}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("plugin update failed: %d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "diff unavailable: currently pinned rev "+oldRev+" is not cached") {
		t.Fatalf("expected a diff-unavailable message naming the uncached old rev, got: %s", stdout.String())
	}
	rewritten, err := os.ReadFile(filepath.Join(agent, "plugins", "obs.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rewritten), "rev: "+newRev) {
		t.Fatalf("expected the reference file to be rewritten to the new rev despite the uncached old rev, got: %s", rewritten)
	}
}
