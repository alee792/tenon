# ADR 0025: Make the fingerprint the unit of revision identity, and the capability surface its delta

- Status: proposed — appetite, not architecture (tenet 4). Acceptance
  trigger: the first real consumer — an improvement loop or an operator —
  drives revisions through tenon and has to shell out to `git diff` plus
  ad-hoc parsing to learn what a revision changed. No comparison code lands
  before that trigger fires; if it never fires, this record is rejected
  with reasons.
- Depends on: [ADR 0024](0024-add-observation-to-the-revision-leg.md),
  accepted; this record is the mechanism that amendment deliberately leaves
  unspecified. Accepting the measure's fourth verb does not accept this
  rendering of it — 0024 binds the outcome, and this record may still be
  rejected without disturbing it.
- Builds on: [ADR 0013](0013-bound-authored-projects-with-aggregate-budgets.md),
  [ADR 0018](0018-add-the-revision-leg-to-the-measure.md)
- Context: [docs/workbench/revision-observability.md](../workbench/revision-observability.md)

## Decision

Two parts. The first states what tenon already produces and commits to its
properties; the second adds the one thing missing.

### 1. Tenon mints the unit of lineage; the outer loop composes the chain

The source fingerprint is the unit of revision identity for an external RSI
system, and the stable diagnostic identifier is the unit of revision
*rejection*. Together they are the atoms an outer loop builds lineage from.
Tenon guarantees four properties of those atoms and stores no chain:

- **Commit-free.** The fingerprint is defined on the working tree at every
  instant, with no commit, staging, or clean tree. This is the property git
  cannot supply: apply's own best-effort `git_commit` is recorded only when
  the tree is clean, so git identity is empty at exactly the moment an
  inner loop is mid-revision.
- **Gate-proven.** A fingerprint is reported only for a project that loads
  and whose tools can be prepared — `tenon fingerprint show` runs the same
  gate as validate and apply. A fingerprint therefore certifies that a
  runnable agent existed, which a tree hash does not.
- **Deterministic and content-addressed.** Identical authored sources
  produce identical fingerprints, with no timestamps anywhere in the
  identity, so an outer loop can recognize a revision it has already seen.
- **Joinable.** The same value apply records is the value every dispatch
  event carries, so observation made outside tenon joins back without the
  loop maintaining its own mapping.

A revision that never becomes a fingerprint is named instead by the
identifier set that rejected it — stable across releases, matching apply's
own failures, and parseable without reading prose. An outer loop's lineage
thus has two node kinds and needs no tenon-side history to build either.

**The edge is captured, never recovered.** The fingerprint is a content
digest, so nothing about a revision's parent is derivable from it: two
fingerprints establish same or different, and no ordering, distance, or
ancestry. Chaining the parent into the digest — what a commit hash does —
would supply ancestry at the cost of determinism, and recognizing a
revision already seen is worth more to a loop than deriving where it came
from, so the fingerprint stays parentless by construction.

The consequence binds the consumer rather than tenon: an outer loop that
wants an edge must fingerprint the source **before** handing it to the
inner loop, not only after. Tenon cannot supply the edge even in
principle, because it is not present when the edge is created — the inner
loop mutates files inside the harness, and tenon first sees the directory
at the next validate or apply, by which time the parent state is gone.
Capture is therefore the loop's own discipline, and a parent not captured
at dispatch time is unrecoverable rather than merely inconvenient. The
[revision observability record](../workbench/revision-observability.md)
carries the sequence and what each step buys.

Tenon retains no sequence of revisions, no parent pointers, no scores, and
no transcripts, and never decides which revision supersedes which. This is
not a re-decision of the lineage-tracking non-goal in `AGENTS.md`: it draws
the line the non-goal assumes. Supplying the atom and refusing the chain is
the smaller commitment, because every RSI system wants a different chain and
all of them want a trustworthy atom.

### 2. A revision's delta is a capability-surface delta, not a textual one

Tenon reports what a revision changed about the **effective agent** — the
parsed `Project`, after skill merging and plugin precedence (ADR 0009),
tool schema validation, connection and subagent acceptance, and budget
accounting (ADR 0013) — never a diff of authored bytes. The reference
rendering is `tenon diff OLD NEW --harness <claude|codex>`, a pure function
of two agent sources that writes nothing:

```
skill  code-review   added
tool   fetch_issue   schema changed: +repo (required)
mcp    github        removed
instructions          4.1KB → 9.7KB (budget 71% → 94%)
```

Binding: the comparison is over the parsed model rather than the file
bytes, so a revision that reorders frontmatter keys, reflows prose, or
renames a plugin directory without changing what the agent can do reports
an empty delta; each entry names the authored component kind and its name;
the machine-readable rendering carries stable per-entry identifiers and
authored paths under the same diagnostics discipline as validate, apply,
and drift; both sides pass the same load and preparation gate before any
delta is reported, so a delta is never computed against a source that could
not run; and the delta closes with both fingerprints, making it a statement
about two named identities rather than two paths.

Textual review stays git's. The delta is a semantic overlay on top of it,
and the reference rendering is expected to be read alongside `git diff`,
never instead of it.

## Evidence

The workbench record carries the full argument; three points decide it.

First, the effective agent is not the folder. `Project.Skills` merges root
`skills/` with every valid plugin's imported skills under collision
precedence — root always wins, then lexically first plugin (ADR 0009). An
outer loop computing capability change from bytes must reimplement those
rules and track them across releases; tenon computes them on every load.
The comparison is correctly computable only where the merge already
happens.

Second, textual churn and capability change diverge in both directions: a
reworded instruction body is a large diff and no capability change, and one
added line under `connections/` is a one-line diff and a whole new MCP
server. An outer loop attributing behavior to a revision is fed noise by
the first case and misses the second's significance.

Third, ADR 0018 recorded that well-formedness validation, not drafting
skill, is the binding constraint on loop-driven authorship. The same
reasoning extends past the gate: once cheap models draft revisions
everywhere, the outer loop's constraint becomes attributing outcomes to the
right variable, and a delta over the compiled surface is the smallest thing
that serves it.

## Consequences

Revision observability becomes a tenon responsibility with a named owner
rather than something each consumer improvises. The outer loop's contract
for driving revisions is then closed with no shell in it: validate to gate,
apply to materialize and name, diff to characterize, dispatch to attribute
— every step machine-readable and keyed by the same fingerprint.

The machinery stays small by construction (tenets 1 and 3): the comparison
is a pure function over a `Project` model that already exists, adds no
stored state, no new authored file kind, and no new persona (tenet 2). If
it cannot be built that way it is too much machinery for what it buys, and
this record should be rejected rather than scaled up — the acceptance
trigger above exists to keep that judgment honest.

Two costs are accepted. The delta's entry vocabulary becomes a stable
machine-facing surface, so it carries the same rename-is-a-breaking-change
discipline the diagnostic identifiers already carry. And a semantic delta
can mislead in the one direction a textual diff cannot: an empty delta
means the declared capability surface is unchanged, never that the agent
will behave the same, since instruction prose changes freely underneath it.
The rendering must not imply otherwise, and no delta ever asserts that a
revision is an improvement.

This record mints a new author-facing concept and so owes explicit north
star reconciliation under the `north-star-review` skill, which acceptance
should run rather than assume.
