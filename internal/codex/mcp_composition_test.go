package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
)

// writeCompositionFixture mirrors internal/claude's fixture of the same
// name: one vendored plugin declaring three MCP servers ("catalog",
// "legacy", "other"), an authored mcp/catalog.md shadowing the plugin's
// "catalog" server, and an authored mcp/legacy.md masking the plugin's
// "legacy" server outright (#53 review: real end-to-end coverage in place
// of a hand-constructed Project).
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

// TestEndToEndCompositionRendersConfigTOML is the real end-to-end coverage
// the #53 review asked for (in place of the hand-constructed
// TestComposedProjectOmitsShadowedAndMaskedPluginServers above): a real
// on-disk project is loaded through agentproject.Load, so shadowing and
// masking are exercised for real, and the generated .codex/config.toml is
// asserted on its actual rendered text — including that the surviving
// plugin server "other" keeps its PLUGIN_ROOT and PLUGIN_DATA environment
// values.
func TestEndToEndCompositionRendersConfigTOML(t *testing.T) {
	root := writeCompositionFixture(t)
	p := loadFixture(t, root)

	workspace := t.TempDir()
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: workspace, Executable: "/bin/tenon"}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var toml string
	for _, f := range files {
		if f.Path == ".codex/config.toml" {
			toml = string(f.Content)
		}
	}
	if toml == "" {
		t.Fatal("expected .codex/config.toml among the generated files")
	}

	if strings.Contains(toml, "[mcp_servers.legacy]") {
		t.Fatalf("the masked plugin server legacy must never render:\n%s", toml)
	}
	if !strings.Contains(toml, "[mcp_servers.catalog]") || !strings.Contains(toml, `url = "https://example.com/mcp"`) {
		t.Fatalf("expected the authored, shadowing catalog server with its own URL to render:\n%s", toml)
	}
	if strings.Contains(toml, "https://plugin.example/catalog") {
		t.Fatalf("the plugin's own shadowed catalog URL must never render:\n%s", toml)
	}
	if !strings.Contains(toml, "[mcp_servers.other]") {
		t.Fatalf("expected the surviving plugin server other to render:\n%s", toml)
	}
	pluginRoot := filepath.Join(root, "plugins", "vendor-x")
	wantData := filepath.Join(workspace, ".tenon", "plugin-data", "my-agent", "vendor-x")
	otherSection := toml[strings.Index(toml, "[mcp_servers.other]"):]
	if !strings.Contains(otherSection, `"PLUGIN_ROOT" = "`+pluginRoot+`"`) {
		t.Fatalf("expected other's env to carry PLUGIN_ROOT = %q:\n%s", pluginRoot, otherSection)
	}
	if !strings.Contains(otherSection, `"PLUGIN_DATA" = "`+wantData+`"`) {
		t.Fatalf("expected other's env to carry PLUGIN_DATA = %q:\n%s", wantData, otherSection)
	}
	if !strings.Contains(otherSection, `"MODE" = "fast"`) {
		t.Fatalf("expected the plugin's own declared env to survive alongside PLUGIN_ROOT/PLUGIN_DATA:\n%s", otherSection)
	}
}
