// Package agentproject discovers and validates filesystem-authored agent
// projects. The authored directory layout is the project API: the directory
// name supplies the agent name and conventional paths register behavior
// without a second inventory. Validation is complete before any workspace
// mutation, and every authored input joins one source fingerprint.
package agentproject

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/frontmatter"
)

// MaxInstructionsBytes bounds the root instructions file (ADR 0013).
const MaxInstructionsBytes = 128 * 1024

// Project is a validated agent project.
type Project struct {
	// Root is the absolute agent source directory.
	Root string
	// Name is the directory-derived agent name, normalized to lowercase
	// hyphenated words.
	Name string
	// Instructions is nil when the root carries no instructions.md; the
	// root must then be proven by a supplied agent manifest.
	Instructions *Instructions
	// Skills are the validated Agent Skills directories, sorted by name.
	Skills []Skill
	// Subagents are the validated immediate subagents/ directories, sorted
	// by name.
	Subagents []Subagent
	// HarnessFiles are the validated harness-specific files, keyed by
	// harness name ("claude", "codex") and sorted by RelPath within each.
	HarnessFiles map[string][]HarnessFile
	// Fingerprint is "sha256:<hex>" over every authored input.
	Fingerprint string
}

// Instructions is a parsed root instructions.md.
type Instructions struct {
	Description   string
	FrictionNotes bool
	// Body is the Markdown body without the frontmatter; generated
	// always-on instructions contain exactly this.
	Body string
}

// componentDirs are the recognized authored component directories. Until a
// component is implemented, its presence fails validation: silently dropping
// authored behavior would pretend the compiled agent is complete.
var componentDirs = []string{
	"plugins", "tools", "connections", "schedules",
}

// Load validates the agent project at dir. Contract violations are reported
// on the diagnostics list; the returned error is reserved for environment
// failures such as unreadable paths.
func Load(dir string) (*Project, *diagnostics.List, error) {
	diags := &diagnostics.List{}

	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, diags, fmt.Errorf("resolving agent root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		diags.Errorf("project.root.missing", ".",
			"the agent root must be an existing directory: %s", dir)
		return nil, diags, nil
	}

	p := &Project{Root: root}

	name, ok := normalizeName(filepath.Base(root))
	if !ok {
		diags.Errorf("project.name.invalid", ".",
			"the directory name must normalize to lowercase hyphenated words starting with a letter: %q", filepath.Base(root))
	}
	p.Name = name

	for _, component := range componentDirs {
		if _, err := os.Lstat(filepath.Join(root, component)); err == nil {
			diags.Errorf("component.unsupported", component,
				"the %s component is not supported yet; tenon refuses to compile a project whose authored behavior it would silently drop", component)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "channels")); err == nil {
		diags.Warnf("component.channel-product", "channels",
			"channels/ belongs to the separately specified channel product and is not compiled by tenon")
	}

	instructions, instructionsBytes := loadInstructions(root, diags)
	p.Instructions = instructions

	if instructions == nil && !diags.HasErrors() {
		// A supplied agent manifest whose expected fingerprint matches the
		// directory also proves an agent root; manifests are not implemented
		// yet, so an instructions-free directory is refused.
		diags.Errorf("project.root.unproven", ".",
			"a directory is an agent project only when instructions.md is present or a supplied agent manifest matches it; neither proof was found")
	}

	skills, skillInputs := loadSkills(root, diags)
	p.Skills = skills

	subagents, subagentInputs := loadSubagents(root, diags)
	p.Subagents = subagents

	harnessFiles, harnessInputs := loadHarnessFiles(root, diags)
	p.HarnessFiles = harnessFiles

	inputs := []sourceInput{
		{Path: "instructions.md", Content: instructionsBytes, Executable: false},
	}
	inputs = append(inputs, skillInputs...)
	inputs = append(inputs, subagentInputs...)
	inputs = append(inputs, harnessInputs...)
	p.Fingerprint = fingerprint(inputs)
	if diags.HasErrors() {
		return nil, diags, nil
	}
	return p, diags, nil
}

// loadInstructions returns the parsed instructions and their exact source
// bytes, or (nil, nil) when the file is absent or invalid.
func loadInstructions(root string, diags *diagnostics.List) (*Instructions, []byte) {
	const path = "instructions.md"
	full := filepath.Join(root, path)

	info, err := os.Lstat(full)
	if err != nil {
		return nil, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		diags.Errorf("instructions.not-regular", path,
			"instructions.md must be a regular file; symlinks are never followed")
		return nil, nil
	}
	if info.Size() > MaxInstructionsBytes {
		diags.Errorf("instructions.too-large", path,
			"instructions.md may contain at most %d bytes; found %d", MaxInstructionsBytes, info.Size())
		return nil, nil
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		diags.Errorf("instructions.unreadable", path, "instructions.md could not be read: %v", err)
		return nil, nil
	}
	if !utf8.Valid(raw) {
		diags.Errorf("instructions.encoding", path, "instructions.md must be valid UTF-8")
		return nil, nil
	}

	parsed, ok := parseInstructions(string(raw), path, diags)
	if !ok {
		return nil, nil
	}
	return parsed, raw
}

// parseInstructions enforces the closed frontmatter contract: one plain
// description, an optional Boolean friction-notes, and a non-empty body.
func parseInstructions(content, path string, diags *diagnostics.List) (*Instructions, bool) {
	raw, bodyStart, err := frontmatter.Split([]byte(content))
	if err != nil {
		diags.Errorf("instructions.frontmatter.missing", path,
			"instructions.md must start with YAML frontmatter delimited by --- lines")
		return nil, false
	}
	doc, err := frontmatter.Parse(raw)
	if err != nil {
		diags.Errorf("instructions.frontmatter.invalid", path, "%s", err)
		return nil, false
	}

	out := &Instructions{}
	valid := true
	for _, key := range doc.Keys() {
		if key != "description" && key != "friction-notes" {
			diags.Errorf("instructions.frontmatter.unknown-field", path,
				"frontmatter permits only description and friction-notes; found %q", key)
			valid = false
		}
	}
	if doc.Has("description") {
		if v, err := doc.String("description"); err == nil && v != "" {
			out.Description = v
		} else {
			diags.Errorf("instructions.description.invalid", path,
				"description must be one plain non-empty string")
			valid = false
		}
	} else if valid {
		diags.Errorf("instructions.description.missing", path,
			"frontmatter must carry one plain description")
		valid = false
	}
	if doc.Has("friction-notes") {
		if v, err := doc.Bool("friction-notes"); err == nil {
			out.FrictionNotes = v
		} else {
			diags.Errorf("instructions.friction-notes.invalid", path,
				"friction-notes must be the Boolean true or false")
			valid = false
		}
	}

	body := content[bodyStart:]
	if after, ok := strings.CutPrefix(body, "\r\n"); ok {
		body = after
	} else {
		body = strings.TrimPrefix(body, "\n")
	}
	if strings.TrimSpace(body) == "" {
		diags.Errorf("instructions.body.empty", path,
			"instructions.md must have a non-empty Markdown body after the frontmatter")
		valid = false
	}
	out.Body = body
	return out, valid
}

// normalizeName lowercases the directory name and collapses every run of
// characters outside [a-z0-9] into one hyphen.
func normalizeName(base string) (string, bool) {
	var b strings.Builder
	lastHyphen := true // trims leading hyphens
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	name := strings.TrimSuffix(b.String(), "-")
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return "", false
	}
	return name, true
}

// sourceInput is one authored input joining the fingerprint: its authored
// relative path, exact bytes (nil when absent), and whether the authored
// file carries the executable bit.
type sourceInput struct {
	Path       string
	Content    []byte
	Executable bool
}

// fingerprint hashes every authored input into one stable identity, sorted
// by path and covering each input's path, content length, content hash, and
// executable intent ("x" or "-").
func fingerprint(inputs []sourceInput) string {
	inputs = slices.Clone(inputs)
	slices.SortFunc(inputs, func(a, b sourceInput) int {
		return strings.Compare(a.Path, b.Path)
	})
	h := sha256.New()
	for _, in := range inputs {
		mode := "-"
		if in.Executable {
			mode = "x"
		}
		contentHash := sha256.Sum256(in.Content)
		fmt.Fprintf(h, "%s\n%d\n%x\n%s\n", in.Path, len(in.Content), contentHash, mode)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
