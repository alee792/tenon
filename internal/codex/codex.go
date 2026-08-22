// Package codex renders an agent project into Codex's native project files.
// Codex-specific formats stay inside this package.
package codex

import (
	"strings"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/generated"
)

// Driver implements the apply seam for Codex.
type Driver struct{}

func (Driver) Harness() string { return "codex" }

// Generate renders AGENTS.md from the instructions body, .codex/config.toml
// wiring the managed MCP server, and every skill under .agents/skills/. An
// instructions-free project generates no always-on surface. Skill resources
// copy byte-for-byte with their executable intent; SKILL.md carries one
// inserted ownership marker line. A recognized vendor field Codex does not
// document honoring is copied unchanged and warned.
func (Driver) Generate(p *agentproject.Project, target apply.Target, diags *diagnostics.List) []apply.GeneratedFile {
	files := []apply.GeneratedFile{{
		Path:    ".codex/config.toml",
		Content: managedMCPConfig(target.Executable, p.Root, target.Workspace),
	}}
	if p.Instructions != nil {
		files = append(files, apply.GeneratedFile{
			Path:    "AGENTS.md",
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
				Path:       ".agents/skills/" + s.Name + "/" + f.RelPath,
				Content:    content,
				Executable: f.Executable,
			})
		}
		for _, field := range s.ClaudeFields {
			if field == "allowed-tools" {
				diags.Warnf("skill.vendor-field.not-honored", s.SourcePath+"/SKILL.md",
					"frontmatter field %q support is not documented by the selected harness (codex); the content was copied unchanged and may have no effect", field)
				continue
			}
			diags.Warnf("skill.vendor-field.not-honored", s.SourcePath+"/SKILL.md",
				"frontmatter field %q carries Claude-specific behavior that the selected harness (codex) does not document honoring; the content was copied unchanged and may have no effect", field)
		}
	}
	for _, sub := range p.Subagents {
		files = append(files, apply.GeneratedFile{
			Path:    ".codex/agents/" + sub.Name + ".toml",
			Content: generated.CodexSubagent(sub.Name, sub.Description, sub.Effort, sub.Body),
		})
	}
	// Harness-specific files copy byte-for-byte with no marker and no
	// transformation: tenon does not parse their semantics. Only this
	// harness's own files apply; claude's harness-specific files contribute
	// nothing here.
	for _, f := range p.HarnessFiles["codex"] {
		files = append(files, apply.GeneratedFile{
			Path:       f.RelPath,
			Content:    f.Content,
			Executable: f.Executable,
		})
	}
	return files
}

// managedMCPConfig renders .codex/config.toml: Codex's project configuration
// carrying exactly the tenon-owned managed stdio server, launched from the
// resolved tenon executable against the absolute agent source and workspace.
// The managed server alone is required and pre-approved, because tenon
// validates and audits every call that crosses its own boundary; every other
// generated entry keeps Codex's native per-call prompt approval. It is
// model-facing configuration, so it carries no fingerprint, version, or other
// setup metadata beyond the paths the server itself needs.
func managedMCPConfig(executable, source, workspace string) []byte {
	args := []string{"mcp", "serve", source, "--workspace", workspace, "--harness", "codex"}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = generated.TOMLString(arg)
	}
	var b strings.Builder
	b.WriteString(generated.TOMLHeader + "\n")
	b.WriteString("[mcp_servers.managed]\n")
	b.WriteString("command = " + generated.TOMLString(executable) + "\n")
	b.WriteString("args = [" + strings.Join(quoted, ", ") + "]\n")
	b.WriteString("required = true\n")
	b.WriteString("default_tools_approval_mode = \"approve\"\n")
	return []byte(b.String())
}
