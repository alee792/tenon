# SIA Build Night kit

Pre-built, challenge-agnostic scaffolding for Frontier Build Night (Hexo Labs
SIA). Branch-isolated experiment; not a Tenon product change.

**Read [`PLAN.md`](PLAN.md) first** — thesis, verified constraints, the two
postures, the three algorithms, the handoff judge, and the night-of runbook.

## Thesis in one line
SIA has a strong *generator* (an LLM proposing edits) and **no *selector*** — its
loop is a blind linear chain. We add the missing selector entirely in the
scaffold surface SIA already exposes (no core edits), pick the algorithm by
*measuring* the revealed environment, and surface the best agent it finds.

## What's here
- `algorithms/` — three interchangeable selectors behind one interface: beam
  hill climbing (primary), simulated-annealing-lite (local-optima hedge), UCB
  strategy bandit (meta-layer). Pure stdlib, unit-tested.
- `kit/seed_agent/` — the instrumented, modular seed agent SIA edits; its
  observability layer; and `GUIDANCE.md`, the selection rule the feedback agent
  executes (Posture A).
- `kit/orchestrate.py` — outer driver that runs the incumbent loop over `sia run`
  (Posture B).
- `kit/triage.py` + `HANDOFF.md.tmpl` — the judge that measures the environment
  and picks posture + algorithm + hyperparameters at competition time.
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
