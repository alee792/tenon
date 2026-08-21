// Package frontmatter splits and validates authored YAML frontmatter under
// one closed contract: one document whose root is a mapping, string keys at
// every depth, no aliases, no anchors, no duplicate keys, and values read
// only through typed accessors. The package validates authored bytes and
// never re-serializes them, so generation always copies exactly what was
// authored. It is the only package that may import the YAML engine: a real
// engine is required because recognized vendor fields carry arbitrary YAML
// that must parse without being interpreted, and this wrapper is where the
// strictness lives, not the engine.
package frontmatter

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"go.yaml.in/yaml/v3"
)

// ErrMissing reports content that does not begin with a "---" line.
var ErrMissing = errors.New("frontmatter is missing")

// ErrUnclosed reports frontmatter whose closing "---" line never arrives.
var ErrUnclosed = errors.New("frontmatter is not closed")

// Split extracts the raw frontmatter from content, which must begin with a
// line exactly "---" (a trailing \r is tolerated). raw is the bytes between
// the delimiter lines (exclusive); bodyStart is the byte offset in content
// just past the closing delimiter line (including its newline when present),
// so callers can address the original body bytes.
func Split(content []byte) (raw []byte, bodyStart int, err error) {
	open, ok := delimiterLineEnd(content, 0)
	if !ok {
		return nil, 0, ErrMissing
	}
	for at := open; at < len(content); {
		if end, ok := delimiterLineEnd(content, at); ok {
			return content[open:at], end, nil
		}
		nl := bytes.IndexByte(content[at:], '\n')
		if nl < 0 {
			break
		}
		at += nl + 1
	}
	return nil, 0, ErrUnclosed
}

// delimiterLineEnd reports whether the line starting at offset is exactly
// "---" (tolerating a trailing \r), returning the offset just past the
// line's newline, or len(content) when the line ends the content.
func delimiterLineEnd(content []byte, offset int) (end int, ok bool) {
	line := content[offset:]
	end = len(content)
	if nl := bytes.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
		end = offset + nl + 1
	}
	line = bytes.TrimSuffix(line, []byte("\r"))
	return end, bytes.Equal(line, []byte("---"))
}

// Doc is one validated frontmatter document: a mapping whose fields are read
// only through the typed accessors.
type Doc struct {
	keys   []string
	fields map[string]*yaml.Node
}

// Parse validates raw against the closed contract of ADR 0020 and returns
// the document. Empty or whitespace-only raw is an empty mapping. Errors are
// complete rule sentences suitable for diagnostic text.
func Parse(raw []byte) (*Doc, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var node yaml.Node
	if err := dec.Decode(&node); err != nil {
		if errors.Is(err, io.EOF) {
			return &Doc{fields: map[string]*yaml.Node{}}, nil
		}
		return nil, fmt.Errorf("frontmatter must be valid YAML: %v", err)
	}
	if err := dec.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("frontmatter must contain one YAML document")
	}

	root := &node
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return &Doc{fields: map[string]*yaml.Node{}}, nil
		}
		root = root.Content[0]
	}
	if err := checkTree(root); err != nil {
		return nil, err
	}
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("frontmatter must be a YAML mapping")
	}

	d := &Doc{
		keys:   make([]string, 0, len(root.Content)/2),
		fields: make(map[string]*yaml.Node, len(root.Content)/2),
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		d.keys = append(d.keys, key)
		d.fields[key] = root.Content[i+1]
	}
	sort.Strings(d.keys)
	return d, nil
}

// checkTree walks the whole tree once and rejects aliases, anchors,
// non-string mapping keys, and duplicate keys at any depth. The alias rule
// takes priority so alias-shaped machinery — including merge keys, whose
// value is an alias — is reported as the alias violation it is.
func checkTree(root *yaml.Node) error {
	c := &treeChecker{}
	c.walk(root)
	if c.alias {
		return errors.New("YAML aliases are not supported")
	}
	return c.err
}

type treeChecker struct {
	alias bool
	err   error
}

func (c *treeChecker) fail(err error) {
	if c.err == nil {
		c.err = err
	}
}

func (c *treeChecker) walk(n *yaml.Node) {
	if n.Kind == yaml.AliasNode {
		c.alias = true
		return
	}
	if n.Anchor != "" {
		c.fail(errors.New("YAML anchors are not supported"))
	}
	switch n.Kind {
	case yaml.MappingNode:
		seen := make(map[string]bool, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			c.walk(key)
			if key.Kind == yaml.ScalarNode && key.Tag == "!!str" {
				if seen[key.Value] {
					c.fail(fmt.Errorf("YAML field %q is duplicated", key.Value))
				}
				seen[key.Value] = true
			} else if key.Kind != yaml.AliasNode {
				c.fail(errors.New("YAML mapping keys must be strings"))
			}
			c.walk(value)
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, child := range n.Content {
			c.walk(child)
		}
	}
}

// Keys returns every top-level field name, sorted.
func (d *Doc) Keys() []string {
	keys := make([]string, len(d.keys))
	copy(keys, d.keys)
	return keys
}

// Has reports whether the top-level field exists.
func (d *Doc) Has(key string) bool {
	_, ok := d.fields[key]
	return ok
}

// String returns the field as a plain string scalar; any other node shape or
// tag is an error.
func (d *Doc) String(key string) (string, error) {
	n, ok := d.fields[key]
	if !ok {
		return "", fmt.Errorf("frontmatter field %q is missing", key)
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", fmt.Errorf("frontmatter field %q must be one plain string", key)
	}
	return n.Value, nil
}

// Bool returns the field as a YAML boolean scalar; only the spellings of
// true and false qualify.
func (d *Doc) Bool(key string) (bool, error) {
	n, ok := d.fields[key]
	if !ok {
		return false, fmt.Errorf("frontmatter field %q is missing", key)
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!bool" {
		switch n.Value {
		case "true", "True", "TRUE":
			return true, nil
		case "false", "False", "FALSE":
			return false, nil
		}
	}
	return false, fmt.Errorf("frontmatter field %q must be the YAML boolean true or false", key)
}

// StringMap returns the field as a one-level string-to-string mapping; every
// value must be a plain string scalar.
func (d *Doc) StringMap(key string) (map[string]string, error) {
	n, ok := d.fields[key]
	if !ok {
		return nil, fmt.Errorf("frontmatter field %q is missing", key)
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("frontmatter field %q must be a mapping of strings", key)
	}
	out := make(map[string]string, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		name, value := n.Content[i].Value, n.Content[i+1]
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return nil, fmt.Errorf("frontmatter field %q must map %q to one plain string", key, name)
		}
		out[name] = value.Value
	}
	return out, nil
}

// IsNull reports whether the field exists and its value is null.
func (d *Doc) IsNull(key string) bool {
	n, ok := d.fields[key]
	return ok && n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}
