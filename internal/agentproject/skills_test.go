package agentproject

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func minimalSkillMD(name string) string {
	return "---\nname: " + name + "\ndescription: Does one thing.\n---\n\nBody.\n"
}

func writeSkillFile(t *testing.T, root, rel string, content []byte, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadValidSkillProject(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "skills/echo/SKILL.md", []byte(minimalSkillMD("echo")), 0o644)
	writeSkillFile(t, root, "skills/echo/scripts/run.sh", []byte("#!/bin/sh\necho hi\n"), 0o755)
	writeSkillFile(t, root, "skills/echo/references/notes.md", []byte("notes\n"), 0o644)
	writeSkillFile(t, root, "skills/echo/agents/openai.yaml", []byte("interface: {}\n"), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Skills) != 1 {
		t.Fatalf("skills = %+v", p.Skills)
	}
	s := p.Skills[0]
	if s.Name != "echo" || s.SourcePath != "skills/echo" {
		t.Fatalf("skill identity = %q %q", s.Name, s.SourcePath)
	}
	var rels []string
	for _, f := range s.Files {
		rels = append(rels, f.RelPath)
	}
	want := []string{"SKILL.md", "agents/openai.yaml", "references/notes.md", "scripts/run.sh"}
	if !slices.Equal(rels, want) {
		t.Fatalf("files = %v, want SKILL.md first then lexical %v", rels, want)
	}
	if !s.Files[3].Executable || s.Files[2].Executable {
		t.Fatalf("executable intent must follow the authored mode: %+v", s.Files)
	}
	if !s.HasOpenAIYAML {
		t.Fatal("agents/openai.yaml must be noted for the Claude generation warning")
	}
	if len(s.ClaudeFields) != 0 {
		t.Fatalf("a portable-only skill must record no vendor fields: %v", s.ClaudeFields)
	}
	md := string(s.Files[0].Content)
	if !strings.HasSuffix(md[:s.SkillMDBodyStart], "---\n") || md[s.SkillMDBodyStart:] != "\nBody.\n" {
		t.Fatalf("body start = %d in %q", s.SkillMDBodyStart, md)
	}
}

func TestLoadSkillRecordsClaudeFieldsSorted(t *testing.T) {
	skillMD := `---
name: vendor
description: Uses vendor fields.
when_to_use: Testing vendor fields.
model: opus
allowed-tools: Bash Read
license: MIT
compatibility: any shell
metadata:
  team: core
---

Body.
`
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "skills/vendor/SKILL.md", []byte(skillMD), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	got := p.Skills[0].ClaudeFields
	want := []string{"allowed-tools", "model", "when_to_use"}
	if !slices.Equal(got, want) {
		t.Fatalf("ClaudeFields = %v, want the recognized vendor fields sorted %v", got, want)
	}
}

func TestLoadRejectsSkillViolations(t *testing.T) {
	cases := map[string]struct {
		dir     string
		skillMD string
		id      string
	}{
		"uppercase directory": {"Echo-X", minimalSkillMD("Echo-X"), "skill.name.invalid"},
		"leading hyphen":      {"-echo", minimalSkillMD("-echo"), "skill.name.invalid"},
		"double hyphen":       {"e--o", minimalSkillMD("e--o"), "skill.name.invalid"},
		"overlong name": {strings.Repeat("a", 65), minimalSkillMD(strings.Repeat("a", 65)),
			"skill.name.invalid"},
		"name mismatch":       {"echo", minimalSkillMD("other"), "skill.name.mismatch"},
		"missing name":        {"echo", "---\ndescription: d\n---\n\nBody.\n", "skill.name.mismatch"},
		"missing frontmatter": {"echo", "Body only.\n", "skill.frontmatter.missing"},
		"duplicate field": {"echo", "---\nname: echo\nname: echo\ndescription: d\n---\nBody.\n",
			"skill.frontmatter.invalid"},
		"unknown field": {"echo", "---\nname: echo\ndescription: d\ntemperature: 1\n---\nBody.\n",
			"skill.frontmatter.unknown-field"},
		"missing description": {"echo", "---\nname: echo\n---\nBody.\n", "skill.description.missing"},
		"empty description":   {"echo", "---\nname: echo\ndescription: \"\"\n---\nBody.\n", "skill.description.invalid"},
		"overlong description": {"echo", "---\nname: echo\ndescription: " + strings.Repeat("d", 1025) + "\n---\nBody.\n",
			"skill.description.invalid"},
		"non-string license": {"echo", "---\nname: echo\ndescription: d\nlicense: [MIT]\n---\nBody.\n",
			"skill.license.invalid"},
		"overlong compatibility": {"echo", "---\nname: echo\ndescription: d\ncompatibility: " + strings.Repeat("c", 501) + "\n---\nBody.\n",
			"skill.compatibility.invalid"},
		"non-string metadata": {"echo", "---\nname: echo\ndescription: d\nmetadata:\n  a: [1]\n---\nBody.\n",
			"skill.metadata.invalid"},
		"allowed-tools list": {"echo", "---\nname: echo\ndescription: d\nallowed-tools: [Bash]\n---\nBody.\n",
			"skill.allowed-tools.invalid"},
		"null vendor field": {"echo", "---\nname: echo\ndescription: d\nmodel:\n---\nBody.\n",
			"skill.field.null"},
		"invalid utf-8": {"echo", minimalSkillMD("echo") + "\xff", "skill.skill-md.encoding"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writeSkillFile(t, root, "skills/"+tc.dir+"/SKILL.md", []byte(tc.skillMD), 0o644)
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Fatal("expected refusal: invalid skills reject the project")
			}
			requireErrorID(t, diags, tc.id)
		})
	}
}

func TestLoadRejectsSkillWithoutSkillMD(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.MkdirAll(filepath.Join(root, "skills", "echo"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "skill.skill-md.missing")
}

func TestLoadRejectsFlatSkillFile(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "skills/flat.md", []byte("body\n"), 0o644)
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "skill.entry.invalid" && d.Path == "skills/flat.md" &&
			strings.Contains(d.Rule, "skills/flat/SKILL.md") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the flat-layout diagnostic pointing at skills/flat/SKILL.md, got %v", diags.All())
	}
}

func TestLoadRejectsSymlinkedSkillEntries(t *testing.T) {
	t.Run("skill directory", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		real := t.TempDir()
		if err := os.WriteFile(filepath.Join(real, "SKILL.md"), []byte(minimalSkillMD("echo")), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(root, "skills", "echo")); err != nil {
			t.Fatal(err)
		}
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Fatal("expected refusal")
		}
		requireErrorID(t, diags, "skill.entry.invalid")
	})
	t.Run("resource", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writeSkillFile(t, root, "skills/echo/SKILL.md", []byte(minimalSkillMD("echo")), 0o644)
		target := filepath.Join(t.TempDir(), "real.md")
		if err := os.WriteFile(target, []byte("real\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "skills", "echo", "link.md")); err != nil {
			t.Fatal(err)
		}
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Fatal("expected refusal")
		}
		requireErrorID(t, diags, "skill.resource.invalid")
	})
}

func TestLoadRejectsTooManySkills(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	for i := 0; i <= MaxSkills; i++ {
		name := fmt.Sprintf("s%d", i)
		writeSkillFile(t, root, "skills/"+name+"/SKILL.md", []byte(minimalSkillMD(name)), 0o644)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "skill.bounds.exceeded")
}

// TestLoadRejectsOversizedSkillMD proves the SKILL.md byte ceiling from file
// metadata: the sparse oversized file is rejected without being read, so no
// spurious content diagnostics follow.
func TestLoadRejectsOversizedSkillMD(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "skills/echo/SKILL.md", []byte(minimalSkillMD("echo")), 0o644)
	if err := os.Truncate(filepath.Join(root, "skills", "echo", "SKILL.md"), MaxSkillMDBytes+1); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "skill.bounds.exceeded")
	for _, d := range diags.All() {
		if strings.HasPrefix(d.ID, "skill.frontmatter") || d.ID == "skill.skill-md.encoding" {
			t.Fatalf("an out-of-bounds SKILL.md must not be read or parsed: %v", d)
		}
	}
}

func TestLoadRejectsOversizedResource(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "skills/echo/SKILL.md", []byte(minimalSkillMD("echo")), 0o644)
	big := filepath.Join(root, "skills", "echo", "big.bin")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, MaxSkillResourceBytes+1); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "skill.bounds.exceeded" && d.Path == "skills/echo/big.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the resource ceiling at skills/echo/big.bin, got %v", diags.All())
	}
}

func TestLoadRejectsSkillFileCountCeiling(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "skills/big/SKILL.md", []byte(minimalSkillMD("big")), 0o644)
	dir := filepath.Join(root, "skills", "big", "r")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxSkillFiles; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "skill.bounds.exceeded" && d.Path == "skills/big" && strings.Contains(d.Rule, "files") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the per-skill file ceiling at skills/big, got %v", diags.All())
	}
}

func TestLoadRejectsSkillSetFileCeiling(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	perSkill := MaxSkillSetFiles/9 + 1 // nine skills cross the set ceiling while each stays inside its own
	for s := 0; s < 9; s++ {
		name := fmt.Sprintf("set%d", s)
		writeSkillFile(t, root, "skills/"+name+"/SKILL.md", []byte(minimalSkillMD(name)), 0o644)
		dir := filepath.Join(root, "skills", name, "r")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < perSkill; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "skill.bounds.exceeded" && d.Path == "skills" && strings.Contains(d.Rule, "files") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the skill-set file ceiling at skills, got %v", diags.All())
	}
}

// TestLoadRejectsSkillByteBudgets proves the aggregate byte math with sparse
// resources at the real 16 MiB per-file ceiling: four of them plus SKILL.md
// cross both the per-skill and set 64 MiB budgets.
func TestLoadRejectsSkillByteBudgets(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "skills/big/SKILL.md", []byte(minimalSkillMD("big")), 0o644)
	for i := 1; i <= 4; i++ {
		res := filepath.Join(root, "skills", "big", fmt.Sprintf("r%d.bin", i))
		if err := os.WriteFile(res, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(res, MaxSkillResourceBytes); err != nil {
			t.Fatal(err)
		}
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	skillBudget, setBudget := false, false
	for _, d := range diags.All() {
		if d.ID != "skill.bounds.exceeded" || !strings.Contains(d.Rule, "bytes") {
			continue
		}
		if d.Path == "skills/big" {
			skillBudget = true
		}
		if d.Path == "skills" {
			setBudget = true
		}
	}
	if !skillBudget || !setBudget {
		t.Fatalf("expected the per-skill and set byte budgets to fail (skill=%v set=%v): %v",
			skillBudget, setBudget, diags.All())
	}
}

func TestSkillFilesJoinFingerprint(t *testing.T) {
	build := func(script []byte, mode os.FileMode) string {
		root := writeAgent(t, "agent", validInstructions)
		writeSkillFile(t, root, "skills/echo/SKILL.md", []byte(minimalSkillMD("echo")), 0o644)
		writeSkillFile(t, root, "skills/echo/scripts/run.sh", script, mode)
		p, diags, err := Load(root)
		if err != nil || p == nil || diags.HasErrors() {
			t.Fatalf("load failed: %v %v", err, diags.All())
		}
		return p.Fingerprint
	}
	base := build([]byte("#!/bin/sh\n"), 0o644)
	if again := build([]byte("#!/bin/sh\n"), 0o644); again != base {
		t.Fatal("identical skill source must fingerprint identically")
	}
	if flipped := build([]byte("#!/bin/sh\n"), 0o755); flipped == base {
		t.Fatal("flipping only a skill file's executable bit must change the fingerprint")
	}
	if changed := build([]byte("#!/bin/sh\necho\n"), 0o644); changed == base {
		t.Fatal("changing a skill file's content must change the fingerprint")
	}
}
