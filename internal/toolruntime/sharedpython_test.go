package toolruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExactPythonSpec(t *testing.T) {
	cases := map[string]string{
		"3.11.13": "3.11.13",
		"3.11":    "",
		"3":       "",
		"":        "",
	}
	for spec, want := range cases {
		if got := exactPythonSpec(spec); got != want {
			t.Errorf("exactPythonSpec(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestPythonBinaryName(t *testing.T) {
	got, err := pythonBinaryName("cpython-3.11.13-linux-x86_64-gnu")
	if err != nil {
		t.Fatalf("pythonBinaryName: %v", err)
	}
	if got != "python3.11" {
		t.Fatalf("pythonBinaryName = %q, want python3.11", got)
	}
	if _, err := pythonBinaryName("not-an-identity"); err == nil {
		t.Fatal("an unrecognized identity must be refused, not guessed at")
	}
}

func TestIsSysconfigdataFile(t *testing.T) {
	yes := []string{"_sysconfigdata__darwin_darwin.py", "_sysconfigdata_x86_64-linux-gnu.py"}
	no := []string{"sysconfigdata.py", "_sysconfigdata_.pyc", "_sysconfigdata_x.txt"}
	for _, name := range yes {
		if !isSysconfigdataFile(name) {
			t.Errorf("isSysconfigdataFile(%q) = false, want true", name)
		}
	}
	for _, name := range no {
		if isSysconfigdataFile(name) {
			t.Errorf("isSysconfigdataFile(%q) = true, want false", name)
		}
	}
}

func TestScanSharedIdentityMatchesFloorAndExactSpecs(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"cpython-3.11.13-linux-x86_64-gnu",
		"cpython-3.12.1-linux-x86_64-gnu",
	} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if got, ok, err := scanSharedIdentity(root, "3.11"); err != nil || !ok || got != "cpython-3.11.13-linux-x86_64-gnu" {
		t.Fatalf("floor spec 3.11: got=%q ok=%v err=%v", got, ok, err)
	}
	if got, ok, err := scanSharedIdentity(root, "3.11.13"); err != nil || !ok || got != "cpython-3.11.13-linux-x86_64-gnu" {
		t.Fatalf("exact spec 3.11.13: got=%q ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := scanSharedIdentity(root, "3.13"); err != nil || ok {
		t.Fatalf("a spec with no installed match must report ok=false, not error: ok=%v err=%v", ok, err)
	}
	// 3.1 must not match 3.11.13 or 3.12.1 by bare string prefix: the "."
	// delimiter requirement in scanSharedIdentity exists exactly to refuse
	// this collision.
	if _, ok, err := scanSharedIdentity(root, "3.1"); err != nil || ok {
		t.Fatalf("a floor spec must not match on a bare string prefix: ok=%v err=%v", ok, err)
	}
}

func TestScanSharedIdentityOnMissingRootIsNotAnError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	if _, ok, err := scanSharedIdentity(root, "3.11"); err != nil || ok {
		t.Fatalf("a shared root that has never been populated must report ok=false, not error: ok=%v err=%v", ok, err)
	}
}

func TestCopySysconfigdataFilesProducesWritableIndependentCopies(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	sysconfigPath := filepath.Join(src, "lib", "python3.11")
	if err := os.MkdirAll(sysconfigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(sysconfigPath, "_sysconfigdata__darwin_darwin.py")
	if err := os.WriteFile(srcFile, []byte("BINDIR = '"+src+"'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate the shared store's own read-only posture (chmodTreeReadOnly)
	// to prove the copy does not inherit it.
	if err := os.Chmod(srcFile, 0o444); err != nil {
		t.Fatal(err)
	}

	if err := copySysconfigdataFiles(src, dst); err != nil {
		t.Fatalf("copySysconfigdataFiles: %v", err)
	}

	dstFile := filepath.Join(dst, "lib", "python3.11", "_sysconfigdata__darwin_darwin.py")
	info, err := os.Stat(dstFile)
	if err != nil {
		t.Fatalf("the copy must exist: %v", err)
	}
	if info.Mode().Perm()&0o200 == 0 {
		t.Fatal("the per-agent copy must be owner-writable, not inherit the shared source's read-only mode")
	}
	// Provable by actually overwriting it, not just by inspecting the mode.
	if err := os.WriteFile(dstFile, []byte("overwritten"), 0o644); err != nil {
		t.Fatalf("a rerun of prepareClosurePython must be able to rewrite this file: %v", err)
	}
}

func TestRewritePythonSysconfigDataReplacesTheBakedPath(t *testing.T) {
	dir := t.TempDir()
	sysDir := filepath.Join(dir, "lib", "python3.11")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sysDir, "_sysconfigdata__darwin_darwin.py")
	oldPath := "/shared/cache/cpython-3.11.13-macos-aarch64-none"
	newPath := "/agent/closure/cpython-3.11.13-macos-aarch64-none"
	if err := os.WriteFile(path, []byte("BINDIR = '"+oldPath+"'\nBINLIBDEST = '"+oldPath+"/lib'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// walkDir == dst (the files already physically live at dir when this
	// runs, per prepareClosurePython's own post-copy usage), not walkDir
	// == oldPath.
	if err := RewritePythonSysconfigData(dir, oldPath, newPath); err != nil {
		t.Fatalf("RewritePythonSysconfigData: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); !strings.Contains(got, newPath) || strings.Contains(got, oldPath) {
		t.Fatalf("expected every occurrence of %q rewritten to %q, got:\n%s", oldPath, newPath, got)
	}
}

func TestRewritePythonSysconfigDataIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "not_sysconfigdata.py")
	oldPath := "/shared/cache/cpython"
	if err := os.WriteFile(other, []byte("BINDIR = '"+oldPath+"'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RewritePythonSysconfigData(dir, oldPath, "/agent/cpython"); err != nil {
		t.Fatalf("RewritePythonSysconfigData: %v", err)
	}
	content, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), oldPath) {
		t.Fatal("a non-sysconfigdata file must be left untouched")
	}
}

func TestRewritePythonSysconfigDataNoOpWhenPathsMatch(t *testing.T) {
	dir := t.TempDir()
	sysDir := filepath.Join(dir, "lib", "python3.11")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sysDir, "_sysconfigdata__darwin_darwin.py")
	if err := os.WriteFile(path, []byte("BINDIR = '/same/path'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RewritePythonSysconfigData(dir, "/same/path", "/same/path"); err != nil {
		t.Fatalf("RewritePythonSysconfigData: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.ModTime() != after.ModTime() {
		t.Fatal("oldPath == newPath must be a true no-op, not rewrite the file")
	}
}
