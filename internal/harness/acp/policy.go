package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Policy answers an agent's session/request_permission calls without a person
// present. It is operator-authored, supplied to the process that opens the
// session (never read from agent source), and evaluated first-match-wins:
// every field a rule names must match the tool call, and Default decides a
// call no rule matches. The policy is how an operator declines to be asked;
// it enforces nothing the native harness would not, and a request that never
// reaches tenon — because the harness's own mode already allowed or denied
// it — is never seen here.
type Policy struct {
	// Default decides a call no rule matches. It is required.
	Default Action `json:"default"`
	// Rules are evaluated in order; the first that matches decides.
	Rules []Rule `json:"rules,omitempty"`
}

// Action is a policy decision.
type Action string

const (
	// Allow selects the agent's allow-once option.
	Allow Action = "allow"
	// Deny selects the agent's reject-once option.
	Deny Action = "deny"
)

// Rule matches one shape of tool call. Every non-empty field must match; a
// rule with no fields matches everything. Tool, Title and Path are globs where
// `*` matches any run of characters (including path separators) and `?`
// matches one; Kind is exact.
type Rule struct {
	// Tool matches the harness-native tool name when the agent reports one
	// (claude-agent-acp reports it under _meta.claudeCode.toolName); a rule
	// naming a tool never matches a call that reports none.
	Tool string `json:"tool,omitempty"`
	// Kind matches the ACP tool kind: read, edit, delete, move, search,
	// execute, think, fetch, switch_mode, or other.
	Kind string `json:"kind,omitempty"`
	// Title matches the agent's human-readable title for the call, which for
	// a command is typically the command line.
	Title string `json:"title,omitempty"`
	// Path matches any of the call's reported file locations (absolute
	// paths). A rule naming a path never matches a call that reports none.
	Path string `json:"path,omitempty"`
	// Action is the decision. It is required.
	Action Action `json:"action"`
}

// Call is the policy-relevant projection of one permission request. It is
// built from the request and discarded; none of it is ever emitted.
type Call struct {
	Tool  string
	Kind  string
	Title string
	Paths []string
}

// Decision is a policy's answer for one call. Rule is the index of the rule
// that matched, or -1 when Default decided.
type Decision struct {
	Action Action
	Rule   int
}

// Bounds on an authored policy, so a policy file is never an unbounded
// input to the process that opens a session.
const (
	maxPolicyBytes   = 64 * 1024
	maxPolicyRules   = 256
	maxPatternLength = 1024
)

// toolKinds is the closed ACP ToolKind vocabulary a rule's Kind must use.
var toolKinds = map[string]bool{
	"read": true, "edit": true, "delete": true, "move": true, "search": true,
	"execute": true, "think": true, "fetch": true, "switch_mode": true, "other": true,
}

// AllowAll answers every request allow.
func AllowAll() Policy { return Policy{Default: Allow} }

// DenyAll answers every request deny.
func DenyAll() Policy { return Policy{Default: Deny} }

// Parse decodes and validates one policy document. Unknown fields, unknown
// actions, unknown kinds, and over-bound inputs are errors.
func Parse(b []byte) (Policy, error) {
	if len(b) > maxPolicyBytes {
		return Policy{}, fmt.Errorf("permissions policy exceeds %d bytes", maxPolicyBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var p Policy
	if err := dec.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("permissions policy: %w", err)
	}
	if dec.More() {
		return Policy{}, errors.New("permissions policy: trailing data after the document")
	}
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	return p, nil
}

// Validate reports the first rule that is not well-formed.
func (p Policy) Validate() error {
	if err := validAction(p.Default); err != nil {
		return fmt.Errorf("permissions policy default: %w", err)
	}
	if len(p.Rules) > maxPolicyRules {
		return fmt.Errorf("permissions policy has %d rules; at most %d are allowed", len(p.Rules), maxPolicyRules)
	}
	for i, r := range p.Rules {
		if err := validAction(r.Action); err != nil {
			return fmt.Errorf("permissions policy rule %d: %w", i, err)
		}
		if r.Kind != "" && !toolKinds[r.Kind] {
			return fmt.Errorf("permissions policy rule %d: kind %q is not an ACP tool kind", i, r.Kind)
		}
		for name, pat := range map[string]string{"tool": r.Tool, "title": r.Title, "path": r.Path} {
			if len(pat) > maxPatternLength {
				return fmt.Errorf("permissions policy rule %d: %s pattern exceeds %d bytes", i, name, maxPatternLength)
			}
		}
	}
	return nil
}

func validAction(a Action) error {
	switch a {
	case Allow, Deny:
		return nil
	case "":
		return errors.New("action is required (allow or deny)")
	default:
		return fmt.Errorf("action %q must be allow or deny", string(a))
	}
}

// Decide returns the first matching rule's action, or Default.
func (p Policy) Decide(c Call) Decision {
	for i, r := range p.Rules {
		if r.matches(c) {
			return Decision{Action: r.Action, Rule: i}
		}
	}
	return Decision{Action: p.Default, Rule: -1}
}

func (r Rule) matches(c Call) bool {
	if r.Kind != "" && r.Kind != c.Kind {
		return false
	}
	if r.Tool != "" && (c.Tool == "" || !glob(r.Tool, c.Tool)) {
		return false
	}
	if r.Title != "" && !glob(r.Title, c.Title) {
		return false
	}
	if r.Path != "" {
		hit := false
		for _, p := range c.Paths {
			if glob(r.Path, p) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// glob matches s against pattern, where `*` matches any run of characters and
// `?` matches exactly one. It is iterative with backtracking over a single
// star, so it is linear in practice and never recursive.
func glob(pattern, s string) bool {
	px, sx := 0, 0
	starPx, starSx := -1, -1
	for sx < len(s) {
		if px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]) {
			px++
			sx++
			continue
		}
		if px < len(pattern) && pattern[px] == '*' {
			starPx, starSx = px, sx
			px++
			continue
		}
		if starPx >= 0 {
			px = starPx + 1
			starSx++
			sx = starSx
			continue
		}
		return false
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// String renders a policy for diagnostics: counts only, never patterns, so a
// policy an operator considers sensitive is not echoed.
func (p Policy) String() string {
	return fmt.Sprintf("default %s, %d rule(s)", strings.ToLower(string(p.Default)), len(p.Rules))
}
