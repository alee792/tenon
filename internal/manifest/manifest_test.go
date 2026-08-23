package manifest

import (
	"errors"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
)

// fakeClosure is a credential-free resolver: it returns fixed pins without ever
// running a real harness or toolchain.
func fakeClosure(harnessVersion, deno, uv, goVer string, pkgs []PackageIdentity) Resolver {
	return Resolver{
		HarnessVersion:    func(string) (string, error) { return harnessVersion, nil },
		ToolRuntimes:      func() (string, string, string, error) { return deno, uv, goVer, nil },
		PackageIdentities: func(string) ([]PackageIdentity, error) { return pkgs, nil },
	}
}

func fakeProject() *agentproject.Project {
	return &agentproject.Project{
		Name:        "my-agent",
		Fingerprint: "sha256:" + strings.Repeat("a", 64),
	}
}

func mustCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %q, got nil", code)
	}
	var me *Error
	if !errors.As(err, &me) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if me.Code != code {
		t.Fatalf("expected code %q, got %q (%v)", code, me.Code, err)
	}
}

func TestParseStrictAndBounds(t *testing.T) {
	valid := `{
  "schema_version": 1,
  "agent": "my-agent",
  "source_fingerprint": "sha256:` + strings.Repeat("a", 64) + `",
  "tenon_version": "0.1.0-dev",
  "harnesses": {"claude": {"harness_version": "2.1.240", "tool_runtimes": {}}}
}`
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	// unknown field is rejected (closed decode)
	mustCode(t, mustParseErr(t, strings.Replace(valid, `"agent": "my-agent",`, `"agent": "my-agent", "extra": 1,`, 1)), "manifest.invalid")

	// wrong schema version
	mustCode(t, mustParseErr(t, strings.Replace(valid, `"schema_version": 1,`, `"schema_version": 2,`, 1)), "manifest.schema-version")

	// bad fingerprint
	mustCode(t, mustParseErr(t, strings.Replace(valid, "sha256:"+strings.Repeat("a", 64), "deadbeef", 1)), "manifest.source-fingerprint.invalid")

	// unknown harness key
	mustCode(t, mustParseErr(t, strings.Replace(valid, `"claude"`, `"gemini"`, 1)), "manifest.harness.unknown")

	// over the 32 KiB bound
	big := `{"schema_version":1,"agent":"a","source_fingerprint":"sha256:x","tenon_version":"v","harnesses":{"claude":{"harness_version":"` + strings.Repeat("v", MaxManifestBytes) + `","tool_runtimes":{}}}}`
	mustCode(t, mustParseErr(t, big), "manifest.too-large")
}

func mustParseErr(t *testing.T, doc string) error {
	t.Helper()
	_, err := Parse([]byte(doc))
	return err
}

// TestMarshalByteIdentical proves `tenon manifest write` reproducibility: two
// Resolves of the same fake closure encode byte-for-byte identically, even when
// the package list is supplied out of order.
func TestMarshalByteIdentical(t *testing.T) {
	pkgs := []PackageIdentity{
		{ID: "zeta", ManifestSHA256: "22"},
		{ID: "alpha", ManifestSHA256: "11"},
	}
	r := fakeClosure("2.1.240", "2.0.0", "", "1.26.5", pkgs)

	a, err := Resolve(fakeProject(), "claude", "0.1.0-dev", r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(fakeProject(), "claude", "0.1.0-dev", r)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Bytes()) != string(b.Bytes()) {
		t.Fatalf("canonical bytes differ across two resolves:\n%s\n---\n%s", a.Bytes(), b.Bytes())
	}
	if a.Identity() != b.Identity() {
		t.Fatalf("identity is not stable: %s vs %s", a.Identity(), b.Identity())
	}
	// The out-of-order package list must be sorted in the canonical encoding.
	body := string(a.Bytes())
	if strings.Index(body, "alpha") > strings.Index(body, "zeta") {
		t.Fatalf("packages must be sorted by id in canonical bytes:\n%s", body)
	}
	// A parsed manifest of the canonical bytes round-trips to the same identity.
	parsed, err := Parse(a.Bytes())
	if err != nil {
		t.Fatalf("canonical bytes do not re-parse: %v", err)
	}
	if parsed.Identity() != a.Identity() {
		t.Fatalf("identity does not round-trip: %s vs %s", parsed.Identity(), a.Identity())
	}
}

func TestVerifyPassesOnIdentical(t *testing.T) {
	r := fakeClosure("2.1.240", "2.0.0", "", "1.26.5", []PackageIdentity{{ID: "gh", ManifestSHA256: "abc"}})
	cur, _ := Resolve(fakeProject(), "claude", "0.1.0-dev", r)
	sup, _ := Resolve(fakeProject(), "claude", "0.1.0-dev", r)
	if err := Verify(sup, cur); err != nil {
		t.Fatalf("identical closures must verify: %v", err)
	}
}

func TestVerifyFailsClosedPerPin(t *testing.T) {
	base := func() Resolver {
		return fakeClosure("2.1.240", "2.0.0", "", "1.26.5", []PackageIdentity{{ID: "gh", ManifestSHA256: "abc"}})
	}
	supplied, _ := Resolve(fakeProject(), "claude", "0.1.0-dev", base())

	// source fingerprint drift
	driftFP := &agentproject.Project{Name: "my-agent", Fingerprint: "sha256:" + strings.Repeat("b", 64)}
	cur, _ := Resolve(driftFP, "claude", "0.1.0-dev", base())
	mustCode(t, Verify(supplied, cur), "manifest.drift.source-fingerprint")

	// tenon version drift
	cur, _ = Resolve(fakeProject(), "claude", "9.9.9", base())
	mustCode(t, Verify(supplied, cur), "manifest.drift.tenon-version")

	// harness version drift
	cur, _ = Resolve(fakeProject(), "claude", "0.1.0-dev", fakeClosure("9.9.9", "2.0.0", "", "1.26.5", []PackageIdentity{{ID: "gh", ManifestSHA256: "abc"}}))
	mustCode(t, Verify(supplied, cur), "manifest.drift.harness-version")

	// package manifest hash drift
	cur, _ = Resolve(fakeProject(), "claude", "0.1.0-dev", fakeClosure("2.1.240", "2.0.0", "", "1.26.5", []PackageIdentity{{ID: "gh", ManifestSHA256: "CHANGED"}}))
	mustCode(t, Verify(supplied, cur), "manifest.drift.package-hash")

	// package pinned but absent in current
	cur, _ = Resolve(fakeProject(), "claude", "0.1.0-dev", fakeClosure("2.1.240", "2.0.0", "", "1.26.5", nil))
	mustCode(t, Verify(supplied, cur), "manifest.drift.package-missing")

	// tool runtime drift
	cur, _ = Resolve(fakeProject(), "claude", "0.1.0-dev", fakeClosure("2.1.240", "2.9.9", "", "1.26.5", []PackageIdentity{{ID: "gh", ManifestSHA256: "abc"}}))
	mustCode(t, Verify(supplied, cur), "manifest.drift.tool-runtime")

	// package now selected but not pinned (present in current, absent in supplied)
	noPkg, _ := Resolve(fakeProject(), "claude", "0.1.0-dev", fakeClosure("2.1.240", "2.0.0", "", "1.26.5", nil))
	cur, _ = Resolve(fakeProject(), "claude", "0.1.0-dev", base())
	mustCode(t, Verify(noPkg, cur), "manifest.drift.package-added")

	// agent drift
	cur, _ = Resolve(&agentproject.Project{Name: "other", Fingerprint: fakeProject().Fingerprint}, "claude", "0.1.0-dev", base())
	mustCode(t, Verify(supplied, cur), "manifest.drift.agent")

	// harness missing: supplied pins no entry for the resolved harness
	curCodex, _ := Resolve(fakeProject(), "codex", "0.1.0-dev", base())
	mustCode(t, Verify(supplied, curCodex), "manifest.drift.harness-missing")
}

// TestVerifyRejectsNoHarnessClosure guards the fail-open path where a current
// closure pins no harness (a hand-built or future caller): Verify must not
// silently pass.
func TestVerifyRejectsNoHarnessClosure(t *testing.T) {
	supplied, _ := Resolve(fakeProject(), "claude", "0.1.0-dev", fakeClosure("2.1.240", "", "", "", nil))
	empty := &Manifest{Agent: supplied.Agent, SourceFingerprint: supplied.SourceFingerprint, TenonVersion: supplied.TenonVersion}
	mustCode(t, Verify(supplied, empty), "manifest.verify.no-harness")
}

// TestVerifyIgnoresModel proves the deliberate non-verification of the model
// pin: two manifests that differ only in model verify cleanly.
func TestVerifyIgnoresModel(t *testing.T) {
	r := fakeClosure("2.1.240", "", "", "", nil)
	cur, _ := Resolve(fakeProject(), "claude", "0.1.0-dev", r)
	sup, _ := Resolve(fakeProject(), "claude", "0.1.0-dev", r)
	// A supplied manifest may record a model; Resolve leaves current empty.
	pins := sup.Harnesses["claude"]
	pins.Model = "claude-opus-4-8"
	sup.Harnesses["claude"] = pins
	if err := Verify(sup, cur); err != nil {
		t.Fatalf("model must not be verified: %v", err)
	}
	// Resolve never fabricates a model.
	if cur.Harnesses["claude"].Model != "" {
		t.Fatalf("Resolve must leave model empty, got %q", cur.Harnesses["claude"].Model)
	}
}

// TestParseRejectsDuplicatePackageID guards Parse strictness: a duplicate
// package id would be collapsed last-writer-wins during Verify, dropping a pin.
func TestParseRejectsDuplicatePackageID(t *testing.T) {
	raw := []byte(`{
  "schema_version": 1,
  "agent": "a",
  "source_fingerprint": "sha256:` + strings.Repeat("a", 64) + `",
  "tenon_version": "0.1.0-dev",
  "harnesses": {
    "claude": {
      "harness_version": "2.1.240",
      "integration_packages": [
        {"id": "gh", "manifest_sha256": "abc"},
        {"id": "gh", "manifest_sha256": "def"}
      ],
      "tool_runtimes": {}
    }
  }
}`)
	_, err := Parse(raw)
	mustCode(t, err, "manifest.package.duplicate")
}
