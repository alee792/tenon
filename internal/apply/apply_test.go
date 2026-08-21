package apply

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/diagnostics"
)

// fakeDriver generates a configurable file set; apply's ownership discipline
// is harness-neutral.
type fakeDriver struct {
	files []GeneratedFile
}

func (fakeDriver) Harness() string { return "fake" }

func (d fakeDriver) Generate(*agentproject.Project, *diagnostics.List) []GeneratedFile {
	return d.files
}

func project(t *testing.T) *agentproject.Project {
	t.Helper()
	return &agentproject.Project{
		Root:        t.TempDir(),
		Name:        "agent",
		Fingerprint: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}
}

func requireErrorID(t *testing.T, diags *diagnostics.List, id string) {
	t.Helper()
	for _, d := range diags.All() {
		if d.ID == id && d.Severity == diagnostics.Error {
			return
		}
	}
	t.Fatalf("expected error diagnostic %q, got %v", id, diags.All())
}

func TestApplyWritesFilesAndOwnerOnlyRecord(t *testing.T) {
	ws := t.TempDir()
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}

	result, diags, err := Apply(project(t), ws, driver)
	if err != nil || diags.HasErrors() {
		t.Fatalf("apply failed: %v %v", err, diags.All())
	}
	got, err := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if err != nil || string(got) != "generated\n" {
		t.Fatalf("generated file = %q, %v", got, err)
	}
	if len(result.Written) != 1 || result.Written[0] != "CLAUDE.md" {
		t.Fatalf("written = %v", result.Written)
	}
	info, err := os.Stat(RecordPath(ws, "fake"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("apply record mode = %v, want owner-only 0600", info.Mode().Perm())
	}
	record := readTestRecord(t, ws)
	owned, ok := record.Files["CLAUDE.md"]
	if !ok || !strings.HasPrefix(owned.Hash, "sha256:") || owned.Executable {
		t.Fatalf("recorded owned state = %+v, %v", owned, ok)
	}
}

func readTestRecord(t *testing.T, ws string) *Record {
	t.Helper()
	raw, err := os.ReadFile(RecordPath(ws, "fake"))
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestApplyWritesExecutableFileWithModeAndRecord(t *testing.T) {
	ws := t.TempDir()
	driver := fakeDriver{files: []GeneratedFile{
		{Path: "tools/run.sh", Content: []byte("#!/bin/sh\n"), Executable: true},
	}}

	if _, diags, err := Apply(project(t), ws, driver); err != nil || diags.HasErrors() {
		t.Fatalf("apply failed: %v %v", err, diags.All())
	}
	info, err := os.Stat(filepath.Join(ws, "tools/run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("executable file mode = %v, want 0755", info.Mode().Perm())
	}
	owned, ok := readTestRecord(t, ws).Files["tools/run.sh"]
	if !ok || !owned.Executable {
		t.Fatalf("recorded owned state = %+v, %v, want executable intent", owned, ok)
	}
}

func TestApplyRefusesModeOnlyModifiedOwnedFile(t *testing.T) {
	ws := t.TempDir()
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}
	if _, diags, err := Apply(project(t), ws, driver); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}
	if err := os.Chmod(filepath.Join(ws, "CLAUDE.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, diags, err := Apply(project(t), ws, driver)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "apply.conflict.modified")
}

func TestApplyExecutableIntentChangeUpdatesMode(t *testing.T) {
	ws := t.TempDir()
	p := project(t)
	content := []byte("#!/bin/sh\n")
	first := fakeDriver{files: []GeneratedFile{{Path: "tools/run.sh", Content: content}}}
	if _, diags, err := Apply(p, ws, first); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}

	second := fakeDriver{files: []GeneratedFile{{Path: "tools/run.sh", Content: content, Executable: true}}}
	if _, diags, err := Apply(p, ws, second); err != nil || diags.HasErrors() {
		t.Fatalf("intent-only reapply failed: %v %v", err, diags.All())
	}
	info, err := os.Stat(filepath.Join(ws, "tools/run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("file mode = %v, want 0755 after intent change", info.Mode().Perm())
	}
	owned := readTestRecord(t, ws).Files["tools/run.sh"]
	if !owned.Executable {
		t.Fatalf("recorded owned state = %+v, want executable intent", owned)
	}
}

func TestApplyStaleRemovalPrunesEmptyDirectories(t *testing.T) {
	ws := t.TempDir()
	p := project(t)
	first := fakeDriver{files: []GeneratedFile{
		{Path: ".claude/agents/deep/helper.md", Content: []byte("helper\n")},
		{Path: ".claude/keep.md", Content: []byte("keep\n")},
	}}
	if _, diags, err := Apply(p, ws, first); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}

	second := fakeDriver{files: []GeneratedFile{{Path: ".claude/keep.md", Content: []byte("keep\n")}}}
	if _, diags, err := Apply(p, ws, second); err != nil || diags.HasErrors() {
		t.Fatalf("second apply failed: %v %v", err, diags.All())
	}
	if _, err := os.Lstat(filepath.Join(ws, ".claude/agents")); !os.IsNotExist(err) {
		t.Fatal("emptied ancestor directories must be removed")
	}
	if _, err := os.Lstat(filepath.Join(ws, ".claude/keep.md")); err != nil {
		t.Fatalf("non-empty ancestor must survive: %v", err)
	}
	if _, err := os.Lstat(ws); err != nil {
		t.Fatalf("workspace root must survive: %v", err)
	}
}

func TestApplyRefusesHandAuthoredFile(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}

	result, diags, err := Apply(project(t), ws, driver)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "apply.conflict.unowned")
	got, _ := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if string(got) != "mine\n" {
		t.Fatalf("hand-authored file was mutated: %q", got)
	}
}

func TestApplyRefusesModifiedOwnedFile(t *testing.T) {
	ws := t.TempDir()
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}
	if _, diags, err := Apply(project(t), ws, driver); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, diags, err := Apply(project(t), ws, driver)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "apply.conflict.modified")
	got, _ := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if string(got) != "edited\n" {
		t.Fatalf("edited file was discarded: %q", got)
	}
}

func TestReapplyIdenticalSourceIsDeterministic(t *testing.T) {
	ws := t.TempDir()
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}
	p := project(t)
	if _, diags, err := Apply(p, ws, driver); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}
	firstRecord, err := os.ReadFile(RecordPath(ws, "fake"))
	if err != nil {
		t.Fatal(err)
	}

	if _, diags, err := Apply(p, ws, driver); err != nil || diags.HasErrors() {
		t.Fatalf("reapply failed: %v %v", err, diags.All())
	}
	secondRecord, err := os.ReadFile(RecordPath(ws, "fake"))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstRecord) != string(secondRecord) {
		t.Fatal("identical reapply must produce a byte-identical record")
	}
	got, _ := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if string(got) != "generated\n" {
		t.Fatalf("generated file = %q", got)
	}
}

func TestApplyUpdatesOwnedFileAndRemovesStale(t *testing.T) {
	ws := t.TempDir()
	p := project(t)
	first := fakeDriver{files: []GeneratedFile{
		{Path: "CLAUDE.md", Content: []byte("one\n")},
		{Path: ".claude/agents/helper.md", Content: []byte("helper\n")},
	}}
	if _, diags, err := Apply(p, ws, first); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}

	second := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("two\n")}}}
	result, diags, err := Apply(p, ws, second)
	if err != nil || diags.HasErrors() {
		t.Fatalf("second apply failed: %v %v", err, diags.All())
	}
	got, _ := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if string(got) != "two\n" {
		t.Fatalf("owned file not updated: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(ws, ".claude/agents/helper.md")); !os.IsNotExist(err) {
		t.Fatal("stale owned file must be removed")
	}
	if len(result.Removed) != 1 || result.Removed[0] != ".claude/agents/helper.md" {
		t.Fatalf("removed = %v", result.Removed)
	}
}

func TestApplyRefusesMissingWorkspace(t *testing.T) {
	result, diags, err := Apply(project(t), filepath.Join(t.TempDir(), "absent"), fakeDriver{})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "apply.workspace.missing")
}

func TestApplyFailsClosedOnCorruptRecord(t *testing.T) {
	ws := t.TempDir()
	recordPath := RecordPath(ws, "fake")
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, diags, err := Apply(project(t), ws, fakeDriver{})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "apply.record.invalid")
}
