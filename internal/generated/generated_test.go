package generated

import (
	"bytes"
	"strings"
	"testing"
)

// TestSkillMDInsertsOneMarkerLine proves the deliberate contract point: the
// generated SKILL.md is the authored bytes plus exactly one marker line after
// the closing frontmatter delimiter, and the marker carries no fingerprint,
// version, or provenance.
func TestSkillMDInsertsOneMarkerLine(t *testing.T) {
	src := []byte("---\nname: echo\n---\n\nBody.\n")
	bodyStart := len("---\nname: echo\n---\n")
	got := SkillMD(src, bodyStart)
	want := []byte("---\nname: echo\n---\n" + Marker + "\n\nBody.\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("SkillMD = %q, want %q", got, want)
	}
	if strings.Contains(Marker, "sha256") || strings.Contains(Marker, "fingerprint") {
		t.Fatalf("the marker must carry no provenance: %q", Marker)
	}
}

func TestSkillMDWithoutTrailingNewline(t *testing.T) {
	src := []byte("---\nname: echo\n---")
	got := SkillMD(src, len(src))
	want := []byte("---\nname: echo\n---\n" + Marker + "\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("SkillMD = %q, want %q", got, want)
	}
}
