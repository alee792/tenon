package agentproject

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/diagnostics"
)

// TestLoadValidToolProject proves the mixed happy path: one TypeScript tool,
// one Python tool, and one Go tool directory, each with its language's
// native dependency files present at the agent root. Tools come back sorted
// by name with the right language and source path, every dependency file
// (including the optional go.sum) joins the fingerprint inputs, and the
// executable bit on an inventoried file is preserved.
func TestLoadValidToolProject(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "tools/hash.ts", []byte("export default {};\n"), 0o644)
	writeSkillFile(t, root, "tools/greet.py", []byte("# tool\n"), 0o644)
	writeSkillFile(t, root, "tools/calc/tool.go", []byte("package calc\n"), 0o644)
	writeSkillFile(t, root, "tools/calc/util.go", []byte("package calc\n"), 0o755)
	writeSkillFile(t, root, "deno.json", []byte("{}"), 0o644)
	writeSkillFile(t, root, "deno.lock", []byte("{}"), 0o644)
	writeSkillFile(t, root, "pyproject.toml", []byte("[project]\n"), 0o644)
	writeSkillFile(t, root, "uv.lock", []byte("version = 1\n"), 0o644)
	writeSkillFile(t, root, "go.mod", []byte("module agent\n"), 0o644)
	writeSkillFile(t, root, "go.sum", []byte(""), 0o644)

	diags := &diagnostics.List{}
	tools, inputs := loadTools(root, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(tools) != 3 {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0].Name != "calc" || tools[1].Name != "greet" || tools[2].Name != "hash" {
		t.Fatalf("tools not sorted by name: %+v", tools)
	}
	if tools[0].Language != "go" || tools[0].SourcePath != "tools/calc" {
		t.Fatalf("calc tool = %+v", tools[0])
	}
	if tools[1].Language != "python" || tools[1].SourcePath != "tools/greet.py" {
		t.Fatalf("greet tool = %+v", tools[1])
	}
	if tools[2].Language != "typescript" || tools[2].SourcePath != "tools/hash.ts" {
		t.Fatalf("hash tool = %+v", tools[2])
	}

	byPath := map[string]sourceInput{}
	for _, in := range inputs {
		byPath[in.Path] = in
	}
	for _, dep := range []string{"deno.json", "deno.lock", "pyproject.toml", "uv.lock", "go.mod", "go.sum"} {
		if _, ok := byPath[dep]; !ok {
			t.Fatalf("expected dependency file %q in fingerprint inputs, got %v", dep, inputs)
		}
	}
	if in, ok := byPath["tools/calc/util.go"]; !ok || !in.Executable {
		t.Fatalf("expected tools/calc/util.go to be inventoried as executable: %+v", in)
	}
	if in, ok := byPath["tools/calc/tool.go"]; !ok || in.Executable {
		t.Fatalf("expected tools/calc/tool.go to be inventoried as non-executable: %+v", in)
	}
}

// TestLoadToolsUnderscoreHelpersNotDeclared proves that files whose basename
// starts with "_" are inventoried into the fingerprint but declare no tool.
func TestLoadToolsUnderscoreHelpersNotDeclared(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "tools/_helpers.ts", []byte("export const shared = 1;\n"), 0o644)
	writeSkillFile(t, root, "tools/_util.py", []byte("# shared\n"), 0o644)
	writeSkillFile(t, root, "tools/real.ts", []byte("export default {};\n"), 0o644)
	writeSkillFile(t, root, "deno.json", []byte("{}"), 0o644)
	writeSkillFile(t, root, "deno.lock", []byte("{}"), 0o644)

	diags := &diagnostics.List{}
	tools, inputs := loadTools(root, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(tools) != 1 || tools[0].Name != "real" {
		t.Fatalf("tools = %+v", tools)
	}
	var paths []string
	for _, in := range inputs {
		paths = append(paths, in.Path)
	}
	for _, want := range []string{"tools/_helpers.ts", "tools/_util.py"} {
		found := false
		for _, p := range paths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected helper file %q inventoried, got %v", want, paths)
		}
	}
}

// TestLoadToolsUnderscoreToHyphenName proves the filename-to-name mapping:
// underscores in the basename become hyphens in the exposed tool name.
func TestLoadToolsUnderscoreToHyphenName(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "tools/hash_text.ts", []byte("export default {};\n"), 0o644)
	writeSkillFile(t, root, "deno.json", []byte("{}"), 0o644)
	writeSkillFile(t, root, "deno.lock", []byte("{}"), 0o644)

	diags := &diagnostics.List{}
	tools, _ := loadTools(root, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if len(tools) != 1 || tools[0].Name != "hash-text" {
		t.Fatalf("tools = %+v", tools)
	}
}

// TestLoadAllowsAbsentToolsDirectory proves tools/ is optional.
func TestLoadAllowsAbsentToolsDirectory(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	diags := &diagnostics.List{}
	tools, inputs := loadTools(root, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	if tools != nil || inputs != nil {
		t.Fatalf("expected (nil, nil) for an absent tools directory, got (%v, %v)", tools, inputs)
	}
}

func TestLoadRejectsToolViolations(t *testing.T) {
	cases := map[string]struct {
		setup func(t *testing.T, root string)
		id    string
	}{
		"unrecognized extension": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/notes.txt", []byte("hi\n"), 0o644)
		}, "tool.entry.invalid"},
		"invalid name uppercase": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/Hash.ts", []byte("export default {};\n"), 0o644)
		}, "tool.name.invalid"},
		"invalid name leading digit": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/1hash.py", []byte("# tool\n"), 0o644)
		}, "tool.name.invalid"},
		"reserved echo": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/echo.ts", []byte("export default {};\n"), 0o644)
		}, "tool.name.reserved"},
		"reserved record-friction underscore normalized": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/record_friction.py", []byte("# tool\n"), 0o644)
		}, "tool.name.reserved"},
		"duplicate cross-language": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/same.ts", []byte("export default {};\n"), 0o644)
			writeSkillFile(t, root, "tools/same.py", []byte("# tool\n"), 0o644)
			writeSkillFile(t, root, "pyproject.toml", []byte("[project]\n"), 0o644)
			writeSkillFile(t, root, "uv.lock", []byte("version = 1\n"), 0o644)
		}, "tool.name.duplicate"},
		"duplicate underscore normalized": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/under_score.ts", []byte("export default {};\n"), 0o644)
			writeSkillFile(t, root, "tools/under-score.py", []byte("# tool\n"), 0o644)
			writeSkillFile(t, root, "pyproject.toml", []byte("[project]\n"), 0o644)
			writeSkillFile(t, root, "uv.lock", []byte("version = 1\n"), 0o644)
		}, "tool.name.duplicate"},
		"go dir missing tool.go": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/mytool/notes.txt", []byte("hi\n"), 0o644)
		}, "tool.go.invalid"},
		"go dir nested directory": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/mytool/tool.go", []byte("package mytool\n"), 0o644)
			writeSkillFile(t, root, "tools/mytool/nested/other.go", []byte("package nested\n"), 0o644)
		}, "tool.go.invalid"},
		"missing deno.json": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/hash.ts", []byte("export default {};\n"), 0o644)
			writeSkillFile(t, root, "deno.lock", []byte("{}"), 0o644)
		}, "tool.dependencies.missing"},
		"missing deno.lock": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/hash.ts", []byte("export default {};\n"), 0o644)
			writeSkillFile(t, root, "deno.json", []byte("{}"), 0o644)
		}, "tool.dependencies.missing"},
		"missing pyproject.toml": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/greet.py", []byte("# tool\n"), 0o644)
			writeSkillFile(t, root, "uv.lock", []byte("version = 1\n"), 0o644)
		}, "tool.dependencies.missing"},
		"missing uv.lock": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/greet.py", []byte("# tool\n"), 0o644)
			writeSkillFile(t, root, "pyproject.toml", []byte("[project]\n"), 0o644)
		}, "tool.dependencies.missing"},
		"missing go.mod": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/calc/tool.go", []byte("package calc\n"), 0o644)
		}, "tool.dependencies.missing"},
		"symlinked entry": {func(t *testing.T, root string) {
			target := filepath.Join(t.TempDir(), "real.ts")
			if err := os.WriteFile(target, []byte("export default {};\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, "tools", "hash.ts")); err != nil {
				t.Fatal(err)
			}
		}, "tool.entry.invalid"},
		"symlinked dependency file": {func(t *testing.T, root string) {
			writeSkillFile(t, root, "tools/calc/tool.go", []byte("package calc\n"), 0o644)
			target := filepath.Join(t.TempDir(), "real.mod")
			if err := os.WriteFile(target, []byte("module agent\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, "go.mod")); err != nil {
				t.Fatal(err)
			}
		}, "tool.file.invalid"},
		"tools not a directory": {func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "tools"), []byte("oops"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "tool.entry.invalid"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeAgent(t, "agent", validInstructions)
			tc.setup(t, root)
			diags := &diagnostics.List{}
			loadTools(root, diags)
			requireErrorID(t, diags, tc.id)
		})
	}
}

// TestLoadRejectsTooManyTools proves the real (non-sparse) 128-tool ceiling:
// 129 distinct, empty TypeScript tool files stay far under every byte and
// file-count budget, isolating the tool-count ceiling.
func TestLoadRejectsTooManyTools(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	for i := 0; i <= MaxTools; i++ {
		writeSkillFile(t, root, fmt.Sprintf("tools/tool%03d.ts", i), nil, 0o644)
	}
	writeSkillFile(t, root, "deno.json", []byte("{}"), 0o644)
	writeSkillFile(t, root, "deno.lock", []byte("{}"), 0o644)

	diags := &diagnostics.List{}
	tools, _ := loadTools(root, diags)
	if len(tools) != MaxTools+1 {
		// every one of them is individually valid; only the aggregate count
		// diagnostic marks the project invalid, so the discovered tools are
		// still returned.
		t.Fatalf("tools = %d, want %d", len(tools), MaxTools+1)
	}
	requireErrorID(t, diags, "tool.bounds.exceeded")
}

// TestLoadRejectsOversizedToolFile proves the per-file byte ceiling from
// file metadata: the sparse oversized file is rejected without being read,
// so it declares no tool.
func TestLoadRejectsOversizedToolFile(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "tools/big.ts", []byte("export default {};\n"), 0o644)
	if err := os.Truncate(filepath.Join(root, "tools", "big.ts"), MaxToolFileBytes+1); err != nil {
		t.Fatal(err)
	}
	diags := &diagnostics.List{}
	tools, _ := loadTools(root, diags)
	if len(tools) != 0 {
		t.Fatalf("expected no tools declared from an out-of-bounds file: %+v", tools)
	}
	requireErrorID(t, diags, "tool.bounds.exceeded")
}

// TestLoadRejectsToolInventoryAggregateByteCeiling proves the aggregate
// math with sparse files at the real 1 MiB per-file ceiling: 65 files of
// exactly MaxToolFileBytes stay within the per-file bound individually
// (64 * 1 MiB == 64 MiB, the aggregate cap exactly) but the 65th crosses the
// 64 MiB tool-source inventory budget.
func TestLoadRejectsToolInventoryAggregateByteCeiling(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	const count = 65
	if int64(count-1)*MaxToolFileBytes != MaxToolInventoryBytes {
		t.Fatalf("test setup must cross the aggregate ceiling only at the last of %d files", count)
	}
	for i := 0; i < count; i++ {
		path := filepath.Join(root, "tools", fmt.Sprintf("big%02d.ts", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, MaxToolFileBytes); err != nil {
			t.Fatal(err)
		}
	}
	writeSkillFile(t, root, "deno.json", []byte("{}"), 0o644)
	writeSkillFile(t, root, "deno.lock", []byte("{}"), 0o644)

	diags := &diagnostics.List{}
	loadTools(root, diags)
	found := false
	for _, d := range diags.All() {
		if d.ID == "tool.bounds.exceeded" && strings.Contains(d.Rule, "bytes") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the tool-source inventory aggregate byte ceiling, got %v", diags.All())
	}
}

// TestToolNames proves the exported helper's shape: exposed tool name to
// authored source path, for the later tool-vs-subagent collision check.
func TestToolNames(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSkillFile(t, root, "tools/hash.ts", []byte("export default {};\n"), 0o644)
	writeSkillFile(t, root, "deno.json", []byte("{}"), 0o644)
	writeSkillFile(t, root, "deno.lock", []byte("{}"), 0o644)

	diags := &diagnostics.List{}
	tools, _ := loadTools(root, diags)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	names := toolNames(tools)
	if names["hash"] != "tools/hash.ts" {
		t.Fatalf("toolNames = %v", names)
	}
}
