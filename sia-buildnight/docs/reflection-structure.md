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

## MCE — Meta Context Engineering (the separation principle)

**MCE = bi-level co-evolution: separate the context *mechanism* from the context
*content*.** The mechanism (context *skills* — *how* the agent reflects, selects,
and manages memory) evolves independently of the content (context *artifacts* —
*what* it stores/edits: the code, the playbook). The point is to **avoid
conflating procedural improvement with artifact growth** — don't let "how I
improve" degrade every time you edit "what I produced."

Mapping to SIA:
- **Mechanism (meta-layer)** = our selection/reflection protocol — the
  `target_agent.py` docstring + `GUIDANCE.md`. This is a *skill*; keep it stable
  and separately versioned. It should NOT be rewritten as a side effect of editing
  the agent's task code.
- **Content (artifacts)** = `target_agent.py`'s strategy code + the itemized
  playbook (`improvement.md` / `PLAYBOOK.md`). These evolve freely.

The default SIA loop conflates the two: every generation the feedback agent both
rewrites the code and re-derives its reflection prose, with nothing protecting the
method. MCE says: freeze the method layer, evolve the content layer. For us that
is almost free — we already tell the agent to preserve the docstring; make the
separation explicit and version the meta-layer.

## High-yield, low-effort changes given SIA's constraints

Ranked for a <2h, ~6–10-generation sprint. All are prompt/format/convention
changes — no fork, no new tooling.

1. **[MCE] Freeze + version the meta-layer.** Put a `PROTOCOL vN` line at the top
   of the docstring/`GUIDANCE.md`; rule: "never edit the protocol while editing
   task code." Separates mechanism from content. *Effort: trivial. Yield: high —
   stops method drift over generations.*
2. **[ACE] Itemized delta playbook, not prose.** Replace the free-form
   `improvement.md` with tagged bullets (`[Strategy]`/`[Mistake]`/`[Tool]`), each
   with an ID, the evidence, and the score outcome; **carry forward and
   delta-update — never rewrite.** *Effort: a schema. Yield: high — directly fixes
   SIA's context-collapse + brevity-bias exposure (it rewrites `improvement.md`
   fresh each gen and summarizes via `_generate_llm_summary`).*
3. **[Reflexion + rule admissibility] Don't repeat rejected hypotheses.** Before
   proposing, scan the playbook; skip anything marked `REJECTED`; promote to
   `VALIDATED` only after a measured score gain. *Effort: a rule. Yield: high —
   SIA has no selector, so it otherwise re-tries known-bad edits and burns
   generations.*
4. **[STOP] Explicit editable-region markers.** A crisp `FROZEN ABOVE / EDITABLE
   BELOW` banner in `target_agent.py` around the strategy region (CLI, logging,
   incumbent surfacing, docstring stay frozen). *Effort: comments. Yield: high —
   keeps edits local; hits the judges' "precise changes without breaking unrelated
   behavior" criterion.*
5. **[Self-Harness Pareto] Put cost in the ledger.** Log per-hypothesis
   token/latency cost (observability already captures `latency_ms`); the agent /
   bandit then avoids expensive-low-gain tactics (e.g. k-sample voting that cost
   3× for −0.4). *Effort: one field. Yield: medium-high under an eval budget.*

Deliberately **out of the low-effort set** (higher effort or low yield at this
scale): evolutionary population / Pareto harness search (= beam / Mode B / fork);
held-in/held-out regression splitting (the eval is external and frozen); and
aggressive curator de-dup (context bloat is minor over ~8 generations — a single
"merge duplicates, cap at N" line is enough).

## Sources
- ACE — Agentic Context Engineering: https://arxiv.org/abs/2510.04618
- Lil'Log, "Harness Engineering for Self-Improvement" (Weng, 2026) — surveys ACE,
  MCE, Meta-Harness, Self-Harness, ADAS; the three harness patterns
  (execute-test-iterate, file system as memory, sub-agent spawning).
- Reading list: github.com/leezythu/Awesome-Harness-Self-Improvement
- Meta-Policy Reflexion (reusable memory + rule admissibility):
  https://arxiv.org/pdf/2509.03990
