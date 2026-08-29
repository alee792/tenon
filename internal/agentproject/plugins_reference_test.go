package agentproject

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePluginCache is a minimal in-memory PluginCache for tests that never
// need a real fetch: it maps a rev directly to an already-materialized
// directory on disk (or to a canned error), exercising exactly the
// interface Load calls.
type fakePluginCache struct {
	roots map[string]string
	errs  map[string]error
}

func (f *fakePluginCache) Resolve(rev string) (string, error) {
	if err, ok := f.errs[rev]; ok {
		return "", err
	}
	root, ok := f.roots[rev]
	if !ok {
		return "", fmt.Errorf("no cache entry for rev %s", rev)
	}
	return root, nil
}

// withPluginCache installs cache for the duration of one test and restores
// the fail-closed default afterward, since ConfigurePluginCache is a
// package-level seam.
func withPluginCache(t *testing.T, cache PluginCache) {
	t.Helper()
	ConfigurePluginCache(cache)
	t.Cleanup(func() { ConfigurePluginCache(nil) })
}

const validRev = "0123456789abcdef0123456789abcdef01234567"
const validRev2 = "fedcba9876543210fedcba9876543210fedcba98"

func writePluginReference(t *testing.T, root, name, source, rev, body string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("source: " + source + "\n")
	b.WriteString("rev: " + rev + "\n")
	b.WriteString("---\n")
	if body != "" {
		b.WriteString("\n" + body + "\n")
	}
	writeSkillFile(t, root, "plugins/"+name+".md", []byte(b.String()), 0o644)
}

// newCachedPluginTree materializes a minimal valid plugin package (manifest
// plus one skill) under a fresh directory, standing in for a resolved
// pluginref cache tree.
func newCachedPluginTree(t *testing.T, pluginName, skillName string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(validPluginJSON(pluginName)), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, "skills", skillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(minimalSkillMD(skillName)), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPluginReferenceOfflineFailureWithNoCacheConfigured(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "")

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected Load to fail with no plugin cache configured")
	}
	requireErrorID(t, diags, "plugin.reference.unresolved")
}

func TestPluginReferenceResolvesThroughCache(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "Provenance notes.")
	cachedRoot := newCachedPluginTree(t, "observability", "telemetry")
	withPluginCache(t, &fakePluginCache{roots: map[string]string{validRev: cachedRoot}})

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Skills) != 1 || p.Skills[0].Name != "telemetry" {
		t.Fatalf("expected one resolved skill named telemetry, got %+v", p.Skills)
	}
	wantSource := "plugins/obs.md -> " + validRev + "/skills/telemetry"
	if p.Skills[0].SourcePath != wantSource {
		t.Fatalf("skill source path = %q, want %q", p.Skills[0].SourcePath, wantSource)
	}
}

func TestPluginReferenceUnresolvedNamesFetchCommand(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "")
	withPluginCache(t, &fakePluginCache{errs: map[string]error{validRev: fmt.Errorf("no cache entry")}})

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected Load to fail when the cache cannot resolve the pin")
	}
	requireErrorID(t, diags, "plugin.reference.unresolved")
	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.reference.unresolved" && strings.Contains(d.Rule, "tenon plugin fetch") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the unresolved diagnostic to name `tenon plugin fetch`, got %v", diags.All())
	}
}

func TestPluginReferenceDigestMismatchFailsClosed(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "")
	withPluginCache(t, &fakePluginCache{errs: map[string]error{validRev: fmt.Errorf("pluginref.digest.mismatch: cached tree no longer matches its recorded digest")}})

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected Load to fail on a digest mismatch")
	}
	requireErrorID(t, diags, "plugin.reference.unresolved")
}

func TestPluginReferenceCollidesWithVendoredDirectory(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "obs", validPluginJSON("obs"))
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "")
	withPluginCache(t, &fakePluginCache{roots: map[string]string{validRev: newCachedPluginTree(t, "obs", "x")}})

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected a name collision between a reference and a vendored directory to fail the project")
	}
	requireErrorID(t, diags, "plugin.entry.collision")
}

// TestPluginReferenceNameEscapeRejected proves a reference filename that
// derives a name outside the plugin storage grammar — in particular "..",
// which filepath.Join's cleaning would walk PluginDataDir's per-plugin data
// directory straight out of its intended tree — is rejected with
// plugin.entry.invalid before ever reaching the plugin cache (#52 review
// finding 2).
func TestPluginReferenceNameEscapeRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "..", "https://github.com/acme/x", validRev, "")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "plugin.entry.invalid")
}

func TestPluginReferenceFingerprintSensitiveToOwnBytes(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "Notes A.")
	cachedRoot := newCachedPluginTree(t, "observability", "telemetry")
	withPluginCache(t, &fakePluginCache{roots: map[string]string{validRev: cachedRoot}})

	p1, diags, err := Load(root)
	if err != nil || p1 == nil || diags.HasErrors() {
		t.Fatalf("first load failed: err=%v diags=%v", err, diags.All())
	}

	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "Notes B, different.")
	p2, diags, err := Load(root)
	if err != nil || p2 == nil || diags.HasErrors() {
		t.Fatalf("second load failed: err=%v diags=%v", err, diags.All())
	}
	if p1.Fingerprint == p2.Fingerprint {
		t.Fatalf("fingerprint did not change when the reference file's own bytes changed")
	}
}

func TestPluginReferenceFingerprintSensitiveToCachedTreeBytes(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "")

	withPluginCache(t, &fakePluginCache{roots: map[string]string{validRev: newCachedPluginTree(t, "observability", "telemetry")}})
	p1, diags, err := Load(root)
	if err != nil || p1 == nil || diags.HasErrors() {
		t.Fatalf("first load failed: err=%v diags=%v", err, diags.All())
	}

	// Same rev, but the cache now resolves to a tree with different skill
	// content: a real cache never does this for one rev (Verify would
	// refuse it), but Load's fingerprint sensitivity to resolved bytes is
	// independent of that guarantee and worth proving directly.
	altRoot := newCachedPluginTree(t, "observability", "telemetry")
	if err := os.WriteFile(filepath.Join(altRoot, "skills", "telemetry", "SKILL.md"), []byte(minimalSkillMD("telemetry")+"\nExtra line.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ConfigurePluginCache(&fakePluginCache{roots: map[string]string{validRev: altRoot}})
	p2, diags, err := Load(root)
	if err != nil || p2 == nil || diags.HasErrors() {
		t.Fatalf("second load failed: err=%v diags=%v", err, diags.All())
	}
	if p1.Fingerprint == p2.Fingerprint {
		t.Fatalf("fingerprint did not change when the resolved cached tree's bytes changed")
	}
}

// TestPluginReferenceFingerprintIndependentOfCacheBase proves the same
// plugin-reference cached tree, materialized under two different cache base
// paths, produces byte-identical fingerprints (ADR 0025's cache-base
// independence, the deferred test named by the #52 review): the fingerprint
// depends only on declared source and resolved content, never on where the
// cache happens to live on disk.
func TestPluginReferenceFingerprintIndependentOfCacheBase(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "")

	base1 := newCachedPluginTree(t, "observability", "telemetry")
	withPluginCache(t, &fakePluginCache{roots: map[string]string{validRev: base1}})
	p1, diags, err := Load(root)
	if err != nil || p1 == nil || diags.HasErrors() {
		t.Fatalf("first load failed: err=%v diags=%v", err, diags.All())
	}

	// A second, distinct cache base directory materializing byte-identical
	// content: only the absolute path differs.
	base2 := newCachedPluginTree(t, "observability", "telemetry")
	if base1 == base2 {
		t.Fatal("test setup: expected two distinct cache base directories")
	}
	ConfigurePluginCache(&fakePluginCache{roots: map[string]string{validRev: base2}})
	p2, diags, err := Load(root)
	if err != nil || p2 == nil || diags.HasErrors() {
		t.Fatalf("second load failed: err=%v diags=%v", err, diags.All())
	}

	if p1.Fingerprint != p2.Fingerprint {
		t.Fatalf("fingerprint must be independent of the cache base path: %s vs %s", p1.Fingerprint, p2.Fingerprint)
	}
}

func TestPluginReferenceValidation(t *testing.T) {
	cases := map[string]struct {
		source, rev string
		wantID      string
	}{
		"non-https source":  {"http://github.com/acme/x", validRev, "plugin.reference.source.invalid"},
		"source with query": {"https://github.com/acme/x?ref=1", validRev, "plugin.reference.source.invalid"},
		"short rev":         {"https://github.com/acme/x", "abcdef", "plugin.reference.rev.invalid"},
		"uppercase rev":     {"https://github.com/acme/x", strings.ToUpper(validRev), "plugin.reference.rev.invalid"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writePluginReference(t, root, "obs", tc.source, tc.rev, "")
			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Fatalf("expected Load to fail")
			}
			requireErrorID(t, diags, tc.wantID)
		})
	}
}

func TestPluginReferenceUnknownFieldRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	content := "---\nsource: https://github.com/acme/x\nrev: " + validRev + "\nextra: true\n---\n"
	writeSkillFile(t, root, "plugins/obs.md", []byte(content), 0o644)
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected Load to fail on an unknown frontmatter field")
	}
	requireErrorID(t, diags, "plugin.reference.frontmatter.unknown-field")
}

func TestPluginReferenceBodyTooLongRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/x", validRev, strings.Repeat("a", MaxPluginReferenceBodyRunes+1))
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected Load to fail when the body exceeds the bound")
	}
	requireErrorID(t, diags, "plugin.reference.body.too-long")
}

func TestLoadPluginReferencesForStatusIndependentOfOthers(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "good", "https://github.com/acme/good", validRev, "")
	content := "---\nsource: not-a-url\nrev: bad\n---\n"
	writeSkillFile(t, root, "plugins/bad.md", []byte(content), 0o644)

	refs, diags, err := LoadPluginReferencesForStatus(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Name != "good" {
		t.Fatalf("expected exactly the good reference to be reported, got %+v", refs)
	}
	requireErrorID(t, diags, "plugin.reference.source.invalid")
}
