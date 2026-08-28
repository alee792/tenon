# ADR 0024: Add observation to the revision leg of the measure

- Status: accepted
- Amends: `docs/north-star.md` — the measure only, applied with this
  record. It changes nothing else and proposes no mechanism, so that the
  change benefiting from the amendment is not the change making it, as
  the north star requires.
- Builds on: [ADR 0018](0018-add-the-revision-leg-to-the-measure.md)
- Context: [docs/workbench/revision-observability.md](../workbench/revision-observability.md)

## Decision

The revision leg of the measure gains a fourth verb. It reads: empty
directory to a working agent inside the author's harness in five minutes;
the same folder runs headless, scheduled, or staged without edits; and a
revision applies, runs, and attributes to its exact configuration without
human hands — **with its change to the agent's capability surface legible
before it runs.**

Legible means legible to both authors, per ADR 0018. A person reads a
revision's diff and understands it because they already know what a skill,
a tool schema, and a budget are. A loop reads the same diff and does not.
The leg is met only when the change is legible to the consumer that cannot
supply the missing knowledge itself.

## Evidence

The north star permits amendment only against named evidence. Three things
are true that were not addressed when the revision leg was written.

1. **The measure's verbs stop one short of the loop's actual need.** Apply,
   run, and attribute all describe what happens to a revision the loop has
   already committed to. None describes how the loop learns what the
   revision *is*. An outer loop attributing an observed behavior change to
   a revision needs the independent variable it manipulated; the measure
   currently obliges tenon to name the configuration but never to
   characterize the change.

2. **North star 1's diff promise is already half-kept.** It promises a
   folder a person can "read, review, and diff." Tenon keeps that promise
   in its textual sense for the person and not at all for the loop, whose
   coequal standing ADR 0018 established. The gap is between two
   commitments the north star already carries, not a new claim on it.

3. **Textual change and capability change diverge in both directions, and
   only tenon can tell them apart.** The effective agent is not the folder:
   `Project.Skills` merges root and plugin-imported skills under
   collision precedence rules (ADR 0009), tools carry validated schemas,
   and projects carry aggregate budgets (ADR 0013). An outer loop reading
   bytes would have to reimplement those rules and track them across
   releases. Recognizing that only the compiler can compute the delta makes
   this a responsibility tenon owns rather than one it may leave to the
   caller's tooling.

## Consequences

A slice serving revision observability is as ordinary as one serving the
first five minutes; the leg can now be cited as failing rather than argued
for from first principles each time. The amendment binds an outcome, not a
mechanism: nothing here specifies a command, an output format, or a
comparison algorithm, all of which
[ADR 0025](0025-make-the-fingerprint-the-unit-of-revision-identity.md)
proposes separately and may be rejected without disturbing this record.

The amendment does not move the boundary in north star 2. Characterizing a
revision is a statement about authored source, which tenon already parses
and validates; it is not model-loop, context, approval, or runtime
supervision behavior, and it observes no transcript, session, or effect.
Nor does it weaken north star 3: a delta describes a contract, never
behavior, and no delta implies a revision is an improvement.

It stays clear of the non-goals in `AGENTS.md` — evaluations, scoring,
transcript retention, selection among revisions, and lineage tracking — by
binding only the legibility of one revision's change, never the retention
or sequencing of many.

The cost is a fourth verb on a measure whose brevity is load-bearing, and
one more thing every revision-leg slice can be asked to serve. That cost is
accepted because the alternative observed in practice is worse: the loop
reaches for `git diff` and reconstructs tenon's own merge and precedence
rules badly, in a place tenon cannot keep honest.
