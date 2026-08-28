package harness

import (
	"context"
	"errors"
	"testing"
)

// TestFakeScriptsTurn proves a scripted turn streams its events, records the
// input and open request, and returns its chosen result.
func TestFakeScriptsTurn(t *testing.T) {
	d := &FakeDriver{}
	d.Push(FakeTurn{
		Events: []Event{{Type: EventAgentOutputDelta, Delta: "hi"}},
		Result: TurnResult{SessionID: "s1", Status: StatusCompleted},
	})
	sess, err := d.Open(context.Background(), OpenRequest{ResumeID: "prev"})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	result, err := sess.RunTurn(context.Background(), Input{ID: "in-1", Text: "go"}, func(e Event) {
		deltas = append(deltas, e.Delta)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "s1" || result.Status != StatusCompleted {
		t.Fatalf("result = %+v", result)
	}
	if len(deltas) != 1 || deltas[0] != "hi" {
		t.Fatalf("deltas = %v", deltas)
	}
	if got := d.Opens(); len(got) != 1 || got[0].ResumeID != "prev" {
		t.Fatalf("opens = %+v", got)
	}
	if got := d.Inputs(); len(got) != 1 || got[0] != "in-1" {
		t.Fatalf("inputs = %v", got)
	}
}

// TestFakeProcessError proves a turn can fail as a process error.
func TestFakeProcessError(t *testing.T) {
	d := &FakeDriver{}
	d.Push(FakeTurn{Err: errors.New("boom")})
	sess, _ := d.Open(context.Background(), OpenRequest{})
	if _, err := sess.RunTurn(context.Background(), Input{ID: "x"}, func(Event) {}); err == nil {
		t.Fatal("want process error")
	}
}

// TestFakeBlocksUntilAbort proves a blocking turn returns once aborted.
func TestFakeBlocksUntilAbort(t *testing.T) {
	d := &FakeDriver{}
	d.Push(FakeTurn{Block: true})
	sess, _ := d.Open(context.Background(), OpenRequest{})
	done := make(chan struct{})
	go func() {
		_, _ = sess.RunTurn(context.Background(), Input{ID: "x"}, func(Event) {})
		close(done)
	}()
	sess.Abort()
	<-done
	if d.Aborts() != 1 {
		t.Fatalf("aborts = %d", d.Aborts())
	}
}

// TestFakeBlocksUntilContext proves a blocking turn returns on context cancel.
func TestFakeBlocksUntilContext(t *testing.T) {
	d := &FakeDriver{}
	d.Push(FakeTurn{Block: true})
	sess, _ := d.Open(context.Background(), OpenRequest{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := sess.RunTurn(ctx, Input{ID: "x"}, func(Event) {})
		done <- err
	}()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("want context error")
	}
}

// TestFakeVerifyAndName proves Verify and Name are configurable.
func TestFakeVerifyAndName(t *testing.T) {
	d := &FakeDriver{NameValue: "claude", VerifyErr: errors.New("no exe")}
	if d.Name() != "claude" {
		t.Fatalf("name = %q", d.Name())
	}
	if d.Verify(context.Background()) == nil {
		t.Fatal("want verify error")
	}
}
