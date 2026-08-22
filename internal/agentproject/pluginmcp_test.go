package agentproject

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/diagnostics"
)

// mcpDoc wraps declared servers in a valid Agent Plugins v1.0.0 MCP document.
func mcpDoc(servers string) string {
	return fmt.Sprintf(`{"$schema": %q, "mcpServers": {%s}}`, pluginMCPSchemaID, servers)
}

func writePluginMCP(t *testing.T, root, pluginDir, content string) {
	t.Helper()
	writeSkillFile(t, root, "plugins/"+pluginDir+"/mcp.json", []byte(content), 0o644)
}

// pluginWithServers writes an agent carrying one valid plugin whose mcp.json
// declares servers, and loads it.
func pluginWithServers(t *testing.T, servers string) (*Project, []string) {
	t.Helper()
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(servers))
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("a plugin MCP violation must never fail the project: %v", diags.All())
	}
	return p, warningIDs(diags)
}

// requireNoMCPWarnings proves nothing about the plugin's MCP component was
// warned. plugin.component.unsupported is ignored here: it covers component
// locations (commands, agents, hooks), which are orthogonal to the MCP
// contract under test.
func requireNoMCPWarnings(t *testing.T, diags *diagnostics.List) {
	t.Helper()
	for _, id := range warningIDs(diags) {
		if strings.HasPrefix(id, "plugin.mcp.") {
			t.Fatalf("unexpected MCP diagnostics: %v", diags.All())
		}
	}
}

func serverNames(p *Project) []string {
	var names []string
	for _, s := range p.PluginServers {
		names = append(names, s.Name)
	}
	return names
}

// realRoot resolves the plugin's real absolute root, which is the
// ${PLUGIN_ROOT} value and the containment boundary.
func realRoot(t *testing.T, agentRoot, plugin string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(filepath.Join(agentRoot, "plugins", plugin))
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

const stdioServer = `"alpha": {"command": "alpha-server"}`

// TestPluginMCPAcceptsSupportedTransports proves a well-formed document
// contributes both supported transports in lexical order, with the plugin
// identity and authored path each accepted server needs.
func TestPluginMCPAcceptsSupportedTransports(t *testing.T) {
	p, warnings := pluginWithServers(t, `
		"remote": {"type": "streamable-http", "url": "https://example.com/mcp", "headers": {"X-Trace": "on"}},
		"local": {"type": "stdio", "command": "alpha-server", "args": ["--serve"], "env": {"MODE": "fast"}}`)
	if len(warnings) != 0 {
		t.Fatalf("a valid document must warn about nothing: %v", warnings)
	}
	if got := serverNames(p); len(got) != 2 || got[0] != "local" || got[1] != "remote" {
		t.Fatalf("servers = %v, want the declared servers in lexical order", got)
	}
	local := p.PluginServers[0]
	if local.Transport != TransportStdio || local.Command != "alpha-server" ||
		len(local.Args) != 1 || local.Args[0] != "--serve" || local.Env["MODE"] != "fast" {
		t.Fatalf("stdio server = %+v", local)
	}
	if local.Plugin != "vendor-x" || local.SourcePath != "plugins/vendor-x/mcp.json" {
		t.Fatalf("stdio server identity = %+v", local)
	}
	remote := p.PluginServers[1]
	if remote.Transport != TransportHTTP || remote.URL != "https://example.com/mcp" ||
		remote.Headers["X-Trace"] != "on" {
		t.Fatalf("remote server = %+v", remote)
	}
}

// TestPluginMCPInvalidDocumentDisablesOnlyThatComponent proves every
// malformed top-level document warns once, contributes no server, and leaves
// the plugin's skills importing.
func TestPluginMCPInvalidDocumentDisablesOnlyThatComponent(t *testing.T) {
	cases := map[string]string{
		"bad json":              `{"$schema": "` + pluginMCPSchemaID + `", "mcpServers": {`,
		"wrong schema":          `{"$schema": "https://example.com/other.json", "mcpServers": {}}`,
		"missing schema":        `{"mcpServers": {}}`,
		"missing mcpServers":    `{"$schema": "` + pluginMCPSchemaID + `"}`,
		"mcpServers not object": `{"$schema": "` + pluginMCPSchemaID + `", "mcpServers": []}`,
		"document not object":   `["mcpServers"]`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
			writePluginMCP(t, root, "vendor-x", doc)
			writeSkillFile(t, root, "plugins/vendor-x/skills/fine/SKILL.md", []byte(minimalSkillMD("fine")), 0o644)

			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if p == nil || diags.HasErrors() {
				t.Fatalf("a malformed mcp.json must not fail the project: %v", diags.All())
			}
			requireWarningID(t, diags, "plugin.mcp.invalid")
			if len(p.PluginServers) != 0 {
				t.Fatalf("servers = %+v, want the component disabled", p.PluginServers)
			}
			if len(p.Skills) != 1 || p.Skills[0].Name != "fine" {
				t.Fatalf("skills = %+v, want the plugin's skills still imported", p.Skills)
			}
		})
	}
}

// TestPluginMCPInvalidServerSkipsOnlyThatServer is the per-server validation
// matrix: every violation warns at the document's authored path, skips
// exactly one server, and never suppresses a valid sibling.
func TestPluginMCPInvalidServerSkipsOnlyThatServer(t *testing.T) {
	cases := map[string]string{
		"sse transport":       `"bad": {"type": "sse", "url": "https://example.com/mcp"}`,
		"unknown transport":   `"bad": {"type": "websocket", "command": "x"}`,
		"non-string type":     `"bad": {"type": 7, "command": "x"}`,
		"not an object":       `"bad": "alpha-server"`,
		"unknown field":       `"bad": {"command": "x", "timeout": 5}`,
		"url on stdio":        `"bad": {"command": "x", "url": "https://example.com/mcp"}`,
		"missing command":     `"bad": {"args": ["--serve"]}`,
		"empty command":       `"bad": {"command": ""}`,
		"path command":        `"bad": {"command": "bin/serve"}`,
		"parent command":      `"bad": {"command": "../serve"}`,
		"absolute command":    `"bad": {"command": "/usr/bin/serve"}`,
		"placeholder command": `"bad": {"command": "${SERVER}"}`,
		"non-string arg":      `"bad": {"command": "x", "args": [7]}`,
		"args not array":      `"bad": {"command": "x", "args": "--serve"}`,
		"env not object":      `"bad": {"command": "x", "env": "MODE=fast"}`,
		"non-string env":      `"bad": {"command": "x", "env": {"MODE": 7}}`,
		"empty cwd":           `"bad": {"command": "x", "cwd": ""}`,
		"escaping cwd":        `"bad": {"command": "x", "cwd": "./.."}`,
		"missing cwd":         `"bad": {"command": "x", "cwd": "./nowhere"}`,
		"uppercase name":      `"Bad": {"command": "x"}`,
		"leading digit name":  `"1bad": {"command": "x"}`,
		"underscore name":     `"bad_name": {"command": "x"}`,
		"empty name":          `"": {"command": "x"}`,
		"missing url":         `"bad": {"type": "streamable-http"}`,
		"empty url":           `"bad": {"type": "streamable-http", "url": ""}`,
		"headers not object":  `"bad": {"type": "streamable-http", "url": "https://example.com/mcp", "headers": "X-Trace: on"}`,
		"header not string":   `"bad": {"type": "streamable-http", "url": "https://example.com/mcp", "headers": {"X-Trace": 1}}`,
	}
	for name, declaration := range cases {
		t.Run(name, func(t *testing.T) {
			p, warnings := pluginWithServers(t, declaration+",\n"+stdioServer)
			if got := serverNames(p); len(got) != 1 || got[0] != "alpha" {
				t.Fatalf("servers = %v, want only the valid sibling", got)
			}
			if len(warnings) != 1 || warnings[0] != "plugin.mcp.server.invalid" {
				t.Fatalf("warnings = %v, want exactly one plugin.mcp.server.invalid", warnings)
			}
		})
	}
}

// TestPluginMCPServerNameLengthBoundary proves the portable grammar's
// 63-character ceiling is exact.
func TestPluginMCPServerNameLengthBoundary(t *testing.T) {
	longest := "a" + strings.Repeat("b", 62)
	p, warnings := pluginWithServers(t, fmt.Sprintf(`%q: {"command": "x"}`, longest))
	if len(warnings) != 0 || len(p.PluginServers) != 1 {
		t.Fatalf("a 63-character name must be accepted: %v %v", warnings, serverNames(p))
	}
	p, warnings = pluginWithServers(t, fmt.Sprintf(`%q: {"command": "x"}`, longest+"c"))
	if len(p.PluginServers) != 0 || len(warnings) != 1 || warnings[0] != "plugin.mcp.server.invalid" {
		t.Fatalf("a 64-character name must warn and skip: %v %v", warnings, serverNames(p))
	}
}

// TestPluginMCPManagedNameIsReserved proves a plugin may not claim tenon's
// own managed server name, and is skipped rather than renamed.
func TestPluginMCPManagedNameIsReserved(t *testing.T) {
	p, warnings := pluginWithServers(t, `"managed": {"command": "impostor"},`+"\n"+stdioServer)
	if got := serverNames(p); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("servers = %v, want the reserved name skipped and never renamed", got)
	}
	if len(warnings) != 1 || warnings[0] != "plugin.mcp.server.collision" {
		t.Fatalf("warnings = %v, want exactly one plugin.mcp.server.collision", warnings)
	}
}

// TestPluginMCPFirstServerNameWinsAcrossPlugins proves acceptance order —
// plugin directories lexically, servers lexically — and that a later
// duplicate is skipped with a warning naming both authored paths.
func TestPluginMCPFirstServerNameWinsAcrossPlugins(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "aaa-plugin", validPluginJSON("aaa-plugin"))
	writePluginMCP(t, root, "aaa-plugin", mcpDoc(`"shared": {"command": "first"}, "solo": {"command": "solo"}`))
	writePluginManifest(t, root, "zzz-plugin", validPluginJSON("zzz-plugin"))
	writePluginMCP(t, root, "zzz-plugin", mcpDoc(`"shared": {"command": "second"}`))

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if got := serverNames(p); len(got) != 2 || got[0] != "shared" || got[1] != "solo" {
		t.Fatalf("servers = %v, want the first plugin's servers in lexical order", got)
	}
	if p.PluginServers[0].Command != "first" {
		t.Fatalf("winning server = %+v, want the lexically first plugin's declaration", p.PluginServers[0])
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.mcp.server.collision" && d.Path == "plugins/zzz-plugin/mcp.json" &&
			strings.Contains(d.Rule, "plugins/aaa-plugin/mcp.json") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a collision warning naming both authored paths, got %v", diags.All())
	}
}

// TestPluginMCPRemoteURLMatrix proves the portable remote endpoint rules.
func TestPluginMCPRemoteURLMatrix(t *testing.T) {
	accepted := map[string]string{
		"https":              "https://example.com/mcp",
		"loopback http":      "http://127.0.0.1:8080/mcp",
		"localhost http":     "http://localhost:8080/mcp",
		"ipv6 loopback http": "http://[::1]:8080/mcp",
	}
	for name, remote := range accepted {
		t.Run("accepted "+name, func(t *testing.T) {
			p, warnings := pluginWithServers(t, fmt.Sprintf(`"remote": {"type": "streamable-http", "url": %q}`, remote))
			if len(p.PluginServers) != 1 || len(warnings) != 0 {
				t.Fatalf("%q must be accepted: %v %v", remote, warnings, serverNames(p))
			}
		})
	}
	rejected := map[string]string{
		"non-loopback http": "http://example.com/mcp",
		"user info":         "https://user:pass@example.com/mcp",
		"fragment":          "https://example.com/mcp#tools",
		"relative":          "/mcp",
		"no host":           "https:///mcp",
		"other scheme":      "ws://example.com/mcp",
		"not a url":         "https://exa mple.com/mcp",
	}
	for name, remote := range rejected {
		t.Run("rejected "+name, func(t *testing.T) {
			p, warnings := pluginWithServers(t,
				fmt.Sprintf(`"remote": {"type": "streamable-http", "url": %q},`, remote)+"\n"+stdioServer)
			if got := serverNames(p); len(got) != 1 || got[0] != "alpha" {
				t.Fatalf("%q must warn and skip: %v", remote, got)
			}
			if len(warnings) != 1 || warnings[0] != "plugin.mcp.server.invalid" {
				t.Fatalf("warnings = %v, want exactly one plugin.mcp.server.invalid", warnings)
			}
		})
	}
}

// TestPluginMCPHeaderMatrix proves headers are validated as HTTP headers,
// rejected on a case-insensitive collision, and otherwise copied literally.
func TestPluginMCPHeaderMatrix(t *testing.T) {
	remote := func(headers string) string {
		return fmt.Sprintf(`"remote": {"type": "streamable-http", "url": "https://example.com/mcp", "headers": {%s}}`, headers)
	}
	t.Run("copied literally", func(t *testing.T) {
		p, warnings := pluginWithServers(t, remote(`"X-Trace": "on", "Accept": "application/json"`))
		if len(warnings) != 0 || len(p.PluginServers) != 1 {
			t.Fatalf("valid headers must be accepted: %v", warnings)
		}
		got := p.PluginServers[0].Headers
		if got["X-Trace"] != "on" || got["Accept"] != "application/json" {
			t.Fatalf("headers = %v, want the authored values copied literally", got)
		}
	})
	rejected := map[string]string{
		"invalid name":               `"X Trace": "on"`,
		"empty name":                 `"": "on"`,
		"control character value":    `"X-Trace": "on\nX-Other: off"`,
		"case-insensitive collision": `"X-Trace": "on", "x-trace": "off"`,
	}
	for name, headers := range rejected {
		t.Run("rejected "+name, func(t *testing.T) {
			p, warnings := pluginWithServers(t, remote(headers)+",\n"+stdioServer)
			if got := serverNames(p); len(got) != 1 || got[0] != "alpha" {
				t.Fatalf("%s must warn and skip: %v", name, got)
			}
			if len(warnings) != 1 || warnings[0] != "plugin.mcp.server.invalid" {
				t.Fatalf("warnings = %v, want exactly one plugin.mcp.server.invalid", warnings)
			}
		})
	}
}

// TestPluginMCPRelativeCommandStaysInsideThePluginTree proves a "./" command
// resolves to a real bounded path inside the plugin, that its content and
// executable intent join the fingerprint, and that an escape attempt or a
// symlink warns and skips.
func TestPluginMCPRelativeCommandStaysInsideThePluginTree(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
		writeSkillFile(t, root, "plugins/vendor-x/bin/serve", []byte("#!/bin/sh\nexec cat\n"), 0o755)
		writePluginMCP(t, root, "vendor-x", mcpDoc(`"alpha": {"command": "./bin/serve"}`))

		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || diags.HasErrors() {
			t.Fatalf("unexpected diagnostics: %v", diags.All())
		}
		requireNoMCPWarnings(t, diags)
		want := filepath.Join(realRoot(t, root, "vendor-x"), "bin", "serve")
		if len(p.PluginServers) != 1 || p.PluginServers[0].Command != want {
			t.Fatalf("command = %+v, want the absolute real path %q", p.PluginServers, want)
		}
	})
	t.Run("escape attempt", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
		writePluginManifest(t, root, "vendor-y", validPluginJSON("vendor-y"))
		writeSkillFile(t, root, "plugins/vendor-y/bin/serve", []byte("#!/bin/sh\n"), 0o755)
		writePluginMCP(t, root, "vendor-x", mcpDoc(`"alpha": {"command": "./../vendor-y/bin/serve"}`))

		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || diags.HasErrors() {
			t.Fatalf("a path escape must warn, not fail: %v", diags.All())
		}
		if len(p.PluginServers) != 0 {
			t.Fatalf("servers = %+v, want the escaping command skipped", p.PluginServers)
		}
		requireWarningID(t, diags, "plugin.mcp.server.invalid")
	})
	t.Run("symlinked command", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
		target := filepath.Join(t.TempDir(), "real-serve")
		if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "plugins", "vendor-x", "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "plugins", "vendor-x", "bin", "serve")); err != nil {
			t.Fatal(err)
		}
		writePluginMCP(t, root, "vendor-x", mcpDoc(`"alpha": {"command": "./bin/serve"}`))

		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || diags.HasErrors() {
			t.Fatalf("a symlinked command must warn, not fail: %v", diags.All())
		}
		if len(p.PluginServers) != 0 {
			t.Fatalf("servers = %+v, want the symlinked command skipped", p.PluginServers)
		}
		requireWarningID(t, diags, "plugin.mcp.server.invalid")
	})
}

// TestPluginMCPCommandContentJoinsFingerprint proves a plugin-relative
// command's content and executable intent are source, exactly like any other
// authored input.
func TestPluginMCPCommandContentJoinsFingerprint(t *testing.T) {
	build := func(t *testing.T, script string, mode os.FileMode) (string, string) {
		t.Helper()
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
		writeSkillFile(t, root, "plugins/vendor-x/bin/serve", []byte(script), mode)
		writePluginMCP(t, root, "vendor-x", mcpDoc(`"alpha": {"command": "./bin/serve"}`))
		p, diags, err := Load(root)
		if err != nil || p == nil || diags.HasErrors() {
			t.Fatalf("load failed: %v %v", err, diags.All())
		}
		return p.Fingerprint, root
	}
	base, _ := build(t, "#!/bin/sh\nexec cat\n", 0o755)
	if again, _ := build(t, "#!/bin/sh\nexec cat\n", 0o755); again != base {
		t.Fatal("identical plugin command source must fingerprint identically")
	}
	if changed, _ := build(t, "#!/bin/sh\nexec tee\n", 0o755); changed == base {
		t.Fatal("changing a plugin command's content must change the fingerprint")
	}
	if mode, _ := build(t, "#!/bin/sh\nexec cat\n", 0o644); mode == base {
		t.Fatal("changing a plugin command's executable intent must change the fingerprint")
	}
}

// TestPluginMCPWorkingDirectoryStaysInsideThePluginTree proves a declared
// working directory is accepted only when it names a real directory inside
// the plugin, and that ${PLUGIN_ROOT} may address it.
func TestPluginMCPWorkingDirectoryStaysInsideThePluginTree(t *testing.T) {
	for name, cwd := range map[string]string{
		"relative":    "./work",
		"plugin root": "${PLUGIN_ROOT}/work",
		"root itself": "${PLUGIN_ROOT}",
	} {
		t.Run("accepted "+name, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
			if err := os.MkdirAll(filepath.Join(root, "plugins", "vendor-x", "work"), 0o755); err != nil {
				t.Fatal(err)
			}
			writePluginMCP(t, root, "vendor-x", mcpDoc(fmt.Sprintf(`"alpha": {"command": "x", "cwd": %q}`, cwd)))
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if p == nil || diags.HasErrors() {
				t.Fatalf("unexpected diagnostics: %v", diags.All())
			}
			requireNoMCPWarnings(t, diags)
			// The unexpanded text is retained: expansion belongs to
			// generation, which alone knows the workspace.
			if len(p.PluginServers) != 1 || p.PluginServers[0].Cwd != cwd {
				t.Fatalf("cwd = %+v, want the authored text %q", p.PluginServers, cwd)
			}
		})
	}
	t.Run("rejected plugin data", func(t *testing.T) {
		p, warnings := pluginWithServers(t, `"alpha": {"command": "x", "cwd": "${PLUGIN_DATA}"}`)
		if len(p.PluginServers) != 0 || len(warnings) != 1 || warnings[0] != "plugin.mcp.server.invalid" {
			t.Fatalf("a working directory outside the plugin tree must warn and skip: %v %v", warnings, serverNames(p))
		}
	})
}

// TestResolveServersExpandsBothVariablesExactlyOnce proves expansion
// correctness in arguments, environment values, and the working directory,
// the two supplied environment variables, and that a substituted value is
// never rescanned.
func TestResolveServersExpandsBothVariablesExactlyOnce(t *testing.T) {
	server := PluginServer{
		Name:       "alpha",
		Plugin:     "vendor-x",
		PluginRoot: "/src/plugins/vendor-x",
		Transport:  TransportStdio,
		Command:    "alpha-server",
		Args:       []string{"--root=${PLUGIN_ROOT}", "--data=${PLUGIN_DATA}/db", "--literal=${PLUGIN_ROOT}${PLUGIN_DATA}"},
		Env:        map[string]string{"CACHE": "${PLUGIN_DATA}/cache", "HOME_LIKE": "${HOME}"},
		Cwd:        "${PLUGIN_ROOT}/work",
	}
	resolved := ResolveServers([]PluginServer{server}, "/ws", "my-agent")
	if len(resolved) != 1 {
		t.Fatalf("resolved = %+v", resolved)
	}
	got := resolved[0]
	data := "/ws/.tenon/plugin-data/my-agent/vendor-x"
	if data != PluginDataDir("/ws", "my-agent", "vendor-x") {
		t.Fatalf("plugin data directory = %q", PluginDataDir("/ws", "my-agent", "vendor-x"))
	}
	want := []string{
		"--root=/src/plugins/vendor-x",
		"--data=" + data + "/db",
		"--literal=/src/plugins/vendor-x" + data,
	}
	for i, arg := range want {
		if got.Args[i] != arg {
			t.Fatalf("arg %d = %q, want %q", i, got.Args[i], arg)
		}
	}
	if got.Cwd != "/src/plugins/vendor-x/work" {
		t.Fatalf("cwd = %q", got.Cwd)
	}
	if got.Env["CACHE"] != data+"/cache" {
		t.Fatalf("env CACHE = %q", got.Env["CACHE"])
	}
	if got.Env["PLUGIN_ROOT"] != "/src/plugins/vendor-x" || got.Env["PLUGIN_DATA"] != data {
		t.Fatalf("both variables must be supplied to every stdio server: %v", got.Env)
	}
	// An unsupported placeholder is never expanded, and is exactly what a
	// harness running its own expansion pass must not receive.
	if got.Env["HOME_LIKE"] != "${HOME}" {
		t.Fatalf("env HOME_LIKE = %q, want the literal text", got.Env["HOME_LIKE"])
	}
	if got.PlaceholderField != "environment value HOME_LIKE" || got.Placeholder != "${HOME}" {
		t.Fatalf("placeholder = %q %q, want the surviving ${...} text named", got.PlaceholderField, got.Placeholder)
	}
}

// TestResolveServersReportsNoPlaceholderForPortableValues proves the
// per-harness skip is not triggered by ordinary expanded values.
func TestResolveServersReportsNoPlaceholderForPortableValues(t *testing.T) {
	resolved := ResolveServers([]PluginServer{{
		Name: "alpha", Plugin: "vendor-x", PluginRoot: "/src", Transport: TransportStdio,
		Command: "alpha-server", Args: []string{"--data=${PLUGIN_DATA}"},
	}}, "/ws", "my-agent")
	if resolved[0].Placeholder != "" {
		t.Fatalf("placeholder = %q, want none", resolved[0].Placeholder)
	}
}

// TestPluginMCPFingerprintChangesWithDocument proves the mcp.json bytes are
// source, including when the document is malformed and disables its own
// component.
func TestPluginMCPFingerprintChangesWithDocument(t *testing.T) {
	build := func(doc string) string {
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
		writePluginMCP(t, root, "vendor-x", doc)
		p, diags, err := Load(root)
		if err != nil || p == nil || diags.HasErrors() {
			t.Fatalf("load failed: %v %v", err, diags.All())
		}
		return p.Fingerprint
	}
	base := build(mcpDoc(stdioServer))
	if again := build(mcpDoc(stdioServer)); again != base {
		t.Fatal("an identical mcp.json must fingerprint identically")
	}
	if changed := build(mcpDoc(`"alpha": {"command": "beta-server"}`)); changed == base {
		t.Fatal("changing an accepted server value must change the fingerprint")
	}
	if malformed := build(`{"$schema": "nope"}`); malformed == base {
		t.Fatal("even a malformed mcp.json's read bytes must join the fingerprint")
	}
}

// TestPluginMCPTooManyServersWarnsAndTruncates proves the accepted-server
// ceiling truncates rather than failing an otherwise valid project.
func TestPluginMCPTooManyServersWarnsAndTruncates(t *testing.T) {
	var declarations []string
	for i := 0; i <= MaxPluginServers; i++ {
		declarations = append(declarations, fmt.Sprintf(`"s%03d": {"command": "x"}`, i))
	}
	p, warnings := pluginWithServers(t, strings.Join(declarations, ",\n"))
	if len(p.PluginServers) != MaxPluginServers {
		t.Fatalf("servers = %d, want exactly the %d accepted ceiling", len(p.PluginServers), MaxPluginServers)
	}
	if len(warnings) != 1 || warnings[0] != "plugin.mcp.bounds.exceeded" {
		t.Fatalf("warnings = %v, want exactly one plugin.mcp.bounds.exceeded", warnings)
	}
	last := fmt.Sprintf("s%03d", MaxPluginServers)
	for _, s := range p.PluginServers {
		if s.Name == last {
			t.Fatalf("the server past the ceiling must not be accepted: %+v", s)
		}
	}
}

// TestPluginMCPOversizedDocumentDisablesTheComponent proves the document
// bound is enforced before the bytes are parsed.
func TestPluginMCPOversizedDocumentDisablesTheComponent(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	padding := strings.Repeat("p", MaxPluginMCPBytes)
	writePluginMCP(t, root, "vendor-x", mcpDoc(fmt.Sprintf(`"alpha": {"command": "x", "args": [%q]}`, padding)))

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("an oversized mcp.json must warn, not fail: %v", diags.All())
	}
	requireWarningID(t, diags, "plugin.mcp.invalid")
	if len(p.PluginServers) != 0 {
		t.Fatalf("servers = %+v, want the component disabled", p.PluginServers)
	}
}

// TestPluginUnsupportedComponentEntryWarns proves every plugin root entry
// other than plugin.json, skills, and mcp.json is skipped with one warning
// each at its own authored path (ADR 0009).
func TestPluginUnsupportedComponentEntryWarns(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(stdioServer))
	writeSkillFile(t, root, "plugins/vendor-x/skills/fine/SKILL.md", []byte(minimalSkillMD("fine")), 0o644)
	writeSkillFile(t, root, "plugins/vendor-x/hooks/hooks.json", []byte("{}\n"), 0o644)
	writeSkillFile(t, root, "plugins/vendor-x/commands/deploy.md", []byte("deploy\n"), 0o644)
	// Ordinary payload content is inert, never a skipped component: the
	// README and the directory holding an accepted command must not warn.
	writeSkillFile(t, root, "plugins/vendor-x/README.md", []byte("# Vendor X\n"), 0o644)
	writeSkillFile(t, root, "plugins/vendor-x/bin/serve", []byte("#!/bin/sh\n"), 0o755)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("an unsupported component must warn, not fail: %v", diags.All())
	}
	var paths []string
	for _, d := range diags.All() {
		if d.ID == "plugin.component.unsupported" {
			paths = append(paths, d.Path)
		}
	}
	want := []string{"plugins/vendor-x/commands", "plugins/vendor-x/hooks"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("unsupported component paths = %v, want %v", paths, want)
	}
	if len(p.Skills) != 1 || len(p.PluginServers) != 1 {
		t.Fatalf("supported components must still load: %+v %+v", p.Skills, p.PluginServers)
	}
}

// TestPluginMCPMissingDocumentIsNormal proves a plugin without mcp.json
// contributes no server and no diagnostic.
func TestPluginMCPMissingDocumentIsNormal(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || len(diags.All()) != 0 || len(p.PluginServers) != 0 {
		t.Fatalf("a plugin without mcp.json must produce no diagnostics: %v", diags.All())
	}
}
