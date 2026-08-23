//go:build harness

// These tests drive the REAL claude executable end to end. They are excluded
// from the normal suite and CI by the harness build tag and are run manually
// with `go test -tags harness ./internal/harness/claude/`.
package claude

import (
	"context"
	"testing"
	"time"

	"github.com/alee792/tenon/internal/harness"
)

// TestRealClaudeOneTurn opens a fresh session against the real claude binary,
// runs one trivial turn, and asserts a session id and a terminal status.
func TestRealClaudeOneTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	d := NewDriver("claude")
	if err := d.Verify(ctx); err != nil {
		t.Skipf("claude is not runnable on this machine: %v", err)
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
		t.Fatal("expected a resumable session id")
	}
	if result.Status != harness.StatusCompleted && result.Status != harness.StatusFailed {
		t.Fatalf("unexpected terminal status %q", result.Status)
	}
}
