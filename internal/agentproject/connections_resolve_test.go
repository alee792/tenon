package agentproject

// Tests for ResolveInstalledConnections, the one seam both native drivers
// share to resolve an installed connection against the operator's
// integration store (ADR 0014, ADR 0016).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/integration"
)

const fixtureTenonVersion = "1.0.0"

func fixturePayload() []byte { return []byte("#!/bin/sh\necho fake-native-mcp\n") }

// fixtureManifest builds a valid host-targeted native-mcp manifest whose
// package id and native server name are configurable, so a test can exercise
// both a matching and a mismatched connection filename.
func fixtureManifest(id, serverName string) map[string]any {
	p := fixturePayload()
	sum := sha256.Sum256(p)
	sha := hex.EncodeToString(sum[:])
	return map[string]any{
		"schema_version": 1,
		"id":             id,
		"version":        "1.0.0",
		"name":           "Resolve Fixture",
		"description":    "A credential-free fake native MCP server for resolution tests.",
		"license":        "MIT",
		"source":         "https://example.test/fixture",
		"revision":       "abc123",
		"compat":         map[string]any{"minimum": "0.0.1", "before": "2.0.0"},
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
			"server_name": serverName,
			"artifacts":   []any{"server-host"},
			"executable":  "bin/server",
			"args":        []any{"--stdio"},
			"workdir":     "",
			"env":         map[string]any{"LOG_LEVEL": "info"},
			"required_env": []any{
				map[string]any{"name": "DEMO_TOKEN", "description": "The ambient demo token the fixture server reads from its own environment."},
			},
			"targets": map[string]any{
				"claude": map[string]any{"startup": "optional"},
				"codex":  map[string]any{"startup": "optional"},
			},
		}},
	}
}

// installFixture writes a fixture native-mcp package source and installs it
// into a fresh temp store, returning the store's absolute base directory.
func installFixture(t *testing.T, id, serverName string) string {
	t.Helper()
	m := fixtureManifest(id, serverName)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "integration.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "payload", "server"), fixturePayload(), 0o600); err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	store := integration.NewStore(base)
	_, err = store.Install(integration.InstallRequest{
		Source:        src,
		TrustOperator: true,
		TenonVersion:  fixtureTenonVersion,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	})
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func installedConn(name, pkg, capability string) Connection {
	return Connection{
		Kind:       ConnectionKindInstalled,
		Name:       name,
		Package:    pkg,
		Capability: capability,
		SourcePath: "connections/" + name + ".md",
	}
}

// TestResolveInstalledConnectionsSuccess proves a matching connection
// resolves to a launch descriptor carrying the exact native launch data, and
// that the required ambient variable travels as a NAME only.
func TestResolveInstalledConnectionsSuccess(t *testing.T) {
	base := installFixture(t, "demo-pkg", "demo")
	diags := &diagnostics.List{}
	resolved := ResolveInstalledConnections([]Connection{installedConn("demo", "demo-pkg", "mcp")}, base, fixtureTenonVersion, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	desc, ok := resolved["demo"]
	if !ok {
		t.Fatal("expected a resolved descriptor for demo")
	}
	if desc.ServerName != "demo" {
		t.Errorf("server name = %q", desc.ServerName)
	}
	if !filepath.IsAbs(desc.Executable) || !filepath.IsAbs(desc.Workdir) {
		t.Errorf("executable and workdir must be absolute: %+v", desc)
	}
	if len(desc.RequiredEnv) != 1 || desc.RequiredEnv[0] != "DEMO_TOKEN" {
		t.Errorf("required env = %v", desc.RequiredEnv)
	}
	if _, leaked := desc.Env["DEMO_TOKEN"]; leaked {
		t.Errorf("the required ambient name must never appear as an env default")
	}
}

// TestResolveInstalledConnectionsServerNameMismatch proves a connection
// whose filename differs from the capability's declared server name fails
// with connection.package.mismatch before any generation proceeds.
func TestResolveInstalledConnectionsServerNameMismatch(t *testing.T) {
	base := installFixture(t, "demo-pkg", "actual-server-name")
	diags := &diagnostics.List{}
	resolved := ResolveInstalledConnections([]Connection{installedConn("demo", "demo-pkg", "mcp")}, base, fixtureTenonVersion, diags)
	if _, ok := resolved["demo"]; ok {
		t.Fatal("a mismatched connection must not resolve")
	}
	requireErrorID(t, diags, "connection.package.mismatch")
}

// TestResolveInstalledConnectionsMissingPackage proves an uninstalled
// package fails with connection.package.unresolved.
func TestResolveInstalledConnectionsMissingPackage(t *testing.T) {
	base := t.TempDir()
	diags := &diagnostics.List{}
	resolved := ResolveInstalledConnections([]Connection{installedConn("demo", "nope-pkg", "mcp")}, base, fixtureTenonVersion, diags)
	if _, ok := resolved["demo"]; ok {
		t.Fatal("an uninstalled package must not resolve")
	}
	requireErrorID(t, diags, "connection.package.unresolved")
}

// TestResolveInstalledConnectionsDisabledPackage proves a disabled package
// fails with connection.package.unresolved.
func TestResolveInstalledConnectionsDisabledPackage(t *testing.T) {
	base := installFixture(t, "demo-pkg", "demo")
	store := integration.NewStore(base)
	if err := store.Disable("demo-pkg"); err != nil {
		t.Fatal(err)
	}
	diags := &diagnostics.List{}
	resolved := ResolveInstalledConnections([]Connection{installedConn("demo", "demo-pkg", "mcp")}, base, fixtureTenonVersion, diags)
	if _, ok := resolved["demo"]; ok {
		t.Fatal("a disabled package must not resolve")
	}
	requireErrorID(t, diags, "connection.package.unresolved")
}

// TestResolveInstalledConnectionsEmptyStoreBase proves an unconfigured
// store (empty IntegrationStore) fails every installed connection with
// connection.package.unresolved rather than panicking or opening a store
// outside a configured base.
func TestResolveInstalledConnectionsEmptyStoreBase(t *testing.T) {
	diags := &diagnostics.List{}
	resolved := ResolveInstalledConnections([]Connection{installedConn("demo", "demo-pkg", "mcp")}, "", fixtureTenonVersion, diags)
	if _, ok := resolved["demo"]; ok {
		t.Fatal("an unconfigured store must not resolve")
	}
	requireErrorID(t, diags, "connection.package.unresolved")
}

// TestResolveInstalledConnectionsIgnoresRemote proves a remote connection is
// left untouched: it never contacts the store and never appears in the
// resolved map.
func TestResolveInstalledConnectionsIgnoresRemote(t *testing.T) {
	diags := &diagnostics.List{}
	remote := Connection{Kind: ConnectionKindRemote, Name: "catalog", URL: "https://example.com/mcp", SourcePath: "connections/catalog.md"}
	resolved := ResolveInstalledConnections([]Connection{remote}, "", fixtureTenonVersion, diags)
	if len(resolved) != 0 || diags.HasErrors() {
		t.Fatalf("a remote connection must be ignored: resolved=%v diags=%v", resolved, diags.All())
	}
}
