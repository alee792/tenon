// Package cron is the sole wrapper around the standard five-field cron
// evaluation used for schedule admission (ADR 0008, ADR 0011). Nothing else in
// tenon imports the underlying cron engine: agentproject validates authored
// `cron` values through Validate, and the foreground clock evaluates them
// through Parse/Next/Due, so swapping the engine behind this wrapper is a local
// change. Evaluation is UTC only by the caller's convention — Parse-produced
// schedules preserve the location of the time they are given.
package cron

import (
	"errors"
	"fmt"
	"strings"
	"time"

	robfig "github.com/robfig/cron/v3"
)

// maxCronBytes bounds one authored cron expression before it reaches the
// engine. A cron value is untrusted authored input, so it is length- and
// charset-bounded before parsing.
const maxCronBytes = 256

// Schedule is one compiled cron expression. It carries no engine type in its
// API so callers depend only on this package.
type Schedule struct {
	inner robfig.Schedule
}

// Validate reports whether expr is a bounded, printable-ASCII, standard
// five-field cron expression the engine accepts. It rejects the engine's
// non-five-field spellings (descriptors such as @daily and @every, and
// six-field second-granular expressions) so the authored contract stays
// exactly five fields.
func Validate(expr string) error {
	_, err := Parse(expr)
	return err
}

// Parse validates expr and returns its compiled schedule. The error is a
// complete rule sentence suitable for a diagnostic's detail.
func Parse(expr string) (Schedule, error) {
	if expr == "" {
		return Schedule{}, errors.New("cron expression must not be empty")
	}
	if len(expr) > maxCronBytes {
		return Schedule{}, fmt.Errorf("cron expression must be at most %d bytes; found %d", maxCronBytes, len(expr))
	}
	for i := 0; i < len(expr); i++ {
		if c := expr[i]; c < 0x20 || c > 0x7e {
			return Schedule{}, errors.New("cron expression must be printable ASCII")
		}
	}
	if fields := strings.Fields(expr); len(fields) != 5 {
		return Schedule{}, fmt.Errorf("cron expression must have exactly five space-separated fields; found %d", len(fields))
	}
	inner, err := robfig.ParseStandard(expr)
	if err != nil {
		return Schedule{}, fmt.Errorf("cron expression is not a valid standard five-field expression: %v", err)
	}
	return Schedule{inner: inner}, nil
}

// Next returns the first activation strictly after `after`, in `after`'s
// location. Callers pass UTC times so evaluation is UTC.
func (s Schedule) Next(after time.Time) time.Time {
	return s.inner.Next(after)
}

// Due reports whether the schedule activates exactly at minute, which must be
// a minute-truncated instant. Standard cron activations land on second zero,
// so the first activation strictly after the prior second is this minute
// itself exactly when the schedule is due.
func (s Schedule) Due(minute time.Time) bool {
	minute = minute.Truncate(time.Minute)
	return s.inner.Next(minute.Add(-time.Second)).Equal(minute)
}

// NextAfter validates expr and returns the first activation strictly after
// `after` in that time's location.
func NextAfter(expr string, after time.Time) (time.Time, error) {
	s, err := Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(after), nil
}
