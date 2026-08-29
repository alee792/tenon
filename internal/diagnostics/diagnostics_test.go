package diagnostics

import (
	"testing"
	"unicode/utf8"
)

// TestBoundMapsControlCharactersToSpaces proves Bound neutralizes every
// control character below 0x20 plus DEL (0x7f) — not just \n — since a
// detail can carry remote-controlled bytes (a subprocess's stderr, a
// server message) where an unmapped \r risks overwriting a rendered
// terminal line.
func TestBoundMapsControlCharactersToSpaces(t *testing.T) {
	in := "line one\rline two\nline three\x01\x7fend"
	want := "line one line two line three  end"
	if got := Bound(in, 1000); got != want {
		t.Fatalf("Bound(%q) = %q, want %q", in, got, want)
	}
}

// TestBoundTruncatesOnRuneBoundary proves a length cut never splits a
// multi-byte UTF-8 rune into an invalid tail.
func TestBoundTruncatesOnRuneBoundary(t *testing.T) {
	// "é" is two bytes (0xC3 0xA9); repeat it so a byte-oriented cut at an
	// odd length would otherwise land mid-rune.
	in := "aé" // 'a' (1 byte) + 'é' (2 bytes) = 3 bytes total
	got := Bound(in, 2)
	if !utf8.ValidString(got) {
		t.Fatalf("Bound(%q, 2) = %q is not valid UTF-8", in, got)
	}
	if got != "a..." {
		t.Fatalf("Bound(%q, 2) = %q, want %q", in, got, "a...")
	}
}

// TestBoundLeavesShortDetailUnchanged proves Bound is a no-op below max,
// preserving identifiers and paths exactly as documented.
func TestBoundLeavesShortDetailUnchanged(t *testing.T) {
	in := "short detail"
	if got := Bound(in, 100); got != in {
		t.Fatalf("Bound(%q) = %q, want unchanged", in, got)
	}
}
