package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/harness"
)

// TestClassifyStatus proves each codex turn status maps to a terminal Status,
// with unknown or non-terminal statuses treated conservatively as cancelled.
func TestClassifyStatus(t *testing.T) {
	cases := map[string]harness.Status{
		"completed":   harness.StatusCompleted,
		"failed":      harness.StatusFailed,
		"interrupted": harness.StatusCancelled,
		"":            harness.StatusCancelled,
	}
	for status, want := range cases {
		if got := classifyStatus(status); got != want {
			t.Fatalf("classifyStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

// TestSafeReasonNeverLeaksSecret is the security-critical proof: a failure
// message carrying credential material is reduced to a fixed category and the
// secret appears nowhere in the output.
func TestSafeReasonNeverLeaksSecret(t *testing.T) {
	secret := "sk-svcac-SECRET123"
	msg := "request failed: 401 Unauthorized (api key " + secret + ")"
	got := safeReason(msg)
	if got != "authentication" {
		t.Fatalf("safeReason classified a 401 as %q, want authentication", got)
	}
	if strings.Contains(got, "SECRET123") || strings.Contains(got, secret) {
		t.Fatalf("safeReason leaked the secret: %q", got)
	}

	// A non-credential failure is the generic category, still text-free.
	generic := safeReason("the model returned an internal error for " + secret)
	if generic != "turn_failed" {
		t.Fatalf("generic failure classified as %q", generic)
	}
	if strings.Contains(generic, "SECRET123") {
		t.Fatalf("generic reason leaked the secret: %q", generic)
	}
}

// TestClassifyCompletedDropsErrorText proves a full turn/completed frame maps to
// a status and a bounded reason that never carries the raw error message.
func TestClassifyCompletedDropsErrorText(t *testing.T) {
	s := &session{threadID: "t1"}
	secret := "sk-svcac-SECRET123"
	params := json.RawMessage(`{"threadId":"t1","turn":{"status":"failed","error":{"message":"401 unauthorized ` + secret + `"}}}`)
	got, err := s.classifyCompleted(params)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != harness.StatusFailed || got.SessionID != "t1" {
		t.Fatalf("failed turn mapped to %+v", got)
	}
	if got.Reason != "authentication" {
		t.Fatalf("reason = %q, want authentication", got.Reason)
	}
	if strings.Contains(got.Reason, secret) || strings.Contains(got.Reason, "SECRET123") {
		t.Fatalf("reason leaked the secret: %q", got.Reason)
	}

	ok, err := s.classifyCompleted(json.RawMessage(`{"turn":{"status":"completed","error":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ok.Status != harness.StatusCompleted || ok.Reason != "" {
		t.Fatalf("completed turn mapped to %+v", ok)
	}
}

// TestItemDeltas proves assistant text is extracted while the prompt echo and
// non-text content are skipped.
func TestItemDeltas(t *testing.T) {
	if got := itemDeltas(json.RawMessage(`{"item":{"type":"userMessage","content":[{"type":"text","text":"prompt"}]}}`)); got != nil {
		t.Fatalf("prompt echo yielded deltas: %v", got)
	}
	got := itemDeltas(json.RawMessage(`{"item":{"type":"agentMessage","content":[{"type":"text","text":"hi "},{"type":"image","text":""},{"type":"text","text":"there"}]}}`))
	if len(got) != 2 || got[0] != "hi " || got[1] != "there" {
		t.Fatalf("deltas = %v", got)
	}
}
