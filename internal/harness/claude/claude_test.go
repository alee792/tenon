package claude

import (
	"testing"

	"github.com/alee792/tenon/internal/harness"
)

// TestClassifyResult proves the terminal result frame maps to a status and a
// bounded reason, and that a missing session id is a process error.
func TestClassifyResult(t *testing.T) {
	got, err := classifyResult(frame{Type: "result", Subtype: "success", SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != harness.StatusCompleted || got.SessionID != "s1" || got.Reason != "" {
		t.Fatalf("success mapped to %+v", got)
	}

	got, err = classifyResult(frame{Type: "result", Subtype: "success", IsError: true, SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != harness.StatusFailed || got.Reason != "turn_failed" {
		t.Fatalf("error result mapped to %+v", got)
	}

	if _, err := classifyResult(frame{Type: "result", Subtype: "success"}); err == nil {
		t.Fatal("a result without a session id must be a process error")
	}
}

// TestAssistantDeltas proves only non-empty text blocks are extracted, in
// order.
func TestAssistantDeltas(t *testing.T) {
	f := frame{Type: "assistant", Message: message{Content: []block{
		{Type: "text", Text: "hello "},
		{Type: "tool_use", Text: ""},
		{Type: "text", Text: "world"},
		{Type: "text", Text: ""},
	}}}
	got := assistantDeltas(f)
	if len(got) != 2 || got[0] != "hello " || got[1] != "world" {
		t.Fatalf("deltas = %v", got)
	}
}

// TestInitEventResumeMismatch proves a resumed session that reports a different
// id fails the turn, and that a matching id resumes.
func TestInitEventResumeMismatch(t *testing.T) {
	s := &session{resumeID: "want"}
	if _, err := s.initEvent(frame{SessionID: "other"}); err == nil {
		t.Fatal("a mismatched resume id must be a process error")
	}
	ev, err := s.initEvent(frame{SessionID: "want"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != harness.EventSessionResumed || ev.SessionID != "want" {
		t.Fatalf("resume event = %+v", ev)
	}

	fresh := &session{}
	ev, err = fresh.initEvent(frame{SessionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != harness.EventSessionStarted || ev.SessionID != "new" {
		t.Fatalf("start event = %+v", ev)
	}
}
