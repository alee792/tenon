# SIA Build Night kit

Pre-built, challenge-agnostic scaffolding for Frontier Build Night (Hexo Labs
SIA). Branch-isolated experiment; not a Tenon product change.

**Read [`PLAN.md`](PLAN.md) first** — thesis, verified constraints, the three
deployment modes, the algorithms, the handoff judge, and the night-of runbook.

## Thesis in one line
SIA has a strong *generator* (an LLM proposing edits) and **no *selector*** — its
loop is a blind linear chain. We add the missing selector in the scaffold surface
SIA already exposes (no core edits required), pick the algorithm by *measuring*
the revealed environment, and surface the best agent it finds.

## Three modes, all pre-built (we don't know the night's scenario)
- **Mode A** — in-loop, injection-only: the selection protocol lives in
  `target_agent.py`'s module docstring (the one artifact SIA embeds verbatim
  every generation); a capable feedback model executes it. Submittable as a stock
  `sia run`.
- **Mode A+fork** — deterministic: a ~30-line `orchestrator.py` patch
  (`fork/`) provably seeds from the incumbent. Only if the repo is forkable.
- **Mode B** — offline scouting only (`kit/orchestrate.py`): drives many runs on
  our machine to find good hypotheses; never submitted.

## What's here
- `kit/seed_agent/` — the instrumented, modular seed agent SIA edits;
  `observability.py` (within-gen taxonomy: failure clusters + exemplars,
  confidence calibration, latency), `signals.py` (cross-generation memory:
  failure-delta vs. last gen, tried-family digest, prediction check, and the
  recommended next family), and `sia_history.py` (deterministic incumbent
  computation) — all re-copied pristine every generation; the **selection
  protocol lives in `target_agent.py`'s docstring**; `DESIGN.md` orients the meta
  agent at gen 1.
- `algorithms/` — three selectors behind one interface (beam hill climbing,
  simulated-annealing-lite, UCB strategy bandit): the reference implementation
  used by Mode B and the fork. Pure stdlib, unit-tested.
- `fork/` — `FORK_PATCH.md` + a ready-to-`git apply` patch for Mode A+fork.
- `kit/triage.py` + `HANDOFF.md.tmpl` — the judge that measures the environment
  and picks mode + algorithm + hyperparameters at competition time.
- `kit/prompts/` — P1–P4 handoff prompts (paste, don't compose, on the night).
- `kit/preflight.sh` — 5:00 PM environment checks.
- `tests/` — credential-free; no model or network.

## Verify locally
```sh
python -m pytest -q sia-buildnight/tests
python sia-buildnight/kit/triage.py --eval-seconds 12 --scores 0.40 0.42 \
  --parallel-ok --minutes-left 90 --task-dir tasks/demo
```

## Night-of, in short
`preflight.sh` → reveal → `P1_ingest` → `P2_wire_seed` → `P3_run_triage`
(→ `HANDOFF.md`) → launch → `P4_supervise` → freeze incumbent + assemble
submission. See PLAN.md §9.

## Terminology
**incumbent** = best-scoring agent so far (challengers compare to it). Not
"champion".
