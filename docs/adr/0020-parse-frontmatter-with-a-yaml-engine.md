# ADR 0020: Parse authored frontmatter with a YAML engine

- Status: accepted

## Decision

Adopt `go.yaml.in/yaml/v3` — the YAML-org-maintained continuation of the
`gopkg.in/yaml.v3` lineage, and the engine the prototype validated — as
tenon's single YAML dependency, wrapped in one frontmatter package that
enforces a closed contract on top of it:

- one document, whose root is a mapping;
- every mapping key, at any depth, is a string;
- no aliases and no anchors anywhere;
- no duplicate keys at any depth; and
- consumers read values only through typed accessors (required plain string,
  string-to-string map), so a field bound as a string rejects non-string
  tags, while recognized vendor fields may carry arbitrary shapes.

The engine validates; it never re-serializes. Authored bytes are what
generation copies, so parsing cannot reorder, re-quote, or normalize a
field.

## Context

The skill compatibility matrix directs: "Parse frontmatter as YAML. Do not
extend the former line parser to approximate nested `metadata`, lists,
booleans, or vendor documents." Skill frontmatter legitimately contains a
nested `metadata` string map and recognized vendor fields (such as Claude's
`hooks`) whose values are arbitrary YAML that tenon preserves without
interpreting. A hand-rolled parser either misparses those shapes or grows
into an unvetted YAML implementation — more machinery and more risk than one
widely used engine (tenet 3). The bootstrap's line-based instructions parser
was correct only because instructions carry two scalar fields; it migrates to
this package rather than being extended.

## North-star reconciliation

This is the repository's first dependency, a named tripwire. It serves
tenet 3 directly — schema over machinery: the engine replaces a bespoke
parser that would otherwise grow with every new frontmatter surface
(instructions, skills, subagents, connections, schedules). The closed
contract above keeps the acceptance surface as small as the subset we would
have hand-written, and the strictness rules (aliases, anchors, duplicates,
non-string keys) are enforced by tenon's wrapper, not assumed of the engine.

## Consequences

- `internal/frontmatter` is the only package that imports the YAML engine;
  every authored frontmatter surface parses through it.
- The connections contract's YAML rejections (aliases, tags, merge keys,
  multiple documents) are implemented by this wrapper once.
- Further dependencies still require their own recorded justification; this
  decision is not a precedent for convenience imports.
