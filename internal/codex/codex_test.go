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
