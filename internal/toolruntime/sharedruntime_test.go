package toolruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// isolateUserCacheDir points os.UserCacheDir() at a throwaway directory for
// the test's lifetime, matching the same isolation internal/stage's and
// cmd/tenon's real-toolchain tests apply: os.UserCacheDir() reads
// $XDG_CACHE_HOME on every platform this repo targets except darwin, which
// ignores it and always resolves $HOME/Library/Caches directly, so both
// are overridden.
func isolateUserCacheDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			_ = os.Chmod(path, info.Mode().Perm()|0o700)
			return nil
		})
	})
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func TestWithRuntimeLockSerializesTwoCallers(t *testing.T) {
	isolateUserCacheDir(t)

	var mu sync.Mutex
	inside := 0
	maxInside := 0
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := withRuntimeLock(context.Background(), "test", func() error {
				mu.Lock()
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("withRuntimeLock: %v", err)
			}
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("at most one caller may hold the lock at once, saw %d concurrently", maxInside)
	}
}

func TestWithRuntimeLockHonorsContextCancellation(t *testing.T) {
	isolateUserCacheDir(t)

	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = withRuntimeLock(context.Background(), "ctxtest", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := withRuntimeLock(ctx, "ctxtest", func() error {
		t.Fatal("fn must not run while the lock is held elsewhere")
		return nil
	})
	if err == nil {
		t.Fatal("a contended lock must fail once ctx is done, not block forever")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ctx cancellation must unblock promptly, took %v", elapsed)
	}
}

func TestHardlinkFileLinksByDefault(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "nested", "dst")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := hardlinkFile(src, dst); err != nil {
		t.Fatalf("hardlinkFile: %v", err)
	}
	if !os.SameFile(mustStat(t, src), mustStat(t, dst)) {
		t.Fatal("hardlinkFile must produce the same inode as its source by default")
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestHardlinkFileFallsBackToCopyOnEXDEV(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("cross-device"), 0o644); err != nil {
		t.Fatal(err)
	}

	original := linkFn
	linkFn = func(string, string) error {
		return &os.LinkError{Op: "link", Err: syscall.EXDEV}
	}
	defer func() { linkFn = original }()

	if err := hardlinkFile(src, dst); err != nil {
		t.Fatalf("hardlinkFile must fall back to a copy on EXDEV: %v", err)
	}
	if os.SameFile(mustStat(t, src), mustStat(t, dst)) {
		t.Fatal("the EXDEV fallback must produce an independent copy, not the same inode")
	}
	content, err := os.ReadFile(dst)
	if err != nil || string(content) != "cross-device" {
		t.Fatalf("the fallback copy must carry the source's content: %v, %q", err, content)
	}
}

func TestHardlinkTreeLinksExceptSkippedFiles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "kept"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "skipped.py"), []byte("skip me"), 0o644); err != nil {
		t.Fatal(err)
	}

	skip := func(name string) bool { return name == "skipped.py" }
	if err := hardlinkTree(src, dst, skip); err != nil {
		t.Fatalf("hardlinkTree: %v", err)
	}

	if !os.SameFile(mustStat(t, filepath.Join(src, "kept")), mustStat(t, filepath.Join(dst, "kept"))) {
		t.Fatal("a non-skipped file must be hardlinked")
	}
	if _, err := os.Stat(filepath.Join(dst, "nested", "skipped.py")); !os.IsNotExist(err) {
		t.Fatalf("a skipped file must not be populated by hardlinkTree at all: %v", err)
	}
}

func TestHardlinkTreeRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "outside")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := hardlinkTree(src, dst, nil); err == nil {
		t.Fatal("hardlinkTree must refuse a symlink under src, not link through it")
	}
}

func TestChmodTreeReadOnlyStripsWriteBitsRecursively(t *testing.T) {
	dir := t.TempDir()
	// t.TempDir()'s own cleanup can't os.RemoveAll a read-only tree, so
	// this restores write bits first; registered after t.TempDir() itself,
	// LIFO cleanup order runs it first.
	t.Cleanup(func() {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			_ = os.Chmod(path, info.Mode().Perm()|0o700)
			return nil
		})
	})
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sub", "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := chmodTreeReadOnly(dir); err != nil {
		t.Fatalf("chmodTreeReadOnly: %v", err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("file must carry no write bit after chmodTreeReadOnly, got %v", info.Mode().Perm())
	}
	if err := os.WriteFile(file, []byte("y"), 0o644); err == nil {
		t.Fatal("writing a chmodTreeReadOnly'd file must fail, not silently succeed")
	}
}

func TestMarkSharedRuntimeReadyIsAtomicAndDetectable(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "identity.ready")
	if markerReady(marker) {
		t.Fatal("a marker that was never written must not report ready")
	}
	if err := markSharedRuntimeReady(marker); err != nil {
		t.Fatalf("markSharedRuntimeReady: %v", err)
	}
	if !markerReady(marker) {
		t.Fatal("a written marker must report ready")
	}
	if _, err := os.Stat(marker + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the temporary marker file must not survive a successful rename: %v", err)
	}
}
