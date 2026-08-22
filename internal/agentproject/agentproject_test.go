package agentproject

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/diagnostics"
)

const validInstructions = `---
description: Reviews pull requests.
---

You review pull requests carefully.
`

func writeAgent(t *testing.T, name, instructions string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if instructions != "" {
		if err := os.WriteFile(filepath.Join(root, "instructions.md"), []byte(instructions), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func errorIDs(diags *diagnostics.List) []string {
	var ids []string
	for _, d := range diags.All() {
		if d.Severity == diagnostics.Error {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

func requireErrorID(t *testing.T, diags *diagnostics.List, id string) {
	t.Helper()
	for _, got := range errorIDs(diags) {
		if got == id {
			return
		}
	}
	t.Fatalf("expected error diagnostic %q, got %v", id, errorIDs(diags))
}

func TestLoadValidProject(t *testing.T) {
	root := writeAgent(t, "my-agent", validInstructions)
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if p.Name != "my-agent" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.Instructions.Description != "Reviews pull requests." {
		t.Fatalf("description = %q", p.Instructions.Description)
	}
	if p.Instructions.Body != "You review pull requests carefully.\n" {
		t.Fatalf("body = %q", p.Instructions.Body)
	}
	if !strings.HasPrefix(p.Fingerprint, "sha256:") {
		t.Fatalf("fingerprint = %q", p.Fingerprint)
	}
}

func TestLoadNormalizesDirectoryName(t *testing.T) {
	root := writeAgent(t, "My_Review Agent", validInstructions)
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if p.Name != "my-review-agent" {
		t.Fatalf("name = %q", p.Name)
	}
}

func TestLoadRefusesUnprovenRoot(t *testing.T) {
	root := writeAgent(t, "empty", "")
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "project.root.unproven")
}

func TestLoadRefusesMissingRoot(t *testing.T) {
	_, diags, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "project.root.missing")
}

func TestLoadRejectsFrontmatterViolations(t *testing.T) {
	cases := map[string]struct {
		content string
		id      string
	}{
		"missing frontmatter": {"just a body\n", "instructions.frontmatter.missing"},
		"unknown field": {"---\ndescription: d\nmodel: opus\n---\n\nbody\n",
			"instructions.frontmatter.unknown-field"},
		"root effort is unknown": {"---\ndescription: d\neffort: high\n---\n\nbody\n",
			"instructions.frontmatter.unknown-field"},
		"missing description": {"---\nfriction-notes: true\n---\n\nbody\n",
			"instructions.description.missing"},
		"empty description": {"---\ndescription:\n---\n\nbody\n",
			"instructions.description.invalid"},
		"bad friction notes": {"---\ndescription: d\nfriction-notes: yes\n---\n\nbody\n",
			"instructions.friction-notes.invalid"},
		"duplicate field": {"---\ndescription: a\ndescription: b\n---\n\nbody\n",
			"instructions.frontmatter.invalid"},
		"yaml machinery": {"---\ndescription: &anchor d\n---\n\nbody\n",
			"instructions.frontmatter.invalid"},
		"empty body": {"---\ndescription: d\n---\n\n  \n", "instructions.body.empty"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeAgent(t, "agent", tc.content)
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Fatal("expected refusal")
			}
			requireErrorID(t, diags, tc.id)
		})
	}
}

func TestLoadRejectsSymlinkedInstructions(t *testing.T) {
	root := writeAgent(t, "agent", "")
	target := filepath.Join(t.TempDir(), "real.md")
	if err := os.WriteFile(target, []byte(validInstructions), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "instructions.md")); err != nil {
		t.Fatal(err)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "instructions.not-regular")
}

func TestLoadRejectsOversizedInstructions(t *testing.T) {
	huge := validInstructions + strings.Repeat("x", MaxInstructionsBytes)
	root := writeAgent(t, "agent", huge)
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "instructions.too-large")
}

func TestLoadRefusesUnimplementedComponents(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.Mkdir(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal: authored behavior must never be silently dropped")
	}
	requireErrorID(t, diags, "component.unsupported")
}

func TestLoadWarnsOnChannelProduct(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.Mkdir(filepath.Join(root, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("channels/ must warn, not fail: %v", diags.All())
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "component.channel-product" && d.Severity == diagnostics.Warning {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected channel-product warning, got %v", diags.All())
	}
}

func TestFingerprintTracksContent(t *testing.T) {
	rootA := writeAgent(t, "agent", validInstructions)
	pA1, _, _ := Load(rootA)
	pA2, _, _ := Load(rootA)
	if pA1.Fingerprint != pA2.Fingerprint {
		t.Fatal("identical source must fingerprint identically")
	}
	rootB := writeAgent(t, "agent", validInstructions+"More.\n")
	pB, _, _ := Load(rootB)
	if pB.Fingerprint == pA1.Fingerprint {
		t.Fatal("changed source must change the fingerprint")
	}
}

func TestFingerprintTracksExecutableBit(t *testing.T) {
	content := []byte("#!/bin/sh\necho run\n")
	plain := fingerprint([]sourceInput{{Path: "skills/run.sh", Content: content, Executable: false}})
	executable := fingerprint([]sourceInput{{Path: "skills/run.sh", Content: content, Executable: true}})
	if plain == executable {
		t.Fatal("flipping only the executable bit must change the fingerprint")
	}
}
