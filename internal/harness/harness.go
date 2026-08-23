// Package harness is the driver seam between tenon's headless turn dispatcher
// and a native coding-agent harness (Claude Code or Codex). It defines only the
// orchestration contract: how the dispatcher opens a session, runs exactly one
// turn, streams events, and learns a turn's terminal outcome. The real wire
// protocol drivers live behind this seam and are built in a later slice; a
// dependency-free FakeDriver (fake.go) stands in for "some harness" so the
// dispatcher is provable without a live model.
//
// A driver never owns durable dispatch state, deduplication, or the wire event
// stream: those are the dispatcher's. The seam carries only a single turn's
// lifecycle. Crucially, RunTurn's non-nil error is a process failure — the
// harness could not be driven to a proven result — and is categorically
// distinct from a model turn that ran and reported a failed Status. The
// dispatcher treats a process failure as uncertain, never as a clean terminal.
package harness

import "context"

// Driver opens sessions against one native harness. It is stateless with
// respect to any single conversation: durable queueing and resume bookkeeping
// live in the dispatcher, which passes the resume identifier back on Open.
type Driver interface {
	// Name is the stable harness name, matching the applied harness ("claude"
	// or "codex").
	Name() string
	// Verify reports whether the harness can be driven at all — for a real
	// driver, that its executable resolves. It never mutates state.
	Verify(ctx context.Context) error
	// Open starts (or resumes) one native session for a single turn. The
	// returned Session runs exactly one turn and is then closed by the caller.
	Open(ctx context.Context, req OpenRequest) (Session, error)
}

// OpenRequest is everything a driver needs to start or resume one session.
type OpenRequest struct {
	// Source is the agent project's source root.
	Source string
	// Workspace is the absolute workspace the session runs in.
	Workspace string
	// ResumeID is the native session id to resume, or empty to start clean.
	// It is always empty when Fresh is true.
	ResumeID string
	// Fresh forces a brand-new session with no inherited context, even if a
	// resume id was recorded. Task-mode turns are always fresh (ADR 0008).
	Fresh bool
}

// Session is one opened native session, driven for exactly one turn.
type Session interface {
	// RunTurn drives one turn to a terminal result, calling emit for each
	// streamed event as it happens. A non-nil error is a process failure: the
	// harness could not be driven to a proven result. A turn that ran but
	// failed is reported as a completed call with Status "failed", not an
	// error.
	RunTurn(ctx context.Context, in Input, emit func(Event)) (TurnResult, error)
	// Close releases the session's resources. It is always called once after
	// RunTurn returns.
	Close() error
	// Abort interrupts an in-flight RunTurn. It is safe to call from another
	// goroutine and safe to call more than once.
	Abort()
}

// Input is one turn's model-facing prompt and its caller-owned identifier.
type Input struct {
	ID   string
	Text string
}

// Event is one streamed turn event. Type is one of the Event* constants; the
// dispatcher translates it verbatim onto the wire stream. SessionID surfaces
// the native session id once the harness assigns it; Delta carries model output
// text and is the only event field that ever does.
type Event struct {
	Type      string
	SessionID string
	Delta     string
}

// The streamed event types a driver may emit through RunTurn's emit callback.
// agent.output.delta is the only model-text surface.
const (
	EventSessionStarted   = "session.started"
	EventSessionResumed   = "session.resumed"
	EventTurnStarted      = "turn.started"
	EventAgentOutputDelta = "agent.output.delta"
)

// Status is a completed turn's terminal classification, as the harness reported
// it. It never carries model text.
type Status string

const (
	// StatusCompleted is a turn the model finished normally.
	StatusCompleted Status = "completed"
	// StatusFailed is a turn that ran but the model or harness reported failed.
	StatusFailed Status = "failed"
	// StatusCancelled is a turn cancelled before or during execution.
	StatusCancelled Status = "cancelled"
	// StatusUncertain is a turn whose outcome the driver itself could not
	// prove. A process failure (RunTurn error) is classified uncertain by the
	// dispatcher regardless of this value.
	StatusUncertain Status = "uncertain"
)

// TurnResult is a turn's proven terminal outcome. SessionID is the resumable
// native session id to persist; Reason is a bounded, model-text-free
// classifier.
type TurnResult struct {
	SessionID string
	Status    Status
	Reason    string
}
