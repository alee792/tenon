package main

// Authored tools are proven through the literal journey: an author writes a
// file under tools/, apply prepares and inspects it once, and the managed
// server lists and calls it. The Go leg always runs — its toolchain is
// tenon's own — while the TypeScript and Python legs skip with a clear
// message when deno or uv is absent.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const goToolFile = `package hash_text

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

var Description = "Hash bounded text with SHA-256."

type Input struct {
	Text string ` + "`json:\"text\"`" + `
}

type Output struct {
	Hex   string ` + "`json:\"hex\"`" + `
	Calls int    ` + "`json:\"calls\"`" + `
}

// calls counts this process's invocations, so a second call proves the host
// process was reused rather than restarted.
var calls int

func Execute(ctx context.Context, in Input) (Output, error) {
	calls++
	sum := sha256.Sum256([]byte(in.Text))
	return Output{Hex: hex.EncodeToString(sum[:]), Calls: calls}, nil
}
`

const brokenGoToolFile = `package hash_text

import "context"

var Description = "Hash bounded text with SHA-256."

type Input struct {
	Text string ` + "`json:\"text\"`" + `
}

type Output struct {
	Hex string ` + "`json:\"hex\"`" + `
}

// Execute returns an int where the contract requires Output: the authored
// module does not compile.
func Execute(ctx context.Context, in Input) (Output, error) {
	return 7, nil
}
`

const typescriptToolFile = `import { z } from "zod";

let calls = 0;

export default {
  description: "Uppercase bounded text.",
  inputSchema: z.object({ text: z.string().max(64) }).strict(),
  outputSchema: z.object({ shouted: z.string(), calls: z.number() }).strict(),
  execute(input: { text: string }, _context: { requestId: string }) {
    calls += 1;
    return { shouted: input.text.toUpperCase(), calls };
  },
};
`

const pythonToolFile = `"""Count the words in bounded text."""

from pydantic import BaseModel

description = "Count the words in bounded text."

calls = 0


class Input(BaseModel):
    text: str


class Output(BaseModel):
    words: int
    calls: int


def execute(input: Input, context: dict) -> Output:
    global calls
    calls += 1
    return Output(words=len(input.text.split()), calls=calls)
`

// writeGoTool gives an agent one Go tool and the module it belongs to.
func writeGoTool(t *testing.T, agent, source string) {
	t.Helper()
	writeFile(t, agent, "go.mod", []byte("module example.com/"+filepath.Base(agent)+"\n\ngo 1.24\n"), 0o644)
	writeFile(t, agent, "tools/hash_text/tool.go", []byte(source), 0o644)
}

// writeGoToolWithOwnModuleImport gives an agent one Go tool that imports a
// sibling package from its own module outside tools/ — permitted by
// docs/product-spec.md (which constrains only tools/'s own shape, not what
// a tool imports) — proving the build source toolruntime prepares against
// carries the whole module, not only the tool's own directory.
func writeGoToolWithOwnModuleImport(t *testing.T, agent string) {
	t.Helper()
	module := "example.com/" + filepath.Base(agent)
	writeFile(t, agent, "go.mod", []byte("module "+module+"\n\ngo 1.24\n"), 0o644)
	writeFile(t, agent, "internal/shout/shout.go", []byte(`package shout

import "strings"

func Text(s string) string { return strings.ToUpper(s) }
`), 0o644)
	writeFile(t, agent, "tools/shout_text/tool.go", []byte(`package shout_text

import (
	"context"

	"`+module+`/internal/shout"
)

var Description = "Shout bounded text."

type Input struct {
	Text string `+"`json:\"text\"`"+`
}

type Output struct {
	Shouted string `+"`json:\"shouted\"`"+`
}

func Execute(ctx context.Context, in Input) (Output, error) {
	return Output{Shouted: shout.Text(in.Text)}, nil
}
`), 0o644)
}

// serveManaged runs one managed session over the given request lines and
// returns the decoded responses.
func serveManaged(t *testing.T, agent, harness, workspace string, requests ...string) []map[string]any {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run([]string{"mcp", "serve", agent, "--harness", harness, "--workspace", workspace},
		bytes.NewBufferString(strings.Join(requests, "\n")+"\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("mcp serve exit %d\nstderr: %s", code, stderr.String())
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("response %q is not JSON: %v", line, err)
		}
		responses = append(responses, decoded)
	}
	return responses
}

func listedTool(t *testing.T, response map[string]any, name string) map[string]any {
	t.Helper()
	for _, listed := range response["result"].(map[string]any)["tools"].([]any) {
		tool := listed.(map[string]any)
		if tool["name"] == name {
			return tool
		}
	}
	t.Fatalf("the managed surface does not carry %q: %#v", name, response)
	return nil
}

func callResult(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no result: %#v", response)
	}
	if result["isError"] == true {
		t.Fatalf("the tool call failed: %#v", result)
	}
	return result["structuredContent"].(map[string]any)
}

func toolCall(id int, name, arguments string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"` + name +
		`","arguments":` + arguments + `}}`
}

const listRequest = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

// TestGoToolIsPreparedByApplyAndServedByTheManagedBoundary is the authored-tool
// journey end to end: one file under tools/, one apply, and the managed server
// lists the tool with the schemas reflected from its own types, round-trips a
// call, and serves the second call from the same host process.
func TestGoToolIsPreparedByApplyAndServedByTheManagedBoundary(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeGoTool(t, agent, goToolFile)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "managed tools: echo, hash-text via MCP") {
		t.Fatalf("apply must report the authored tool after the built-ins: %q", stdout.String())
	}

	responses := serveManaged(t, agent, "claude", ws, listRequest,
		toolCall(2, "hash-text", `{"text":"hi"}`),
		toolCall(3, "hash-text", `{"text":"hi"}`),
		toolCall(4, "hash-text", `{"nope":1}`))

	listed := listedTool(t, responses[0], "hash-text")
	if listed["description"] != "Hash bounded text with SHA-256." {
		t.Fatalf("description = %v", listed["description"])
	}
	input := listed["inputSchema"].(map[string]any)
	if input["type"] != "object" || input["additionalProperties"] != false {
		t.Fatalf("inputSchema = %#v, want a closed object schema", input)
	}
	if input["properties"].(map[string]any)["text"].(map[string]any)["type"] != "string" {
		t.Fatalf("inputSchema properties = %#v", input["properties"])
	}
	output := listed["outputSchema"].(map[string]any)["properties"].(map[string]any)
	if output["hex"].(map[string]any)["type"] != "string" || output["calls"].(map[string]any)["type"] != "integer" {
		t.Fatalf("outputSchema properties = %#v", output)
	}

	first := callResult(t, responses[1])
	if first["hex"] != "8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4" {
		t.Fatalf("hash-text output = %#v", first)
	}
	if first["calls"] != float64(1) {
		t.Fatalf("first call count = %v, want 1", first["calls"])
	}
	// One host serves the whole session: the second call sees the first.
	if second := callResult(t, responses[2]); second["calls"] != float64(2) {
		t.Fatalf("second call count = %v, want 2 from the same host process", second["calls"])
	}
	refused := responses[3]["result"].(map[string]any)
	if refused["isError"] != true {
		t.Fatalf("an unknown argument must be refused: %#v", refused)
	}
}

// TestBrokenToolFailsValidateAndApplyIdentically proves validate reports
// apply's tool failures while writing nothing: same stable identifier, same
// bounded message, no cache and no generated files left behind.
func TestBrokenToolFailsValidateAndApplyIdentically(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeGoTool(t, agent, brokenGoToolFile)

	var validateOut, applyOut, stderr bytes.Buffer
	validateCode := run([]string{"validate", agent, "--harness", "claude", "--diagnostics", "jsonl"}, nil, &validateOut, &stderr)
	if validateCode == 0 {
		t.Fatalf("a tool that does not compile must fail validate: %q", validateOut.String())
	}
	diags := filterDiags(parseDiagLines(t, validateOut.String()), "tool.prepare.failed")
	if len(diags) != 1 || diags[0].Path != "tools" {
		t.Fatalf("expected one tool.prepare.failed at tools, got %q", validateOut.String())
	}
	if !strings.Contains(diags[0].Rule, "go") {
		t.Fatalf("the diagnostic must name the language: %q", diags[0].Rule)
	}
	// Validate writes nothing: no cache, no state, no generated files.
	for _, path := range []string{".tenon", "CLAUDE.md", ".mcp.json"} {
		if _, err := os.Stat(filepath.Join(agent, path)); !os.IsNotExist(err) {
			t.Fatalf("validate must not write %s into the workspace", path)
		}
	}

	ws := t.TempDir()
	applyCode := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--diagnostics", "jsonl"}, nil, &applyOut, &stderr)
	if applyCode == 0 {
		t.Fatal("a tool that does not compile must fail apply")
	}
	if validateOut.String() != applyOut.String() {
		t.Fatalf("validate and apply must report identical diagnostics:\n%s\n%s",
			validateOut.String(), applyOut.String())
	}
	// Preparation happens before apply mutates anything, so no generated
	// native file and no apply record exist.
	for _, path := range []string{"CLAUDE.md", ".mcp.json", ".tenon/apply-claude.json"} {
		if _, err := os.Stat(filepath.Join(ws, path)); !os.IsNotExist(err) {
			t.Fatalf("a failing apply must not write %s", path)
		}
	}
}

// TestBrokenToolFailsFingerprintShowToo proves fingerprint show runs the same
// tool preparation gate as validate and apply: a project whose tool does not
// compile never reports a fingerprint, matching validate/apply's own
// tool.prepare.failed diagnostic instead of silently succeeding.
func TestBrokenToolFailsFingerprintShowToo(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeGoTool(t, agent, brokenGoToolFile)

	var stdout, stderr bytes.Buffer
	code := run([]string{"fingerprint", "show", agent, "--diagnostics", "jsonl"}, nil, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("a tool that does not compile must fail fingerprint show: %q", stdout.String())
	}
	diags := filterDiags(parseDiagLines(t, stdout.String()), "tool.prepare.failed")
	if len(diags) != 1 || diags[0].Path != "tools" {
		t.Fatalf("expected one tool.prepare.failed at tools, got %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "\"fingerprint\"") {
		t.Fatalf("a project whose tools fail to build must not report a fingerprint: %s", stdout.String())
	}
}

// TestToolAndSubagentNamesMayNotCollide proves the collision fails validation
// before any preparation, naming both authored paths.
func TestToolAndSubagentNamesMayNotCollide(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeGoTool(t, agent, goToolFile)
	writeFile(t, agent, "subagents/hash-text/instructions.md",
		[]byte(minimalSubagentInstructionsFor("hash-text")), 0o644)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate", agent, "--harness", "claude", "--diagnostics", "jsonl"}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("a tool and subagent sharing a name must fail validation")
	}
	got := filterDiags(parseDiagLines(t, stdout.String()), "tool.name.collision")
	if len(got) != 1 || got[0].Path != "tools/hash_text" ||
		!strings.Contains(got[0].Rule, "subagents/hash-text") {
		t.Fatalf("expected one collision naming both paths, got %q", stdout.String())
	}
}

// TestCrossLanguageDuplicateToolNamesAreRejected proves one exposed name means
// one tool however many languages declare it.
func TestCrossLanguageDuplicateToolNamesAreRejected(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeFile(t, agent, "tools/shout_text.ts", []byte("export default {}\n"), 0o644)
	writeFile(t, agent, "tools/shout_text.py", []byte("description = 'x'\n"), 0o644)
	for _, dependency := range []string{"deno.json", "deno.lock", "pyproject.toml", "uv.lock"} {
		writeFile(t, agent, dependency, []byte("\n"), 0o644)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate", agent, "--harness", "claude", "--diagnostics", "jsonl"}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("two languages declaring one tool name must fail validation")
	}
	if len(filterDiags(parseDiagLines(t, stdout.String()), "tool.name.duplicate")) != 1 {
		t.Fatalf("expected tool.name.duplicate, got %q", stdout.String())
	}
}

// TestStaleToolCacheFailsServeClosed proves a served workspace whose prepared
// tools were edited or removed refuses to serve and directs the operator to
// reapply, without writing anything to the protocol stream.
func TestStaleToolCacheFailsServeClosed(t *testing.T) {
	agent := writeAgent(t, "my-agent", validInstructions)
	writeGoTool(t, agent, goToolFile)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	cache := filepath.Join(ws, ".tenon", "cache", "tools")
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 1 {
		t.Fatalf("apply must prepare exactly one tool cache: %v, %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(cache, entries[0].Name(), "go", "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"mcp", "serve", agent, "--harness", "claude", "--workspace", ws},
		bytes.NewBufferString(listRequest+"\n"), &stdout, &stderr)
	if code == 0 {
		t.Fatal("serving an edited tool cache must fail closed")
	}
	if !strings.Contains(stderr.String(), "tool runtime is missing or changed; run tenon apply") {
		t.Fatalf("stderr must direct the operator to reapply: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("a refused session must write nothing to the protocol stream: %q", stdout.String())
	}
}

// requireToolchain skips a leg of the polyglot journey when its toolchain is
// absent, naming exactly what is missing.
func requireToolchain(t *testing.T, name string) string {
	t.Helper()
	found, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not on PATH; the polyglot tool journey needs deno, uv, and go. "+
			"The host protocol, its bounds, and the Go tool journey are proven without it.", name)
	}
	return found
}

// lockDependencies resolves the fixture's own locked dependencies with its
// native toolchain, exactly as an author would before committing the lock.
func lockDependencies(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("%s could not resolve the fixture's locked dependencies (a warm cache or network is needed): %v\n%s",
			name, err, output)
	}
}

// TestPolyglotToolsServeOneHostPerLanguage proves spec acceptance 2 for
// authored tools: a mixed TypeScript, Python, and Go project prepares once,
// exposes every tool with its filename-derived name, and serves each language
// from one long-lived host across calls.
func TestPolyglotToolsServeOneHostPerLanguage(t *testing.T) {
	deno := requireToolchain(t, "deno")
	uv := requireToolchain(t, "uv")
	requireToolchain(t, "go")

	agent := writeAgent(t, "my-agent", validInstructions)
	writeGoTool(t, agent, goToolFile)
	writeFile(t, agent, "tools/shout_text.ts", []byte(typescriptToolFile), 0o644)
	writeFile(t, agent, "tools/count_words.py", []byte(pythonToolFile), 0o644)
	writeFile(t, agent, "tools/_shared.ts", []byte("export const unused = 1;\n"), 0o644)
	writeFile(t, agent, "deno.json", []byte("{\n  \"imports\": {\n    \"zod\": \"npm:zod@^4.1.12\"\n  }\n}\n"), 0o644)
	writeFile(t, agent, "pyproject.toml", []byte("[project]\nname = \"my-agent\"\nversion = \"0.0.0\"\n"+
		"requires-python = \">=3.11\"\ndependencies = [\"pydantic>=2\"]\n\n[tool.uv]\npackage = false\n"), 0o644)
	lockDependencies(t, agent, deno, "install", "--entrypoint", "tools/shout_text.ts")
	lockDependencies(t, agent, uv, "lock")

	ws := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "codex", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "managed tools: echo, count-words, hash-text, shout-text via MCP") {
		t.Fatalf("apply must report every authored tool by its exposed name: %q", stdout.String())
	}

	responses := serveManaged(t, agent, "codex", ws, listRequest,
		toolCall(2, "shout-text", `{"text":"hi"}`),
		toolCall(3, "shout-text", `{"text":"again"}`),
		toolCall(4, "count-words", `{"text":"one two three"}`),
		toolCall(5, "count-words", `{"text":"one"}`),
		toolCall(6, "hash-text", `{"text":"hi"}`))

	for _, name := range []string{"shout-text", "count-words", "hash-text"} {
		if listedTool(t, responses[0], name)["inputSchema"].(map[string]any)["type"] != "object" {
			t.Fatalf("%s must publish an object input schema", name)
		}
	}
	if got := callResult(t, responses[1]); got["shouted"] != "HI" || got["calls"] != float64(1) {
		t.Fatalf("shout-text = %#v", got)
	}
	if got := callResult(t, responses[2]); got["calls"] != float64(2) {
		t.Fatalf("the typescript host must serve both calls: %#v", got)
	}
	if got := callResult(t, responses[3]); got["words"] != float64(3) || got["calls"] != float64(1) {
		t.Fatalf("count-words = %#v", got)
	}
	if got := callResult(t, responses[4]); got["calls"] != float64(2) {
		t.Fatalf("the python host must serve both calls: %#v", got)
	}
	if got := callResult(t, responses[5]); got["calls"] != float64(1) {
		t.Fatalf("hash-text = %#v", got)
	}
}

// TestToolShapeViolationsFailInspectionNamingTheFile proves a tool that does
// not declare the authored contract fails at inspection, before apply mutates
// anything, with a bounded message naming the authored file and the shape it
// must export.
func TestToolShapeViolationsFailInspectionNamingTheFile(t *testing.T) {
	cases := map[string]struct {
		toolchain string
		path      string
		source    string
		lock      func(t *testing.T, dir, toolchain string)
		wants     []string
	}{
		"typescript": {
			toolchain: "deno",
			path:      "tools/shout_text.ts",
			source: "import { z } from \"zod\";\n\nexport default {\n" +
				"  description: \"Uppercase bounded text.\",\n" +
				"  inputSchema: z.object({ text: z.string() }).strict(),\n" +
				"  outputSchema: z.object({ shouted: z.string() }).strict(),\n};\n",
			lock: func(t *testing.T, dir, toolchain string) {
				writeFile(t, dir, "deno.json", []byte("{\n  \"imports\": {\n    \"zod\": \"npm:zod@^4.1.12\"\n  }\n}\n"), 0o644)
				lockDependencies(t, dir, toolchain, "install", "--entrypoint", "tools/shout_text.ts")
			},
			wants: []string{"tools/shout_text.ts", "execute is not a function"},
		},
		"python": {
			toolchain: "uv",
			path:      "tools/count_words.py",
			source:    "from pydantic import BaseModel\n\ndescription = \"Count words.\"\n\n\nclass Input(BaseModel):\n    text: str\n",
			lock: func(t *testing.T, dir, toolchain string) {
				writeFile(t, dir, "pyproject.toml", []byte("[project]\nname = \"my-agent\"\nversion = \"0.0.0\"\n"+
					"requires-python = \">=3.11\"\ndependencies = [\"pydantic>=2\"]\n\n[tool.uv]\npackage = false\n"), 0o644)
				lockDependencies(t, dir, toolchain, "lock")
			},
			wants: []string{"tools/count_words.py", "description, Input, Output, and execute"},
		},
	}
	for language, test := range cases {
		t.Run(language, func(t *testing.T) {
			toolchain := requireToolchain(t, test.toolchain)
			agent := writeAgent(t, "my-agent", validInstructions)
			writeFile(t, agent, test.path, []byte(test.source), 0o644)
			test.lock(t, agent, toolchain)

			ws := t.TempDir()
			var stdout, stderr bytes.Buffer
			code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws, "--diagnostics", "jsonl"},
				nil, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("a tool that does not declare the contract must fail apply: %q", stdout.String())
			}
			got := filterDiags(parseDiagLines(t, stdout.String()), "tool.inspect.failed")
			if len(got) != 1 || got[0].Path != "tools" {
				t.Fatalf("expected one tool.inspect.failed at tools, got %q", stdout.String())
			}
			for _, want := range test.wants {
				if !strings.Contains(got[0].Rule, want) {
					t.Fatalf("the diagnostic must name %q: %q", want, got[0].Rule)
				}
			}
			if _, err := os.Stat(filepath.Join(ws, "CLAUDE.md")); !os.IsNotExist(err) {
				t.Fatal("a failing inspection must precede every generated file")
			}
		})
	}
}

// TestServeCallsAGoToolFromAStagedTree is ADR 0021's acceptance shape for Go:
// stage a Go-tool agent, then serve from the staged workspace and staged
// closure directly, with no workspace tool cache ever prepared there, and
// prove a tools/list and a tools/call round-trip. Before the ADR 0021 fix,
// the closure staged at /opt/tenon/runtimes/tools/<key> was unreachable from
// the staged workspace's own apply record, so this failed closed with "tool
// runtime is missing or changed; run tenon apply" even though `tenon stage
// verify` reported the tree clean.
func TestServeCallsAGoToolFromAStagedTree(t *testing.T) {
	agent := writeAgent(t, "staged-tool-agent", validInstructions)
	writeGoTool(t, agent, goToolFile)
	out := filepath.Join(t.TempDir(), "staged")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"stage", agent, "--harness", "claude", "--output", out}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("stage exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	artifact := filepath.Join(out, "opt", "tenon", "artifact.json")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"stage", "verify", "--artifact", artifact, "--prefix", out}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("a staged Go-tool tree must verify: exit %d\nstderr: %s", code, stderr.String())
	}

	stagedSource := filepath.Join(out, "opt", "tenon", "agents", "staged-tool-agent")
	stagedWorkspace := filepath.Join(out, "workspace")

	// No workspace tool cache exists anywhere in the staged tree: serving
	// must reach the closure staged under opt/tenon/runtimes, not a
	// re-prepared workspace cache.
	if _, err := os.Stat(filepath.Join(stagedWorkspace, ".tenon", "cache", "tools")); !os.IsNotExist(err) {
		t.Fatalf("the staged workspace must carry no tool cache, found one (or a stat error): %v", err)
	}

	responses := serveManaged(t, stagedSource, "claude", stagedWorkspace, listRequest,
		toolCall(2, "hash-text", `{"text":"hi"}`))

	listed := listedTool(t, responses[0], "hash-text")
	if listed["description"] != "Hash bounded text with SHA-256." {
		t.Fatalf("description = %v", listed["description"])
	}
	result := callResult(t, responses[1])
	if result["hex"] != "8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4" {
		t.Fatalf("hash-text output = %#v", result)
	}
}

// TestGoToolImportsOwnModulePackageOutsideTools proves a Go tool that
// imports a sibling package from its own module outside tools/ — a shape
// docs/product-spec.md permits (it constrains only tools/'s own shape) and
// that `go build ./...` against the real agent source always accepted —
// still prepares, applies, and serves. It once regressed: preparation
// narrowed the directory it built the Go host against to only tools/ and
// the two native dependency files, so an import like this failed inside
// toolruntime with a diagnostic that told the author to run the toolchain
// directly against their own source to see the failure, where it in fact
// succeeds.
func TestGoToolImportsOwnModulePackageOutsideTools(t *testing.T) {
	agent := writeAgent(t, "own-module-import-agent", validInstructions)
	writeGoToolWithOwnModuleImport(t, agent)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude", "--workspace", ws}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	responses := serveManaged(t, agent, "claude", ws, listRequest, toolCall(2, "shout-text", `{"text":"hi"}`))
	listed := listedTool(t, responses[0], "shout-text")
	if listed["description"] != "Shout bounded text." {
		t.Fatalf("description = %v", listed["description"])
	}
	result := callResult(t, responses[1])
	if result["shouted"] != "HI" {
		t.Fatalf("shout-text output = %#v", result)
	}
}

// TestStageGoToolImportsOwnModulePackageOutsideTools is the same proof
// through the stage path: the same agent stages cleanly and the staged
// closure serves the tool, so the whole-module build source copy fixes the
// import for staging too, not only for the local apply/serve path.
func TestStageGoToolImportsOwnModulePackageOutsideTools(t *testing.T) {
	agent := writeAgent(t, "staged-own-module-import-agent", validInstructions)
	writeGoToolWithOwnModuleImport(t, agent)
	out := filepath.Join(t.TempDir(), "staged")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"stage", agent, "--harness", "claude", "--output", out}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("stage exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}

	stagedSource := filepath.Join(out, "opt", "tenon", "agents", "staged-own-module-import-agent")
	stagedWorkspace := filepath.Join(out, "workspace")

	responses := serveManaged(t, stagedSource, "claude", stagedWorkspace, listRequest, toolCall(2, "shout-text", `{"text":"hi"}`))
	result := callResult(t, responses[1])
	if result["shouted"] != "HI" {
		t.Fatalf("shout-text output = %#v", result)
	}
}
