package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/generated"
	"github.com/alee792/tenon/internal/integration"
)

// TestGeneratedMCPConfigCeilingIsAnError proves the generated-configuration
// ceiling (ADR 0013) is measured against the fully rendered .mcp.json and
// reported as an error, which apply checks before it mutates the workspace.
func TestGeneratedMCPConfigCeilingIsAnError(t *testing.T) {
	p := &agentproject.Project{
		Root: "/src/my-agent",
		Name: "my-agent",
		PluginServers: []agentproject.PluginServer{{
			Name:       "huge",
			Plugin:     "vendor-x",
			PluginRoot: "/src/my-agent/plugins/vendor-x",
			SourcePath: "plugins/vendor-x/mcp.json",
			Transport:  agentproject.TransportStdio,
			Command:    "huge-server",
			Args:       []string{strings.Repeat("a", generated.MaxMCPConfigBytes)},
		}},
	}
	diags := &diagnostics.List{}
	Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)

	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.mcp.bounds.exceeded" && d.Severity == diagnostics.Error {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error plugin.mcp.bounds.exceeded, got %v", diags.All())
	}
}

func projectWithConnection(name, url, context string) *agentproject.Project {
	return &agentproject.Project{
		Root:         "/src/my-agent",
		Name:         "my-agent",
		Instructions: &agentproject.Instructions{Body: "Body text.\n"},
		Connections: []agentproject.Connection{
			{Name: name, URL: url, Context: context, SourcePath: "connections/" + name + ".md"},
		},
	}
}

// TestConnectionRendersAsNativeHTTPServer proves a standalone connection
// renders into .mcp.json as a native http entry with no headers field, and
// into the generated CLAUDE.md's connections section (ADR 0016).
func TestConnectionRendersAsNativeHTTPServer(t *testing.T) {
	p := projectWithConnection("catalog", "https://example.com/mcp", "Use for the catalog.")
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var mcpJSON []byte
	var claudeMD []byte
	for _, f := range files {
		switch f.Path {
		case ".mcp.json":
			mcpJSON = f.Content
		case "CLAUDE.md":
			claudeMD = f.Content
		}
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(mcpJSON, &doc); err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(doc.MCPServers["catalog"], &entry); err != nil {
		t.Fatal(err)
	}
	if entry["type"] != "http" || entry["url"] != "https://example.com/mcp" {
		t.Fatalf("connection entry = %+v", entry)
	}
	if _, hasHeaders := entry["headers"]; hasHeaders {
		t.Fatalf("a connection entry must never carry a headers field: %+v", entry)
	}
	if !strings.Contains(string(claudeMD), "### catalog") ||
		!strings.Contains(string(claudeMD), "Use for the catalog.") {
		t.Fatalf("expected the connections section in CLAUDE.md: %s", claudeMD)
	}
}

// TestClaudeReservedConnectionNameFailsForClaude proves the Claude-only
// native project surface names are rejected at generation for claude, with
// an error that stops apply before it mutates the workspace.
func TestClaudeReservedConnectionNameFailsForClaude(t *testing.T) {
	for _, name := range []string{"workspace", "claude-in-chrome", "computer-use"} {
		p := projectWithConnection(name, "https://example.com/mcp", "")
		diags := &diagnostics.List{}
		files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)

		found := false
		for _, d := range diags.All() {
			if d.ID == "connection.name.reserved" && d.Severity == diagnostics.Error &&
				d.Path == "connections/"+name+".md" {
				found = true
			}
		}
		if !found {
			t.Fatalf("name %q: expected an error connection.name.reserved, got %v", name, diags.All())
		}
		for _, f := range files {
			if f.Path == ".mcp.json" && strings.Contains(string(f.Content), `"`+name+`"`) {
				t.Fatalf("name %q: a reserved connection name must not render into .mcp.json: %s", name, f.Content)
			}
		}
	}
}

// --- Installed connections (ADR 0016 closing the installed form) ----------

const claudeFixtureTenonVersion = "1.0.0"

func claudeFixturePayload() []byte { return []byte("#!/bin/sh\necho fake-native-mcp\n") }

// installClaudeFixture writes and installs a fixture native-mcp package into
// a fresh temp store, whose native server name equals serverName, so a test
// can select it from a connection whose filename matches or mismatches.
func installClaudeFixture(t *testing.T, id, serverName string) string {
	t.Helper()
	p := claudeFixturePayload()
	sum := sha256.Sum256(p)
	sha := hex.EncodeToString(sum[:])
	m := map[string]any{
		"schema_version": 1,
		"id":             id,
		"version":        "1.0.0",
		"name":           "Claude Driver Fixture",
		"description":    "A credential-free fake native MCP server for driver tests.",
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
			},
		}},
	}
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
	if err := os.WriteFile(filepath.Join(src, "payload", "server"), p, 0o600); err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	store := integration.NewStore(base)
	if _, err := store.Install(integration.InstallRequest{
		Source: src, TrustOperator: true, TenonVersion: claudeFixtureTenonVersion, OS: runtime.GOOS, Arch: runtime.GOARCH,
	}); err != nil {
		t.Fatal(err)
	}
	return base
}

func projectWithInstalledConnection(name, pkg, capability string) *agentproject.Project {
	return &agentproject.Project{
		Root: "/src/my-agent",
		Name: "my-agent",
		Connections: []agentproject.Connection{
			{Kind: agentproject.ConnectionKindInstalled, Name: name, Package: pkg, Capability: capability, SourcePath: "connections/" + name + ".md"},
		},
	}
}

// TestInstalledConnectionRendersNativeStdioEntry proves a resolved installed
// connection renders into .mcp.json as the exact /usr/bin/env -C adapter
// entry, carrying only non-secret env defaults — never the required ambient
// name as a value, since Claude's project format inherits the launch
// environment instead (ADR 0016).
func TestInstalledConnectionRendersNativeStdioEntry(t *testing.T) {
	base := installClaudeFixture(t, "demo-pkg", "demo")
	p := projectWithInstalledConnection("demo", "demo-pkg", "mcp")
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{
		Workspace: "/ws", Executable: "/bin/tenon",
		IntegrationStore: base, TenonVersion: claudeFixtureTenonVersion,
	}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var mcpJSON []byte
	for _, f := range files {
		if f.Path == ".mcp.json" {
			mcpJSON = f.Content
		}
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(mcpJSON, &doc); err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	if err := json.Unmarshal(doc.MCPServers["demo"], &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Type != "stdio" || entry.Command != "/usr/bin/env" {
		t.Fatalf("entry = %+v", entry)
	}
	if len(entry.Args) < 4 || entry.Args[0] != "-C" || entry.Args[2] != "--" {
		t.Fatalf("args = %v", entry.Args)
	}
	if !filepath.IsAbs(entry.Args[1]) {
		t.Fatalf("workdir arg must be absolute: %v", entry.Args)
	}
	if !strings.HasSuffix(entry.Args[3], filepath.Join("bin", "server")) {
		t.Fatalf("executable arg must be the prepared absolute executable: %v", entry.Args)
	}
	if entry.Args[len(entry.Args)-1] != "--stdio" {
		t.Fatalf("literal args must be preserved: %v", entry.Args)
	}
	if entry.Env["LOG_LEVEL"] != "info" {
		t.Fatalf("non-secret env defaults must be preserved: %+v", entry.Env)
	}
	if _, leaked := entry.Env["DEMO_TOKEN"]; leaked {
		t.Fatalf("the required ambient name must never be written as an env value: %+v", entry.Env)
	}
}

// TestInstalledConnectionServerNameMismatchFailsBeforeMutation proves a
// connection whose filename differs from the capability's declared native
// server name fails with connection.package.mismatch and contributes no
// entry.
func TestInstalledConnectionServerNameMismatchFailsBeforeMutation(t *testing.T) {
	base := installClaudeFixture(t, "demo-pkg", "actual-name")
	p := projectWithInstalledConnection("demo", "demo-pkg", "mcp")
	diags := &diagnostics.List{}
	Driver{}.Generate(p, apply.Target{
		Workspace: "/ws", Executable: "/bin/tenon",
		IntegrationStore: base, TenonVersion: claudeFixtureTenonVersion,
	}, diags)

	found := false
	for _, d := range diags.All() {
		if d.ID == "connection.package.mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected connection.package.mismatch, got %v", diags.All())
	}
}

// TestInstalledConnectionUnconfiguredStoreFailsClearly proves an installed
// connection with no configured integration store fails with
// connection.package.unresolved rather than panicking.
func TestInstalledConnectionUnconfiguredStoreFailsClearly(t *testing.T) {
	p := projectWithInstalledConnection("demo", "demo-pkg", "mcp")
	diags := &diagnostics.List{}
	Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)

	found := false
	for _, d := range diags.All() {
		if d.ID == "connection.package.unresolved" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected connection.package.unresolved, got %v", diags.All())
	}
}
