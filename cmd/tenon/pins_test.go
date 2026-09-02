package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/manifest"
)

// withFakeResolver installs a credential-free closure resolver for the duration
// of a test, so the CLI never runs a real harness or toolchain. The seam is the
// package-level manifestResolverFor variable.
func withFakeResolver(t *testing.T, harnessVersion string, pkgs []manifest.PackageIdentity) {
	t.Helper()
	prev := manifestResolverFor
	t.Cleanup(func() { manifestResolverFor = prev })
	manifestResolverFor = func(_ *agentproject.Project, _, _ string) manifest.Resolver {
		return manifest.Resolver{
			HarnessVersion:    func(string) (string, error) { return harnessVersion, nil },
			ToolRuntimes:      func() (string, string, string, string, error) { return "", "", "", "", nil },
			PackageIdentities: func(string) ([]manifest.PackageIdentity, error) { return pkgs, nil },
		}
	}
}

// writePinsFor runs `tenon check --write-pins` for agent with the active fake
// resolver and returns the written manifest path.
func writePinsFor(t *testing.T, agent string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pins.json")
	var out, errb bytes.Buffer
	if code := run([]string{"check", agent, "--harness", "claude", "--write-pins", path}, nil, &out, &errb); code != 0 {
		t.Fatalf("check --write-pins exit %d: %s", code, errb.String())
	}
	return path
}

// TestCheckWritePinsByteIdentical proves acceptance item 12's determinism clause:
// writing the manifest for an unchanged closure is byte-identical across runs.
func TestCheckWritePinsByteIdentical(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", []manifest.PackageIdentity{{ID: "gh", ManifestSHA256: "abc123"}})

	a := filepath.Join(t.TempDir(), "a.json")
	b := filepath.Join(t.TempDir(), "b.json")
	var out, errb bytes.Buffer
	if code := run([]string{"check", agent, "--harness", "claude", "--write-pins", a}, nil, &out, &errb); code != 0 {
		t.Fatalf("first write failed: %s", errb.String())
	}
	if code := run([]string{"check", agent, "--harness", "claude", "--write-pins", b}, nil, &out, &errb); code != 0 {
		t.Fatalf("second write failed: %s", errb.String())
	}
	ba, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	if len(ba) == 0 {
		t.Fatal("manifest write produced no bytes")
	}
	if !bytes.Equal(ba, bb) {
		t.Fatalf("manifest write is not byte-identical:\n%s\n---\n%s", ba, bb)
	}
}

// TestApplyManifestDriftFailsClosed proves that apply --manifest with a drifted
// pin fails closed naming the pin and writes nothing.
func TestApplyManifestDriftFailsClosed(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	manifestPath := writePinsFor(t, agent)

	// Drift the closure: a different harness version.
	withFakeResolver(t, "9.9.9", nil)
	workspace := t.TempDir()
	var out, errb bytes.Buffer
	code := run([]string{"apply", agent, "--harness", "claude", "--workspace", workspace, "--pins", manifestPath}, nil, &out, &errb)
	if code == 0 {
		t.Fatalf("apply must fail on drift; stdout=%s stderr=%s", out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "version drift") {
		t.Fatalf("drift must name the exact pin: %s", errb.String())
	}
	if _, err := os.Stat(filepath.Join(workspace, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("a drifted apply must write no generated files")
	}
	if _, err := os.Stat(filepath.Join(workspace, ".tenon")); !os.IsNotExist(err) {
		t.Fatal("a drifted apply must write no state")
	}
}

// TestApplyManifestMatchRecordsIdentity proves a matching manifest lets apply
// proceed and the apply record carries the manifest identity as a provenance
// join key.
func TestApplyManifestMatchRecordsIdentity(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", []manifest.PackageIdentity{{ID: "gh", ManifestSHA256: "abc123"}})
	manifestPath := writePinsFor(t, agent)

	workspace := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", workspace, "--pins", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("apply with matching manifest must succeed: %s", errb.String())
	}
	want := parsedIdentity(t, manifestPath)
	if got := recordManifest(t, workspace); got != want {
		t.Fatalf("apply record manifest = %q, want %q", got, want)
	}
}

// TestApplyWithoutManifestUnchanged proves an unsupplied manifest changes
// nothing: the apply record carries no manifest field.
func TestApplyWithoutManifestUnchanged(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	workspace := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", workspace}, nil, &out, &errb); code != 0 {
		t.Fatalf("apply failed: %s", errb.String())
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".tenon", "apply-claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if _, ok := record["manifest"]; ok {
		t.Fatalf("an unsupplied manifest must leave no manifest field: %s", raw)
	}
}

// TestManifestValueNeverInGeneratedContent is the model-facing exclusion guard:
// a conspicuous manifest value (a fake harness version and package hash) appears
// in NONE of the generated, model-visible files. The tenon-owned apply record
// under .tenon legitimately carries the manifest identity and is excluded.
func TestManifestValueNeverInGeneratedContent(t *testing.T) {
	const conspicuousVersion = "9.9.9-CONSPICUOUS"
	const conspicuousHash = "deadbeefconspicuoushash"
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, conspicuousVersion, []manifest.PackageIdentity{{ID: "conspicuous-pkg", ManifestSHA256: conspicuousHash}})
	manifestPath := writePinsFor(t, agent)

	workspace := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", workspace, "--pins", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("apply failed: %s", errb.String())
	}
	identity := parsedIdentity(t, manifestPath)

	err := filepath.Walk(workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// The tenon-owned .tenon state is not model-facing.
			if info.Name() == ".tenon" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		body := string(data)
		for _, needle := range []string{conspicuousVersion, conspicuousHash, identity} {
			if strings.Contains(body, needle) {
				t.Fatalf("generated file %s leaks a manifest value %q", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCheckManifestReportsSameDriftAsApply proves check/apply parity for
// manifest drift: identical structured diagnostics, and check mutates
// nothing.
func TestCheckManifestReportsSameDriftAsApply(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	manifestPath := writePinsFor(t, agent)

	withFakeResolver(t, "9.9.9", nil) // drift
	var checkOut, applyOut, errb bytes.Buffer
	checkCode := run([]string{"check", agent, "--harness", "claude", "--pins", manifestPath, "--format", "jsonl"}, nil, &checkOut, &errb)
	applyWorkspace := t.TempDir()
	applyCode := run([]string{"apply", agent, "--harness", "claude", "--workspace", applyWorkspace, "--pins", manifestPath, "--format", "jsonl"}, nil, &applyOut, &errb)

	if checkCode == 0 || applyCode == 0 {
		t.Fatalf("both must fail on drift: check=%d apply=%d", checkCode, applyCode)
	}
	if checkDiagnostics(t, checkOut.String()) != checkDiagnostics(t, applyOut.String()) {
		t.Fatalf("check and apply must report identical drift:\ncheck: %s\napply: %s", checkOut.String(), applyOut.String())
	}
	if !strings.Contains(checkOut.String(), "pins.drift.harness-version") {
		t.Fatalf("drift diagnostic must carry the stable identifier: %s", checkOut.String())
	}
	if _, err := os.Stat(filepath.Join(applyWorkspace, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("a drifted apply must not mutate the workspace")
	}
	if _, err := os.Stat(filepath.Join(applyWorkspace, ".tenon")); !os.IsNotExist(err) {
		t.Fatal("a drifted apply must write no state")
	}
}

func parsedIdentity(t *testing.T, manifestPath string) string {
	t.Helper()
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		t.Fatalf("supplied manifest does not parse: %v", err)
	}
	return m.Identity()
}

func recordManifest(t *testing.T, workspace string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workspace, ".tenon", "apply-claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Manifest string `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record.Manifest
}

// TestCheckWritePinsProvesInstructionsFreeRoot proves the RSI-substrate flow: an
// instructions-free directory (a legitimate loop-generated candidate) can mint
// its own proving manifest with `check --write-pins`, and applying with that
// manifest then proves the root that instructions.md would otherwise prove.
func TestCheckWritePinsProvesInstructionsFreeRoot(t *testing.T) {
	agent := writeAgent(t, "my-agent", "") // no instructions.md
	withFakeResolver(t, "2.1.240", nil)

	manifestPath := filepath.Join(t.TempDir(), "pins.json")
	var out, errb bytes.Buffer
	if code := run([]string{"check", agent, "--harness", "claude", "--write-pins", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("manifest write must self-prove an instructions-free root: %s", errb.String())
	}

	workspace := t.TempDir()
	out.Reset()
	errb.Reset()
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", workspace, "--pins", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("apply with the self-proving manifest must succeed: %s", errb.String())
	}
	// The manifest proved the root; an instructions-free project generates an
	// empty always-on surface, so no CLAUDE.md exists but the apply recorded.
	if _, err := os.Stat(filepath.Join(workspace, ".tenon", "apply-claude.json")); err != nil {
		t.Fatalf("apply record missing after manifest-proven apply: %v", err)
	}
}

// TestCheckWritePinsUsageErrors proves the two flag preconditions the gate
// enforces before it loads anything: resolving a closure to write is
// harness-specific, so --write-pins requires --harness; and a model is only
// ever recorded into a pin set being written, so --model requires
// --write-pins. Both are usage errors (exit 2), not gate failures.
func TestCheckWritePinsUsageErrors(t *testing.T) {
	// The absence of --harness is the subject here, so the developer's own
	// TENON_HARNESS must not supply one: with it set, these runs would take
	// the harness path and the assertions below would prove nothing.
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	path := filepath.Join(t.TempDir(), "pins.json")

	var out, errb bytes.Buffer
	if code := run([]string{"check", agent, "--write-pins", path}, nil, &out, &errb); code != 2 {
		t.Fatalf("--write-pins without --harness must be a usage error, got %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "--write-pins requires --harness") {
		t.Fatalf("the usage error must name the missing flag: %s", errb.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a refused --write-pins must write nothing")
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"check", agent, "--harness", "claude", "--model", "some-model"}, nil, &out, &errb); code != 2 {
		t.Fatalf("--model without --write-pins must be a usage error, got %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "--model requires --write-pins") {
		t.Fatalf("the usage error must name the missing flag: %s", errb.String())
	}
}

// TestWritePinsNeverWritesWhenTheGateFails proves the ordering ADR 0027
// binds: a pin set is only ever minted by a project that gates now, so a
// failing gate leaves no file behind at all — not an empty one, not a
// partial one.
func TestWritePinsNeverWritesWhenTheGateFails(t *testing.T) {
	agent := writeAgent(t, "my-agent", "no frontmatter\n")
	withFakeResolver(t, "2.1.240", nil)
	path := filepath.Join(t.TempDir(), "pins.json")

	var out, errb bytes.Buffer
	if code := run([]string{"check", agent, "--harness", "claude", "--write-pins", path, "--format", "jsonl"}, nil, &out, &errb); code != 1 {
		t.Fatalf("a failing gate must exit 1, got %d: %s", code, out.String())
	}
	if !strings.HasSuffix(strings.TrimSpace(out.String()), `{"outcome":"gate_failed"}`) {
		t.Fatalf("the stream must end with gate_failed: %q", out.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a failing gate must write no pin set: %v", err)
	}
}

// TestPinsAndWritePinsVerifyThenWrite proves the two flags compose in the
// fail-closed order: supplied pins are verified first, and the pin set is
// written only when that verification passed. On drift, nothing is written.
func TestPinsAndWritePinsVerifyThenWrite(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	supplied := writePinsFor(t, agent)

	// Verification passes: the written pin set is the verified closure, and
	// is byte-identical to the one supplied.
	rewritten := filepath.Join(t.TempDir(), "rewritten.json")
	var out, errb bytes.Buffer
	if code := run([]string{"check", agent, "--harness", "claude", "--pins", supplied, "--write-pins", rewritten}, nil, &out, &errb); code != 0 {
		t.Fatalf("verify-then-write exit %d: %s", code, errb.String())
	}
	before, err := os.ReadFile(supplied)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(rewritten)
	if err != nil {
		t.Fatalf("the verified closure must have been written: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("the written pin set must be the closure just verified:\n%s\n%s", before, after)
	}

	// Verification fails: the run refuses, and writes nothing.
	withFakeResolver(t, "9.9.9", nil)
	refused := filepath.Join(t.TempDir(), "refused.json")
	out.Reset()
	errb.Reset()
	if code := run([]string{"check", agent, "--harness", "claude", "--pins", supplied, "--write-pins", refused, "--format", "jsonl"}, nil, &out, &errb); code != 1 {
		t.Fatalf("drifted pins must refuse, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "pins.drift.harness-version") {
		t.Fatalf("the refusal must name the drifted pin: %q", out.String())
	}
	if _, err := os.Stat(refused); !os.IsNotExist(err) {
		t.Fatalf("a refused run must write no pin set: %v", err)
	}
}

// TestWritePinsWithEmitEndsWithExactlyOneResultObject proves the stream
// contract holds when every optional projection is switched on at once: the
// inventories are lines like any other, and the run still ends with one
// distinct result object carrying the outcome and the written path.
func TestWritePinsWithEmitEndsWithExactlyOneResultObject(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	path := filepath.Join(t.TempDir(), "pins.json")

	var out, errb bytes.Buffer
	if code := run([]string{"check", agent, "--harness", "claude", "--emit", "files,catalog", "--write-pins", path, "--format", "jsonl"}, nil, &out, &errb); code != 0 {
		t.Fatalf("check exit %d: %s", code, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	outcomes := 0
	for _, line := range lines {
		var obj struct {
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %q is not JSON: %v", line, err)
		}
		if obj.Outcome != "" {
			outcomes++
		}
	}
	if outcomes != 1 {
		t.Fatalf("exactly one object carries an outcome, got %d:\n%s", outcomes, out.String())
	}
	var summary struct {
		Outcome     string `json:"outcome"`
		PinsWritten string `json:"pins_written"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Outcome != "ok" || summary.PinsWritten != path {
		t.Fatalf("summary = %+v, want ok and the written path", summary)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the pin set must have been written: %v", err)
	}
}
