package integration

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testTenon = "1.0.0"

// hostManifest builds a valid native-mcp manifest whose single binary artifact
// targets the running host, so install prepares it on any test machine.
func hostManifest() map[string]any {
	p := fakePayload()
	sha := hashHex(p)
	return map[string]any{
		"schema_version": 1,
		"id":             "fake-integration",
		"version":        "1.2.0",
		"name":           "Fake Integration",
		"description":    "A credential-free fake native MCP server for tests.",
		"license":        "MIT",
		"source":         "https://example.test/fake",
		"revision":       "abc123",
		"compat":         map[string]any{"minimum": "0.1.0", "before": "2.0.0"},
		"artifacts": []any{map[string]any{
			"id":          "server-host",
			"os":          runtime.GOOS,
			"arch":        runtime.GOARCH,
			"format":      "binary",
			"size":        len(p),
			"sha256":      sha,
			"exec_path":   "bin/server",
			"exec_size":   len(p),
			"exec_sha256": sha,
			"package":     "payload/server",
		}},
		"capabilities": []any{map[string]any{
			"id":          "mcp",
			"type":        "native-mcp",
			"version":     1,
			"server_name": "fake-server",
			"artifacts":   []any{"server-host"},
			"executable":  "bin/server",
			"args":        []any{"--stdio"},
			"workdir":     "",
			"env":         map[string]any{"LOG_LEVEL": "info"},
			"required_env": []any{
				map[string]any{"name": "FAKE_API_TOKEN", "description": "The ambient token the fake server reads from its own environment."},
			},
			"targets": map[string]any{
				"claude": map[string]any{"startup": "optional"},
				"codex":  map[string]any{"startup": "required"},
			},
		}},
	}
}

// writeSourceDir writes a valid package source directory: integration.json at
// the root and the fake payload at its package-relative path.
func writeSourceDir(t *testing.T, m map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "integration.json"), mustJSON(t, m), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload", "server"), fakePayload(), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func hostRequest(source string) InstallRequest {
	return InstallRequest{
		Source:        source,
		TrustOperator: true,
		TenonVersion:  testTenon,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
}

func wantStoreCode(t *testing.T, err error, code string) {
	t.Helper()
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("want StoreError, got %T: %v", err, err)
	}
	if se.Code != code {
		t.Fatalf("StoreError code = %q, want %q", se.Code, code)
	}
}

func TestInstallFromDirectory(t *testing.T) {
	store := newStore(t)
	installed, err := store.Install(hostRequest(writeSourceDir(t, hostManifest())))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.State.Trust != "operator" || !installed.State.Enabled {
		t.Fatalf("state = %+v", installed.State)
	}
	if len(installed.State.Artifacts) != 1 {
		t.Fatalf("expected one prepared artifact, got %+v", installed.State.Artifacts)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 || list[0].ID != "fake-integration" || !list[0].Enabled {
		t.Fatalf("List = %+v err %v", list, err)
	}
}

func TestInstallFromTarGz(t *testing.T) {
	store := newStore(t)
	archive := filepath.Join(t.TempDir(), "pkg.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "integration.json", data: mustJSON(t, hostManifest())},
		{name: "payload/server", data: fakePayload()},
	})
	if _, err := store.Install(hostRequest(archive)); err != nil {
		t.Fatalf("Install from tar.gz: %v", err)
	}
}

func TestInstallFromZip(t *testing.T) {
	store := newStore(t)
	archive := filepath.Join(t.TempDir(), "pkg.zip")
	writeZip(t, archive, map[string][]byte{
		"integration.json": mustJSON(t, hostManifest()),
		"payload/server":   fakePayload(),
	})
	if _, err := store.Install(hostRequest(archive)); err != nil {
		t.Fatalf("Install from zip: %v", err)
	}
}

func TestInstallArchiveTraversalRejected(t *testing.T) {
	store := newStore(t)
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "integration.json", data: mustJSON(t, hostManifest())},
		{name: "../escape", data: []byte("x")},
	})
	_, err := store.Install(hostRequest(archive))
	wantStoreCode(t, err, "install.source.traversal")
}

func TestInstallArchiveSymlinkRejected(t *testing.T) {
	store := newStore(t)
	archive := filepath.Join(t.TempDir(), "link.tar.gz")
	writeTarGz(t, archive, []tarEntry{
		{name: "integration.json", data: mustJSON(t, hostManifest())},
		{name: "payload/server", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	_, err := store.Install(hostRequest(archive))
	wantStoreCode(t, err, "install.source.special")
}

func TestInstallArchiveOversizeRejected(t *testing.T) {
	saved := maxPayloadBytes
	maxPayloadBytes = 8
	defer func() { maxPayloadBytes = saved }()

	store := newStore(t)
	_, err := store.Install(hostRequest(writeSourceDir(t, hostManifest())))
	wantStoreCode(t, err, "install.source.too-large")
}

func TestInstallRawHashMismatch(t *testing.T) {
	store := newStore(t)
	dir := writeSourceDir(t, hostManifest())
	// Tamper the payload so its bytes no longer match the pinned SHA-256.
	if err := os.WriteFile(filepath.Join(dir, "payload", "server"), []byte("tampered-payload-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Install(hostRequest(dir))
	wantStoreCode(t, err, "install.artifact.corrupt")
}

func TestInstallPreparedHashMismatch(t *testing.T) {
	store := newStore(t)
	m := hostManifest()
	// Keep the raw pin correct but declare a wrong prepared exec identity, so
	// the raw payload verifies yet the prepared executable does not.
	art := m["artifacts"].([]any)[0].(map[string]any)
	art["exec_sha256"] = hashHex([]byte("different-executable-bytes"))
	_, err := store.Install(hostRequest(writeSourceDir(t, m)))
	wantStoreCode(t, err, "install.prepared.corrupt")
}

func TestInstallWrongPlatform(t *testing.T) {
	store := newStore(t)
	m := hostManifest()
	art := m["artifacts"].([]any)[0].(map[string]any)
	art["os"] = "plan9"
	art["arch"] = "mips"
	_, err := store.Install(hostRequest(writeSourceDir(t, m)))
	wantStoreCode(t, err, "install.platform.unsupported")
}

func TestInstallWithoutTrust(t *testing.T) {
	store := newStore(t)
	req := hostRequest(writeSourceDir(t, hostManifest()))
	req.TrustOperator = false
	_, err := store.Install(req)
	wantStoreCode(t, err, "install.trust.required")
}

func TestInstallManifestConflictNeedsUpdate(t *testing.T) {
	store := newStore(t)
	if _, err := store.Install(hostRequest(writeSourceDir(t, hostManifest()))); err != nil {
		t.Fatal(err)
	}
	// A different manifest under the same id: plain install refuses.
	m := hostManifest()
	m["revision"] = "def456"
	_, err := store.Install(hostRequest(writeSourceDir(t, m)))
	wantStoreCode(t, err, "install.manifest.conflict")
	// Update accepts it with a fresh operator trust decision.
	if _, err := store.Update(hostRequest(writeSourceDir(t, m))); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func TestCompatBoundaries(t *testing.T) {
	c := Compat{Minimum: "0.1.0", Before: "2.0.0"}
	for _, tt := range []struct {
		version string
		ok      bool
	}{
		{"0.1.0", true},  // inclusive minimum
		{"1.9.9", true},  // below before
		{"0.0.9", false}, // below minimum
		{"2.0.0", false}, // exclusive before
		{"2.0.1", false},
	} {
		v, ok := parseSemver(tt.version)
		if !ok {
			t.Fatalf("parseSemver(%q) failed", tt.version)
		}
		err := checkCompat(c, v)
		if tt.ok && err != nil {
			t.Errorf("version %s should be compatible: %v", tt.version, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("version %s should be incompatible", tt.version)
		}
	}
}

func TestInstallCompatRejected(t *testing.T) {
	store := newStore(t)
	req := hostRequest(writeSourceDir(t, hostManifest()))
	req.TenonVersion = "2.0.0" // equals before: excluded
	_, err := store.Install(req)
	wantStoreCode(t, err, "compat.unsupported")
}

func TestEnableVerifiesFirst(t *testing.T) {
	store := newStore(t)
	installed, err := store.Install(hostRequest(writeSourceDir(t, hostManifest())))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Disable("fake-integration"); err != nil {
		t.Fatal(err)
	}
	// Corrupt the prepared executable, then attempt to enable: enable must
	// verify first and refuse.
	corruptPreparedExec(t, store, installed)
	if err := store.Enable("fake-integration"); err == nil {
		t.Fatalf("Enable should verify first and fail on corruption")
	}
}

func TestDisableBlocksResolve(t *testing.T) {
	store := newStore(t)
	if _, err := store.Install(hostRequest(writeSourceDir(t, hostManifest()))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve("fake-integration", "mcp", testTenon, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("Resolve before disable: %v", err)
	}
	if err := store.Disable("fake-integration"); err != nil {
		t.Fatal(err)
	}
	_, err := store.Resolve("fake-integration", "mcp", testTenon, runtime.GOOS, runtime.GOARCH)
	wantStoreCode(t, err, "resolve.disabled")
}

func TestRemoveKeepsBlobs(t *testing.T) {
	store := newStore(t)
	if _, err := store.Install(hostRequest(writeSourceDir(t, hostManifest()))); err != nil {
		t.Fatal(err)
	}
	blob := store.blobPath(hashHex(fakePayload()))
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("blob missing after install: %v", err)
	}
	if err := store.Remove("fake-integration"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("Remove deleted the shared blob: %v", err)
	}
	if _, err := os.Stat(store.packageDir("fake-integration")); !os.IsNotExist(err) {
		t.Fatalf("Remove left the installation record")
	}
	// Reinstall reuses the retained content.
	if _, err := store.Install(hostRequest(writeSourceDir(t, hostManifest()))); err != nil {
		t.Fatalf("reinstall after remove: %v", err)
	}
}

func TestVerifyCatchesCorruptedPreparedFile(t *testing.T) {
	store := newStore(t)
	installed, err := store.Install(hostRequest(writeSourceDir(t, hostManifest())))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Verify("fake-integration"); err != nil {
		t.Fatalf("Verify clean: %v", err)
	}
	corruptPreparedExec(t, store, installed)
	err = store.Verify("fake-integration")
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Verify should report corruption, got %v", err)
	}
}

func TestResolveDescriptor(t *testing.T) {
	store := newStore(t)
	if _, err := store.Install(hostRequest(writeSourceDir(t, hostManifest()))); err != nil {
		t.Fatal(err)
	}
	desc, err := store.Resolve("fake-integration", "mcp", testTenon, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if desc.ServerName != "fake-server" {
		t.Errorf("server_name = %s", desc.ServerName)
	}
	if !filepath.IsAbs(desc.Executable) || !strings.HasSuffix(desc.Executable, filepath.Join("bin", "server")) {
		t.Errorf("executable = %s", desc.Executable)
	}
	if _, err := os.Stat(desc.Executable); err != nil {
		t.Errorf("prepared executable is not present: %v", err)
	}
	if !filepath.IsAbs(desc.Workdir) {
		t.Errorf("workdir must be absolute: %s", desc.Workdir)
	}
	if len(desc.Args) != 1 || desc.Args[0] != "--stdio" {
		t.Errorf("args = %v", desc.Args)
	}
	if desc.Env["LOG_LEVEL"] != "info" {
		t.Errorf("env = %v", desc.Env)
	}
	// The required ambient variable travels as a NAME only; no value channel.
	if len(desc.RequiredEnv) != 1 || desc.RequiredEnv[0] != "FAKE_API_TOKEN" {
		t.Errorf("required env = %v", desc.RequiredEnv)
	}
	if _, leaked := desc.Env["FAKE_API_TOKEN"]; leaked {
		t.Errorf("required ambient name must not appear as an env default")
	}
	if desc.Targets["claude"].Startup != "optional" || desc.Targets["claude"].Trust != "native-project" {
		t.Errorf("claude target = %+v", desc.Targets["claude"])
	}
	if desc.Targets["codex"].Startup != "required" || desc.Targets["codex"].Trust != "native-project" {
		t.Errorf("codex target = %+v", desc.Targets["codex"])
	}
}

func TestResolveRefusals(t *testing.T) {
	store := newStore(t)
	if _, err := store.Install(hostRequest(writeSourceDir(t, hostManifest()))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve("missing", "mcp", testTenon, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatalf("resolving an uninstalled id should fail")
	} else {
		wantStoreCode(t, err, "store.not-found")
	}
	_, err := store.Resolve("fake-integration", "nope", testTenon, runtime.GOOS, runtime.GOARCH)
	wantStoreCode(t, err, "resolve.capability.unknown")
	_, err = store.Resolve("fake-integration", "mcp", "2.0.0", runtime.GOOS, runtime.GOARCH)
	wantStoreCode(t, err, "compat.unsupported")
	_, err = store.Resolve("fake-integration", "mcp", testTenon, "plan9", "mips")
	wantStoreCode(t, err, "resolve.platform.unsupported")
}

func TestHTTPSOfflineFailure(t *testing.T) {
	store := newStore(t)
	m := hostManifest()
	art := m["artifacts"].([]any)[0].(map[string]any)
	delete(art, "package")
	art["https"] = "https://example.test/server"
	// Source carries only integration.json; the https bytes are not present by
	// pin and this slice fetches nothing.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "integration.json"), mustJSON(t, m), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Install(hostRequest(dir))
	wantStoreCode(t, err, "install.remote.unavailable")
}

func TestStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	store := newStore(t)
	if _, err := store.Install(hostRequest(writeSourceDir(t, hostManifest()))); err != nil {
		t.Fatal(err)
	}
	assertMode(t, store.base, 0o700)
	assertMode(t, store.packagesRoot(), 0o700)
	assertMode(t, store.packageDir("fake-integration"), 0o700)
	assertMode(t, filepath.Join(store.packageDir("fake-integration"), "state.json"), 0o600)
	assertMode(t, filepath.Join(store.packageDir("fake-integration"), "manifest.json"), 0o600)
	assertMode(t, store.blobPath(hashHex(fakePayload())), 0o600)
}

// corruptPreparedExec mutates a byte of the prepared executable so verification
// detects the change.
func corruptPreparedExec(t *testing.T, store *Store, installed *Installed) {
	t.Helper()
	as := installed.State.Artifacts[0]
	exec := filepath.Join(store.preparedPath(as.PreparedKey), filepath.FromSlash(as.ExecPath))
	data, err := os.ReadFile(exec)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0xff
	if err := os.WriteFile(exec, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != want {
		t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}

type tarEntry struct {
	name     string
	data     []byte
	typeflag byte
	linkname string
}

func writeTarGz(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: typeflag,
			Linkname: e.linkname,
			Mode:     0o600,
			Size:     int64(len(e.data)),
		}
		if typeflag == tar.TypeSymlink {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func writeZip(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
}
