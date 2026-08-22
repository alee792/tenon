package agentproject

// Plugins vendor Agent Plugins v1 packages under plugins/ (ADR 0009). Each
// immediate real directory carries a bounded plugin.json manifest validated
// locally, with no network fetch, against the exact canonical Agent Plugins
// v1.0.0 schema identifier. A valid plugin contributes skills only from
// immediate real directories under its fixed skills/ location, reusing the
// same per-skill loader as root skills/.
//
// The isolation contrast with root skills is deliberate: a manifest
// violation, an invalid plugin skill, or a name collision skips only that
// plugin or skill with a warning, never the project. Structural violations
// of the plugins/ layout itself — plugins/ or an immediate entry not being a
// real directory — are project errors, exactly like the analogous skills/
// entries.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
)

// Plugin bounds (ADR 0013): safety ceilings, not ordinary-use quotas.
const (
	// MaxPluginEntries bounds the immediate directories under plugins/.
	MaxPluginEntries = 128
	// MaxPluginSkillEntries bounds the entries in one plugin's skills/
	// location.
	MaxPluginSkillEntries = 1024
	// MaxPluginManifestBytes bounds plugin.json.
	MaxPluginManifestBytes = 128 * 1024
)

// pluginSchemaID is the exact canonical Agent Plugins v1.0.0 schema
// identifier every plugin.json must target. Tenon implements this small
// schema locally and never fetches it.
const pluginSchemaID = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"

// pluginKnownFields are the recognized top-level plugin.json fields; every
// other field is ignored with a precise warning.
var pluginKnownFields = map[string]bool{
	"$schema":     true,
	"name":        true,
	"description": true,
	"version":     true,
	"author":      true,
	"homepage":    true,
	"license":     true,
	"extensions":  true,
}

// pluginSkill is one candidate skill imported from a plugin, still carrying
// its own fingerprint inputs so a later collision can drop both without
// having touched the shared skill list.
type pluginSkill struct {
	Skill  Skill
	Inputs []sourceInput
}

// loadPlugins discovers plugins/ and returns every candidate skill from
// every valid plugin, in precedence order: plugin directories in lexical
// order by storage name, and each plugin's skill directories in lexical
// order. Collision resolution against root skills happens in the caller,
// which alone knows the root names. budget is the aggregate skill-set
// budget shared with root skills/ (ADR 0013); it is not reset here.
func loadPlugins(root string, budget *skillSetBudget, diags *diagnostics.List) ([]pluginSkill, []sourceInput) {
	dir := filepath.Join(root, "plugins")
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil // missing plugins/ is normal
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Errorf("plugin.entry.invalid", "plugins",
			"plugins must be a real directory; symlinks are never followed")
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		diags.Errorf("plugin.entry.invalid", "plugins", "plugins could not be read: %v", err)
		return nil, nil
	}

	var names []string
	count := 0
	truncated := false
	for _, entry := range entries {
		entryPath := "plugins/" + entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("plugin.entry.invalid", entryPath,
				"each plugins entry must be a real plugin directory; symlinks are never followed")
			continue
		}
		if !entry.IsDir() {
			diags.Errorf("plugin.entry.invalid", entryPath,
				"each plugins entry must be a real plugin directory")
			continue
		}
		count++
		if count > MaxPluginEntries {
			if !truncated {
				diags.Warnf("plugin.bounds.exceeded", "plugins",
					"plugins may contain at most %d plugin directories; later entries are ignored", MaxPluginEntries)
				truncated = true
			}
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	var candidates []pluginSkill
	var inputs []sourceInput
	for _, name := range names {
		pluginCandidates, manifestInputs := loadPlugin(dir, name, budget, diags)
		candidates = append(candidates, pluginCandidates...)
		inputs = append(inputs, manifestInputs...)
	}
	return candidates, inputs
}

// loadPlugin validates one plugin directory's manifest and, when it is
// valid, its skills/ location. A manifest violation makes the plugin
// contribute no skills, but its bytes still join the fingerprint once read.
func loadPlugin(pluginsDir, dirName string, budget *skillSetBudget, diags *diagnostics.List) ([]pluginSkill, []sourceInput) {
	pluginRoot := filepath.Join(pluginsDir, dirName)
	authoredRoot := "plugins/" + dirName

	valid, manifestInput := loadPluginManifest(pluginRoot, authoredRoot, diags)
	var inputs []sourceInput
	if manifestInput != nil {
		inputs = append(inputs, *manifestInput)
	}
	if !valid {
		return nil, inputs
	}

	skillsDir := filepath.Join(pluginRoot, "skills")
	candidates := loadPluginSkills(skillsDir, authoredRoot+"/skills", budget, diags)
	return candidates, inputs
}

// loadPluginManifest validates plugin.json: a bounded, regular, UTF-8 file
// holding a JSON object that targets the exact canonical schema and carries
// a non-empty name. It returns whether the plugin is valid and, whenever the
// file's bytes were actually read from disk, the fingerprint input for those
// bytes — read manifest bytes join the fingerprint even when the manifest
// turns out invalid.
func loadPluginManifest(pluginRoot, authoredRoot string, diags *diagnostics.List) (bool, *sourceInput) {
	path := authoredRoot + "/plugin.json"
	full := filepath.Join(pluginRoot, "plugin.json")

	info, err := os.Lstat(full)
	if err != nil {
		diags.Warnf("plugin.manifest.invalid", path,
			"each plugin requires a plugin.json manifest; none was found")
		return false, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		diags.Warnf("plugin.manifest.invalid", path,
			"plugin.json must be a regular file; symlinks are never followed")
		return false, nil
	}
	if info.Size() > MaxPluginManifestBytes {
		diags.Warnf("plugin.manifest.invalid", path,
			"plugin.json may contain at most %d bytes; found %d", MaxPluginManifestBytes, info.Size())
		return false, nil
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		diags.Warnf("plugin.manifest.invalid", path, "plugin.json could not be read: %v", err)
		return false, nil
	}
	input := &sourceInput{Path: path, Content: raw, Executable: info.Mode().Perm()&0o111 != 0}

	if !utf8.Valid(raw) {
		diags.Warnf("plugin.manifest.invalid", path, "plugin.json must be valid UTF-8")
		return false, input
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		diags.Warnf("plugin.manifest.invalid", path, "plugin.json is not valid JSON: %v", err)
		return false, input
	}

	valid := true
	if schema, ok := doc["$schema"].(string); !ok || schema != pluginSchemaID {
		diags.Warnf("plugin.manifest.invalid", path,
			"plugin.json must set \"$schema\" to the canonical Agent Plugins v1.0.0 schema %q", pluginSchemaID)
		valid = false
	}
	if name, ok := doc["name"].(string); !ok || strings.TrimSpace(name) == "" {
		diags.Warnf("plugin.manifest.invalid", path,
			"plugin.json must declare a non-empty \"name\" string")
		valid = false
	}
	if !valid {
		return false, input
	}

	// Unknown top-level fields and an unusable extensions field are ignored
	// with warnings; tenon cannot operationalize them, but they do not
	// invalidate an otherwise-valid manifest.
	for _, key := range sortedKeys(doc) {
		if !pluginKnownFields[key] {
			diags.Warnf("plugin.manifest.unknown-field", path,
				"plugin.json contains the unrecognized field %q; it is ignored", key)
		}
	}
	if raw, ok := doc["extensions"]; ok {
		ext, isObject := raw.(map[string]any)
		if !isObject {
			diags.Warnf("plugin.extension.unsupported", path,
				"plugin.json \"extensions\" must be a JSON object when present; found %s; the extensions field is ignored",
				jsonTypeName(raw))
		} else {
			for _, ns := range sortedKeys(ext) {
				diags.Warnf("plugin.extension.unsupported", path,
					"plugin.json extension namespace %q is not supported by tenon and is ignored", ns)
			}
		}
	}

	return true, input
}

// loadPluginSkills discovers one plugin's skills/ location and validates
// each immediate real directory with the same per-skill loader root skills
// use, in warn-and-skip mode. A missing or empty location is normal.
func loadPluginSkills(skillsDir, authoredSkillsRoot string, budget *skillSetBudget, diags *diagnostics.List) []pluginSkill {
	info, err := os.Lstat(skillsDir)
	if err != nil {
		return nil // missing plugin skills/ is normal
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Warnf("plugin.skill.invalid", authoredSkillsRoot,
			"a plugin's skills entry must be a real directory; symlinks are never followed")
		return nil
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		diags.Warnf("plugin.skill.invalid", authoredSkillsRoot,
			"the plugin skills directory could not be read: %v", err)
		return nil
	}

	var names []string
	count := 0
	truncated := false
	for _, entry := range entries {
		entryPath := authoredSkillsRoot + "/" + entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Warnf("plugin.skill.invalid", entryPath,
				"each plugin skills entry must be a real skill directory; symlinks are never followed")
			continue
		}
		if !entry.IsDir() {
			diags.Warnf("plugin.skill.invalid", entryPath,
				"each plugin skills entry must be a real skill directory")
			continue
		}
		count++
		if count > MaxPluginSkillEntries {
			if !truncated {
				diags.Warnf("plugin.bounds.exceeded", authoredSkillsRoot,
					"a plugin's skills location may contain at most %d entries; later entries are ignored", MaxPluginSkillEntries)
				truncated = true
			}
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	var out []pluginSkill
	for _, name := range names {
		sourcePath := authoredSkillsRoot + "/" + name
		budget.countSkill(diags)
		skill, inputs, ok := loadPluginSkill(skillsDir, name, sourcePath, budget, diags)
		if ok {
			out = append(out, pluginSkill{Skill: skill, Inputs: inputs})
		}
	}
	return out
}

// loadPluginSkill reuses loadSkill exactly as root skills use it, capturing
// its diagnostics into a scratch list so they can be re-emitted at warning
// severity under one stable ID with the plugin's authored path preserved.
// The shared aggregate skill-set budget (ADR 0013) is a hard safety ceiling
// that keeps its existing project-rejecting error behavior regardless of
// source, so those diagnostics — always reported at the fixed "skills" path
// — propagate unchanged instead of being downgraded.
func loadPluginSkill(skillsDir, dirName, sourcePath string, budget *skillSetBudget, diags *diagnostics.List) (Skill, []sourceInput, bool) {
	scratch := &diagnostics.List{}
	skill, inputs, ok := loadSkill(skillsDir, dirName, sourcePath, budget, scratch)
	for _, d := range scratch.All() {
		if d.Path == "skills" {
			diags.Add(d)
			continue
		}
		diags.Warnf("plugin.skill.invalid", d.Path, "%s", d.Rule)
	}
	if !ok {
		return Skill{}, nil, false
	}
	return skill, inputs, true
}

// mergeSkills combines root skills (already unique by construction) with
// plugin skill candidates in precedence order. Root skills always win; among
// plugin candidates the first in precedence order wins. A later collision is
// skipped with a warning naming both authored paths and is never renamed.
// The result is sorted by name, and only accepted plugin skills' resources
// are returned as fingerprint inputs.
func mergeSkills(rootSkills []Skill, candidates []pluginSkill, diags *diagnostics.List) ([]Skill, []sourceInput) {
	seen := make(map[string]string, len(rootSkills)+len(candidates))
	for _, s := range rootSkills {
		seen[s.Name] = s.SourcePath
	}
	merged := slices.Clone(rootSkills)
	var inputs []sourceInput
	for _, cand := range candidates {
		if existing, collide := seen[cand.Skill.Name]; collide {
			diags.Warnf("plugin.skill.collision", cand.Skill.SourcePath,
				"skill name %q at %s collides with the earlier skill at %s; the later skill is skipped and never renamed",
				cand.Skill.Name, cand.Skill.SourcePath, existing)
			continue
		}
		seen[cand.Skill.Name] = cand.Skill.SourcePath
		merged = append(merged, cand.Skill)
		inputs = append(inputs, cand.Inputs...)
	}
	slices.SortFunc(merged, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return merged, inputs
}

// sortedKeys returns m's keys sorted, so diagnostics render deterministically.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonTypeName names a decoded JSON value's type for a diagnostic message.
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "value"
	}
}
