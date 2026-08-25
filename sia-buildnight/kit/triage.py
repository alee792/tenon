"""Triage judge — picks the posture, algorithm, and hyperparameters for the
revealed challenge at competition time, then writes HANDOFF.md.

We do not guess the search strategy. We measure the environment (evaluation
cost, score noise, parallelizability, affordable generations) and read the
decision off a table (PLAN.md §6). Run this once, right after the challenge is
revealed and preflight passes.

Two modes:
  * measured  — you pass the probe numbers you observed from 1-2 baseline runs;
  * probe     — (optional) provide a callable that runs a baseline and returns
                (score, seconds); triage runs it twice to estimate noise.

The decision function `recommend(...)` is pure and unit-tested.
"""

from __future__ import annotations

import argparse
import statistics
from dataclasses import dataclass, field
from pathlib import Path

# Thresholds (tune during the practice challenge on Monday).
CHEAP_EVAL_SECONDS = 60.0        # below this, a beam of parallel runs is affordable
HIGH_NOISE = 0.02                # score std (in metric units) above this = noisy
EDIT_SECONDS_EST = 90.0          # rough feedback-agent edit time per generation
BANDIT_MIN_GENS = 15             # enough pulls for a bandit over ~7 arms to learn


@dataclass
class Probe:
    eval_seconds: float
    noise_std: float
    parallel_ok: bool
    minutes_left: float
    baseline_score: float | None = None

    @property
    def affordable_gens(self) -> int:
        per_gen = self.eval_seconds + EDIT_SECONDS_EST
        return int((self.minutes_left * 60) / per_gen) if per_gen > 0 else 0


@dataclass
class Recommendation:
    posture: str            # "A" (in-loop guidance) or "B" (outer driver)
    selector: str           # "greedy" | "beam-hill-climb" | "annealed"
    with_bandit: bool
    beam_width: int
    noise_margin: float
    rationale: list[str] = field(default_factory=list)

    def command(self, task_dir: str = "<TASK_DIR>") -> str:
        if self.posture == "B":
            b = " --bandit" if self.with_bandit else ""
            return (
                f"python kit/orchestrate.py --task_dir {task_dir} "
                f"--selector {self.selector} --beam-width {self.beam_width} "
                f"--noise-margin {self.noise_margin:g}{b}"
            )
        # Posture A: a single sia run; selection happens via GUIDANCE.md.
        return (
            f"sia run --task_dir {task_dir} --max_gen {max(6, 8)} "
            f"--target-agent-profile target-buildnight "
            f"--meta-agent-profile meta-buildnight --run_id 1"
        )


def recommend(p: Probe) -> Recommendation:
    r: list[str] = []
    cheap = p.eval_seconds < CHEAP_EVAL_SECONDS
    noisy = p.noise_std > HIGH_NOISE
    r.append(f"eval≈{p.eval_seconds:.0f}s ({'cheap' if cheap else 'expensive'}), "
             f"noise_std={p.noise_std:.4f} ({'high' if noisy else 'low'}), "
             f"parallel={'yes' if p.parallel_ok else 'no'}, "
             f"affordable_gens≈{p.affordable_gens}")

    with_bandit = p.affordable_gens >= BANDIT_MIN_GENS
    if with_bandit:
        r.append(f"≥{BANDIT_MIN_GENS} affordable generations → layer the strategy bandit")

    if cheap and p.parallel_ok:
        r.append("cheap + parallel → Posture B outer driver with a wide beam")
        return Recommendation("B", "beam-hill-climb", with_bandit,
                              beam_width=4, noise_margin=p.noise_std, rationale=r)
    if noisy:
        r.append("expensive + noisy → Posture A, annealed with a wide noise margin")
        return Recommendation("A", "annealed", with_bandit,
                              beam_width=1, noise_margin=1.5 * p.noise_std, rationale=r)
    r.append("expensive + low-noise → Posture A, greedy hill climb (beam=1)")
    return Recommendation("A", "greedy", with_bandit,
                          beam_width=1, noise_margin=p.noise_std, rationale=r)


def render_handoff(p: Probe, rec: Recommendation, task_dir: str, template: str) -> str:
    fill = {
        "POSTURE": rec.posture,
        "SELECTOR": rec.selector,
        "BANDIT": "yes" if rec.with_bandit else "no",
        "BEAM_WIDTH": str(rec.beam_width),
        "NOISE_MARGIN": f"{rec.noise_margin:g}",
        "EVAL_SECONDS": f"{p.eval_seconds:.0f}",
        "NOISE_STD": f"{p.noise_std:.4f}",
        "PARALLEL": "yes" if p.parallel_ok else "no",
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
