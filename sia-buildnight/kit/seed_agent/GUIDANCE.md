# Selection & reflection guidance (feedback agent) — PROTOCOL v1

> **When this file is used.** SIA copies this directory into your working
> directory every generation, but the feedback *prompt* does not point at it.
> Agentic implementations that explore their working directory (e.g. OpenHands,
> and capable models) discover and follow it; minimal implementations do not.
> The same protocol also lives in the `target_agent.py` module docstring, which is
> always in-context — that docstring is the floor, this file is the richer
> carrier. **Keep them in sync, and keep the version number matched.**
>
> SIA's prompt asks you to "create exactly two files." That is a soft
> instruction, not a harness rule — nothing deletes or rejects extra files, so an
> agentic impl may also maintain the `ledger.jsonl` below.

## Method vs. content — do not conflate them (MCE)

This protocol is the **method** (how you improve). The task code between the
`EDITABLE REGION` markers in `target_agent.py` and the playbook are the
**content** (what you improve). **Never rewrite this protocol while editing task
code.** If you truly improve the method, bump the version (PROTOCOL v2) and note
why in `improvement.md`; otherwise leave it intact. This keeps "how I improve"
from degrading every time you edit "what I produced."

## Why selection matters

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
3. **Pick one hypothesis — and don't repeat a known failure.** First read the
   playbook (prior `improvement.md` blocks and, if present, `PLAYBOOK.md`). **Skip
   any item tagged REJECTED** — do not re-try an edit that already regressed.
   Prefer families that earned a VALIDATED gain, or an untried family that targets
   the worst stage in the diagnostic. Weigh gain against **COST** (the
   `total_tokens` / latency in the diagnostic): don't keep an expensive tactic
   that buys little. Change exactly one family, local to one stage.
4. **Edit only the EDITABLE region.** Make your one change strictly between the
   `EDITABLE REGION START` / `EDITABLE REGION END` markers in `target_agent.py`.
   Everything outside — the docstring, imports, and `main()` (CLI contract,
   `surface_incumbent`, `TrajectoryLogger`) — is FROZEN. Keep it intact.
5. **Record it as an itemized playbook — carried forward, delta-updated.** Always
   write `improvement.md` (SIA folds it into `context.md`). Do **not** re-write it
   as fresh prose each generation — that erodes detail. Carry prior items forward
   verbatim; only ADD or UPDATE what changed. If your tooling allows, mirror the
   items to `../ledger.jsonl` at the run root.

   Per-generation block:

       ## Generation <N>
       - incumbent_gen: <M>   incumbent_score: <S>   this_score: <T or pending>
       - seed_gen: <the gen whose code you edited>
       - hypothesis: <one family below>
       - edit_summary: <one sentence: what + where>
       - evidence: <worst stage / error class from the diagnostic>
       - cost: <total_tokens / total_latency_ms from the diagnostic>

   Carried playbook (tagged items with IDs — edit deltas only):

       ## Playbook (carried forward — edit deltas only)
       - [T-003 | harden-output-parsing | VALIDATED +3.1] strip trailing
         punctuation before the answer regex; fixed 42% of parse failures.
       - [T-007 | self-consistency-voting | REJECTED -0.4, 3x tokens] no gain;
         do not retry unless eval is cheap.

   Tag VALIDATED only after a measured score gain; tag REJECTED if it regressed
   (and never retry it); keep the specific detail — do not generalize into vague
   principles.

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
