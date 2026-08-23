package schedule

import (
	"context"
	"sync"
	"time"
)

// Clock is the foreground runner's view of time. The UTC-only evaluation and
// the wait between wakes both go through it, so tests drive the clock
// deterministically instead of sleeping real minutes.
type Clock interface {
	// Now returns the current time; callers convert to UTC.
	Now() time.Time
	// SleepUntil blocks until the clock reaches t, ctx is done, or the clock
	// is otherwise released. It returns ctx.Err() when ctx ends first and nil
	// when t is reached.
	SleepUntil(ctx context.Context, t time.Time) error
}

// systemClock is the production clock backed by the wall clock.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) SleepUntil(ctx context.Context, t time.Time) error {
	d := time.Until(t)
	if d <= 0 {
		// Honor cancellation even on the already-elapsed fast path, so a
		// tight sequence of due wakes can never outrun a stop signal.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ManualClock is a test clock whose time only advances when a test calls
// Advance or Set. It is exported so tests in other packages could drive a
// runner, and it is safe for concurrent use.
type ManualClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*manualWaiter
}

type manualWaiter struct {
	until time.Time
	ch    chan struct{}
}

// NewManualClock returns a clock started at start.
func NewManualClock(start time.Time) *ManualClock {
	return &ManualClock{now: start}
}

// Now returns the clock's current time.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// SleepUntil blocks until the clock is advanced to at least t or ctx is done.
func (c *ManualClock) SleepUntil(ctx context.Context, t time.Time) error {
	c.mu.Lock()
	if !c.now.Before(t) {
		c.mu.Unlock()
		return nil
	}
	w := &manualWaiter{until: t, ch: make(chan struct{})}
	c.waiters = append(c.waiters, w)
	c.mu.Unlock()
	select {
	case <-w.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Advance moves the clock forward by d, waking every waiter the new time has
// reached.
func (c *ManualClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

// Set moves the clock to t (which should not move backward for a live runner,
// though tests may to exercise backward-clock handling) and wakes reached
// waiters.
func (c *ManualClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	var remain []*manualWaiter
	for _, w := range c.waiters {
		if !c.now.Before(w.until) {
			close(w.ch)
		} else {
			remain = append(remain, w)
		}
	}
	c.waiters = remain
}
