package acp

import (
	"strings"
	"testing"
)

// TestParsePolicy proves the document shape: a required default, ordered
// rules, closed vocabularies, and rejection of unknown fields.
func TestParsePolicy(t *testing.T) {
	p, err := Parse([]byte(`{"default":"deny","rules":[{"kind":"read","action":"allow"},{"tool":"Bash","title":"git *","action":"allow"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Default != Deny || len(p.Rules) != 2 || p.Rules[1].Title != "git *" {
		t.Fatalf("parsed %+v", p)
	}
	if p.String() != "default deny, 2 rule(s)" {
		t.Fatalf("String = %q", p.String())
	}
	bad := map[string]string{
		"missing default":  `{"rules":[]}`,
		"unknown action":   `{"default":"ask"}`,
		"rule no action":   `{"default":"deny","rules":[{"kind":"read"}]}`,
		"unknown kind":     `{"default":"deny","rules":[{"kind":"shell","action":"allow"}]}`,
		"unknown field":    `{"default":"deny","rules":[{"command":"ls","action":"allow"}]}`,
		"trailing data":    `{"default":"deny"} {}`,
		"not json":         `default: deny`,
		"too many rules":   `{"default":"deny","rules":[` + strings.Repeat(`{"action":"allow"},`, maxPolicyRules) + `{"action":"allow"}]}`,
		"pattern too long": `{"default":"deny","rules":[{"title":"` + strings.Repeat("x", maxPatternLength+1) + `","action":"allow"}]}`,
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
	if _, err := Parse([]byte(strings.Repeat(" ", maxPolicyBytes+1))); err == nil {
		t.Fatal("an over-bound document must be refused before decoding")
	}
}

// TestDecide proves matching semantics: every named field must match, a tool
// or path rule never matches a call without one, and order decides.
func TestDecide(t *testing.T) {
	p := Policy{Default: Deny, Rules: []Rule{
		{Kind: "read", Action: Allow},
		{Tool: "Bash", Title: "git *", Action: Allow},
		{Path: "/ws/docs/*", Kind: "edit", Action: Allow},
		{Title: "rm *", Action: Deny},
		{Kind: "execute", Action: Allow},
	}}
	cases := []struct {
		call Call
		want Action
		rule int
	}{
		{Call{Kind: "read", Title: "Read file"}, Allow, 0},
		{Call{Kind: "execute", Title: "git status", Tool: "Bash"}, Allow, 1},
		{Call{Kind: "execute", Title: "git status"}, Allow, 4}, // no tool reported: rule 1 cannot match
		{Call{Kind: "edit", Title: "Edit", Paths: []string{"/ws/docs/a.md"}}, Allow, 2},
		{Call{Kind: "edit", Title: "Edit", Paths: []string{"/ws/src/a.go"}}, Deny, -1},
		{Call{Kind: "edit", Title: "Edit"}, Deny, -1}, // no path reported: rule 2 cannot match
		{Call{Kind: "execute", Title: "rm -rf /"}, Deny, 3},
		{Call{Kind: "fetch", Title: "GET"}, Deny, -1},
	}
	for _, tc := range cases {
		got := p.Decide(tc.call)
		if got.Action != tc.want || got.Rule != tc.rule {
			t.Fatalf("%+v: got %+v, want %s via rule %d", tc.call, got, tc.want, tc.rule)
		}
	}
	if d := (Policy{Default: Allow}).Decide(Call{}); d.Action != Allow || d.Rule != -1 {
		t.Fatalf("empty policy: %+v", d)
	}
}

// TestGlob proves the two-metacharacter glob, including a star across path
// separators and backtracking over a single star.
func TestGlob(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"git *", "git status", true},
		{"git *", "git", false},
		{"git*", "git", true},
		{"*", "", true},
		{"", "", true},
		{"", "x", false},
		{"/ws/*", "/ws/a/b/c", true},
		{"/ws/*.md", "/ws/docs/a.md", true},
		{"/ws/*.md", "/ws/docs/a.go", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*a*b*", "xxaxxbxx", true},
		{"*a*b*", "xxbxxaxx", false},
		{"Bash", "bash", false},
	}
	for _, tc := range cases {
		if got := glob(tc.pattern, tc.s); got != tc.want {
			t.Fatalf("glob(%q, %q) = %v", tc.pattern, tc.s, got)
		}
	}
}
