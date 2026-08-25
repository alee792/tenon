# What we changed in SIA — and why

A concise account of our modifications, framed against the harness-engineering
literature. Companion to the machine-readable `run-report.json` that every
improvement run emits (see `kit/report.py`).

## The central insight

**SIA has a strong *generator* and no *selector*.** Its loop derives generation
*N+1* from generation *N*'s code regardless of score (`orchestrator.py:576-580`),
so a regression propagates and the best agent it finds is never the one it
reports (`context_manager.py:276-289` computes "best" for the summary only). Every
change below adds the missing selector and makes the loop *diagnosable*, and we do
it **inside the scaffold surface SIA already exposes — no core edits required**.

## The modifications

| Change | Grounded in | Why it helps |
| --- | --- | --- |
| **Incumbent hill-climb with revert** — branch each generation from the best-so-far agent, never a regression | our diagnosis; STOP-style scaffolding self-improvement | supplies the absent selector; turns a blind linear chain into monotone hill-climbing |
| **Deterministic incumbent surfacing** — compute the incumbent from sibling `results.json`, hand it to the feedback agent | — (our mechanism) | removes the least-reliable step from the model; falls back to `context.md` when sandboxed |
| **Method/content separation** — freeze + version the protocol (`PROTOCOL v1`) apart from the evolving task code | **MCE — Meta Context Engineering** | keeps "how we improve" from degrading each time the agent edits "what we produce" |
| **Itemized delta playbook** — carry a tagged, ID'd playbook forward with delta updates instead of re-writing prose | **ACE — Agentic Context Engineering** ([arXiv 2510.04618](https://arxiv.org/abs/2510.04618)) | avoids *context collapse* and *brevity bias* in the carried memory |
| **Rule admissibility / don't-repeat-rejected** — skip `REJECTED` hypotheses; promote `VALIDATED` only after a measured gain | **Reflexion**; **Meta-Policy Reflexion** ([arXiv 2509.03990](https://arxiv.org/pdf/2509.03990)) | stops the loop wasting generations on known failures |
| **Editable-region markers** — explicit `FROZEN`/`EDITABLE` boundaries in the agent | **STOP** (self-improving scaffolding) | precise changes without breaking unrelated behavior |
| **Cost-aware selection** — log per-hypothesis tokens/latency; weigh gain vs. cost | **Self-Harness** (Pareto accuracy/cost) | avoids expensive-low-gain tactics under an eval budget |
| **Environment-measured algorithm choice** — a triage step measures eval cost, noise, parallelism, then picks selector + mode | — (our mechanism) | we don't *guess* the search strategy; we *measure* and select |
| **Optional deterministic fork** — a ~30-line `orchestrator.py` patch for provable incumbent seeding | **Meta-Harness** (harness as editable files) | deterministic enforcement when forking is permitted |

The harness-as-optimization-target framing follows Weng, *Harness Engineering for
Self-Improvement* (Lil'Log, 2026), which surveys ACE, MCE, Meta-Harness,
Self-Harness, and ADAS.

## What each change buys — and how to verify it

Every improvement run emits a `run-report.json` whose numbers are recomputable
from the run tree, so a reader re-derives them rather than trusting them:

- **Measured improvement** — the incumbent curve (`kit/report.py` → baseline vs.
  best, absolute + relative), read from each `gen_*/results.json`.
- **Reproducible history** — the itemized playbook / `ledger.jsonl` + the
  deterministic incumbent + a fixed config fingerprint (models, profiles, commits)
  and the exact `sia run` command.
- **Failure-mode insight** — the structured diagnostic (failure taxonomy, worst
  stage, cost) that drove each hypothesis.
- **What changed & why** — a selector added *without touching SIA*, the
  method/content split (MCE), and the measured algorithm choice — each cited.
- **Integrity** — `evaluate.py` untouched, gains produced through the loop, no
  sample-level hard-coding; the reported best generation is auditable from disk.

## Reproducibility & honesty

- No `evaluate.py` / dataset / ground-truth edits; the score signal is SIA's own.
- Gains are produced by the SIA loop, not hand-carried (Mode A / A+fork).
- The incumbent is recomputable from the run tree (`sia_history.py`), so the
  reported best generation is verifiable, not asserted.

## Modes shipped (chosen at handoff)

- **Mode A** — injection-only; protocol in the `target_agent.py` docstring.
- **Mode A+fork** — deterministic incumbent seeding (`fork/`), if forkable.
- **Mode B** — offline scouting only; never submitted.

See `docs/reflection-structure.md` for the per-technique detail and
`docs/how-sia-improves.md` for SIA's own mechanism.
