# Improvement guidance (read this first, every generation)

You are the SIA feedback agent improving `target_agent.py`. This file rides in
your working directory each generation. Follow it exactly — it encodes the
selection rule that turns SIA's blind linear chain into hill climbing.

## Why this matters

SIA hands you the *previous* generation's agent and asks you to improve it. But
the previous generation may have **regressed**. If you always build on it, one
bad edit poisons every generation after it. So before you edit, you reconstruct
the **incumbent** (the best agent so far) and branch from *that*.

## The rule (do this in order, every generation)

1. **Reconstruct the incumbent.** List the sibling directories `../gen_*/`. For
   each, read `results.json` and take the primary metric (e.g. `accuracy`;
   percentage strings like `"48.99%"` are numbers). The **incumbent** is the
   generation with the highest score. Its `target_agent.py` is the incumbent
   code.
2. **Choose your seed.** If the generation you were handed scored *below* the
   incumbent, **start from the incumbent's `target_agent.py`**, not the one you
   were given. If it *is* the incumbent, continue from it. Never build on a
   regression.
3. **Pick one hypothesis.** Change exactly one edit family this generation
   (list below). Keep the edit local to one stage (`load_dataset`, `plan`,
   `solve_one`, `format_submission`) so unrelated behavior can't break.
4. **Write the improved `target_agent.py`** into your working directory. Keep
   the CLI contract (`--dataset_dir`, `--working_dir`), the `TrajectoryLogger`
   calls, and the stage boundaries intact.
5. **Record your reasoning.** Write `improvement.md` in your working directory
   stating: the incumbent gen and score, the seed you chose, the one hypothesis,
   the exact edit, and your predicted effect. Append one JSON line to
   `../ledger.jsonl` (at the run root, not your gen dir):
   `{"gen": N, "hypothesis": "...", "edit_summary": "...", "seed_gen": M}`.

## Read the evidence before choosing a hypothesis

The `agent_execution/` folder holds per-sample traces. Read
`execution_q-diagnostic.json` first — it aggregates failures by stage and error
class and names the worst stage. Also read the incumbent's stdout tail
(`=== DIAGNOSTIC SUMMARY ===`). Let the worst stage pick your hypothesis.

## Hypothesis families (change one per generation)

- `harden-output-parsing` — make `format_submission` / answer extraction robust
  to malformed model output.
- `self-consistency-voting` — sample `solve_one` k times, take majority/best.
- `retry-on-malformed` — detect a bad sample result and retry it once.
- `restructure-prompt` — rewrite the task-model prompt in `solve_one`.
- `decompose-reasoning` — split `solve_one` into explicit sub-steps.
- `improve-retrieval` — better select/order the context given to `solve_one`.
- `add-verification` — a check pass that validates answers before writing.

## Never

- Never hard-code answers or fit to specific samples — the gain must come from a
  general capability improvement, produced by this loop.
- Never touch `evaluate.py`, dataset files, or the ground truth.
- Never delete or weaken the logging — it is how the next generation diagnoses.
