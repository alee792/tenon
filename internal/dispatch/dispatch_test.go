package dispatch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/dispatchstate"
	"github.com/alee792/tenon/internal/harness"
)

const testInstructions = `---
description: A dispatch test agent.
---

You do dispatch test work.
`

// appliedWorkspace builds a valid agent project and applies it into a fresh
// workspace with the real claude driver, so apply.Verify passes exactly as it
// would for a genuinely applied setup. It returns the loaded project and the
// workspace; the harness name is always "claude".
func appliedWorkspace(t *testing.T) (*agentproject.Project, string) {
	t.Helper()
	agentDir := filepath.Join(t.TempDir(), "my-agent")
	if err := os.Mkdir(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "instructions.md"), []byte(testInstructions), 0o644); err != nil {
		t.Fatal(err)
	}
	p, diags, err := agentproject.Load(agentDir)
	if err != nil || p == nil || diags.HasErrors() {
		t.Fatalf("load agent: err=%v diags=%v", err, diags)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if _, applyDiags, err := apply.Apply(p, ws, exe, claude.Driver{}); err != nil || applyDiags.HasErrors() {
		t.Fatalf("apply: err=%v diags=%v", err, applyDiags)
	}
	return p, ws
}

func options(p *agentproject.Project, ws string, in io.Reader, out io.Writer) Options {
	return Options{
		Project:   p,
		Driver:    &harness.FakeDriver{},
		Workspace: ws,
		Harness:   "claude",
		Mode:      Interactive,
		In:        in,
		Out:       out,
	}
}

// runCollect runs a dispatch to completion against buffered input and returns
// the decoded wire events.
func runCollect(t *testing.T, opts Options) []event {
	t.Helper()
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return decodeEvents(t, opts.Out.(*bytes.Buffer).Bytes())
}

func decodeEvents(t *testing.T, raw []byte) []event {
	t.Helper()
	var out []event
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var e event
		err := dec.Decode(&e)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode events: %v", err)
		}
		out = append(out, e)
	}
	return out
}

func lines(objs ...string) io.Reader {
	return strings.NewReader(strings.Join(objs, "\n") + "\n")
}

func typesFor(events []event, inputID string) []string {
	var out []string
	for _, e := range events {
		if e.InputID == inputID {
			out = append(out, e.Type)
		}
	}
	return out
}

func firstOf(events []event, typ string) (event, bool) {
	for _, e := range events {
		if e.Type == typ {
			return e, true
		}
	}
	return event{}, false
}

// TestFIFOAcrossInputs proves several inputs run one turn at a time in
// acceptance order.
func TestFIFOAcrossInputs(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	var out bytes.Buffer
	opts := options(p, ws, lines(
		`{"input_id":"a","text":"1"}`,
		`{"input_id":"b","text":"2"}`,
		`{"input_id":"c","text":"3"}`,
	), &out)
	opts.Driver = fake
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := fake.Inputs(); !equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("turn order = %v, want a,b,c", got)
	}
	events := decodeEvents(t, out.Bytes())
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := firstTerminal(events, id); !ok {
			t.Fatalf("input %s never reached a terminal turn event", id)
		}
	}
}

// TestAcceptWhileTurnActive proves input is durably accepted and queued while a
// turn is active, then run after it.
func TestAcceptWhileTurnActive(t *testing.T) {
	p, ws := appliedWorkspace(t)
	release := make(chan struct{})
	fake := &harness.FakeDriver{}
	fake.Push(
		harness.FakeTurn{Release: release, Result: harness.TurnResult{SessionID: "s1", Status: harness.StatusCompleted}},
		harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}},
	)

	pr, pw := io.Pipe()
	opts := options(p, ws, lines(
		`{"input_id":"a","text":"1"}`,
		`{"input_id":"b","text":"2"}`,
	), pw)
	opts.Driver = fake

	events := make(chan event)
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			var e event
			if err := json.Unmarshal(scanner.Bytes(), &e); err == nil {
				events <- e
			}
		}
		close(events)
	}()

	runErr := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), opts)
		pw.Close()
		runErr <- err
	}()

	var seen []event
	for e := range events {
		seen = append(seen, e)
		if e.Type == typeTurnQueued && e.InputID == "b" {
			break // b is durably queued while a's turn is still blocked
		}
	}
	close(release) // let a's turn finish; b then runs
	for e := range events {
		seen = append(seen, e)
	}
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}

	queuedB := indexOf(seen, typeTurnQueued, "b")
	completedA := indexOf(seen, typeTurnCompleted, "a")
	startedB := indexOf(seen, typeTurnStarted, "b")
	if queuedB < 0 || completedA < 0 || startedB < 0 {
		t.Fatalf("missing events: queuedB=%d completedA=%d startedB=%d\n%+v", queuedB, completedA, startedB, seen)
	}
	if !(queuedB < completedA) {
		t.Fatalf("b must be queued before a completes: queuedB=%d completedA=%d", queuedB, completedA)
	}
	if !(completedA < startedB) {
		t.Fatalf("b must start only after a completes: completedA=%d startedB=%d", completedA, startedB)
	}
}

// TestDuplicateRetainsOutcome proves a repeated input id deduplicates to
// input.duplicate with the retained status, both while queued and after
// completion.
func TestDuplicateRetainsOutcome(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	var out bytes.Buffer
	opts := options(p, ws, lines(
		`{"input_id":"dup","text":"1"}`,
		`{"input_id":"dup","text":"2"}`,
	), &out)
	opts.Driver = fake
	events := runCollect(t, opts)

	var dups []event
	for _, e := range events {
		if e.Type == typeInputDuplicate {
			dups = append(dups, e)
		}
	}
	if len(dups) != 1 {
		t.Fatalf("want exactly one input.duplicate, got %d: %+v", len(dups), events)
	}
	// The retained status is a durable dispatchstate status (queued or, once it
	// ran, completed); either way it is carried, never model text.
	if dups[0].Status == "" {
		t.Fatalf("input.duplicate must carry the retained status: %+v", dups[0])
	}
	if got := fake.Inputs(); len(got) != 1 || got[0] != "dup" {
		t.Fatalf("duplicate must not re-execute: ran %v", got)
	}
}

// TestInteractiveResumesSession proves a fresh open carries the prior turn's
// captured session id as the resume id.
func TestInteractiveResumesSession(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{}
	fake.Push(
		harness.FakeTurn{Result: harness.TurnResult{SessionID: "sess-1", Status: harness.StatusCompleted}},
		harness.FakeTurn{Result: harness.TurnResult{SessionID: "sess-2", Status: harness.StatusCompleted}},
	)
	var out bytes.Buffer
	opts := options(p, ws, lines(
		`{"input_id":"a","text":"1"}`,
		`{"input_id":"b","text":"2"}`,
	), &out)
	opts.Driver = fake
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	opens := fake.Opens()
	if len(opens) != 2 {
		t.Fatalf("want 2 opens, got %d", len(opens))
	}
	if opens[0].ResumeID != "" || opens[0].Fresh {
		t.Fatalf("first open must be a clean, non-fresh start: %+v", opens[0])
	}
	if opens[1].ResumeID != "sess-1" {
		t.Fatalf("second open must resume sess-1, got %q", opens[1].ResumeID)
	}
}

// TestTaskModeOpensFresh proves task mode opens a fresh session with no resume
// id every turn and never persists a resumable session.
func TestTaskModeOpensFresh(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{}
	fake.Push(
		harness.FakeTurn{Result: harness.TurnResult{SessionID: "sess-1", Status: harness.StatusCompleted}},
		harness.FakeTurn{Result: harness.TurnResult{SessionID: "sess-2", Status: harness.StatusCompleted}},
	)
	var out bytes.Buffer
	opts := options(p, ws, lines(
		`{"input_id":"a","text":"1"}`,
		`{"input_id":"b","text":"2"}`,
	), &out)
	opts.Driver = fake
	opts.Mode = Task
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i, o := range fake.Opens() {
		if !o.Fresh || o.ResumeID != "" {
			t.Fatalf("task open %d must be fresh with no resume id: %+v", i, o)
		}
	}
	// The persisted session id stays cleared across task turns.
	store, err := dispatchstate.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	_, resumeID, ok, err := store.StartNext(refFor(p))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("no input should remain queued")
	}
	if resumeID != "" {
		t.Fatalf("task mode must not persist a resumable session, got %q", resumeID)
	}
}

// TestProcessErrorIsUncertain proves a RunTurn process error emits
// driver.process_failed and records the input uncertain rather than a clean
// terminal.
func TestProcessErrorIsUncertain(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{}
	fake.Push(harness.FakeTurn{Err: errors.New("host crashed")})
	var out bytes.Buffer
	opts := options(p, ws, lines(`{"input_id":"a","text":"1"}`), &out)
	opts.Driver = fake
	events := runCollect(t, opts)

	e, ok := firstOf(events, typeDriverProcessed)
	if !ok {
		t.Fatalf("want driver.process_failed, got %+v", events)
	}
	if e.InputID != "a" || !strings.Contains(e.Reason, "host crashed") {
		t.Fatalf("driver.process_failed must name the input and bounded reason: %+v", e)
	}
	if _, ok := firstOf(events, typeTurnCompleted); ok {
		t.Fatal("a process failure must never be a clean terminal")
	}
	// A repeat of the same id now deduplicates to the retained uncertain
	// outcome.
	store, err := dispatchstate.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.Accept(refFor(p), "a", "again")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Duplicate || res.Status != dispatchstate.Uncertain {
		t.Fatalf("retained outcome must be uncertain, got %+v", res)
	}
}

// TestUncertainRestartRecovers proves a turn left active by a prior dispatcher
// is reported uncertain (dispatcher_restarted) at startup and never executed.
func TestUncertainRestartRecovers(t *testing.T) {
	p, ws := appliedWorkspace(t)
	// Pre-seed an active entry directly via dispatchstate, as a crashed
	// dispatcher would leave behind.
	seed, err := dispatchstate.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Accept(refFor(p), "left-active", "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := seed.StartNext(refFor(p)); err != nil || !ok {
		t.Fatalf("seed StartNext: ok=%v err=%v", ok, err)
	}

	fake := &harness.FakeDriver{}
	var out bytes.Buffer
	opts := options(p, ws, strings.NewReader(""), &out)
	opts.Driver = fake
	events := runCollect(t, opts)

	e, ok := firstOf(events, typeTurnUncertain)
	if !ok || e.InputID != "left-active" || e.Reason != "dispatcher_restarted" {
		t.Fatalf("want turn.uncertain(left-active, dispatcher_restarted), got %+v", events)
	}
	if len(fake.Inputs()) != 0 {
		t.Fatalf("recovered work must never execute, ran %v", fake.Inputs())
	}
}

// TestOverLimitLineIsFatal proves an input line over the bounded scanner size
// stops the run with a clear error.
func TestOverLimitLineIsFatal(t *testing.T) {
	p, ws := appliedWorkspace(t)
	huge := strings.Repeat("x", dispatchstate.MaxInputBytes+4096+100)
	var out bytes.Buffer
	opts := options(p, ws, strings.NewReader(huge+"\n"), &out)
	_, err := Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "bounded JSONL line size") {
		t.Fatalf("want bounded-line fatal error, got %v", err)
	}
}

// TestInvalidJSONContinues proves a malformed line is rejected as invalid_json
// and the loop keeps processing later lines.
func TestInvalidJSONContinues(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	var out bytes.Buffer
	opts := options(p, ws, lines(
		`not json at all`,
		`{"input_id":"ok","text":"1"}`,
	), &out)
	opts.Driver = fake
	events := runCollect(t, opts)

	rej, ok := firstOf(events, typeInputRejected)
	if !ok || rej.Reason != "invalid_json" {
		t.Fatalf("want input.rejected(invalid_json), got %+v", events)
	}
	if _, ok := firstTerminal(events, "ok"); !ok {
		t.Fatal("processing must continue after an invalid line")
	}
}

// TestRejectionReasons proves a bad id, empty text, and oversize text are each
// rejected with the store's reason.
func TestRejectionReasons(t *testing.T) {
	p, ws := appliedWorkspace(t)
	oversize := strings.Repeat("y", dispatchstate.MaxInputBytes+1)
	var out bytes.Buffer
	opts := options(p, ws, lines(
		`{"input_id":"!bad","text":"1"}`,
		`{"input_id":"empty","text":""}`,
		`{"input_id":"big","text":"`+oversize+`"}`,
	), &out)
	events := runCollect(t, opts)

	wantReason := map[string]string{
		"!bad":  "grammar",
		"empty": "empty",
		"big":   "over the",
	}
	for id, want := range wantReason {
		found := false
		for _, e := range events {
			if e.Type == typeInputRejected && e.InputID == id {
				if !strings.Contains(e.Reason, want) {
					t.Fatalf("reject %s reason %q, want substring %q", id, e.Reason, want)
				}
				found = true
			}
		}
		// The bad-id line has no input_id echoed only when the store rejects it
		// before echo; it still carries the offending id here.
		if id == "!bad" {
			// A bad id is still echoed on the rejection.
			if !found {
				t.Fatalf("bad id must be rejected: %+v", events)
			}
			continue
		}
		if !found {
			t.Fatalf("input %s was not rejected: %+v", id, events)
		}
	}
}

// TestEventStreamSchemaAndOrdering proves every event carries schema_version 1,
// a monotonic sequence, the harness and conversation, and that one input's
// events are ordered accepted < queued < started < terminal.
func TestEventStreamSchemaAndOrdering(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	var out bytes.Buffer
	opts := options(p, ws, lines(`{"input_id":"a","text":"1"}`), &out)
	opts.Driver = fake
	events := runCollect(t, opts)

	for i, e := range events {
		if e.SchemaVersion != 1 {
			t.Fatalf("event %d schema_version = %d", i, e.SchemaVersion)
		}
		if e.Sequence != i+1 {
			t.Fatalf("event %d sequence = %d, want %d", i, e.Sequence, i+1)
		}
		if e.Harness != "claude" || e.Conversation != "local" {
			t.Fatalf("event %d missing invariant fields: %+v", i, e)
		}
	}
	// accepted < queued < started < terminal, allowing session.* in between.
	accepted := indexOf(events, typeInputAccepted, "a")
	queued := indexOf(events, typeTurnQueued, "a")
	started := indexOf(events, typeTurnStarted, "a")
	completed := indexOf(events, typeTurnCompleted, "a")
	if !(accepted >= 0 && accepted < queued && queued < started && started < completed) {
		t.Fatalf("ordering violated: accepted=%d queued=%d started=%d completed=%d (%v)",
			accepted, queued, started, completed, typesFor(events, "a"))
	}
	// accepted carries the byte count.
	acc, _ := firstOf(events, typeInputAccepted)
	if acc.Bytes != 1 {
		t.Fatalf("input.accepted bytes = %d, want 1", acc.Bytes)
	}
}

// TestVerifyGuardFailsClosed proves a run against an un-applied workspace fails
// closed before accepting any input.
func TestVerifyGuardFailsClosed(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), "my-agent")
	if err := os.Mkdir(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "instructions.md"), []byte(testInstructions), 0o644); err != nil {
		t.Fatal(err)
	}
	p, diags, err := agentproject.Load(agentDir)
	if err != nil || p == nil || diags.HasErrors() {
		t.Fatalf("load: %v %v", err, diags)
	}
	var out bytes.Buffer
	opts := options(p, t.TempDir(), strings.NewReader(""), &out) // never applied
	_, err = Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "tenon apply") {
		t.Fatalf("want fail-closed apply guard, got %v", err)
	}
}

// TestTaskDeadlineAbortsUncertain proves a task turn that overruns its deadline
// is aborted and recorded uncertain with the deadline reason.
func TestTaskDeadlineAbortsUncertain(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{}
	fake.Push(harness.FakeTurn{Block: true}) // blocks until aborted
	var out bytes.Buffer
	opts := options(p, ws, lines(`{"input_id":"a","text":"1"}`), &out)
	opts.Driver = fake
	opts.Mode = Task
	opts.TurnTimeout = 20 * time.Millisecond
	events := runCollect(t, opts)

	e, ok := firstOf(events, typeTurnUncertain)
	if !ok || e.InputID != "a" || e.Reason != "deadline_exceeded" {
		t.Fatalf("want turn.uncertain(a, deadline_exceeded), got %+v", events)
	}
	if fake.Aborts() == 0 {
		t.Fatal("the stalled turn must be aborted")
	}
}

func refFor(p *agentproject.Project) dispatchstate.Ref {
	return dispatchstate.Ref{Agent: p.Name, Fingerprint: p.Fingerprint, Harness: "claude", Conversation: "local"}
}

func firstTerminal(events []event, inputID string) (event, bool) {
	terminals := map[string]bool{
		typeTurnCompleted: true, typeTurnFailed: true, typeTurnCancelled: true,
		typeTurnUncertain: true, typeDriverProcessed: true,
	}
	for _, e := range events {
		if e.InputID == inputID && terminals[e.Type] {
			return e, true
		}
	}
	return event{}, false
}

func indexOf(events []event, typ, inputID string) int {
	for i, e := range events {
		if e.Type == typ && e.InputID == inputID {
			return i
		}
	}
	return -1
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestManifestIdentityOnEveryEvent proves the provenance join key: when a
// supplied manifest gates the run, every emitted wire event carries its
// identity, and an unsupplied manifest leaves the field empty.
func TestManifestIdentityOnEveryEvent(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}

	var out bytes.Buffer
	opts := options(p, ws, lines(`{"input_id":"a","text":"1"}`), &out)
	opts.Driver = fake
	opts.Manifest = "sha256:deadbeef"
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events := decodeEvents(t, out.Bytes())
	if len(events) == 0 {
		t.Fatal("expected wire events")
	}
	for _, e := range events {
		if e.Manifest != "sha256:deadbeef" {
			t.Fatalf("event %s missing manifest identity: %+v", e.Type, e)
		}
		// The source fingerprint is unconditional on every event, so the stream
		// joins to its exact source configuration even without a manifest.
		if e.Fingerprint != p.Fingerprint {
			t.Fatalf("event %s missing source fingerprint: got %q want %q", e.Type, e.Fingerprint, p.Fingerprint)
		}
	}

	// With no manifest, the field is empty and omitted from the wire JSON.
	var out2 bytes.Buffer
	opts2 := options(p, ws, lines(`{"input_id":"b","text":"2"}`), &out2)
	opts2.Driver = &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	if _, err := Run(context.Background(), opts2); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out2.String(), "manifest") {
		t.Fatalf("an unsupplied manifest must not appear on the wire: %s", out2.String())
	}
	// The source fingerprint is still present with no manifest supplied.
	for _, e := range decodeEvents(t, out2.Bytes()) {
		if e.Fingerprint != p.Fingerprint {
			t.Fatalf("event %s missing source fingerprint without a manifest: %+v", e.Type, e)
		}
	}
}

// TestRunEndsWithACompletedEvent proves the stream's terminator is an event
// like every line before it — full envelope, next sequence — and that run's
// outcome answers only whether the dispatcher finished the work it was
// given. A run whose every turn failed is a run that finished: it ends ok,
// and what a loop scores is the turn counts, which is the one place the
// failure is visible.
func TestRunEndsWithACompletedEvent(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{}
	fake.Push(harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}})
	fake.Push(harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusFailed, Reason: "tool_error"}})
	var out bytes.Buffer
	opts := options(p, ws, lines(
		`{"input_id":"a","text":"1"}`,
		`{"input_id":"b","text":"2"}`,
	), &out)
	opts.Driver = fake
	events := runCollect(t, opts)

	last := events[len(events)-1]
	if last.Type != typeRunCompleted {
		t.Fatalf("the stream must end with %s, got %q: %+v", typeRunCompleted, last.Type, events)
	}
	// The terminator is a valid event: it carries the envelope every other
	// line carries, and continues the sequence rather than restarting it.
	if last.SchemaVersion != schemaVersion || last.Sequence != len(events) {
		t.Fatalf("the terminator must carry the envelope and the next sequence: %+v", last)
	}
	if last.Harness != "claude" || last.Conversation != "local" || last.Fingerprint != p.Fingerprint {
		t.Fatalf("the terminator must carry the wire envelope: %+v", last)
	}
	if last.Outcome != "ok" {
		t.Fatalf("a dispatch that completed every turn it was given ends ok, got %q", last.Outcome)
	}
	if last.Turns == nil || last.Turns.Failed < 1 || last.Turns.Completed != 1 {
		t.Fatalf("the counts must report the turns' own statuses: %+v", last.Turns)
	}
	// No other event carries an outcome: that field is what tells the
	// terminator apart from the lines before it.
	for _, e := range events[:len(events)-1] {
		if e.Outcome != "" || e.Turns != nil {
			t.Fatalf("only the terminator carries an outcome: %+v", e)
		}
	}
}

// TestRunSummaryContinuesTheSequence proves the summary a failing Run hands
// back lets the caller terminate the stream itself without restarting the
// numbering a consumer relies on being monotonic.
func TestRunSummaryContinuesTheSequence(t *testing.T) {
	p, ws := appliedWorkspace(t)
	fake := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	var out bytes.Buffer
	opts := options(p, ws, lines(`{"input_id":"a","text":"1"}`), &out)
	opts.Driver = fake
	summary, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	events := decodeEvents(t, out.Bytes())
	if summary.Sequence != len(events) {
		t.Fatalf("summary sequence = %d, want the last emitted %d", summary.Sequence, len(events))
	}
	if summary.Turns.Completed != 1 {
		t.Fatalf("summary turns = %+v, want one completed", summary.Turns)
	}

	// A caller writing its own terminator continues that numbering.
	var tail bytes.Buffer
	if err := WriteCompleted(&tail, Completion{
		Sequence:     summary.Sequence + 1,
		Harness:      "claude",
		Conversation: "local",
		Outcome:      "error",
		Turns:        summary.Turns,
		Error:        "the harness could not be verified",
	}); err != nil {
		t.Fatal(err)
	}
	written := decodeEvents(t, tail.Bytes())
	if len(written) != 1 || written[0].Sequence != len(events)+1 || written[0].Type != typeRunCompleted {
		t.Fatalf("a caller-written terminator must be one run.completed event at the next sequence: %+v", written)
	}
	if written[0].Outcome != "error" || written[0].Error == "" {
		t.Fatalf("the error terminator must carry its reason: %+v", written[0])
	}
}
