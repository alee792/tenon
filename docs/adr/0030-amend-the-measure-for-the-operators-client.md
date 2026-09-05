# ADR 0030: Amend the measure for the operator's client

- Status: accepted. Proposed in the change that removed the schedules
  surface; the wording is applied in this dedicated change, per the north
  star's own change rule (it changes only through a dedicated ADR naming
  the evidence, never in the same change that benefits)
- Amends: [north star](../north-star.md) — the measure's second and third
  legs, and commitment 2's list of what tenon never absorbs
- Evidence: [ADR 0029](0029-stop-driving-the-harness.md)

## Decision

The measure's second leg reads "the same folder later runs headless or
staged, under any client, without edits" in place of "runs headless,
scheduled, or staged without edits". Its third leg reads "a revision
applies, runs under the operator's client, and attributes to its exact
configuration without human hands" in place of "applies, runs, and
attributes". Commitment 2 gains one sentence: tenon's crossing ends at the
applied workspace, and it never absorbs the session itself.

## Evidence

ADR 0029 removed `tenon run`, schedule execution, every harness driver, and
then the authored `schedules/` surface. Two words in the measure named
things tenon no longer does. "Scheduled" named a tenon clock; a task on a
clock is now the operator's scheduler launching the same client, which the
"headless" leg already covers once it says "under any client". "Runs" read
as a tenon act; the revision leg is met by `check`, `apply`, the operator's
client, and a recorded fingerprint, and the wording should say whose act
the run is so the leg cannot be read as a promise to bring the dispatcher
back.

Commitment 2 already listed runtime supervision among the things tenon
never absorbs. The dispatcher was arguably that, and the ambiguity let it
stand for a year; naming the session closes it.

## What does not change

Commitments 1 and 3, the five-minute leg, the revision leg's legibility
clause, the two authors, and every tenet. The measure still requires that
the same folder runs unedited and that a revision attributes to its exact
configuration; only who launches it is now stated.
