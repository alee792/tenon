package codex

import (
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/generated"
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
