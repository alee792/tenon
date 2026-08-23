package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/dispatchstate"
	"github.com/alee792/tenon/internal/harness"
)

func taskOptions(p *agentproject.Project, ws string, driver harness.Driver, turnTimeout time.Duration) Options {
	return Options{
		Project:      p,
		Driver:       driver,
		Workspace:    ws,
		Harness:      "claude",
		Conversation: "schedule-test",
		Mode:         Task,
		TurnTimeout:  turnTimeout,
	}
}

func TestRunTaskFreshOccurrenceCompletes(t *testing.T) {
	p, ws := appliedWorkspace(t)
	driver := &harness.FakeDriver{}
	driver.Push(harness.FakeTurn{
		Events: []harness.Event{{Type: harness.EventSessionStarted, SessionID: "sess-1"}},
		Result: harness.TurnResult{Status: harness.StatusCompleted, SessionID: "sess-1"},
	})

	out, err := RunTask(context.Background(), taskOptions(p, ws, driver, 0), "occ-a", "do the work")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != dispatchstate.Completed || out.Duplicate {
		t.Fatalf("outcome = %+v", out)
	}
	if opens := driver.Opens(); len(opens) != 1 {
		t.Fatalf("expected 1 Open, got %d", len(opens))
	}
	if !driver.Opens()[0].Fresh {
		t.Fatal("a task occurrence must open a fresh session")
	}
}

func TestRunTaskDuplicateReturnsRetainedWithoutOpen(t *testing.T) {
	p, ws := appliedWorkspace(t)
	driver := &harness.FakeDriver{}
	driver.Push(harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}})

	if _, err := RunTask(context.Background(), taskOptions(p, ws, driver, 0), "occ-dup", "work"); err != nil {
		t.Fatal(err)
	}
	if len(driver.Opens()) != 1 {
		t.Fatalf("first run should open once, got %d", len(driver.Opens()))
	}

	out, err := RunTask(context.Background(), taskOptions(p, ws, driver, 0), "occ-dup", "work")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Duplicate || out.Status != dispatchstate.Completed {
		t.Fatalf("duplicate outcome = %+v", out)
	}
	if len(driver.Opens()) != 1 {
		t.Fatalf("a duplicate must not open a harness: opens=%d", len(driver.Opens()))
	}
}

func TestRunTaskDeadlineExceededIsUncertain(t *testing.T) {
	p, ws := appliedWorkspace(t)
	driver := &harness.FakeDriver{}
	driver.Push(harness.FakeTurn{Block: true}) // never completes on its own

	out, err := RunTask(context.Background(), taskOptions(p, ws, driver, 50*time.Millisecond), "occ-slow", "work")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != dispatchstate.Uncertain || out.Reason != "deadline_exceeded" {
		t.Fatalf("outcome = %+v", out)
	}
	if driver.Aborts() != 1 {
		t.Fatalf("deadline must abort the session once, got %d", driver.Aborts())
	}
}

func TestRunTaskNonCompletedStatusPropagates(t *testing.T) {
	p, ws := appliedWorkspace(t)
	driver := &harness.FakeDriver{}
	driver.Push(harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusFailed, Reason: "tool_error"}})

	out, err := RunTask(context.Background(), taskOptions(p, ws, driver, 0), "occ-fail", "work")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != dispatchstate.Failed || out.Reason != "tool_error" {
		t.Fatalf("outcome = %+v", out)
	}
}

// TestBoundReasonProducesValidUTF8 guards the fix for a harness error string
// truncated mid-rune: an invalid-UTF-8 reason would be rejected by the durable
// store and escalate one occurrence's outcome into a clock-halting failure.
func TestBoundReasonProducesValidUTF8(t *testing.T) {
	// 'é' (two bytes) straddles the byte cap, so a naive slice cuts it in half.
	straddle := strings.Repeat("a", dispatchstate.MaxReasonBytes-1) + "é" + "tail"
	got := boundReason(straddle)
	if !utf8.ValidString(got) {
		t.Fatalf("boundReason produced invalid UTF-8: %q", got)
	}
	if len(got) > dispatchstate.MaxReasonBytes {
		t.Fatalf("boundReason exceeded the cap: %d bytes", len(got))
	}
	// A source that is already invalid UTF-8 (raw harness bytes) is cleaned.
	if dirty := boundReason("ok\xff\xfebad"); !utf8.ValidString(dirty) {
		t.Fatalf("boundReason left invalid bytes: %q", dirty)
	}
}
