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

// testExecutable stands in for the resolved tenon executable; apply only
// requires that it be absolute.
const testExecutable = "/usr/local/bin/tenon"

// fakeDriver generates a configurable file set; apply's ownership discipline
// is harness-neutral.
type fakeDriver struct {
	files []GeneratedFile
	// target records the last target the driver was generated for.
	target *Target
}

func (fakeDriver) Harness() string { return "fake" }

func (d fakeDriver) Generate(_ *agentproject.Project, target Target, _ *diagnostics.List) []GeneratedFile {
	if d.target != nil {
		*d.target = target
	}
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

	result, diags, err := Apply(project(t), ws, testExecutable, driver)
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

	if _, diags, err := Apply(project(t), ws, testExecutable, driver); err != nil || diags.HasErrors() {
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
	if _, diags, err := Apply(project(t), ws, testExecutable, driver); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}
	if err := os.Chmod(filepath.Join(ws, "CLAUDE.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, diags, err := Apply(project(t), ws, testExecutable, driver)
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
	if _, diags, err := Apply(p, ws, testExecutable, first); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}

	second := fakeDriver{files: []GeneratedFile{{Path: "tools/run.sh", Content: content, Executable: true}}}
	if _, diags, err := Apply(p, ws, testExecutable, second); err != nil || diags.HasErrors() {
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
	if _, diags, err := Apply(p, ws, testExecutable, first); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}

	second := fakeDriver{files: []GeneratedFile{{Path: ".claude/keep.md", Content: []byte("keep\n")}}}
	if _, diags, err := Apply(p, ws, testExecutable, second); err != nil || diags.HasErrors() {
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

	result, diags, err := Apply(project(t), ws, testExecutable, driver)
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
	if _, diags, err := Apply(project(t), ws, testExecutable, driver); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, diags, err := Apply(project(t), ws, testExecutable, driver)
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
	if _, diags, err := Apply(p, ws, testExecutable, driver); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}
	firstRecord, err := os.ReadFile(RecordPath(ws, "fake"))
	if err != nil {
		t.Fatal(err)
	}

	if _, diags, err := Apply(p, ws, testExecutable, driver); err != nil || diags.HasErrors() {
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
	if _, diags, err := Apply(p, ws, testExecutable, first); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}

	second := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("two\n")}}}
	result, diags, err := Apply(p, ws, testExecutable, second)
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
	result, diags, err := Apply(project(t), filepath.Join(t.TempDir(), "absent"), testExecutable, fakeDriver{})
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
	result, diags, err := Apply(project(t), ws, testExecutable, fakeDriver{})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected refusal")
	}
	requireErrorID(t, diags, "apply.record.invalid")
}

// TestApplyPassesTheResolvedTargetToTheDriver proves the driver receives the
// absolute workspace and executable that generated managed-server
// configuration must embed.
func TestApplyPassesTheResolvedTargetToTheDriver(t *testing.T) {
	ws := t.TempDir()
	var seen Target
	driver := fakeDriver{
		files:  []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}},
		target: &seen,
	}
	if _, diags, err := Apply(project(t), ws, testExecutable, driver); err != nil || diags.HasErrors() {
		t.Fatalf("apply failed: %v %v", err, diags.All())
	}
	if seen.Executable != testExecutable || !filepath.IsAbs(seen.Workspace) {
		t.Fatalf("target = %+v, want the resolved workspace and executable", seen)
	}
	if seen.Workspace != ws {
		t.Fatalf("target workspace = %q, want %q", seen.Workspace, ws)
	}
}

// TestApplyRefusesAnUnresolvedExecutable proves an empty or relative
// executable is an environment failure rather than a diagnostic: a driver
// must never be asked to generate configuration it cannot launch.
func TestApplyRefusesAnUnresolvedExecutable(t *testing.T) {
	ws := t.TempDir()
	var seen Target
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("x\n")}}, target: &seen}
	for _, executable := range []string{"", "bin/tenon"} {
		result, diags, err := Apply(project(t), ws, executable, driver)
		if err == nil {
			t.Fatalf("executable %q must fail as an environment error", executable)
		}
		if result != nil || len(diags.All()) != 0 {
			t.Fatalf("executable %q produced %v and %v", executable, result, diags.All())
		}
	}
	if seen.Workspace != "" {
		t.Fatal("the driver must not run without a resolved executable")
	}
	entries, err := os.ReadDir(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused apply must write nothing; found %v", entries)
	}
}

// TestVerifyAcceptsFreshlyAppliedState proves a workspace still carrying
// exactly what apply wrote verifies clean.
func TestVerifyAcceptsFreshlyAppliedState(t *testing.T) {
	ws := t.TempDir()
	p := project(t)
	driver := fakeDriver{files: []GeneratedFile{
		{Path: "CLAUDE.md", Content: []byte("generated\n")},
		{Path: ".mcp.json", Content: []byte("{}\n")},
	}}
	if _, diags, err := Apply(p, ws, testExecutable, driver); err != nil || diags.HasErrors() {
		t.Fatalf("apply failed: %v %v", err, diags.All())
	}
	if err := Verify(p, ws, "fake"); err != nil {
		t.Fatalf("freshly applied state must verify: %v", err)
	}
}

// TestVerifyFailsClosedOnDrift proves each way applied state goes stale is
// refused by name: no record at all, a record for another harness, a source
// fingerprint that moved, a removed generated file, and an edited one.
func TestVerifyFailsClosedOnDrift(t *testing.T) {
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}

	applied := func(t *testing.T) (*agentproject.Project, string) {
		t.Helper()
		ws := t.TempDir()
		p := project(t)
		if _, diags, err := Apply(p, ws, testExecutable, driver); err != nil || diags.HasErrors() {
			t.Fatalf("apply failed: %v %v", err, diags.All())
		}
		return p, ws
	}

	t.Run("missing record", func(t *testing.T) {
		p, ws := applied(t)
		if err := os.Remove(RecordPath(ws, "fake")); err != nil {
			t.Fatal(err)
		}
		err := Verify(p, ws, "fake")
		if err == nil || !strings.Contains(err.Error(), "no fake apply record") ||
			!strings.Contains(err.Error(), "tenon apply") {
			t.Fatalf("missing record error = %v", err)
		}
	})

	t.Run("other harness", func(t *testing.T) {
		p, ws := applied(t)
		if err := Verify(p, ws, "claude"); err == nil || !strings.Contains(err.Error(), "no claude apply record") {
			t.Fatalf("other-harness error = %v", err)
		}
		// A record copied under another harness's name is refused by its own
		// recorded harness, not by the path it was found at.
		raw, err := os.ReadFile(RecordPath(ws, "fake"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(RecordPath(ws, "claude"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Verify(p, ws, "claude"); err == nil || !strings.Contains(err.Error(), `"fake"`) {
			t.Fatalf("mismatched-harness record error = %v", err)
		}
	})

	t.Run("fingerprint drift", func(t *testing.T) {
		p, ws := applied(t)
		drifted := *p
		drifted.Fingerprint = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
		err := Verify(&drifted, ws, "fake")
		if err == nil || !strings.Contains(err.Error(), p.Fingerprint) ||
			!strings.Contains(err.Error(), drifted.Fingerprint) || !strings.Contains(err.Error(), "tenon apply") {
			t.Fatalf("fingerprint drift error = %v", err)
		}
	})

	t.Run("missing generated file", func(t *testing.T) {
		p, ws := applied(t)
		if err := os.Remove(filepath.Join(ws, "CLAUDE.md")); err != nil {
			t.Fatal(err)
		}
		err := Verify(p, ws, "fake")
		if err == nil || !strings.Contains(err.Error(), "CLAUDE.md") || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("missing file error = %v", err)
		}
	})

	t.Run("modified generated file", func(t *testing.T) {
		p, ws := applied(t)
		if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := Verify(p, ws, "fake")
		if err == nil || !strings.Contains(err.Error(), "CLAUDE.md") || !strings.Contains(err.Error(), "modified") {
			t.Fatalf("modified file error = %v", err)
		}
	})

	t.Run("mode-only change", func(t *testing.T) {
		p, ws := applied(t)
		if err := os.Chmod(filepath.Join(ws, "CLAUDE.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := Verify(p, ws, "fake"); err == nil || !strings.Contains(err.Error(), "CLAUDE.md") {
			t.Fatalf("mode-only change error = %v", err)
		}
	})

	t.Run("corrupt record", func(t *testing.T) {
		p, ws := applied(t)
		if err := os.WriteFile(RecordPath(ws, "fake"), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Verify(p, ws, "fake"); err == nil || !strings.Contains(err.Error(), "tenon apply") {
			t.Fatalf("corrupt record error = %v", err)
		}
	})
}
