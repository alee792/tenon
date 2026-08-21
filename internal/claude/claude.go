// Package claude renders an agent project into Claude Code's native project
// files. Claude-specific formats stay inside this package.
package claude

import (
	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/generated"
)

// Driver implements the apply seam for Claude Code.
type Driver struct{}

func (Driver) Harness() string { return "claude" }

// Generate renders CLAUDE.md from the instructions body and every skill
// under .claude/skills/. An instructions-free project generates no always-on
// surface. Skill resources copy byte-for-byte with their executable intent;
// SKILL.md carries one inserted ownership marker line. A vendor surface
// Claude does not document honoring is copied unchanged and warned.
func (Driver) Generate(p *agentproject.Project, diags *diagnostics.List) []apply.GeneratedFile {
	var files []apply.GeneratedFile
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
	return files
}
