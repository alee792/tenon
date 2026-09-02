package apply

import (
	"encoding/json"
	"os"
	"os/exec"
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

// runGit runs one git command in dir, failing the test on any error. It is
// the test fixture's own use of git as a subprocess, distinct from the
// production CleanHeadCommit helper it is exercising.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

// initCleanGitRepo creates a git repository at dir with one committed file
// and returns its HEAD commit SHA.
func initCleanGitRepo(t *testing.T, dir string) string {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Tenon Test")
	if err := os.WriteFile(filepath.Join(dir, "instructions.md"), []byte("agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "instructions.md")
	runGit(t, dir, "commit", "-m", "initial")
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
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

// TestApplyDiscardLocalOverwritesModifiedOwnedFile proves --discard-local's
// production seam (Target.DiscardLocal): a tenon-owned file modified since
// the previous apply is overwritten instead of refused when DiscardLocal is
// set, and the record reflects the freshly generated content.
func TestApplyDiscardLocalOverwritesModifiedOwnedFile(t *testing.T) {
	ws := t.TempDir()
	p := project(t)
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}
	if _, diags, err := ApplyWithTarget(p, Target{Workspace: ws, Executable: testExecutable}, driver); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, diags, err := ApplyWithTarget(p, Target{Workspace: ws, Executable: testExecutable, DiscardLocal: true}, driver)
	if err != nil || diags.HasErrors() {
		t.Fatalf("discard-local apply failed: %v %v", err, diags.All())
	}
	if result == nil || len(result.Written) != 1 || result.Written[0] != "CLAUDE.md" {
		t.Fatalf("result = %+v, want CLAUDE.md written", result)
	}
	got, err := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if err != nil || string(got) != "generated\n" {
		t.Fatalf("discard-local must overwrite the local edit: got %q, err %v", got, err)
	}
	owned, ok := readTestRecord(t, ws).Files["CLAUDE.md"]
	if !ok || !strings.HasPrefix(owned.Hash, "sha256:") {
		t.Fatalf("record must reflect the freshly written content: %+v, %v", owned, ok)
	}
}

// TestApplyDiscardLocalStillRefusesHandAuthoredFile proves --discard-local
// never widens apply.conflict.unowned: a file that was never recorded as
// tenon-owned is refused exactly as without the flag, and never touched.
func TestApplyDiscardLocalStillRefusesHandAuthoredFile(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}

	result, diags, err := ApplyWithTarget(project(t), Target{Workspace: ws, Executable: testExecutable, DiscardLocal: true}, driver)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("expected refusal despite --discard-local")
	}
	requireErrorID(t, diags, "apply.conflict.unowned")
	got, _ := os.ReadFile(filepath.Join(ws, "CLAUDE.md"))
	if string(got) != "mine\n" {
		t.Fatalf("hand-authored file was mutated: %q", got)
	}
}

// TestApplyDiscardLocalOverwritesModifiedStaleFile proves --discard-local
// also lets a modified-but-now-stale owned file be removed, instead of
// refusing the whole apply the way it would without the flag.
func TestApplyDiscardLocalOverwritesModifiedStaleFile(t *testing.T) {
	ws := t.TempDir()
	p := project(t)
	first := fakeDriver{files: []GeneratedFile{{Path: "old.md", Content: []byte("generated\n")}}}
	if _, diags, err := ApplyWithTarget(p, Target{Workspace: ws, Executable: testExecutable}, first); err != nil || diags.HasErrors() {
		t.Fatalf("first apply failed: %v %v", err, diags.All())
	}
	if err := os.WriteFile(filepath.Join(ws, "old.md"), []byte("edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	second := fakeDriver{files: []GeneratedFile{{Path: "new.md", Content: []byte("generated\n")}}}
	result, diags, err := ApplyWithTarget(p, Target{Workspace: ws, Executable: testExecutable, DiscardLocal: true}, second)
	if err != nil || diags.HasErrors() {
		t.Fatalf("discard-local apply failed: %v %v", err, diags.All())
	}
	if len(result.Removed) != 1 || result.Removed[0] != "old.md" {
		t.Fatalf("result.Removed = %v, want [old.md]", result.Removed)
	}
	if _, err := os.Stat(filepath.Join(ws, "old.md")); !os.IsNotExist(err) {
		t.Fatal("the modified stale file must be removed under --discard-local")
	}
}

// TestApplyDiscardLocalNeverForwardedToDriver pins the design choice
// documented on Target.DiscardLocal: it is a caller policy decision about
// conflict handling, not generated content, so — like ManifestIdentity — it
// must never reach a driver's Generate. A driver that somehow branched on it
// would be reading a signal apply never intended to leak.
func TestApplyDiscardLocalNeverForwardedToDriver(t *testing.T) {
	ws := t.TempDir()
	var seen Target
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}, target: &seen}
	if _, diags, err := ApplyWithTarget(project(t), Target{Workspace: ws, Executable: testExecutable, DiscardLocal: true}, driver); err != nil || diags.HasErrors() {
		t.Fatalf("apply failed: %v %v", err, diags.All())
	}
	if seen.DiscardLocal {
		t.Fatal("DiscardLocal must never be forwarded into the driver's Generate target")
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

// TestApplyRecordsGitCommitForCleanSource proves a source directory that is a
// clean git working tree gets its HEAD commit SHA recorded.
func TestApplyRecordsGitCommitForCleanSource(t *testing.T) {
	source := t.TempDir()
	want := initCleanGitRepo(t, source)

	ws := t.TempDir()
	p := project(t)
	p.Root = source
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}
	if _, diags, err := Apply(p, ws, testExecutable, driver); err != nil || diags.HasErrors() {
		t.Fatalf("apply failed: %v %v", err, diags.All())
	}
	got := readTestRecord(t, ws).GitCommit
	if got != want {
		t.Fatalf("git commit = %q, want %q", got, want)
	}
}

// TestApplyRecordsGitCommitDespiteUnrelatedDirtySibling proves the recorded
// commit reflects only the agent's own subtree: an uncommitted edit
// elsewhere in a larger repository must not blank GitCommit for an agent
// directory that is itself fully committed and clean.
func TestApplyRecordsGitCommitDespiteUnrelatedDirtySibling(t *testing.T) {
	// One repository rooted at repo, with the agent living in a subtree
	// (agents/foo) alongside an unrelated sibling (other-service): the
	// scenario CleanHeadCommit's pathspec exists to handle correctly.
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Tenon Test")

	agentDir := filepath.Join(repo, "agents", "foo")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "instructions.md"), []byte("agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherDir := filepath.Join(repo, "other-service")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "bar.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "agents/foo/instructions.md", "other-service/bar.go")
	runGit(t, repo, "commit", "-m", "initial")
	want := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	// An unrelated, uncommitted edit elsewhere in the same repository —
	// agents/foo itself remains fully committed and clean.
	if err := os.WriteFile(filepath.Join(otherDir, "bar.go"), []byte("package other\n\nvar x int\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	p := project(t)
	p.Root = agentDir
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}
	if _, diags, err := Apply(p, ws, testExecutable, driver); err != nil || diags.HasErrors() {
		t.Fatalf("apply failed: %v %v", err, diags.All())
	}
	got := readTestRecord(t, ws).GitCommit
	if got != want {
		t.Fatalf("git commit = %q, want %q despite an unrelated dirty sibling directory", got, want)
	}
}

// TestApplyOmitsGitCommitForDirtySource proves an uncommitted change in the
// source directory suppresses the recorded commit rather than failing apply.
func TestApplyOmitsGitCommitForDirtySource(t *testing.T) {
	source := t.TempDir()
	initCleanGitRepo(t, source)
	if err := os.WriteFile(filepath.Join(source, "instructions.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	p := project(t)
	p.Root = source
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}
	if _, diags, err := Apply(p, ws, testExecutable, driver); err != nil || diags.HasErrors() {
		t.Fatalf("apply failed: %v %v", err, diags.All())
	}
	if got := readTestRecord(t, ws).GitCommit; got != "" {
		t.Fatalf("git commit = %q, want empty for a dirty source tree", got)
	}
}

// TestApplyOmitsGitCommitForNonGitSource proves a source directory outside
// any git repository leaves the field empty rather than erroring apply.
func TestApplyOmitsGitCommitForNonGitSource(t *testing.T) {
	ws := t.TempDir()
	p := project(t) // p.Root is a plain t.TempDir(), never a git repository
	driver := fakeDriver{files: []GeneratedFile{{Path: "CLAUDE.md", Content: []byte("generated\n")}}}
	if _, diags, err := Apply(p, ws, testExecutable, driver); err != nil || diags.HasErrors() {
		t.Fatalf("apply failed: %v %v", err, diags.All())
	}
	if got := readTestRecord(t, ws).GitCommit; got != "" {
		t.Fatalf("git commit = %q, want empty for a non-git source", got)
	}
}

// TestReadRecordAcceptsPreGitCommitRecords proves an apply record written
// before GitCommit existed still decodes, with the field simply empty: a
// missing JSON field decodes to its zero value, so this required no schema
// bump.
func TestReadRecordAcceptsPreGitCommitRecords(t *testing.T) {
	ws := t.TempDir()
	recordPath := RecordPath(ws, "fake")
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "schema": 1,
  "agent": "agent",
  "source": "/some/source",
  "harness": "fake",
  "fingerprint": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
  "files": {"CLAUDE.md": {"hash": "sha256:abc", "executable": false}}
}
`)
	if err := os.WriteFile(recordPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := readRecord(recordPath)
	if err != nil {
		t.Fatalf("readRecord failed on a pre-GitCommit record: %v", err)
	}
	if record.GitCommit != "" {
		t.Fatalf("git commit = %q, want empty for a record with no git_commit field", record.GitCommit)
	}
	if record.Schema != 1 || record.Agent != "agent" {
		t.Fatalf("record = %+v, want the legacy fields to still decode", record)
	}
}

// TestStrictlyInsideRejectsWhatIsNotBelowRoot covers strictlyInside's false
// branch directly. PruneEmptyParents only ever hands it paths CheckContainment
// has already vouched for, so nothing reaches the guard through that route
// today — it is defense in depth, and defense in depth that is never executed
// is a claim rather than a guarantee.
func TestStrictlyInsideRejectsWhatIsNotBelowRoot(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name      string
		candidate string
		want      bool
	}{
		{"a directory below root", filepath.Join(root, ".claude", "skills"), true},
		{"root itself", root, false},
		{"a sibling of root", filepath.Join(filepath.Dir(root), "elsewhere"), false},
		{"an unrelated absolute path", string(filepath.Separator) + "etc", false},
		{"a parent of root", filepath.Dir(root), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strictlyInside(root, tc.candidate); got != tc.want {
				t.Fatalf("strictlyInside(%q, %q) = %v, want %v", root, tc.candidate, got, tc.want)
			}
		})
	}
}
