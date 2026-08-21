package frontmatter

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	cases := map[string]struct {
		content string
		raw     string
		body    string
		err     error
	}{
		"missing delimiter":     {content: "a: b\nbody\n", err: ErrMissing},
		"leading blank line":    {content: "\n---\na: b\n---\nbody\n", err: ErrMissing},
		"decorated delimiter":   {content: "--- yaml\na: b\n---\nbody\n", err: ErrMissing},
		"unclosed":              {content: "---\na: b\n", err: ErrUnclosed},
		"unclosed at delimiter": {content: "---", err: ErrUnclosed},
		"dashes are not close":  {content: "---\na: b\n----\nbody\n", err: ErrUnclosed},
		"closed": {
			content: "---\na: b\n---\nbody line\n",
			raw:     "a: b\n", body: "body line\n",
		},
		"crlf delimiters": {
			content: "---\r\na: b\r\n---\r\nbody\r\n",
			raw:     "a: b\r\n", body: "body\r\n",
		},
		"closing delimiter at eof": {
			content: "---\na: b\n---",
			raw:     "a: b\n", body: "",
		},
		"empty frontmatter": {
			content: "---\n---\nbody\n",
			raw:     "", body: "body\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw, bodyStart, err := Split([]byte(tc.content))
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if tc.err != nil {
				return
			}
			if string(raw) != tc.raw {
				t.Fatalf("raw = %q, want %q", raw, tc.raw)
			}
			// bodyStart must address the original bytes: slicing the input
			// at bodyStart yields exactly the body.
			if got := tc.content[bodyStart:]; got != tc.body {
				t.Fatalf("content[bodyStart:] = %q, want %q", got, tc.body)
			}
		})
	}
}

func TestParseRejections(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want string
	}{
		"alias":                {"base: &x 1\nother: *x\n", "YAML aliases are not supported"},
		"anchor":               {"a: &x 1\n", "YAML anchors are not supported"},
		"nested anchor":        {"a:\n  b: &x 1\n", "YAML anchors are not supported"},
		"merge key":            {"base: &x\n  k: v\nover:\n  <<: *x\n", "YAML aliases are not supported"},
		"duplicate key":        {"a: 1\na: 2\n", `YAML field "a" is duplicated`},
		"nested duplicate key": {"outer:\n  a: 1\n  a: 2\n", `YAML field "a" is duplicated`},
		"non-string key":       {"1: x\n", "YAML mapping keys must be strings"},
		"null key":             {"~: x\n", "YAML mapping keys must be strings"},
		"multiple documents":   {"a: 1\n---\nb: 2\n", "frontmatter must contain one YAML document"},
		"sequence root":        {"- a\n- b\n", "frontmatter must be a YAML mapping"},
		"scalar root":          {"just a string\n", "frontmatter must be a YAML mapping"},
		"null root":            {"~\n", "frontmatter must be a YAML mapping"},
		"invalid yaml":         {"a: [unclosed\n", "frontmatter must be valid YAML"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.raw))
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want error containing %q", tc.raw, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseEmptyIsEmptyMapping(t *testing.T) {
	for _, raw := range []string{"", "  \n\n", "# only a comment\n"} {
		doc, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse(%q) = %v, want empty mapping", raw, err)
		}
		if len(doc.Keys()) != 0 {
			t.Fatalf("Parse(%q).Keys() = %v, want none", raw, doc.Keys())
		}
	}
}

// vendor-shaped nested values parse fine; they are simply not reachable
// through the typed getters.
const accessorRaw = `description: Reviews pull requests.
count: 5
flag: true
off-flag: false
word: yes
quoted: "5"
empty:
meta:
  a: "1"
  b: two
nested:
  deep:
    x: y
listed:
  a:
    - 1
hooks:
  PreToolUse:
    - matcher: x
`

func parseAccessorDoc(t *testing.T) *Doc {
	t.Helper()
	doc, err := Parse([]byte(accessorRaw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func TestDocKeysAndHas(t *testing.T) {
	doc := parseAccessorDoc(t)
	want := []string{
		"count", "description", "empty", "flag", "hooks",
		"listed", "meta", "nested", "off-flag", "quoted", "word",
	}
	if !slices.Equal(doc.Keys(), want) {
		t.Fatalf("Keys() = %v, want %v", doc.Keys(), want)
	}
	if !doc.Has("description") || doc.Has("absent") {
		t.Fatal("Has must report exactly the present fields")
	}
}

func TestDocString(t *testing.T) {
	doc := parseAccessorDoc(t)
	cases := map[string]struct {
		key  string
		want string
		ok   bool
	}{
		"plain string":    {key: "description", want: "Reviews pull requests.", ok: true},
		"quoted number":   {key: "quoted", want: "5", ok: true},
		"int scalar":      {key: "count"},
		"bool scalar":     {key: "flag"},
		"null value":      {key: "empty"},
		"mapping value":   {key: "meta"},
		"absent field":    {key: "absent"},
		"vendor document": {key: "hooks"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := doc.String(tc.key)
			if tc.ok != (err == nil) {
				t.Fatalf("String(%q) err = %v, want ok=%v", tc.key, err, tc.ok)
			}
			if err == nil && got != tc.want {
				t.Fatalf("String(%q) = %q, want %q", tc.key, got, tc.want)
			}
			if err != nil && !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error %q must name the field %q", err, tc.key)
			}
		})
	}
}

func TestDocBool(t *testing.T) {
	doc := parseAccessorDoc(t)
	if got, err := doc.Bool("flag"); err != nil || !got {
		t.Fatalf("Bool(flag) = %v, %v; want true", got, err)
	}
	if got, err := doc.Bool("off-flag"); err != nil || got {
		t.Fatalf("Bool(off-flag) = %v, %v; want false", got, err)
	}
	for _, key := range []string{"word", "count", "description", "empty", "absent"} {
		if _, err := doc.Bool(key); err == nil {
			t.Fatalf("Bool(%q) succeeded, want error", key)
		} else if !strings.Contains(err.Error(), key) {
			t.Fatalf("error %q must name the field %q", err, key)
		}
	}
}

func TestDocStringMap(t *testing.T) {
	doc := parseAccessorDoc(t)
	got, err := doc.StringMap("meta")
	if err != nil {
		t.Fatalf("StringMap(meta) = %v", err)
	}
	if want := map[string]string{"a": "1", "b": "two"}; !maps.Equal(got, want) {
		t.Fatalf("StringMap(meta) = %v, want %v", got, want)
	}
	for _, key := range []string{"nested", "listed", "hooks", "description", "count", "absent"} {
		if _, err := doc.StringMap(key); err == nil {
			t.Fatalf("StringMap(%q) succeeded, want error", key)
		} else if !strings.Contains(err.Error(), key) {
			t.Fatalf("error %q must name the field %q", err, key)
		}
	}
}

func TestDocIsNull(t *testing.T) {
	doc := parseAccessorDoc(t)
	if !doc.IsNull("empty") {
		t.Fatal("IsNull(empty) = false, want true")
	}
	for _, key := range []string{"flag", "description", "meta", "absent"} {
		if doc.IsNull(key) {
			t.Fatalf("IsNull(%q) = true, want false", key)
		}
	}
}
