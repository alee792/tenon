---
name: evolve
description: Run a hill climb or genetic search over a tenon agent project — propose mutated or recombined agent folders, gate them with tenon check, evaluate each generation through fanout, and select. Use when the user wants to optimize an agent's instructions, skills, or tools against a measurable objective, iterate across generations rather than a single fan-out, or inspect the lineage of an in-flight or finished search.
---

# evolve

`improve/evolve.py` is the outer loop over `fanout`: propose → gate → evaluate
→ select, repeated across generations. Read `improve/EVOLVE.md` for the design
and `improve/README.md` for the generation runner underneath it.

A genome is an agent directory; a gene is one authored component
(`instructions.md`, a `skills/<name>/`, a `tools/<file>`). `tenon check
--format jsonl` both gates a candidate and mints its fingerprint, which
serves as the genome id — so invalid offspring die before a model is opened,
and an already-scored genome is never paid for twice.

## Before starting a search

Work through these in order. Skipping the first two produces a number that
means nothing, which is worse than no search at all.

1. **Define fitness first.** Ask the user what a better agent measurably does.
   Prefer a mechanical scorer — tests passing, a linter, a property of the
   diff — over an LLM judge. If a model must judge, keep the judge fixed for
   the whole search and outside the population.
2. **Get a held-out task set.** Several tasks, not one. Ask for tasks the
   winner can later be re-checked against, and say plainly that a genome
   selected on the search tasks has been fitted to them.
3. **Do the budget math out loud** and get agreement before launching:
   `(1 + generations × population) × tasks × repeats` full harness runs.
4. **Pick the strategy.** Hill climb when one axis is being tuned (usually
   `instructions.md`). Genetic only when the seed has several separable genes
   worth recombining.
5. **`--dry-run` the spec** and show the user the resolved config.

## Running it

```bash
python3 improve/evolve.py run --spec search.json
```

```bash
python3 improve/evolve.py lineage RUN
```

```bash
python3 improve/evolve.py best RUN
```

Copy `improve/examples/search-hill-climb.json` or `search-genetic.json` and
edit; `improve/examples/score-tests.sh` and `mutate-llm.sh` are working
operators to adapt.

## Guidance

- **Never promote a winner.** Tenon makes automatic or unreviewed promotion of
  an agent-authored improvement an explicit non-goal, and evolve never writes
  to the source agent. Finish by showing the diff and the score, and leave the
  decision with the user.
- **Report gains against their spread.** Every genome carries a standard
  deviation. When a generation's improvement is smaller than that spread, say
  it is noise — do not present it as progress.
- **Keep each operator to one gene.** An operator touching three files makes
  the score unattributable. If the user's operator edits broadly, say so.
- **Suggest structural operators when the search stalls.** If `combine` keeps
  producing duplicates, the genome's dimensionality is the limit — a `grow`
  operator adds loci, and a `prune` keeps the result from bloating.
- **Rejections are signal, not errors.** `lineage` shows which stable tenon
  diagnostic killed each candidate. A search rejecting most offspring means
  the mutation operator is too destructive — report the pattern rather than
  silently continuing.
- **Watch for a stalled climb.** Repeated `duplicate` lines mean the operator
  keeps regenerating known genomes; widen the mutation or change `rng_seed`.
- **Island models and MAP-Elites are policies, not features.** Tag the slot in
  `pair` or `score`, honour the tag in `select`; see
  `improve/examples/policies/pair-island.py`. Do not add machinery to evolve for
  them.
- **Report re-evaluation drift.** `reevaluate` defaults to the incumbent; when
  its score moves on re-scoring, that is the correction working — surface it
  rather than reporting only the headline number.
- **Reach for a policy hook before changing evolve.** `pair`, `combine`,
  `select`, `score`, and every variation operator are all commands. If the user wants a
  different search behaviour, write a policy in `improve/examples/policies/`
  rather than editing the loop.
- **Do not invent tasks, fitness, or mutations** to fill a gap in the user's
  spec. Ask.
- **Stop and report on budget exhaustion** — evolve already does this and
  writes `best.json`; surface it rather than relaunching.
