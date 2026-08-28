package cron

import (
	"testing"
	"time"
)

func TestValidateAcceptsStandardFiveField(t *testing.T) {
	valid := []string{
		"* * * * *",
		"*/5 * * * *",
		"0 0 * * *",
		"0 9-17 * * 1-5",
		"0,30 * * * *",
		"15 0 1,15 * *",
		"0 22 * * 1-5",
	}
	for _, expr := range valid {
		if err := Validate(expr); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", expr, err)
		}
	}
}

func TestValidateRejectsNonFiveFieldAndGarbage(t *testing.T) {
	invalid := []string{
		"",                // empty
		"* * * * * *",     // six-field (seconds granularity)
		"@daily",          // descriptor, not five-field
		"@every 5m",       // descriptor
		"garbage",         // not a cron at all
		"60 * * * *",      // minute out of range
		"* * * *",         // four-field
		"* * * * 8",       // day-of-week out of range
		"* * * * *\n\x00", // non-printable
	}
	for _, expr := range invalid {
		if err := Validate(expr); err == nil {
			t.Errorf("Validate(%q) = nil, want error", expr)
		}
	}
}

func TestNextAfterIsStrictlyAfterInUTC(t *testing.T) {
	base := time.Date(2026, 8, 22, 10, 2, 30, 0, time.UTC)
	next, err := NextAfter("*/5 * * * *", base)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 22, 10, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want %v", next, want)
	}
	if !next.After(base) {
		t.Fatalf("next %v is not strictly after %v", next, base)
	}

	// An instant exactly on an activation advances to the following one.
	onTick := time.Date(2026, 8, 22, 10, 5, 0, 0, time.UTC)
	after, err := NextAfter("*/5 * * * *", onTick)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Equal(time.Date(2026, 8, 22, 10, 10, 0, 0, time.UTC)) {
		t.Fatalf("after = %v, want 10:10", after)
	}
}

func TestDueMatchesActivationMinute(t *testing.T) {
	s, err := Parse("*/5 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Due(time.Date(2026, 8, 22, 10, 5, 0, 0, time.UTC)) {
		t.Fatal("expected due at 10:05")
	}
	if s.Due(time.Date(2026, 8, 22, 10, 6, 0, 0, time.UTC)) {
		t.Fatal("did not expect due at 10:06")
	}
}
