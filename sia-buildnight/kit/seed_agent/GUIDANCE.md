# Selection guidance (feedback agent) — the file-based carrier

> **When this file is used.** SIA copies this directory into your working
> directory every generation, but the feedback *prompt* does not point at it.
> Agentic implementations that explore their working directory (e.g. OpenHands,
> and capable models) discover and follow it; minimal implementations do not.
> For that reason the **same protocol also lives in the `target_agent.py` module
> docstring**, which is always in-context — that docstring is the floor, this
> file is the richer carrier. Keep them in sync.
>
> SIA's prompt asks you to "create exactly two files." That is a soft
> instruction, not a harness rule — nothing deletes or rejects extra files, so an
> agentic impl may also maintain the `ledger.jsonl` below. Do so when your tooling
> allows; otherwise fall back to the `improvement.md` ledger the prompt sanctions.

## Why this matters

SIA hands you the *previous* generation's agent and asks you to improve it. But
the previous generation may have **regressed**. If you always build on it, one
bad edit poisons every generation after. So before you edit, find the
**incumbent** (the best agent so far) and branch from *that* — turning SIA's
blind linear chain into a hill climb that never steps down.

## The rule (do this in order, every generation)

1. **Find the incumbent.** Prefer the deterministic `incumbent` field in the
   diagnostic (`agent_execution/execution_q-diagnostic.json` and the stdout
   `INCUMBENT:` line, computed by `sia_history.py`). If it is absent (sandboxed
   run), read `context.md` in the run directory — SIA records every generation's
   score there — and take the highest.
2. **Choose your seed.** If the generation you were handed scored *below* the
   incumbent, start from the incumbent's `../gen_<M>/target_agent.py`, not the one
   you were given. If it *is* the incumbent, continue from it. Never build on a
   regression.
3. **Pick one hypothesis.** Change exactly one edit family (below). Keep the edit
   local to one stage (`load_dataset`, `plan`, `solve_one`, `format_submission`)
   so unrelated behavior cannot break.
4. **Preserve the load-bearing parts.** Keep the CLI contract
   (`--dataset_dir`, `--working_dir`), the `TrajectoryLogger` and
   `surface_incumbent` calls, and the `target_agent.py` docstring intact.
5. **Record it.** Always write an `improvement.md` block (the sanctioned ledger):

       ## Generation <N>
       - incumbent_gen: <M>
       - incumbent_score: <S>
       - seed_gen: <the gen whose code you actually edited>
       - hypothesis: <one family below>
       - edit_summary: <one sentence: what and where>
       - predicted_effect: <why it should raise the score>

   If your tooling allows, also append one JSON line to `../ledger.jsonl` at the
   run root: `{"gen": N, "hypothesis": "...", "edit_summary": "...",
   "seed_gen": M, "incumbent_score": S}`. This richer log powers the strategy
   bandit; it is optional and impl-dependent.

## Hypothesis families (change one per generation)

- `harden-output-parsing` — make answer extraction / `format_submission` robust
  to malformed model output.
- `self-consistency-voting` — sample `solve_one` k times, take majority / best.
- `retry-on-malformed` — detect a bad sample result and retry it once.
- `restructure-prompt` — rewrite the task-model prompt in `solve_one`.
- `decompose-reasoning` — split `solve_one` into explicit sub-steps.
- `improve-retrieval` — better select / order the context given to `solve_one`.
- `add-verification` — a check pass that validates answers before writing.

## Never

- Never hard-code answers or fit specific samples — the gain must be a general
  capability improvement produced by this loop.
- Never touch `evaluate.py`, dataset files, or the ground truth.
- Never delete or weaken the logging or the incumbent surfacing.
