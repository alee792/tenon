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
	dir := filepath.Join(root, "connections")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func remoteConnection(url, context string) string {
	body := "---\ntype: mcp\ntransport: streamable-http\nurl: " + url + "\n---\n"
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
		c.Context != "Use this for the public catalog." || c.SourcePath != "connections/catalog.md" {
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
	writeConnectionFile(t, root, "catalog.md", "---\ntype: mcp\ntransport: streamable-http\nurl: https://example.com/mcp\n---\n\n   \n")

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
	if err := os.Symlink(filepath.Join(root, "connections", "target.md"), filepath.Join(root, "connections", "link.md")); err != nil {
		t.Fatal(err)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.entry.invalid")
}

func TestLoadConnectionsRejectsDirectory(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.MkdirAll(filepath.Join(root, "connections", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.entry.invalid")
}

func TestLoadConnectionsRejectsOtherExtension(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.txt", remoteConnection("https://example.com/mcp", ""))
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.entry.invalid")
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
	requireErrorID(t, diags, "connection.bounds.exceeded")
}

func TestLoadConnectionsRejectsOversizedFile(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	huge := remoteConnection("https://example.com/mcp", "") + strings.Repeat("x", MaxConnectionBytes)
	writeConnectionFile(t, root, "big.md", huge)
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.bounds.exceeded")
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
			requireErrorID(t, diags, "connection.name.invalid")
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
	requireErrorID(t, diags, "connection.name.reserved")
}

// --- Frontmatter and target-shape diagnostics ------------------------------

func TestLoadConnectionsRejectsMissingFrontmatter(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "no frontmatter here\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.frontmatter.missing")
}

func TestLoadConnectionsRejectsWrongType(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\ntype: other\ntransport: streamable-http\nurl: https://example.com/mcp\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.frontmatter.invalid")
}

func TestLoadConnectionsRejectsMissingTarget(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\ntype: mcp\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.frontmatter.missing")
}

func TestLoadConnectionsRejectsMixedTargetFields(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\ntype: mcp\ntransport: streamable-http\nurl: https://example.com/mcp\npackage: github-mcp-server\ncapability: github\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.frontmatter.unknown-field")
}

func TestLoadConnectionsRejectsUnknownField(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\ntype: mcp\ntransport: streamable-http\nurl: https://example.com/mcp\nheaders:\n  X-Trace: on\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.frontmatter.unknown-field")
}

func TestLoadConnectionsRejectsDuplicateField(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\ntype: mcp\ntransport: streamable-http\nurl: https://example.com/mcp\nurl: https://example.com/other\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.frontmatter.invalid")
}

func TestLoadConnectionsRejectsYAMLAlias(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\nanchor: &a mcp\ntype: *a\ntransport: streamable-http\nurl: https://example.com/mcp\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.frontmatter.invalid")
}

// TestLoadConnectionsInstalledFormFailsUnsupported proves the installed
// package/capability shape is recognized and rejected with the exact honest
// diagnostic, not silently dropped.
func TestLoadConnectionsInstalledFormFailsUnsupported(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "github.md",
		"---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nUse for GitHub work.\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "connection.target.unsupported")
	for _, d := range diags.All() {
		if d.ID == "connection.target.unsupported" {
			if !strings.Contains(d.Rule, "installed package targets are not supported yet") {
				t.Fatalf("rule text = %q", d.Rule)
			}
		}
	}
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
				requireErrorID(t, diags, "connection.target.invalid")
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
	requireErrorID(t, diags, "connection.context.too-long")
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
	requireErrorID(t, diags, "connection.name.collision")
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
