package agentproject

// LoadMCPSurface reports the composed MCP surface for `tenon mcp status`
// (issue #54): every authored connection, every accepted plugin-provided
// server, every plugin server shadowed by an authored connection, and every
// (non-dangling) masking declaration — the one OFFLINE view of an agent's
// entire MCP surface. It is built from the exact same loadPlugins and
// loadConnections calls Load itself uses, so composition is never
// duplicated: this file adds no new composition logic of its own, only a
// caller that asks for the shadow and mask bookkeeping loadConnections
// already computes and, for Load's own purposes, discards.
//
// Unlike Load, LoadMCPSurface never requires instructions.md or a supplied
// manifest to prove the root (status reports on the mcp/ and plugins/
// surface alone, independent of whether the rest of the project is even a
// valid agent), and one malformed connection, plugin, or mask never
// suppresses reporting the others.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alee792/tenon/internal/diagnostics"
)

// MCPSurface is the fully composed, offline MCP view of one agent (issue
// #54).
type MCPSurface struct {
	// Connections are the rendered, composed authored connections (ADR
	// 0016/0026): the winning side of any shadow, sorted by name. A mask
	// never appears here — see Masks.
	Connections []Connection
	// PluginServers are the accepted plugin-declared MCP servers with every
	// shadowed or masked name already removed — exactly what a driver would
	// render.
	PluginServers []PluginServer
	// Shadowed lists every accepted plugin server suppressed because an
	// authored connection of the same name won.
	Shadowed []ShadowedPluginServer
	// Masked lists every valid (non-dangling) masking declaration.
	Masked []MaskedPluginServer
}

// LoadMCPSurface loads and composes the MCP surface at root, entirely
// offline: a plugins/<name>.md reference resolves against whatever plugin
// cache the process already configured (ConfigurePluginCache), which itself
// performs no network operation, exactly like Load.
func LoadMCPSurface(root string) (*MCPSurface, *diagnostics.List, error) {
	diags := &diagnostics.List{}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, diags, fmt.Errorf("resolving agent root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		diags.Errorf("project.root.missing", ".", "the agent root must be an existing directory: %s", root)
		return nil, diags, nil
	}

	budget := &skillSetBudget{}
	_, pluginServers, skippedPluginServers, _ := loadPlugins(abs, budget, diags)
	connections, composedPluginServers, shadowed, masked, _ := loadConnections(abs, pluginServers, skippedPluginServers, true, diags)

	return &MCPSurface{
		Connections:   connections,
		PluginServers: composedPluginServers,
		Shadowed:      shadowed,
		Masked:        masked,
	}, diags, nil
}
