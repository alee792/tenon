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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/frontmatter"
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
	// MaxPluginReferenceBytes bounds one plugins/<name>.md reference file,
	// mirroring mcp/<name>.md's MaxConnectionBytes exactly (ADR 0026).
	MaxPluginReferenceBytes = 8 * 1024
	// MaxPluginReferenceBodyRunes bounds a plugin reference file's optional
	// trimmed Markdown body, mirroring MaxConnectionContextRunes exactly.
	MaxPluginReferenceBodyRunes = 1024
)

// pluginReferenceRevPattern is the exact grammar a plugin reference file's
// pinned revision must match: a full, lowercase 40-character git commit SHA
// (ADR 0026 "plugin acquisition by pointer and pin"). A short SHA, a branch,
// or a tag is refused: the review-and-pin discipline this record preserves
// depends on an unambiguous, immutable commit identity.
var pluginReferenceRevPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// pluginReferenceFields are the only two recognized plugins/<name>.md
// reference frontmatter fields; every other field is unknown.
var pluginReferenceFields = map[string]bool{"source": true, "rev": true}

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

// loadPlugins discovers plugins/ and returns every candidate skill and every
// accepted MCP server from every valid plugin, in precedence order: plugin
// directories in lexical order by storage name, and each plugin's skill
// directories and declared servers in lexical order. Skill collision
// resolution against root skills happens in the caller, which alone knows the
// root names; server names have no root surface, so they are resolved here.
// budget is the aggregate skill-set budget shared with root skills/ (ADR
// 0013); it is not reset here.
func loadPlugins(root string, budget *skillSetBudget, diags *diagnostics.List) ([]pluginSkill, []PluginServer, []sourceInput) {
	dir := filepath.Join(root, "plugins")
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil, nil // missing plugins/ is normal
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Errorf("plugin.entry.invalid", "plugins",
			"plugins must be a real directory; symlinks are never followed")
		return nil, nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		diags.Errorf("plugin.entry.invalid", "plugins", "plugins could not be read: %v", err)
		return nil, nil, nil
	}

	// Each entry is either a vendored plugin directory or a plugin reference
	// file (plugins/<name>.md, ADR 0026); every other shape is invalid. Both
	// forms share one name space and one bound, and a reference colliding
	// with a directory of the same name fails closed before either is
	// loaded, since the two forms could otherwise silently coexist under
	// different vocabularies for what an author intends as one plugin.
	type pluginEntry struct {
		name     string
		fileName string
		isRef    bool
	}
	var found []pluginEntry
	seenNames := map[string]bool{}
	count := 0
	truncated := false
	for _, entry := range entries {
		entryPath := "plugins/" + entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("plugin.entry.invalid", entryPath,
				"each plugins entry must be a real plugin directory or plugin reference file; symlinks are never followed")
			continue
		}
		var name string
		isRef := false
		switch {
		case entry.IsDir():
			name = entry.Name()
		case entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".md"):
			name = strings.TrimSuffix(entry.Name(), ".md")
			isRef = true
			if name == "" {
				diags.Errorf("plugin.entry.invalid", entryPath,
					"a plugin reference filename must carry a non-empty name before .md")
				continue
			}
		default:
			diags.Errorf("plugin.entry.invalid", entryPath,
				"each plugins entry must be a real plugin directory or a plugin reference file (<name>.md)")
			continue
		}
		count++
		if count > MaxPluginEntries {
			if !truncated {
				diags.Warnf("plugin.bounds.exceeded", "plugins",
					"plugins may contain at most %d entries (directories and reference files combined); later entries are ignored", MaxPluginEntries)
				truncated = true
			}
			continue
		}
		if seenNames[name] {
			diags.Errorf("plugin.entry.collision", entryPath,
				"the plugin name %q collides with another plugins/ entry of the same name; a plugin reference file and a vendored plugin directory may not share a name",
				name)
			continue
		}
		seenNames[name] = true
		found = append(found, pluginEntry{name: name, fileName: entry.Name(), isRef: isRef})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })

	var candidates []pluginSkill
	var servers []PluginServer
	var inputs []sourceInput
	for _, f := range found {
		var pluginCandidates []pluginSkill
		var pluginServers []PluginServer
		var pluginInputs []sourceInput
		if f.isRef {
			pluginCandidates, pluginServers, pluginInputs, _ = loadPluginReference(dir, f.fileName, budget, diags)
		} else {
			pluginCandidates, pluginServers, pluginInputs = loadPlugin(filepath.Join(dir, f.fileName), "plugins/"+f.fileName, f.fileName, budget, diags)
		}
		candidates = append(candidates, pluginCandidates...)
		servers = append(servers, pluginServers...)
		inputs = append(inputs, pluginInputs...)
	}
	return candidates, mergePluginServers(servers, diags), inputs
}

// loadPlugin validates one plugin's manifest and, when it is valid, its two
// supported component locations: skills/ and mcp.json. A manifest violation
// makes the plugin contribute nothing, but its bytes still join the
// fingerprint once read. pluginRoot is the real filesystem directory to read
// from — a vendored plugins/<dirName>/ directory, or a plugin reference
// file's resolved cache tree — and authoredRoot is the stable path every
// diagnostic and fingerprint entry reports, which for a resolved reference is
// the synthetic "plugins/<name>.md -> <rev>" form rather than a real
// filesystem path.
func loadPlugin(pluginRoot, authoredRoot, pluginName string, budget *skillSetBudget, diags *diagnostics.List) ([]pluginSkill, []PluginServer, []sourceInput) {
	valid, manifestInput := loadPluginManifest(pluginRoot, authoredRoot, diags)
	var inputs []sourceInput
	if manifestInput != nil {
		inputs = append(inputs, *manifestInput)
	}
	if !valid {
		return nil, nil, inputs
	}

	warnUnsupportedComponents(pluginRoot, authoredRoot, diags)

	skillsDir := filepath.Join(pluginRoot, "skills")
	candidates := loadPluginSkills(skillsDir, authoredRoot+"/skills", budget, diags)
	servers, mcpInputs := loadPluginMCP(pluginRoot, authoredRoot, pluginName, diags)
	inputs = append(inputs, mcpInputs...)
	return candidates, servers, inputs
}

// pluginComponents are the plugin root entries tenon compiles. Every other
// entry is an Agent Plugins component tenon does not implement, skipped with
// one warning each rather than silently ignored (ADR 0009).
var pluginComponents = map[string]bool{
	"plugin.json": true,
	"skills":      true,
	"mcp.json":    true,
}

// unsupportedComponentDirs are Agent Plugins component locations tenon
// cannot operationalize. Only these warn: ordinary payload files and
// directories (binaries an accepted command runs, READMEs, licenses) are
// inert plugin content, not skipped components.
var unsupportedComponentDirs = map[string]bool{
	"commands": true,
	"agents":   true,
	"hooks":    true,
}

// warnUnsupportedComponents reports each component location tenon cannot
// operationalize in an accepted plugin's root exactly once, in lexical
// order.
func warnUnsupportedComponents(pluginRoot, authoredRoot string, diags *diagnostics.List) {
	entries, err := os.ReadDir(pluginRoot)
	if err != nil {
		return // the manifest already proved the directory readable
	}
	for _, entry := range entries {
		if !unsupportedComponentDirs[entry.Name()] {
			continue
		}
		diags.Warnf("plugin.component.unsupported", authoredRoot+"/"+entry.Name(),
			"tenon consumes only plugin.json, skills, and mcp.json; the %s component is not supported and is skipped",
			entry.Name())
	}
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

// loadPluginReference validates one plugins/<name>.md reference file and, on
// success, resolves it against the injected plugin cache (ConfigurePluginCache)
// and loads the cached tree through the exact same loadPlugin path a vendored
// plugins/<name>/ directory uses (ADR 0026): the same manifest, skills, and
// mcp.json validation, the same collision checks, and the same fingerprint
// treatment for its component bytes. The reference file's own bytes always
// join the fingerprint once read, independent of whether resolution
// succeeds; a failed resolution contributes no candidates but is always a
// project error, naming `tenon plugin fetch`, because an authored reference
// is a first-class request exactly like mcp/<name>.md (ADR 0026), never a
// silently-skipped optional plugin component.
func loadPluginReference(dir, filename string, budget *skillSetBudget, diags *diagnostics.List) ([]pluginSkill, []PluginServer, []sourceInput, string) {
	sourcePath := "plugins/" + filename
	name := strings.TrimSuffix(filename, ".md")

	full := filepath.Join(dir, filename)
	info, err := os.Lstat(full)
	if err != nil {
		diags.Errorf("plugin.reference.invalid", sourcePath, "the plugin reference file could not be read: %v", err)
		return nil, nil, nil, name
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		diags.Errorf("plugin.reference.invalid", sourcePath,
			"a plugin reference must be a real regular file; symlinks are never followed")
		return nil, nil, nil, name
	}
	if info.Size() > MaxPluginReferenceBytes {
		diags.Errorf("plugin.reference.invalid", sourcePath,
			"a plugin reference file may contain at most %d bytes; found %d", MaxPluginReferenceBytes, info.Size())
		return nil, nil, nil, name
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		diags.Errorf("plugin.reference.invalid", sourcePath, "the plugin reference file could not be read: %v", err)
		return nil, nil, nil, name
	}
	inputs := []sourceInput{{Path: sourcePath, Content: raw, Executable: false}}

	if !utf8.Valid(raw) {
		diags.Errorf("plugin.reference.invalid", sourcePath, "the plugin reference file must be valid UTF-8")
		return nil, nil, inputs, name
	}

	source, rev, ok := parsePluginReference(raw, sourcePath, diags)
	if !ok {
		return nil, nil, inputs, name
	}
	_ = source // validated above; resolution below keys only on rev

	if pluginCache == nil {
		diags.Errorf("plugin.reference.unresolved", sourcePath,
			"plugin reference %q pins rev %s, which is not cached; run `tenon plugin fetch` before apply or validate",
			name, rev)
		return nil, nil, inputs, name
	}
	cachedRoot, err := pluginCache.Resolve(rev)
	if err != nil {
		diags.Errorf("plugin.reference.unresolved", sourcePath,
			"plugin reference %q could not be resolved against the plugin cache for rev %s: %s; run `tenon plugin fetch`",
			name, rev, diagnostics.Bound(err.Error(), 256))
		return nil, nil, inputs, name
	}

	authoredRoot := fmt.Sprintf("plugins/%s.md -> %s", name, rev)
	candidates, servers, pluginInputs := loadPlugin(cachedRoot, authoredRoot, name, budget, diags)
	inputs = append(inputs, pluginInputs...)
	return candidates, servers, inputs, name
}

// parsePluginReference validates a plugin reference file's closed frontmatter
// contract: exactly the fields "source" (an absolute https URL, mirroring
// mcp/<name>.md's remote target rule exactly) and "rev" (a full 40-character
// lowercase git commit SHA), plus an optional bounded informational body that
// is never rendered into instructions. It is shared by Load (via
// loadPluginReference) and LoadPluginReferencesForStatus, so `tenon plugin
// fetch|update|status` read a reference file under the identical rule Load
// enforces.
func parsePluginReference(raw []byte, sourcePath string, diags *diagnostics.List) (source, rev string, ok bool) {
	rawFM, bodyStart, err := frontmatter.Split(raw)
	if err != nil {
		diags.Errorf("plugin.reference.frontmatter.missing", sourcePath,
			"a plugin reference file must start with YAML frontmatter delimited by --- lines")
		return "", "", false
	}
	doc, err := frontmatter.Parse(rawFM)
	if err != nil {
		diags.Errorf("plugin.reference.frontmatter.invalid", sourcePath, "%s", err)
		return "", "", false
	}
	for _, key := range doc.Keys() {
		if !pluginReferenceFields[key] {
			diags.Errorf("plugin.reference.frontmatter.unknown-field", sourcePath,
				"a plugin reference frontmatter permits only source and rev; found %q", key)
			return "", "", false
		}
	}
	source, err = doc.String("source")
	if err != nil || source == "" {
		diags.Errorf("plugin.reference.frontmatter.invalid", sourcePath,
			"frontmatter field \"source\" must be a non-empty string")
		return "", "", false
	}
	if err := validConnectionURL(source); err != nil {
		diags.Errorf("plugin.reference.source.invalid", sourcePath, "%s", err)
		return "", "", false
	}
	rev, err = doc.String("rev")
	if err != nil || !pluginReferenceRevPattern.MatchString(rev) {
		diags.Errorf("plugin.reference.rev.invalid", sourcePath,
			"frontmatter field \"rev\" must be a full 40-character lowercase hexadecimal git commit SHA")
		return "", "", false
	}

	body := string(raw[bodyStart:])
	if after, cut := strings.CutPrefix(body, "\r\n"); cut {
		body = after
	} else {
		body = strings.TrimPrefix(body, "\n")
	}
	body = strings.TrimSpace(body)
	if n := utf8.RuneCountInString(body); n > MaxPluginReferenceBodyRunes {
		diags.Errorf("plugin.reference.body.too-long", sourcePath,
			"the optional Markdown body may contain at most %d Unicode characters; found %d", MaxPluginReferenceBodyRunes, n)
		return "", "", false
	}
	return source, rev, true
}

// PluginReferenceInfo is one independently-validated plugins/<name>.md
// reference file's declared source and pin.
type PluginReferenceInfo struct {
	// Name is the filename-derived plugin name.
	Name string
	// Source is the exact validated absolute HTTPS URL.
	Source string
	// Rev is the exact validated 40-character lowercase git commit SHA.
	Rev string
	// SourcePath is the authored path relative to the agent root:
	// "plugins/<name>.md".
	SourcePath string
}

// LoadPluginReferencesForStatus validates every plugins/<name>.md reference
// file's frontmatter independently of the rest of the project and of cache
// resolution: unlike Load, one malformed reference or an unresolved pin never
// suppresses reporting the others. It never resolves the pin against a cache
// and never touches a vendored plugins/<name>/ directory (Load remains the
// authority for the reference/directory collision check). This is what
// `tenon plugin fetch|status|update` read directly, since fetch necessarily
// runs before a pin can resolve.
func LoadPluginReferencesForStatus(root string) ([]PluginReferenceInfo, *diagnostics.List, error) {
	diags := &diagnostics.List{}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, diags, fmt.Errorf("resolving agent root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		diags.Errorf("project.root.missing", ".", "the agent root must be an existing directory: %s", root)
		return nil, diags, nil
	}
	dir := filepath.Join(abs, "plugins")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, diags, nil // missing plugins/ is normal
	}

	var out []PluginReferenceInfo
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".md") {
			continue // directories and non-reference entries are Load's concern
		}
		sourcePath := "plugins/" + entry.Name()
		name := strings.TrimSuffix(entry.Name(), ".md")
		full := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(full)
		if err != nil {
			diags.Errorf("plugin.reference.invalid", sourcePath, "the plugin reference file could not be read: %v", err)
			continue
		}
		if !utf8.Valid(raw) {
			diags.Errorf("plugin.reference.invalid", sourcePath, "the plugin reference file must be valid UTF-8")
			continue
		}
		source, rev, ok := parsePluginReference(raw, sourcePath, diags)
		if !ok {
			continue
		}
		out = append(out, PluginReferenceInfo{Name: name, Source: source, Rev: rev, SourcePath: sourcePath})
	}
	slices.SortFunc(out, func(a, b PluginReferenceInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, diags, nil
}
