package toolruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFakeExecutable(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-deno")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnsureSharedDenoIsStableAndCachesOnSecondCall(t *testing.T) {
	isolateUserCacheDir(t)
	deno := writeFakeExecutable(t, "#!/bin/sh\necho fake-deno\n")

	id1, path1, err := ensureSharedDeno(context.Background(), deno)
	if err != nil {
		t.Fatalf("ensureSharedDeno: %v", err)
	}
	if id1 == "" || path1 == "" {
		t.Fatal("ensureSharedDeno must return a non-empty identity and path")
	}
	info, err := os.Stat(path1)
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the shared copy must be a regular executable file: %v, %v", err, info)
	}

	id2, path2, err := ensureSharedDeno(context.Background(), deno)
	if err != nil {
		t.Fatalf("ensureSharedDeno (second call): %v", err)
	}
	if id1 != id2 || path1 != path2 {
		t.Fatalf("a second call for the same content must resolve to the same identity and path: (%q,%q) vs (%q,%q)",
			id1, path1, id2, path2)
	}

	// The second call must have been a pure cache hit: it never re-wrote
	// the already-secured shared file, so the file's mode must still be
	// read-only (chmodTreeReadOnly), not the 0o755 writeCacheFile would
	// have (harmlessly) re-applied had it run again.
	info2, err := os.Stat(path2)
	if err != nil {
		t.Fatal(err)
	}
	if info2.Mode().Perm()&0o200 != 0 {
		t.Fatal("a cache-hit call must not have rewritten the shared entry, which chmodTreeReadOnly already secured")
	}
}

func TestEnsureSharedDenoDistinctContentGetsDistinctIdentity(t *testing.T) {
	isolateUserCacheDir(t)
	denoA := writeFakeExecutable(t, "#!/bin/sh\necho a\n")
	denoB := writeFakeExecutable(t, "#!/bin/sh\necho b\n")

	idA, _, err := ensureSharedDeno(context.Background(), denoA)
	if err != nil {
		t.Fatalf("ensureSharedDeno(a): %v", err)
	}
	idB, _, err := ensureSharedDeno(context.Background(), denoB)
	if err != nil {
		t.Fatalf("ensureSharedDeno(b): %v", err)
	}
	if idA == idB {
		t.Fatal("distinct executable content must resolve to distinct shared identities")
	}
}

func TestEnsureSharedDenoRefusesANonExecutableFile(t *testing.T) {
	isolateUserCacheDir(t)
	path := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(path, []byte("not a binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureSharedDeno(context.Background(), path); err == nil {
		t.Fatal("a non-executable file must be refused, not silently accepted")
	}
}
