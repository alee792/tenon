package schedule

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/cron"
	"github.com/alee792/tenon/internal/dispatchstate"
	"github.com/alee792/tenon/internal/harness"
)

const testInstructions = "---\ndescription: A schedule test agent.\n---\n\nYou do scheduled work.\n"

// appliedScheduleWorkspace builds an agent with the given schedules
// (name->cron) and applies it into a fresh workspace with the real claude
// driver so apply.Verify passes exactly as for a genuinely applied setup.
func appliedScheduleWorkspace(t *testing.T, crons map[string]string) (*agentproject.Project, string) {
	t.Helper()
	agentDir := filepath.Join(t.TempDir(), "sched-agent")
	if err := os.Mkdir(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "instructions.md"), []byte(testInstructions), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, expr := range crons {
		full := filepath.Join(agentDir, "schedules", filepath.FromSlash(name)+".md")
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\ncron: \"" + expr + "\"\n---\n\nDo " + name + ".\n"
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
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

// newTestRunner builds a runner directly for admission-logic tests, bypassing
// the lock/verify orchestration that full-Run tests exercise separately.
func newTestRunner(t *testing.T, p *agentproject.Project, ws string, driver harness.Driver, out io.Writer, turnTimeout time.Duration) *runner {
	t.Helper()
	store, err := dispatchstate.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	compiled := make(map[string]cron.Schedule, len(p.Schedules))
	for _, s := range p.Schedules {
		sched, err := cron.Parse(s.Cron)
		if err != nil {
			t.Fatal(err)
		}
		compiled[s.Name] = sched
	}
	return &runner{
		opts:        Options{Project: p, Driver: driver, Workspace: ws, Harness: "claude", TurnTimeout: turnTimeout},
		clock:       systemClock{},
		store:       store,
		schedules:   p.Schedules,
		compiled:    compiled,
		turnTimeout: turnTimeout,
		lastMinute:  map[string]time.Time{},
		inflight:    map[string]bool{},
		sem:         make(chan struct{}, DefaultMaxActive),
		out:         out,
	}
}

// syncBuffer is a concurrency-safe writer whose contents a test can poll.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) waitFor(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), substr) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; output:\n%s", substr, s.String())
}

func minute(h, m int) time.Time {
	return time.Date(2026, 8, 22, h, m, 0, 0, time.UTC)
}

func TestOccurrenceAndConversationIdentity(t *testing.T) {
	m := minute(10, 5)
	id := OccurrenceID("daily/digest", m)
	if got := OccurrenceID("daily/digest", m.Add(30*time.Second)); got != id {
		t.Fatalf("same minute must yield the same occurrence id: %q vs %q", id, got)
	}
	if OccurrenceID("daily/digest", m.Add(time.Minute)) == id {
		t.Fatal("a different minute must yield a different occurrence id")
	}
	if OccurrenceID("other", m) == id {
		t.Fatal("a different name must yield a different occurrence id")
	}
	if !strings.HasPrefix(id, "occ-") {
		t.Fatalf("occurrence id = %q", id)
	}
	conv := ConversationID("daily/digest")
	if !strings.HasPrefix(conv, "schedule-") {
		t.Fatalf("conversation id = %q", conv)
	}
	if ConversationID("other") == conv {
		t.Fatal("distinct schedules must have distinct conversations")
	}
}

func TestEvaluateAdmitsCurrentMinuteOnly(t *testing.T) {
	p, ws := appliedScheduleWorkspace(t, map[string]string{"five": "*/5 * * * *"})
	driver := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	out := &syncBuffer{}
	r := newTestRunner(t, p, ws, driver, out, 0)

	r.evaluate(minute(10, 5)) // due
	r.wg.Wait()
	if n := len(driver.Opens()); n != 1 {
		t.Fatalf("expected 1 admission at a due minute, got %d", n)
	}
	out.waitFor(t, "status=completed")

	r.evaluate(minute(10, 6)) // not a multiple of five: not due
	r.wg.Wait()
	if n := len(driver.Opens()); n != 1 {
		t.Fatalf("a non-due minute must not admit: opens=%d", n)
	}
}

func TestEvaluateWatermarkBlocksRepeatAndBackward(t *testing.T) {
	p, ws := appliedScheduleWorkspace(t, map[string]string{"every": "* * * * *"})
	driver := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	out := &syncBuffer{}
	r := newTestRunner(t, p, ws, driver, out, 0)

	r.evaluate(minute(10, 5))
	r.wg.Wait()
	if n := len(driver.Opens()); n != 1 {
		t.Fatalf("first minute must admit: opens=%d", n)
	}
	r.evaluate(minute(10, 5)) // same minute again
	r.evaluate(minute(10, 4)) // backward step
	r.wg.Wait()
	if n := len(driver.Opens()); n != 1 {
		t.Fatalf("a repeated same minute and a backward step must not admit: opens=%d", n)
	}
	r.evaluate(minute(10, 6)) // strictly later minute
	r.wg.Wait()
	if n := len(driver.Opens()); n != 2 {
		t.Fatalf("a strictly later minute must admit: opens=%d", n)
	}
}

func TestEvaluateNoOverlapPerScheduleSkips(t *testing.T) {
	p, ws := appliedScheduleWorkspace(t, map[string]string{"every": "* * * * *"})
	release := make(chan struct{})
	driver := &harness.FakeDriver{}
	driver.Push(harness.FakeTurn{Release: release, Result: harness.TurnResult{Status: harness.StatusCompleted}})
	out := &syncBuffer{}
	r := newTestRunner(t, p, ws, driver, out, 0)

	r.evaluate(minute(10, 5)) // admit; the occurrence blocks on release
	out.waitFor(t, "status=started")

	r.evaluate(minute(10, 6)) // same schedule due again while in flight
	out.waitFor(t, "status=skipped reason=\"overlap\"")

	close(release)
	r.wg.Wait()
	if n := len(driver.Opens()); n != 1 {
		t.Fatalf("an overlapping occurrence must not open a harness: opens=%d", n)
	}
}

func TestRunStartupRecoversInterruptedUncertain(t *testing.T) {
	p, ws := appliedScheduleWorkspace(t, map[string]string{"every": "* * * * *"})

	// Seed an interrupted (active) occurrence in the schedule's conversation.
	store, err := dispatchstate.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	ref := dispatchstate.Ref{Agent: p.Name, Fingerprint: p.Fingerprint, Harness: "claude", Conversation: ConversationID("every")}
	if _, err := store.Accept(ref, "occ-interrupted", "do work"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.StartNext(ref); err != nil {
		t.Fatal(err)
	}

	driver := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	out := &syncBuffer{}
	clock := NewManualClock(minute(10, 4).Add(30 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Project: p, Driver: driver, Workspace: ws, Harness: "claude", Out: out, Clock: clock})
	}()

	out.waitFor(t, "occurrence=occ-interrupted status=uncertain")
	out.waitFor(t, "schedule_runtime status=started")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if !strings.Contains(out.String(), "dispatcher_restarted") {
		t.Fatalf("recovered occurrence must carry the restart reason:\n%s", out.String())
	}
	if len(driver.Opens()) != 0 {
		t.Fatal("a recovered occurrence must never execute")
	}
}

func TestRunGracefulDrainCompletesInFlight(t *testing.T) {
	p, ws := appliedScheduleWorkspace(t, map[string]string{"every": "* * * * *"})
	release := make(chan struct{})
	driver := &harness.FakeDriver{}
	driver.Push(harness.FakeTurn{Release: release, Result: harness.TurnResult{Status: harness.StatusCompleted}})
	out := &syncBuffer{}
	clock := NewManualClock(minute(10, 4).Add(30 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Project: p, Driver: driver, Workspace: ws, Harness: "claude", TurnTimeout: 5 * time.Second, Out: out, Clock: clock})
	}()

	out.waitFor(t, "schedule_runtime status=started")
	clock.Set(minute(10, 5))      // wake at the due minute
	out.waitFor(t, "occurrence=") // the occurrence started and is blocking
	out.waitFor(t, "status=started")

	cancel()                                           // stop admission
	out.waitFor(t, "schedule_runtime status=stopping") // admission stopped first
	close(release)                                     // let the in-flight occurrence finish
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}

	s := out.String()
	compIdx := strings.Index(s, "status=completed")
	stopIdx := strings.Index(s, "schedule_runtime status=stopped")
	if compIdx < 0 || stopIdx < 0 {
		t.Fatalf("expected both a completed occurrence and a stopped runtime:\n%s", s)
	}
	if compIdx > stopIdx {
		t.Fatalf("an in-flight occurrence must drain before the runtime stops:\n%s", s)
	}
}

func TestRunSecondClockIsExcluded(t *testing.T) {
	p, ws := appliedScheduleWorkspace(t, map[string]string{"every": "* * * * *"})
	driver := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	out := &syncBuffer{}
	clock := NewManualClock(minute(10, 4).Add(30 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Project: p, Driver: driver, Workspace: ws, Harness: "claude", Out: out, Clock: clock})
	}()
	out.waitFor(t, "schedule_runtime status=started") // first clock holds the lock

	err := Run(context.Background(), Options{Project: p, Driver: driver, Workspace: ws, Harness: "claude", Out: io.Discard, Clock: NewManualClock(minute(10, 4))})
	if !ErrClockHeld(err) {
		t.Fatalf("a second clock must be excluded, got %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("first Run returned %v", err)
	}
}

// TestNextWakeSkipsNeverFiringSchedule guards the fix for a valid-but-
// impossible cron (Feb 31): robfig returns the zero time, which must not
// poison the wake for other schedules nor spin the loop. With only impossible
// schedules, nextWake reports no wake so the loop blocks on ctx.
func TestNextWakeSkipsNeverFiringSchedule(t *testing.T) {
	never, err := cron.Parse("0 0 31 2 *") // February 31 never occurs
	if err != nil {
		t.Fatal(err)
	}
	if !never.Next(minute(10, 0)).IsZero() {
		t.Fatal("precondition: an impossible schedule must return the zero time")
	}
	every, err := cron.Parse("*/5 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := minute(10, 1)

	mixed := &runner{
		schedules: []agentproject.Schedule{{Name: "never"}, {Name: "every"}},
		compiled:  map[string]cron.Schedule{"never": never, "every": every},
	}
	wake, ok := mixed.nextWake(now)
	if !ok || wake.IsZero() || !wake.After(now) {
		t.Fatalf("a firing schedule must yield a real future wake, got ok=%v wake=%v", ok, wake)
	}

	only := &runner{
		schedules: []agentproject.Schedule{{Name: "never"}},
		compiled:  map[string]cron.Schedule{"never": never},
	}
	if _, ok := only.nextWake(now); ok {
		t.Fatal("an all-impossible set must report no wake so the loop blocks on ctx")
	}
}

// TestVerifyOccurrenceDriftFailsClosedWithoutOpening proves a supplied manifest
// re-verified per occurrence: on drift the occurrence opens no harness, is
// reported uncertain, and admission ends.
func TestVerifyOccurrenceDriftFailsClosedWithoutOpening(t *testing.T) {
	p, ws := appliedScheduleWorkspace(t, map[string]string{"every": "* * * * *"})
	driver := &harness.FakeDriver{Default: harness.FakeTurn{Result: harness.TurnResult{Status: harness.StatusCompleted}}}
	out := &syncBuffer{}
	r := newTestRunner(t, p, ws, driver, out, time.Second)
	r.opts.VerifyOccurrence = func() error { return errors.New("manifest drift") }

	r.evaluate(minute(10, 5))
	r.wg.Wait()

	if n := len(driver.Opens()); n != 0 {
		t.Fatalf("occurrence manifest drift must open no harness, got %d opens", n)
	}
	if got := out.String(); !strings.Contains(got, "status=uncertain") {
		t.Fatalf("drift must report the occurrence uncertain: %s", got)
	}
	if !r.stopAdmit.Load() {
		t.Fatal("occurrence drift must end admission")
	}
}
