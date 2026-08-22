package agentproject

// Tools are the static half of authored-tools discovery: this file finds
// and validates tools/ under an agent root, but never parses tool bodies,
// runs a language host, or generates anything. Visible tools/*.ts,
// tools/*.py files and tools/NAME/tool.go directories each declare one
// tool; filenames (or the Go directory name) supply tool names, with
// underscores exposed as hyphens. Dependencies use the native lockfiles
// (deno.json/deno.lock, pyproject.toml/uv.lock, go.mod/go.sum); there is no
// authored manifest, registry, or duplicated tool inventory.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
)

// Tool bounds (ADR 0013): safety ceilings, not ordinary-use quotas.
// Violations fail before any workspace mutation.
const (
	// MaxTools bounds the number of tools declared under tools/.
	MaxTools = 128
	// MaxToolInventoryFiles bounds the tool-source inventory: every tool
	// source file plus every native dependency file, together.
	MaxToolInventoryFiles = 1024
	// MaxToolFileBytes bounds one tool source or dependency file.
	MaxToolFileBytes = 1 << 20
	// MaxToolInventoryBytes bounds the tool-source inventory in aggregate.
	MaxToolInventoryBytes = 64 << 20
)

// Tool is one validated authored tool.
type Tool struct {
	// Name is the exposed tool name: the filename (or Go tool directory
	// name) minus extension, with underscores replaced by hyphens.
	Name string
	// Language is "typescript", "python", or "go".
	Language string
	// SourcePath is the authored path relative to the agent root:
	// "tools/hash_text.ts" or "tools/hash_text" for a Go tool directory.
	SourcePath string
}

// toolDependencySpec names the native dependency files a language's tools
// require at the agent root (not inside tools/): every required file must
// be present, and every optional file is inventoried into the fingerprint
// when present but does not by itself gate validity.
type toolDependencySpec struct {
	language string
	required []string
	optional []string
}

// toolDependencySpecs enumerates the recognized tool languages' native
// dependency files. Each spec applies only when the language is actually
// present among the discovered tools.
var toolDependencySpecs = []toolDependencySpec{
	{language: "typescript", required: []string{"deno.json", "deno.lock"}},
	{language: "python", required: []string{"pyproject.toml", "uv.lock"}},
	{language: "go", required: []string{"go.mod"}, optional: []string{"go.sum"}},
}

// toolInventoryBudget tracks the file-count and byte budget shared across
// every tool source and native dependency file (ADR 0013), so the whole
// tool-source inventory stays bounded even when each file is individually
// small.
type toolInventoryBudget struct {
	files         int
	bytes         int64
	filesExceeded bool
	bytesExceeded bool
}

// loadTools discovers and validates tools/ under root, returning tools
// sorted by name and every inventoried file (tool sources and native
// dependency files) as a fingerprint input. Invalid tools reject the
// project: they are authored project source, not isolatable plugin
// components.
func loadTools(root string, diags *diagnostics.List) ([]Tool, []sourceInput) {
	dir := filepath.Join(root, "tools")
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Errorf("tool.entry.invalid", "tools",
			"tools must be a real directory; symlinks are never followed")
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		diags.Errorf("tool.entry.invalid", "tools", "tools could not be read: %v", err)
		return nil, nil
	}

	budget := &toolInventoryBudget{}
	seen := map[string]string{} // tool name -> first authored source path
	langsPresent := map[string]bool{}
	var tools []Tool
	var inputs []sourceInput
	count := 0

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "__pycache__" {
			continue
		}
		entryPath := "tools/" + name
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("tool.entry.invalid", entryPath,
				"each tools entry must be a real TypeScript file, Python file, or Go tool directory; symlinks are never followed")
			continue
		}
		if !utf8.ValidString(name) {
			diags.Errorf("tool.file.invalid", entryPath, "every tools entry name must be valid UTF-8")
			continue
		}

		if entry.IsDir() {
			count++
			if count == MaxTools+1 {
				diags.Errorf("tool.bounds.exceeded", "tools",
					"tools may contain at most %d tools", MaxTools)
			}
			contentOK, toolInputs := loadGoToolDir(dir, name, budget, diags)
			inputs = append(inputs, toolInputs...)
			tool, nameOK := registerToolName(name, entryPath, "go", seen, diags)
			if contentOK && nameOK {
				tools = append(tools, tool)
				langsPresent["go"] = true
			}
			continue
		}
		if !entry.Type().IsRegular() {
			diags.Errorf("tool.entry.invalid", entryPath,
				"tools may contain TypeScript, Python, or Go tool directories")
			continue
		}

		ext := filepath.Ext(name)
		var lang string
		switch ext {
		case ".ts":
			lang = "typescript"
		case ".py":
			lang = "python"
		default:
			diags.Errorf("tool.entry.invalid", entryPath,
				"tools may contain TypeScript, Python, or Go tool directories")
			continue
		}

		fileInfo, err := entry.Info()
		if err != nil {
			diags.Errorf("tool.file.invalid", entryPath, "the tool file could not be read: %v", err)
			continue
		}
		content, executable, sizeOK := inventoryFile(dir, name, entryPath, fileInfo, budget, diags)
		inputs = append(inputs, sourceInput{Path: entryPath, Content: content, Executable: executable})
		if !sizeOK {
			continue
		}
		if strings.HasPrefix(name, "_") {
			continue // shared helper module: inventoried, declares no tool
		}

		count++
		if count == MaxTools+1 {
			diags.Errorf("tool.bounds.exceeded", "tools",
				"tools may contain at most %d tools", MaxTools)
		}
		base := strings.TrimSuffix(name, ext)
		tool, nameOK := registerToolName(base, entryPath, lang, seen, diags)
		if nameOK {
			tools = append(tools, tool)
			langsPresent[lang] = true
		}
	}

	slices.SortFunc(tools, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })

	inputs = append(inputs, loadToolDependencies(root, langsPresent, budget, diags)...)

	return tools, inputs
}

// loadGoToolDir validates one immediate tools/ directory entry as a Go tool:
// it must contain a regular tool.go file, nested directories are rejected,
// and every other non-dot regular file is inventoried with its executable
// bit. Every file is returned as a fingerprint input regardless of validity
// so identity tracks exactly what was authored.
func loadGoToolDir(toolsDir, dirName string, budget *toolInventoryBudget, diags *diagnostics.List) (bool, []sourceInput) {
	sourcePath := "tools/" + dirName
	full := filepath.Join(toolsDir, dirName)
	entries, err := os.ReadDir(full)
	if err != nil {
		diags.Errorf("tool.go.invalid", sourcePath, "the tool directory could not be read: %v", err)
		return false, nil
	}

	valid := true
	hasToolGo := false
	var inputs []sourceInput
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		childPath := sourcePath + "/" + name
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("tool.file.invalid", childPath,
				"every file inside a Go tool directory must be a real regular file; symlinks are never followed")
			valid = false
			continue
		}
		if !utf8.ValidString(name) {
			diags.Errorf("tool.file.invalid", childPath, "every path inside a Go tool directory must be valid UTF-8")
			valid = false
			continue
		}
		if entry.IsDir() {
			diags.Errorf("tool.go.invalid", childPath,
				"a Go tool directory may not contain nested directories")
			valid = false
			continue
		}
		if !entry.Type().IsRegular() {
			diags.Errorf("tool.file.invalid", childPath,
				"every file inside a Go tool directory must be a real regular file")
			valid = false
			continue
		}
		if name == "tool.go" {
			hasToolGo = true
		}
		info, err := entry.Info()
		if err != nil {
			diags.Errorf("tool.file.invalid", childPath, "the tool file could not be read: %v", err)
			valid = false
			continue
		}
		content, executable, ok := inventoryFile(full, name, childPath, info, budget, diags)
		inputs = append(inputs, sourceInput{Path: childPath, Content: content, Executable: executable})
		if !ok {
			valid = false
		}
	}
	if !hasToolGo {
		diags.Errorf("tool.go.invalid", sourcePath,
			"a Go tool directory requires a regular tool.go file at %s/tool.go", sourcePath)
		valid = false
	}
	return valid, inputs
}

// registerToolName normalizes base into a tool name (underscores exposed as
// hyphens), validates the name grammar, refuses names reserved for a
// managed built-in tool, and refuses a name already declared by another
// tool source, naming both authored paths.
func registerToolName(base, sourcePath, language string, seen map[string]string, diags *diagnostics.List) (Tool, bool) {
	name := strings.ReplaceAll(base, "_", "-")
	if !subagentNamePattern.MatchString(name) {
		diags.Errorf("tool.name.invalid", sourcePath,
			"a tool name must be 1-63 characters, starting with a lowercase letter and continuing with lowercase letters, digits, or hyphens: %q", name)
		return Tool{}, false
	}
	if reservedSubagentNames[name] {
		diags.Errorf("tool.name.reserved", sourcePath,
			"the name %q is reserved for a managed built-in tool", name)
		return Tool{}, false
	}
	if first, dup := seen[name]; dup {
		diags.Errorf("tool.name.duplicate", sourcePath,
			"tool name %q is already declared by %s", name, first)
		return Tool{}, false
	}
	seen[name] = sourcePath
	return Tool{Name: name, Language: language, SourcePath: sourcePath}, true
}

// inventoryFile applies the shared tool-source inventory bounds (ADR 0013)
// to one file already confirmed to be a real, non-symlink regular file:
// 1 MiB per file, and 1,024 files and 64 MiB in aggregate across every tool
// source and native dependency file together. Size bounds come from file
// metadata so an out-of-bounds file is rejected before it is read. It
// returns the file's content (nil when it could not be safely read), its
// executable bit, and whether the file is within bounds and was read.
func inventoryFile(dir, name, sourcePath string, info os.FileInfo, budget *toolInventoryBudget, diags *diagnostics.List) ([]byte, bool, bool) {
	readable := true
	if info.Size() > MaxToolFileBytes {
		diags.Errorf("tool.bounds.exceeded", sourcePath,
			"a tool source or dependency file may contain at most %d bytes; found %d", MaxToolFileBytes, info.Size())
		readable = false
	}

	budget.files++
	if budget.files == MaxToolInventoryFiles+1 {
		diags.Errorf("tool.bounds.exceeded", "tools",
			"the tool-source inventory may contain at most %d files", MaxToolInventoryFiles)
		budget.filesExceeded = true
	}

	budget.bytes += info.Size()
	if !budget.bytesExceeded && budget.bytes > MaxToolInventoryBytes {
		diags.Errorf("tool.bounds.exceeded", "tools",
			"the tool-source inventory may contain at most %d bytes in aggregate", MaxToolInventoryBytes)
		budget.bytesExceeded = true
	}

	executable := info.Mode().Perm()&0o111 != 0
	if !readable || budget.filesExceeded || budget.bytesExceeded {
		return nil, executable, false
	}
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		diags.Errorf("tool.file.invalid", sourcePath, "the file could not be read: %v", err)
		return nil, executable, false
	}
	return content, executable, true
}

// loadToolDependencies validates the native dependency files at the agent
// root for every language actually present among the discovered tools:
// every required file must exist, and every present file (required or
// optional) is inventoried into the fingerprint.
func loadToolDependencies(root string, langsPresent map[string]bool, budget *toolInventoryBudget, diags *diagnostics.List) []sourceInput {
	var inputs []sourceInput
	for _, spec := range toolDependencySpecs {
		if !langsPresent[spec.language] {
			continue
		}
		for _, filename := range spec.required {
			if in, ok := loadToolDependencyFile(root, filename, spec.language, true, budget, diags); ok {
				inputs = append(inputs, in)
			}
		}
		for _, filename := range spec.optional {
			if in, ok := loadToolDependencyFile(root, filename, spec.language, false, budget, diags); ok {
				inputs = append(inputs, in)
			}
		}
	}
	return inputs
}

// loadToolDependencyFile validates one native dependency file at the agent
// root: it must be a real regular file when present, and a required file
// that is absent is an error naming the file and language. A present file,
// required or optional, is inventoried through the shared bounds.
func loadToolDependencyFile(root, filename, language string, required bool, budget *toolInventoryBudget, diags *diagnostics.List) (sourceInput, bool) {
	full := filepath.Join(root, filename)
	info, err := os.Lstat(full)
	if err != nil {
		if required {
			diags.Errorf("tool.dependencies.missing", filename,
				"%s tools require %s at the agent root; none was found", language, filename)
		}
		return sourceInput{}, false
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		diags.Errorf("tool.file.invalid", filename,
			"%s must be a regular file; symlinks are never followed", filename)
		return sourceInput{}, false
	}
	content, executable, ok := inventoryFile(root, filename, filename, info, budget, diags)
	if !ok {
		return sourceInput{}, false
	}
	return sourceInput{Path: filename, Content: content, Executable: executable}, true
}

// toolNames returns each validated tool's exposed name mapped to its
// authored source path, for the tool-vs-subagent name collision check a
// later wiring slice performs once both discoveries have run.
func toolNames(tools []Tool) map[string]string {
	out := make(map[string]string, len(tools))
	for _, t := range tools {
		out[t.Name] = t.SourcePath
	}
	return out
}
