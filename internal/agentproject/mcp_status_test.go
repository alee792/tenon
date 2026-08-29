package agentproject

import "testing"

// TestLoadMCPSurfaceReportsFullComposition proves LoadMCPSurface (issue #54)
// reports the entire composed MCP surface offline: the winning authored
// connection, the surviving plugin server, the plugin server an authored
// connection shadows, and a valid masking declaration — using the exact
// same composition Load itself performs, never a duplicated computation.
func TestLoadMCPSurfaceReportsFullComposition(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(
		`"catalog": {"command": "server"}, "legacy": {"command": "old-server"}, "other": {"command": "server2"}`))
	writeConnectionFile(t, root, "catalog.md", remoteConnection("https://example.com/mcp", ""))
	writeConnectionFile(t, root, "legacy.md", maskConnection("plugins/vendor-x", false))

	surface, diags, err := LoadMCPSurface(root)
	if err != nil {
		t.Fatal(err)
	}
	if surface == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	if len(surface.Connections) != 1 || surface.Connections[0].Name != "catalog" {
		t.Fatalf("expected exactly the authored catalog connection to render: %+v", surface.Connections)
	}

	if len(surface.PluginServers) != 1 || surface.PluginServers[0].Name != "other" {
		t.Fatalf("expected exactly the surviving plugin server other: %+v", surface.PluginServers)
	}

	if len(surface.Shadowed) != 1 || surface.Shadowed[0].Server.Name != "catalog" || surface.Shadowed[0].ShadowedBy != "mcp/catalog.md" {
		t.Fatalf("expected catalog reported shadowed by mcp/catalog.md: %+v", surface.Shadowed)
	}

	if len(surface.Masked) != 1 || surface.Masked[0].Name != "legacy" || surface.Masked[0].Override != "vendor-x" ||
		surface.Masked[0].SourcePath != "mcp/legacy.md" {
		t.Fatalf("expected legacy reported masked by mcp/legacy.md: %+v", surface.Masked)
	}
}

// TestLoadMCPSurfaceIndependentOfInstructions proves LoadMCPSurface reports
// the MCP surface even when the rest of the project is not a valid agent at
// all (no instructions.md, no supplied manifest): the status view is a
// question about mcp/ and plugins/ alone, not about the whole project's
// validity.
func TestLoadMCPSurfaceIndependentOfInstructions(t *testing.T) {
	root := writeAgent(t, "agent", "") // no instructions.md: an unproven root for Load
	writeConnectionFile(t, root, "catalog.md", remoteConnection("https://example.com/mcp", ""))

	surface, diags, err := LoadMCPSurface(root)
	if err != nil {
		t.Fatal(err)
	}
	if surface == nil {
		t.Fatalf("expected a surface even though the root is unproven for Load: %v", diags.All())
	}
	if len(surface.Connections) != 1 || surface.Connections[0].Name != "catalog" {
		t.Fatalf("expected the catalog connection reported: %+v", surface.Connections)
	}
}

// TestLoadMCPSurfaceMaskNeverFalselyDangling proves a bare status view on a
// legitimate mask never reports mcp.override.dangling (the regression fixed
// alongside issue #54): unlike LoadConnectionsForStatus, LoadMCPSurface
// loads plugins itself, so the mask's override resolves for real.
func TestLoadMCPSurfaceMaskNeverFalselyDangling(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(`"catalog": {"command": "server"}`))
	writeConnectionFile(t, root, "catalog.md", maskConnection("plugins/vendor-x", false))

	surface, diags, err := LoadMCPSurface(root)
	if err != nil {
		t.Fatal(err)
	}
	if diags.HasErrors() {
		t.Fatalf("a legitimate mask must never report an error: %v", diags.All())
	}
	if len(surface.Masked) != 1 {
		t.Fatalf("expected the mask reported: %+v", surface.Masked)
	}
}
