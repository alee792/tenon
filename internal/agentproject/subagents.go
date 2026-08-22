package agentproject

// Subagents give one level of native inherited routing to a child agent
// (ADR 0004, amended by ADR 0007 for effort). Each immediate directory under
// subagents/ carries only a descriptive instructions.md; native parent
// inheritance in each harness supplies everything else, so child tools,
// skills, dependency files, and nested subagents are rejected rather than
// ignored.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/frontmatter"
)

// Subagent bounds (ADR 0013): safety ceilings, not ordinary-use quotas.
// Violations fail before any workspace mutation.
const (
	// MaxSubagents bounds the number of immediate subagents/ entries.
	MaxSubagents = 128
	// MaxSubagentInstructionsBytes bounds one child instructions.md.
	MaxSubagentInstructionsBytes = 128 * 1024
	// MaxSubagentsAggregateBytes bounds every child instructions.md combined.
	MaxSubagentsAggregateBytes = 16 << 20
)

// subagentNamePattern is the subagent directory grammar (distinct from the
// skill grammar): a leading lowercase letter, then lowercase letters,
// digits, or hyphens, at most 63 characters total.
var subagentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// reservedSubagentNames are names claimed by the managed built-in tools.
// Authored tool names are not reserved here: they are discovered per project
// and checked against subagents once both discoveries have run.
var reservedSubagentNames = map[string]bool{
	"echo":            true,
	"record-friction": true,
}

// Subagent is one validated immediate subagents/ entry.
type Subagent struct {
	// Name is the subagent directory name.
	Name string
	// Description is the required frontmatter description.
	Description string
	// Effort is "low", "medium", "high", or "" when absent.
	Effort string
	// Body is the Markdown body without the frontmatter; generation emits
	// exactly this, trimmed.
	Body string
}

// subagentBudget tracks the aggregate byte budget shared across every
// subagent instructions.md (ADR 0013), so the whole set stays bounded even
// when each child is individually small.
type subagentBudget struct {
	bytes    int64
	exceeded bool
}

// loadSubagents discovers and validates subagents/, returning the subagents
// sorted by name and every child instructions.md as a fingerprint input.
// Invalid subagents reject the project: they are authored project source,
// not isolatable plugin components.
func loadSubagents(root string, diags *diagnostics.List) ([]Subagent, []sourceInput) {
	dir := filepath.Join(root, "subagents")
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Errorf("subagent.entry.invalid", "subagents",
			"subagents must be a real directory; symlinks are never followed")
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		diags.Errorf("subagent.entry.invalid", "subagents", "subagents could not be read: %v", err)
		return nil, nil
	}

	var subagents []Subagent
	var inputs []sourceInput
	budget := &subagentBudget{}
	count := 0
	for _, entry := range entries {
		entryPath := "subagents/" + entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("subagent.entry.invalid", entryPath,
				"each subagents entry must be a real subagent directory; symlinks are never followed")
			continue
		}
		if !entry.IsDir() {
			diags.Errorf("subagent.entry.invalid", entryPath,
				"each subagents entry must be a real subagent directory")
			continue
		}
		count++
		if count == MaxSubagents+1 {
			diags.Errorf("subagent.bounds.exceeded", "subagents",
				"subagents may contain at most %d immediate subagents", MaxSubagents)
		}
		sub, subInputs, ok := loadSubagentDir(dir, entry.Name(), budget, diags)
		inputs = append(inputs, subInputs...)
		if ok {
			subagents = append(subagents, sub)
		}
	}
	slices.SortFunc(subagents, func(a, b Subagent) int { return strings.Compare(a.Name, b.Name) })
	return subagents, inputs
}

// loadSubagentDir validates one subagent directory: its name, that it
// carries only instructions.md (dot-prefixed entries are skipped, any other
// entry is rejected, not ignored), and the instructions.md contract itself.
// The instructions.md content, when read, is always returned as a
// fingerprint input regardless of validity so identity tracks exactly what
// was authored.
func loadSubagentDir(subagentsDir, name string, budget *subagentBudget, diags *diagnostics.List) (Subagent, []sourceInput, bool) {
	sourcePath := "subagents/" + name
	valid := true
	if !subagentNamePattern.MatchString(name) {
		diags.Errorf("subagent.name.invalid", sourcePath,
			"a subagent directory name must be 1-63 characters, starting with a lowercase letter and continuing with lowercase letters, digits, or hyphens: %q", name)
		valid = false
	}
	if reservedSubagentNames[name] {
		diags.Errorf("subagent.name.reserved", sourcePath,
			"the name %q is reserved for a managed built-in tool", name)
		valid = false
	}

	full := filepath.Join(subagentsDir, name)
	entries, err := os.ReadDir(full)
	if err != nil {
		diags.Errorf("subagent.entry.invalid", sourcePath, "the subagent directory could not be read: %v", err)
		return Subagent{Name: name}, nil, false
	}

	var instructionsEntry os.DirEntry
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Name() == "instructions.md" {
			instructionsEntry = entry
			continue
		}
		diags.Errorf("subagent.child.unsupported", sourcePath+"/"+entry.Name(),
			"a subagent supports instructions.md only; found %q", entry.Name())
		valid = false
	}

	sub := Subagent{Name: name}
	instructionsPath := sourcePath + "/instructions.md"
	if instructionsEntry == nil {
		diags.Errorf("subagent.instructions.missing", instructionsPath,
			"each subagent requires a regular instructions.md file at %s", instructionsPath)
		return sub, nil, false
	}
	if instructionsEntry.Type()&os.ModeSymlink != 0 || !instructionsEntry.Type().IsRegular() {
		diags.Errorf("subagent.instructions.missing", instructionsPath,
			"instructions.md must be a regular file; symlinks are never followed")
		return sub, nil, false
	}

	info, err := instructionsEntry.Info()
	if err != nil {
		diags.Errorf("subagent.instructions.missing", instructionsPath, "instructions.md could not be read: %v", err)
		return sub, nil, false
	}

	readable := true
	if info.Size() > MaxSubagentInstructionsBytes {
		diags.Errorf("subagent.instructions.too-large", instructionsPath,
			"instructions.md may contain at most %d bytes; found %d", MaxSubagentInstructionsBytes, info.Size())
		valid, readable = false, false
	}

	budget.bytes += info.Size()
	if !budget.exceeded && budget.bytes > MaxSubagentsAggregateBytes {
		diags.Errorf("subagent.bounds.exceeded", "subagents",
			"every subagent instructions.md together may contain at most %d bytes", MaxSubagentsAggregateBytes)
		budget.exceeded = true
		valid = false
	}

	var raw []byte
	if readable && !budget.exceeded {
		raw, err = os.ReadFile(filepath.Join(full, "instructions.md"))
		if err != nil {
			diags.Errorf("subagent.instructions.missing", instructionsPath, "instructions.md could not be read: %v", err)
			valid = false
			raw = nil
		}
	}
	inputs := []sourceInput{{Path: instructionsPath, Content: raw, Executable: false}}

	if raw != nil {
		if !utf8.Valid(raw) {
			diags.Errorf("subagent.instructions.encoding", instructionsPath, "instructions.md must be valid UTF-8")
			valid = false
		} else {
			parsed, ok := parseSubagentInstructions(string(raw), instructionsPath, diags)
			if parsed != nil {
				sub.Description = parsed.Description
				sub.Effort = parsed.Effort
				sub.Body = parsed.Body
			}
			if !ok {
				valid = false
			}
		}
	}

	return sub, inputs, valid
}

// parseSubagentInstructions enforces the closed child frontmatter contract:
// one plain description, an optional effort of exactly low, medium, or
// high, and a non-empty body. friction-notes is not permitted on a child.
func parseSubagentInstructions(content, path string, diags *diagnostics.List) (*Subagent, bool) {
	raw, bodyStart, err := frontmatter.Split([]byte(content))
	if err != nil {
		diags.Errorf("subagent.frontmatter.missing", path,
			"instructions.md must start with YAML frontmatter delimited by --- lines")
		return nil, false
	}
	doc, err := frontmatter.Parse(raw)
	if err != nil {
		diags.Errorf("subagent.frontmatter.invalid", path, "%s", err)
		return nil, false
	}

	out := &Subagent{}
	valid := true
	for _, key := range doc.Keys() {
		if key != "description" && key != "effort" {
			diags.Errorf("subagent.frontmatter.unknown-field", path,
				"frontmatter permits only description and effort; found %q", key)
			valid = false
		}
	}
	if doc.Has("description") {
		v, strErr := doc.String("description")
		runeLen := utf8.RuneCountInString(v)
		if strErr != nil || runeLen < 1 || runeLen > 1024 || containsControlRune(v) {
			diags.Errorf("subagent.description.invalid", path,
				"description must be one plain string of 1-1024 characters with no control characters")
			valid = false
		} else {
			out.Description = v
		}
	} else if valid {
		diags.Errorf("subagent.description.missing", path,
			"frontmatter must carry one plain description")
		valid = false
	}
	if doc.Has("effort") {
		v, strErr := doc.String("effort")
		if strErr != nil || (v != "low" && v != "medium" && v != "high") {
			diags.Errorf("subagent.effort.invalid", path,
				"effort must be exactly one of low, medium, or high")
			valid = false
		} else {
			out.Effort = v
		}
	}

	body := content[bodyStart:]
	if after, ok := strings.CutPrefix(body, "\r\n"); ok {
		body = after
	} else {
		body = strings.TrimPrefix(body, "\n")
	}
	if strings.TrimSpace(body) == "" {
		diags.Errorf("subagent.body.empty", path,
			"instructions.md must have a non-empty Markdown body after the frontmatter")
		valid = false
	}
	out.Body = body
	return out, valid
}

// containsControlRune reports whether s carries any Unicode control
// character (the description contract forbids them).
func containsControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
