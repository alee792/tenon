package stage

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
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

// TestStageRefusesPythonTools proves staging refuses a Python-bearing agent
// with the stable stage.tools.runtime-unsupported diagnostic, before any
// mutation: the output directory is never created.
func TestStageRefusesPythonTools(t *testing.T) {
	agent := writeAgent(t, "python-tool-agent")
	writeFile(t, agent, "pyproject.toml", "[project]\nname = \"x\"\n")
	writeFile(t, agent, "uv.lock", "")
	writeFile(t, agent, "tools/count_words.py", "description = 'x'\n")
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
			if !strings.Contains(d.Rule, "Python") {
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

// TestStageRefusesTypeScriptTools mirrors TestStageRefusesPythonTools for
// TypeScript.
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

// TestStageGoClosureCarriesNoBuildMachinePath scans the entire staged Go-tool
// tree for the physical build-machine paths this test's own fixture used
// (the agent source directory and the fake tenon executable's directory,
// which stand in for whatever directories a real preparation machine would
// use) and fails if any staged file's content contains one. It also proves
// the staged tree still passes stage verify, so the fix this proves does not
// disturb it.
//
// Without the fix this once failed: the generated Go host's go.mod named its
// replace target with the absolute preparation-machine agent source path
// (toolruntime.renderGoHost embedded cfg.Source verbatim), and `go build
// -trimpath` does not scrub a module replace target the way it scrubs
// recorded source-file paths, so the built host binary's embedded module
// info (visible via `go version -m`, read by runtime/debug.ReadBuildInfo)
// carried the same absolute path even after go.mod itself was rewritten.
// renderGoHost now names the replace target relative to the directory its
// go.mod is written into, so the absolute path is never embedded in the
// first place, in either the go.mod source or the binary's own build info —
// the smaller true fix, over rewriting or deleting the file after the fact.
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

	buildPaths := []string{agent, filepath.Dir(exe)}
	err = filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, bad := range buildPaths {
			if strings.Contains(string(content), bad) {
				return fmt.Errorf("%s embeds the build-machine path %s", path, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := Verify(filepath.Join(out, "opt", "tenon", "artifact.json"), out); err != nil {
		t.Fatalf("the normalized staged tree must still verify: %v", err)
	}
}
