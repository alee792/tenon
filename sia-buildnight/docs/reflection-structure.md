# Structuring reflections for RSI (ACE-informed)

Reference for the observability/guidance work. SIA imposes **no** reflection
schema (see `how-sia-improves.md`), so we impose one. The strongest published
guidance is **ACE — Agentic Context Engineering** (Zhang et al., arXiv
2510.04618). Below: what ACE says, and how to apply it to SIA's
`improvement.md` / `context.md` / carried playbook.

## What ACE argues

ACE treats the evolving context as a **playbook** — a growing, itemized set of
strategies — refined by three roles:

- **Generator** — runs the task, produces a trajectory. (SIA: the target agent.)
- **Reflector** — analyzes the trajectory + outcome, extracts concrete lessons.
- **Curator** — merges those lessons into the playbook as small deltas, de-dups.

(In SIA the *feedback agent* plays Reflector + Curator in one step.)

Two failure modes ACE is built to avoid — both of which the default SIA loop is
exposed to:

- **Context collapse** — rewriting the whole reflection each round erodes detail
  over generations. (SIA rewrites `improvement.md` fresh every gen → at risk.)
- **Brevity bias** — summarizing drops domain-specific insight ("improve
  robustness" instead of "answers with a trailing period fail the regex in
  `format_submission`"). (SIA's `_generate_llm_summary` is a summarizer → at risk.)

ACE's fixes, and how we apply them:

1. **Itemized bullets with stable IDs, not prose.** Represent the reflection as a
   list of atomic, retrievable items, each individually updatable — not a
   paragraph. → Make `improvement.md` (and any carried `PLAYBOOK.md`) a bulleted
   list of tactics with IDs, not narrative.
2. **Incremental delta updates, never full rewrites.** Append new bullets, edit
   the specific ones that changed, keep the rest verbatim. → Tell the feedback
   agent to CARRY FORWARD the prior playbook and only add/modify bullets.
3. **Ground every item in evidence + outcome.** Each bullet ties to a concrete
   failure mode and its measured effect. → Link each tactic to the diagnostic
   (worst stage / error class) and the `results.json` score delta it produced.
4. **Periodic de-duplication.** Collapse redundant bullets so the playbook stays
   compact without losing detail.

Reported effect: ~8.6% average gain on complex reasoning benchmarks by building
comprehensive, non-collapsing playbooks.

## Concrete schema for our loop

Keep the fixed `improvement.md` block (SIA re-parses it into `context.md`) AND,
for agentic impls that write extra files, a carried `PLAYBOOK.md`:

```
## Generation <N>
- incumbent_gen: <M>   incumbent_score: <S>   this_score: <T>
- seed_gen: <gen whose code you edited>
- hypothesis: <family>            # tag, from the fixed set
- edit_summary: <one sentence: what + where>
- evidence: <worst stage / error class from the diagnostic that motivated it>
- outcome: <score delta vs incumbent; accepted? y/n>

## Playbook deltas this generation   # ACE-style, carried forward in PLAYBOOK.md
- [T-014 | harden-output-parsing | VALIDATED +3.1] strip trailing punctuation
  before the answer regex — fixed 42% of parse-stage failures at gen 4.
- [T-021 | self-consistency-voting | TRIED -0.4 REJECTED] k=3 majority; no gain,
  cost 3x tokens. Do not retry unless eval is cheap.
```

Rules the guidance should enforce (ACE + metacognitive-reflection literature):

- **Carry the playbook forward; edit deltas only.** Never re-summarize it from
  scratch (avoids context collapse).
- **Rule admissibility** — promote a tactic to `VALIDATED` only after a measured
  score gain; mark regressions `REJECTED` and don't retry them (from Meta-Policy
  Reflexion's reusable-memory + admissibility idea).
- **Keep the specific detail** — reference the exact failure, sample pattern, or
  regex; resist the urge to generalize into a vague principle (anti-brevity).
- **Separate principle from procedure** — note both the general lesson and the
  concrete edit (metacognitive-reflection framing).

## On "MCE"

I could not resolve **MCE** to a specific published RSI/reflection framework —
it did not surface in search. The nearest metacognitive-reflection work, if one
of these is what you meant:
- **MARS — Metacognitive Agent with Reflective Self-improvement** (principle +
  procedural learning in one recurrence cycle).
- **"Learn Like Humans: Use Meta-cognitive Reflection for Efficient
  Self-Improvement"** (arXiv 2601.11974).
- **Meta-Policy Reflexion** (arXiv 2509.03990) — reusable reflective memory +
  rule admissibility.

Tell me which (or the real expansion of MCE) and I'll fold its specifics in.

## Sources
- ACE: https://arxiv.org/abs/2510.04618
- Reflexion (foundational verbal self-reflection): the origin of outcome-grounded
  textual reflection loops.
- Meta-Policy Reflexion: https://arxiv.org/pdf/2509.03990
- "Learn Like Humans" (metacognitive reflection): https://arxiv.org/pdf/2601.11974
