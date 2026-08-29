package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
)

// writeCompositionFixture writes an on-disk agent carrying: one vendored
// plugin declaring three MCP servers ("catalog", "legacy", "other"), an
// authored mcp/catalog.md that shadows the plugin's "catalog" server, and
// an authored mcp/legacy.md that masks the plugin's "legacy" server
// outright — the exact end-to-end fixture the #53 review asked for in place
// of a hand-constructed Project, so this test exercises the real Load
// composition (shadowing, masking, first-wins) rather than asserting on a
// Project a test author already pre-composed by hand.
func writeCompositionFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "my-agent")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(root, 0o755))
	must(os.WriteFile(filepath.Join(root, "instructions.md"),
		[]byte("---\ndescription: Fixture agent.\n---\n\nBody.\n"), 0o644))

	pluginRoot := filepath.Join(root, "plugins", "vendor-x")
	must(os.MkdirAll(filepath.Join(pluginRoot, "bin"), 0o755))
	must(os.WriteFile(filepath.Join(pluginRoot, "plugin.json"),
		[]byte(`{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "vendor-x"}`), 0o644))
	must(os.WriteFile(filepath.Join(pluginRoot, "bin", "serve"), []byte("#!/bin/sh\nexec cat\n"), 0o755))
	must(os.WriteFile(filepath.Join(pluginRoot, "mcp.json"), []byte(`{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "catalog": {"type": "streamable-http", "url": "https://plugin.example/catalog"},
    "legacy": {"command": "./bin/serve"},
    "other": {"command": "./bin/serve", "env": {"MODE": "fast"}}
  }
}`), 0o644))

	must(os.MkdirAll(filepath.Join(root, "mcp"), 0o755))
	must(os.WriteFile(filepath.Join(root, "mcp", "catalog.md"),
		[]byte("---\ntype: streamable-http\nurl: https://example.com/mcp\n---\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "mcp", "legacy.md"),
		[]byte("---\noverride: plugins/vendor-x\nenabled: false\n---\n"), 0o644))

	return root
}

func loadFixture(t *testing.T, root string) *agentproject.Project {
	t.Helper()
	p, diags, err := agentproject.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected load failure: %v", diags.All())
	}
	return p
}

// TestEndToEndCompositionRendersMCPJSON is the real end-to-end coverage the
// #53 review asked for (in place of the hand-constructed
// TestComposedProjectOmitsShadowedAndMaskedPluginServers above): a real
// on-disk project is loaded through agentproject.Load, so shadowing and
// masking are exercised for real, and the generated .mcp.json is asserted
// on its actual decoded shape — including that the surviving plugin server
// "other" keeps its PLUGIN_ROOT and PLUGIN_DATA environment values.
func TestEndToEndCompositionRendersMCPJSON(t *testing.T) {
	root := writeCompositionFixture(t)
	p := loadFixture(t, root)

	workspace := t.TempDir()
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: workspace, Executable: "/bin/tenon"}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var mcpJSON []byte
	for _, f := range files {
		if f.Path == ".mcp.json" {
			mcpJSON = f.Content
		}
	}
	if mcpJSON == nil {
		t.Fatal("expected .mcp.json among the generated files")
	}

	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(mcpJSON, &doc); err != nil {
		t.Fatalf("decoding .mcp.json: %v\n%s", err, mcpJSON)
	}

	if _, ok := doc.MCPServers["legacy"]; ok {
		t.Fatalf("the masked plugin server legacy must never render: %v", doc.MCPServers)
	}
	catalog, ok := doc.MCPServers["catalog"]
	if !ok {
		t.Fatalf("expected the authored, shadowing catalog server to render: %v", doc.MCPServers)
	}
	if catalog["type"] != "http" || catalog["url"] != "https://example.com/mcp" {
		t.Fatalf("expected the authored catalog server's own URL to win over the plugin's: %+v", catalog)
	}
	other, ok := doc.MCPServers["other"]
	if !ok {
		t.Fatalf("expected the surviving plugin server other to render: %v", doc.MCPServers)
	}
	env, _ := other["env"].(map[string]any)
	if env == nil {
		t.Fatalf("expected other to carry an env map: %+v", other)
	}
	pluginRoot := filepath.Join(root, "plugins", "vendor-x")
	if env["PLUGIN_ROOT"] != pluginRoot {
		t.Fatalf("expected PLUGIN_ROOT = %q, got %+v", pluginRoot, env)
	}
	wantData := filepath.Join(workspace, ".tenon", "plugin-data", "my-agent", "vendor-x")
	if env["PLUGIN_DATA"] != wantData {
		t.Fatalf("expected PLUGIN_DATA = %q, got %+v", wantData, env)
	}
	if env["MODE"] != "fast" {
		t.Fatalf("expected the plugin's own declared env to survive alongside PLUGIN_ROOT/PLUGIN_DATA: %+v", env)
	}
}
