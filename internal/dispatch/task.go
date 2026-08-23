package dispatch

// RunTask dispatches exactly one task-mode occurrence, reusing the durable
// acceptance, deduplication, fresh-session, turn-deadline, and terminal-outcome
// responsibilities the JSONL Run loop rests on (ADR 0008). It is the single
// building block behind both `tenon schedule trigger` and `tenon schedule run`:
// trigger calls RunTask once; the foreground clock calls RunTaskWithStore
// concurrently across schedules over one shared, mutex-guarded store, so the
// single-owner dispatch file stays consistent while distinct schedules run at
// once.
//
// Unlike Run, RunTask drives one occurrence synchronously rather than through
// the owner/reader/turn goroutine choreography: a single occurrence has no
// concurrent input to accept, so the compact synchronous path is clearer than
// feeding one synthetic JSONL line through Run and re-parsing its events. It
// shares Run's terminal-classification helpers (terminalStatus, boundReason) so
// classification is identical.

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/alee792/tenon/internal/dispatchstate"
	"github.com/alee792/tenon/internal/harness"
)

// Outcome is one occurrence's bounded terminal report. It never carries model
// text. Duplicate is true when the occurrence id was already known in its
// conversation, in which case the retained Status and Reason are returned and
// no harness is opened.
type Outcome struct {
	Status    dispatchstate.Status
	Reason    string
	SessionID string
	Duplicate bool
}

// RunTask opens the workspace's dispatch store and runs one occurrence through
// RunTaskWithStore. It is the one-shot entry point (`tenon schedule trigger`).
// The caller is responsible for verifying that the workspace still carries the
// applied setup (apply.Verify) and that the driver is usable; RunTask itself
// performs no verification so the foreground clock can verify once at startup
// rather than per occurrence.
func RunTask(ctx context.Context, opts Options, occurrenceID, prompt string) (Outcome, error) {
	store, err := dispatchstate.Open(opts.Workspace)
	if err != nil {
		return Outcome{}, fmt.Errorf("dispatch: %w", err)
	}
	return RunTaskWithStore(ctx, store, opts, occurrenceID, prompt)
}

// RunTaskWithStore runs one task occurrence against an already-open store. A
// repeated occurrenceID returns the retained outcome with Duplicate true and
// opens no harness. Otherwise it opens a fresh native session, drives one turn,
// and durably records the terminal outcome: a fired turn deadline or a process
// failure records the occurrence uncertain with a stable reason so the same id
// returns that classified result rather than replaying the work. The returned
// error is reserved for durable-state I/O failures; every occurrence outcome,
// including uncertain, is reported through Outcome with a nil error.
//
// The occurrence runs under ctx, but its turn deadline is enforced by a timer
// that aborts the session independently, so a caller that keeps ctx alive
// during a graceful drain still bounds the turn. Callers that must not have an
// in-flight occurrence cancelled by a stop signal pass a ctx not tied to that
// signal.
func RunTaskWithStore(ctx context.Context, store *dispatchstate.Store, opts Options, occurrenceID, prompt string) (Outcome, error) {
	if opts.Project == nil {
		return Outcome{}, fmt.Errorf("dispatch: a loaded agent project is required")
	}
	if opts.Driver == nil {
		return Outcome{}, fmt.Errorf("dispatch: a harness driver is required")
	}
	conversation := opts.Conversation
	if conversation == "" {
		conversation = "local"
	}
	ref := dispatchstate.Ref{
		Agent:        opts.Project.Name,
		Fingerprint:  opts.Project.Fingerprint,
		Harness:      opts.Harness,
		Conversation: conversation,
	}

	accepted, err := store.Accept(ref, occurrenceID, prompt)
	if err != nil {
		return Outcome{}, fmt.Errorf("dispatch: %w", err)
	}
	if accepted.Rejected {
		return Outcome{}, fmt.Errorf("dispatch: occurrence rejected: %s", accepted.Reason)
	}
	if accepted.Duplicate {
		return Outcome{Status: accepted.Status, Reason: accepted.Reason, Duplicate: true}, nil
	}

	head, _, ok, err := store.StartNext(ref)
	if err != nil {
		return Outcome{}, fmt.Errorf("dispatch: %w", err)
	}
	if !ok {
		// The occurrence was just accepted and nothing else runs this
		// conversation, so its queue head must be promotable; a miss is a bug.
		return Outcome{}, fmt.Errorf("dispatch: the accepted occurrence could not be started")
	}

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session, err := opts.Driver.Open(turnCtx, harness.OpenRequest{
		Source:    opts.Project.Root,
		Workspace: opts.Workspace,
		Fresh:     true,
	})
	if err != nil {
		return completeTask(store, ref, head.InputID, dispatchstate.Uncertain, boundReason(err.Error()), "")
	}

	var deadlineFired atomic.Bool
	var timer *time.Timer
	if opts.TurnTimeout > 0 {
		timer = time.AfterFunc(opts.TurnTimeout, func() {
			deadlineFired.Store(true)
			session.Abort()
		})
	}

	var sessionID string
	result, runErr := session.RunTurn(turnCtx, harness.Input{ID: head.InputID, Text: head.Text}, func(ev harness.Event) {
		if ev.SessionID != "" {
			sessionID = ev.SessionID
		}
	})
	// Stop the deadline timer before closing so a turn that returned cleanly
	// is not aborted-then-misclassified in the window after RunTurn, and so
	// Abort never lands on an already-closed session.
	if timer != nil {
		timer.Stop()
	}
	_ = session.Close()
	if result.SessionID != "" {
		sessionID = result.SessionID
	}

	if deadlineFired.Load() {
		return completeTask(store, ref, head.InputID, dispatchstate.Uncertain, "deadline_exceeded", sessionID)
	}
	if runErr != nil {
		return completeTask(store, ref, head.InputID, dispatchstate.Uncertain, boundReason(runErr.Error()), sessionID)
	}
	return completeTask(store, ref, head.InputID, terminalStatus(result.Status), result.Reason, sessionID)
}

// completeTask records one occurrence's terminal outcome and returns it. A
// store failure here is a durable-state I/O error, distinct from the
// occurrence's own (already classified) outcome.
func completeTask(store *dispatchstate.Store, ref dispatchstate.Ref, id string, status dispatchstate.Status, reason, sessionID string) (Outcome, error) {
	if err := store.Complete(ref, id, status, reason, true); err != nil {
		return Outcome{}, fmt.Errorf("dispatch: %w", err)
	}
	return Outcome{Status: status, Reason: reason, SessionID: sessionID}, nil
}
