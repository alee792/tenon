package agentproject

// Installed connections resolve offline against the operator's integration
// store at generation time (ADR 0016, consuming ADR 0014's Store.Resolve).
// This file is the one shared resolution seam both native drivers
// (internal/claude, internal/codex) call, so the diagnostics and the failure
// categories are identical for both harnesses. Resolution never starts a
// process, never resolves an ambient value, and never writes configuration:
// it only ever returns NAMES for required ambient environment variables.

import (
	"runtime"

	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/integration"
)

// ResolveInstalledConnections resolves every installed connection in
// connections against the integration store rooted at storeBase, checking
// compatibility against tenonVersion and the running host's GOOS/GOARCH. A
// remote connection is ignored. On success the connection's exact
// server-name match is also verified before the descriptor is returned; a
// mismatch fails before mutation like every other collision (ADR 0016). Every
// failure is reported as a bounded diagnostic naming the connection's
// authored path and never carries a secret, a resolved ambient value, or raw
// store internals. The returned map carries only the connections that
// resolved cleanly; callers must skip generating a native entry for any
// installed connection absent from it, since its failure is already on
// diags.
func ResolveInstalledConnections(connections []Connection, storeBase, tenonVersion string, diags *diagnostics.List) map[string]*integration.LaunchDescriptor {
	out := map[string]*integration.LaunchDescriptor{}
	var store *integration.Store
	if storeBase != "" {
		store = integration.NewStore(storeBase)
	}
	for _, c := range connections {
		if c.Kind != ConnectionKindInstalled {
			continue
		}
		if store == nil {
			diags.Errorf("connection.package.unresolved", c.SourcePath,
				"connection %q selects installed package %q capability %q, but no integration store is configured",
				c.Name, c.Package, c.Capability)
			continue
		}
		desc, err := store.Resolve(c.Package, c.Capability, tenonVersion, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			diags.Errorf("connection.package.unresolved", c.SourcePath,
				"connection %q could not be resolved against the integration store (package %q, capability %q): %s",
				c.Name, c.Package, c.Capability, diagnostics.Bound(err.Error(), 256))
			continue
		}
		if desc.ServerName != c.Name {
			diags.Errorf("connection.package.mismatch", c.SourcePath,
				"connection %q selects capability %q of package %q, whose declared native server name %q does not equal the connection filename",
				c.Name, c.Capability, c.Package, desc.ServerName)
			continue
		}
		out[c.Name] = desc
	}
	return out
}
