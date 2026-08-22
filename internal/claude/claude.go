// Package claude renders an agent project into Claude Code's native project
// files. Claude-specific formats stay inside this package.
package claude

import (
	"encoding/json"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/generated"
)

// Driver implements the apply seam for Claude Code.
type Driver struct{}

func (Driver) Harness() string { return "claude" }

// Generate renders CLAUDE.md from the instructions body, .mcp.json wiring the
// managed MCP server, and every skill under .claude/skills/. An
// instructions-free project generates no always-on surface. Skill resources
// copy byte-for-byte with their executable intent; SKILL.md carries one
// inserted ownership marker line. A vendor surface Claude does not document
// honoring is copied unchanged and warned.
func (Driver) Generate(p *agentproject.Project, target apply.Target, diags *diagnostics.List) []apply.GeneratedFile {
	config := mcpConfig(target.Executable, p.Root, target.Workspace,
		acceptedServers(p, target, diags))
	if len(config) > generated.MaxMCPConfigBytes {
		diags.Errorf("plugin.mcp.bounds.exceeded", "plugins",
			"the generated .mcp.json may contain at most %d bytes; the accepted plugin MCP servers render %d",
			generated.MaxMCPConfigBytes, len(config))
	}
	files := []apply.GeneratedFile{{Path: ".mcp.json", Content: config}}
	if p.Instructions != nil {
		files = append(files, apply.GeneratedFile{
			Path:    "CLAUDE.md",
			Content: generated.Instructions(p.Instructions.Body),
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
	// Harness-specific files copy byte-for-byte with no marker and no
	// transformation: tenon does not parse their semantics. Only this
	// harness's own files apply; codex's harness-specific files contribute
	// nothing here.
	for _, f := range p.HarnessFiles["claude"] {
		files = append(files, apply.GeneratedFile{
			Path:       f.RelPath,
			Content:    f.Content,
			Executable: f.Executable,
		})
	}
	return files
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

// mcpConfig renders .mcp.json: Claude's project MCP configuration carrying
// the tenon-owned managed stdio server, launched from the resolved tenon
// executable against the absolute agent source and workspace, plus every
// accepted plugin server. It is model-facing configuration, so it carries no
// fingerprint, version, or other setup metadata beyond the paths the servers
// themselves need. Keys are ordered by encoding/json's sorted map
// marshalling, so identical input always renders identical bytes.
func mcpConfig(executable, source, workspace string, servers []agentproject.ResolvedServer) []byte {
	entries := map[string]any{"managed": map[string]any{
		"type":    "stdio",
		"command": executable,
		"args":    []string{"mcp", "serve", source, "--workspace", workspace, "--harness", "claude"},
	}}
	for _, s := range servers {
		entries[s.Name] = serverEntry(s)
	}
	// A fixed map of strings, string slices, and string maps always encodes.
	content, _ := json.MarshalIndent(map[string]any{"mcpServers": entries}, "", "  ")
	return append(content, '\n')
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
