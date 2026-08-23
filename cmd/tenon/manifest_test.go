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
			ToolRuntimes:      func() (string, string, string, error) { return "", "", "", nil },
			PackageIdentities: func(string) ([]manifest.PackageIdentity, error) { return pkgs, nil },
		}
	}
}

// writeManifestFor runs `tenon manifest write` for agent with the active fake
// resolver and returns the written manifest path.
func writeManifestFor(t *testing.T, agent string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	var out, errb bytes.Buffer
	if code := run([]string{"manifest", "write", agent, "--harness", "claude", "--output", path}, nil, &out, &errb); code != 0 {
		t.Fatalf("manifest write exit %d: %s", code, errb.String())
	}
	return path
}

// TestManifestWriteByteIdentical proves acceptance item 12's determinism clause:
// writing the manifest for an unchanged closure is byte-identical across runs.
func TestManifestWriteByteIdentical(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", []manifest.PackageIdentity{{ID: "gh", ManifestSHA256: "abc123"}})

	a := filepath.Join(t.TempDir(), "a.json")
	b := filepath.Join(t.TempDir(), "b.json")
	var out, errb bytes.Buffer
	if code := run([]string{"manifest", "write", agent, "--harness", "claude", "--output", a}, nil, &out, &errb); code != 0 {
		t.Fatalf("first write failed: %s", errb.String())
	}
	if code := run([]string{"manifest", "write", agent, "--harness", "claude", "--output", b}, nil, &out, &errb); code != 0 {
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
	manifestPath := writeManifestFor(t, agent)

	// Drift the closure: a different harness version.
	withFakeResolver(t, "9.9.9", nil)
	workspace := t.TempDir()
	var out, errb bytes.Buffer
	code := run([]string{"apply", agent, "--harness", "claude", "--workspace", workspace, "--manifest", manifestPath}, nil, &out, &errb)
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
	manifestPath := writeManifestFor(t, agent)

	workspace := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", workspace, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
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
	manifestPath := writeManifestFor(t, agent)

	workspace := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", workspace, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
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

// TestValidateManifestReportsSameDriftAsApply proves validate/apply parity for
// manifest drift: identical structured diagnostics, and validate mutates
// nothing.
func TestValidateManifestReportsSameDriftAsApply(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	withFakeResolver(t, "2.1.240", nil)
	manifestPath := writeManifestFor(t, agent)

	withFakeResolver(t, "9.9.9", nil) // drift
	var validateOut, applyOut, errb bytes.Buffer
	validateCode := run([]string{"validate", agent, "--harness", "claude", "--manifest", manifestPath, "--diagnostics", "jsonl"}, nil, &validateOut, &errb)
	applyWorkspace := t.TempDir()
	applyCode := run([]string{"apply", agent, "--harness", "claude", "--workspace", applyWorkspace, "--manifest", manifestPath, "--diagnostics", "jsonl"}, nil, &applyOut, &errb)

	if validateCode == 0 || applyCode == 0 {
		t.Fatalf("both must fail on drift: validate=%d apply=%d", validateCode, applyCode)
	}
	if validateOut.String() != applyOut.String() {
		t.Fatalf("validate and apply must report identical drift:\nvalidate: %s\napply: %s", validateOut.String(), applyOut.String())
	}
	if !strings.Contains(validateOut.String(), "manifest.drift.harness-version") {
		t.Fatalf("drift diagnostic must carry the stable identifier: %s", validateOut.String())
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

// TestManifestWriteProvesInstructionsFreeRoot proves the RSI-substrate flow: an
// instructions-free directory (a legitimate loop-generated candidate) can mint
// its own proving manifest with `manifest write`, and applying with that
// manifest then proves the root that instructions.md would otherwise prove.
func TestManifestWriteProvesInstructionsFreeRoot(t *testing.T) {
	agent := writeAgent(t, "my-agent", "") // no instructions.md
	withFakeResolver(t, "2.1.240", nil)

	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	var out, errb bytes.Buffer
	if code := run([]string{"manifest", "write", agent, "--harness", "claude", "--output", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("manifest write must self-prove an instructions-free root: %s", errb.String())
	}

	workspace := t.TempDir()
	out.Reset()
	errb.Reset()
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", workspace, "--manifest", manifestPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("apply with the self-proving manifest must succeed: %s", errb.String())
	}
	// The manifest proved the root; an instructions-free project generates an
	// empty always-on surface, so no CLAUDE.md exists but the apply recorded.
	if _, err := os.Stat(filepath.Join(workspace, ".tenon", "apply-claude.json")); err != nil {
		t.Fatalf("apply record missing after manifest-proven apply: %v", err)
	}
}
