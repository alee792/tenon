"""Build Night seed target agent — modular and instrumented.

This is the seed SIA's meta/feedback agent studies and iteratively edits. It is
deliberately structured into small, single-responsibility stages so the feedback
agent can change ONE stage without breaking unrelated behavior:

    load_dataset -> plan -> solve_one(sample) -> format_submission -> write

Only the task-shaped glue marked `# HANDOFF:` is filled in on the night, from the
revealed task.md / evaluate.py / dataset. Everything else — the CLI contract, the
structured logging, the incumbent/ledger awareness — is generic and stays intact.

Runtime deps (anthropic etc.) belong here, never in the algorithms/ library.
Declare them in this directory's requirements.txt so SIA installs them.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

from observability import TrajectoryLogger  # copied alongside this file each gen

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
    primary surface the SIA loop will iterate on (prompting, voting, retries,
    verification — see kit/guidance/GUIDANCE.md hypothesis classes).
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
    log.finalize()  # writes diagnostic + prints summary to stdout tail


if __name__ == "__main__":
    main()
