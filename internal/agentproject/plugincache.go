package agentproject

// PluginCache resolves a plugin reference file's pinned revision to a
// verified, offline plugin tree (ADR 0026 "plugin acquisition by pointer and
// pin"). The concrete implementation (internal/pluginref) lives outside this
// package so agentproject depends on no fetch machinery — only this
// interface crosses the boundary, and nothing implementing it may be called
// from anywhere in this package except loadPluginReference, below.
type PluginCache interface {
	// Resolve returns the absolute path to rev's cached, digest-verified
	// plugin tree, or an error naming why resolution failed (no cache entry,
	// a digest mismatch). It must perform no network operation: Load never
	// fetches anything, by construction, because it never calls anything
	// but Resolve.
	Resolve(rev string) (root string, err error)
}

// pluginCache is the plugin cache every subsequent Load call consults to
// resolve plugins/<name>.md reference files, installed once by
// ConfigurePluginCache. It is a package-level seam rather than a per-call
// parameter deliberately: fetch is kept out of Load's call graph entirely
// (ADR 0026), so every tenon command that loads a project configures the
// same cache once at startup, exactly as it resolves one shared integration-
// store base for connections. A nil cache (the default, and always the case
// in every existing test that predates this feature) makes every
// plugins/<name>.md reference fail closed, naming `tenon plugin fetch`,
// while leaving every other authored form (instructions, skills, vendored
// plugins/<name>/ directories, subagents, tools, mcp/, schedules) completely
// unaffected.
var pluginCache PluginCache

// ConfigurePluginCache installs cache as the plugin reference resolver every
// subsequent Load, LoadWithManifest, and LoadForManifestWrite call consults.
// Passing nil restores the fail-closed default. It is not safe to call
// concurrently with Load; callers configure it once at process startup
// (cmd/tenon) or once per test.
func ConfigurePluginCache(cache PluginCache) { pluginCache = cache }
