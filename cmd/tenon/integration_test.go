package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// cliPayload is the deterministic fake executable bytes; it is never run.
func cliPayload() []byte { return []byte("#!/bin/sh\necho fake-native-mcp\n") }

func cliSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// cliSourceDir writes a valid host-targeted package source. The compatibility
// minimum sits below the development version constant so the CLI's own version
// resolves as compatible.
func cliSourceDir(t *testing.T) string {
	t.Helper()
	p := cliPayload()
	sha := cliSHA(p)
	m := map[string]any{
		"schema_version": 1,
		"id":             "fake-integration",
		"version":        "1.2.0",
		"name":           "Fake Integration",
		"description":    "A credential-free fake native MCP server for tests.",
		"license":        "MIT",
		"source":         "https://example.test/fake",
		"revision":       "abc123",
		"compat":         map[string]any{"minimum": "0.0.1", "before": "1.0.0"},
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
			},
		}},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "integration.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload", "server"), p, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// isolateStore points the CLI's default store at a temporary location for the
// duration of one test.
func isolateStore(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "")
}

func runInt(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runIntegration(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestIntegrationCLILifecycle(t *testing.T) {
	isolateStore(t)
	src := cliSourceDir(t)

	code, out, errb := runInt("install", src, "--trust", "operator")
	if code != 0 {
		t.Fatalf("install exit %d: %s", code, errb)
	}
	if !strings.Contains(out, "installed: package fake-integration") {
		t.Fatalf("install output: %q", out)
	}

	code, out, _ = runInt("list")
	if code != 0 || !strings.Contains(out, "fake-integration") || !strings.Contains(out, "enabled") {
		t.Fatalf("list output: %q", out)
	}

	code, out, errb = runInt("inspect", "fake-integration")
	if code != 0 {
		t.Fatalf("inspect exit %d: %s", code, errb)
	}
	if !strings.Contains(out, "server fake-server") || !strings.Contains(out, "FAKE_API_TOKEN") {
		t.Fatalf("inspect output: %q", out)
	}

	code, _, errb = runInt("verify", "fake-integration")
	if code != 0 {
		t.Fatalf("verify exit %d: %s", code, errb)
	}

	code, _, errb = runInt("disable", "fake-integration")
	if code != 0 {
		t.Fatalf("disable exit %d: %s", code, errb)
	}
	code, out, _ = runInt("list")
	if !strings.Contains(out, "disabled") {
		t.Fatalf("expected disabled after disable: %q", out)
	}
	code, _, errb = runInt("enable", "fake-integration")
	if code != 0 {
		t.Fatalf("enable exit %d: %s", code, errb)
	}

	code, _, errb = runInt("remove", "fake-integration")
	if code != 0 {
		t.Fatalf("remove exit %d: %s", code, errb)
	}
	code, out, _ = runInt("list")
	if !strings.Contains(out, "no packages installed") {
		t.Fatalf("expected empty list after remove: %q", out)
	}
}

func TestIntegrationCLIInstallRequiresTrust(t *testing.T) {
	isolateStore(t)
	src := cliSourceDir(t)
	code, _, errb := runInt("install", src)
	if code != 2 {
		t.Fatalf("install without trust exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "explicit trust decision; pass --trust operator") {
		t.Fatalf("stderr = %q", errb)
	}
}

func TestIntegrationCLIUnknownSubcommand(t *testing.T) {
	isolateStore(t)
	code, _, errb := runInt("frobnicate")
	if code != 2 || !strings.Contains(errb, "unknown subcommand") {
		t.Fatalf("code %d stderr %q", code, errb)
	}
}
