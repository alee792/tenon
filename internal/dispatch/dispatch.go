// Package dispatch is the headless turn dispatcher's run loop. It reads bounded
// JSONL input, durably accepts and deduplicates it per conversation, drives one
// FIFO turn at a time through a harness.Driver, and emits an ordered wire event
// stream. Durable state and its recovery live in internal/dispatchstate; the
// harness seam lives in internal/harness; this package is the orchestration
// that binds them (see docs/product-spec.md "Headless operation" and ADR 0008).
//
// One goroutine — the owner — solely owns the dispatchstate.Store and the wire
// encoder. A separate reader goroutine decodes input lines and hands them to
// the owner over a channel, so input is durably accepted while a turn is
// active; the turn itself runs in a third goroutine whose events and result are
// marshaled back to the owner, keeping every store mutation and every emitted
// line single-owned and race-free. Only one turn runs at a time: the owner
// starts the next turn only after the current one's terminal outcome is
// recorded.
package dispatch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/dispatchstate"
	"github.com/alee792/tenon/internal/harness"
)

// Mode selects a conversation's session discipline.
type Mode int

const (
	// Interactive resumes the recorded native session across turns.
	Interactive Mode = iota
	// Task opens a fresh native session every turn and never resumes (ADR
	// 0008).
	Task
)

// Options configures one Run.
type Options struct {
	// Project is the loaded, validated agent project being dispatched.
	Project *agentproject.Project
	// Driver drives one native harness.
	Driver harness.Driver
	// Workspace is the workspace the session runs in and where dispatch state
	// lives.
	Workspace string
	// Harness is the applied harness name ("claude" or "codex").
	Harness string
	// Conversation is the caller-owned conversation id; empty means "local".
	Conversation string
	// Manifest is the supplied agent manifest's identity, stamped on every
	// emitted wire event as a provenance join key. Empty when no manifest was
	// supplied, in which case events carry no manifest field.
	Manifest string
	// Mode selects interactive or task session discipline.
	Mode Mode
	// In carries bounded JSONL input, one {input_id,text} object per line.
	In io.Reader
	// Out receives the wire event stream, one JSON object per line.
	Out io.Writer
	// TurnTimeout bounds one task-mode turn; 0 means no per-turn deadline.
	TurnTimeout time.Duration
}

// dispatcher is one Run's owner state. Every field is touched only by the owner
// goroutine unless noted.
type dispatcher struct {
	store        *dispatchstate.Store
	ref          dispatchstate.Ref
	driver       harness.Driver
	project      *agentproject.Project
	workspace    string
	harness      string
	conversation string
	fingerprint  string
	manifest     string
	mode         Mode
	turnTimeout  time.Duration

	enc   *json.Encoder
	seq   int
	turns Turns

	// per-turn state, valid only while turnActive
	turnActive    bool
	current       dispatchstate.QueuedInput
	session       harness.Session
	turnCancel    context.CancelFunc
	deadlineFired bool
	turnEventCh   chan harness.Event
	turnDoneCh    chan turnOutcome
	turnDeadline  <-chan time.Time
}

// turnOutcome is a finished RunTurn's report, marshaled from the turn goroutine
// back to the owner.
type turnOutcome struct {
	result harness.TurnResult
	err    error
}

// subKind classifies a line the reader hands to the owner.
type subKind int

const (
	subValid subKind = iota
	subInvalidJSON
	subFatal
)

// submission is one decoded input line (or a terminal reader signal) sent to
// the owner.
type submission struct {
	kind    subKind
	inputID string
	text    string
	err     error
}

// Run executes the dispatch loop until input is exhausted, the context is done,
// or a fatal input or state error occurs. It fails closed at startup if the
// workspace's generated setup is stale or missing.
//
// A clean run ends the stream itself with the terminal run.completed event
// carrying outcome "ok" and the turn counts. "ok" means the dispatcher
// completed every turn it was given, whatever those turns' own statuses: a
// loop reads the counts to score, and reads the outcome only to learn
// whether the dispatch itself finished. The returned Summary lets a caller
// that must write its own terminator — Run returned an error, or never ran
// at all — continue this stream's sequence numbering.
func Run(ctx context.Context, opts Options) (Summary, error) {
	if opts.Project == nil {
		return Summary{}, errors.New("dispatch: a loaded agent project is required")
	}
	if opts.Driver == nil {
		return Summary{}, errors.New("dispatch: a harness driver is required")
	}

	// Fail closed on stale or missing generated setup, exactly as mcp serve
	// does: a dispatcher started against drifted setup would serve an agent
	// nobody applied.
	if err := apply.Verify(opts.Project, opts.Workspace, opts.Harness); err != nil {
		return Summary{}, fmt.Errorf("dispatch: %w", err)
	}
	if err := opts.Driver.Verify(ctx); err != nil {
		return Summary{}, fmt.Errorf("dispatch: the %s harness could not be verified: %w", opts.Harness, err)
	}

	store, err := dispatchstate.Open(opts.Workspace)
	if err != nil {
		return Summary{}, fmt.Errorf("dispatch: %w", err)
	}

	conversation := opts.Conversation
	if conversation == "" {
		conversation = "local"
	}
	d := &dispatcher{
		store:        store,
		driver:       opts.Driver,
		project:      opts.Project,
		workspace:    opts.Workspace,
		harness:      opts.Harness,
		conversation: conversation,
		fingerprint:  opts.Project.Fingerprint,
		manifest:     opts.Manifest,
		mode:         opts.Mode,
		turnTimeout:  opts.TurnTimeout,
		enc:          json.NewEncoder(opts.Out),
		turnEventCh:  make(chan harness.Event),
		turnDoneCh:   make(chan turnOutcome, 1),
	}
	d.ref = dispatchstate.Ref{
		Agent:        opts.Project.Name,
		Fingerprint:  opts.Project.Fingerprint,
		Harness:      opts.Harness,
		Conversation: conversation,
	}

	if err := d.recover(); err != nil {
		return d.summary(), err
	}

	subCh := make(chan submission)
	go readInput(ctx, opts.In, subCh)
	if err := d.loop(ctx, subCh); err != nil {
		return d.summary(), err
	}
	// The stream terminates the way every other command's does — with one
	// object carrying an outcome — except that here it is also an event, so
	// the envelope invariant holds for the last line as much as the first.
	if err := d.emit(event{Type: typeRunCompleted, Outcome: "ok", Turns: &d.turns}); err != nil {
		return d.summary(), err
	}
	return d.summary(), nil
}

// summary reports the sequence reached and the turn counts, for a caller
// that must continue or terminate this stream itself.
func (d *dispatcher) summary() Summary {
	return Summary{Sequence: d.seq, Turns: d.turns}
}

// recover terminalizes any turn left active by a prior dispatcher and reports
// each recovered input as uncertain, once, before any new turn can start. Such
// work has no proven terminal result and is never re-executed.
func (d *dispatcher) recover() error {
	var recovered []dispatchstate.Recovered
	var err error
	if d.mode == Task {
		recovered, err = d.store.RecoverTaskUncertain(d.ref)
	} else {
		recovered, err = d.store.RecoverUncertain(d.ref)
	}
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	for _, r := range recovered {
		if err := d.emit(event{Type: typeTurnUncertain, InputID: r.InputID, Reason: r.Reason}); err != nil {
			return err
		}
	}
	return nil
}

// loop is the owner: it starts turns, accepts submissions while a turn runs,
// forwards streamed events, records terminal outcomes, and enforces the
// per-turn deadline. It returns when input is exhausted with nothing left to
// run, on the context being done, or on a fatal error.
func (d *dispatcher) loop(ctx context.Context, subCh chan submission) error {
	for {
		if !d.turnActive {
			started, err := d.startNextTurn(ctx)
			if err != nil {
				return err
			}
			if !started {
				if subCh == nil {
					return nil // reader done, nothing running, nothing queued
				}
			} else {
				continue // a turn is now active; re-enter select for its events
			}
		}

		select {
		case <-ctx.Done():
			d.abortActiveTurn()
			return ctx.Err()

		case sub, ok := <-subCh:
			if !ok {
				subCh = nil
				continue
			}
			if sub.kind == subFatal {
				d.abortActiveTurn()
				return sub.err
			}
			if err := d.accept(sub); err != nil {
				return err
			}

		case ev := <-d.turnEventCh:
			if err := d.forward(ev); err != nil {
				return err
			}

		case <-d.turnDeadline:
			if d.turnActive {
				d.deadlineFired = true
				d.session.Abort()
				d.turnDeadline = nil
			}

		case out := <-d.turnDoneCh:
			if err := d.finishTurn(out); err != nil {
				return err
			}
		}
	}
}

// accept durably records one submission and emits its acceptance decision.
func (d *dispatcher) accept(sub submission) error {
	if sub.kind == subInvalidJSON {
		return d.emit(event{Type: typeInputRejected, Reason: "invalid_json"})
	}
	res, err := d.store.Accept(d.ref, sub.inputID, sub.text)
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	if res.Rejected {
		return d.emit(event{Type: typeInputRejected, InputID: sub.inputID, Reason: res.Reason})
	}
	if res.Duplicate {
		return d.emit(event{
			Type:    typeInputDuplicate,
			InputID: sub.inputID,
			Status:  string(res.Status),
			Reason:  res.Reason,
		})
	}
	if err := d.emit(event{Type: typeInputAccepted, InputID: sub.inputID, Bytes: len(sub.text)}); err != nil {
		return err
	}
	return d.emit(event{Type: typeTurnQueued, InputID: sub.inputID})
}

// startNextTurn promotes the queue head, opens its session, emits the session
// and turn.started events, and spawns the turn goroutine. It reports whether a
// turn is now active. An Open failure is a process failure: the head is
// completed uncertain and no turn becomes active.
func (d *dispatcher) startNextTurn(ctx context.Context) (bool, error) {
	head, resumeID, ok, err := d.store.StartNext(d.ref)
	if err != nil {
		return false, fmt.Errorf("dispatch: %w", err)
	}
	if !ok {
		return false, nil
	}

	fresh := d.mode == Task
	req := harness.OpenRequest{Source: d.project.Root, Workspace: d.workspace, Fresh: fresh}
	if !fresh {
		req.ResumeID = resumeID
	}

	turnCtx, cancel := context.WithCancel(ctx)
	session, err := d.driver.Open(turnCtx, req)
	if err != nil {
		cancel()
		return false, d.processFailed(head.InputID, err)
	}

	// The driver is authoritative for session.started/resumed: only it learns
	// the real native session id (from the harness's first streamed frame), so
	// it emits that event through the turn's event stream. The dispatcher does
	// not emit its own session event, which would duplicate the driver's and
	// carry no id on a fresh start.
	if err := d.emit(event{Type: typeTurnStarted, InputID: head.InputID}); err != nil {
		cancel()
		session.Close()
		return false, err
	}

	d.turnActive = true
	d.current = head
	d.session = session
	d.turnCancel = cancel
	d.deadlineFired = false
	d.turnDeadline = nil
	if d.mode == Task && d.turnTimeout > 0 {
		d.turnDeadline = time.After(d.turnTimeout)
	}
	go runTurn(turnCtx, session, head, d.turnEventCh, d.turnDoneCh)
	return true, nil
}

// forward translates one streamed harness event onto the wire stream. It runs
// in the owner goroutine so the encoder is never shared.
func (d *dispatcher) forward(ev harness.Event) error {
	return d.emit(event{
		Type:      ev.Type,
		InputID:   d.current.InputID,
		SessionID: ev.SessionID,
		Delta:     ev.Delta,
	})
}

// finishTurn records the current turn's terminal outcome and emits its terminal
// event, then clears the per-turn state so the next turn can start. A fired
// deadline classifies the turn uncertain; a process failure classifies it
// uncertain too; otherwise the harness's reported status stands.
func (d *dispatcher) finishTurn(out turnOutcome) error {
	d.clearTurn()

	id := d.current.InputID
	task := d.mode == Task

	if d.deadlineFired {
		if err := d.store.Complete(d.ref, id, dispatchstate.Uncertain, "deadline_exceeded", task); err != nil {
			return fmt.Errorf("dispatch: %w", err)
		}
		d.turns.Uncertain++
		return d.emit(event{Type: typeTurnUncertain, InputID: id, Reason: "deadline_exceeded"})
	}
	if out.err != nil {
		return d.processFailed(id, out.err)
	}

	if d.mode == Interactive && out.result.SessionID != "" {
		if err := d.store.SetSessionID(d.ref, out.result.SessionID); err != nil {
			return fmt.Errorf("dispatch: %w", err)
		}
	}
	status := terminalStatus(out.result.Status)
	if err := d.store.Complete(d.ref, id, status, out.result.Reason, task); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	d.count(status)
	return d.emit(event{
		Type:      "turn." + string(status),
		InputID:   id,
		SessionID: out.result.SessionID,
		Reason:    out.result.Reason,
	})
}

// processFailed records one input as uncertain because the harness could not be
// driven to a proven result, and emits driver.process_failed.
func (d *dispatcher) processFailed(id string, cause error) error {
	reason := boundReason(cause.Error())
	if err := d.store.Complete(d.ref, id, dispatchstate.Uncertain, reason, d.mode == Task); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	d.turns.ProcessFailed++
	return d.emit(event{Type: typeDriverProcessed, InputID: id, Reason: reason})
}

// count records one turn's terminal status. Only turns this dispatcher ran
// are counted: an input recovered as uncertain at startup belongs to the
// dispatcher that abandoned it, and counting it here would attribute another
// run's work to this one.
func (d *dispatcher) count(status dispatchstate.Status) {
	switch status {
	case dispatchstate.Completed:
		d.turns.Completed++
	case dispatchstate.Failed:
		d.turns.Failed++
	case dispatchstate.Cancelled:
		d.turns.Cancelled++
	default:
		d.turns.Uncertain++
	}
}

// abortActiveTurn interrupts the running turn and drains it without recording a
// terminal outcome: the input stays active for uncertain recovery at the next
// startup. It is used on context cancellation and fatal input errors.
func (d *dispatcher) abortActiveTurn() {
	if !d.turnActive {
		return
	}
	d.session.Abort()
	for {
		select {
		case <-d.turnEventCh:
		case <-d.turnDoneCh:
			d.clearTurn()
			return
		}
	}
}

// clearTurn cancels the turn context and clears per-turn state. It does not
// touch durable state.
func (d *dispatcher) clearTurn() {
	if d.turnCancel != nil {
		d.turnCancel()
	}
	d.turnActive = false
	d.session = nil
	d.turnCancel = nil
	d.turnDeadline = nil
}

// runTurn drives one turn in its own goroutine, forwarding events to the owner
// and reporting the outcome. The session is always closed once RunTurn returns.
func runTurn(ctx context.Context, session harness.Session, head dispatchstate.QueuedInput, events chan<- harness.Event, done chan<- turnOutcome) {
	emit := func(ev harness.Event) { events <- ev }
	result, err := session.RunTurn(ctx, harness.Input{ID: head.InputID, Text: head.Text}, emit)
	_ = session.Close()
	done <- turnOutcome{result: result, err: err}
}

// readInput scans bounded JSONL lines from in and hands each to the owner. A
// blank line is skipped; a decode failure is reported as invalid_json and the
// loop continues; an over-limit line is fatal. It stops when in is exhausted or
// the context is done, and always closes subCh on the non-fatal exhaustion
// path.
func readInput(ctx context.Context, in io.Reader, subCh chan<- submission) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), dispatchstate.MaxInputBytes+4096)
	send := func(sub submission) bool {
		select {
		case subCh <- sub:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var decoded struct {
			InputID string `json:"input_id"`
			Text    string `json:"text"`
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&decoded); err != nil || dec.More() {
			if !send(submission{kind: subInvalidJSON}) {
				return
			}
			continue
		}
		if !send(submission{kind: subValid, inputID: decoded.InputID, text: decoded.Text}) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			send(submission{kind: subFatal, err: errors.New("run input exceeded the bounded JSONL line size")})
			return
		}
		send(submission{kind: subFatal, err: fmt.Errorf("dispatch: reading run input: %w", err)})
		return
	}
	close(subCh)
}

// terminalStatus maps a harness status onto a durable terminal status,
// defaulting an unrecognized value to uncertain rather than trusting it.
func terminalStatus(s harness.Status) dispatchstate.Status {
	switch s {
	case harness.StatusCompleted:
		return dispatchstate.Completed
	case harness.StatusFailed:
		return dispatchstate.Failed
	case harness.StatusCancelled:
		return dispatchstate.Cancelled
	default:
		return dispatchstate.Uncertain
	}
}

// boundReason flattens and bounds a process error to a single-line,
// model-text-free reason within the store's reason bound.
func boundReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > dispatchstate.MaxReasonBytes {
		s = s[:dispatchstate.MaxReasonBytes]
	}
	// A byte-length cap can slice mid-rune, and the source (a harness error
	// string) may itself be invalid UTF-8. The durable store rejects invalid
	// UTF-8, and a rejected reason would escalate one occurrence's classified
	// outcome into a durable-state failure that halts the whole clock, so
	// drop any invalid sequence here.
	s = strings.ToValidUTF8(s, "")
	if s == "" {
		return "process_failed"
	}
	return s
}
