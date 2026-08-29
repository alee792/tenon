package agentproject

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/diagnostics"
)

// validPluginJSON returns a minimal valid Agent Plugins v1.0.0 manifest.
func validPluginJSON(name string) string {
	return fmt.Sprintf(`{"$schema": %q, "name": %q}`, pluginSchemaID, name)
}

func writePluginManifest(t *testing.T, root, pluginDir, content string) {
	t.Helper()
	writeSkillFile(t, root, "plugins/"+pluginDir+"/plugin.json", []byte(content), 0o644)
}

func warningIDs(diags *diagnostics.List) []string {
	var ids []string
	for _, d := range diags.All() {
		if d.Severity == diagnostics.Warning {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

func requireWarningID(t *testing.T, diags *diagnostics.List, id string) {
	t.Helper()
	for _, got := range warningIDs(diags) {
		if got == id {
			return
		}
	}
	t.Fatalf("expected warning diagnostic %q, got %v", id, diags.All())
}

// TestLoadPluginVendoredDirNameGrammar proves a vendored plugins/ directory
// name is validated against the same component grammar a plugin reference's
// derived name is (#52 review finding 2): lowercase hyphenated words only,
// so an uppercase letter or an underscore is rejected with
// plugin.entry.invalid rather than silently becoming the plugin storage name
// baked into PluginDataDir.
func TestLoadPluginVendoredDirNameGrammar(t *testing.T) {
	cases := []string{"Vendor-X", "vendor_x", "-vendor", "vendor-"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writePluginManifest(t, root, name, validPluginJSON(name))
			_, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			requireErrorID(t, diags, "plugin.entry.invalid")
		})
	}
}

// TestLoadPluginImportsSkillsInOrder proves a valid plugin's skills import,
// with plugin skill directories loaded in lexical order.
func TestLoadPluginImportsSkillsInOrder(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writeSkillFile(t, root, "plugins/vendor-x/skills/bravo/SKILL.md", []byte(minimalSkillMD("bravo")), 0o644)
	writeSkillFile(t, root, "plugins/vendor-x/skills/alpha/SKILL.md", []byte(minimalSkillMD("alpha")), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	var names, sources []string
	for _, s := range p.Skills {
		names = append(names, s.Name)
		sources = append(sources, s.SourcePath)
	}
	// Project.Skills is sorted by name regardless of import order.
	wantNames := []string{"alpha", "bravo"}
	if names[0] != wantNames[0] || names[1] != wantNames[1] {
		t.Fatalf("names = %v, want %v", names, wantNames)
	}
	wantSources := map[string]string{
		"alpha": "plugins/vendor-x/skills/alpha",
		"bravo": "plugins/vendor-x/skills/bravo",
	}
	for _, s := range p.Skills {
		if s.SourcePath != wantSources[s.Name] {
			t.Fatalf("skill %q source = %q, want %q", s.Name, s.SourcePath, wantSources[s.Name])
		}
	}
	_ = sources
}

// TestLoadPluginRootVsPluginCollisionKeepsRoot proves root skills always win
// a name collision against an imported plugin skill, with a warning naming
// both authored paths.
func TestLoadPluginRootVsPluginCollisionKeepsRoot(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	rootMD := "---\nname: echo\ndescription: The root echo skill.\n---\n\nRoot body.\n"
	writeSkillFile(t, root, "skills/echo/SKILL.md", []byte(rootMD), 0o644)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writeSkillFile(t, root, "plugins/vendor-x/skills/echo/SKILL.md", []byte(minimalSkillMD("echo")), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Skills) != 1 {
		t.Fatalf("skills = %+v, want exactly the root skill", p.Skills)
	}
	if p.Skills[0].SourcePath != "skills/echo" {
		t.Fatalf("winning skill source = %q, want the root skill", p.Skills[0].SourcePath)
	}
	if string(p.Skills[0].Files[0].Content) != rootMD {
		t.Fatalf("winning skill content must be the root skill's bytes")
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.skill.collision" && d.Severity == diagnostics.Warning &&
			d.Path == "plugins/vendor-x/skills/echo" &&
			strings.Contains(d.Rule, "skills/echo") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plugin.skill.collision naming both authored paths, got %v", diags.All())
	}
}

// TestLoadPluginVsPluginCollisionKeepsLexicalFirst proves that among
// colliding plugin skills, the lexically first plugin directory wins.
func TestLoadPluginVsPluginCollisionKeepsLexicalFirst(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "aaa-plugin", validPluginJSON("aaa-plugin"))
	writeSkillFile(t, root, "plugins/aaa-plugin/skills/shared/SKILL.md", []byte(minimalSkillMD("shared")), 0o644)
	writePluginManifest(t, root, "zzz-plugin", validPluginJSON("zzz-plugin"))
	writeSkillFile(t, root, "plugins/zzz-plugin/skills/shared/SKILL.md", []byte(minimalSkillMD("shared")), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(p.Skills) != 1 {
		t.Fatalf("skills = %+v, want exactly one merged skill", p.Skills)
	}
	if p.Skills[0].SourcePath != "plugins/aaa-plugin/skills/shared" {
		t.Fatalf("winning skill source = %q, want the lexically first plugin", p.Skills[0].SourcePath)
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.skill.collision" && d.Path == "plugins/zzz-plugin/skills/shared" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the later plugin's skill to be named in the collision warning, got %v", diags.All())
	}
}

// TestLoadPluginInvalidManifestSkipsOnlyThatPlugin proves manifest
// violations warn and skip only the offending plugin, while a valid sibling
// plugin's skills still import.
func TestLoadPluginInvalidManifestSkipsOnlyThatPlugin(t *testing.T) {
	cases := map[string]string{
		"bad json":     `{"$schema": ` + `"` + pluginSchemaID + `", "name": "bad"`, // truncated
		"wrong schema": `{"$schema": "https://example.com/other.json", "name": "bad"}`,
		"missing name": `{"$schema": "` + pluginSchemaID + `"}`,
	}
	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			writePluginManifest(t, root, "broken", manifest)
			writeSkillFile(t, root, "plugins/broken/skills/orphan/SKILL.md", []byte(minimalSkillMD("orphan")), 0o644)
			writePluginManifest(t, root, "good", validPluginJSON("good"))
			writeSkillFile(t, root, "plugins/good/skills/fine/SKILL.md", []byte(minimalSkillMD("fine")), 0o644)

			p, diags, err := Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if p == nil || diags.HasErrors() {
				t.Fatalf("a plugin manifest violation must not fail the project: %v", diags.All())
			}
			requireWarningID(t, diags, "plugin.manifest.invalid")
			if len(p.Skills) != 1 || p.Skills[0].Name != "fine" {
				t.Fatalf("skills = %+v, want only the valid sibling plugin's skill", p.Skills)
			}
		})
	}
}

// TestLoadPluginNonObjectExtensionsWarnsAndContinues proves a non-object
// extensions value is ignored with a warning while the plugin remains valid.
func TestLoadPluginNonObjectExtensionsWarnsAndContinues(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	manifest := fmt.Sprintf(`{"$schema": %q, "name": "vendor-x", "extensions": "nope"}`, pluginSchemaID)
	writePluginManifest(t, root, "vendor-x", manifest)
	writeSkillFile(t, root, "plugins/vendor-x/skills/one/SKILL.md", []byte(minimalSkillMD("one")), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	requireWarningID(t, diags, "plugin.extension.unsupported")
	if len(p.Skills) != 1 || p.Skills[0].Name != "one" {
		t.Fatalf("skills = %+v, want the plugin's skill still imported", p.Skills)
	}
}

// TestLoadPluginUnsupportedExtensionNamespaceWarns proves each extension
// namespace key is warned individually while the plugin remains valid.
func TestLoadPluginUnsupportedExtensionNamespaceWarns(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	manifest := fmt.Sprintf(`{"$schema": %q, "name": "vendor-x", "extensions": {"mcp": {}, "hooks": {}}}`, pluginSchemaID)
	writePluginManifest(t, root, "vendor-x", manifest)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	got := 0
	for _, d := range diags.All() {
		if d.ID == "plugin.extension.unsupported" {
			got++
		}
	}
	if got != 2 {
		t.Fatalf("expected one warning per unsupported namespace, got %d: %v", got, diags.All())
	}
}

// TestLoadPluginUnknownTopLevelFieldWarns proves an unrecognized top-level
// manifest field is ignored with a warning.
func TestLoadPluginUnknownTopLevelFieldWarns(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	manifest := fmt.Sprintf(`{"$schema": %q, "name": "vendor-x", "temperature": 1}`, pluginSchemaID)
	writePluginManifest(t, root, "vendor-x", manifest)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	requireWarningID(t, diags, "plugin.manifest.unknown-field")
}

// TestLoadPluginInvalidSkillSkipsOnlyThatSkill proves an invalid SKILL.md
// inside a plugin is skipped with a warning while a sibling skill imports.
func TestLoadPluginInvalidSkillSkipsOnlyThatSkill(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
	writeSkillFile(t, root, "plugins/vendor-x/skills/broken/SKILL.md", []byte("no frontmatter\n"), 0o644)
	writeSkillFile(t, root, "plugins/vendor-x/skills/fine/SKILL.md", []byte(minimalSkillMD("fine")), 0o644)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("an invalid plugin skill must not fail the project: %v", diags.All())
	}
	if len(p.Skills) != 1 || p.Skills[0].Name != "fine" {
		t.Fatalf("skills = %+v, want only the sibling skill", p.Skills)
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.skill.invalid" && d.Severity == diagnostics.Warning &&
			strings.HasPrefix(d.Path, "plugins/vendor-x/skills/broken") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plugin.skill.invalid at the broken skill's authored path, got %v", diags.All())
	}
}

// TestLoadPluginSymlinkedEntriesRejectedOrSkipped proves a symlinked plugin
// directory is a project-rejecting error (like a symlinked root skill
// directory), while a symlinked skill directory inside a valid plugin is
// isolated as a warning.
func TestLoadPluginSymlinkedEntriesRejectedOrSkipped(t *testing.T) {
	t.Run("plugin directory", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		real := t.TempDir()
		writeSkillFile(t, real, "plugin.json", []byte(validPluginJSON("vendor-x")), 0o644)
		if err := os.MkdirAll(filepath.Join(root, "plugins"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(root, "plugins", "vendor-x")); err != nil {
			t.Fatal(err)
		}
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Fatal("expected refusal: a symlinked plugins entry must reject the project")
		}
		requireErrorID(t, diags, "plugin.entry.invalid")
	})
	t.Run("skill directory", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
		real := t.TempDir()
		if err := os.WriteFile(filepath.Join(real, "SKILL.md"), []byte(minimalSkillMD("linked")), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "plugins", "vendor-x", "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(root, "plugins", "vendor-x", "skills", "linked")); err != nil {
			t.Fatal(err)
		}
		writeSkillFile(t, root, "plugins/vendor-x/skills/fine/SKILL.md", []byte(minimalSkillMD("fine")), 0o644)
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || diags.HasErrors() {
			t.Fatalf("a symlinked plugin skill entry must warn and skip, not fail: %v", diags.All())
		}
		requireWarningID(t, diags, "plugin.skill.invalid")
		if len(p.Skills) != 1 || p.Skills[0].Name != "fine" {
			t.Fatalf("skills = %+v, want only the sibling skill", p.Skills)
		}
	})
}

// TestLoadPluginMissingAndEmptyLocationsProduceNoDiagnostics proves missing
// plugins/, missing plugin skills/, and an empty plugin skills/ are all
// normal.
func TestLoadPluginMissingAndEmptyLocationsProduceNoDiagnostics(t *testing.T) {
	t.Run("missing plugins", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || len(diags.All()) != 0 {
			t.Fatalf("missing plugins/ must produce no diagnostics: %v", diags.All())
		}
	})
	t.Run("missing plugin skills", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || len(diags.All()) != 0 {
			t.Fatalf("a plugin without skills/ must produce no diagnostics: %v", diags.All())
		}
	})
	t.Run("empty plugin skills", func(t *testing.T) {
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
		if err := os.MkdirAll(filepath.Join(root, "plugins", "vendor-x", "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		p, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || len(diags.All()) != 0 {
			t.Fatalf("an empty plugin skills/ must produce no diagnostics: %v", diags.All())
		}
	})
}

// TestPluginSkillFingerprintChangesWithContent proves a plugin skill's
// resources join the fingerprint like any other skill source.
func TestPluginSkillFingerprintChangesWithContent(t *testing.T) {
	build := func(body string) string {
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "vendor-x", validPluginJSON("vendor-x"))
		md := "---\nname: one\ndescription: One.\n---\n\n" + body + "\n"
		writeSkillFile(t, root, "plugins/vendor-x/skills/one/SKILL.md", []byte(md), 0o644)
		p, diags, err := Load(root)
		if err != nil || p == nil || diags.HasErrors() {
			t.Fatalf("load failed: %v %v", err, diags.All())
		}
		return p.Fingerprint
	}
	base := build("Body.")
	if again := build("Body."); again != base {
		t.Fatal("identical plugin skill source must fingerprint identically")
	}
	if changed := build("Different body."); changed == base {
		t.Fatal("changing a plugin skill's content must change the fingerprint")
	}
}

// TestLoadPluginManifestJoinsFingerprintEvenWhenInvalid proves a manifest
// whose bytes were read still joins the fingerprint even though the plugin
// itself is invalid and contributes no skills.
func TestLoadPluginManifestJoinsFingerprintEvenWhenInvalid(t *testing.T) {
	build := func(manifest string) string {
		root := writeAgent(t, "agent", validInstructions)
		writePluginManifest(t, root, "broken", manifest)
		p, diags, err := Load(root)
		if err != nil || p == nil || diags.HasErrors() {
			t.Fatalf("load failed: %v %v", err, diags.All())
		}
		return p.Fingerprint
	}
	base := build(`{"$schema": "` + pluginSchemaID + `"}`)
	changed := build(`{"$schema": "` + pluginSchemaID + `", "extra": "x"}`)
	if base == changed {
		t.Fatal("even an invalid manifest's read bytes must join the fingerprint")
	}
}

// TestLoadPluginTooManyPluginDirectoriesWarnsAndTruncates proves the
// 129th plugin directory is not imported and warns without failing the
// project.
func TestLoadPluginTooManyPluginDirectoriesWarnsAndTruncates(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	// Each plugin declares a uniquely named skill, so an imported 129th
	// plugin would show up as its own extra skill rather than being masked
	// by a collision with an earlier plugin.
	for i := 0; i <= MaxPluginEntries; i++ {
		pluginName := fmt.Sprintf("p%03d", i)
		skillName := fmt.Sprintf("s%03d", i)
		writePluginManifest(t, root, pluginName, validPluginJSON(pluginName))
		writeSkillFile(t, root, "plugins/"+pluginName+"/skills/"+skillName+"/SKILL.md", []byte(minimalSkillMD(skillName)), 0o644)
	}
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("exceeding the plugin directory ceiling must warn, not fail: %v", diags.All())
	}
	requireWarningID(t, diags, "plugin.bounds.exceeded")
	if len(p.Skills) != MaxPluginEntries {
		t.Fatalf("skills = %d, want exactly the %d imported plugins' skills", len(p.Skills), MaxPluginEntries)
	}
	lastSkill := fmt.Sprintf("s%03d", MaxPluginEntries)
	for _, s := range p.Skills {
		if s.Name == lastSkill {
			t.Fatalf("the 129th plugin directory must not be imported, but found its skill: %+v", s)
		}
	}
}
