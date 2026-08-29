package pluginref

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitEnv is a hermetic git identity for fixture commits, so tests never
// depend on (or pollute) the invoking user's global git config.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=tenon-test",
		"GIT_AUTHOR_EMAIL=tenon-test@example.invalid",
		"GIT_COMMITTER_NAME=tenon-test",
		"GIT_COMMITTER_EMAIL=tenon-test@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func runGitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// newFixtureRepo creates a local git repository under a fresh t.TempDir with
// one commit writing files, and returns the repo's absolute path and the
// commit's full SHA — a local source, since the authored `source` grammar's
// https-only requirement is enforced at the CLI/agentproject layer, not
// here (this package's Fetch takes whatever string it is given, exactly
// like a manual `git clone`).
func newFixtureRepo(t *testing.T, files map[string]string) (repo, rev string) {
	t.Helper()
	repo = t.TempDir()
	runGitFixture(t, repo, "init", "--initial-branch=main")
	for path, content := range files {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitFixture(t, repo, "add", "-A")
	runGitFixture(t, repo, "commit", "-m", "fixture commit")
	rev = strings.TrimSpace(runGitFixture(t, repo, "rev-parse", "HEAD"))
	return repo, rev
}

func TestFetchThenResolveRoundTrips(t *testing.T) {
	repo, rev := newFixtureRepo(t, map[string]string{
		"plugin.json":       `{"$schema":"x","name":"fixture"}`,
		"skills/a/SKILL.md": "hello",
	})
	cache := NewCache(t.TempDir())

	result, err := cache.Fetch(repo, rev)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Cached {
		t.Fatalf("first fetch reported Cached: true")
	}
	if result.Digest == "" {
		t.Fatalf("Fetch returned an empty digest")
	}

	root, err := cache.Resolve(rev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "plugin.json"))
	if err != nil {
		t.Fatalf("reading resolved plugin.json: %v", err)
	}
	if !strings.Contains(string(data), "fixture") {
		t.Fatalf("resolved plugin.json content unexpected: %s", data)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		t.Fatalf("resolved tree carries a .git directory; only worktree bytes should be cached")
	}

	// Re-fetching the same rev does no network work and reports Cached.
	result2, err := cache.Fetch(repo, rev)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !result2.Cached {
		t.Fatalf("second fetch of the same rev did not report Cached: true")
	}
	if result2.Digest != result.Digest {
		t.Fatalf("digest changed across an idempotent fetch: %s vs %s", result.Digest, result2.Digest)
	}
}

func TestResolveWithoutFetchFails(t *testing.T) {
	cache := NewCache(t.TempDir())
	rev := strings.Repeat("a", 40)
	if _, err := cache.Resolve(rev); err == nil {
		t.Fatal("expected Resolve to fail for an unfetched rev")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.not-cached" {
		t.Fatalf("expected pluginref.not-cached, got %v", err)
	}
}

func TestResolveDetectsTamperedContent(t *testing.T) {
	repo, rev := newFixtureRepo(t, map[string]string{"plugin.json": `{"$schema":"x","name":"fixture"}`})
	cache := NewCache(t.TempDir())
	if _, err := cache.Fetch(repo, rev); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	root, err := cache.Resolve(rev)
	if err != nil {
		t.Fatalf("Resolve before tamper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Resolve(rev); err == nil {
		t.Fatal("expected Resolve to fail after the cached tree was tampered with")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.digest.mismatch" {
		t.Fatalf("expected pluginref.digest.mismatch, got %v", err)
	}
}

func TestFetchInvalidRevRejected(t *testing.T) {
	cache := NewCache(t.TempDir())
	if _, err := cache.Fetch("/dev/null", "not-a-sha"); err == nil {
		t.Fatal("expected Fetch to reject a malformed rev")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.rev.invalid" {
		t.Fatalf("expected pluginref.rev.invalid, got %v", err)
	}
}

func TestFetchRejectsSymlinkInTree(t *testing.T) {
	repo := t.TempDir()
	runGitFixture(t, repo, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(repo, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}
	runGitFixture(t, repo, "add", "-A")
	runGitFixture(t, repo, "commit", "-m", "fixture with symlink")
	rev := strings.TrimSpace(runGitFixture(t, repo, "rev-parse", "HEAD"))

	cache := NewCache(t.TempDir())
	if _, err := cache.Fetch(repo, rev); err == nil {
		t.Fatal("expected Fetch to reject a tree containing a symlink")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.tree.invalid" {
		t.Fatalf("expected pluginref.tree.invalid, got %v", err)
	}
}

func TestDiffReportsAddedRemovedChanged(t *testing.T) {
	repo := t.TempDir()
	runGitFixture(t, repo, "init", "--initial-branch=main")
	write := func(files map[string]string) {
		for path, content := range files {
			full := filepath.Join(repo, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(map[string]string{"a.txt": "one", "b.txt": "keep"})
	runGitFixture(t, repo, "add", "-A")
	runGitFixture(t, repo, "commit", "-m", "first")
	oldRev := strings.TrimSpace(runGitFixture(t, repo, "rev-parse", "HEAD"))

	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	write(map[string]string{"b.txt": "changed", "c.txt": "new"})
	runGitFixture(t, repo, "add", "-A")
	runGitFixture(t, repo, "commit", "-m", "second")
	newRev := strings.TrimSpace(runGitFixture(t, repo, "rev-parse", "HEAD"))

	cache := NewCache(t.TempDir())
	if _, err := cache.Fetch(repo, oldRev); err != nil {
		t.Fatalf("Fetch old: %v", err)
	}
	if _, err := cache.Fetch(repo, newRev); err != nil {
		t.Fatalf("Fetch new: %v", err)
	}
	added, removed, changed, err := cache.Diff(oldRev, newRev)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(added) != 1 || added[0] != "c.txt" {
		t.Fatalf("added = %v, want [c.txt]", added)
	}
	if len(removed) != 1 || removed[0] != "a.txt" {
		t.Fatalf("removed = %v, want [a.txt]", removed)
	}
	if len(changed) != 1 || changed[0] != "b.txt" {
		t.Fatalf("changed = %v, want [b.txt]", changed)
	}
}
