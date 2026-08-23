//go:build harness

// These tests drive the REAL codex executable end to end. They are excluded
// from the normal suite and CI by the harness build tag and are run manually
// with `go test -tags harness ./internal/harness/codex/`.
//
// NOTE: the success path is UNVALIDATED on a machine whose stored key returns
// 401 — the handshake, thread start, and failure/error classification still run
// for real; only a clean turn/completed with status "completed" needs a working
// credential.
package codex

import (
	"context"
	"testing"
	"time"

	"github.com/alee792/tenon/internal/harness"
)

// TestRealCodexOneTurn handshakes with a real codex app-server, starts a fresh
// thread, runs one trivial turn, and asserts a thread id and a terminal status.
func TestRealCodexOneTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	d := NewDriver("codex", "0.1.0-dev")
	if err := d.Verify(ctx); err != nil {
		t.Skipf("codex is not runnable on this machine: %v", err)
	}
	sess, err := d.Open(ctx, harness.OpenRequest{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var started bool
	result, err := sess.RunTurn(ctx, harness.Input{ID: "t1", Text: "Reply with the single word ok."}, func(ev harness.Event) {
		if ev.Type == harness.EventSessionStarted {
			started = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("expected a session.started event")
	}
	if result.SessionID == "" {
		t.Fatal("expected a thread id as the session id")
	}
	switch result.Status {
	case harness.StatusCompleted, harness.StatusFailed, harness.StatusCancelled:
	default:
		t.Fatalf("unexpected terminal status %q", result.Status)
	}
}
