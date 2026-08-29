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
	// sources, when non-nil, records the source each rev was "cached" under;
	// Resolve fails with a source-mismatch error when the caller's source
	// disagrees, mirroring internal/pluginref.Cache.Resolve.
	sources map[string]string
}

func (f *fakePluginCache) Resolve(source, rev string) (string, error) {
	if err, ok := f.errs[rev]; ok {
		return "", err
	}
	if f.sources != nil {
		if cached, ok := f.sources[rev]; ok && cached != source {
			return "", fmt.Errorf("pluginref.source.mismatch: rev %s is cached from a different source (%s) than declared (%s)", rev, cached, source)
		}
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

// TestPluginReferenceSourceMismatchFailsClosed proves Load fails when the
// declared reference's source disagrees with the source the cache recorded
// for the same already-cached rev (Part C, PluginCache source threading): a
// rev is content-addressed, not source-addressed, so this is exactly the
// swap Resolve's source parameter exists to catch, independent of any
// digest check.
func TestPluginReferenceSourceMismatchFailsClosed(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/renamed-plugin", validRev, "")
	cachedRoot := newCachedPluginTree(t, "observability", "telemetry")
	withPluginCache(t, &fakePluginCache{
		roots:   map[string]string{validRev: cachedRoot},
		sources: map[string]string{validRev: "https://github.com/acme/observability-plugin"},
	})

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("expected Load to fail when the cached rev's recorded source disagrees with the declared source")
	}
	requireErrorID(t, diags, "plugin.reference.unresolved")
	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.reference.unresolved" && strings.Contains(d.Rule, "different source") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the unresolved diagnostic to name the source mismatch, got %v", diags.All())
	}
}

// copyPluginTree copies a resolved plugin tree into plugins/<name>/ under an
// agent root, exactly as `tenon stage` materializes one (issue #58).
func copyPluginTree(t *testing.T, src, root, name string) {
	t.Helper()
	dest := filepath.Join(root, "plugins", name)
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestMaterializedPluginReferenceLoadsFromAdjacentDirectory proves the
// materialized-reference shape a staged tree carries (issue #58): a
// plugins/<name>.md reference with its pinned content beside it at
// plugins/<name>/ is one plugin, not a collision, and loads entirely
// offline — no plugin cache is configured at all, which is exactly the
// container's situation when `tenon stage verify` re-loads the staged tree.
func TestMaterializedPluginReferenceLoadsFromAdjacentDirectory(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "")
	copyPluginTree(t, newCachedPluginTree(t, "observability", "telemetry"), root, "obs")

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Skills) != 1 || p.Skills[0].Name != "telemetry" {
		t.Fatalf("expected one skill named telemetry, got %+v", p.Skills)
	}
	// The authored root stays the reference's synthetic form, never the
	// vendored plugins/obs/ path: that is what keeps the fingerprint
	// identical to the cache-resolved load the same reference produced
	// before staging materialized it.
	wantSource := "plugins/obs.md -> " + validRev + "/skills/telemetry"
	if p.Skills[0].SourcePath != wantSource {
		t.Fatalf("skill source path = %q, want %q", p.Skills[0].SourcePath, wantSource)
	}
}

// TestMaterializedPluginReferenceFingerprintMatchesCacheResolved proves the
// property the staged tree's verification depends on (issue #58 blocker):
// the same reference over the same content fingerprints identically whether
// its content was resolved from the plugin cache (build time) or from the
// materialized directory beside it (a re-load of the staged tree).
func TestMaterializedPluginReferenceFingerprintMatchesCacheResolved(t *testing.T) {
	content := newCachedPluginTree(t, "observability", "telemetry")

	cached := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, cached, "obs", "https://github.com/acme/observability-plugin", validRev, "Notes.")
	withPluginCache(t, &fakePluginCache{roots: map[string]string{validRev: content}})
	fromCache, diags, err := Load(cached)
	if err != nil || fromCache == nil || diags.HasErrors() {
		t.Fatalf("cache-resolved load failed: err=%v diags=%v", err, diags.All())
	}

	materialized := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, materialized, "obs", "https://github.com/acme/observability-plugin", validRev, "Notes.")
	copyPluginTree(t, content, materialized, "obs")
	ConfigurePluginCache(nil)
	fromTree, diags, err := Load(materialized)
	if err != nil || fromTree == nil || diags.HasErrors() {
		t.Fatalf("materialized load failed: err=%v diags=%v", err, diags.All())
	}

	if fromCache.Fingerprint != fromTree.Fingerprint {
		t.Fatalf("fingerprint from cache %s must equal fingerprint from the materialized tree %s",
			fromCache.Fingerprint, fromTree.Fingerprint)
	}
}

// TestMaterializedPluginReferenceWinsOverCache proves the precedence rule:
// with content materialized beside the reference, the cache is not consulted
// at all — deterministic and offline-first, so a staged tree never depends on
// whatever the operator's cache happens to hold for the same pin.
func TestMaterializedPluginReferenceWinsOverCache(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "")
	copyPluginTree(t, newCachedPluginTree(t, "observability", "telemetry"), root, "obs")
	withPluginCache(t, &fakePluginCache{roots: map[string]string{validRev: newCachedPluginTree(t, "observability", "other")}})

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Skills) != 1 || p.Skills[0].Name != "telemetry" {
		t.Fatalf("the materialized content must win over the cache, got %+v", p.Skills)
	}
}

// TestMaterializedPluginReferenceServersAreVendored proves a materialized
// reference's declared servers re-anchor like a vendored plugin's: the plugin
// root IS plugins/<name>/ inside the agent tree, so ResolveServers must
// recompute PLUGIN_ROOT against the root it renders for rather than trust a
// Load-time absolute path.
func TestMaterializedPluginReferenceServersAreVendored(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginReference(t, root, "obs", "https://github.com/acme/observability-plugin", validRev, "")
	content := newCachedPluginTree(t, "observability", "telemetry")
	if err := os.WriteFile(filepath.Join(content, "mcp.json"),
		[]byte(`{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {`+
			`"telemetry": {"command": "server"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	copyPluginTree(t, content, root, "obs")

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.PluginServers) != 1 || !p.PluginServers[0].Vendored {
		t.Fatalf("expected one vendored plugin server, got %+v", p.PluginServers)
	}
	resolved := ResolveServers(p.PluginServers, "/opt/tenon/agents/agent", "/workspace", p.Name)
	if got := resolved[0].Env["PLUGIN_ROOT"]; got != "/opt/tenon/agents/agent/plugins/obs" {
		t.Fatalf("PLUGIN_ROOT = %q, want the in-tree materialized root", got)
	}
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
