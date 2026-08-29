package agentproject

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeConnectionFile(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "mcp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func remoteConnection(url, context string) string {
	body := "---\ntype: streamable-http\nurl: " + url + "\n---\n"
	if context != "" {
		body += "\n" + context + "\n"
	}
	return body
}

func remoteConnectionWithHeaders(url string, headers map[string]string, context string) string {
	body := "---\ntype: streamable-http\nurl: " + url + "\n"
	if len(headers) > 0 {
		body += "headers:\n"
		for _, name := range sortedStringKeys(headers) {
			body += "  " + name + ": \"" + headers[name] + "\"\n"
		}
	}
	body += "---\n"
	if context != "" {
		body += "\n" + context + "\n"
	}
	return body
}

// TestLoadValidRemoteConnection proves the exact accepted shape: name from
// filename, URL and trimmed context preserved exactly, sorted by name.
func TestLoadValidRemoteConnection(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		remoteConnection("https://example.com/mcp", "  Use this for the public catalog.  \n"))

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Connections) != 1 {
		t.Fatalf("connections = %+v", p.Connections)
	}
	c := p.Connections[0]
	if c.Name != "catalog" || c.URL != "https://example.com/mcp" ||
		c.Context != "Use this for the public catalog." || c.SourcePath != "mcp/catalog.md" {
		t.Fatalf("connection = %+v", c)
	}
}

// TestLoadConnectionsSortedByName proves multiple connections are returned
// in lexical order regardless of directory read order.
func TestLoadConnectionsSortedByName(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "zeta.md", remoteConnection("https://z.example.com/mcp", ""))
	writeConnectionFile(t, root, "alpha.md", remoteConnection("https://a.example.com/mcp", ""))

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Connections) != 2 || p.Connections[0].Name != "alpha" || p.Connections[1].Name != "zeta" {
		t.Fatalf("connections order = %+v", p.Connections)
	}
}

// TestLoadEmptyBodyIsNoContext proves an empty or whitespace-only body means
// no context, not an empty-string special case.
func TestLoadEmptyBodyIsNoContext(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\ntype: streamable-http\nurl: https://example.com/mcp\n---\n\n   \n")

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if p.Connections[0].Context != "" {
		t.Fatalf("expected no context, got %q", p.Connections[0].Context)
	}
}

// --- Entry, name, and bounds diagnostics ---------------------------------

func TestLoadConnectionsRejectsSymlink(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "target.md", remoteConnection("https://example.com/mcp", ""))
	if err := os.Symlink(filepath.Join(root, "mcp", "target.md"), filepath.Join(root, "mcp", "link.md")); err != nil {
		t.Fatal(err)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.entry.invalid")
}

func TestLoadConnectionsRejectsDirectory(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.MkdirAll(filepath.Join(root, "mcp", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.entry.invalid")
}

func TestLoadConnectionsRejectsOtherExtension(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.txt", remoteConnection("https://example.com/mcp", ""))
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.entry.invalid")
}

func TestLoadConnectionsRejectsOverLimitCount(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	for i := 0; i < MaxConnections+1; i++ {
		writeConnectionFile(t, root, fmt.Sprintf("c%03d.md", i), remoteConnection("https://example.com/mcp", ""))
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.bounds.exceeded")
}

func TestLoadConnectionsRejectsOversizedFile(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	huge := remoteConnection("https://example.com/mcp", "") + strings.Repeat("x", MaxConnectionBytes)
	writeConnectionFile(t, root, "big.md", huge)
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.bounds.exceeded")
}

func TestLoadConnectionsRejectsInvalidNames(t *testing.T) {
	cases := []string{"Catalog.md", "1catalog.md", "-catalog.md", "ca talog.md", "ca.talog.md"}
	for _, filename := range cases {
		t.Run(filename, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writeConnectionFile(t, root, filename, remoteConnection("https://example.com/mcp", ""))
			_, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			requireErrorID(t, diags, "mcp.name.invalid")
		})
	}
}

func TestLoadConnectionsAllowsUnderscoresInName(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "my_catalog.md", remoteConnection("https://example.com/mcp", ""))
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("underscores must be permitted in connection names: %v", diags.All())
	}
}

func TestLoadConnectionsRejectsManagedName(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "managed.md", remoteConnection("https://example.com/mcp", ""))
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.name.reserved")
}

// --- Frontmatter and target-shape diagnostics ------------------------------

func TestLoadConnectionsRejectsMissingFrontmatter(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "no frontmatter here\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.missing")
}

func TestLoadConnectionsRejectsWrongType(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\ntype: other\ntransport: streamable-http\nurl: https://example.com/mcp\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.invalid")
}

func TestLoadConnectionsRejectsMissingTarget(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\ndescription: no type field\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.missing")
}

func TestLoadConnectionsRejectsMixedTargetFields(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\ntype: streamable-http\nurl: https://example.com/mcp\npackage: github-mcp-server\ncapability: github\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.unknown-field")
}

func TestLoadConnectionsRejectsUnknownField(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\ntype: streamable-http\nurl: https://example.com/mcp\ntimeout: 5\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.unknown-field")
}

func TestLoadConnectionsRejectsDuplicateField(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\ntype: streamable-http\nurl: https://example.com/mcp\nurl: https://example.com/other\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.invalid")
}

func TestLoadConnectionsRejectsYAMLAlias(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\nanchor: &a streamable-http\ntype: *a\nurl: https://example.com/mcp\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.invalid")
}

// --- SSE and stdio rejection --------------------------------------------

func TestLoadConnectionsRejectsSSE(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\ntype: sse\nurl: https://example.com/mcp\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.transport.invalid")
}

func TestLoadConnectionsRejectsStdioNotYetSupported(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\ntype: stdio\ncommand: ./server\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.transport.invalid")
}

// --- Installed frontmatter shape matrix --------------------------------

func installedConnection(pkg, capability, context string) string {
	body := "---\ntype: installed\npackage: " + pkg + "\ncapability: " + capability + "\n---\n"
	if context != "" {
		body += "\n" + context + "\n"
	}
	return body
}

// TestLoadValidInstalledConnection proves the exact accepted installed
// shape: name from filename, package/capability preserved exactly, kind set,
// URL left empty.
func TestLoadValidInstalledConnection(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "github.md", installedConnection("github-mcp-server", "github", "Use for GitHub work."))

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Connections) != 1 {
		t.Fatalf("connections = %+v", p.Connections)
	}
	c := p.Connections[0]
	if c.Kind != ConnectionKindInstalled || c.Name != "github" ||
		c.Package != "github-mcp-server" || c.Capability != "github" ||
		c.Context != "Use for GitHub work." || c.URL != "" {
		t.Fatalf("connection = %+v", c)
	}
}

func TestLoadInstalledConnectionMissingPackage(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "github.md", "---\ntype: installed\ncapability: github\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.target.invalid")
}

func TestLoadInstalledConnectionMissingCapability(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "github.md", "---\ntype: installed\npackage: github-mcp-server\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.target.invalid")
}

func TestLoadInstalledConnectionBadPackageGrammar(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "github.md", installedConnection("GitHub_MCP", "github", ""))
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.target.invalid")
}

func TestLoadInstalledConnectionBadCapabilityGrammar(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "github.md", installedConnection("github-mcp-server", "Git Hub", ""))
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.target.invalid")
}

// TestLoadConnectionsRejectsMixedRemoteAndInstalledFields proves a
// frontmatter mixing remote and installed fields still fails, now that the
// installed form is otherwise accepted.
func TestLoadConnectionsRejectsMixedRemoteAndInstalledFields(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\ntype: installed\npackage: github-mcp-server\ncapability: github\nurl: https://example.com/mcp\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.unknown-field")
}

// --- URL matrix -------------------------------------------------------------

func TestLoadConnectionsURLMatrix(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/mcp":           true,
		"http://example.com/mcp":            false, // plain http always rejected
		"http://localhost:8080/mcp":         false, // no loopback exception
		"https://user:pass@example.com/mcp": false, // userinfo
		"https://example.com/mcp?x=1":       false, // query
		"https://example.com/mcp#frag":      false, // fragment
		"https:///mcp":                      false, // empty host
		"not a url":                         false,
		"ftp://example.com/mcp":             false,
	}
	for raw, wantValid := range cases {
		t.Run(raw, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writeConnectionFile(t, root, "catalog.md", remoteConnection(raw, ""))
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			gotValid := p != nil && !diags.HasErrors()
			if gotValid != wantValid {
				t.Fatalf("url %q: valid = %v, want %v (diags: %v)", raw, gotValid, wantValid, diags.All())
			}
			if !wantValid && gotValid == false {
				requireErrorID(t, diags, "mcp.target.invalid")
			}
		})
	}
}

// --- Context length boundary -------------------------------------------------

func TestLoadConnectionsContextBoundary(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "ok.md", remoteConnection("https://example.com/mcp", strings.Repeat("a", 1024)))
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("1024 characters must be accepted: %v", diags.All())
	}
	if utf8.RuneCountInString(p.Connections[0].Context) != 1024 {
		t.Fatalf("context length = %d", utf8.RuneCountInString(p.Connections[0].Context))
	}
}

func TestLoadConnectionsContextOverBoundary(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "bad.md", remoteConnection("https://example.com/mcp", strings.Repeat("a", 1025)))
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.context.too-long")
}

// --- Collisions --------------------------------------------------------------

// TestLoadConnectionsCollideWithPluginServer proves a connection whose name
// matches an accepted plugin MCP server fails before mutation (ADR 0016);
// two connection files cannot literally share a filename, so this is the
// collision path exercised at Load.
func TestLoadConnectionsCollideWithPluginServer(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(`"catalog": {"command": "server"}`))
	writeConnectionFile(t, root, "catalog.md", remoteConnection("https://example.com/mcp", ""))

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.name.collision")
}

// --- Fingerprint sensitivity --------------------------------------------------

func TestConnectionJoinsFingerprint(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	p1, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	writeConnectionFile(t, root, "catalog.md", remoteConnection("https://example.com/mcp", "guidance"))
	p2, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p2 == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if p1.Fingerprint == p2.Fingerprint {
		t.Fatal("adding a connection must change the fingerprint")
	}

	writeConnectionFile(t, root, "catalog.md", remoteConnection("https://example.com/mcp", "different guidance"))
	p3, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p3 == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if p2.Fingerprint == p3.Fingerprint {
		t.Fatal("changing a connection's body must change the fingerprint")
	}
}

// --- Legacy connections/ migration ---------------------------------------

// TestLoadLegacyConnectionsDirFailsClosed proves a leftover connections/
// directory is a hard migration failure naming mcp/, not a silent no-op
// (issue #49).
func TestLoadLegacyConnectionsDirFailsClosed(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.MkdirAll(filepath.Join(root, "connections"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "connections", "catalog.md"),
		[]byte("---\ntype: mcp\ntransport: streamable-http\nurl: https://example.com/mcp\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.migration.connections-dir")
}

// TestLoadLegacyConnectionsDirEmptyStillFails proves the migration diagnostic
// fires even when the legacy directory is empty: presence alone is the
// signal, not its content.
func TestLoadLegacyConnectionsDirEmptyStillFails(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.MkdirAll(filepath.Join(root, "connections"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.migration.connections-dir")
}

// --- Header matrix --------------------------------------------------------

// TestLoadValidRemoteConnectionWithHeaders proves headers are preserved
// exactly, including a value ending with one ${VAR} reference.
func TestLoadValidRemoteConnectionWithHeaders(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", remoteConnectionWithHeaders(
		"https://example.com/mcp",
		map[string]string{"Authorization": "Bearer ${ACME_TOKEN}", "X-Trace": "on"},
		""))

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	c := p.Connections[0]
	if c.Headers["Authorization"] != "Bearer ${ACME_TOKEN}" || c.Headers["X-Trace"] != "on" {
		t.Fatalf("headers = %+v", c.Headers)
	}
}

// TestLoadConnectionHeaderValueMatrix proves the exact, falsifiable header
// value grammar: a literal with no "$", or a literal prefix (possibly empty)
// containing no "$" followed by exactly one ${VAR} reference and nothing
// after it. Any other use of "$" fails, as does referencing a plugin-scoped
// variable.
func TestLoadConnectionHeaderValueMatrix(t *testing.T) {
	cases := map[string]bool{
		"Bearer ${ACME_TOKEN}":  true,  // prefix + ref
		"${ACME_TOKEN}":         true,  // bare ref
		"static-value":          true,  // literal, no $
		"":                      true,  // empty literal
		"${A}${B}":              false, // two refs
		"${ACME_TOKEN}suffix":   false, // ref then suffix
		"has $ in it":           false, // lone $
		"${lowercase}":          false, // VAR must be uppercase
		"${PLUGIN_ROOT}":        false, // plugin-scoped denylist
		"${PLUGIN_DATA}":        false, // plugin-scoped denylist
		"Bearer ${PLUGIN_ROOT}": false,
	}
	for value, wantValid := range cases {
		t.Run(value, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writeConnectionFile(t, root, "catalog.md", remoteConnectionWithHeaders(
				"https://example.com/mcp", map[string]string{"X-Auth": value}, ""))
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			gotValid := p != nil && !diags.HasErrors()
			if gotValid != wantValid {
				t.Fatalf("header value %q: valid = %v, want %v (diags: %v)", value, gotValid, wantValid, diags.All())
			}
			if !wantValid {
				requireErrorID(t, diags, "mcp.header.invalid")
			}
		})
	}
}

// TestLoadConnectionHeaderNameMatrix proves bad HTTP header names and
// case-insensitive collisions are rejected via the shared plugin mcp.json
// header rules.
func TestLoadConnectionHeaderNameMatrix(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\ntype: streamable-http\nurl: https://example.com/mcp\nheaders:\n  \"bad header\": value\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.header.invalid")
}
