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
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
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
	"skills", "plugins", "tools", "subagents", "connections", "schedules", "harnesses",
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

	p.Fingerprint = fingerprint(map[string][]byte{"instructions.md": instructionsBytes})
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
	body, fields, ok := splitFrontmatter(content)
	if !ok {
		diags.Errorf("instructions.frontmatter.missing", path,
			"instructions.md must start with YAML frontmatter delimited by --- lines")
		return nil, false
	}

	out := &Instructions{}
	seen := map[string]bool{}
	valid := true
	for _, f := range fields {
		key, value, found := strings.Cut(f, ":")
		if !found {
			diags.Errorf("instructions.frontmatter.invalid", path,
				"frontmatter lines must be plain 'key: value' mappings; found %q", diagnostics.Bound(f, 80))
			valid = false
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if seen[key] {
			diags.Errorf("instructions.frontmatter.invalid", path,
				"frontmatter field %q is duplicated", key)
			valid = false
			continue
		}
		seen[key] = true
		switch key {
		case "description":
			if v, ok := plainScalar(value); ok && v != "" {
				out.Description = v
			} else {
				diags.Errorf("instructions.description.invalid", path,
					"description must be one plain non-empty string")
				valid = false
			}
		case "friction-notes":
			switch value {
			case "true":
				out.FrictionNotes = true
			case "false":
				out.FrictionNotes = false
			default:
				diags.Errorf("instructions.friction-notes.invalid", path,
					"friction-notes must be the Boolean true or false; found %q", diagnostics.Bound(value, 80))
				valid = false
			}
		default:
			diags.Errorf("instructions.frontmatter.unknown-field", path,
				"frontmatter permits only description and friction-notes; found %q", key)
			valid = false
		}
	}
	if !seen["description"] && valid {
		diags.Errorf("instructions.description.missing", path,
			"frontmatter must carry one plain description")
		valid = false
	}

	body = strings.TrimPrefix(body, "\n")
	if strings.TrimSpace(body) == "" {
		diags.Errorf("instructions.body.empty", path,
			"instructions.md must have a non-empty Markdown body after the frontmatter")
		valid = false
	}
	out.Body = body
	return out, valid
}

// splitFrontmatter cuts "---\n<fields>\n---\n<body>" into its parts. Field
// lines are returned raw; blank lines and comment-free simplicity are the
// contract — this is deliberately a closed subset, not a YAML engine.
func splitFrontmatter(content string) (body string, fields []string, ok bool) {
	rest, found := strings.CutPrefix(content, "---\n")
	if !found {
		return "", nil, false
	}
	head, body, found := strings.Cut(rest, "\n---\n")
	if !found {
		if trimmed, endFound := strings.CutSuffix(rest, "\n---"); endFound {
			head, body = trimmed, ""
		} else {
			return "", nil, false
		}
	}
	for _, line := range strings.Split(head, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields = append(fields, line)
	}
	return body, fields, true
}

// plainScalar accepts an unquoted or double-quoted plain string and rejects
// YAML indicators that would need a real YAML engine to interpret.
func plainScalar(value string) (string, bool) {
	if v, ok := strings.CutPrefix(value, `"`); ok {
		v, ok = strings.CutSuffix(v, `"`)
		return v, ok && !strings.Contains(v, `"`)
	}
	if value == "" {
		return "", true
	}
	switch value[0] {
	case '&', '*', '!', '|', '>', '[', '{', '\'', '#', '%', '@', '`':
		return "", false
	}
	return value, true
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

// fingerprint hashes every authored input into one stable identity. Inputs
// map authored relative paths to exact bytes; absent inputs use nil.
func fingerprint(inputs map[string][]byte) string {
	paths := make([]string, 0, len(inputs))
	for p := range inputs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		content := inputs[p]
		contentHash := sha256.Sum256(content)
		fmt.Fprintf(h, "%s\n%d\n%x\n", p, len(content), contentHash)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
