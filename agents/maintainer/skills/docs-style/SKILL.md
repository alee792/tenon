---
name: docs-style
description: The lens for writing and rewriting documentation. Apply when authoring or revising any doc; calibrate by the doc's audience before cutting.
---

Rewrite for concision, honesty, and progressive disclosure. First decide
who the doc serves — the rules below apply everywhere, but what counts as
"internal" depends on the answer:

- **User-facing** (README, use cases, guides, examples): the reader is a
  user or customer. ADR numbers, issue references, prototype history,
  release choreography, and unvalidated-path caveats are internal — cut
  them, linking once to the spec or ADR only where the reader genuinely
  needs the depth.
- **Contract** (product spec, glossary, north star): internal
  cross-references are the point; keep them. Everything else below still
  applies.
- **Internal** (ADRs, workbench): reasoning and provenance are the
  content. Apply only honesty and say-it-once.

The rules:

**Audience first.** Write for the reader's task, not the author's process.
Every sentence either moves the reader toward doing something or gets cut.

**Honest as of today.** Every command and claim must be true right now.
Never document a hoped-for future as fact; if something does not exist
yet, say so in one plain line. Never include a quote you cannot verify —
paraphrase.

**Say it once.** Each idea gets one canonical home; everywhere else links
instead of restating. Repetition and hedged, triple-qualified sentences
read as machine-written — cut both.

**Progressive disclosure.** Sharp one-paragraph claim, then the smallest
complete working example, then the growth path as scannable bullets, then
links for depth. Tables for enumerable mappings, prose for reasoning. Drop
decoration — ASCII diagrams, section throat-clearing — that carries no
meaning.

**Lead with the strongest framing.** Where the project's thinking has
moved past the doc, rewrite around the current framing rather than
patching the old one; the accepted ADRs say where thinking currently
stands.
