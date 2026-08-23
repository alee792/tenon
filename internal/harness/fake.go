package harness

import (
	"context"
	"errors"
	"sync"
)

// FakeDriver is a dependency-free stand-in harness for driving the dispatcher
// without a real model. It is a non-test file so both dispatch and cmd tests
// can use it. It scripts one turn plan per Open (consumed FIFO), records every
// OpenRequest it was opened with and every Input id it ran, and can make a turn
// stream events, choose its terminal result, fail as a process error, or block
// until aborted or the context is cancelled.
//
// The dispatcher opens exactly one session per turn, so each queued FakeTurn
// governs one turn. Once the scripted turns are exhausted, Default governs any
// further turns.
type FakeDriver struct {
	// NameValue overrides the reported harness name; empty means "fake".
	NameValue string
	// VerifyErr, when set, is returned by Verify.
	VerifyErr error
	// OpenErr, when set, fails every Open with this error.
	OpenErr error
	// Default governs any turn opened after the scripted turns are exhausted.
	Default FakeTurn

	mu     sync.Mutex
	turns  []FakeTurn
	opens  []OpenRequest
	inputs []string
	closes int
	aborts int
}

// FakeTurn scripts one turn's behavior.
type FakeTurn struct {
	// Events are emitted, in order, at the start of RunTurn.
	Events []Event
	// Result is returned when the turn completes normally (Err nil).
	Result TurnResult
	// Err, when set, makes RunTurn return a process failure after emitting
	// Events (unless the turn blocks and is aborted first).
	Err error
	// Release, when non-nil, makes RunTurn block after emitting Events until
	// Release is closed, the session is aborted, or the context is cancelled.
	// It is the seam for asserting that input is accepted while a turn is
	// active.
	Release chan struct{}
	// Block, when true (and Release nil), makes RunTurn block after emitting
	// Events until the session is aborted or the context is cancelled. It is
	// the seam for abort and deadline tests.
	Block bool
	// CloseErr, when set, is returned by Session.Close.
	CloseErr error
}

// Push appends scripted turns, one consumed per Open in order.
func (d *FakeDriver) Push(turns ...FakeTurn) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.turns = append(d.turns, turns...)
}

// Opens returns a copy of every OpenRequest the driver was opened with, in
// order.
func (d *FakeDriver) Opens() []OpenRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]OpenRequest(nil), d.opens...)
}

// Inputs returns a copy of every Input id RunTurn ran, in order.
func (d *FakeDriver) Inputs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.inputs...)
}

// Aborts reports how many times a session was aborted.
func (d *FakeDriver) Aborts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.aborts
}

// Name reports the harness name.
func (d *FakeDriver) Name() string {
	if d.NameValue != "" {
		return d.NameValue
	}
	return "fake"
}

// Verify reports the configured VerifyErr.
func (d *FakeDriver) Verify(ctx context.Context) error { return d.VerifyErr }

// Open records the request, pops the next scripted turn, and returns a session
// for it, or fails with OpenErr.
func (d *FakeDriver) Open(ctx context.Context, req OpenRequest) (Session, error) {
	d.mu.Lock()
	d.opens = append(d.opens, req)
	turn := d.Default
	if len(d.turns) > 0 {
		turn = d.turns[0]
		d.turns = d.turns[1:]
	}
	d.mu.Unlock()
	if d.OpenErr != nil {
		return nil, d.OpenErr
	}
	return &FakeSession{driver: d, turn: turn, abort: make(chan struct{})}, nil
}

// FakeSession is one scripted turn's session.
type FakeSession struct {
	driver *FakeDriver
	turn   FakeTurn
	abort  chan struct{}
	once   sync.Once
}

// RunTurn records the input id, streams the scripted events, optionally blocks,
// then returns the scripted result or process error.
func (s *FakeSession) RunTurn(ctx context.Context, in Input, emit func(Event)) (TurnResult, error) {
	s.driver.mu.Lock()
	s.driver.inputs = append(s.driver.inputs, in.ID)
	s.driver.mu.Unlock()

	for _, ev := range s.turn.Events {
		emit(ev)
	}
	switch {
	case s.turn.Release != nil:
		select {
		case <-s.turn.Release:
		case <-s.abort:
			return TurnResult{}, errors.New("turn aborted")
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		}
	case s.turn.Block:
		select {
		case <-s.abort:
			return TurnResult{}, errors.New("turn aborted")
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		}
	}
	if s.turn.Err != nil {
		return TurnResult{}, s.turn.Err
	}
	return s.turn.Result, nil
}

// Close records the close and returns the scripted CloseErr.
func (s *FakeSession) Close() error {
	s.driver.mu.Lock()
	s.driver.closes++
	s.driver.mu.Unlock()
	return s.turn.CloseErr
}

// Abort interrupts a blocking RunTurn. It is idempotent and goroutine-safe.
func (s *FakeSession) Abort() {
	s.once.Do(func() {
		s.driver.mu.Lock()
		s.driver.aborts++
		s.driver.mu.Unlock()
		close(s.abort)
	})
}
