package pluginref

import (
	"encoding/json"
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

	root, err := cache.Resolve(repo, rev)
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
	if _, err := cache.Resolve("https://example.invalid/x", rev); err == nil {
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
	root, err := cache.Resolve(repo, rev)
	if err != nil {
		t.Fatalf("Resolve before tamper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Resolve(repo, rev); err == nil {
		t.Fatal("expected Resolve to fail after the cached tree was tampered with")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.digest.mismatch" {
		t.Fatalf("expected pluginref.digest.mismatch, got %v", err)
	}
}

// TestResolveDetectsSourceMismatch proves Resolve fails when the caller's
// source disagrees with the source recorded at fetch time for the same
// rev, independent of the tree's digest still verifying cleanly: a rev is
// content-addressed, not source-addressed, so this is the one swap the
// digest alone cannot catch.
func TestResolveDetectsSourceMismatch(t *testing.T) {
	repo, rev := newFixtureRepo(t, map[string]string{"plugin.json": `{"$schema":"x","name":"fixture"}`})
	cache := NewCache(t.TempDir())
	if _, err := cache.Fetch(repo, rev); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := cache.Resolve("https://example.invalid/a-different-repo", rev); err == nil {
		t.Fatal("expected Resolve to fail when the declared source disagrees with the recorded one")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.source.mismatch" {
		t.Fatalf("expected pluginref.source.mismatch, got %v", err)
	}
	// The identical source still resolves cleanly.
	if _, err := cache.Resolve(repo, rev); err != nil {
		t.Fatalf("Resolve with the correct source: %v", err)
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

// TestResolveDetectsDeletedFile proves a tampered tree missing a
// previously-fetched file fails verify, alongside the existing
// content-tamper coverage above.
func TestResolveDetectsDeletedFile(t *testing.T) {
	repo, rev := newFixtureRepo(t, map[string]string{
		"plugin.json": `{"$schema":"x","name":"fixture"}`,
		"extra.txt":   "keep me",
	})
	cache := NewCache(t.TempDir())
	if _, err := cache.Fetch(repo, rev); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	root, err := cache.Resolve(repo, rev)
	if err != nil {
		t.Fatalf("Resolve before tamper: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "extra.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Resolve(repo, rev); err == nil {
		t.Fatal("expected Resolve to fail after a cached file was deleted")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.digest.mismatch" {
		t.Fatalf("expected pluginref.digest.mismatch, got %v", err)
	}
}

// TestResolveDetectsAddedFile proves a tampered tree carrying an extra file
// beyond what was fetched fails verify.
func TestResolveDetectsAddedFile(t *testing.T) {
	repo, rev := newFixtureRepo(t, map[string]string{"plugin.json": `{"$schema":"x","name":"fixture"}`})
	cache := NewCache(t.TempDir())
	if _, err := cache.Fetch(repo, rev); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	root, err := cache.Resolve(repo, rev)
	if err != nil {
		t.Fatalf("Resolve before tamper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "smuggled.txt"), []byte("not part of the fetch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Resolve(repo, rev); err == nil {
		t.Fatal("expected Resolve to fail after an extra file was added to the cached tree")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.digest.mismatch" {
		t.Fatalf("expected pluginref.digest.mismatch, got %v", err)
	}
}

// TestResolveDetectsExecutableBitChange proves the digest folds in the
// executable bit, not just content: chmod +x on an otherwise byte-identical
// file fails verify.
func TestResolveDetectsExecutableBitChange(t *testing.T) {
	repo, rev := newFixtureRepo(t, map[string]string{"plugin.json": `{"$schema":"x","name":"fixture"}`})
	cache := NewCache(t.TempDir())
	if _, err := cache.Fetch(repo, rev); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	root, err := cache.Resolve(repo, rev)
	if err != nil {
		t.Fatalf("Resolve before tamper: %v", err)
	}
	if err := os.Chmod(filepath.Join(root, "plugin.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Resolve(repo, rev); err == nil {
		t.Fatal("expected Resolve to fail after the executable bit changed")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.digest.mismatch" {
		t.Fatalf("expected pluginref.digest.mismatch, got %v", err)
	}
}

// TestVerifyRejectsWrongRecordedDigest proves a cache entry recorded
// against the wrong digest (a hand-written state.json, standing in for any
// out-of-band corruption) is rejected exactly like tampering, and never
// silently trusted.
func TestVerifyRejectsWrongRecordedDigest(t *testing.T) {
	repo, rev := newFixtureRepo(t, map[string]string{"plugin.json": `{"$schema":"x","name":"fixture"}`})
	cache := NewCache(t.TempDir())
	if _, err := cache.Fetch(repo, rev); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	statePath := filepath.Join(cache.base, rev, "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.Digest = "sha256v2:0000000000000000000000000000000000000000000000000000000000000000"
	rewritten, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Resolve(repo, rev); err == nil {
		t.Fatal("expected Resolve to fail against a wrong recorded digest")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.digest.mismatch" {
		t.Fatalf("expected pluginref.digest.mismatch, got %v", err)
	}
}

// TestCheckHeadMatchesRevRejectsMismatch exercises Fetch's post-checkout
// guard directly: a real git checkout of a valid 40-character SHA cannot be
// made to disagree with rev-parse HEAD without a git server that actively
// lies, so the guard is tested at the unit it was split into instead.
func TestCheckHeadMatchesRevRejectsMismatch(t *testing.T) {
	other := strings.Repeat("b", 40)
	rev := strings.Repeat("a", 40)
	if err := checkHeadMatchesRev(other+"\n", rev); err == nil {
		t.Fatal("expected a mismatched HEAD to fail")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.fetch.verify" {
		t.Fatalf("expected pluginref.fetch.verify, got %v", err)
	}
	if err := checkHeadMatchesRev(rev+"\n", rev); err != nil {
		t.Fatalf("expected a matching HEAD (with trailing whitespace trimmed) to pass, got %v", err)
	}
}

// TestFetchCachedHitRequiresMatchingSource proves a cache hit is keyed on
// (rev, source) together, not rev alone: a second Fetch for the same rev but
// a different declared source is treated as not-cached and re-fetches,
// overwriting the recorded source, rather than silently reusing content
// fetched from somewhere else under the same SHA.
func TestFetchCachedHitRequiresMatchingSource(t *testing.T) {
	repoA, rev := newFixtureRepo(t, map[string]string{"plugin.json": `{"$schema":"x","name":"a"}`})
	// repoB is a distinct repository that happens to produce the same
	// commit content is not achievable without a SHA-1 collision, so instead
	// this proves the source-mismatch path re-fetches from repoA a second
	// time under a different claimed source string, rather than trusting
	// the first fetch's cached bytes without checking.
	cache := NewCache(t.TempDir())
	result1, err := cache.Fetch(repoA, rev)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if result1.Cached {
		t.Fatalf("first fetch reported Cached: true")
	}
	state, err := cache.State(rev)
	if err != nil {
		t.Fatal(err)
	}
	if state.Source != repoA {
		t.Fatalf("recorded source = %q, want %q", state.Source, repoA)
	}

	// A second Fetch for the same rev under a different declared source
	// must not report Cached: true against the first fetch's recorded
	// source; it re-fetches (from the same repo here, since that is the
	// only fixture available) and overwrites the recorded source.
	result2, err := cache.Fetch(repoA+"/", rev)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if result2.Cached {
		t.Fatalf("second fetch under a different declared source reported Cached: true")
	}
	state2, err := cache.State(rev)
	if err != nil {
		t.Fatal(err)
	}
	if state2.Source != repoA+"/" {
		t.Fatalf("recorded source after re-fetch = %q, want %q", state2.Source, repoA+"/")
	}
}

// TestTreeBoundsErrorEnforcesFileAndByteCaps exercises MaxTreeFiles and
// MaxTreeBytes enforcement directly against the shared bounds helper, since
// materializing an 8193-file or 64-megabyte fixture tree in a unit test
// would be needlessly slow; walkTree and copyTreeInto both call exactly
// this helper on every entry, so this proves the rule itself without
// depending on how expensive it would be to trip it end-to-end.
func TestTreeBoundsErrorEnforcesFileAndByteCaps(t *testing.T) {
	if err := treeBoundsError(MaxTreeFiles, MaxTreeBytes); err != nil {
		t.Fatalf("at-the-limit counts must not fail: %v", err)
	}
	if err := treeBoundsError(MaxTreeFiles+1, 0); err == nil {
		t.Fatal("expected a file-count-over-limit error")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.tree.bounds" {
		t.Fatalf("expected pluginref.tree.bounds, got %v", err)
	}
	if err := treeBoundsError(0, MaxTreeBytes+1); err == nil {
		t.Fatal("expected a byte-count-over-limit error")
	} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.tree.bounds" {
		t.Fatalf("expected pluginref.tree.bounds, got %v", err)
	}
}

// TestValidTreePathRejectsControlCharacters proves a path carrying \n, \r,
// or any other control byte is rejected, independent of the
// length-prefixed digest encoding also closing the same gap.
func TestValidTreePathRejectsControlCharacters(t *testing.T) {
	for _, rel := range []string{"a\nb", "a\rb", "a\x01b", "a\x7fb"} {
		if err := validTreePath(rel); err == nil {
			t.Fatalf("expected validTreePath(%q) to fail", rel)
		} else if perr, ok := err.(*Error); !ok || perr.Code != "pluginref.tree.invalid" {
			t.Fatalf("expected pluginref.tree.invalid for %q, got %v", rel, err)
		}
	}
	if err := validTreePath("plain/path.txt"); err != nil {
		t.Fatalf("expected an ordinary path to pass, got %v", err)
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
