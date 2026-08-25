"""Triage judge — picks the deployment mode, algorithm, and hyperparameters for
the revealed challenge at competition time, then writes HANDOFF.md.

We do not guess the search strategy. We measure the environment (evaluation
cost, score noise, parallelizability, affordable generations) plus what the rules
allow (forkable? re-seeding permitted?), and read the decision off a table
(PLAN.md §6). Run this once, right after the challenge is revealed and preflight
passes.

The submitted artifact is always a stock `sia run` (Mode A) or a forked one
(Mode A+fork); Mode B (`orchestrate.py`) is *offline scouting only* and never the
submission, so `recommend(...)` always returns a submittable mode and separately
flags whether offline scouting is worthwhile.

The decision function `recommend(...)` is pure and unit-tested.
"""

from __future__ import annotations

import argparse
import statistics
from dataclasses import dataclass, field
from pathlib import Path

# Thresholds (tune during the practice challenge on Monday).
CHEAP_EVAL_SECONDS = 60.0        # below this, offline parallel scouting is affordable
HIGH_NOISE = 0.02                # score std (in metric units) above this = noisy
EDIT_SECONDS_EST = 90.0          # rough feedback-agent edit time per generation
BANDIT_MIN_GENS = 15             # enough pulls for a bandit over ~7 arms to learn


@dataclass
class Probe:
    eval_seconds: float
    noise_std: float
    parallel_ok: bool
    minutes_left: float
    forkable: bool = False          # repo is forkable AND the fork is submittable
    reseed_allowed: bool = False    # re-seeding a new run from an evolved agent is allowed
    baseline_score: float | None = None

    @property
    def affordable_gens(self) -> int:
        per_gen = self.eval_seconds + EDIT_SECONDS_EST
        return int((self.minutes_left * 60) / per_gen) if per_gen > 0 else 0


@dataclass
class Recommendation:
    mode: str               # "A" (in-loop docstring) or "A+fork" (deterministic)
    selector: str           # "greedy" | "annealed"
    with_bandit: bool
    noise_margin: float
    scout_offline: bool     # also run Mode B on our own machine to scout
    rationale: list[str] = field(default_factory=list)

    def command(self, task_dir: str = "<TASK_DIR>") -> str:
        run = (
            f"sia run --task_dir {task_dir} --max_gen 8 "
            f"--target-agent-profile target-buildnight "
            f"--meta-agent-profile meta-buildnight --run_id 1"
        )
        if self.mode == "A+fork":
            return (
                "git apply fork/orchestrator_incumbent_seed.patch   # in the SIA checkout\n"
                f"{run}"
            )
        return run  # Mode A: selection via the target_agent.py docstring protocol


def recommend(p: Probe) -> Recommendation:
    r: list[str] = []
    cheap = p.eval_seconds < CHEAP_EVAL_SECONDS
    noisy = p.noise_std > HIGH_NOISE
    r.append(f"eval≈{p.eval_seconds:.0f}s ({'cheap' if cheap else 'expensive'}), "
             f"noise_std={p.noise_std:.4f} ({'high' if noisy else 'low'}), "
             f"parallel={'yes' if p.parallel_ok else 'no'}, "
             f"forkable={'yes' if p.forkable else 'no'}, "
             f"affordable_gens≈{p.affordable_gens}")

    # Submittable mode: fork if we're allowed (deterministic enforcement), else
    # the injection-only docstring path.
    mode = "A+fork" if p.forkable else "A"
    if p.forkable:
        r.append("forkable & submittable → Mode A+fork (deterministic incumbent seeding)")
    else:
        r.append("stock sia run only → Mode A (docstring protocol; needs a capable meta model)")

    # Selector: annealing hedges noisy/deceptive landscapes; greedy otherwise.
    if noisy:
        selector, margin = "annealed", 1.5 * p.noise_std
        r.append("noisy scores → annealed selector + widened noise margin")
    else:
        selector, margin = "greedy", p.noise_std
        r.append("low noise → greedy hill-climb-with-revert")

    with_bandit = p.affordable_gens >= BANDIT_MIN_GENS
    if with_bandit:
        r.append(f"≥{BANDIT_MIN_GENS} affordable generations → layer the strategy bandit")

    # Mode B is offline scouting only — worthwhile when evals are cheap, parallel
    # runs are allowed, and re-seeding is permitted. Never the submission.
    scout_offline = cheap and p.parallel_ok and p.reseed_allowed
    if scout_offline:
        r.append("cheap + parallel + re-seed allowed → also scout offline (Mode B), fold wins into the seed")

    return Recommendation(mode, selector, with_bandit, margin, scout_offline, r)


def render_handoff(p: Probe, rec: Recommendation, task_dir: str, template: str) -> str:
    fill = {
        "MODE": rec.mode,
        "SELECTOR": rec.selector,
        "BANDIT": "yes" if rec.with_bandit else "no",
        "SCOUT_OFFLINE": "yes" if rec.scout_offline else "no",
        "NOISE_MARGIN": f"{rec.noise_margin:g}",
        "EVAL_SECONDS": f"{p.eval_seconds:.0f}",
        "NOISE_STD": f"{p.noise_std:.4f}",
        "PARALLEL": "yes" if p.parallel_ok else "no",
        "FORKABLE": "yes" if p.forkable else "no",
        "AFFORDABLE_GENS": str(p.affordable_gens),
        "COMMAND": rec.command(task_dir),
        "RATIONALE": "\n".join(f"- {line}" for line in rec.rationale),
    }
    out = template
    for k, v in fill.items():
        out = out.replace("{{" + k + "}}", v)
    return out


def main() -> None:
    ap = argparse.ArgumentParser(description="SIA Build Night triage judge")
    ap.add_argument("--eval-seconds", type=float, required=True,
                    help="observed seconds for one candidate evaluation")
    ap.add_argument("--scores", type=float, nargs="*", default=[],
                    help="two+ baseline scores from identical runs, to estimate noise")
    ap.add_argument("--noise-std", type=float, default=None,
                    help="override: score std directly")
    ap.add_argument("--parallel-ok", action="store_true")
    ap.add_argument("--forkable", action="store_true",
                    help="the SIA repo is forkable AND the fork is submittable")
    ap.add_argument("--reseed-allowed", action="store_true",
                    help="re-seeding a new run from an evolved agent is permitted (Mode B scouting)")
    ap.add_argument("--minutes-left", type=float, default=100.0)
    ap.add_argument("--task-dir", default="<TASK_DIR>")
    ap.add_argument("--out", default=str(Path(__file__).parent / "HANDOFF.md"))
    args = ap.parse_args()

    if args.noise_std is not None:
        noise = args.noise_std
    elif len(args.scores) >= 2:
        noise = statistics.pstdev(args.scores)
    else:
        noise = 0.0  # unknown → treat as low noise, greedy
    baseline = statistics.mean(args.scores) if args.scores else None

    probe = Probe(eval_seconds=args.eval_seconds, noise_std=noise,
                  parallel_ok=args.parallel_ok, minutes_left=args.minutes_left,
                  forkable=args.forkable, reseed_allowed=args.reseed_allowed,
                  baseline_score=baseline)
    rec = recommend(probe)

    tmpl_path = Path(__file__).parent / "HANDOFF.md.tmpl"
    template = tmpl_path.read_text(encoding="utf-8")
    out = render_handoff(probe, rec, args.task_dir, template)
    Path(args.out).write_text(out, encoding="utf-8")

    print(out)
    print(f"\n[triage] wrote {args.out}")


if __name__ == "__main__":
    main()
