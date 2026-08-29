package agentproject

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
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

// TestLoadValidStdioConnection proves the exact accepted stdio shape: the
// command resolves to an agent-root-relative slash path proven to stay
// inside the agent root, args and env are preserved literally, and cwd
// defaults to empty (rendering fills in the agent root; see internal/claude
// and internal/codex).
func TestLoadValidStdioConnection(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "servers/deployctl/bin/deployctl", []byte("#!/bin/sh\nexec cat\n"), 0o755)
	writeConnectionFile(t, root, "deployctl.md",
		"---\ntype: stdio\ncommand: ./servers/deployctl/bin/deployctl\nargs: [\"--flag\", \"value $HOME\"]\nenv:\n  DEPLOY_ENV: staging\n  TOKEN: \"Bearer ${ACME_TOKEN}\"\n---\n\nDeploy guidance.\n")

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
	wantCommand := "servers/deployctl/bin/deployctl"
	if c.Kind != ConnectionKindStdio || c.Command != wantCommand {
		t.Fatalf("connection = %+v, want the agent-root-relative command %q", c, wantCommand)
	}
	if len(c.Args) != 2 || c.Args[0] != "--flag" || c.Args[1] != "value $HOME" {
		t.Fatalf("args = %+v", c.Args)
	}
	if c.Env["DEPLOY_ENV"] != "staging" || c.Env["TOKEN"] != "Bearer ${ACME_TOKEN}" {
		t.Fatalf("env = %+v", c.Env)
	}
	if c.Cwd != "" {
		t.Fatalf("cwd = %q, want empty (no cwd declared)", c.Cwd)
	}
	if c.Context != "Deploy guidance." {
		t.Fatalf("context = %q", c.Context)
	}
}

// TestLoadStdioConnectionWithCwd proves a declared cwd resolves the same way
// command does: an agent-root-relative "./" path proven to exist as a real
// directory inside the agent root.
func TestLoadStdioConnectionWithCwd(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "servers/deployctl/bin/deployctl", []byte("#!/bin/sh\n"), 0o755)
	writeConnectionFile(t, root, "deployctl.md",
		"---\ntype: stdio\ncommand: ./servers/deployctl/bin/deployctl\ncwd: ./servers/deployctl\n---\n")

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	wantCwd := "servers/deployctl"
	if p.Connections[0].Cwd != wantCwd {
		t.Fatalf("cwd = %q, want %q", p.Connections[0].Cwd, wantCwd)
	}
}

// --- Stdio command matrix -------------------------------------------------

// TestLoadStdioCommandMatrix proves every rejected command shape fails with
// mcp.command.invalid before workspace mutation, and the "./" form succeeds.
func TestLoadStdioCommandMatrix(t *testing.T) {
	setup := func(t *testing.T) (root string) {
		root = writeAgent(t, "agent", validInstructions)
		writeSkillFile(t, root, "servers/bin/serve", []byte("#!/bin/sh\n"), 0o755)
		return root
	}

	cases := map[string]struct {
		command string
		valid   bool
	}{
		"ok":              {"./servers/bin/serve", true},
		"bare name":       {"serve", false},
		"absolute":        {"/usr/bin/serve", false},
		"escape":          {"./../outside", false},
		"missing":         {"./servers/bin/does-not-exist", false},
		"dot dot literal": {"./servers/../../etc/passwd", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := setup(t)
			writeConnectionFile(t, root, "srv.md", "---\ntype: stdio\ncommand: "+tc.command+"\n---\n")
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			gotValid := p != nil && !diags.HasErrors()
			if gotValid != tc.valid {
				t.Fatalf("command %q: valid = %v, want %v (diags: %v)", tc.command, gotValid, tc.valid, diags.All())
			}
			if !tc.valid {
				requireErrorID(t, diags, "mcp.command.invalid")
			}
		})
	}

	t.Run("non-regular (directory)", func(t *testing.T) {
		root := setup(t)
		writeConnectionFile(t, root, "srv.md", "---\ntype: stdio\ncommand: ./servers\n---\n")
		_, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		requireErrorID(t, diags, "mcp.command.invalid")
	})

	t.Run("symlink escape", func(t *testing.T) {
		root := setup(t)
		outside := filepath.Join(t.TempDir(), "real-serve")
		if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "servers", "bin", "link")); err != nil {
			t.Fatal(err)
		}
		writeConnectionFile(t, root, "srv.md", "---\ntype: stdio\ncommand: ./servers/bin/link\n---\n")
		_, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		requireErrorID(t, diags, "mcp.command.invalid")
	})

	// A command resolving inside mcp/ is rejected even when it sits in a
	// nested directory the plain mcp/ entry scan (which lists only immediate
	// entries) never itself inspects: mcp/ is reserved for declaration files
	// by the ADR, and the containment check enforces that independently of
	// the entry scan (SF5, post-review).
	t.Run("inside mcp/ (nested)", func(t *testing.T) {
		root := setup(t)
		writeSkillFile(t, root, "mcp/sub/evil.sh", []byte("#!/bin/sh\n"), 0o755)
		writeConnectionFile(t, root, "srv.md", "---\ntype: stdio\ncommand: ./mcp/sub/evil.sh\n---\n")
		_, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		requireErrorID(t, diags, "mcp.command.invalid")
	})

	// An intermediate path segment being a symlink is rejected exactly like a
	// symlinked final segment: resolveInRoot walks every component, not only
	// the last one.
	t.Run("intermediate symlink", func(t *testing.T) {
		root := setup(t)
		if err := os.Symlink(filepath.Join(root, "servers", "bin"), filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		writeConnectionFile(t, root, "srv.md", "---\ntype: stdio\ncommand: ./link/serve\n---\n")
		_, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		requireErrorID(t, diags, "mcp.command.invalid")
	})

	// A command file with no executable bit warns rather than being rejected
	// (SF6, post-review): the ADR does not require the bit at validation
	// time, only that the harness can be asked to run it.
	t.Run("not executable warns but is accepted", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writeSkillFile(t, root, "servers/bin/serve", []byte("#!/bin/sh\n"), 0o644)
		writeConnectionFile(t, root, "srv.md", "---\ntype: stdio\ncommand: ./servers/bin/serve\n---\n")
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || diags.HasErrors() {
			t.Fatalf("a non-executable command file must warn, not fail: %v", diags.All())
		}
		found := false
		for _, d := range diags.All() {
			if d.ID == "mcp.command.not-executable" && d.Severity == diagnostics.Warning {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected mcp.command.not-executable warning, got %v", diags.All())
		}
	})
}

// --- Stdio cwd matrix -------------------------------------------------------

func TestLoadStdioCwdMatrix(t *testing.T) {
	setup := func(t *testing.T) string {
		root := writeAgent(t, "agent", validInstructions)
		writeSkillFile(t, root, "servers/bin/serve", []byte("#!/bin/sh\n"), 0o755)
		if err := os.MkdirAll(filepath.Join(root, "servers", "work"), 0o755); err != nil {
			t.Fatal(err)
		}
		return root
	}
	cases := map[string]struct {
		cwd   string
		valid bool
	}{
		"ok":           {"./servers/work", true},
		"absolute":     {"/tmp", false},
		"escape":       {"./../outside", false},
		"not-a-dir":    {"./servers/bin/serve", false},
		"missing":      {"./servers/does-not-exist", false},
		"bare (no ./)": {"servers/work", false},
		"inside mcp/":  {"./mcp", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := setup(t)
			writeConnectionFile(t, root, "srv.md",
				"---\ntype: stdio\ncommand: ./servers/bin/serve\ncwd: "+tc.cwd+"\n---\n")
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			gotValid := p != nil && !diags.HasErrors()
			if gotValid != tc.valid {
				t.Fatalf("cwd %q: valid = %v, want %v (diags: %v)", tc.cwd, gotValid, tc.valid, diags.All())
			}
			if !tc.valid {
				requireErrorID(t, diags, "mcp.cwd.invalid")
			}
		})
	}

	// An intermediate path segment being a symlink is rejected exactly like
	// it is for a command (resolveInRoot walks every component).
	t.Run("symlinked cwd", func(t *testing.T) {
		root := setup(t)
		if err := os.Symlink(filepath.Join(root, "servers", "work"), filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		writeConnectionFile(t, root, "srv.md",
			"---\ntype: stdio\ncommand: ./servers/bin/serve\ncwd: ./link\n---\n")
		_, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		requireErrorID(t, diags, "mcp.cwd.invalid")
	})
}

// --- Stdio env grammar spot-check (full matrix already covered for headers)

func TestLoadStdioEnvValueGrammar(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "servers/bin/serve", []byte("#!/bin/sh\n"), 0o755)
	writeConnectionFile(t, root, "srv.md",
		"---\ntype: stdio\ncommand: ./servers/bin/serve\nenv:\n  OK: \"Bearer ${ACME_TOKEN}\"\n---\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if diags.HasErrors() {
		t.Fatalf("a literal prefix plus one ${VAR} reference must be accepted: %v", diags.All())
	}

	root2 := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root2, "servers/bin/serve", []byte("#!/bin/sh\n"), 0o755)
	writeConnectionFile(t, root2, "srv.md",
		"---\ntype: stdio\ncommand: ./servers/bin/serve\nenv:\n  BAD: \"${PLUGIN_ROOT}\"\n---\n")
	_, diags2, err := Load(root2)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags2, "mcp.env.invalid")
}

// --- Stdio args PLUGIN_ROOT/PLUGIN_DATA rejection ---------------------------

func TestLoadStdioArgsRejectPluginVars(t *testing.T) {
	cases := []string{"${PLUGIN_ROOT}/x", "${PLUGIN_DATA}"}
	for _, arg := range cases {
		t.Run(arg, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writeSkillFile(t, root, "servers/bin/serve", []byte("#!/bin/sh\n"), 0o755)
			writeConnectionFile(t, root, "srv.md",
				"---\ntype: stdio\ncommand: ./servers/bin/serve\nargs: [\""+arg+"\"]\n---\n")
			_, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			requireErrorID(t, diags, "mcp.args.invalid")
		})
	}
	// Other "$" usage in args is allowed: it is shell-meaningless since args
	// are execv'd, never interpolated by a shell tenon invokes.
	t.Run("other dollar allowed", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writeSkillFile(t, root, "servers/bin/serve", []byte("#!/bin/sh\n"), 0o755)
		writeConnectionFile(t, root, "srv.md",
			"---\ntype: stdio\ncommand: ./servers/bin/serve\nargs: [\"$HOME/x\", \"a${b}c\"]\n---\n")
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || diags.HasErrors() {
			t.Fatalf("non-plugin-scoped $ in args must be accepted: %v", diags.All())
		}
	})
}

// --- Stdio fingerprint sensitivity ------------------------------------------

// TestStdioCommandJoinsFingerprintSensitively proves a stdio command's exact
// content and executable bit are source, exactly like a plugin-relative
// command (ADR 0026).
func TestStdioCommandJoinsFingerprintSensitively(t *testing.T) {
	build := func(t *testing.T, script string, mode os.FileMode) string {
		t.Helper()
		root := writeAgent(t, "agent", validInstructions)
		writeSkillFile(t, root, "servers/bin/serve", []byte(script), mode)
		writeConnectionFile(t, root, "srv.md", "---\ntype: stdio\ncommand: ./servers/bin/serve\n---\n")
		p, diags, err := Load(root)
		if err != nil || p == nil || diags.HasErrors() {
			t.Fatalf("load failed: %v %v", err, diags.All())
		}
		return p.Fingerprint
	}
	base := build(t, "#!/bin/sh\nexec cat\n", 0o755)
	if again := build(t, "#!/bin/sh\nexec cat\n", 0o755); again != base {
		t.Fatal("identical stdio command source must fingerprint identically")
	}
	if changed := build(t, "#!/bin/sh\nexec tee\n", 0o755); changed == base {
		t.Fatal("changing a stdio command's content must change the fingerprint")
	}
	if mode := build(t, "#!/bin/sh\nexec cat\n", 0o644); mode == base {
		t.Fatal("changing a stdio command's executable bit must change the fingerprint")
	}
}

// --- Stdio aggregate bounds -------------------------------------------------

// TestCheckStdioBudgetCount proves the 17th declared stdio server trips the
// count bound, exercised directly against already-validated connections so
// the test stays fast: 17 real files would work too, but the pure
// accounting function is what actually enforces the bound.
func TestCheckStdioBudgetCount(t *testing.T) {
	diags := &diagnostics.List{}
	var connections []Connection
	for i := 0; i < MaxStdioServers; i++ {
		connections = append(connections, Connection{Kind: ConnectionKindStdio, Command: fmt.Sprintf("/agent/bin/s%d", i)})
	}
	checkStdioBudget(connections, diags)
	if diags.HasErrors() {
		t.Fatalf("exactly the limit must be accepted: %v", diags.All())
	}

	connections = append(connections, Connection{Kind: ConnectionKindStdio, Command: "/agent/bin/one-too-many"})
	diags = &diagnostics.List{}
	checkStdioBudget(connections, diags)
	requireErrorID(t, diags, "mcp.stdio.bounds.exceeded")
}

// TestCheckStdioBudgetAggregateBytes proves the aggregate byte bound trips
// without ever writing a 64 MiB fixture file, and that the same command path
// declared twice is charged once, matching the fingerprint's own dedup.
func TestCheckStdioBudgetAggregateBytes(t *testing.T) {
	diags := &diagnostics.List{}
	connections := []Connection{
		{Kind: ConnectionKindStdio, Command: "/agent/bin/big", commandBytes: MaxStdioCommandAggregateBytes},
	}
	checkStdioBudget(connections, diags)
	if diags.HasErrors() {
		t.Fatalf("exactly the byte limit must be accepted: %v", diags.All())
	}

	diags = &diagnostics.List{}
	connections = []Connection{
		{Kind: ConnectionKindStdio, Command: "/agent/bin/big", commandBytes: MaxStdioCommandAggregateBytes + 1},
	}
	checkStdioBudget(connections, diags)
	requireErrorID(t, diags, "mcp.stdio.bounds.exceeded")

	// The same resolved command path referenced by two connection names is
	// charged once, not twice.
	diags = &diagnostics.List{}
	connections = []Connection{
		{Kind: ConnectionKindStdio, Command: "/agent/bin/shared", commandBytes: MaxStdioCommandAggregateBytes},
		{Kind: ConnectionKindStdio, Command: "/agent/bin/shared", commandBytes: MaxStdioCommandAggregateBytes},
	}
	checkStdioBudget(connections, diags)
	if diags.HasErrors() {
		t.Fatalf("a shared command path must be charged once: %v", diags.All())
	}
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

// TestLoadConnectionsShadowsPluginServer proves an authored connection whose
// name matches an accepted plugin MCP server now wins (ADR 0026, issue #53):
// the project loads with a warning naming both sources, the authored server
// renders, and the plugin's server of that name is removed from
// Project.PluginServers.
func TestLoadConnectionsShadowsPluginServer(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(`"catalog": {"command": "server"}, "other": {"command": "server2"}`))
	writeConnectionFile(t, root, "catalog.md", remoteConnection("https://example.com/mcp", ""))

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatalf("expected the project to load; diags: %v", diags.All())
	}
	requireWarningID(t, diags, "mcp.name.shadowed")

	if len(p.Connections) != 1 || p.Connections[0].Name != "catalog" || p.Connections[0].Kind != ConnectionKindRemote {
		t.Fatalf("expected the authored catalog connection to render: %+v", p.Connections)
	}
	for _, s := range p.PluginServers {
		if s.Name == "catalog" {
			t.Fatalf("expected the shadowed plugin server catalog to be removed from PluginServers: %+v", p.PluginServers)
		}
	}
	foundOther := false
	for _, s := range p.PluginServers {
		if s.Name == "other" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Fatalf("expected the plugin's other, unrelated server to survive: %+v", p.PluginServers)
	}
}

// --- Masking form (ADR 0026, issue #53) -------------------------------------

func maskConnection(override string, enabled bool) string {
	return fmt.Sprintf("---\noverride: %s\nenabled: %v\n---\n", override, enabled)
}

// TestLoadMaskSuppressesPluginServer proves a valid masking declaration
// removes the named plugin server from PluginServers with no warning (the
// mask file is the deliberate record) and never itself renders as a
// connection.
func TestLoadMaskSuppressesPluginServer(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(`"catalog": {"command": "server"}, "other": {"command": "server2"}`))
	writeConnectionFile(t, root, "catalog.md", maskConnection("plugins/vendor-x", false))

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatalf("expected the project to load; diags: %v", diags.All())
	}
	for _, d := range diags.All() {
		if d.Severity == diagnostics.Warning {
			t.Fatalf("a mask must never produce a warning: %v", diags.All())
		}
	}
	if len(p.Connections) != 0 {
		t.Fatalf("a mask must never render as a connection: %+v", p.Connections)
	}
	for _, s := range p.PluginServers {
		if s.Name == "catalog" {
			t.Fatalf("expected the masked plugin server catalog to be removed: %+v", p.PluginServers)
		}
	}
	foundOther := false
	for _, s := range p.PluginServers {
		if s.Name == "other" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Fatalf("expected the plugin's other, unrelated server to survive: %+v", p.PluginServers)
	}
}

// TestLoadMaskDanglingOverridePluginAbsent proves a mask naming a plugin
// that is not present fails validation before workspace mutation.
func TestLoadMaskDanglingOverridePluginAbsent(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", maskConnection("plugins/vendor-x", false))

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.override.dangling")
}

// TestLoadMaskDanglingOverrideNoMatchingServer proves a mask naming a
// present plugin that does not contribute a server of that name fails
// validation before workspace mutation.
func TestLoadMaskDanglingOverrideNoMatchingServer(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(`"other": {"command": "server2"}`))
	writeConnectionFile(t, root, "catalog.md", maskConnection("plugins/vendor-x", false))

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.override.dangling")
}

// TestLoadMaskRejectsEnabledTrue proves enabled: true is rejected as
// meaningless, since a true mask would be a no-op: the plugin server it
// names is already emitted.
func TestLoadMaskRejectsEnabledTrue(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(`"catalog": {"command": "server"}`))
	writeConnectionFile(t, root, "catalog.md", maskConnection("plugins/vendor-x", true))

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.override.enabled")
}

// TestLoadMaskRejectsNonEmptyBody proves a mask carrying a Markdown body
// fails validation: a mask declares absence and carries no guidance prose.
func TestLoadMaskRejectsNonEmptyBody(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(`"catalog": {"command": "server"}`))
	writeConnectionFile(t, root, "catalog.md",
		"---\noverride: plugins/vendor-x\nenabled: false\n---\n\nSome guidance.\n")

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.override.body")
}

// TestLoadMaskManagedUnmaskable proves the reserved "managed" name cannot be
// masked either: the existing filename reservation check applies to every
// connection kind, including the masking arm.
func TestLoadMaskManagedUnmaskable(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "managed.md", maskConnection("plugins/vendor-x", false))

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.name.reserved")
}

// TestLoadMaskMixedFieldsRejected proves a file mixing "override" with a
// server-declaring field (here "url", with no "type") is rejected as a
// union violation of the masking arm rather than silently accepted.
func TestLoadMaskMixedFieldsRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\noverride: plugins/vendor-x\nenabled: false\nurl: https://example.com/mcp\n---\n")

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.unknown-field")
}

// TestLoadMaskMixedWithTypeRejected proves a file that does carry "type"
// alongside "override" is rejected as an unknown field of that
// server-declaring form, the other shape a union violation can take.
func TestLoadMaskMixedWithTypeRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md",
		"---\ntype: streamable-http\nurl: https://example.com/mcp\noverride: plugins/vendor-x\n---\n")

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.unknown-field")
}

// TestLoadMaskMissingEnabledRejected proves "override" alone, without the
// required "enabled" field, is rejected rather than defaulted.
func TestLoadMaskMissingEnabledRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\noverride: plugins/vendor-x\n---\n")

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.missing")
}

// TestLoadMaskBadOverrideGrammarRejected proves an override value that is
// not exactly "plugins/<name>" is rejected.
func TestLoadMaskBadOverrideGrammarRejected(t *testing.T) {
	for _, override := range []string{"vendor-x", "plugins/", "plugins/vendor-x/extra", "plugin/vendor-x"} {
		root := writeAgent(t, "agent", validInstructions)
		writeConnectionFile(t, root, "catalog.md", maskConnection(override, false))

		_, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		requireErrorID(t, diags, "mcp.override.invalid")
	}
}

// TestMaskJoinsFingerprint proves a mask file's exact source bytes join the
// project fingerprint like every other mcp/ file (ADR 0026), even though a
// mask never renders a connection: adding one changes the fingerprint, and
// reloading identical bytes reproduces it exactly.
func TestMaskJoinsFingerprint(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writePluginMCP(t, root, "vendor-x", mcpDoc(`"catalog": {"command": "server"}`))

	p1, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == nil {
		t.Fatalf("expected the project to load; diags: %v", diags.All())
	}

	writeConnectionFile(t, root, "catalog.md", maskConnection("plugins/vendor-x", false))
	p2, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p2 == nil {
		t.Fatalf("expected the project to load; diags: %v", diags.All())
	}
	if p1.Fingerprint == p2.Fingerprint {
		t.Fatal("adding a mask file must change the fingerprint even though it renders nothing")
	}

	p3, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p3 == nil {
		t.Fatalf("expected the project to load; diags: %v", diags.All())
	}
	if p2.Fingerprint != p3.Fingerprint {
		t.Fatal("reloading an unchanged mask file must reproduce the same fingerprint")
	}
}

// TestLoadMaskArmTriggersOnOverrideAlone proves the masking union arm is
// detected by the presence of "override" alone, not "override or enabled"
// (post-#53-review finding): a type-less file carrying "enabled" without
// "override" is a missing-type server declaration, never a masking
// declaration, and must be reported as such rather than with a masking
// diagnostic that would obscure the real problem.
func TestLoadMaskArmTriggersOnOverrideAlone(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\nenabled: false\n---\n")

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.missing")
	for _, d := range diags.All() {
		if d.ID == "mcp.frontmatter.missing" && !strings.Contains(d.Rule, "masking declaration") {
			t.Fatalf("expected the missing-type message to name the masking declaration shape as an alternative: %v", diags.All())
		}
	}
}

// TestLoadMaskEnabledWrongTypeIsFrontmatterInvalid proves a wrong-typed
// "enabled" value on an otherwise well-formed masking declaration is
// reported as mcp.frontmatter.invalid, the identifier established for a
// present field of the wrong YAML type, not mcp.override.invalid (post-#53-
// review finding).
func TestLoadMaskEnabledWrongTypeIsFrontmatterInvalid(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", "---\noverride: plugins/vendor-x\nenabled: \"nope\"\n---\n")

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.frontmatter.invalid")
	for _, d := range diags.All() {
		if d.ID == "mcp.override.invalid" {
			t.Fatalf("a wrong-typed enabled must never report mcp.override.invalid: %v", diags.All())
		}
	}
}

// TestLoadMaskOverrideReferenceFileRejectedWithHint proves naming a plugin
// reference file's own filename in "override" (a common mistake: "plugins/
// x.md" instead of "plugins/x") is rejected with a hint naming the correct
// form, rather than the generic bad-grammar message (post-#53-review
// finding).
func TestLoadMaskOverrideReferenceFileRejectedWithHint(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeConnectionFile(t, root, "catalog.md", maskConnection("plugins/vendor-x.md", false))

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.override.invalid")
	found := false
	for _, d := range diags.All() {
		if d.ID == "mcp.override.invalid" && strings.Contains(d.Rule, "name the plugin, not its reference file") &&
			strings.Contains(d.Rule, "plugins/vendor-x") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a hint naming the plugin storage name instead of the reference file, got %v", diags.All())
	}
}

// TestLoadMaskDanglingNamesWinningPluginOnCollision proves a mask naming a
// plugin that did declare the overridden server, but lost a plugin-to-plugin
// naming collision (ADR 0010, unchanged) to a different plugin, reports the
// winning plugin by name rather than the generic "no such server" message
// (post-#53-review finding 4): the author needs to know the server the mask
// would need to suppress is actually rendered by the winner, not by the
// plugin they named.
func TestLoadMaskDanglingNamesWinningPluginOnCollision(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	// "alpha" and "beta" both declare "catalog"; first-wins-with-warning
	// (ADR 0010) means "alpha" (lexically first) wins and "beta" loses.
	writePluginManifest(t, root, "alpha", validPluginJSON("alpha"))
	writePluginMCP(t, root, "alpha", mcpDoc(`"catalog": {"command": "server-a"}`))
	writePluginManifest(t, root, "beta", validPluginJSON("beta"))
	writePluginMCP(t, root, "beta", mcpDoc(`"catalog": {"command": "server-b"}`))
	// The mask names the loser, "beta", not the winner "alpha".
	writeConnectionFile(t, root, "catalog.md", maskConnection("plugins/beta", false))

	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "mcp.override.dangling")
	found := false
	for _, d := range diags.All() {
		if d.ID == "mcp.override.dangling" && strings.Contains(d.Rule, "beta") && strings.Contains(d.Rule, "alpha") &&
			strings.Contains(d.Rule, "naming collision") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the dangling message to name both the losing plugin and the winner, got %v", diags.All())
	}
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

// TestLoadLegacyConnectionsSymlinkFailsClosed proves a connections/ symlink —
// not just a real directory — also trips the migration diagnostic (issue #49
// review fix): checkLegacyConnectionsDir already checks for a symlink, but
// nothing previously exercised that branch.
func TestLoadLegacyConnectionsSymlinkFailsClosed(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "connections")); err != nil {
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
