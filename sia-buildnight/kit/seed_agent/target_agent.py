r"""
================================================================================
PROTOCOL v1 — FOR THE IMPROVING AGENT — READ THIS ENTIRELY, EVERY GENERATION
================================================================================
You are SIA's feedback agent. SIA embeds this file *verbatim* in your prompt
every generation, so this docstring is the always-in-context contract — it works
with any implementation. A `GUIDANCE.md` in your working directory carries the
same protocol in more detail; agentic implementations that explore their working
dir (e.g. OpenHands) will find and follow it. If you see it, use it and keep it
consistent with this docstring.

METHOD vs. CONTENT — DO NOT CONFLATE THEM. This docstring is the *method* (how
you improve). The task code below the FROZEN/EDITABLE banner and the playbook are
the *content* (what you improve). **Never rewrite this protocol while editing task
code.** If you genuinely improve the method itself, bump the version (PROTOCOL v2)
and say why in `improvement.md` — otherwise leave it byte-for-byte intact.

WHY THIS EXISTS
SIA hands you the PREVIOUS generation's agent and asks you to improve it. But
the previous generation may have REGRESSED. If you always build on it, one bad
edit poisons every generation after. Your job each generation is to branch from
the best agent so far (the "incumbent"), not blindly from the last one — turning
SIA's linear chain into a hill climb that never steps down.

THE SELECTION PROTOCOL (do these in order, every generation)
  1. FIND THE INCUMBENT.
     a. First look at the EXECUTION LOGS you were given: the diagnostic block
        (stdout `=== DIAGNOSTIC SUMMARY ===` and the file
        `agent_execution/execution_q-diagnostic.json`) contains an
        `incumbent` field computed deterministically at runtime — use it when
        present. It names the best prior generation and its score.
     b. If it is absent (the run was sandboxed and could not see siblings),
        READ `context.md` in the run directory: SIA writes every generation's
        score there. The incumbent is the generation with the highest score.
  2. READ THE SIGNALS (all in the same diagnostic file/stdout block).
     These are computed deterministically each generation — do not re-derive
     them by eye from the trajectories.
       - `cross_gen.failure_delta` — what your LAST edit changed in the failure
         profile: `new_failure_classes` a regression introduced, and
         `cleared_failure_classes` it fixed. A non-empty `new_failure_classes`
         is the smoking gun of a regressive edit.
       - `cross_gen.prediction_check` — whether last generation's
         `predicted_effect` actually happened (`held`). If a change was
         predicted to help and `actual_delta` came back <= 0, that family did
         NOT work here; revert and pick a different one.
       - `clusters` — concrete failing (expected, got) exemplars grouped by
         cause. Fix what these SHOW, not what the counts merely suggest.
       - `confidence` / `latency` — if `confidence.degenerate` is true, wire
         `solve_one` to return a real confidence first (a blind loop can't target
         retries/voting without it); `latency.over_budget` flags efficiency debt.
  3. CHOOSE YOUR SEED CODE.
     If the generation you were handed scored BELOW the incumbent, do NOT edit
     the code you were given. Instead read the incumbent generation's file,
     `<run_dir>/gen_<M>/target_agent.py`, and improve THAT. If the handed
     generation IS the incumbent, continue from it. Never build on a regression.
  4. CHOOSE ONE HYPOTHESIS — AND DON'T REPEAT A KNOWN FAILURE.
     Start from `recommended_hypothesis.family` in the diagnostic: it is computed
     from your crash profile and already EXCLUDES families in `cross_gen.tried`
     that were tried without payoff, so you explore instead of repeating a dud.
     Cross-check it against the playbook (prior `improvement.md` blocks and, if
     present, `PLAYBOOK.md`): **skip any hypothesis tagged REJECTED**, and prefer
     a family that earned a VALIDATED gain or is untried against the worst stage.
     Override the recommendation only when `failure_delta` / `clusters` point
     somewhere more specific. Pick exactly one family (see HYPOTHESIS FAMILIES)
     and keep the edit local to a single stage so unrelated behavior cannot break.
     Weigh gain against COST (tokens/latency in the diagnostic): don't keep an
     expensive tactic that buys little.
     NOTE: the taxonomy only sees crashes and self-flagged bad output — NOT
     answers that are merely wrong. If crashes are near zero but the score is
     flat, your failures are SEMANTIC: reach for a reasoning family
     (self-consistency-voting, decompose-reasoning, add-verification) — which is
     exactly what `recommended_hypothesis` escalates to in that case.
  5. EDIT ONLY THE EDITABLE REGION; PRESERVE THE FROZEN PARTS. Make your one
     change strictly between the `EDITABLE REGION START` / `EDITABLE REGION END`
     markers below (the task-strategy stages). Everything outside them is
     load-bearing and FROZEN: this docstring, the imports, and `main()` — the CLI
     contract (`--dataset_dir` / `--working_dir`), the `surface_incumbent` call,
     the `signals.gather(...)` call, and the `TrajectoryLogger` calls
     (instrumentation + incumbent surfacing). Deleting the frozen parts blinds the
     next generation.
  6. RECORD IT AS A PLAYBOOK — ITEMIZED, CARRIED FORWARD, DELTA-UPDATED.
     Always write `improvement.md` (SIA reads it back and folds it into
     `context.md`; `signals.py` also parses this block to compute the tried-digest
     and the prediction-check). Do NOT re-write it as fresh prose each generation —
     that erodes detail over time. Maintain a growing, itemized playbook: carry
     prior items forward verbatim and only ADD or UPDATE the ones that changed. If
     your tooling allows, mirror the items to `../ledger.jsonl` at the run root
     (SIA's "two files only" is soft and unenforced, so it persists).

     Write this per-generation block (keep `hypothesis` and `predicted_effect`
     verbatim — `signals.py` reads them back):

         ## Generation <N>
         - incumbent_gen: <M>   incumbent_score: <S>   this_score: <T or pending>
         - seed_gen: <the gen whose code you edited>
         - hypothesis: <one HYPOTHESIS FAMILY>
         - edit_summary: <one sentence: what + where>
         - predicted_effect: <why it should raise the score — checked next gen>
         - evidence: <worst stage / error class from the diagnostic>
         - cost: <total_tokens / total_latency_ms from the diagnostic>

     And UPDATE the carried playbook (append new items, re-tag existing ones):

         ## Playbook (carried forward — edit deltas only)
         - [T-003 | harden-output-parsing | VALIDATED +3.1] strip trailing
           punctuation before the answer regex; fixed 42% of parse failures.
         - [T-007 | self-consistency-voting | REJECTED -0.4, 3x tokens] no gain;
           do not retry unless eval is cheap.

     Tag an item VALIDATED only after a measured score gain; tag it REJECTED if it
     regressed (and never retry it); keep the specific detail (which failure,
     which pattern) — do not generalize into vague principles.

HYPOTHESIS FAMILIES (change exactly one per generation)
  harden-output-parsing   make answer extraction / format_submission robust to
                          malformed model output
  self-consistency-voting sample solve_one k times, take a majority / best
  retry-on-malformed      detect a bad sample result and retry it once
  restructure-prompt      rewrite the task-model prompt in solve_one
  decompose-reasoning     split solve_one into explicit sub-steps
  improve-retrieval       better select / order the context given to solve_one
  add-verification        a check pass that validates answers before writing

NEVER
  - Never hard-code answers or fit to specific samples. The score gain must come
    from a general capability improvement produced by this loop.
  - Never touch evaluate.py, the dataset, or the ground truth.
  - Never delete or weaken the logging or the incumbent surfacing.

--------------------------------------------------------------------------------
IMPLEMENTATION NOTES (for the human wiring this up on the night)
This seed is deliberately split into small single-responsibility stages so the
feedback agent can change ONE stage without breaking the rest:

    load_dataset -> plan -> solve_one(sample) -> format_submission -> write

Only the task-shaped glue marked `# HANDOFF:` is filled in on the night, from the
revealed task.md / evaluate.py / dataset. Runtime deps (anthropic, …) go in this
directory's requirements.txt so SIA installs them; the pure-stdlib helpers
(`sia_history`, `observability`, `signals`) are re-copied pristine into every
generation, so the feedback agent cannot corrupt them.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

import signals                              # pristine: cross-generation signals
from observability import TrajectoryLogger  # pristine, re-copied each generation
from sia_history import surface_incumbent   # deterministic incumbent computation

# HANDOFF: set from the setup-kit credentials / provider. Left importable-safe.
MODEL = os.getenv("TASK_MODEL", "claude-haiku-4-5-20251001")


# ============================================================================= #
# EDITABLE REGION START — task strategy only. Make your ONE change per generation
# somewhere between this marker and EDITABLE REGION END. Everything outside is
# frozen (see the protocol docstring, step 4).
# ============================================================================= #


# --------------------------------------------------------------------------- #
# Task-model client seam — the one obvious hook for the improvement agent.
# --------------------------------------------------------------------------- #
def make_client():
    """Return the task-model client. HANDOFF: match the provider in the profile.

    Kept in one function so the feedback agent can swap providers / params in a
    single, local edit.
    """
    import anthropic  # imported lazily so `--help` and imports work without keys

    return anthropic.Anthropic()


def solve_one(client, sample: dict) -> dict:
    """Solve a single sample. Returns {"answer": ..., "confidence": float}.

    HANDOFF: fill the prompt and parsing for the revealed task. This is the
    primary surface the SIA loop iterates on (prompting, voting, retries,
    verification — see the HYPOTHESIS FAMILIES in the module docstring).
    """
    # HANDOFF: replace with the real task prompt + output parsing.
    prompt = f"Solve the following.\n\n{json.dumps(sample)[:4000]}\n\nAnswer:"
    resp = client.messages.create(
        model=MODEL,
        max_tokens=1024,
        messages=[{"role": "user", "content": prompt}],
    )
    text = "".join(getattr(b, "text", "") for b in resp.content).strip()
    usage = getattr(resp, "usage", None)  # cost signal for the Pareto view
    tokens = (getattr(usage, "input_tokens", 0) + getattr(usage, "output_tokens", 0)) if usage else None
    # HANDOFF: return a REAL confidence, not this constant. Without it the
    # diagnostic flags `confidence.degenerate` and the loop cannot target
    # retries / self-consistency voting. Wire it to logprobs, a self-reported
    # certainty, or vote agreement once solve_one does real work.
    return {"answer": text, "confidence": 1.0, "tokens": tokens}


# --------------------------------------------------------------------------- #
# Generic stages — keep these boundaries stable across generations.
# --------------------------------------------------------------------------- #
def load_dataset(dataset_dir: str) -> list[dict]:
    """HANDOFF: read the revealed dataset file(s) from dataset_dir.

    Return a list of sample dicts. The default probes common filenames so a
    gen-1 run does something even before the handoff wiring lands.
    """
    d = Path(dataset_dir)
    for name in ("test.json", "questions.json", "diamond_qna.json", "data.json"):
        p = d / name
        if p.exists():
            obj = json.loads(p.read_text(encoding="utf-8"))
            return obj if isinstance(obj, list) else obj.get("data", [])
    for p in sorted(d.glob("*.jsonl")):
        return [json.loads(line) for line in p.read_text(encoding="utf-8").splitlines() if line.strip()]
    raise FileNotFoundError(f"HANDOFF: point load_dataset at the real file in {dataset_dir}")


def plan(samples: list[dict]) -> dict:
    """Optional global planning step (budgeting, ordering). Generic no-op seed."""
    return {"n": len(samples)}


def format_submission(results: list[dict]) -> str:
    """HANDOFF: serialize predictions to the exact format evaluate.py expects.

    Default emits JSON lines of {id, prediction}; the build agent adjusts to the
    submission contract read from evaluate.py in P1_ingest.
    """
    return "\n".join(
        json.dumps({"id": r.get("id", i), "prediction": r.get("answer")})
        for i, r in enumerate(results)
    )


SUBMISSION_FILENAME = "submission.jsonl"  # HANDOFF: match evaluate.py's expectation


# ============================================================================= #
# EDITABLE REGION END — everything below is FROZEN instrumentation & wiring.
# Do not edit main(): the CLI contract, incumbent surfacing, and logging live here
# and the next generation depends on them.
# ============================================================================= #


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dataset_dir", required=True)
    ap.add_argument("--working_dir", required=True)
    args = ap.parse_args()

    log = TrajectoryLogger(args.working_dir)

    # Deterministic: compute the incumbent from prior generations' results.json
    # (when this run can see sibling gen dirs) so the feedback agent is handed
    # the best-so-far generation instead of having to derive it. Returns None
    # under sandboxing / at gen 1; that path is handled by the protocol above.
    incumbent = surface_incumbent(args.working_dir)

    samples = load_dataset(args.dataset_dir)
    plan(samples)

    client = make_client()
    results: list[dict] = []
    for i, sample in enumerate(samples):
        with log.sample(i, stage="solve") as rec:
            out = solve_one(client, sample)
            rec["got"] = out.get("answer")
            rec["confidence"] = out.get("confidence")
            rec["tokens"] = out.get("tokens")  # cost signal, if solve_one reports it
            results.append({**sample, **out})

    submission = format_submission(results)
    (Path(args.working_dir) / SUBMISSION_FILENAME).write_text(submission, encoding="utf-8")

    # Cross-generation signals: what the last edit changed (failure_delta),
    # whether its prediction held, which families are spent, and the family to
    # try next. Deterministic bookkeeping over sibling gen dirs; degrades to
    # empty under sandboxing. Merged into the always-visible diagnostic.
    summary = log.summary()
    cross = signals.gather(args.working_dir, summary, incumbent)
    log.finalize(extra={"incumbent": incumbent, **cross})


if __name__ == "__main__":
    main()
