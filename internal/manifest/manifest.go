// Package manifest is the supplied agent manifest: the bounded, closed document
// that PINS the runtime closure the authored directory alone cannot express
// (the harness executable version, the integration packages a project selects,
// and the authored-tool runtime versions). A manifest belongs to APPLICATION,
// not to the definition: it is supplied to validate, apply, run, and every
// tenon-owned process open, never stored inside agent source. It IDENTIFIES and
// PINS; it never lists components — the authored directory stays the sole
// registry (see docs/product-spec.md "Agent manifest").
//
// This package parses and canonicalizes manifest bytes, resolves the CURRENT
// closure through an injectable Resolver (so tests never call a real
// harness/toolchain), and verifies a supplied manifest against the current
// closure, failing closed and naming the exact drifted pin.
//
// Model note: the manifest carries an OPTIONAL model field. Tenon does not
// verify which model actually served a turn (the harness owns model selection),
// so Verify deliberately ignores model and Resolve leaves it empty. The model
// is operator-supplied (manifest write --model); this package neither fabricates
// a model nor runs a billed turn to discover one. Emitting a pinned model into
// generated harness configuration is done by the harness drivers, not here
// (ADR 0020).
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// MaxManifestBytes bounds one supplied manifest (ADR 0013 / the bounds table:
// manifest at most 32 KiB). A larger document is refused before it is decoded.
const MaxManifestBytes = 32 * 1024

// SchemaVersion is the only manifest schema version this package emits or
// accepts.
const SchemaVersion = 1

// harnessNames are the only harness keys a manifest may pin; an unknown key is
// refused so a manifest cannot silently pin a harness tenon does not compile.
var harnessNames = map[string]bool{"claude": true, "codex": true}

// Manifest is a parsed, closed agent manifest. Its fields are the axes of
// variation a manifest pins; unknown fields are rejected at Parse.
type Manifest struct {
	SchemaVersion     int                    `json:"schema_version"`
	Agent             string                 `json:"agent"`
	SourceFingerprint string                 `json:"source_fingerprint"`
	TenonVersion      string                 `json:"tenon_version"`
	Harnesses         map[string]HarnessPins `json:"harnesses"`
}

// HarnessPins is one harness's pinned closure. Model is optional and
// deliberately unverified; it is recorded but never compared (see the package
// and model notes).
type HarnessPins struct {
	HarnessVersion      string            `json:"harness_version"`
	Model               string            `json:"model,omitempty"`
	IntegrationPackages []PackageIdentity `json:"integration_packages,omitempty"`
	ToolRuntimes        ToolRuntimes      `json:"tool_runtimes"`
}

// PackageIdentity pins one integration package the project selects: its stable
// id and the SHA-256 of the installed package manifest.
type PackageIdentity struct {
	ID             string `json:"id"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

// ToolRuntimes pins the authored-tool runtime versions for the languages the
// project's tools actually use. A language the project does not use stays empty
// and is omitted from the canonical encoding.
//
// Python is the resolved Python version SPECIFICATION a project's own pin
// names (a `.python-version` file's exact pin, or the floor of
// pyproject.toml's `requires-python` range) — not the exact interpreter
// patch and ABI `uv python install` resolves it to at preparation (for
// example "cpython-3.11.13-linux-x86_64-gnu"). Per ADR 0021 that fuller
// identity belongs on the resolved closure, and the staged artifact
// manifest already carries it once preparation has actually run
// (internal/stage.RuntimeInfo.Interpreters); it is not available here
// because a supplied manifest is verified before any workspace mutation —
// before tool preparation ever runs — so this pin cannot yet read what
// preparation will install. What it can and does catch as drift is the
// project's own pin changing.
type ToolRuntimes struct {
	Deno   string `json:"deno,omitempty"`
	UV     string `json:"uv,omitempty"`
	Go     string `json:"go,omitempty"`
	Python string `json:"python,omitempty"`
}

// Error is a typed manifest error carrying a stable dotted code and a bounded
// message, so callers and tests can match failures without parsing prose.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Parse strictly decodes and validates one supplied manifest. It rejects an
// over-bound document, unknown fields, an unsupported schema version, a missing
// required pin, and an unknown harness key. Every failure is a typed *Error with
// a stable code.
func Parse(data []byte) (*Manifest, error) {
	if len(data) > MaxManifestBytes {
		return nil, errorf("pins.too-large",
			"a pin set may contain at most %d bytes; found %d", MaxManifestBytes, len(data))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, errorf("pins.invalid", "the pin set is not a valid closed pins document: %v", err)
	}
	if dec.More() {
		return nil, errorf("pins.invalid", "the pin set must be exactly one JSON document")
	}
	if m.SchemaVersion != SchemaVersion {
		return nil, errorf("pins.schema-version",
			"the pin set schema_version must be %d; found %d", SchemaVersion, m.SchemaVersion)
	}
	if m.Agent == "" {
		return nil, errorf("pins.agent.missing", "the pin set must carry a non-empty agent name")
	}
	if !strings.HasPrefix(m.SourceFingerprint, "sha256:") {
		return nil, errorf("pins.source-fingerprint.invalid",
			"the pin set source_fingerprint must be a \"sha256:\" identity")
	}
	if m.TenonVersion == "" {
		return nil, errorf("pins.tenon-version.missing", "the pin set must carry a non-empty tenon_version")
	}
	if len(m.Harnesses) == 0 {
		return nil, errorf("pins.harnesses.missing", "the pin set must pin at least one harness")
	}
	for name, pins := range m.Harnesses {
		if !harnessNames[name] {
			return nil, errorf("pins.harness.unknown",
				"the pin set names an unknown harness %q; only claude and codex are supported", name)
		}
		if pins.HarnessVersion == "" {
			return nil, errorf("pins.harness-version.missing",
				"the pin set entry for harness %q must carry a non-empty harness_version", name)
		}
		seen := make(map[string]bool, len(pins.IntegrationPackages))
		for _, pkg := range pins.IntegrationPackages {
			if pkg.ID == "" || pkg.ManifestSHA256 == "" {
				return nil, errorf("pins.package.invalid",
					"each integration package for harness %q must carry a non-empty id and manifest_sha256", name)
			}
			// A duplicate id would be collapsed last-writer-wins during Verify,
			// silently dropping one pin; reject it here where strictness lives.
			if seen[pkg.ID] {
				return nil, errorf("pins.package.duplicate",
					"harness %q pins integration package %q more than once", name, pkg.ID)
			}
			seen[pkg.ID] = true
		}
	}
	return &m, nil
}

// Bytes returns the deterministic canonical encoding of the manifest: sorted
// keys, stable field order, no timestamps. Two manifests describing the same
// closure encode BYTE-IDENTICALLY, which is what makes `tenon check --write-pins`
// reproducible and Identity stable.
func (m *Manifest) Bytes() []byte {
	// Canonicalize the package lists so a caller that built them out of order
	// still encodes identically. json.Marshal already sorts map keys, so the
	// per-harness map needs no explicit ordering.
	clone := *m
	clone.Harnesses = make(map[string]HarnessPins, len(m.Harnesses))
	for name, pins := range m.Harnesses {
		pkgs := append([]PackageIdentity(nil), pins.IntegrationPackages...)
		sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ID < pkgs[j].ID })
		pins.IntegrationPackages = pkgs
		clone.Harnesses[name] = pins
	}
	out, err := json.MarshalIndent(clone, "", "  ")
	if err != nil {
		// The manifest is a closed struct of strings and ints; marshaling it
		// cannot fail. A nil return would only surface as an empty identity.
		return nil
	}
	return append(out, '\n')
}

// Identity is a stable content identity of the manifest: the SHA-256 over its
// canonical Bytes, as a "sha256:" string. It is the provenance join key an apply
// record and dispatch wire events carry when a manifest is supplied; it carries
// no pin, fingerprint, or model value on its own.
func (m *Manifest) Identity() string {
	sum := sha256.Sum256(m.Bytes())
	return fmt.Sprintf("sha256:%x", sum)
}
