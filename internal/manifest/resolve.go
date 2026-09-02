package manifest

import (
	"sort"

	"github.com/alee792/tenon/internal/agentproject"
)

// Resolver resolves the pieces of the current closure that live outside the
// authored directory. Every field is injected so tests build a credential-free
// closure without ever running a real harness or toolchain; production wiring
// (cmd/tenon) supplies functions that run `<harness> --version`, read the
// integration store, and inspect the tool toolchains.
type Resolver struct {
	// HarnessVersion returns the version string of the named harness executable
	// (e.g. Claude "2.1.240", Codex "0.144.1").
	HarnessVersion func(harness string) (string, error)
	// ToolRuntimes returns the Deno, uv, Go, and Python runtime pins for the
	// languages the project's tools use; a language the project does not use is
	// returned empty. python is the resolved version SPECIFICATION (see
	// ToolRuntimes.Python's doc), not the installed interpreter's exact patch
	// and ABI.
	ToolRuntimes func() (deno, uv, goVer, python string, err error)
	// PackageIdentities returns the {id, manifest_sha256} identity of every
	// integration package the project selects on the harness, in any order.
	PackageIdentities func(harness string) ([]PackageIdentity, error)
}

// Resolve builds the CURRENT closure for exactly the selected harness. The
// source fingerprint, tenon version, and agent name come from the validated
// project; the per-harness pins come from the resolver. The model pin is left
// empty: tenon does not discover or verify which model served a turn.
func Resolve(p *agentproject.Project, harness, tenonVersion string, r Resolver) (*Manifest, error) {
	if p == nil {
		return nil, errorf("pins.resolve.project", "resolving a pin set requires a loaded project")
	}
	if !harnessNames[harness] {
		return nil, errorf("pins.resolve.harness",
			"resolving a pin set requires a supported harness; got %q", harness)
	}
	if r.HarnessVersion == nil || r.ToolRuntimes == nil || r.PackageIdentities == nil {
		return nil, errorf("pins.resolve.resolver", "the pin set resolver is incomplete")
	}

	harnessVersion, err := r.HarnessVersion(harness)
	if err != nil {
		return nil, errorf("pins.resolve.harness-version",
			"the %s harness version could not be resolved: %v", harness, err)
	}
	deno, uv, goVer, python, err := r.ToolRuntimes()
	if err != nil {
		return nil, errorf("pins.resolve.tool-runtimes",
			"the authored tool runtimes could not be resolved: %v", err)
	}
	packages, err := r.PackageIdentities(harness)
	if err != nil {
		return nil, errorf("pins.resolve.packages",
			"the integration package identities could not be resolved: %v", err)
	}
	packages = append([]PackageIdentity(nil), packages...)
	sort.Slice(packages, func(i, j int) bool { return packages[i].ID < packages[j].ID })

	return &Manifest{
		SchemaVersion:     SchemaVersion,
		Agent:             p.Name,
		SourceFingerprint: p.Fingerprint,
		TenonVersion:      tenonVersion,
		Harnesses: map[string]HarnessPins{
			harness: {
				HarnessVersion:      harnessVersion,
				IntegrationPackages: packages,
				ToolRuntimes:        ToolRuntimes{Deno: deno, UV: uv, Go: goVer, Python: python},
			},
		},
	}, nil
}

// Verify compares a supplied manifest against the freshly resolved current
// closure and FAILS CLOSED naming the exact drifted pin. It compares only the
// fields the manifest pins — source fingerprint, tenon version, and, for the
// resolved harness, the harness version, each selected package's manifest hash,
// and each used tool runtime. It deliberately does NOT compare the model pin:
// tenon does not claim to verify which model served a turn. Every failure is a
// typed *Error with a stable code.
func Verify(supplied, current *Manifest) error {
	if supplied == nil || current == nil {
		return errorf("pins.verify.nil", "verification requires both a supplied and a current pin set")
	}
	if supplied.Agent != current.Agent {
		return errorf("pins.drift.agent",
			"pins agent drift: supplied %q, current %q", supplied.Agent, current.Agent)
	}
	if supplied.SourceFingerprint != current.SourceFingerprint {
		return errorf("pins.drift.source-fingerprint",
			"pins source fingerprint drift: supplied %s, current %s",
			supplied.SourceFingerprint, current.SourceFingerprint)
	}
	if supplied.TenonVersion != current.TenonVersion {
		return errorf("pins.drift.tenon-version",
			"pins tenon version drift: supplied %s, current %s",
			supplied.TenonVersion, current.TenonVersion)
	}

	// current pins exactly the one resolved harness; a current closure with no
	// harness entry would make the loop below a no-op and fail open, so guard
	// it. Resolve always injects one, so this is defense against a hand-built
	// or future caller.
	if len(current.Harnesses) == 0 {
		return errorf("pins.verify.no-harness",
			"the current closure pins no harness to verify against")
	}
	// verify the supplied manifest's entry for that same harness.
	for name, cur := range current.Harnesses {
		sup, ok := supplied.Harnesses[name]
		if !ok {
			return errorf("pins.drift.harness-missing",
				"the supplied pins do not pin harness %q, which the current closure resolves", name)
		}
		if err := verifyHarness(name, sup, cur); err != nil {
			return err
		}
	}
	return nil
}

// verifyHarness compares one harness's supplied pins against the current pins.
// Model is ignored by design.
func verifyHarness(name string, sup, cur HarnessPins) error {
	if sup.HarnessVersion != cur.HarnessVersion {
		return errorf("pins.drift.harness-version",
			"pins harness %q version drift: supplied %s, current %s",
			name, sup.HarnessVersion, cur.HarnessVersion)
	}
	if err := verifyPackages(name, sup.IntegrationPackages, cur.IntegrationPackages); err != nil {
		return err
	}
	return verifyRuntimes(name, sup.ToolRuntimes, cur.ToolRuntimes)
}

// verifyPackages compares the selected integration packages by id. A package
// pinned but absent from the current closure, a package present now but not
// pinned, and a changed manifest hash are each a distinct, bounded drift.
func verifyPackages(name string, supplied, current []PackageIdentity) error {
	suppliedByID := map[string]string{}
	for _, p := range supplied {
		suppliedByID[p.ID] = p.ManifestSHA256
	}
	currentByID := map[string]string{}
	for _, p := range current {
		currentByID[p.ID] = p.ManifestSHA256
	}
	for _, p := range sortedIDs(suppliedByID) {
		curHash, ok := currentByID[p]
		if !ok {
			return errorf("pins.drift.package-missing",
				"pins harness %q names package %q, which the current closure no longer selects", name, p)
		}
		if suppliedByID[p] != curHash {
			return errorf("pins.drift.package-hash",
				"pins harness %q package %q manifest hash drift: supplied %s, current %s",
				name, p, suppliedByID[p], curHash)
		}
	}
	for _, p := range sortedIDs(currentByID) {
		if _, ok := suppliedByID[p]; !ok {
			return errorf("pins.drift.package-added",
				"pins harness %q does not name package %q, which the current closure now selects", name, p)
		}
	}
	return nil
}

// verifyRuntimes compares each tool runtime pin. Only a differing value drifts;
// an unused (empty) language matches an unused one.
func verifyRuntimes(name string, sup, cur ToolRuntimes) error {
	for _, rt := range []struct {
		lang     string
		supplied string
		current  string
	}{
		{"deno", sup.Deno, cur.Deno},
		{"uv", sup.UV, cur.UV},
		{"go", sup.Go, cur.Go},
		{"python", sup.Python, cur.Python},
	} {
		if rt.supplied != rt.current {
			return errorf("pins.drift.tool-runtime",
				"pins harness %q %s runtime drift: supplied %q, current %q",
				name, rt.lang, rt.supplied, rt.current)
		}
	}
	return nil
}

func sortedIDs(m map[string]string) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
