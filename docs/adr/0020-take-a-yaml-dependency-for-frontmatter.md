# ADR 0020: Take a YAML dependency for authored frontmatter

- Status: accepted

## Decision

Authored frontmatter is parsed by a real YAML engine taken as a dependency,
not by a parser this repository writes. One wrapper package
(`internal/frontmatter`) is the engine's only importer and enforces the
closed contract every frontmatter surface shares:

- one document, whose root is a mapping;
- every mapping key, at any depth, is a string;
- no aliases and no anchors anywhere;
- no duplicate keys at any depth; and
- values are read only through typed accessors (required plain string,
  Boolean, string-to-string map), so bound fields reject foreign shapes
  while recognized vendor fields pass through unread.

The engine validates; it never re-serializes. Authored bytes are what
generation copies, so parsing cannot reorder, re-quote, or normalize a
field.

The engine currently selected is `go.yaml.in/yaml/v3` — the maintained
continuation of the `gopkg.in/yaml.v3` lineage, and the engine the prototype
validated in production. The selection is a detail behind the wrapper:
swapping engines does not reopen this decision, provided the closed contract
and the only-importer rule hold.

## Context: why a dependency at all

The skill compatibility matrix directs: "Parse frontmatter as YAML. Do not
extend the former line parser to approximate nested `metadata`, lists,
booleans, or vendor documents." Skill frontmatter legitimately contains a
nested `metadata` string map and recognized vendor fields (such as Claude's
`hooks`) whose values are arbitrary YAML that tenon preserves without
interpreting. A hand-rolled parser faces a bad dichotomy: misparse those
shapes, or grow into an unvetted YAML implementation — and this parser sits
on the untrusted-input path, where every authored byte crosses before
workspace mutation, so a parsing bug is a validation bug. One widely used
engine is less machinery and less risk than either horn (tenet 3). The
bootstrap's line-based instructions parser was correct only because
instructions carry two scalar fields; it migrates to the wrapper rather than
being extended.

## North-star reconciliation

This is the repository's first dependency, a named tripwire. It serves
tenet 3 — schema over machinery — by deleting a bespoke parser that would
otherwise grow with every frontmatter surface (instructions, skills,
subagents, connections, schedules). The strictness rules above are enforced
by tenon's wrapper, not assumed of the engine, so the accepted surface stays
as small as the subset we would have hand-written.

## Consequences

- `internal/frontmatter` remains the engine's only importer; every authored
  frontmatter surface parses through it.
- The connections contract's YAML rejections (aliases, tags, merge keys,
  multiple documents) are implemented by this wrapper once.
- `go.mod` carries a comment pointing each dependency at its justification;
  further dependencies still require their own recorded reasoning. This
  decision is not a precedent for convenience imports.
