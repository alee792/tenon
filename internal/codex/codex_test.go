package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/generated"
	"github.com/alee792/tenon/internal/integration"
)

// TestGeneratedMCPConfigCeilingIsAnError proves the generated-configuration
// ceiling (ADR 0013) is measured against the fully rendered
// .codex/config.toml and reported as an error, which apply checks before it
// mutates the workspace.
func TestGeneratedMCPConfigCeilingIsAnError(t *testing.T) {
	p := &agentproject.Project{
		Root: "/src/my-agent",
		Name: "my-agent",
		PluginServers: []agentproject.PluginServer{{
			Name:       "huge",
			Plugin:     "vendor-x",
			PluginRoot: "/src/my-agent/plugins/vendor-x",
			SourcePath: "plugins/vendor-x/mcp.json",
			Transport:  agentproject.TransportStdio,
			Command:    "huge-server",
			Args:       []string{strings.Repeat("a", generated.MaxMCPConfigBytes)},
		}},
	}
	diags := &diagnostics.List{}
	Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)

	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.mcp.bounds.exceeded" && d.Severity == diagnostics.Error {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an error plugin.mcp.bounds.exceeded, got %v", diags.All())
	}
}

// TestModelPinRendersTopLevelKeyAboveManagedTable proves a pinned Target.Model
// (ADR 0020) renders as one top-level `model` key in the generated
// .codex/config.toml, positioned above [mcp_servers.managed] as TOML requires
// for top-level keys to precede tables.
func TestModelPinRendersTopLevelKeyAboveManagedTable(t *testing.T) {
	p := &agentproject.Project{Root: "/src/my-agent", Name: "my-agent"}
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon", Model: "claude-opus-4"}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var config string
	for _, f := range files {
		if f.Path == ".codex/config.toml" {
			config = string(f.Content)
		}
	}
	modelIdx := strings.Index(config, `model = "claude-opus-4"`)
	tableIdx := strings.Index(config, "[mcp_servers.managed]")
	if modelIdx < 0 || tableIdx < 0 {
		t.Fatalf("expected both a model key and the managed table: %s", config)
	}
	if modelIdx > tableIdx {
		t.Fatalf("the model key must precede the first table: %s", config)
	}
}

// TestNoModelPinLeavesConfigTOMLUnchanged proves an empty Target.Model (no
// manifest supplied, or a manifest that pins no model) renders
// .codex/config.toml byte-identical to a target that never carried the Model
// field at all — the pre-ADR-0020 behavior.
func TestNoModelPinLeavesConfigTOMLUnchanged(t *testing.T) {
	p := &agentproject.Project{Root: "/src/my-agent", Name: "my-agent"}

	diagsNoModel := &diagnostics.List{}
	withoutModel := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diagsNoModel)

	diagsEmptyModel := &diagnostics.List{}
	withEmptyModel := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon", Model: ""}, diagsEmptyModel)

	var a, b []byte
	for _, f := range withoutModel {
		if f.Path == ".codex/config.toml" {
			a = f.Content
		}
	}
	for _, f := range withEmptyModel {
		if f.Path == ".codex/config.toml" {
			b = f.Content
		}
	}
	if len(a) == 0 || string(a) != string(b) {
		t.Fatalf("config.toml without a model pin must be unchanged:\n%s\nvs\n%s", a, b)
	}
	if strings.Contains(string(a), "model =") {
		t.Fatalf("config.toml without a pinned model must carry no model key: %s", a)
	}
}

// TestRemoteHeadersWarnForCodex proves declared remote headers, which the
// generated Codex configuration does not carry, are reported rather than
// silently dropped, while the url entry itself still renders.
func TestRemoteHeadersWarnForCodex(t *testing.T) {
	p := &agentproject.Project{
		Root: "/src/my-agent",
		Name: "my-agent",
		PluginServers: []agentproject.PluginServer{{
			Name:       "remote",
			Plugin:     "vendor-x",
			PluginRoot: "/src/my-agent/plugins/vendor-x",
			SourcePath: "plugins/vendor-x/mcp.json",
			Transport:  agentproject.TransportHTTP,
			URL:        "https://example.com/mcp",
			Headers:    map[string]string{"X-Trace": "on"},
		}},
	}
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)

	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.mcp.header.not-honored" && d.Severity == diagnostics.Warning &&
			d.Path == "plugins/vendor-x/mcp.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plugin.mcp.header.not-honored warning, got %v", diags.All())
	}
	for _, f := range files {
		if f.Path == ".codex/config.toml" {
			if !strings.Contains(string(f.Content), "[mcp_servers.remote]") ||
				strings.Contains(string(f.Content), "X-Trace") {
				t.Fatalf("remote server must render without headers: %s", f.Content)
			}
		}
	}
}

// TestConnectionHeadersWarnForCodex proves a remote connection's declared
// headers, which the generated Codex configuration does not carry, are
// reported rather than silently dropped, while the url entry itself still
// renders (issue #49).
func TestConnectionHeadersWarnForCodex(t *testing.T) {
	p := &agentproject.Project{
		Root:         "/src/my-agent",
		Name:         "my-agent",
		Instructions: &agentproject.Instructions{Body: "Body text.\n"},
		Connections: []agentproject.Connection{
			{
				Kind:       agentproject.ConnectionKindRemote,
				Name:       "catalog",
				URL:        "https://example.com/mcp",
				Headers:    map[string]string{"Authorization": "Bearer ${ACME_TOKEN}"},
				SourcePath: "mcp/catalog.md",
			},
		},
	}
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)

	found := false
	for _, d := range diags.All() {
		if d.ID == "mcp.header.not-honored" && d.Severity == diagnostics.Warning &&
			d.Path == "mcp/catalog.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected mcp.header.not-honored warning, got %v", diags.All())
	}
	for _, f := range files {
		if f.Path == ".codex/config.toml" {
			if !strings.Contains(string(f.Content), "[mcp_servers.catalog]") ||
				strings.Contains(string(f.Content), "ACME_TOKEN") {
				t.Fatalf("connection must render without headers: %s", f.Content)
			}
		}
	}
}

func projectWithConnection(name, url, context string) *agentproject.Project {
	return &agentproject.Project{
		Root:         "/src/my-agent",
		Name:         "my-agent",
		Instructions: &agentproject.Instructions{Body: "Body text.\n"},
		Connections: []agentproject.Connection{
			{Kind: agentproject.ConnectionKindRemote, Name: name, URL: url, Context: context, SourcePath: "mcp/" + name + ".md"},
		},
	}
}

// TestConnectionRendersAsCodexHTTPServer proves a standalone connection
// renders into .codex/config.toml as a startup-optional entry, and into the
// generated AGENTS.md's connections section (ADR 0016).
func TestConnectionRendersAsCodexHTTPServer(t *testing.T) {
	p := projectWithConnection("catalog", "https://example.com/mcp", "Use for the catalog.")
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var config, agentsMD string
	for _, f := range files {
		switch f.Path {
		case ".codex/config.toml":
			config = string(f.Content)
		case "AGENTS.md":
			agentsMD = string(f.Content)
		}
	}
	want := "\n[mcp_servers.catalog]\n" +
		`url = "https://example.com/mcp"` + "\n" +
		"required = false\n" +
		`default_tools_approval_mode = "prompt"` + "\n"
	if !strings.Contains(config, want) {
		t.Fatalf("expected exact connection entry in config.toml:\ngot:\n%s\nwant substring:\n%s", config, want)
	}
	if !strings.Contains(agentsMD, "### catalog") || !strings.Contains(agentsMD, "Use for the catalog.") {
		t.Fatalf("expected the connections section in AGENTS.md: %s", agentsMD)
	}
}

// TestComposedProjectOmitsShadowedAndMaskedPluginServers proves the codex
// driver, like the claude driver, renders exactly the composition
// internal/agentproject already decided (ADR 0026, issue #53): a Project as
// Load would hand back after shadowing or masking a plugin server carries no
// trace of the suppressed server in PluginServers, so neither driver needs
// any shadow- or mask-aware logic of its own.
func TestComposedProjectOmitsShadowedAndMaskedPluginServers(t *testing.T) {
	p := &agentproject.Project{
		Root:         "/src/my-agent",
		Name:         "my-agent",
		Instructions: &agentproject.Instructions{Body: "Body text.\n"},
		PluginServers: []agentproject.PluginServer{{
			Name:       "other",
			Plugin:     "vendor-x",
			PluginRoot: "/src/my-agent/plugins/vendor-x",
			SourcePath: "plugins/vendor-x/mcp.json",
			Transport:  agentproject.TransportHTTP,
			URL:        "https://example.com/other",
		}},
		Connections: []agentproject.Connection{
			{Kind: agentproject.ConnectionKindRemote, Name: "catalog", URL: "https://example.com/mcp", SourcePath: "mcp/catalog.md"},
		},
	}
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var config string
	for _, f := range files {
		if f.Path == ".codex/config.toml" {
			config = string(f.Content)
		}
	}
	if !strings.Contains(config, "[mcp_servers.catalog]") {
		t.Fatalf("expected the authored catalog server to render: %s", config)
	}
	if !strings.Contains(config, "[mcp_servers.other]") {
		t.Fatalf("expected the surviving plugin server other to render: %s", config)
	}
	if strings.Contains(config, "[mcp_servers.legacy]") {
		t.Fatalf("expected the masked plugin server legacy to never render: %s", config)
	}
}

// TestClaudeReservedConnectionNamePassesForCodex proves the Claude-only
// native surface reservation (workspace, claude-in-chrome, computer-use) is
// a per-harness rule: those names are ordinary, accepted connection names
// for Codex.
func TestClaudeReservedConnectionNamePassesForCodex(t *testing.T) {
	for _, name := range []string{"workspace", "claude-in-chrome", "computer-use"} {
		p := projectWithConnection(name, "https://example.com/mcp", "")
		diags := &diagnostics.List{}
		files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)
		if diags.HasErrors() {
			t.Fatalf("name %q: unexpected diagnostics for codex: %v", name, diags.All())
		}
		found := false
		for _, f := range files {
			if f.Path == ".codex/config.toml" && strings.Contains(string(f.Content), "[mcp_servers."+name+"]") {
				found = true
			}
		}
		if !found {
			t.Fatalf("name %q: expected the connection to render for codex", name)
		}
	}
}

// --- Installed connections (ADR 0016 closing the installed form) ----------

const codexFixtureTenonVersion = "1.0.0"

func codexFixturePayload() []byte { return []byte("#!/bin/sh\necho fake-native-mcp\n") }

// installCodexFixture writes and installs a fixture native-mcp package into
// a fresh temp store, whose native server name equals serverName.
func installCodexFixture(t *testing.T, id, serverName string) string {
	t.Helper()
	p := codexFixturePayload()
	sum := sha256.Sum256(p)
	sha := hex.EncodeToString(sum[:])
	m := map[string]any{
		"schema_version": 1,
		"id":             id,
		"version":        "1.0.0",
		"name":           "Codex Driver Fixture",
		"description":    "A credential-free fake native MCP server for driver tests.",
		"license":        "MIT",
		"source":         "https://example.test/fixture",
		"revision":       "abc123",
		"compat":         map[string]any{"minimum": "0.0.1", "before": "2.0.0"},
		"artifacts": []any{map[string]any{
			"id":          "server-host",
			"os":          runtime.GOOS,
			"arch":        runtime.GOARCH,
			"format":      "binary",
			"size":        len(p),
			"sha256":      sha,
			"exec_path":   "bin/server",
			"exec_size":   len(p),
			"exec_sha256": sha,
			"package":     "payload/server",
		}},
		"capabilities": []any{map[string]any{
			"id":          "mcp",
			"type":        "native-mcp",
			"version":     1,
			"server_name": serverName,
			"artifacts":   []any{"server-host"},
			"executable":  "bin/server",
			"args":        []any{"--stdio"},
			"workdir":     "",
			"env":         map[string]any{"LOG_LEVEL": "info"},
			"required_env": []any{
				map[string]any{"name": "DEMO_TOKEN", "description": "The ambient demo token the fixture server reads from its own environment."},
			},
			"targets": map[string]any{
				"codex": map[string]any{"startup": "optional"},
			},
		}},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "integration.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "payload", "server"), p, 0o600); err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	store := integration.NewStore(base)
	if _, err := store.Install(integration.InstallRequest{
		Source: src, TrustOperator: true, TenonVersion: codexFixtureTenonVersion, OS: runtime.GOOS, Arch: runtime.GOARCH,
	}); err != nil {
		t.Fatal(err)
	}
	return base
}

func projectWithInstalledConnection(name, pkg, capability string) *agentproject.Project {
	return &agentproject.Project{
		Root: "/src/my-agent",
		Name: "my-agent",
		Connections: []agentproject.Connection{
			{Kind: agentproject.ConnectionKindInstalled, Name: name, Package: pkg, Capability: capability, SourcePath: "mcp/" + name + ".md"},
		},
	}
}

// TestInstalledConnectionRendersNativeCommandEntry proves a resolved
// installed connection renders into .codex/config.toml as a
// command/args/cwd/env entry from the launch descriptor, forwarding the
// required ambient name by name only through env_vars (ADR 0016) — never as
// a value.
func TestInstalledConnectionRendersNativeCommandEntry(t *testing.T) {
	base := installCodexFixture(t, "demo-pkg", "demo")
	p := projectWithInstalledConnection("demo", "demo-pkg", "mcp")
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{
		Workspace: "/ws", Executable: "/bin/tenon",
		IntegrationStore: base, TenonVersion: codexFixtureTenonVersion,
	}, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var config string
	for _, f := range files {
		if f.Path == ".codex/config.toml" {
			config = string(f.Content)
		}
	}
	if !strings.Contains(config, "[mcp_servers.demo]") {
		t.Fatalf("missing the installed connection table: %s", config)
	}
	if !strings.Contains(config, filepath.Join("bin", "server")) {
		t.Fatalf("missing the prepared absolute executable: %s", config)
	}
	if !strings.Contains(config, `args = ["--stdio"]`) {
		t.Fatalf("missing the literal args: %s", config)
	}
	if !strings.Contains(config, `env = { "LOG_LEVEL" = "info" }`) {
		t.Fatalf("missing the non-secret env default: %s", config)
	}
	if !strings.Contains(config, `env_vars = ["DEMO_TOKEN"]`) {
		t.Fatalf("the required ambient name must be forwarded by name only: %s", config)
	}
	if !strings.Contains(config, "required = false") || !strings.Contains(config, `default_tools_approval_mode = "prompt"`) {
		t.Fatalf("an installed connection must stay startup-optional with native prompt approval: %s", config)
	}
}

// TestInstalledConnectionServerNameMismatchFailsForCodex proves a connection
// whose filename differs from the capability's declared native server name
// fails with mcp.package.mismatch.
func TestInstalledConnectionServerNameMismatchFailsForCodex(t *testing.T) {
	base := installCodexFixture(t, "demo-pkg", "actual-name")
	p := projectWithInstalledConnection("demo", "demo-pkg", "mcp")
	diags := &diagnostics.List{}
	Driver{}.Generate(p, apply.Target{
		Workspace: "/ws", Executable: "/bin/tenon",
		IntegrationStore: base, TenonVersion: codexFixtureTenonVersion,
	}, diags)

	found := false
	for _, d := range diags.All() {
		if d.ID == "mcp.package.mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected mcp.package.mismatch, got %v", diags.All())
	}
}

// --- Stdio connection rendering (ADR 0026, issue #50) -----------------------

func stdioConnectionProject() *agentproject.Project {
	return &agentproject.Project{
		Root:         "/src/my-agent",
		Name:         "my-agent",
		Instructions: &agentproject.Instructions{Body: "Body text.\n"},
		Connections: []agentproject.Connection{
			{
				Kind:    agentproject.ConnectionKindStdio,
				Name:    "deployctl",
				Command: "/src/my-agent/servers/deployctl/bin/deployctl",
				Args:    []string{"--flag"},
				Env: map[string]string{
					"MODE":   "prod",
					"TOKEN":  "${ACME_TOKEN}",
					"PREFIX": "Bearer ${ACME_TOKEN}",
				},
				SourcePath: "mcp/deployctl.md",
			},
		},
	}
}

// TestStdioConnectionRendersForCodex proves command/args/cwd render directly,
// a literal env value renders verbatim, a bare ${VAR} reference is forwarded
// by name only through env_vars, and a prefixed reference is reported
// unforwardable and omitted rather than rendered as a literal nobody expands.
func TestStdioConnectionRendersForCodex(t *testing.T) {
	p := stdioConnectionProject()
	diags := &diagnostics.List{}
	files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)

	var config string
	for _, f := range files {
		if f.Path == ".codex/config.toml" {
			config = string(f.Content)
		}
	}
	if !strings.Contains(config, "[mcp_servers.deployctl]") {
		t.Fatalf("missing the deployctl table: %s", config)
	}
	if !strings.Contains(config, `command = "/src/my-agent/servers/deployctl/bin/deployctl"`) {
		t.Fatalf("missing the absolute resolved command: %s", config)
	}
	if !strings.Contains(config, `args = ["--flag"]`) {
		t.Fatalf("missing the literal args: %s", config)
	}
	if !strings.Contains(config, `cwd = "/src/my-agent"`) {
		t.Fatalf("an undeclared cwd must default to the agent root: %s", config)
	}
	if !strings.Contains(config, `"MODE" = "prod"`) {
		t.Fatalf("a literal env value must render verbatim: %s", config)
	}
	if strings.Contains(config, "ACME_TOKEN\" = ") || strings.Contains(config, `"TOKEN" = "${ACME_TOKEN}"`) {
		t.Fatalf("a bare ${VAR} reference must never render its literal text into env: %s", config)
	}
	if !strings.Contains(config, `env_vars = ["TOKEN"]`) {
		t.Fatalf("a bare ${VAR} reference must be forwarded by name only through env_vars: %s", config)
	}
	if strings.Contains(config, "PREFIX") {
		t.Fatalf("a prefixed ${VAR} reference must be omitted entirely, not rendered: %s", config)
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "mcp.env.not-honored" && d.Severity == diagnostics.Warning {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected mcp.env.not-honored warning, got %v", diags.All())
	}
}

// TestCodexMCPConfigDeterministic proves two renders of the same project
// produce byte-identical .codex/config.toml output, including a stdio
// connection's split env.
func TestCodexMCPConfigDeterministic(t *testing.T) {
	p := stdioConnectionProject()
	render := func() []byte {
		diags := &diagnostics.List{}
		files := Driver{}.Generate(p, apply.Target{Workspace: "/ws", Executable: "/bin/tenon"}, diags)
		for _, f := range files {
			if f.Path == ".codex/config.toml" {
				return f.Content
			}
		}
		t.Fatal(".codex/config.toml not generated")
		return nil
	}
	a, b := render(), render()
	if string(a) != string(b) {
		t.Fatalf("identical input must render byte-identical output:\n%s\nvs\n%s", a, b)
	}
}
