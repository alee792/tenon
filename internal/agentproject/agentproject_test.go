package agentproject

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// TestLoadWithManifestProvesInstructionsFreeRoot proves acceptance item 12's
// root-proof clause: an instructions-free directory whose supplied manifest
// fingerprint matches loads as a valid root with an empty always-on surface,
// while the no-manifest path stays unchanged.
func TestLoadWithManifestProvesInstructionsFreeRoot(t *testing.T) {
	root := writeAgent(t, "empty", "")

	// The freshly computed fingerprint is reported by a deliberate mismatch.
	_, diags, err := LoadWithManifest(root, "sha256:"+strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "project.manifest.fingerprint-mismatch")
	computed := lastFingerprint(t, diags)

	p, diags, err := LoadWithManifest(root, computed)
	if err != nil {
		t.Fatal(err)
	}
	if diags.HasErrors() {
		t.Fatalf("a matching manifest must prove the root: %v", diags.All())
	}
	if p == nil {
		t.Fatal("expected a proven project")
	}
	if p.Instructions != nil {
		t.Fatal("an instructions-free root must carry no instructions (empty always-on surface)")
	}
	if p.Fingerprint != computed {
		t.Fatalf("fingerprint = %q, want %q", p.Fingerprint, computed)
	}

	// The no-manifest path is unchanged: still refused as unproven.
	if _, diags, _ := LoadWithManifest(root, ""); !hasErrorID(diags, "project.root.unproven") {
		t.Fatalf("no-manifest instructions-free path must stay unproven: %v", diags.All())
	}
}

// lastFingerprint extracts the directory's computed fingerprint from a
// fingerprint-mismatch diagnostic's rule text.
func lastFingerprint(t *testing.T, diags *diagnostics.List) string {
	t.Helper()
	for _, d := range diags.All() {
		if d.ID != "project.manifest.fingerprint-mismatch" {
			continue
		}
		i := strings.LastIndex(d.Rule, "sha256:")
		if i < 0 {
			t.Fatalf("mismatch rule carries no fingerprint: %q", d.Rule)
		}
		return d.Rule[i:]
	}
	t.Fatalf("no fingerprint-mismatch diagnostic: %v", diags.All())
	return ""
}

func hasErrorID(diags *diagnostics.List, id string) bool {
	for _, got := range errorIDs(diags) {
		if got == id {
			return true
		}
	}
	return false
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

// TestLoadAllowsEmptySchedules proves schedules/ is now an implemented,
// optional component (ADR 0008): an empty schedules/ directory produces no
// diagnostics and starts no clock. Every recognized component is implemented,
// so there is no longer an unimplemented component to refuse.
func TestLoadAllowsEmptySchedules(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.Mkdir(filepath.Join(root, "schedules"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("an empty schedules/ must be valid: p=%v diags=%v", p, diags.All())
	}
	if len(p.Schedules) != 0 {
		t.Fatalf("expected no schedules, got %d", len(p.Schedules))
	}
}

// TestLoadAllowsEmptyConnections proves mcp/ is now an implemented, optional
// component: an empty mcp/ directory produces no diagnostics.
func TestLoadAllowsEmptyConnections(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.Mkdir(filepath.Join(root, "mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if diags.HasErrors() {
		t.Fatalf("empty mcp/ must be valid: %v", diags.All())
	}
	if p == nil || len(p.Connections) != 0 {
		t.Fatalf("expected zero connections, got %v", p)
	}
}

// TestLoadAllowsEmptyPlugins proves plugins/ is now an implemented,
// optional component: an empty plugins/ directory produces no diagnostics.
func TestLoadAllowsEmptyPlugins(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.Mkdir(filepath.Join(root, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("an empty plugins/ must be normal: %v", diags.All())
	}
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
	_, plain := computeFingerprint([]sourceInput{{Path: "skills/run.sh", Content: content, Executable: false}})
	_, executable := computeFingerprint([]sourceInput{{Path: "skills/run.sh", Content: content, Executable: true}})
	if plain == executable {
		t.Fatal("flipping only the executable bit must change the fingerprint")
	}
}

// TestFingerprintEntriesMatchRollup proves the per-file list tenon
// fingerprint show renders is exactly what feeds the rolled-up fingerprint:
// sorted by path, each entry's own content hash, and executable intent
// preserved, with instructions.md always present as an entry.
func TestFingerprintEntriesMatchRollup(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeHarnessFile(t, root, "harnesses/claude/.claude/hooks/pre.sh", []byte("#!/bin/sh\n"), 0o755)
	writeHarnessFile(t, root, "harnesses/claude/.claude/settings.json", []byte(`{"a":1}`), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}

	var paths []string
	for _, e := range p.FingerprintEntries {
		paths = append(paths, e.Path)
	}
	if !slices.IsSorted(paths) {
		t.Fatalf("fingerprint entries must be sorted by path: %v", paths)
	}

	byPath := make(map[string]FingerprintEntry, len(p.FingerprintEntries))
	for _, e := range p.FingerprintEntries {
		byPath[e.Path] = e
	}

	instr, ok := byPath["instructions.md"]
	if !ok {
		t.Fatalf("instructions.md must be a fingerprint entry: %v", paths)
	}
	if instr.Executable {
		t.Fatalf("instructions.md must never be executable: %+v", instr)
	}
	wantInstrHash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(validInstructions)))
	if instr.Hash != wantInstrHash {
		t.Fatalf("instructions.md hash = %s, want %s", instr.Hash, wantInstrHash)
	}

	hook, ok := byPath["harnesses/claude/.claude/hooks/pre.sh"]
	if !ok || !hook.Executable {
		t.Fatalf("executable harness file must be a fingerprint entry carrying its bit: %+v", hook)
	}
	wantHookHash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("#!/bin/sh\n")))
	if hook.Hash != wantHookHash {
		t.Fatalf("hook hash = %s, want %s", hook.Hash, wantHookHash)
	}

	settings, ok := byPath["harnesses/claude/.claude/settings.json"]
	if !ok || settings.Executable {
		t.Fatalf("non-executable harness file must not carry the bit: %+v", settings)
	}
}
