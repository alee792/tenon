"""Outer driver — Posture B.

Wraps repeated `sia run` invocations with an incumbent-and-ledger loop SIA
itself lacks: seed each round from the best-so-far agent, evaluate a beam of
candidates, promote only genuine improvements, repeat until the time budget.

Zero SIA core edits — this only calls the `sia run` CLI and reads its run
directories. The selection logic (`evaluate_round`) is pure and unit-tested
against a fake `sia run`; the subprocess launcher is isolated in `run_sia` so
tests monkeypatch it exactly as SIA's own tests do.

Use only if the challenge leads confirm re-seeding a new run from an evolved
agent is allowed (PLAN.md §8). Otherwise use Posture A (guidance-driven).
"""

from __future__ import annotations

import argparse
import shutil
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from algorithms import (  # noqa: E402
    Candidate,
    IncumbentRecord,
    IncumbentStore,
    Ledger,
    LedgerEntry,
    Selector,
    make_selector,
    read_score_from_gen,
)

SEED_DIR = Path(__file__).resolve().parent / "seed_agent"
INCUMBENT_AGENT = "target_agent.py"


# --------------------------------------------------------------------------- #
# Side-effecting seam — patched in tests.
# --------------------------------------------------------------------------- #
def run_sia(task_dir: str, run_id: int, max_gen: int, runs_root: Path,
            target_profile: str, meta_profile: str) -> Path:
    """Invoke `sia run` once; return the run directory it produced."""
    cmd = [
        "sia", "run",
        "--task_dir", task_dir,
        "--run_id", str(run_id),
        "--max_gen", str(max_gen),
        "--target-agent-profile", target_profile,
        "--meta-agent-profile", meta_profile,
        "--no-web",
    ]
    subprocess.run(cmd, check=False)
    return runs_root / f"run_{run_id}"


def best_gen_in_run(run_dir: Path, metric: str) -> tuple[Path, float | None]:
    """Best-scoring generation directory within one run."""
    best_dir, best_score = None, None
    for gen_dir in sorted(run_dir.glob("gen_*")):
        score = read_score_from_gen(gen_dir, metric)
        if score is not None and (best_score is None or score > best_score):
            best_dir, best_score = gen_dir, score
    return best_dir, best_score


# --------------------------------------------------------------------------- #
# Pure selection core — unit-tested.
# --------------------------------------------------------------------------- #
def evaluate_round(
    candidates: list[Candidate],
    selector: Selector,
    incumbent_store: IncumbentStore,
    ledger: Ledger,
    workspace: Path,
) -> IncumbentRecord | None:
    """Given this round's evaluated candidates, promote the incumbent if one
    beats it, and record every candidate to the ledger. Returns the incumbent."""
    incumbent = incumbent_store.get()

    # Steepest ascent: consider the best candidate for promotion, but log all.
    best = selector.best(candidates)
    for c in candidates:
        accepted = c is best and selector.accept(c, incumbent)
        ledger.append(LedgerEntry.from_candidate(
            c, incumbent.score if incumbent else None, accepted))
        if accepted:
            # Persist the winning agent as the new incumbent code.
            dest = workspace / "incumbent_agent.py"
            if c.agent_path and Path(c.agent_path).exists():
                shutil.copy2(c.agent_path, dest)
            incumbent = IncumbentRecord(
                gen=c.gen, score=c.score, metric=c.metric, agent_path=str(dest))
            incumbent_store.set(incumbent)
    return incumbent


def seed_incumbent_into_reference(incumbent: IncumbentRecord | None) -> None:
    """Copy the incumbent's code into the seed dir so the next `sia run` starts
    from it. No-op on cold start (generation 1 uses the pristine seed)."""
    if incumbent and incumbent.agent_path and Path(incumbent.agent_path).exists():
        shutil.copy2(incumbent.agent_path, SEED_DIR / INCUMBENT_AGENT)


# --------------------------------------------------------------------------- #
# Loop
# --------------------------------------------------------------------------- #
def drive(args) -> None:
    workspace = Path(args.workspace)
    workspace.mkdir(parents=True, exist_ok=True)
    runs_root = Path(args.runs_root)
    incumbent_store = IncumbentStore(workspace / "incumbent.json")
    ledger = Ledger(workspace / "ledger.jsonl")
    selector = make_selector(
        args.selector, noise_margin=args.noise_margin,
        beam_width=args.beam_width, with_bandit=args.bandit)

    deadline = time.time() + args.minutes * 60
    run_id = args.start_run_id
    rnd = 0
    while time.time() < deadline and rnd < args.max_rounds:
        rnd += 1
        seed_incumbent_into_reference(incumbent_store.get())
        candidates: list[Candidate] = []
        # A beam: `beam_width` independent sia runs seeded from the incumbent.
        for _ in range(args.beam_width):
            run_dir = run_sia(args.task_dir, run_id, args.max_gen, runs_root,
                              args.target_profile, args.meta_profile)
            gen_dir, score = best_gen_in_run(run_dir, args.metric)
            if gen_dir is not None:
                candidates.append(Candidate(
                    gen=run_id, agent_path=str(gen_dir / "target_agent.py"),
                    score=score, metric=args.metric))
            run_id += 1
        incumbent = evaluate_round(candidates, selector, incumbent_store, ledger, workspace)
        best_score = incumbent.score if incumbent else None
        print(f"[round {rnd}] incumbent score = {best_score}")

    final = incumbent_store.get()
    print(f"Done. Incumbent: gen {final.gen if final else '-'} "
          f"score {final.score if final else '-'} at {final.agent_path if final else '-'}")


def build_argparser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(description="SIA Build Night outer driver (Posture B)")
    ap.add_argument("--task_dir", required=True)
    ap.add_argument("--workspace", default="./buildnight_workspace")
    ap.add_argument("--runs_root", default="./runs")
    ap.add_argument("--target-profile", dest="target_profile", default="target-buildnight")
    ap.add_argument("--meta-profile", dest="meta_profile", default="meta-buildnight")
    ap.add_argument("--selector", default="beam-hill-climb",
                    choices=["beam-hill-climb", "greedy", "annealed"])
    ap.add_argument("--bandit", action="store_true", help="wrap in the strategy bandit")
    ap.add_argument("--beam-width", dest="beam_width", type=int, default=2)
    ap.add_argument("--noise-margin", dest="noise_margin", type=float, default=0.0)
    ap.add_argument("--max-gen", dest="max_gen", type=int, default=2)
    ap.add_argument("--minutes", type=float, default=60.0)
    ap.add_argument("--max-rounds", dest="max_rounds", type=int, default=100)
    ap.add_argument("--start-run-id", dest="start_run_id", type=int, default=1)
    ap.add_argument("--metric", default="accuracy")
    return ap


if __name__ == "__main__":
    drive(build_argparser().parse_args())
