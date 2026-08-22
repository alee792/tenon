package codex

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
// ceiling (ADR 0013) is measured against the fully rendered
// .codex/config.toml and reported as an error, which apply checks before it
// mutates the workspace.
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

// TestRemoteHeadersWarnForCodex proves declared remote headers, which the
// generated Codex configuration does not carry, are reported rather than
// silently dropped, while the url entry itself still renders.
func TestRemoteHeadersWarnForCodex(t *testing.T) {
	p := &agentproject.Project{
		Root: "/src/my-agent",
		Name: "my-agent",
		PluginServers: []agentproject.PluginServer{{
			Name:       "remote",
			Plugin:     "vendor-x",
			PluginRoot: "/src/my-agent/plugins/vendor-x",
			SourcePath: "plugins/vendor-x/mcp.json",
			Transport:  agentproject.TransportHTTP,
			URL:        "https://example.com/mcp",
			Headers:    map[string]string{"X-Trace": "on"},
		}},
	}
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)

	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.mcp.header.not-honored" && d.Severity == diagnostics.Warning &&
			d.Path == "plugins/vendor-x/mcp.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plugin.mcp.header.not-honored warning, got %v", diags.All())
	}
	for _, f := range files {
		if f.Path == ".codex/config.toml" {
			if !strings.Contains(string(f.Content), "[mcp_servers.remote]") ||
				strings.Contains(string(f.Content), "X-Trace") {
				t.Fatalf("remote server must render without headers: %s", f.Content)
			}
		}
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

// TestConnectionRendersAsCodexHTTPServer proves a standalone connection
// renders into .codex/config.toml as a startup-optional entry, and into the
// generated AGENTS.md's connections section (ADR 0016).
func TestConnectionRendersAsCodexHTTPServer(t *testing.T) {
	p := projectWithConnection("catalog", "https://example.com/mcp", "Use for the catalog.")
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var config, agentsMD string
	for _, f := range files {
		switch f.Path {
		case ".codex/config.toml":
			config = string(f.Content)
		case "AGENTS.md":
			agentsMD = string(f.Content)
		}
	}
	want := "\n[mcp_servers.catalog]\n" +
		`url = "https://example.com/mcp"` + "\n" +
		"required = false\n" +
		`default_tools_approval_mode = "prompt"` + "\n"
	if !strings.Contains(config, want) {
		t.Fatalf("expected exact connection entry in config.toml:\ngot:\n%s\nwant substring:\n%s", config, want)
	}
	if !strings.Contains(agentsMD, "### catalog") || !strings.Contains(agentsMD, "Use for the catalog.") {
		t.Fatalf("expected the connections section in AGENTS.md: %s", agentsMD)
	}
}

// TestClaudeReservedConnectionNamePassesForCodex proves the Claude-only
// native surface reservation (workspace, claude-in-chrome, computer-use) is
// a per-harness rule: those names are ordinary, accepted connection names
// for Codex.
func TestClaudeReservedConnectionNamePassesForCodex(t *testing.T) {
	for _, name := range []string{"workspace", "claude-in-chrome", "computer-use"} {
		p := projectWithConnection(name, "https://example.com/mcp", "")
		diags := &diagnostics.List{}
		files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)
		if diags.HasErrors() {
			t.Fatalf("name %q: unexpected diagnostics for codex: %v", name, diags.All())
		}
		found := false
		for _, f := range files {
			if f.Path == ".codex/config.toml" && strings.Contains(string(f.Content), "[mcp_servers."+name+"]") {
				found = true
			}
		}
		if !found {
			t.Fatalf("name %q: expected the connection to render for codex", name)
		}
	}
}

// --- Installed connections (ADR 0016 closing the installed form) ----------

const codexFixtureTenonVersion = "1.0.0"

func codexFixturePayload() []byte { return []byte("#!/bin/sh\necho fake-native-mcp\n") }

// installCodexFixture writes and installs a fixture native-mcp package into
// a fresh temp store, whose native server name equals serverName.
func installCodexFixture(t *testing.T, id, serverName string) string {
	t.Helper()
	p := codexFixturePayload()
	sum := sha256.Sum256(p)
	sha := hex.EncodeToString(sum[:])
	m := map[string]any{
		"schema_version": 1,
		"id":             id,
		"version":        "1.0.0",
		"name":           "Codex Driver Fixture",
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
				"codex": map[string]any{"startup": "optional"},
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
		Source: src, TrustOperator: true, TenonVersion: codexFixtureTenonVersion, OS: runtime.GOOS, Arch: runtime.GOARCH,
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

// TestInstalledConnectionRendersNativeCommandEntry proves a resolved
// installed connection renders into .codex/config.toml as a
// command/args/cwd/env entry from the launch descriptor, forwarding the
// required ambient name by name only through env_vars (ADR 0016) — never as
// a value.
func TestInstalledConnectionRendersNativeCommandEntry(t *testing.T) {
	base := installCodexFixture(t, "demo-pkg", "demo")
	p := projectWithInstalledConnection("demo", "demo-pkg", "mcp")
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{
		Workspace: "/ws", Executable: "/bin/tenon",
		IntegrationStore: base, TenonVersion: codexFixtureTenonVersion,
	}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var config string
	for _, f := range files {
		if f.Path == ".codex/config.toml" {
			config = string(f.Content)
		}
	}
	if !strings.Contains(config, "[mcp_servers.demo]") {
		t.Fatalf("missing the installed connection table: %s", config)
	}
	if !strings.Contains(config, filepath.Join("bin", "server")) {
		t.Fatalf("missing the prepared absolute executable: %s", config)
	}
	if !strings.Contains(config, `args = ["--stdio"]`) {
		t.Fatalf("missing the literal args: %s", config)
	}
	if !strings.Contains(config, `env = { "LOG_LEVEL" = "info" }`) {
		t.Fatalf("missing the non-secret env default: %s", config)
	}
	if !strings.Contains(config, `env_vars = ["DEMO_TOKEN"]`) {
		t.Fatalf("the required ambient name must be forwarded by name only: %s", config)
	}
	if !strings.Contains(config, "required = false") || !strings.Contains(config, `default_tools_approval_mode = "prompt"`) {
		t.Fatalf("an installed connection must stay startup-optional with native prompt approval: %s", config)
	}
}

// TestInstalledConnectionServerNameMismatchFailsForCodex proves a connection
// whose filename differs from the capability's declared native server name
// fails with connection.package.mismatch.
func TestInstalledConnectionServerNameMismatchFailsForCodex(t *testing.T) {
	base := installCodexFixture(t, "demo-pkg", "actual-name")
	p := projectWithInstalledConnection("demo", "demo-pkg", "mcp")
	diags := &diagnostics.List{}
	Driver{}.Generate(p, apply.Target{
		Workspace: "/ws", Executable: "/bin/tenon",
		IntegrationStore: base, TenonVersion: codexFixtureTenonVersion,
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
