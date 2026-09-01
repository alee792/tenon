package agentproject

// Skills follow the open Agent Skills directory format: each immediate
// directory under skills/ is one skill carrying a SKILL.md whose frontmatter
// name matches the directory, plus arbitrary regular-file resources copied
// byte-for-byte. Recognized vendor fields are preserved and recorded so
// generation can warn when the selected harness does not document honoring
// them; tenon never translates, strips, or enforces them.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/frontmatter"
)

// Skill bounds (ADR 0013): safety ceilings, not ordinary-use quotas.
// Violations fail before any workspace mutation, and sizes are checked from
// file metadata so an out-of-bounds file is rejected before it is read.
const (
	// MaxSkills bounds the number of skills under skills/.
	MaxSkills = 256
	// MaxSkillFiles bounds the files in one skill, including SKILL.md.
	MaxSkillFiles = 1024
	// MaxSkillSetFiles and MaxSkillSetBytes bound the whole skill set.
	MaxSkillSetFiles = 8192
	MaxSkillSetBytes = 64 << 20
	// MaxSkillBytes bounds one skill's total content.
	MaxSkillBytes = 64 << 20
	// MaxSkillMDBytes bounds SKILL.md; MaxSkillResourceBytes bounds every
	// other resource file.
	MaxSkillMDBytes       = 128 * 1024
	MaxSkillResourceBytes = 16 << 20
)

// Skill is one validated Agent Skills directory.
type Skill struct {
	// Name is the skill directory name, which the frontmatter name must
	// equal exactly.
	Name string
	// SourcePath is the authored path relative to the agent root:
	// "skills/NAME".
	SourcePath string
	// Description is SKILL.md's required frontmatter description, the
	// model-facing summary of when to use the skill.
	Description string
	// Files carries every regular file in the skill, SKILL.md first and
	// the rest sorted by relative path.
	Files []SkillFile
	// ClaudeFields lists the recognized vendor frontmatter fields present
	// in SKILL.md (including allowed-tools), sorted, so generation can warn
	// when the selected harness does not document honoring them.
	ClaudeFields []string
	// HasOpenAIYAML reports whether the skill carries the OpenAI host file
	// agents/openai.yaml; it is copied like any resource and warned for
	// Claude at generation.
	HasOpenAIYAML bool
	// SkillMDBodyStart is the byte offset just past SKILL.md's closing
	// frontmatter delimiter line, where generation inserts the ownership
	// marker.
	SkillMDBodyStart int
}

// SkillFile is one regular file inside a skill.
type SkillFile struct {
	// RelPath is the skill-relative path, slash-separated.
	RelPath string
	// Content is the exact authored bytes.
	Content []byte
	// Executable is the authored executable intent.
	Executable bool
}

// skillNamePattern is the Agent Skills name rule: lowercase words of letters
// and digits joined by single hyphens.
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// skillClaudeFields is the recognized Claude extension allowlist. Their
// value shapes are vendor-owned, so tenon rejects only a null value and
// otherwise preserves each field unchanged for the generation warning.
var skillClaudeFields = map[string]bool{
	"agent":                    true,
	"argument-hint":            true,
	"arguments":                true,
	"background":               true,
	"context":                  true,
	"disable-model-invocation": true,
	"disallowed-tools":         true,
	"effort":                   true,
	"hooks":                    true,
	"model":                    true,
	"paths":                    true,
	"shell":                    true,
	"user-invocable":           true,
	"when_to_use":              true,
}

// skillSetBudget tracks the count, file, and byte budget shared across every
// skill from every source — root skills/ and imported plugin skills alike
// (ADR 0009, ADR 0013) — so the merged set stays bounded even when composed
// from many small skills.
type skillSetBudget struct {
	count         int
	files         int
	bytes         int64
	filesExceeded bool
	bytesExceeded bool
}

// countSkill increments the aggregate skill count and emits the aggregate
// skill.bounds.exceeded error exactly once when the shared MaxSkills ceiling
// is first crossed. This aggregate ceiling is a hard safety limit that keeps
// its existing project-rejecting behavior regardless of source, so counting
// and validation both continue afterward rather than stopping early.
func (b *skillSetBudget) countSkill(diags *diagnostics.List) {
	b.count++
	if b.count == MaxSkills+1 {
		diags.Errorf("skill.bounds.exceeded", "skills",
			"skills may contain at most %d skills", MaxSkills)
	}
}

// loadSkills discovers and validates skills/, returning the skills sorted by
// name and every skill file as a fingerprint input. Invalid skills reject
// the project: they are authored project source, not isolatable plugin
// components. budget tracks the aggregate skill-set count, file, and byte
// ceilings shared with any imported plugin skills (ADR 0013).
func loadSkills(root string, budget *skillSetBudget, diags *diagnostics.List) ([]Skill, []sourceInput) {
	dir := filepath.Join(root, "skills")
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Errorf("skill.entry.invalid", "skills",
			"skills must be a real directory; symlinks are never followed")
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		diags.Errorf("skill.entry.invalid", "skills", "skills could not be read: %v", err)
		return nil, nil
	}

	var skills []Skill
	var inputs []sourceInput
	for _, entry := range entries {
		entryPath := "skills/" + entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("skill.entry.invalid", entryPath,
				"each skills entry must be a real skill directory; symlinks are never followed")
			continue
		}
		if !entry.IsDir() {
			if entry.Type().IsRegular() {
				diags.Errorf("skill.entry.invalid", entryPath,
					"the flat %s layout is not supported; the content belongs at skills/%s/SKILL.md",
					entryPath, strings.TrimSuffix(entry.Name(), ".md"))
			} else {
				diags.Errorf("skill.entry.invalid", entryPath,
					"each skills entry must be a real skill directory")
			}
			continue
		}
		budget.countSkill(diags)
		skill, skillInputs, ok := loadSkill(dir, entry.Name(), entryPath, budget, diags)
		inputs = append(inputs, skillInputs...)
		if ok {
			skills = append(skills, skill)
		}
	}
	slices.SortFunc(skills, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return skills, inputs
}

// loadSkill validates one skill directory: its name, its bounded regular
// files, and its SKILL.md contract. Every regular file is returned as a
// fingerprint input regardless of validity so identity tracks exactly what
// was authored. sourcePath is the skill's authored path relative to the
// agent root ("skills/NAME" for root skills, "plugins/X/skills/NAME" for an
// imported plugin skill); every diagnostic and the returned Skill name it.
func loadSkill(skillsDir, dirName, sourcePath string, budget *skillSetBudget, diags *diagnostics.List) (Skill, []sourceInput, bool) {
	valid := true
	if len(dirName) > 64 || !skillNamePattern.MatchString(dirName) {
		diags.Errorf("skill.name.invalid", sourcePath,
			"a skill directory name must be 1-64 lowercase letters, digits, and single hyphens, with no leading or trailing hyphen: %q", dirName)
		valid = false
	}

	s := Skill{Name: dirName, SourcePath: sourcePath}
	var inputs []sourceInput
	files := 0
	var skillBytes int64
	filesExceeded, bytesExceeded := false, false
	skillMDRead := false

	skillRoot := filepath.Join(skillsDir, dirName)
	walkErr := filepath.WalkDir(skillRoot, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if full == skillRoot {
			return nil
		}
		rel, err := filepath.Rel(skillRoot, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		relPath := sourcePath + "/" + rel
		if !utf8.ValidString(rel) {
			diags.Errorf("skill.resource.invalid", sourcePath,
				"every path inside a skill must be valid UTF-8; found %q", rel)
			valid = false
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			diags.Errorf("skill.resource.invalid", relPath,
				"every skill entry must be a real directory or regular file; symlinks are never followed")
			valid = false
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			diags.Errorf("skill.resource.invalid", relPath,
				"every skill entry must be a real directory or regular file")
			valid = false
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}

		// Size bounds come from file metadata so an out-of-bounds file is
		// rejected before it is read.
		readable := true
		if rel == "SKILL.md" {
			if info.Size() > MaxSkillMDBytes {
				diags.Errorf("skill.bounds.exceeded", relPath,
					"SKILL.md may contain at most %d bytes; found %d", MaxSkillMDBytes, info.Size())
				valid, readable = false, false
			}
		} else if info.Size() > MaxSkillResourceBytes {
			diags.Errorf("skill.bounds.exceeded", relPath,
				"a skill resource may contain at most %d bytes; found %d", MaxSkillResourceBytes, info.Size())
			valid, readable = false, false
		}
		files++
		if files == MaxSkillFiles+1 {
			diags.Errorf("skill.bounds.exceeded", sourcePath,
				"a skill may contain at most %d files", MaxSkillFiles)
			filesExceeded = true
			valid = false
		}
		skillBytes += info.Size()
		if !bytesExceeded && skillBytes > MaxSkillBytes {
			diags.Errorf("skill.bounds.exceeded", sourcePath,
				"a skill may contain at most %d bytes", MaxSkillBytes)
			bytesExceeded = true
			valid = false
		}
		budget.files++
		if !budget.filesExceeded && budget.files > MaxSkillSetFiles {
			diags.Errorf("skill.bounds.exceeded", "skills",
				"the skill set may contain at most %d files", MaxSkillSetFiles)
			budget.filesExceeded = true
		}
		budget.bytes += info.Size()
		if !budget.bytesExceeded && budget.bytes > MaxSkillSetBytes {
			diags.Errorf("skill.bounds.exceeded", "skills",
				"the skill set may contain at most %d bytes", MaxSkillSetBytes)
			budget.bytesExceeded = true
		}

		var content []byte
		if readable && !filesExceeded && !bytesExceeded && !budget.filesExceeded && !budget.bytesExceeded {
			content, err = os.ReadFile(full)
			if err != nil {
				diags.Errorf("skill.resource.invalid", relPath,
					"the skill file could not be read: %v", err)
				valid = false
			} else if rel == "SKILL.md" {
				skillMDRead = true
			}
		}
		executable := info.Mode().Perm()&0o111 != 0
		s.Files = append(s.Files, SkillFile{RelPath: rel, Content: content, Executable: executable})
		inputs = append(inputs, sourceInput{Path: relPath, Content: content, Executable: executable})
		if rel == "agents/openai.yaml" {
			s.HasOpenAIYAML = true
		}
		return nil
	})
	if walkErr != nil {
		diags.Errorf("skill.resource.invalid", sourcePath,
			"the skill directory could not be read: %v", walkErr)
		valid = false
	}

	mdIndex := slices.IndexFunc(s.Files, func(f SkillFile) bool { return f.RelPath == "SKILL.md" })
	if mdIndex < 0 {
		diags.Errorf("skill.skill-md.missing", sourcePath,
			"each skill requires a SKILL.md regular file at %s/SKILL.md", sourcePath)
		valid = false
	} else if skillMDRead {
		mdPath := sourcePath + "/SKILL.md"
		if !utf8.Valid(s.Files[mdIndex].Content) {
			diags.Errorf("skill.skill-md.encoding", mdPath, "SKILL.md must be valid UTF-8")
			valid = false
		} else {
			bodyStart, fields, description, ok := parseSkillMD(s.Files[mdIndex].Content, dirName, mdPath, diags)
			s.SkillMDBodyStart = bodyStart
			s.ClaudeFields = fields
			s.Description = description
			if !ok {
				valid = false
			}
		}
	}

	slices.SortFunc(s.Files, func(a, b SkillFile) int {
		switch {
		case a.RelPath == b.RelPath:
			return 0
		case a.RelPath == "SKILL.md":
			return -1
		case b.RelPath == "SKILL.md":
			return 1
		}
		return strings.Compare(a.RelPath, b.RelPath)
	})
	return s, inputs, valid
}

// parseSkillMD enforces the closed SKILL.md frontmatter contract: the
// standard portable fields validated to the standard's rules, the recognized
// Claude extension allowlist preserved without value-shape validation, and
// nothing else. It returns the body offset for marker insertion, the
// recognized vendor fields present, sorted, and the validated description.
func parseSkillMD(content []byte, name, path string, diags *diagnostics.List) (int, []string, string, bool) {
	raw, bodyStart, err := frontmatter.Split(content)
	if err != nil {
		diags.Errorf("skill.frontmatter.missing", path,
			"SKILL.md must start with YAML frontmatter delimited by --- lines")
		return 0, nil, "", false
	}
	doc, err := frontmatter.Parse(raw)
	if err != nil {
		diags.Errorf("skill.frontmatter.invalid", path, "%s", err)
		return 0, nil, "", false
	}

	valid := true
	var claudeFields []string
	var description string
	for _, key := range doc.Keys() {
		switch {
		case key == "name" || key == "description" || key == "license" ||
			key == "compatibility" || key == "metadata" || key == "allowed-tools":
		case skillClaudeFields[key]:
			if doc.IsNull(key) {
				diags.Errorf("skill.field.null", path,
					"frontmatter field %q must not be null", key)
				valid = false
			}
			claudeFields = append(claudeFields, key)
		default:
			diags.Errorf("skill.frontmatter.unknown-field", path,
				"frontmatter permits the Agent Skills fields and recognized Claude extensions only; found %q", key)
			valid = false
		}
	}

	if v, err := doc.String("name"); err != nil || v != name {
		diags.Errorf("skill.name.mismatch", path,
			"frontmatter must carry one plain name equal to the skill directory name %q", name)
		valid = false
	}
	if doc.Has("description") {
		if v, err := doc.String("description"); err != nil ||
			utf8.RuneCountInString(v) < 1 || utf8.RuneCountInString(v) > 1024 {
			diags.Errorf("skill.description.invalid", path,
				"description must be one plain string of 1-1024 characters")
			valid = false
		} else {
			description = v
		}
	} else {
		diags.Errorf("skill.description.missing", path,
			"frontmatter must carry one plain description")
		valid = false
	}
	if doc.Has("license") {
		if _, err := doc.String("license"); err != nil {
			diags.Errorf("skill.license.invalid", path, "license must be one plain string")
			valid = false
		}
	}
	if doc.Has("compatibility") {
		if v, err := doc.String("compatibility"); err != nil ||
			utf8.RuneCountInString(v) < 1 || utf8.RuneCountInString(v) > 500 {
			diags.Errorf("skill.compatibility.invalid", path,
				"compatibility must be one plain string of 1-500 characters")
			valid = false
		}
	}
	if doc.Has("metadata") {
		if _, err := doc.StringMap("metadata"); err != nil {
			diags.Errorf("skill.metadata.invalid", path, "%s", err)
			valid = false
		}
	}
	if doc.Has("allowed-tools") {
		if _, err := doc.String("allowed-tools"); err != nil {
			diags.Errorf("skill.allowed-tools.invalid", path,
				"allowed-tools must be one plain space-separated string")
			valid = false
		}
		claudeFields = append(claudeFields, "allowed-tools")
	}
	slices.Sort(claudeFields)
	return bodyStart, claudeFields, description, valid
}
