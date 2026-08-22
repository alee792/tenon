// Package codex renders an agent project into Codex's native project files.
// Codex-specific formats stay inside this package.
package codex

import (
	"slices"
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
	resolved := agentproject.ResolveServers(p.PluginServers, target.Workspace, p.Name)
	for _, s := range resolved {
		if s.Transport == agentproject.TransportHTTP && len(s.Headers) > 0 {
			diags.Warnf("plugin.mcp.header.not-honored", s.SourcePath,
				"declared headers for server %q are not emitted into Codex project configuration, which tenon generates without header support; the server may fail to authenticate", s.Name)
		}
	}
	config := mcpConfig(target.Executable, p.Root, target.Workspace, resolved)
	if len(config) > generated.MaxMCPConfigBytes {
		diags.Errorf("plugin.mcp.bounds.exceeded", "plugins",
			"the generated .codex/config.toml may contain at most %d bytes; the accepted plugin MCP servers render %d",
			generated.MaxMCPConfigBytes, len(config))
	}
	files := []apply.GeneratedFile{{Path: ".codex/config.toml", Content: config}}
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

// mcpConfig renders .codex/config.toml: Codex's project configuration
// carrying the tenon-owned managed stdio server, launched from the resolved
// tenon executable against the absolute agent source and workspace, followed
// by every accepted plugin server in lexical order. The managed server alone
// is required and pre-approved, because tenon validates and audits every call
// that crosses its own boundary; every other generated entry is optional to
// start and keeps Codex's native per-server prompt approval. It is
// model-facing configuration, so it carries no fingerprint, version, or other
// setup metadata beyond the paths the servers themselves need.
func mcpConfig(executable, source, workspace string, servers []agentproject.ResolvedServer) []byte {
	var b strings.Builder
	b.WriteString(generated.TOMLHeader + "\n")
	b.WriteString("[mcp_servers.managed]\n")
	b.WriteString("command = " + generated.TOMLString(executable) + "\n")
	b.WriteString("args = " + tomlArray([]string{"mcp", "serve", source, "--workspace", workspace, "--harness", "codex"}) + "\n")
	b.WriteString("required = true\n")
	b.WriteString("default_tools_approval_mode = \"approve\"\n")

	sorted := slices.Clone(servers)
	slices.SortFunc(sorted, func(a, c agentproject.ResolvedServer) int {
		return strings.Compare(a.Name, c.Name)
	})
	for _, s := range sorted {
		// The portable server-name grammar is a bare TOML key by
		// construction, so the table header never needs quoting.
		b.WriteString("\n[mcp_servers." + s.Name + "]\n")
		if s.Transport == agentproject.TransportHTTP {
			b.WriteString("url = " + generated.TOMLString(s.URL) + "\n")
		} else {
			b.WriteString("command = " + generated.TOMLString(s.Command) + "\n")
			if len(s.Args) > 0 {
				b.WriteString("args = " + tomlArray(s.Args) + "\n")
			}
			if s.Cwd != "" {
				b.WriteString("cwd = " + generated.TOMLString(s.Cwd) + "\n")
			}
			if len(s.Env) > 0 {
				b.WriteString("env = " + tomlInlineTable(s.Env) + "\n")
			}
		}
		b.WriteString("required = false\n")
		b.WriteString("default_tools_approval_mode = \"prompt\"\n")
	}
	return []byte(b.String())
}

// tomlArray renders values as one TOML array of basic strings.
func tomlArray(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = generated.TOMLString(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// tomlInlineTable renders m as one TOML inline table with quoted keys, sorted
// so identical input always renders identical bytes.
func tomlInlineTable(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	pairs := make([]string, len(keys))
	for i, key := range keys {
		pairs[i] = generated.TOMLString(key) + " = " + generated.TOMLString(m[key])
	}
	return "{ " + strings.Join(pairs, ", ") + " }"
}
