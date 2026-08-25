r"""
================================================================================
PROTOCOL FOR THE IMPROVING AGENT — READ THIS ENTIRE DOCSTRING, EVERY GENERATION
================================================================================
You are SIA's feedback agent. SIA embeds this file *verbatim* in your prompt
every generation, so this docstring is the always-in-context contract — it works
with any implementation. A `GUIDANCE.md` in your working directory carries the
same protocol in more detail; agentic implementations that explore their working
dir (e.g. OpenHands) will find and follow it. If you see it, use it and keep it
consistent with this docstring.

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
  2. CHOOSE YOUR SEED CODE.
     If the generation you were handed scored BELOW the incumbent, do NOT edit
     the code you were given. Instead read the incumbent generation's file,
     `<run_dir>/gen_<M>/target_agent.py`, and improve THAT. If the handed
     generation IS the incumbent, continue from it. Never build on a regression.
  3. CHANGE ONE HYPOTHESIS.
     Pick exactly one edit family (see HYPOTHESIS FAMILIES). Keep the edit local
     to a single stage function (`load_dataset`, `plan`, `solve_one`,
     `format_submission`) so unrelated behavior cannot break.
  4. PRESERVE THE LOAD-BEARING PARTS. These make the next generation able to
     diagnose and select; deleting them blinds the loop:
       - the CLI contract: `--dataset_dir` / `--working_dir`;
       - `from sia_history import surface_incumbent` and the `TrajectoryLogger`
         calls — keep the instrumentation and the incumbent surfacing intact;
       - this docstring: keep it at the top of the file, updated but intact.
  5. RECORD IT IN `improvement.md` (the ledger SIA explicitly reads back — it
     folds prior `improvement.md` insights into `context.md`; always write this).
     If your tooling allows, ALSO append a line to `../ledger.jsonl` at the run
     root — a richer log the strategy bandit can use. SIA asks for "two files
     only," but that is a soft instruction it does not enforce, so the extra
     ledger persists. Use exactly this block so the history is machine- and
     judge-readable:

         ## Generation <N>
         - incumbent_gen: <M>
         - incumbent_score: <S>
         - seed_gen: <the gen whose code you actually edited>
         - hypothesis: <one of the HYPOTHESIS FAMILIES>
         - edit_summary: <one sentence: what you changed and where>
         - predicted_effect: <why it should raise the score>

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
(`sia_history`, `observability`) are re-copied pristine into every generation, so
the feedback agent cannot corrupt them.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

from observability import TrajectoryLogger  # pristine, re-copied each generation
from sia_history import surface_incumbent   # deterministic incumbent computation

# HANDOFF: set from the setup-kit credentials / provider. Left importable-safe.
MODEL = os.getenv("TASK_MODEL", "claude-haiku-4-5-20251001")


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
    return {"answer": text, "confidence": 1.0}


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
            results.append({**sample, **out})

    submission = format_submission(results)
    (Path(args.working_dir) / SUBMISSION_FILENAME).write_text(submission, encoding="utf-8")
    log.finalize(extra={"incumbent": incumbent})  # diagnostic + incumbent, always visible


if __name__ == "__main__":
    main()
