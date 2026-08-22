package generated

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
)

// TestSkillMDInsertsOneMarkerLine proves the deliberate contract point: the
// generated SKILL.md is the authored bytes plus exactly one marker line after
// the closing frontmatter delimiter, and the marker carries no fingerprint,
// version, or provenance.
func TestSkillMDInsertsOneMarkerLine(t *testing.T) {
	src := []byte("---\nname: echo\n---\n\nBody.\n")
	bodyStart := len("---\nname: echo\n---\n")
	got := SkillMD(src, bodyStart)
	want := []byte("---\nname: echo\n---\n" + Marker + "\n\nBody.\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("SkillMD = %q, want %q", got, want)
	}
	if strings.Contains(Marker, "sha256") || strings.Contains(Marker, "fingerprint") {
		t.Fatalf("the marker must carry no provenance: %q", Marker)
	}
}

func TestSkillMDWithoutTrailingNewline(t *testing.T) {
	src := []byte("---\nname: echo\n---")
	got := SkillMD(src, len(src))
	want := []byte("---\nname: echo\n---\n" + Marker + "\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("SkillMD = %q, want %q", got, want)
	}
}

// TestInstructionsWithoutConnections proves the connections section is
// omitted entirely, not emitted empty, when no connection exists.
func TestInstructionsWithoutConnections(t *testing.T) {
	got := Instructions("Body text.\n", nil)
	want := []byte(Marker + "\n\nBody text.\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("Instructions = %q, want %q", got, want)
	}
	if strings.Contains(string(got), "Native MCP connections") {
		t.Fatalf("no connections section expected: %q", got)
	}
}

// TestInstructionsWithConnectionsExactBytes proves the exact rendering
// contract (ADR 0016): the authored body first, then one bounded "## Native
// MCP connections" section with each connection named once in the given
// order, its nonempty context rendered once, no frontmatter, and one closing
// boundary sentence stated exactly once.
func TestInstructionsWithConnectionsExactBytes(t *testing.T) {
	connections := []agentproject.Connection{
		{Name: "catalog", URL: "https://example.com/mcp", Context: "Use this for the public reference catalog."},
		{Name: "search", URL: "https://search.example.com/mcp", Context: ""},
	}
	got := Instructions("Body text.\n", connections)
	want := []byte(Marker + "\n\n" +
		"Body text.\n" +
		"\n## Native MCP connections\n" +
		"\n### catalog\n" +
		"\nUse this for the public reference catalog.\n" +
		"\n### search\n" +
		"\n" + connectionsBoundary + "\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("Instructions =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(string(got), "type: mcp") || strings.Contains(string(got), "transport:") {
		t.Fatalf("no frontmatter must leak into generated instructions: %q", got)
	}
	if strings.Count(string(got), connectionsBoundary) != 1 {
		t.Fatalf("the boundary sentence must appear exactly once: %q", got)
	}
	if strings.Count(string(got), "### catalog") != 1 || strings.Count(string(got), "### search") != 1 {
		t.Fatalf("each connection name must appear exactly once as a subheading: %q", got)
	}
}

func TestYAMLStringEscapesControlCharsAndKeepsUnicodeLiteral(t *testing.T) {
	cases := map[string]string{
		"plain":                 `"plain"`,
		`back\slash`:            `"back\\slash"`,
		`quo"te`:                `"quo\"te"`,
		"line\nbreak":           `"line\nbreak"`,
		"a\ttab":                `"a\ttab"`,
		"cr\rreturn":            `"cr\rreturn"`,
		"bell\x07here":          `"bell\x07here"`,
		"café emoji \U0001F600": "\"café emoji \U0001F600\"",
	}
	for in, want := range cases {
		if got := YAMLString(in); got != want {
			t.Fatalf("YAMLString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTOMLStringEscapesControlCharsAndKeepsUnicodeLiteral(t *testing.T) {
	cases := map[string]string{
		"plain":                 `"plain"`,
		`back\slash`:            `"back\\slash"`,
		`quo"te`:                `"quo\"te"`,
		"line\nbreak":           `"line\nbreak"`,
		"a\ttab":                `"a\ttab"`,
		"cr\rreturn":            `"cr\rreturn"`,
		"bell\x07here":          `"bell\u0007here"`,
		"café emoji \U0001F600": "\"café emoji \U0001F600\"",
	}
	for in, want := range cases {
		if got := TOMLString(in); got != want {
			t.Fatalf("TOMLString(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestClaudeSubagentExactBytes proves the exact Claude subagent rendering:
// closed frontmatter carrying only the child's own routing metadata, never
// the parent's instructions, skills, or tools.
func TestClaudeSubagentExactBytes(t *testing.T) {
	got := ClaudeSubagent("code-reviewer", "Reviews pull requests.", "high", "\n  Review the diff.\n\nBe thorough.\n\n")
	want := []byte("---\nname: code-reviewer\ndescription: \"Reviews pull requests.\"\neffort: high\n---\n\nReview the diff.\n\nBe thorough.\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("ClaudeSubagent = %q, want %q", got, want)
	}
}

// TestClaudeSubagentOmitsEffortWhenAbsent proves the effort line is omitted
// entirely, not emitted empty, when the source carries no effort.
func TestClaudeSubagentOmitsEffortWhenAbsent(t *testing.T) {
	got := ClaudeSubagent("greeter", "Greets the user.", "", "Say hello.\n")
	want := []byte("---\nname: greeter\ndescription: \"Greets the user.\"\n---\n\nSay hello.\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("ClaudeSubagent = %q, want %q", got, want)
	}
	if strings.Contains(string(got), "effort") {
		t.Fatalf("effort must be omitted entirely when absent: %q", got)
	}
}

// TestCodexSubagentExactBytes proves the exact Codex TOML rendering,
// including valid (not Go-syntax) escaping of a body containing a newline
// and a double quote.
func TestCodexSubagentExactBytes(t *testing.T) {
	got := CodexSubagent("code-reviewer", "Reviews \"pull\" requests.", "medium", "Review the diff.\nQuote \"issues\" precisely.")
	want := []byte("name = \"code_reviewer\"\n" +
		"description = \"Reviews \\\"pull\\\" requests.\"\n" +
		"model_reasoning_effort = \"medium\"\n" +
		"developer_instructions = \"Review the diff.\\nQuote \\\"issues\\\" precisely.\"\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("CodexSubagent = %q, want %q", got, want)
	}
}

// TestCodexSubagentOmitsEffortWhenAbsent proves model_reasoning_effort is
// omitted entirely, not emitted empty, when the source carries no effort.
func TestCodexSubagentOmitsEffortWhenAbsent(t *testing.T) {
	got := CodexSubagent("greeter", "Greets the user.", "", "Say hello.\n")
	want := []byte("name = \"greeter\"\n" +
		"description = \"Greets the user.\"\n" +
		"developer_instructions = \"Say hello.\"\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("CodexSubagent = %q, want %q", got, want)
	}
	if strings.Contains(string(got), "model_reasoning_effort") {
		t.Fatalf("model_reasoning_effort must be omitted entirely when absent: %q", got)
	}
}

// TestCodexSubagentUnderscoresHyphenatedName proves only the name VALUE
// converts hyphens to underscores; the filename (chosen by the driver, not
// this renderer) keeps the portable hyphenated form.
func TestCodexSubagentUnderscoresHyphenatedName(t *testing.T) {
	got := CodexSubagent("pr-reviewer-v2", "d", "", "b\n")
	if !bytes.Contains(got, []byte(`name = "pr_reviewer_v2"`)) {
		t.Fatalf("expected the underscored name value: %q", got)
	}
}

// TestTOMLHeaderIsTheMarkerSentenceWithoutProvenance proves the TOML
// ownership header states the same claim as the Markdown marker and, like it,
// carries no fingerprint, version, or provenance.
func TestTOMLHeaderIsTheMarkerSentenceWithoutProvenance(t *testing.T) {
	sentence := "Generated by tenon apply; do not edit. Edit the agent source and reapply."
	if TOMLHeader != "# "+sentence {
		t.Fatalf("TOMLHeader = %q, want the marker sentence in comment form", TOMLHeader)
	}
	if !strings.Contains(Marker, sentence) {
		t.Fatalf("Marker = %q, want the same sentence", Marker)
	}
	if strings.Contains(TOMLHeader, "sha256") || strings.Contains(TOMLHeader, "fingerprint") ||
		strings.Contains(TOMLHeader, "\n") {
		t.Fatalf("the header must be one provenance-free line: %q", TOMLHeader)
	}
}
