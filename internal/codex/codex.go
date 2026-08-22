// Package codex renders an agent project into Codex's native project files.
// Codex-specific formats stay inside this package.
package codex

import (
	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/generated"
)

// Driver implements the apply seam for Codex.
type Driver struct{}

func (Driver) Harness() string { return "codex" }

// Generate renders AGENTS.md from the instructions body and every skill
// under .agents/skills/. An instructions-free project generates no always-on
// surface. Skill resources copy byte-for-byte with their executable intent;
// SKILL.md carries one inserted ownership marker line. A recognized vendor
// field Codex does not document honoring is copied unchanged and warned.
func (Driver) Generate(p *agentproject.Project, diags *diagnostics.List) []apply.GeneratedFile {
	var files []apply.GeneratedFile
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
	return files
}
