// Package codex renders an agent project into Codex's native project files.
// Codex-specific formats stay inside this package.
package codex

import (
	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/generated"
)

// Driver implements the apply seam for Codex.
type Driver struct{}

func (Driver) Harness() string { return "codex" }

// Generate renders AGENTS.md from the instructions body. An
// instructions-free project generates no always-on surface.
func (Driver) Generate(p *agentproject.Project) []apply.GeneratedFile {
	var files []apply.GeneratedFile
	if p.Instructions != nil {
		files = append(files, apply.GeneratedFile{
			Path:    "AGENTS.md",
			Content: generated.Instructions(p.Instructions.Body),
		})
	}
	return files
}
