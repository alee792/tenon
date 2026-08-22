package agentproject

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeHarnessFile(t *testing.T, root, rel string, content []byte, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadValidHarnessFilesProject(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeHarnessFile(t, root, "harnesses/claude/.claude/settings.json", []byte(`{"a":1}`), 0o644)
	writeHarnessFile(t, root, "harnesses/claude/.claude/hooks/pre.sh", []byte("#!/bin/sh\n"), 0o755)
	writeHarnessFile(t, root, "harnesses/codex/.codex/rules.md", []byte("# Rules\n"), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.HarnessFiles) != 2 {
		t.Fatalf("harness files = %+v", p.HarnessFiles)
	}
	claudeFiles := p.HarnessFiles["claude"]
	var claudeRels []string
	for _, f := range claudeFiles {
		claudeRels = append(claudeRels, f.RelPath)
	}
	wantClaude := []string{".claude/hooks/pre.sh", ".claude/settings.json"}
	if !slices.Equal(claudeRels, wantClaude) {
		t.Fatalf("claude harness file paths = %v, want sorted %v", claudeRels, wantClaude)
	}
	for _, f := range claudeFiles {
		switch f.RelPath {
		case ".claude/hooks/pre.sh":
			if !f.Executable {
				t.Fatalf("executable intent must survive: %+v", f)
			}
		case ".claude/settings.json":
			if f.Executable || string(f.Content) != `{"a":1}` {
				t.Fatalf("settings.json = %+v", f)
			}
		}
	}
	codexFiles := p.HarnessFiles["codex"]
	if len(codexFiles) != 1 || codexFiles[0].RelPath != ".codex/rules.md" ||
		string(codexFiles[0].Content) != "# Rules\n" {
		t.Fatalf("codex harness files = %+v", codexFiles)
	}
}

func TestLoadHarnessFilesAbsentIsFine(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.HarnessFiles) != 0 {
		t.Fatalf("expected no harness files, got %+v", p.HarnessFiles)
	}
}

func TestLoadRejectsUnknownHarness(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeHarnessFile(t, root, "harnesses/cursor/rules.md", []byte("body\n"), 0o644)
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "harnessfile.harness.unknown")
}

func TestLoadRejectsSymlinkedHarnessesEntries(t *testing.T) {
	t.Run("harnesses directory itself", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		real := t.TempDir()
		if err := os.Symlink(real, filepath.Join(root, "harnesses")); err != nil {
			t.Fatal(err)
		}
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Fatal("expected refusal")
		}
		requireErrorID(t, diags, "harnessfile.entry.invalid")
	})
	t.Run("harness entry", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		real := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "harnesses"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(root, "harnesses", "claude")); err != nil {
			t.Fatal(err)
		}
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Fatal("expected refusal")
		}
		requireErrorID(t, diags, "harnessfile.entry.invalid")
	})
	t.Run("dot directory", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		real := t.TempDir()
		if err := os.WriteFile(filepath.Join(real, "settings.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "harnesses", "claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(root, "harnesses", "claude", ".claude")); err != nil {
			t.Fatal(err)
		}
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Fatal("expected refusal")
		}
		requireErrorID(t, diags, "harnessfile.entry.invalid")
	})
	t.Run("file inside subtree", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writeHarnessFile(t, root, "harnesses/claude/.claude/settings.json", []byte("{}"), 0o644)
		target := filepath.Join(t.TempDir(), "real.json")
		if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "harnesses", "claude", ".claude", "link.json")); err != nil {
			t.Fatal(err)
		}
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Fatal("expected refusal")
		}
		requireErrorID(t, diags, "harnessfile.file.invalid")
	})
}

func TestLoadRejectsNonDirectoryHarnessEntry(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.MkdirAll(filepath.Join(root, "harnesses"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "harnesses", "claude"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "harnessfile.entry.invalid")
}

func TestLoadRejectsExtraEntryUnderHarnessDir(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeHarnessFile(t, root, "harnesses/claude/.claude/settings.json", []byte("{}"), 0o644)
	writeHarnessFile(t, root, "harnesses/claude/README.md", []byte("hi\n"), 0o644)
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "harnessfile.entry.invalid")
}

func TestLoadRejectsReservedDestinations(t *testing.T) {
	cases := map[string]string{
		"claude skills":      "harnesses/claude/.claude/skills/anything",
		"claude agents":      "harnesses/claude/.claude/agents/anything",
		"claude case-folded": "harnesses/claude/.claude/SKILLS/x",
		"codex agents":       "harnesses/codex/.codex/agents/anything",
		"codex config.toml":  "harnesses/codex/.codex/config.toml",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writeHarnessFile(t, root, path, []byte("x"), 0o644)
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Fatal("expected refusal")
			}
			requireErrorID(t, diags, "harnessfile.path.reserved")
		})
	}
}

func TestLoadRejectsTooManyHarnessFiles(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	dir := filepath.Join(root, "harnesses", "claude", ".claude", "r")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= MaxHarnessFiles; i++ {
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
	requireErrorID(t, diags, "harnessfile.bounds.exceeded")
}

// TestLoadRejectsOversizedHarnessFile proves the per-file byte ceiling from
// file metadata: the sparse oversized file is rejected before it is read.
func TestLoadRejectsOversizedHarnessFile(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	big := filepath.Join(root, "harnesses", "claude", ".claude", "big.bin")
	if err := os.MkdirAll(filepath.Dir(big), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, MaxHarnessFileBytes+1); err != nil {
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
		if d.ID == "harnessfile.bounds.exceeded" && d.Path == "harnesses/claude/.claude/big.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the per-file ceiling at harnesses/claude/.claude/big.bin, got %v", diags.All())
	}
}

// TestLoadRejectsHarnessAggregateByteCeiling proves the aggregate byte math
// with sparse files at the real 1 MiB per-file ceiling: nine of them cross
// the 8 MiB aggregate budget for one harness.
func TestLoadRejectsHarnessAggregateByteCeiling(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	dir := filepath.Join(root, "harnesses", "claude", ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		res := filepath.Join(dir, fmt.Sprintf("r%d.bin", i))
		if err := os.WriteFile(res, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(res, MaxHarnessFileBytes); err != nil {
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
		if d.ID == "harnessfile.bounds.exceeded" && d.Path == "harnesses/claude" && strings.Contains(d.Rule, "aggregate") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the harness aggregate byte ceiling at harnesses/claude, got %v", diags.All())
	}
}

func TestHarnessFilesJoinFingerprint(t *testing.T) {
	build := func(content []byte, mode os.FileMode) string {
		root := writeAgent(t, "agent", validInstructions)
		writeHarnessFile(t, root, "harnesses/claude/.claude/settings.json", content, mode)
		p, diags, err := Load(root)
		if err != nil || p == nil || diags.HasErrors() {
			t.Fatalf("load failed: %v %v", err, diags.All())
		}
		return p.Fingerprint
	}
	base := build([]byte("{}"), 0o644)
	if again := build([]byte("{}"), 0o644); again != base {
		t.Fatal("identical harness file source must fingerprint identically")
	}
	if flipped := build([]byte("{}"), 0o755); flipped == base {
		t.Fatal("flipping only a harness file's executable bit must change the fingerprint")
	}
	if changed := build([]byte(`{"a":1}`), 0o644); changed == base {
		t.Fatal("changing a harness file's content must change the fingerprint")
	}
}
