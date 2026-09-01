package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/manifest"
)

// writeManifestForModel runs `tenon manifest write --model model` for agent
// with the active fake resolver and returns the written manifest path.
func writeManifestForModel(t *testing.T, agent, harnessName, model string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	var out, errb bytes.Buffer
	args := []string{"manifest", "write", agent, "--harness", harnessName, "--output", path}
	if model != "" {
		args = append(args, "--model", model)
	}
	if code := run(args, nil, &out, &errb); code != 0 {
		t.Fatalf("manifest write exit %d: %s", code, errb.String())
	}
	return path
}

// TestManifestWriteModelFlagRecordsAndIsDeterministic proves --model records
// the value as the selected harness's pinned model, and that writing it twice
// for an unchanged closure is byte-identical (the same determinism clause
// TestManifestWriteByteIdentical proves for the unpinned case).
func TestManifestWriteModelFlagRecordsAndIsDeterministic(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)

	a := filepath.Join(t.TempDir(), "a.json")
	b := filepath.Join(t.TempDir(), "b.json")
	var out, errb bytes.Buffer
	if code := run([]string{"manifest", "write", agent, "--harness", "claude", "--model", "claude-opus-4", "--output", a}, nil, &out, &errb); code != 0 {
		t.Fatalf("first write failed: %s", errb.String())
	}
	if code := run([]string{"manifest", "write", agent, "--harness", "claude", "--model", "claude-opus-4", "--output", b}, nil, &out, &errb); code != 0 {
		t.Fatalf("second write failed: %s", errb.String())
	}
	ba, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ba, bb) {
		t.Fatalf("manifest write --model is not byte-identical:\n%s\n---\n%s", ba, bb)
	}
	m, err := manifest.Parse(ba)
	if err != nil {
		t.Fatalf("written manifest does not parse: %v", err)
	}
	if m.Harnesses["claude"].Model != "claude-opus-4" {
		t.Fatalf("recorded model = %q, want %q", m.Harnesses["claude"].Model, "claude-opus-4")
	}
}

// TestManifestWriteWithoutModelLeavesModelEmpty proves an unset --model
// leaves the model empty rather than resolving any default.
func TestManifestWriteWithoutModelLeavesModelEmpty(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	path := writeManifestForModel(t, agent, "claude", "")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Harnesses["claude"].Model != "" {
		t.Fatalf("model = %q, want empty when --model is not given", m.Harnesses["claude"].Model)
	}
}

// TestApplyCodexModelPinEmitsConfigTOML proves a model-pinned manifest
// supplied to apply threads Target.Model into the codex driver: the generated
// .codex/config.toml carries `model = "X"` above [mcp_servers.managed], and
// without a model the file is byte-identical to a plain no-manifest apply.
func TestApplyCodexModelPinEmitsConfigTOML(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "0.144.1", nil)
	manifestPath := writeManifestForModel(t, agent, "codex", "o4-mini")

	ws := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "codex", "--workspace", ws, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("apply failed: %s", errb.String())
	}
	got, err := os.ReadFile(filepath.Join(ws, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	modelIdx := strings.Index(string(got), `model = "o4-mini"`)
	tableIdx := strings.Index(string(got), "[mcp_servers.managed]")
	if modelIdx < 0 || tableIdx < 0 || modelIdx > tableIdx {
		t.Fatalf(".codex/config.toml must carry model = \"o4-mini\" above [mcp_servers.managed]:\n%s", got)
	}

	// Without a model, config.toml is byte-identical to a no-manifest apply —
	// compared at the SAME workspace path, since the path itself is embedded
	// in the rendered managed-server args.
	sharedWS := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(sharedWS, 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"apply", agent, "--harness", "codex", "--workspace", sharedWS}, nil, &out, &errb); code != 0 {
		t.Fatalf("no-manifest apply failed: %s", errb.String())
	}
	want, err := os.ReadFile(filepath.Join(sharedWS, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(sharedWS); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharedWS, 0o755); err != nil {
		t.Fatal(err)
	}

	unpinnedManifest := writeManifestForModel(t, agent, "codex", "")
	out.Reset()
	errb.Reset()
	if code := run([]string{"apply", agent, "--harness", "codex", "--workspace", sharedWS, "--manifest", unpinnedManifest}, nil, &out, &errb); code != 0 {
		t.Fatalf("unpinned-manifest apply failed: %s", errb.String())
	}
	gotUnpinned, err := os.ReadFile(filepath.Join(sharedWS, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, gotUnpinned) {
		t.Fatalf("an unpinned manifest must leave config.toml byte-identical to a no-manifest apply:\n%s\nvs\n%s", gotUnpinned, want)
	}
}

// TestApplyClaudeModelPinNoAuthorBase proves a model-pinned manifest with no
// authored harnesses/claude/.claude/settings.json base generates
// .claude/settings.json = {"model":"X"}.
func TestApplyClaudeModelPinNoAuthorBase(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	manifestPath := writeManifestForModel(t, agent, "claude", "claude-opus-4")

	ws := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("apply failed: %s", errb.String())
	}
	got, err := os.ReadFile(filepath.Join(ws, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"model\": \"claude-opus-4\"\n}\n"
	if string(got) != want {
		t.Fatalf("settings.json = %q, want %q", got, want)
	}
}

// TestApplyClaudeModelPinWithAuthorBaseInjectsAndDropsPassthrough proves a
// model-pinned manifest injects onto an authored
// harnesses/claude/.claude/settings.json base: the author's other keys
// survive, "model" is set, and exactly one .claude/settings.json lands in the
// workspace (the injected one, not also a raw copy).
func TestApplyClaudeModelPinWithAuthorBaseInjectsAndDropsPassthrough(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeFile(t, agent, "harnesses/claude/.claude/settings.json",
		[]byte(`{"permissions":{"allow":["Bash"]}}`), 0o644)
	withFakeResolver(t, "2.1.240", nil)
	manifestPath := writeManifestForModel(t, agent, "claude", "claude-opus-4")

	ws := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("apply failed: %s", errb.String())
	}
	got, err := os.ReadFile(filepath.Join(ws, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"model": "claude-opus-4"`) {
		t.Fatalf("settings.json must carry the injected model: %s", got)
	}
	if !strings.Contains(string(got), `"allow"`) {
		t.Fatalf("settings.json must preserve the author's other keys: %s", got)
	}
}

// TestApplyClaudeModelPinInvalidAuthorJSONFailsClosed proves invalid JSON in
// the authored settings.json base fails apply with claude.settings.invalid
// and writes nothing.
func TestApplyClaudeModelPinInvalidAuthorJSONFailsClosed(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeFile(t, agent, "harnesses/claude/.claude/settings.json", []byte(`{not valid`), 0o644)
	withFakeResolver(t, "2.1.240", nil)
	manifestPath := writeManifestForModel(t, agent, "claude", "claude-opus-4")

	ws := t.TempDir()
	var out, errb bytes.Buffer
	code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--manifest", manifestPath, "--diagnostics", "jsonl"}, nil, &out, &errb)
	if code == 0 {
		t.Fatal("invalid authored settings.json must fail apply")
	}
	if len(filterDiags(parseDiagLines(t, out.String()), "claude.settings.invalid")) == 0 {
		t.Fatalf("expected claude.settings.invalid, got %q", out.String())
	}
	entries, err := os.ReadDir(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failing apply must write nothing; found %v", entries)
	}
}

// TestReapplyHandEditedGeneratedClaudeSettingsFailsClosed proves the generic
// ownership machinery covers the generated .claude/settings.json exactly like
// any other tenon-owned file: hand-editing it after apply makes the next
// apply fail closed with apply.conflict.modified rather than overwrite the
// edit.
func TestReapplyHandEditedGeneratedClaudeSettingsFailsClosed(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	manifestPath := writeManifestForModel(t, agent, "claude", "claude-opus-4")

	ws := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("first apply failed: %s", errb.String())
	}
	settingsPath := filepath.Join(ws, ".claude", "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"model":"hand-edited"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errb.Reset()
	code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--manifest", manifestPath, "--diagnostics", "jsonl"}, nil, &out, &errb)
	if code == 0 {
		t.Fatal("reapplying over a hand-edited generated settings.json must fail closed")
	}
	got := filterDiags(parseDiagLines(t, out.String()), "apply.conflict.modified")
	if len(got) == 0 {
		t.Fatalf("expected apply.conflict.modified, got %q", out.String())
	}
	current, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != `{"model":"hand-edited"}` {
		t.Fatal("the hand-edited file must not be overwritten")
	}
}

// TestModelValueNeverLeaksIntoModelFacingContent is the model-facing exclusion
// guard for ADR 0020: a conspicuous pinned model value appears in the
// generated .codex/config.toml and .claude/settings.json alone, never in
// CLAUDE.md, AGENTS.md, instructions, or any generated skill.
func TestModelValueNeverLeaksIntoModelFacingContent(t *testing.T) {
	const conspicuousModel = "conspicuous-model-x"

	for _, harnessName := range []string{"claude", "codex"} {
		t.Run(harnessName, func(t *testing.T) {
			agent := writeAgent(t, "my-agent", validInstructions)
			withFakeResolver(t, "2.1.240", nil)
			manifestPath := writeManifestForModel(t, agent, harnessName, conspicuousModel)

			ws := t.TempDir()
			var out, errb bytes.Buffer
			if code := run([]string{"apply", agent, "--harness", harnessName, "--workspace", ws, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
				t.Fatalf("apply failed: %s", errb.String())
			}

			allowedPaths := map[string]bool{
				filepath.Join(ws, ".codex", "config.toml"):    true,
				filepath.Join(ws, ".claude", "settings.json"): true,
			}
			err := filepath.Walk(ws, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					if info.Name() == ".tenon" {
						return filepath.SkipDir
					}
					return nil
				}
				if allowedPaths[path] {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if strings.Contains(string(data), conspicuousModel) {
					t.Fatalf("model-facing file %s must never carry the pinned model value: %q", path, data)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestCheckApplyModelParity proves check builds the same Target.Model
// apply does: a model-pinned manifest produces identical diagnostics from
// check and apply, and check mutates nothing.
func TestCheckApplyModelParity(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeFile(t, agent, "harnesses/claude/.claude/settings.json", []byte(`{not valid`), 0o644)
	withFakeResolver(t, "2.1.240", nil)
	manifestPath := writeManifestForModel(t, agent, "claude", "claude-opus-4")

	ws := t.TempDir()
	var checkOut, applyOut, errb bytes.Buffer
	checkCode := run([]string{"check", agent, "--harness", "claude", "--manifest", manifestPath, "--diagnostics", "jsonl"}, nil, &checkOut, &errb)
	applyCode := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--manifest", manifestPath, "--diagnostics", "jsonl"}, nil, &applyOut, &errb)

	if checkCode == 0 || applyCode == 0 {
		t.Fatalf("both must fail on the invalid authored settings.json: check=%d apply=%d", checkCode, applyCode)
	}
	if checkDiagnostics(checkOut.String()) != applyOut.String() {
		t.Fatalf("check and apply must report identical diagnostics:\ncheck: %s\napply: %s", checkOut.String(), applyOut.String())
	}
	if !strings.Contains(checkOut.String(), "claude.settings.invalid") {
		t.Fatalf("expected claude.settings.invalid: %s", checkOut.String())
	}
	entries, err := os.ReadDir(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failing apply must write nothing; found %v", entries)
	}
}

// TestClaudeModelPinToUnpinnedTransition proves the ownership transition when a
// model pin is removed: with an author base, reapplying without the manifest
// reverts .claude/settings.json to the raw author base (model gone) with no
// ownership conflict; with no base, it is removed entirely.
func TestClaudeModelPinToUnpinnedTransition(t *testing.T) {
	t.Run("with author base reverts to passthrough", func(t *testing.T) {
		agent := writeAgent(t, "my-agent", validInstructions)
		base := []byte("{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash\"\n    ]\n  }\n}\n")
		writeFile(t, agent, "harnesses/claude/.claude/settings.json", base, 0o644)
		withFakeResolver(t, "2.1.240", nil)
		manifestPath := writeManifestForModel(t, agent, "claude", "claude-opus-4")

		ws := t.TempDir()
		var out, errb bytes.Buffer
		if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
			t.Fatalf("pinned apply failed: %s", errb.String())
		}
		if got, _ := os.ReadFile(filepath.Join(ws, ".claude", "settings.json")); !strings.Contains(string(got), `"model"`) {
			t.Fatalf("pinned settings.json must carry the model: %s", got)
		}
		// Reapply with no manifest: the pin is gone, so settings.json must
		// revert to the author's raw base, byte-for-byte, without a conflict.
		out.Reset()
		errb.Reset()
		if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &out, &errb); code != 0 {
			t.Fatalf("unpinned reapply failed (ownership conflict?): %s", errb.String())
		}
		got, err := os.ReadFile(filepath.Join(ws, ".claude", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, base) {
			t.Fatalf("settings.json must revert to the raw author base:\n%q\nwant\n%q", got, base)
		}
	})

	t.Run("without author base is removed", func(t *testing.T) {
		agent := writeAgent(t, "my-agent", validInstructions)
		withFakeResolver(t, "2.1.240", nil)
		manifestPath := writeManifestForModel(t, agent, "claude", "claude-opus-4")

		ws := t.TempDir()
		var out, errb bytes.Buffer
		if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
			t.Fatalf("pinned apply failed: %s", errb.String())
		}
		if _, err := os.Stat(filepath.Join(ws, ".claude", "settings.json")); err != nil {
			t.Fatalf("pinned apply must generate settings.json: %v", err)
		}
		out.Reset()
		errb.Reset()
		if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &out, &errb); code != 0 {
			t.Fatalf("unpinned reapply failed: %s", errb.String())
		}
		if _, err := os.Stat(filepath.Join(ws, ".claude", "settings.json")); !os.IsNotExist(err) {
			t.Fatalf("unpinned reapply must remove the generated settings.json, got %v", err)
		}
	})
}
