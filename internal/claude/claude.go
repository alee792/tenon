// Package claude renders an agent project into Claude Code's native project
// files. Claude-specific formats stay inside this package.
package claude

import (
	"bytes"
	"encoding/json"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/generated"
	"github.com/alee792/tenon/internal/integration"
)

// Driver implements the apply seam for Claude Code.
type Driver struct{}

func (Driver) Harness() string { return "claude" }

// Generate renders CLAUDE.md from the instructions body, .mcp.json wiring the
// managed MCP server, and every skill under .claude/skills/. An
// instructions-free project generates no always-on surface. Skill resources
// copy byte-for-byte with their executable intent; SKILL.md carries one
// inserted ownership marker line. A vendor surface Claude does not document
// honoring is copied unchanged and warned. When a model is pinned (ADR 0020),
// it also owns .claude/settings.json — injected onto the authored base under
// harnesses/claude/ when present, generated fresh otherwise; see
// claudeSettingsFile.
func (Driver) Generate(p *agentproject.Project, target apply.Target, diags *diagnostics.List) []apply.GeneratedFile {
	connections := acceptedConnections(p, diags)
	resolved := agentproject.ResolveInstalledConnections(p.Connections, target.IntegrationStore, target.TenonVersion, diags)
	config := mcpConfig(target.Executable, p.Root, target.Workspace,
		acceptedServers(p, target, diags), connections, resolved)
	if len(config) > generated.MaxMCPConfigBytes {
		diags.Errorf("plugin.mcp.bounds.exceeded", "plugins",
			"the generated .mcp.json may contain at most %d bytes; the accepted plugin MCP servers render %d",
			generated.MaxMCPConfigBytes, len(config))
	}
	files := []apply.GeneratedFile{{Path: ".mcp.json", Content: config}}
	if p.Instructions != nil {
		files = append(files, apply.GeneratedFile{
			Path:    "CLAUDE.md",
			Content: generated.Instructions(p.Instructions.Body, connections),
		})
	}
	for _, s := range p.Skills {
		for _, f := range s.Files {
			content := f.Content
			if f.RelPath == "SKILL.md" {
				content = generated.SkillMD(f.Content, s.SkillMDBodyStart)
			}
			files = append(files, apply.GeneratedFile{
				Path:       ".claude/skills/" + s.Name + "/" + f.RelPath,
				Content:    content,
				Executable: f.Executable,
			})
		}
		if s.HasOpenAIYAML {
			diags.Warnf("skill.vendor-file.not-honored", s.SourcePath+"/agents/openai.yaml",
				"OpenAI host metadata is not documented by the selected harness (claude); the file was copied unchanged and may have no effect")
		}
	}
	for _, sub := range p.Subagents {
		files = append(files, apply.GeneratedFile{
			Path:    ".claude/agents/" + sub.Name + ".md",
			Content: generated.ClaudeSubagent(sub.Name, sub.Description, sub.Effort, sub.Body),
		})
	}
	settings, skipAuthoredSettings := claudeSettingsFile(p, target, diags)
	if settings != nil {
		files = append(files, *settings)
	}
	// Harness-specific files copy byte-for-byte with no marker and no
	// transformation: tenon does not parse their semantics. Only this
	// harness's own files apply; codex's harness-specific files contribute
	// nothing here. The authored .claude/settings.json is the one exception:
	// when a model is pinned, tenon injects it above and drops the raw
	// passthrough here so the workspace never gets two conflicting writes to
	// the same path.
	for _, f := range p.HarnessFiles["claude"] {
		if skipAuthoredSettings && f.RelPath == claudeSettingsRelPath {
			continue
		}
		files = append(files, apply.GeneratedFile{
			Path:       f.RelPath,
			Content:    f.Content,
			Executable: f.Executable,
		})
	}
	return files
}

// claudeSettingsRelPath is the workspace-relative destination of Claude's
// native settings file — both the author's base under
// harnesses/claude/.claude/settings.json and the generated file apply writes.
const claudeSettingsRelPath = ".claude/settings.json"

// claudeSettingsFile renders the tenon-owned .claude/settings.json carrying
// the pinned model (ADR 0020). When target.Model is "", it returns (nil,
// false): no model is pinned, tenon owns no settings.json, and the author's
// base (if any) continues to pass through byte-for-byte exactly as before
// this ADR.
//
// When a model is pinned, the author's base under
// harnesses/claude/.claude/settings.json — if present — is parsed as JSON and
// injected into: its "model" key is set or overridden, every other key is
// preserved, and the result is marshaled deterministically (sorted keys,
// two-space indent, trailing newline). skipAuthored reports true whenever an
// author base was found, so the caller drops it from the raw harness-file
// passthrough rather than writing it twice. Invalid author JSON is a
// claude.settings.invalid diagnostic reported before any mutation; no
// settings.json is generated for that apply, and the broken base is still
// dropped from the passthrough since tenon attempted to own it.
//
// Absent an author base, the generated file carries only the model key.
func claudeSettingsFile(p *agentproject.Project, target apply.Target, diags *diagnostics.List) (file *apply.GeneratedFile, skipAuthored bool) {
	if target.Model == "" {
		return nil, false
	}
	settings := map[string]any{}
	for _, f := range p.HarnessFiles["claude"] {
		if f.RelPath != claudeSettingsRelPath {
			continue
		}
		// UseNumber keeps authored numeric literals exact rather than
		// round-tripping them through float64, so tenon injects the model
		// without silently reformatting an author's numeric setting.
		dec := json.NewDecoder(bytes.NewReader(f.Content))
		dec.UseNumber()
		if err := dec.Decode(&settings); err != nil {
			diags.Errorf("claude.settings.invalid", "harnesses/claude/"+claudeSettingsRelPath,
				"the authored .claude/settings.json is not valid JSON and cannot receive the pinned model: %v", err)
			return nil, true
		}
		skipAuthored = true
		break
	}
	settings["model"] = target.Model
	// Encode with HTML escaping off so an authored string containing <, >, or &
	// (for example a hook command using shell operators) is preserved verbatim
	// rather than rewritten to \u00XX escapes.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		// settings is JSON-decoded values plus one string key; marshaling it
		// back to JSON cannot fail.
		diags.Errorf("claude.settings.invalid", claudeSettingsRelPath,
			"encoding the generated settings.json failed: %v", err)
		return nil, skipAuthored
	}
	// json.Encoder.Encode already appends a trailing newline.
	return &apply.GeneratedFile{Path: claudeSettingsRelPath, Content: buf.Bytes()}, skipAuthored
}

// acceptedServers expands every accepted plugin MCP server (ADR 0010) for
// this workspace and drops the ones Claude's own environment-expansion pass
// could turn into something the portable specification treats as literal.
// Claude expands project MCP values itself, so text that still looks like a
// placeholder after portable expansion could substitute an ambient secret;
// tenon skips such a server for this harness alone rather than risk it.
func acceptedServers(p *agentproject.Project, target apply.Target, diags *diagnostics.List) []agentproject.ResolvedServer {
	var out []agentproject.ResolvedServer
	for _, s := range agentproject.ResolveServers(p.PluginServers, target.Workspace, p.Name) {
		if s.Placeholder != "" {
			diags.Warnf("plugin.mcp.claude-expansion", s.SourcePath,
				"MCP server %q is skipped for the selected harness (claude): its %s %q still contains placeholder-like ${...} text after portable expansion, and claude expands project MCP values itself",
				s.Name, s.PlaceholderField, diagnostics.Bound(s.Placeholder, 256))
			continue
		}
		out = append(out, s)
	}
	return out
}

// claudeReservedConnectionNames are native Claude project surface names a
// standalone connection may never claim (ADR 0016): tenon cannot preflight
// harness-owned higher-precedence configuration, so the collision is
// reported here, at generation, for this harness alone.
var claudeReservedConnectionNames = map[string]bool{
	"workspace":        true,
	"claude-in-chrome": true,
	"computer-use":     true,
}

// acceptedConnections drops every connection whose name collides with a name
// Claude's native project surface reserves, reporting an error rather than
// silently renaming or shadowing it (ADR 0016). Connections arrive already
// sorted by name from agentproject.Load, so the accepted subset stays sorted.
func acceptedConnections(p *agentproject.Project, diags *diagnostics.List) []agentproject.Connection {
	var out []agentproject.Connection
	for _, c := range p.Connections {
		if claudeReservedConnectionNames[c.Name] {
			diags.Errorf("mcp.name.reserved", c.SourcePath,
				"the connection name %q is reserved by the selected harness (claude)'s native project surface", c.Name)
			continue
		}
		out = append(out, c)
	}
	return out
}

// mcpConfig renders .mcp.json: Claude's project MCP configuration carrying
// the tenon-owned managed stdio server, launched from the resolved tenon
// executable against the absolute agent source and workspace, every accepted
// plugin server, every accepted remote connection as a native http entry
// carrying its declared headers verbatim (Claude expands ${VAR} references
// itself), and every accepted installed connection that resolved cleanly as
// a native stdio entry from its launch descriptor. An installed connection
// absent from resolved already carries a mcp.package.* error on diags and
// contributes no entry. It is model-facing configuration, so it carries no
// fingerprint, version, or other setup metadata beyond the paths the servers
// themselves need. Keys are ordered by encoding/json's sorted map
// marshalling, so identical input always renders identical bytes.
func mcpConfig(executable, source, workspace string, servers []agentproject.ResolvedServer, connections []agentproject.Connection, resolved map[string]*integration.LaunchDescriptor) []byte {
	entries := map[string]any{"managed": map[string]any{
		"type":    "stdio",
		"command": executable,
		"args":    []string{"mcp", "serve", source, "--workspace", workspace, "--harness", "claude"},
	}}
	for _, s := range servers {
		entries[s.Name] = serverEntry(s)
	}
	for _, c := range connections {
		switch c.Kind {
		case agentproject.ConnectionKindInstalled:
			desc, ok := resolved[c.Name]
			if !ok {
				continue // already reported as mcp.package.unresolved/mismatch
			}
			entries[c.Name] = installedServerEntry(desc)
		default:
			entry := map[string]any{"type": "http", "url": c.URL}
			if len(c.Headers) > 0 {
				entry["headers"] = c.Headers
			}
			entries[c.Name] = entry
		}
	}
	// A fixed map of strings, string slices, and string maps always encodes.
	content, _ := json.MarshalIndent(map[string]any{"mcpServers": entries}, "", "  ")
	return append(content, '\n')
}

// installedServerEntry renders one resolved installed connection in Claude's
// project format: the /usr/bin/env -C adapter carrying the descriptor's
// absolute workdir and executable, its literal args, and its non-secret env
// defaults. .mcp.json cannot forward an ambient value by name, so the server
// simply inherits the launch environment: a required-ambient NAME is never
// written here as a value or otherwise.
func installedServerEntry(desc *integration.LaunchDescriptor) map[string]any {
	args := append([]string{"-C", desc.Workdir, "--", desc.Executable}, desc.Args...)
	return map[string]any{"type": "stdio", "command": "/usr/bin/env", "args": args, "env": desc.Env}
}

// serverEntry renders one accepted plugin server in Claude's project format.
// That format carries no working-directory field, so a declared directory is
// preserved exactly by wrapping the command in the system exec adapter, which
// changes directory before replacing itself with the declared command.
func serverEntry(s agentproject.ResolvedServer) map[string]any {
	if s.Transport == agentproject.TransportHTTP {
		entry := map[string]any{"type": "http", "url": s.URL}
		if len(s.Headers) > 0 {
			entry["headers"] = s.Headers
		}
		return entry
	}
	command, args := s.Command, s.Args
	if s.Cwd != "" {
		args = append([]string{"-C", s.Cwd, "--", command}, args...)
		command = "/usr/bin/env"
	}
	entry := map[string]any{"type": "stdio", "command": command, "env": s.Env}
	if len(args) > 0 {
		entry["args"] = args
	}
	return entry
}
