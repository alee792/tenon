# Repository guidance

Tenon compiles filesystem-authored agent projects into native configuration
for coding-agent harnesses — Claude Code and Codex — and is the
reproducibility substrate for agent improvement loops.

Authority order, first wins:

1. `docs/north-star.md` — what stays constant; overrules every other
   document wherever they disagree.
2. `docs/vision.md` and `docs/product-spec.md` — the product and its binding
   contract; every stated behavior in the specification binds, and its
   acceptance section is the proof skeleton.
3. `docs/adr/` — accepted decisions and their rationale.
4. `docs/glossary.md` and `docs/workbench/status.md` — shared vocabulary and
   the current gap between contract and implementation.

Read those documents in that order before changing product behavior. Silence
asserts alignment with the north star; explicit reconciliation is owed only
on its tripwires (a new author-facing concept, a new subsystem or dependency,
a moved boundary, a bet on an unvalidated future), covered by the
maintainer's `north-star-review` skill.

Keep the native harness responsible for its model loop, context, native
tools, approvals, and interactive interface. Tenon owns agent-project
discovery and validation, generated harness files, dispatcher-managed
sessions, and explicitly managed tools. Never overwrite hand-authored harness
files or claim governance over native harness effects.

Keep packages organized by concrete responsibility; no generic core, common,
util, or services layers. Prefer the standard library. Dependencies are rare
and justified inline in go.mod — what the module is for and why the standard
library cannot cover it — not by ADR; do not re-litigate a dependency whose
justification stands. Validate and bound
filesystem, process, protocol, and model-visible inputs before any workspace
mutation. Diagnostics are a stable surface: validation failures carry stable
identifiers and authored paths, legible to people and parseable by drafting
harnesses and improvement loops; identifier stability and machine-readable
parity with apply are binding.

Tests and the literal CLI journey define completion; every slice is proven by
credential-free tests (fake harness processes, no live model calls). Record
consequential architecture choices as short ADRs and run `./scripts/check.sh`
for affected work. Credentials, live external actions, publication, and
deployment require explicit human authorization.

The `hctl` prototype (alee792/hctl) is the frozen, read-only reference
implementation. When a specified behavior is in doubt, consult its tests and
acceptance records; port intent, never code. Out of scope unless re-decided
by ADR: evaluations, scoring, transcript retention, selection among
revisions, lineage tracking, a marketplace, and network acquisition of
components. The conversational channel product stays in the prototype.
