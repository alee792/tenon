// Package diagnostics is tenon's stable diagnostic surface. Validation and
// apply failures carry a stable identifier, the authored path, and the exact
// rule violated, legible as prose and parseable as one JSON object per line.
// Identifiers are part of the product contract: renaming one is a breaking
// change.
package diagnostics

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Severity classifies a diagnostic. Errors fail validation; warnings do not.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Diagnostic is one validation finding.
type Diagnostic struct {
	// ID is the stable dotted identifier, e.g. "instructions.body.empty".
	ID string `json:"id"`
	// Severity is "error" or "warning".
	Severity Severity `json:"severity"`
	// Path is the authored path relative to the agent root, or "." for the
	// root itself.
	Path string `json:"path"`
	// Rule states the exact rule violated, with the violating specifics.
	Rule string `json:"rule"`
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s: %s: %s", d.Severity, d.Path, d.ID, d.Rule)
}

// List accumulates diagnostics in a deterministic order.
type List struct {
	all []Diagnostic
}

// Add appends a diagnostic.
func (l *List) Add(d Diagnostic) { l.all = append(l.all, d) }

// Errorf appends an error diagnostic with a formatted rule.
func (l *List) Errorf(id, path, format string, args ...any) {
	l.Add(Diagnostic{ID: id, Severity: Error, Path: path, Rule: fmt.Sprintf(format, args...)})
}

// Warnf appends a warning diagnostic with a formatted rule.
func (l *List) Warnf(id, path, format string, args ...any) {
	l.Add(Diagnostic{ID: id, Severity: Warning, Path: path, Rule: fmt.Sprintf(format, args...)})
}

// HasErrors reports whether any diagnostic is an error.
func (l *List) HasErrors() bool {
	for _, d := range l.all {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// All returns the diagnostics sorted by path, then identifier, then rule, so
// identical input always renders identically.
func (l *List) All() []Diagnostic {
	out := make([]Diagnostic, len(l.all))
	copy(out, l.all)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// WriteProse renders each diagnostic as one bounded prose line.
func (l *List) WriteProse(w io.Writer) error {
	for _, d := range l.All() {
		if _, err := fmt.Fprintln(w, d.String()); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSONL renders each diagnostic as one JSON object per line.
func (l *List) WriteJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, d := range l.All() {
		if err := enc.Encode(d); err != nil {
			return err
		}
	}
	return nil
}

// Bound trims a rule detail to a bounded, single-line form so diagnostics
// stay bounded prose. It never truncates identifiers or paths.
func Bound(detail string, max int) string {
	detail = strings.ReplaceAll(detail, "\n", " ")
	if len(detail) > max {
		return detail[:max] + "..."
	}
	return detail
}
