package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/generated"
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
