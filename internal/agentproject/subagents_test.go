package agentproject

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func minimalSubagentInstructions() string {
	return "---\ndescription: Reviews code for style.\n---\n\nReview the diff for style issues.\n"
}

func writeSubagentFile(t *testing.T, root, rel string, content []byte, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadValidSubagentProject(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
		[]byte("---\ndescription: Reviews pull requests.\neffort: high\n---\n\nReview carefully.\n"), 0o644)
	writeSubagentFile(t, root, "subagents/greeter/instructions.md",
		[]byte(minimalSubagentInstructions()), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Subagents) != 2 {
		t.Fatalf("subagents = %+v", p.Subagents)
	}
	// sorted by name
	if p.Subagents[0].Name != "greeter" || p.Subagents[1].Name != "reviewer" {
		t.Fatalf("subagents order = %+v", p.Subagents)
	}
	reviewer := p.Subagents[1]
	if reviewer.Description != "Reviews pull requests." || reviewer.Effort != "high" ||
		reviewer.Body != "Review carefully.\n" {
		t.Fatalf("reviewer = %+v", reviewer)
	}
	greeter := p.Subagents[0]
	if greeter.Effort != "" {
		t.Fatalf("greeter effort must be empty when absent: %+v", greeter)
	}
}

// TestLoadSubagentSkipsDotEntries proves the dot-prefixed skip applies
// inside a subagent directory itself: siblings of instructions.md that
// start with "." (editor and OS metadata files) are ignored rather than
// rejected as unsupported children.
func TestLoadSubagentSkipsDotEntries(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSubagentFile(t, root, "subagents/reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
	writeSubagentFile(t, root, "subagents/reviewer/.gitkeep", []byte(""), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("dot-prefixed entries must be skipped, not rejected: %v", diags.All())
	}
	if len(p.Subagents) != 1 {
		t.Fatalf("subagents = %+v", p.Subagents)
	}
}

// TestLoadRejectsDotFileDirectlyUnderSubagents proves the dot-skip is scoped
// to inside a subagent directory: a stray dot file as an immediate
// subagents/ entry is still rejected as not a real subagent directory.
func TestLoadRejectsDotFileDirectlyUnderSubagents(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSubagentFile(t, root, "subagents/reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
	writeSubagentFile(t, root, "subagents/.DS_Store", []byte(""), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "subagent.entry.invalid")
}

func TestLoadRejectsSubagentViolations(t *testing.T) {
	cases := map[string]struct {
		setup func(t *testing.T, root string)
		id    string
	}{
		"uppercase name": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/Reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
		}, "subagent.name.invalid"},
		"leading digit": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/1reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
		}, "subagent.name.invalid"},
		"leading hyphen": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/-reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
		}, "subagent.name.invalid"},
		"overlong name": {func(t *testing.T, root string) {
			name := "a" + strings.Repeat("b", 63)
			writeSubagentFile(t, root, "subagents/"+name+"/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
		}, "subagent.name.invalid"},
		"reserved echo": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/echo/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
		}, "subagent.name.reserved"},
		"reserved record-friction": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/record-friction/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
		}, "subagent.name.reserved"},
		"child tools directory": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
			if err := os.Mkdir(filepath.Join(root, "subagents", "reviewer", "tools"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "subagent.child.unsupported"},
		"nested subagents": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
			if err := os.Mkdir(filepath.Join(root, "subagents", "reviewer", "subagents"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "subagent.child.unsupported"},
		"child skills directory": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
			if err := os.Mkdir(filepath.Join(root, "subagents", "reviewer", "skills"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "subagent.child.unsupported"},
		"dependency file": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
			writeSubagentFile(t, root, "subagents/reviewer/package.json", []byte("{}"), 0o644)
		}, "subagent.child.unsupported"},
		"missing instructions": {func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "subagents", "reviewer"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "subagent.instructions.missing"},
		"missing frontmatter": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md", []byte("just a body\n"), 0o644)
		}, "subagent.frontmatter.missing"},
		"friction-notes unknown on child": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
				[]byte("---\ndescription: d\nfriction-notes: true\n---\n\nbody\n"), 0o644)
		}, "subagent.frontmatter.unknown-field"},
		"unknown field": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
				[]byte("---\ndescription: d\nmodel: opus\n---\n\nbody\n"), 0o644)
		}, "subagent.frontmatter.unknown-field"},
		"missing description": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
				[]byte("---\neffort: low\n---\n\nbody\n"), 0o644)
		}, "subagent.description.missing"},
		"empty description": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
				[]byte("---\ndescription: \"\"\n---\n\nbody\n"), 0o644)
		}, "subagent.description.invalid"},
		"control character in description": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
				[]byte("---\ndescription: \"bad\\tvalue\"\n---\n\nbody\n"), 0o644)
		}, "subagent.description.invalid"},
		"invalid effort value": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
				[]byte("---\ndescription: d\neffort: extreme\n---\n\nbody\n"), 0o644)
		}, "subagent.effort.invalid"},
		"empty body": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
				[]byte("---\ndescription: d\n---\n\n  \n"), 0o644)
		}, "subagent.body.empty"},
		"invalid utf-8": {func(t *testing.T, root string) {
			writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
				append([]byte(minimalSubagentInstructions()), 0xff), 0o644)
		}, "subagent.instructions.encoding"},
		"subagents not a directory": {func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "subagents"), []byte("oops"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "subagent.entry.invalid"},
		"symlinked entry": {func(t *testing.T, root string) {
			real := t.TempDir()
			if err := os.WriteFile(filepath.Join(real, "instructions.md"), []byte(minimalSubagentInstructions()), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "subagents"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(root, "subagents", "reviewer")); err != nil {
				t.Fatal(err)
			}
		}, "subagent.entry.invalid"},
		"symlinked instructions.md": {func(t *testing.T, root string) {
			target := filepath.Join(t.TempDir(), "real.md")
			if err := os.WriteFile(target, []byte(minimalSubagentInstructions()), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "subagents", "reviewer"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, "subagents", "reviewer", "instructions.md")); err != nil {
				t.Fatal(err)
			}
		}, "subagent.instructions.missing"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			tc.setup(t, root)
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Fatal("expected refusal: invalid subagents reject the project")
			}
			requireErrorID(t, diags, tc.id)
		})
	}
}

func TestLoadRejectsTooManySubagents(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	for i := 0; i <= MaxSubagents; i++ {
		name := fmt.Sprintf("sub%d", i)
		writeSubagentFile(t, root, "subagents/"+name+"/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "subagent.bounds.exceeded")
}

// TestLoadRejectsOversizedSubagentInstructions proves the per-file byte
// ceiling from file metadata: the sparse oversized file is rejected without
// being read, so no spurious content diagnostics follow.
func TestLoadRejectsOversizedSubagentInstructions(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSubagentFile(t, root, "subagents/reviewer/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
	if err := os.Truncate(filepath.Join(root, "subagents", "reviewer", "instructions.md"), MaxSubagentInstructionsBytes+1); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "subagent.instructions.too-large")
	for _, d := range diags.All() {
		if strings.HasPrefix(d.ID, "subagent.frontmatter") || d.ID == "subagent.instructions.encoding" {
			t.Fatalf("an out-of-bounds instructions.md must not be read or parsed: %v", d)
		}
	}
}

// TestLoadRejectsSubagentAggregateByteCeiling proves the aggregate math with
// sparse files: by ADR 0013's own numbers, MaxSubagents * MaxSubagentInstructionsBytes
// equals MaxSubagentsAggregateBytes exactly (128 * 128 KiB = 16 MiB), so the
// aggregate ceiling can only be crossed together with the count ceiling —
// one real-sized-but-sparse file short of the per-file cap, one more
// directory than the count allows. This single test proves both ceilings:
// the count ceiling for real (129 directories) and the aggregate byte math
// (each file sparse, well under its own per-file ceiling).
func TestLoadRejectsSubagentAggregateByteCeiling(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	const (
		count   = MaxSubagents + 1 // 129: over the count ceiling
		perFile = 130057           // under the per-file ceiling; 129*perFile > 16 MiB
	)
	if perFile >= MaxSubagentInstructionsBytes {
		t.Fatalf("test setup must stay under the per-file ceiling")
	}
	if int64(count-1)*perFile >= MaxSubagentsAggregateBytes || int64(count)*perFile <= MaxSubagentsAggregateBytes {
		t.Fatalf("test setup must cross the aggregate ceiling only at the last of %d files", count)
	}
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("sub%d", i)
		path := filepath.Join(root, "subagents", name, "instructions.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		content := []byte(minimalSubagentInstructions())
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, perFile); err != nil {
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
	countCeiling, bytesCeiling := false, false
	for _, d := range diags.All() {
		if d.ID != "subagent.bounds.exceeded" {
			continue
		}
		if strings.Contains(d.Rule, "immediate subagents") {
			countCeiling = true
		}
		if strings.Contains(d.Rule, "bytes") {
			bytesCeiling = true
		}
	}
	if !countCeiling || !bytesCeiling {
		t.Fatalf("expected both the count and aggregate byte ceilings (count=%v bytes=%v): %v",
			countCeiling, bytesCeiling, diags.All())
	}
}

func TestSubagentInstructionsJoinFingerprint(t *testing.T) {
	build := func(body string) string {
		root := writeAgent(t, "agent", validInstructions)
		writeSubagentFile(t, root, "subagents/reviewer/instructions.md",
			[]byte("---\ndescription: d\n---\n\n"+body+"\n"), 0o644)
		p, diags, err := Load(root)
		if err != nil || p == nil || diags.HasErrors() {
			t.Fatalf("load failed: %v %v", err, diags.All())
		}
		return p.Fingerprint
	}
	base := build("Review carefully.")
	if again := build("Review carefully."); again != base {
		t.Fatal("identical subagent source must fingerprint identically")
	}
	if changed := build("Review very carefully."); changed == base {
		t.Fatal("changing a subagent instructions.md must change the fingerprint")
	}
}

func TestSubagentsSortedByName(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSubagentFile(t, root, "subagents/zeta/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
	writeSubagentFile(t, root, "subagents/alpha/instructions.md", []byte(minimalSubagentInstructions()), 0o644)
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	var names []string
	for _, s := range p.Subagents {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"alpha", "zeta"}) {
		t.Fatalf("names = %v", names)
	}
}

func TestLoadAllowsAbsentSubagentsDirectory(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Subagents) != 0 {
		t.Fatalf("subagents = %+v", p.Subagents)
	}
}
