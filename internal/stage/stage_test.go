package stage

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
	"github.com/alee792/tenon/internal/diagnostics"
)

const validInstructions = `---
description: Reviews pull requests.
---

You review pull requests carefully.
`

// writeAgent creates a minimal valid agent directory named name.
func writeAgent(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "instructions.md"), []byte(validInstructions), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// fakeExecutable writes a stand-in tenon binary and returns its absolute path.
// Staging copies its bytes; it need not be runnable.
func fakeExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tenon")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// treeHashes returns a map of every regular file's tree-relative slash path to
// its content hash.
func treeHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		h, herr := hashFile(path)
		if herr != nil {
			return herr
		}
		out[filepath.ToSlash(rel)] = h
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStageDeterministic(t *testing.T) {
	agent := writeAgent(t, "my-agent")
	exe := fakeExecutable(t)

	a := stageWith(t, agent, "claude", exe)
	b := stageWith(t, agent, "claude", exe)

	ha, hb := treeHashes(t, a), treeHashes(t, b)
	if len(ha) != len(hb) {
		t.Fatalf("file counts differ: %d vs %d", len(ha), len(hb))
	}
	for path, h := range ha {
		if hb[path] != h {
			t.Fatalf("file %s differs between two staged trees", path)
		}
	}

	ma, err := os.ReadFile(filepath.Join(a, filepath.FromSlash(strings.TrimPrefix(finalArtifact, "/"))))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := os.ReadFile(filepath.Join(b, filepath.FromSlash(strings.TrimPrefix(finalArtifact, "/"))))
	if err != nil {
		t.Fatal(err)
	}
	if string(ma) != string(mb) {
		t.Fatal("artifact.json is not deterministic across two identical stagings")
	}
}

// stageWith stages into a named subdirectory of a fresh temp parent.
func stageWith(t *testing.T, agent, harness, exe string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "staged")
	res, diags, err := Stage(context.Background(), Options{
		AgentDir: agent, Harness: harness, Output: out, Executable: exe, Driver: pickDriver(harness),
	})
	if err != nil {
		t.Fatalf("stage error: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("stage diagnostics: %v", diags.All())
	}
	if res == nil {
		t.Fatal("nil result")
	}
	return out
}

func pickDriver(harness string) apply.Driver {
	if harness == "codex" {
		return codex.Driver{}
	}
	return claude.Driver{}
}

func TestStageDoesNotMutateSource(t *testing.T) {
	agent := writeAgent(t, "immutable-agent")
	exe := fakeExecutable(t)
	before, err := hashSource(agent)
	if err != nil {
		t.Fatal(err)
	}
	stageWith(t, agent, "codex", exe)
	after, err := hashSource(agent)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("staging mutated authored source")
	}
}

func TestStageRefusesExistingOutput(t *testing.T) {
	agent := writeAgent(t, "agent")
	exe := fakeExecutable(t)
	out := filepath.Join(t.TempDir(), "exists")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	res, diags, err := Stage(context.Background(), Options{
		AgentDir: agent, Harness: "claude", Output: out, Executable: exe, Driver: claude.Driver{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != nil || !diags.HasErrors() {
		t.Fatal("staging into an existing directory must fail with a diagnostic")
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "stage.output.exists" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected stage.output.exists diagnostic, got %v", diags.All())
	}
}

func TestStageFinalPathCorrectness(t *testing.T) {
	agent := writeAgent(t, "paths-agent")
	exe := fakeExecutable(t)
	out := stageWith(t, agent, "claude", exe)

	mcp, err := os.ReadFile(filepath.Join(out, "workspace", ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mcp), finalTenonBin) {
		t.Fatalf(".mcp.json must embed the final tenon path %s: %s", finalTenonBin, mcp)
	}
	if !strings.Contains(string(mcp), finalWorkspace) {
		t.Fatalf(".mcp.json must embed the final workspace path %s: %s", finalWorkspace, mcp)
	}
	if !strings.Contains(string(mcp), finalAgentsRoot+"/paths-agent") {
		t.Fatalf(".mcp.json must embed the final agent source path: %s", mcp)
	}
	if strings.Contains(string(mcp), out) {
		t.Fatalf(".mcp.json must never embed the physical staging directory %s: %s", out, mcp)
	}

	record, err := os.ReadFile(filepath.Join(out, "workspace", ".tenon", "apply-claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record), finalAgentsRoot+"/paths-agent") {
		t.Fatalf("apply record must record the final source path: %s", record)
	}
	if strings.Contains(string(record), out) {
		t.Fatalf("apply record must never record the physical staging directory: %s", record)
	}
}

func TestStageCredentialAbsent(t *testing.T) {
	t.Setenv("FAKE_SECRET", "CONSPICUOUS")
	agent := writeAgent(t, "clean-agent")
	exe := fakeExecutable(t)
	out := stageWith(t, agent, "codex", exe)

	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), "CONSPICUOUS") {
			t.Fatalf("the conspicuous secret leaked into %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStageVerifyRoundTrip(t *testing.T) {
	agent := writeAgent(t, "verify-agent")
	exe := fakeExecutable(t)
	out := stageWith(t, agent, "claude", exe)
	artifact := filepath.Join(out, "opt", "tenon", "artifact.json")

	if err := Verify(artifact, out); err != nil {
		t.Fatalf("a freshly staged tree must verify: %v", err)
	}

	// Corrupt a staged generated file: verification must fail closed.
	target := filepath.Join(out, "workspace", "CLAUDE.md")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, append(data, '!'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(artifact, out); err == nil {
		t.Fatal("verification must fail closed after a staged file is corrupted")
	}
}

func TestStageAtomicPublishNoLeftoverOnFailure(t *testing.T) {
	agent := writeAgent(t, "symlink-agent")
	exe := fakeExecutable(t)
	// A stray symlink in the source is rejected mid-stage, after some files
	// were written to the temporary tree.
	if err := os.Symlink("/etc/hostname", filepath.Join(agent, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	parent := t.TempDir()
	out := filepath.Join(parent, "out")
	res, _, err := Stage(context.Background(), Options{
		AgentDir: agent, Harness: "claude", Output: out, Executable: exe, Driver: claude.Driver{},
	})
	if err == nil {
		t.Fatal("staging a source with a symlink must fail")
	}
	if res != nil {
		t.Fatal("no result on failure")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("a failed staging must leave no output directory")
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tenon-stage-") {
			t.Fatalf("a failed staging left a temporary directory behind: %s", e.Name())
		}
	}
}

func TestStageToolFreeCarriesNoRuntime(t *testing.T) {
	agent := writeAgent(t, "tool-free")
	exe := fakeExecutable(t)
	out := stageWith(t, agent, "claude", exe)

	runtimes := filepath.Join(out, "opt", "tenon", "runtimes")
	entries, err := os.ReadDir(runtimes)
	if err != nil {
		t.Fatalf("the runtimes directory must exist: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a tool-free agent must stage no runtime closure, found %d entries", len(entries))
	}
}

func TestStageGoToolCarriesRuntimeClosure(t *testing.T) {
	agent := writeAgent(t, "go-tool-agent")
	writeFile(t, agent, "go.mod", "module example.com/go-tool-agent\n\ngo 1.24\n")
	writeFile(t, agent, "tools/hash_text/tool.go", goTool)
	exe := fakeExecutable(t)

	out := filepath.Join(t.TempDir(), "staged")
	res, diags, err := Stage(context.Background(), Options{
		AgentDir: agent, Harness: "codex", Output: out, Executable: exe, Driver: codex.Driver{},
	})
	if err != nil {
		t.Fatalf("stage error: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("stage diagnostics: %v", diags.All())
	}
	if len(res.RuntimeLanguages) != 1 || res.RuntimeLanguages[0] != "go" {
		t.Fatalf("expected a go runtime closure, got %v", res.RuntimeLanguages)
	}

	// The self-contained Go host binary must be present under the closure.
	var found bool
	closure := filepath.Join(out, "opt", "tenon", "runtimes", "tools")
	_ = filepath.WalkDir(closure, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(path) == "host" {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatal("the staged Go closure must contain the built host binary")
	}
	if err := Verify(filepath.Join(out, "opt", "tenon", "artifact.json"), out); err != nil {
		t.Fatalf("a staged Go-tool tree must verify: %v", err)
	}
}

const pythonTool = `"""Count the words in bounded text."""

from pydantic import BaseModel

description = "Count the words in bounded text."


class Input(BaseModel):
    text: str


class Output(BaseModel):
    words: int


def execute(input: Input, context: dict) -> Output:
    return Output(words=len(input.text.split()))
`

// requireUV skips the test when uv is absent, matching cmd/tenon's own
// requireToolchain gate: TENON_REQUIRE_TOOLCHAINS=1 turns the gap into a
// named failure so CI green still means the Python closure path ran.
func requireUV(t *testing.T) string {
	t.Helper()
	found, err := exec.LookPath("uv")
	if err != nil {
		if os.Getenv("TENON_REQUIRE_TOOLCHAINS") == "1" {
			t.Fatal("uv is not on PATH but TENON_REQUIRE_TOOLCHAINS=1 requires it")
		}
		t.Skip("uv is not on PATH; the Python closure staging path is proven without it")
	}
	return found
}

// TestStagePythonToolCarriesRuntimeClosure proves ADR 0021's Python closure
// stages: preparation installs a pinned standalone CPython and the project's
// locked dependencies into the closure with no venv, staging normalizes the
// interpreter's baked-in install path to the final canonical location, and
// the staged tree carries no symlink and no build-machine path.
func TestStagePythonToolCarriesRuntimeClosure(t *testing.T) {
	uv := requireUV(t)
	agent := writeAgent(t, "python-tool-agent")
	writeFile(t, agent, "pyproject.toml", "[project]\nname = \"python-tool-agent\"\nversion = \"0.0.0\"\n"+
		"requires-python = \">=3.11\"\ndependencies = [\"pydantic>=2\"]\n\n[tool.uv]\npackage = false\n")
	writeFile(t, agent, "tools/count_words.py", pythonTool)
	cmd := exec.Command(uv, "lock")
	cmd.Dir = agent
	if output, err := cmd.CombinedOutput(); err != nil {
		if os.Getenv("TENON_REQUIRE_TOOLCHAINS") == "1" {
			t.Fatalf("uv lock failed but TENON_REQUIRE_TOOLCHAINS=1 requires it: %v\n%s", err, output)
		}
		t.Skipf("uv lock could not resolve the fixture's dependencies (network needed): %v\n%s", err, output)
	}
	exe := fakeExecutable(t)

	out := filepath.Join(t.TempDir(), "staged")
	res, diags, err := Stage(context.Background(), Options{
		AgentDir: agent, Harness: "claude", Output: out, Executable: exe, Driver: claude.Driver{},
	})
	if err != nil {
		t.Fatalf("stage error: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("stage diagnostics: %v", diags.All())
	}
	if len(res.RuntimeLanguages) != 1 || res.RuntimeLanguages[0] != "python" {
		t.Fatalf("expected a python runtime closure, got %v", res.RuntimeLanguages)
	}

	closure := filepath.Join(out, "opt", "tenon", "runtimes", "tools")

	// The closure is entirely symlink-free: copyTree already refuses a
	// symlink, but this proves the interpreter's own convenience symlinks
	// (bin/python, bin/python3, python3-config, pydoc3, 2to3, idle3, the
	// lib/libpython*.so link) were actually removed at preparation, not
	// merely not-yet-encountered.
	var interpreterBin string
	err = filepath.WalkDir(closure, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			t.Fatalf("the staged python closure carries a symlink: %s", path)
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), "python3.") && filepath.Base(filepath.Dir(path)) == "bin" {
			interpreterBin = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if interpreterBin == "" {
		t.Fatal("the staged python closure must carry the versioned interpreter binary")
	}

	// The sysconfigdata rewrite left no trace of the throwaway preparation
	// directory anywhere in the closure — the build-machine-path scan
	// already proves this for the whole tree, but this asserts the specific
	// mechanism this test exists to cover.
	_ = filepath.WalkDir(closure, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "_sysconfigdata_") {
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if !strings.Contains(string(content), "/opt/tenon/runtimes/tools/") {
			t.Fatalf("the sysconfigdata file must be rewritten to the final canonical closure path: %s", path)
		}
		return nil
	})

	if err := Verify(filepath.Join(out, "opt", "tenon", "artifact.json"), out); err != nil {
		t.Fatalf("a staged Python-tool tree must verify: %v", err)
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goTool = `package hash_text

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

var Description = "Hash bounded text with SHA-256."

type Input struct {
	Text string ` + "`json:\"text\"`" + `
}

type Output struct {
	Hex string ` + "`json:\"hex\"`" + `
}

func Execute(ctx context.Context, in Input) (Output, error) {
	sum := sha256.Sum256([]byte(in.Text))
	return Output{Hex: hex.EncodeToString(sum[:])}, nil
}
`

// TestStageRefusesTypeScriptTools proves staging refuses a TypeScript-bearing
// agent with the stable stage.tools.runtime-unsupported diagnostic, before
// any mutation: the output directory is never created.
func TestStageRefusesTypeScriptTools(t *testing.T) {
	agent := writeAgent(t, "ts-tool-agent")
	writeFile(t, agent, "deno.json", "{}\n")
	writeFile(t, agent, "deno.lock", "{}\n")
	writeFile(t, agent, "tools/shout_text.ts", "export default {}\n")
	exe := fakeExecutable(t)

	out := filepath.Join(t.TempDir(), "staged")
	res, diags, err := Stage(context.Background(), Options{
		AgentDir: agent, Harness: "claude", Output: out, Executable: exe, Driver: claude.Driver{},
	})
	if err != nil {
		t.Fatalf("stage error: %v", err)
	}
	if res != nil {
		t.Fatalf("a refused stage must report no result, got %v", res)
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "stage.tools.runtime-unsupported" {
			found = true
			if !strings.Contains(d.Rule, "TypeScript") {
				t.Fatalf("the diagnostic must name the language: %q", d.Rule)
			}
		}
	}
	if !found {
		t.Fatalf("expected stage.tools.runtime-unsupported, got %v", diags.All())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("a refused stage must leave no output directory")
	}
}

// TestStageGoClosureCarriesNoBuildMachinePath stages a real Go-tool agent and
// proves no staged file embeds a build-machine path component of the
// fixture's own physical directories — matched exactly the way Stage's own
// build-machine-path scan matches, component by component, not by looking
// for the joined absolute string. A joined-absolute-string check is not
// equivalent: it passes by construction against a relative path fragment
// that omits the absolute prefix entirely while still leaking a directory
// name from partway up the chain, which is exactly the shape a relative
// go.mod replace target could take.
//
// The real defect this once caught: the generated Go host's go.mod named
// its replace target with the absolute preparation-machine agent source
// path (toolruntime.renderGoHost embedded cfg.Source verbatim), and `go
// build -trimpath` does not scrub a module replace target the way it
// scrubs recorded source-file paths, so the built host binary's own
// embedded module info (visible via `go version -m`, read by
// runtime/debug.ReadBuildInfo) carried the same absolute path even after a
// hypothetical post-build go.mod rewrite. The fix is upstream in two parts:
// toolruntime.renderGoHost now names the replace target relative to the
// directory its go.mod is written into, and preparation now builds against
// a copy of the tool source at a fixed directory name rather than cfg.Source
// directly, so that relative target renders as a machine-independent
// constant ("../agent-source") instead of a relative path shaped by this
// machine's own directory layout.
func TestStageGoClosureCarriesNoBuildMachinePath(t *testing.T) {
	agent := writeAgent(t, "scan-go-tool-agent")
	writeFile(t, agent, "go.mod", "module example.com/scan-go-tool-agent\n\ngo 1.24\n")
	writeFile(t, agent, "tools/hash_text/tool.go", goTool)
	exe := fakeExecutable(t)

	out := filepath.Join(t.TempDir(), "staged")
	res, diags, err := Stage(context.Background(), Options{
		AgentDir: agent, Harness: "codex", Output: out, Executable: exe, Driver: codex.Driver{},
	})
	if err != nil {
		t.Fatalf("stage error: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("stage diagnostics: %v", diags.All())
	}
	if len(res.RuntimeLanguages) != 1 || res.RuntimeLanguages[0] != "go" {
		t.Fatalf("expected a go runtime closure, got %v", res.RuntimeLanguages)
	}

	componentNeedles := buildMachineNeedles(agent, filepath.Dir(exe), nil)
	joinedNeedles := buildMachineJoinedNeedles(agent, filepath.Dir(exe), nil)
	if len(componentNeedles) == 0 && len(joinedNeedles) == 0 {
		t.Fatal("the fixture's own agent and executable directories must yield at least one dangerous needle to check against; the test proves nothing otherwise")
	}
	scanDiags := &diagnostics.List{}
	if err := rejectBuildMachinePaths(out, "", componentNeedles, joinedNeedles, scanDiags); err != nil {
		t.Fatal(err)
	}
	if scanDiags.HasErrors() {
		t.Fatalf("the staged tree embeds a build-machine path: %v", scanDiags.All())
	}

	if err := Verify(filepath.Join(out, "opt", "tenon", "artifact.json"), out); err != nil {
		t.Fatalf("the normalized staged tree must still verify: %v", err)
	}
}

// TestBuildPathComponentsExcludesExpectedVocabulary proves the scan's
// component extraction behaves as designed: tenon's own canonical
// vocabulary and short segments (a Go test harness's own "001"-style
// TempDir counters among them) never register as dangerous, a directory's
// own final segment is dropped when skipLast names an intentionally
// published identity (the agent name, expected in canonical output), and a
// genuinely distinguishing segment survives either way.
func TestBuildPathComponentsExcludesExpectedVocabulary(t *testing.T) {
	dir := "/home/tenon/workspace/opt/go/003/sessionsuffix123456/my-agent"

	got := buildPathComponents(dir, true)
	if len(got) != 1 || got[0] != "sessionsuffix123456" {
		t.Fatalf("buildPathComponents(skipLast) = %v, want [\"sessionsuffix123456\"]", got)
	}

	full := buildPathComponents(dir, false)
	if len(full) != 2 || full[0] != "sessionsuffix123456" || full[1] != "my-agent" {
		t.Fatalf("buildPathComponents(!skipLast) = %v, want [\"sessionsuffix123456\" \"my-agent\"]", full)
	}
}

// TestRejectBuildMachinePathsFiresOnALeak proves the stage-time scan itself
// — not just its absence of effect once the Go closure fix landed — fails
// closed with the stable stage.tree.build-path-leaked diagnostic naming the
// offending staged path and the exact leaked component, and leaves a clean
// tree with no diagnostic at all.
func TestRejectBuildMachinePathsFiresOnALeak(t *testing.T) {
	needles := [][]byte{[]byte("sessionsuffix123456")}

	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, "file.txt"), []byte("nothing interesting here"), 0o644); err != nil {
		t.Fatal(err)
	}
	diags := &diagnostics.List{}
	if err := rejectBuildMachinePaths(clean, "", needles, nil, diags); err != nil {
		t.Fatalf("scanning a clean tree: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("a clean tree must report no diagnostic: %v", diags.All())
	}

	leaking := t.TempDir()
	if err := os.WriteFile(filepath.Join(leaking, "go.mod"),
		[]byte("replace example.com/x => /tmp/sessionsuffix123456/agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diags = &diagnostics.List{}
	if err := rejectBuildMachinePaths(leaking, "", needles, nil, diags); err != nil {
		t.Fatalf("scanning a leaking tree: %v", err)
	}
	found := false
	for _, d := range diags.All() {
		if d.ID != "stage.tree.build-path-leaked" {
			continue
		}
		found = true
		if d.Path != "go.mod" {
			t.Fatalf("the diagnostic must name the leaking staged file, got path %q", d.Path)
		}
		if !strings.Contains(d.Rule, "sessionsuffix123456") {
			t.Fatalf("the diagnostic must name the leaked component: %q", d.Rule)
		}
	}
	if !found {
		t.Fatalf("expected stage.tree.build-path-leaked, got %v", diags.All())
	}
}

// TestRejectBuildMachinePathsIgnoresBareComponentsInABinary proves the
// heart of the binary/text split: a compiled binary embedding a dangerous
// component only as a bare, isolated token (the exact shape a Go module's
// own domain-style import prefix, owner segment, or a standard-library
// package name legitimately takes in any binary's own build info) is not
// flagged, where the same bytes in a text file would be.
func TestRejectBuildMachinePathsIgnoresBareComponentsInABinary(t *testing.T) {
	componentNeedles := [][]byte{[]byte("github.com"), []byte("someowner")}

	textDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(textDir, "notes.txt"),
		[]byte("see github.com/someowner for details"), 0o644); err != nil {
		t.Fatal(err)
	}
	diags := &diagnostics.List{}
	if err := rejectBuildMachinePaths(textDir, "", componentNeedles, nil, diags); err != nil {
		t.Fatalf("scanning text: %v", err)
	}
	if !diags.HasErrors() {
		t.Fatal("a text file embedding a dangerous component must still be flagged")
	}

	binDir := t.TempDir()
	// A NUL byte is looksBinary's simplest reliable signal; the rest of the
	// content is the same bare component a Go binary's own module data
	// would legitimately carry.
	binContent := append([]byte{0}, []byte("dep github.com/someowner/thing v1.0.0")...)
	if err := os.WriteFile(filepath.Join(binDir, "host"), binContent, 0o755); err != nil {
		t.Fatal(err)
	}
	diags = &diagnostics.List{}
	if err := rejectBuildMachinePaths(binDir, "", componentNeedles, nil, diags); err != nil {
		t.Fatalf("scanning a binary: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("bare components must not be checked against a binary file: %v", diags.All())
	}
}

// TestRejectBuildMachinePathsFiresOnAJoinedLeakInABinary proves binaries are
// not simply exempt from the scan: a binary embedding a dangerous
// directory's full path, or its "../"+basename relative form, still fails
// closed.
func TestRejectBuildMachinePathsFiresOnAJoinedLeakInABinary(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
	}{
		{"absolute", []byte("...replace target => /tmp/x/sessionsuffix123456 ...")},
		{"relative", []byte("...replace target => ../sessionsuffix123456 ...")},
	}
	joinedNeedles := buildMachineJoinedNeedles("/tmp/x/sessionsuffix123456", "", nil)
	if len(joinedNeedles) < 2 {
		t.Fatalf("expected both an absolute and a relative joined needle, got %v", joinedNeedles)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			binContent := append([]byte{0}, c.content...)
			if err := os.WriteFile(filepath.Join(dir, "host"), binContent, 0o755); err != nil {
				t.Fatal(err)
			}
			diags := &diagnostics.List{}
			if err := rejectBuildMachinePaths(dir, "", nil, joinedNeedles, diags); err != nil {
				t.Fatalf("scanning: %v", err)
			}
			found := false
			for _, d := range diags.All() {
				if d.ID == "stage.tree.build-path-leaked" {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected the scan to fire on a binary embedding a joined leak, got %v", diags.All())
			}
		})
	}
}

// TestRejectBuildMachinePathsRoutesByProvenanceNotContentType is the
// synthetic, network-free proof of the fix for the false-positive class a
// real Python closure produces: a TEXT file positioned inside a carried-in
// runtime payload (the closure's interpreter tree) that happens to carry a
// bare component match — exactly CPython's own stdlib shape, thousands of
// ordinary text files free to carry short tokens like "github.com" or an
// organization name — must NOT fire, purely because of where it lives, not
// because of whether its bytes look binary. A text file living at a
// tenon-generated position (the workspace's own apply record, standing in
// for go.mod/main.go/the copied agent source) carrying the identical bytes
// still must fire: the scan is not simply disabled near a closure, only
// re-routed within it. See carriedPayload.
func TestRejectBuildMachinePathsRoutesByProvenanceNotContentType(t *testing.T) {
	root := t.TempDir()
	closureRootFinal := "/opt/tenon/runtimes/tools"
	hash := "abc123hash"

	// Carried-in payload position: a plain-text stdlib-shaped file under the
	// closure's own interpreter tree.
	writeFile(t, root, filepath.Join("opt", "tenon", "runtimes", "tools", hash,
		"cpython", "cpython-3.11.13-linux-x86_64-gnu", "lib", "python3.11", "some_stdlib.py"),
		"import os\n# see github.com/someowner for details\n")

	// Generated position: text tenon itself writes (standing in for the
	// apply record / go.mod / main.go / the copied agent source).
	writeFile(t, root, filepath.Join("workspace", ".tenon", "apply-claude.json"),
		`{"note":"replace target => /tmp/x/github.com/someowner/leak"}`)

	componentNeedles := [][]byte{[]byte("github.com"), []byte("someowner")}
	diags := &diagnostics.List{}
	if err := rejectBuildMachinePaths(root, closureRootFinal, componentNeedles, nil, diags); err != nil {
		t.Fatal(err)
	}

	flagged := map[string]bool{}
	for _, d := range diags.All() {
		if d.ID == "stage.tree.build-path-leaked" {
			flagged[d.Path] = true
		}
	}
	if flagged[filepath.ToSlash(filepath.Join("opt", "tenon", "runtimes", "tools", hash,
		"cpython", "cpython-3.11.13-linux-x86_64-gnu", "lib", "python3.11", "some_stdlib.py"))] {
		t.Fatalf("a carried-in payload text file must not be component-matched: %v", diags.All())
	}
	if !flagged[filepath.ToSlash(filepath.Join("workspace", ".tenon", "apply-claude.json"))] {
		t.Fatalf("a generated text file embedding the identical bytes must still be flagged: %v", diags.All())
	}
}

// TestBuildMachineJoinedNeedles proves the joined-needle construction: each
// dangerous directory's own full path is always a needle, and its
// "../"+basename relative form is a needle only when that basename clears
// the same length and vocabulary bar component matching uses.
func TestBuildMachineJoinedNeedles(t *testing.T) {
	got := buildMachineJoinedNeedles("/a/b/sessionsuffix123456", "", nil)
	want := map[string]bool{
		"/a/b/sessionsuffix123456": true,
		"../sessionsuffix123456":   true,
	}
	if len(got) != len(want) {
		t.Fatalf("buildMachineJoinedNeedles = %v, want exactly %v", asStrings(got), want)
	}
	for _, n := range got {
		if !want[string(n)] {
			t.Fatalf("unexpected needle %q", n)
		}
	}

	// A short or safe-listed basename yields no relative-join needle, only
	// the full path.
	got = buildMachineJoinedNeedles("/a/b/opt", "", nil)
	if len(got) != 1 || string(got[0]) != "/a/b/opt" {
		t.Fatalf("buildMachineJoinedNeedles with a safe basename = %v", asStrings(got))
	}
}

func asStrings(bs [][]byte) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = string(b)
	}
	return out
}
