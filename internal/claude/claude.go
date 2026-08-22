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
	files := []apply.GeneratedFile{{
		Path:    ".mcp.json",
		Content: managedMCPConfig(target.Executable, p.Root, target.Workspace),
	}}
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

// managedMCPConfig renders .mcp.json: Claude's project MCP configuration
// carrying exactly the tenon-owned managed stdio server, launched from the
// resolved tenon executable against the absolute agent source and workspace.
// It is model-facing configuration, so it carries no fingerprint, version, or
// other setup metadata beyond the paths the server itself needs. Keys are
// ordered by encoding/json's sorted map marshalling, so identical input
// always renders identical bytes.
func managedMCPConfig(executable, source, workspace string) []byte {
	config := map[string]any{"mcpServers": map[string]any{"managed": map[string]any{
		"type":    "stdio",
		"command": executable,
		"args":    []string{"mcp", "serve", source, "--workspace", workspace, "--harness", "claude"},
	}}}
	// A fixed map of strings and string slices always encodes.
	content, _ := json.MarshalIndent(config, "", "  ")
	return append(content, '\n')
}
