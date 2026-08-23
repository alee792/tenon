// Package schedule runs an agent's already-applied schedules from a foreground
// UTC clock and dispatches individual occurrences (ADR 0008, ADR 0011). It owns
// occurrence and conversation identity, the exclusive local lock that stops a
// second clock, and the clock loop's current-minute admission; the fresh
// session, deduplication, turn deadline, and terminal classification come from
// internal/dispatch. It installs no daemon and never backfills missed work.
//
// Concurrency: distinct schedules run concurrently up to a bounded worker pool,
// sharing one mutex-guarded dispatchstate store so the single-owner dispatch
// file stays consistent while turns run at once (the store serializes its short
// state critical sections; the slow turns run outside them, and distinct
// schedules touch distinct conversations). No schedule ever overlaps itself: an
// in-flight occurrence makes a later due minute for that schedule a skip.
package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/cron"
	"github.com/alee792/tenon/internal/dispatch"
	"github.com/alee792/tenon/internal/dispatchstate"
	"github.com/alee792/tenon/internal/harness"
)

// Concurrency bounds for the foreground clock's worker pool.
const (
	// DefaultMaxActive is the default number of occurrences that may run at
	// once across distinct schedules.
	DefaultMaxActive = 4
	// MinMaxActive and MaxMaxActive bound an operator-selected capacity.
	MinMaxActive = 1
	MaxMaxActive = 64
)

// errClockHeld is the sentinel a contended lock returns; callers render the
// operator-facing message.
var errClockHeld = errors.New("another schedule clock or trigger is already running for this agent, workspace, and harness")

// ErrClockHeld reports whether err is the contended-lock condition, so a caller
// can render the exact operator message with a nonzero exit.
func ErrClockHeld(err error) bool { return errors.Is(err, errClockHeld) }

// OccurrenceID derives one occurrence's stable dispatch id from the exact
// schedule name and its scheduled UTC minute (ADR 0011), so the same due minute
// deduplicates through the ordinary dispatch contract.
func OccurrenceID(name string, minute time.Time) string {
	m := minute.UTC().Truncate(time.Minute).Format("2006-01-02T15:04Z")
	sum := sha256.Sum256([]byte(name + "\x00" + m))
	return "occ-" + hex.EncodeToString(sum[:])
}

// ConversationID derives one schedule's stable dispatch conversation from its
// name, so its deduplication memory stays bounded per schedule.
func ConversationID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "schedule-" + hex.EncodeToString(sum[:12])
}

// Options configures one foreground clock run.
type Options struct {
	// Project is the loaded, validated agent project whose schedules run.
	Project *agentproject.Project
	// Driver drives one native harness.
	Driver harness.Driver
	// Workspace is the applied workspace.
	Workspace string
	// Harness is the applied harness name ("claude" or "codex").
	Harness string
	// TurnTimeout bounds one occurrence's turn; 0 disables the per-turn
	// deadline.
	TurnTimeout time.Duration
	// MaxActive bounds concurrent occurrences across distinct schedules; a
	// value outside [MinMaxActive, MaxMaxActive] is normalized to the default.
	MaxActive int
	// Out receives bounded lifecycle lines, never model text.
	Out io.Writer
	// Clock is the time source; nil uses the wall clock.
	Clock Clock
}

// lockPath returns the exclusive-ownership lock path for a workspace, agent,
// and harness, keyed by the symlink-resolved workspace so two spellings of one
// workspace collide.
func lockPath(workspace, agent, harness string) (string, error) {
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(ws); err == nil {
		ws = resolved
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolving the user cache directory: %w", err)
	}
	sum := sha256.Sum256([]byte(ws + "\x00" + agent + "\x00" + harness))
	return filepath.Join(cache, "tenon", "locks", "schedule-"+hex.EncodeToString(sum[:])+".lock"), nil
}

// Lock takes exclusive local ownership of the dispatch state for one
// workspace, agent, and harness. The clock and every trigger for the same
// setup acquire it so their writes to the single owner-only dispatch file
// serialize across processes rather than corrupting it under last-writer-wins
// (the in-process store mutex alone cannot coordinate separate processes). A
// contended lock returns an error for which ErrClockHeld reports true; the
// returned function releases ownership.
func Lock(workspace, agent, harness string) (func(), error) {
	lp, err := lockPath(workspace, agent, harness)
	if err != nil {
		return nil, err
	}
	return acquireLock(lp)
}

// runner is one Run's state.
type runner struct {
	opts        Options
	clock       Clock
	store       *dispatchstate.Store
	schedules   []agentproject.Schedule
	compiled    map[string]cron.Schedule
	turnTimeout time.Duration

	mu         sync.Mutex
	lastMinute map[string]time.Time
	inflight   map[string]bool

	sem chan struct{}
	wg  sync.WaitGroup

	outMu sync.Mutex
	out   io.Writer

	stopAdmit  atomic.Bool
	stopReason atomic.Value // string
}

// Run acquires exclusive local ownership, verifies the applied setup, classifies
// any interrupted occurrence uncertain, then runs the foreground UTC clock until
// a stop signal, a durable-state failure, or an output failure ends admission,
// draining in-flight occurrences before releasing ownership. Its error is
// reserved for setup failures (a held lock, stale setup, an unverifiable
// harness, or unreadable state) reported before the started line; once started
// it returns nil and reports terminal conditions on Out.
func Run(ctx context.Context, opts Options) error {
	if opts.Project == nil {
		return errors.New("schedule: a loaded agent project is required")
	}
	if opts.Driver == nil {
		return errors.New("schedule: a harness driver is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = systemClock{}
	}
	maxActive := opts.MaxActive
	if maxActive < MinMaxActive || maxActive > MaxMaxActive {
		maxActive = DefaultMaxActive
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	release, err := Lock(opts.Workspace, opts.Project.Name, opts.Harness)
	if err != nil {
		return err
	}
	defer release()

	// Current setup only: no auto-apply, no hot reload.
	if err := apply.Verify(opts.Project, opts.Workspace, opts.Harness); err != nil {
		return fmt.Errorf("schedule: %w", err)
	}
	if err := opts.Driver.Verify(ctx); err != nil {
		return fmt.Errorf("schedule: the %s harness could not be verified: %w", opts.Harness, err)
	}

	compiled := make(map[string]cron.Schedule, len(opts.Project.Schedules))
	for _, s := range opts.Project.Schedules {
		sched, err := cron.Parse(s.Cron)
		if err != nil {
			return fmt.Errorf("schedule: %s: %w", s.SourcePath, err)
		}
		compiled[s.Name] = sched
	}

	store, err := dispatchstate.Open(opts.Workspace)
	if err != nil {
		return fmt.Errorf("schedule: %w", err)
	}

	r := &runner{
		opts:        opts,
		clock:       clock,
		store:       store,
		schedules:   opts.Project.Schedules,
		compiled:    compiled,
		turnTimeout: opts.TurnTimeout,
		lastMinute:  map[string]time.Time{},
		inflight:    map[string]bool{},
		sem:         make(chan struct{}, maxActive),
		out:         out,
	}

	if err := r.recover(); err != nil {
		return err
	}
	if err := r.emitRuntime("started", ""); err != nil {
		return err
	}

	r.loop(ctx)

	_ = r.emitRuntime("stopping", r.reason())
	r.wg.Wait()
	return r.emitRuntime("stopped", "")
}

// recover classifies every interrupted (queued or active) occurrence in each
// schedule's conversation as uncertain and reports it, before the started line
// and before any new occurrence. Such work is never executed.
func (r *runner) recover() error {
	for _, s := range r.schedules {
		recovered, err := r.store.RecoverTaskUncertain(r.refFor(s.Name))
		if err != nil {
			return fmt.Errorf("schedule: %w", err)
		}
		for _, rc := range recovered {
			if err := r.emitOccurrence(s.Name, rc.InputID, string(rc.Status), rc.Reason); err != nil {
				return err
			}
		}
	}
	return nil
}

// loop waits for each wake and evaluates admission until admission stops. With
// no schedules it simply waits for the context to end.
func (r *runner) loop(ctx context.Context) {
	if len(r.schedules) == 0 {
		<-ctx.Done()
		return
	}
	for !r.stopAdmit.Load() {
		now := r.clock.Now().UTC()
		wake, ok := r.nextWake(now)
		if !ok {
			<-ctx.Done()
			return
		}
		if err := r.clock.SleepUntil(ctx, wake); err != nil {
			return // context ended: stop admission
		}
		r.evaluate(r.clock.Now())
	}
}

// nextWake returns the earliest activation strictly after now across all
// schedules, the instant the loop sleeps until.
func (r *runner) nextWake(now time.Time) (time.Time, bool) {
	var wake time.Time
	found := false
	for _, s := range r.schedules {
		next := r.compiled[s.Name].Next(now)
		// A syntactically valid expression can still never fire (e.g. Feb 31);
		// robfig returns the zero time. Skip it so one impossible schedule
		// cannot poison the wake for every other schedule (a zero time sorts
		// before every real activation) and spin the loop.
		if next.IsZero() {
			continue
		}
		if !found || next.Before(wake) {
			wake, found = next, true
		}
	}
	return wake, found
}

// evaluate admits, for the current UTC minute only, each schedule due this
// minute that has not already been admitted this minute or later and is not in
// flight. A schedule due this minute but already in flight is skipped as an
// overlap. The process-local watermark blocks a repeated same-minute admission
// and any backward-clock admission; an older stored candidate is never
// admitted because dueness is tested against the current minute itself.
func (r *runner) evaluate(now time.Time) {
	curMin := now.UTC().Truncate(time.Minute)
	for _, s := range r.schedules {
		// Stop admitting immediately once a signal or a durable-state failure
		// has ended admission, rather than finishing the current minute's
		// remaining schedules.
		if r.stopAdmit.Load() {
			return
		}
		r.mu.Lock()
		if !curMin.After(r.lastMinute[s.Name]) {
			r.mu.Unlock()
			continue
		}
		if !r.compiled[s.Name].Due(curMin) {
			r.mu.Unlock()
			continue
		}
		r.lastMinute[s.Name] = curMin
		occ := OccurrenceID(s.Name, curMin)
		if r.inflight[s.Name] {
			r.mu.Unlock()
			if err := r.emitOccurrence(s.Name, occ, "skipped", "overlap"); err != nil {
				r.fail(err)
			}
			continue
		}
		r.inflight[s.Name] = true
		r.mu.Unlock()
		r.dispatch(s, occ)
	}
}

// dispatch launches one occurrence in the bounded worker pool. Admission does
// not block on a full pool: the occurrence counts as in flight the moment it is
// admitted and its turn begins once a slot frees.
func (r *runner) dispatch(s agentproject.Schedule, occ string) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.sem <- struct{}{}
		defer func() { <-r.sem }()

		if err := r.emitOccurrence(s.Name, occ, "started", ""); err != nil {
			r.clearInflight(s.Name)
			r.fail(err)
			return
		}
		// Occurrences run under a context independent of any stop signal so an
		// admitted occurrence drains rather than being cancelled; its turn
		// deadline still bounds it.
		outcome, err := dispatch.RunTaskWithStore(context.Background(), r.store, r.taskOptions(s), occ, s.Prompt)
		r.clearInflight(s.Name)
		if err != nil {
			// A durable-state failure stops admission; the occurrence is still
			// reported with a terminal, model-text-free line.
			_ = r.emitOccurrence(s.Name, occ, string(dispatchstate.Uncertain), boundReason(err.Error()))
			r.fail(err)
			return
		}
		if err := r.emitOccurrence(s.Name, occ, string(outcome.Status), outcome.Reason); err != nil {
			r.fail(err)
		}
	}()
}

// taskOptions builds the dispatch options for one schedule's occurrence.
func (r *runner) taskOptions(s agentproject.Schedule) dispatch.Options {
	return dispatch.Options{
		Project:      r.opts.Project,
		Driver:       r.opts.Driver,
		Workspace:    r.opts.Workspace,
		Harness:      r.opts.Harness,
		Conversation: ConversationID(s.Name),
		Mode:         dispatch.Task,
		TurnTimeout:  r.turnTimeout,
	}
}

// refFor builds the dispatch state ref for one schedule's conversation.
func (r *runner) refFor(name string) dispatchstate.Ref {
	return dispatchstate.Ref{
		Agent:        r.opts.Project.Name,
		Fingerprint:  r.opts.Project.Fingerprint,
		Harness:      r.opts.Harness,
		Conversation: ConversationID(name),
	}
}

func (r *runner) clearInflight(name string) {
	r.mu.Lock()
	r.inflight[name] = false
	r.mu.Unlock()
}

// fail stops admission and records the first stop reason. One schedule's
// terminal (model-reported) failure never calls this: only a durable-state or
// output failure does, and it does not stop already-admitted occurrences.
func (r *runner) fail(err error) {
	if r.stopAdmit.CompareAndSwap(false, true) {
		r.stopReason.Store(boundReason(err.Error()))
	}
}

// reason returns the recorded stop reason, or "" when admission ended on a
// stop signal.
func (r *runner) reason() string {
	if v, ok := r.stopReason.Load().(string); ok {
		return v
	}
	return ""
}

// emitRuntime writes one bounded schedule_runtime lifecycle line.
func (r *runner) emitRuntime(status, reason string) error {
	line := "schedule_runtime status=" + status
	if reason != "" {
		line += fmt.Sprintf(" reason=%q", reason)
	}
	return r.writeLine(line)
}

// emitOccurrence writes one bounded per-occurrence lifecycle line. It never
// carries model text.
func (r *runner) emitOccurrence(name, occ, status, reason string) error {
	line := fmt.Sprintf("schedule=%q occurrence=%s status=%s", name, occ, status)
	if reason != "" {
		line += fmt.Sprintf(" reason=%q", reason)
	}
	return r.writeLine(line)
}

func (r *runner) writeLine(line string) error {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	if _, err := io.WriteString(r.out, line+"\n"); err != nil {
		return fmt.Errorf("schedule: writing lifecycle output: %w", err)
	}
	return nil
}

// boundReason flattens and bounds an error to a single-line, model-text-free
// reason within the dispatch store's reason bound.
func boundReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > dispatchstate.MaxReasonBytes {
		s = s[:dispatchstate.MaxReasonBytes]
	}
	if s == "" {
		return "failed"
	}
	return s
}
