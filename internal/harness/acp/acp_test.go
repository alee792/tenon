package acp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/harness"
)

// TestMain lets the test binary serve as the fake ACP agent when the driver
// launches it, so every driver test runs the real wire path without a model.
func TestMain(m *testing.M) {
	if opts, ok := FakeFromEnv(); ok {
		if err := RunFake(os.Stdin, os.Stdout, os.Stderr, opts); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeDriver returns a driver that launches this test binary as the fake
// agent, scripted through env. Tests using it must not run in parallel.
func fakeDriver(t *testing.T, policy Policy, env map[string]string) Driver {
	t.Helper()
	t.Setenv(FakeEnv, "1")
	// Reset every scripted variable so a driver built earlier in the same
	// test never leaks its script into this one.
	for _, k := range []string{FakeEnvProtocol, FakeEnvLoadable, FakeEnvStop, FakeEnvPromptErr, FakeEnvPermission, FakeEnvAllowOnly, FakeEnvRequestFS} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	d, err := NewDriver("claude", []string{os.Args[0]}, policy, "test")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// runOne opens one session in a temp workspace and runs one turn, returning
// the events, the result, and the process error.
func runOne(t *testing.T, d Driver, req harness.OpenRequest) ([]harness.Event, harness.TurnResult, error) {
	t.Helper()
	if req.Workspace == "" {
		req.Workspace = t.TempDir()
	}
	s, err := d.Open(context.Background(), req)
	if err != nil {
		return nil, harness.TurnResult{}, err
	}
	defer s.Close()
	var events []harness.Event
	res, err := s.RunTurn(context.Background(), harness.Input{ID: "in-1", Text: "do it"}, func(e harness.Event) {
		events = append(events, e)
	})
	return events, res, err
}

func deltas(events []harness.Event) string {
	var b strings.Builder
	for _, e := range events {
		if e.Type == harness.EventAgentOutputDelta {
			b.WriteString(e.Delta)
		}
	}
	return b.String()
}

// TestTurnCompletes proves a plain turn: session started with the agent's id,
// only agent text chunks become deltas (thoughts and tool calls do not), and
// end_turn is completed.
func TestTurnCompletes(t *testing.T) {
	d := fakeDriver(t, DenyAll(), nil)
	events, res, err := runOne(t, d, harness.OpenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Type != harness.EventSessionStarted || events[0].SessionID != "fake-session" {
		t.Fatalf("first event = %+v", events[0])
	}
	if got := deltas(events); got != "hello world" {
		t.Fatalf("deltas = %q", got)
	}
	for _, e := range events {
		if strings.Contains(e.Delta, "THOUGHT") || strings.Contains(e.Delta, "TOOL") {
			t.Fatalf("a non-message update leaked as a delta: %+v", e)
		}
	}
	if res.Status != harness.StatusCompleted || res.SessionID != "fake-session" || res.Reason != "" {
		t.Fatalf("result = %+v", res)
	}
}

// TestStopReasons proves each protocol stop reason maps to a status and that
// the reason is the closed vocabulary, never free text.
func TestStopReasons(t *testing.T) {
	cases := map[string]struct {
		status harness.Status
		reason string
	}{
		"end_turn":          {harness.StatusCompleted, ""},
		"cancelled":         {harness.StatusCancelled, "cancelled"},
		"refusal":           {harness.StatusFailed, "refusal"},
		"max_tokens":        {harness.StatusFailed, "max_tokens"},
		"max_turn_requests": {harness.StatusFailed, "max_turn_requests"},
		"something_new":     {harness.StatusFailed, "turn_failed"},
	}
	for stop, want := range cases {
		got := classifyStop("s", stop)
		if got.Status != want.status || got.Reason != want.reason {
			t.Fatalf("%s: got %+v, want %+v", stop, got, want)
		}
	}
	d := fakeDriver(t, DenyAll(), map[string]string{FakeEnvStop: "refusal"})
	_, res, err := runOne(t, d, harness.OpenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != harness.StatusFailed || res.Reason != "refusal" {
		t.Fatalf("result = %+v", res)
	}
}

// TestPromptRejectionNeverLeaksMessage proves an error reply to the prompt is
// a failed turn whose reason is the fixed vocabulary, and the message — which
// in the wild has carried a live key — appears nowhere.
func TestPromptRejectionNeverLeaksMessage(t *testing.T) {
	const secret = "sk-live-SECRET-KEY-VALUE"
	d := fakeDriver(t, DenyAll(), map[string]string{FakeEnvPromptErr: "401 Unauthorized: bad key " + secret})
	events, res, err := runOne(t, d, harness.OpenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != harness.StatusFailed || res.Reason != "authentication" {
		t.Fatalf("result = %+v", res)
	}
	for _, e := range events {
		if strings.Contains(e.Delta, secret) {
			t.Fatal("the rejection message leaked into an event")
		}
	}
	if strings.Contains(res.Reason, secret) {
		t.Fatal("the rejection message leaked into the reason")
	}
	if safeReason("model overloaded") != "prompt_rejected" {
		t.Fatal("a non-auth message must classify as prompt_rejected")
	}
}

// TestNoClientCapabilities proves tenon advertises no file-system capability:
// an agent that asks anyway gets method-not-found, and the turn proceeds.
func TestNoClientCapabilities(t *testing.T) {
	d := fakeDriver(t, DenyAll(), map[string]string{FakeEnvRequestFS: "1"})
	events, res, err := runOne(t, d, harness.OpenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(deltas(events), "fs:-32601") {
		t.Fatalf("an fs request must be answered method-not-found, deltas = %q", deltas(events))
	}
	if res.Status != harness.StatusCompleted {
		t.Fatalf("result = %+v", res)
	}
}

// TestPermissionByPolicy proves session/request_permission is answered by
// the policy: a matching rule's action, the default otherwise, always the
// once variant, and cancelled when the agent offers no option of that kind.
func TestPermissionByPolicy(t *testing.T) {
	call := `{"toolCallId":"t2","kind":"execute","title":"git status","status":"pending",` +
		`"locations":[{"path":"/ws/README.md"}],"_meta":{"claudeCode":{"toolName":"Bash"}}}`
	cases := []struct {
		name   string
		policy Policy
		env    map[string]string
		want   string
	}{
		{"default deny", DenyAll(), nil, "reject-once"},
		{"default allow", AllowAll(), nil, "allow-once"},
		{"rule by kind", Policy{Default: Deny, Rules: []Rule{{Kind: "execute", Action: Allow}}}, nil, "allow-once"},
		{"rule by title glob", Policy{Default: Deny, Rules: []Rule{{Title: "git *", Action: Allow}}}, nil, "allow-once"},
		{"rule by tool", Policy{Default: Allow, Rules: []Rule{{Tool: "Bash", Action: Deny}}}, nil, "reject-once"},
		{"rule by path", Policy{Default: Deny, Rules: []Rule{{Path: "/ws/*", Action: Allow}}}, nil, "allow-once"},
		{"first match wins", Policy{Default: Deny, Rules: []Rule{{Kind: "execute", Action: Deny}, {Title: "git *", Action: Allow}}}, nil, "reject-once"},
		{"non-matching rule falls to default", Policy{Default: Deny, Rules: []Rule{{Kind: "read", Action: Allow}}}, nil, "reject-once"},
		{"no reject option offered", DenyAll(), map[string]string{FakeEnvAllowOnly: "1"}, "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{FakeEnvPermission: call}
			for k, v := range tc.env {
				env[k] = v
			}
			d := fakeDriver(t, tc.policy, env)
			events, res, err := runOne(t, d, harness.OpenRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(deltas(events), "decision:"+tc.want) {
				t.Fatalf("deltas = %q, want decision %s", deltas(events), tc.want)
			}
			if res.Status != harness.StatusCompleted {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

// TestResumeLoadsSession proves a resume is a session/load whose replayed
// history is not re-emitted, and that an agent without loadSession refuses
// the resume rather than starting a different session.
func TestResumeLoadsSession(t *testing.T) {
	d := fakeDriver(t, DenyAll(), map[string]string{FakeEnvLoadable: "1"})
	events, res, err := runOne(t, d, harness.OpenRequest{ResumeID: "fake-session"})
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Type != harness.EventSessionResumed || events[0].SessionID != "fake-session" {
		t.Fatalf("first event = %+v", events[0])
	}
	if strings.Contains(deltas(events), "replayed") {
		t.Fatal("replayed history must not be re-emitted as output")
	}
	if res.Status != harness.StatusCompleted || res.SessionID != "fake-session" {
		t.Fatalf("result = %+v", res)
	}

	// Fresh overrides a recorded id, as task mode requires.
	events, _, err = runOne(t, d, harness.OpenRequest{ResumeID: "fake-session", Fresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Type != harness.EventSessionStarted {
		t.Fatalf("a fresh open must start, got %+v", events[0])
	}

	unloadable := fakeDriver(t, DenyAll(), nil)
	if _, _, err := runOne(t, unloadable, harness.OpenRequest{ResumeID: "fake-session"}); err == nil || !strings.Contains(err.Error(), "cannot load sessions") {
		t.Fatalf("a resume against an agent without loadSession must be refused, got %v", err)
	}
}

// TestProtocolVersionMismatch proves an agent on another major version is a
// process failure at open.
func TestProtocolVersionMismatch(t *testing.T) {
	d := fakeDriver(t, DenyAll(), map[string]string{FakeEnvProtocol: "2"})
	if _, _, err := runOne(t, d, harness.OpenRequest{}); err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("want a protocol version error, got %v", err)
	}
}

// TestAgentExitIsProcessFailure proves an agent that dies mid-turn is a
// process error, never a clean terminal.
func TestAgentExitIsProcessFailure(t *testing.T) {
	d, err := NewDriver("claude", []string{"true"}, DenyAll(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runOne(t, d, harness.OpenRequest{}); err == nil {
		t.Fatal("an agent that exits without replying must be a process failure")
	}
}

// TestDriverConstruction proves the defaults and the checks NewDriver makes.
func TestDriverConstruction(t *testing.T) {
	d, err := NewDriver("claude", nil, DenyAll(), "v")
	if err != nil || d.Command()[0] != "claude-agent-acp" || d.Name() != "claude" {
		t.Fatalf("claude default: %+v %v", d, err)
	}
	d, err = NewDriver("codex", nil, DenyAll(), "v")
	if err != nil || d.Command()[0] != "codex-acp" {
		t.Fatalf("codex default: %+v %v", d, err)
	}
	if _, err := NewDriver("gemini", nil, DenyAll(), "v"); err == nil {
		t.Fatal("a harness with no default adapter and no command must fail")
	}
	d, err = NewDriver("gemini", []string{"gemini", "--acp"}, DenyAll(), "v")
	if err != nil || strings.Join(d.Command(), " ") != "gemini --acp" {
		t.Fatalf("explicit command: %+v %v", d, err)
	}
	if _, err := NewDriver("claude", nil, Policy{}, "v"); err == nil {
		t.Fatal("an invalid policy must be refused at construction")
	}
	if err := d.Verify(context.Background()); err == nil {
		t.Fatal("verify must fail for an executable that is not on PATH")
	}
	ok, _ := NewDriver("claude", []string{"true"}, DenyAll(), "v")
	if err := ok.Verify(context.Background()); err != nil {
		t.Fatalf("verify must resolve an executable on PATH: %v", err)
	}
	var _ harness.Driver = d
	if !errors.Is(context.Canceled, context.Canceled) {
		t.Fatal("unreachable")
	}
}
