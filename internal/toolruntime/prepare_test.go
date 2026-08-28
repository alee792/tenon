package toolruntime

// These tests build a Python closure's on-disk shape by hand, without ever
// running uv, so the CI-breaking scenario (a newer uv release's own layout
// change) is reproducible and fixable offline: uv 0.8.17, this repo's
// pinned release, lays every one of its own convenience symlinks inside the
// versioned interpreter directory (interpDir); uv 0.12.6, resolved by an
// unpinned CI setup-uv step falling back to "latest", additionally creates
// a minor-version symlink as interpDir's own SIBLING —
// cpython/cpython-3.11-linux-x86_64-gnu -> cpython/cpython-3.11.13-linux-x86_64-gnu/
// — outside anything an interpDir-scoped walk ever visited, so it survived
// normalization and copyTree correctly refused it at staging time with
// "staging refuses a non-regular entry: cpython/cpython-3.11-linux-x86_64-gnu".

import (
	"os"
	"path/filepath"
	"testing"
)

// buildFakeCPythonClosure lays out a closure directory by hand, shaped
// exactly like uv 0.12.6's own `uv python install --no-bin --install-dir`
// output for one interpreter: the real versioned interpreter directory
// (with a stand-in interpreter binary and a couple of the convenience
// symlinks uv 0.8.17 already produces inside it), PLUS the newer release's
// additional minor-version symlink beside it. It returns the closure root
// (dir), cpythonRoot, interpDir, and siteDir.
func buildFakeCPythonClosure(t *testing.T) (dir, cpythonRoot, interpDir, siteDir string) {
	t.Helper()
	dir = t.TempDir()
	cpythonRoot = filepath.Join(dir, "cpython")
	interpDir = filepath.Join(cpythonRoot, "cpython-3.11.13-linux-x86_64-gnu")
	siteDir = filepath.Join(dir, "site")

	if err := os.MkdirAll(filepath.Join(interpDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interpDir, "bin", "python3.11"), []byte("#!fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink uv 0.8.17 already produces inside interpDir, so the
	// interpDir-scoped walk this normalization replaces would have caught
	// it — the regression is specifically about the SIBLING symlink below.
	if err := os.Symlink("python3.11", filepath.Join(interpDir, "bin", "python3")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// uv 0.12.6's own addition: a minor-version symlink as interpDir's
	// sibling, relative, exactly as uv lays its own convenience symlinks.
	if err := os.Symlink("cpython-3.11.13-linux-x86_64-gnu",
		filepath.Join(cpythonRoot, "cpython-3.11-linux-x86_64-gnu")); err != nil {
		t.Fatal(err)
	}
	return dir, cpythonRoot, interpDir, siteDir
}

// TestNormalizePythonClosureRemovesUV0126MinorVersionSymlink is the
// reproduce-then-fix proof for the CI failure: before the fix,
// normalizePythonClosure walked only interpDir and siteDir, so the sibling
// minor-version symlink survived into the copied closure, where copyTree
// (internal/stage) correctly refused it as a non-regular entry. Walking
// cpythonRoot itself (see normalizePythonClosure's own doc) finds and
// removes it, and pythonClosureLayout's ambiguity guard — which already
// tolerates a symlink matching cpython-* because os.DirEntry.IsDir() is
// false for a symlink, even one pointing at a directory — still resolves to
// exactly the one real interpreter directory once normalization is done.
func TestNormalizePythonClosureRemovesUV0126MinorVersionSymlink(t *testing.T) {
	dir, cpythonRoot, interpDir, siteDir := buildFakeCPythonClosure(t)

	// Sanity check: the fixture actually reproduces the raw material of the
	// bug — a symlink sits directly under cpythonRoot, beside interpDir.
	if info, err := os.Lstat(filepath.Join(cpythonRoot, "cpython-3.11-linux-x86_64-gnu")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("fixture setup did not produce the sibling symlink: %v, %v", info, err)
	}

	if err := normalizeInterpreterClosure(cpythonRoot, interpDir); err != nil {
		t.Fatalf("normalizing a closure shaped like uv 0.12.6's output must succeed: %v", err)
	}
	if err := normalizeSiteClosure(siteDir); err != nil {
		t.Fatalf("normalizing the site directory must succeed: %v", err)
	}

	// Zero symlinks survive anywhere in the closure — the same guarantee
	// copyTree's own symlink prohibition requires downstream, proven here
	// directly rather than only by copyTree's later, less specific refusal.
	assertNoSymlinksAnywhere(t, cpythonRoot)
	assertNoSymlinksAnywhere(t, siteDir)

	// pythonClosureLayout's ambiguity guard (both a symlink and a real
	// directory could match cpython-* before normalization; only the real
	// directory does after) still resolves to exactly the one real
	// interpreter directory, not staleCache from a false "more than one
	// python interpreter was installed".
	interpBin, gotSite, identity, err := pythonClosureLayout(dir)
	if err != nil {
		t.Fatalf("layout resolution after normalization must succeed: %v", err)
	}
	if interpBin != filepath.Join(interpDir, "bin", "python3.11") {
		t.Fatalf("interpBin = %q, want %q", interpBin, filepath.Join(interpDir, "bin", "python3.11"))
	}
	if gotSite != siteDir {
		t.Fatalf("siteDir = %q, want %q", gotSite, siteDir)
	}
	if identity != "cpython-3.11.13-linux-x86_64-gnu" {
		t.Fatalf("identity = %q, want cpython-3.11.13-linux-x86_64-gnu", identity)
	}
}

// TestRemovePythonClosureSymlinksRefusesATargetOutsideTheClosure proves the
// fail-closed half of the fix: a symlink whose target does not resolve
// inside the closure is left alone and reported, not silently deleted or
// silently kept — an unexpected shape a future uv layout change could
// introduce should stop preparation with a diagnostic, not be guessed at.
func TestRemovePythonClosureSymlinksRefusesATargetOutsideTheClosure(t *testing.T) {
	dir := t.TempDir()
	cpythonRoot := filepath.Join(dir, "cpython")
	if err := os.MkdirAll(cpythonRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "escape"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "escape"), filepath.Join(cpythonRoot, "escape-link")); err != nil {
		t.Fatal(err)
	}

	err := removePythonClosureSymlinks(cpythonRoot, []string{cpythonRoot})
	if err == nil {
		t.Fatal("a symlink targeting outside the closure must be refused, not silently deleted")
	}
	if _, statErr := os.Lstat(filepath.Join(cpythonRoot, "escape-link")); statErr != nil {
		t.Fatalf("the refused symlink must be left in place, not removed: %v", statErr)
	}
}

// buildFakeDenoCache lays out a DENO_DIR by hand, shaped like Deno 2.9's own
// on-disk cache after `deno check` populates it: flat derived-cache files
// keyed by preparation-time specifiers (some with SQLite -shm/-wal
// siblings), a lazily created node_compat_bin carrying a symlink back to
// the build-time deno executable, and the actual downloaded npm package
// cache that must survive pruning.
func buildFakeDenoCache(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	for _, name := range []string{
		"check_cache_v2", "dep_analysis_cache_v2", "fast_check_cache_v2",
		"node_analysis_cache_v2", "v8_code_cache_v2", "v8_code_cache_v2-shm", "v8_code_cache_v2-wal",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("derived cache\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fakeDeno := filepath.Join(dir, "..", "fake-build-time-deno")
	if err := os.WriteFile(fakeDeno, []byte("#!fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_compat_bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fakeDeno, filepath.Join(dir, "node_compat_bin", "deno")); err != nil {
		t.Fatal(err)
	}
	npmPkg := filepath.Join(dir, "npm", "registry.npmjs.org", "zod", "4.4.3")
	if err := os.MkdirAll(npmPkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(npmPkg, "index.js"), []byte("export const z = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPruneDenoCacheKeepsNpmAndDiscardsDerivedCaches proves pruneDenoCache
// discards every path-keyed derived cache entry and node_compat_bin's
// symlink back to the build-time executable, while leaving the actually
// downloaded npm package cache a --cached-only run depends on untouched and
// leaving no symlink anywhere in the result (copyTree refuses one outright
// at staging time).
func TestPruneDenoCacheKeepsNpmAndDiscardsDerivedCaches(t *testing.T) {
	dir := buildFakeDenoCache(t)

	if err := pruneDenoCache(dir); err != nil {
		t.Fatalf("pruning a normally shaped deno cache must succeed: %v", err)
	}

	for _, gone := range []string{
		"check_cache_v2", "dep_analysis_cache_v2", "fast_check_cache_v2",
		"node_analysis_cache_v2", "v8_code_cache_v2", "v8_code_cache_v2-shm", "v8_code_cache_v2-wal",
		"node_compat_bin",
	} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s must be discarded by pruning: %v", gone, err)
		}
	}

	kept := filepath.Join(dir, "npm", "registry.npmjs.org", "zod", "4.4.3", "index.js")
	if content, err := os.ReadFile(kept); err != nil || string(content) != "export const z = {};\n" {
		t.Fatalf("the downloaded npm package cache must survive pruning intact: %v, %q", err, content)
	}

	assertNoSymlinksAnywhere(t, dir)
}

// TestPruneDenoCacheFailsClosedOnASurvivingSymlink proves the defense-in-
// depth walk actually fires: a symlink outside the known node_compat_bin
// shape is refused rather than silently kept or silently deleted.
func TestPruneDenoCacheFailsClosedOnASurvivingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "..", "escape-target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "surprise-link")); err != nil {
		t.Fatal(err)
	}
	if err := pruneDenoCache(dir); err == nil {
		t.Fatal("a surviving symlink outside the known prune list must fail closed")
	}
}

func assertNoSymlinksAnywhere(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			t.Errorf("a symlink survived normalization at %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
