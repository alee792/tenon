# evolve

Hill-climbing and genetic search over tenon agent projects. fanout runs one
round; `evolve` is the loop around it.

```
seed ──▶ propose ──▶ gate ──▶ evaluate ──▶ select ──┐
           ▲          │         (fanout)            │
           └──────────┴─────────────────────────────┘
                   lineage.jsonl
```

## The representation

Two properties of tenon do the heavy lifting, and neither needed a change to
it.

**A genome is a directory; a gene is one authored component.** Three words,
kept distinct: a **locus** is a component path (`skills/alpha`,
`instructions.md`), a **gene** is the content at that path, and a **genome** is
the map from loci to genes — on disk, the agent directory. An agent
project is already a folder of files, so `instructions.md`, each
`skills/<name>/`, each `tools/<file>`, each `subagents/<name>.md` — and the
same for `plugins/`, `mcp/`, `schedules/` — is a gene. Crossover is file-level
recombination, not text surgery: for every locus either parent holds, the
offspring inherits one parent's copy of the gene there.

**Which paths are genes is configuration, not a constant.** `spec.genes.dirs`
and `spec.genes.files` default to `["skills", "tools", "subagents",
"plugins", "mcp", "schedules", "harnesses"]` and `["instructions.md"]` —
everything tenon's loader inventories today. They are a **mirror of what tenon's loader
inventories**, and a mirror drifts: the day tenon recognises a new component
directory, a search that does not know about it silently stops recombining
that component and carries it along with the first parent instead, which
looks like a search that simply never varies there. Keep them level with the
agent-project layout. `harnesses/` is a default gene like the rest: a
per-harness override is authored surface, and a search that cannot vary it
cannot find a harness-specific fix. A search that wants harness config held
fixed lists `genes.dirs` without it.

**The fingerprint is the genome id.** The adapter's `gate` is
a single call that both gates a candidate and names it — stable diagnostic
identifiers on rejection, the source fingerprint on success. That gives three
things for free:

- **A cheap gate.** Crossover produces incoherent offspring routinely (a skill
  whose tool did not come along). They are rejected before a model is ever
  opened, and the lineage records *which rule* rejected them.
- **Deduplication.** A fingerprint already scored is never paid for twice —
  and since the digest is content-addressed and commit-free, a mutation that
  cycles back to a previous genome is caught even across rounds.
- **Attribution.** The same fingerprint travels on every dispatch event, so
  the score and the configuration cannot drift apart.

Tenon mints those units; this loop composes the chain. Lineage lives in
`lineage.jsonl` here, never in tenon.

## What evolve does not do

Fitness is your `score` command. Mutation is your `mutate` command. And
promotion is a human act — tenon's spec makes automatic or unreviewed
promotion of an agent-authored improvement an explicit non-goal, so `evolve`
never writes to the source agent. `evolve best` prints a diff to review.

## Spec

```json
{
  "run": "instructions-climb",
  "strategy": "hill-climb",
  "repo": "/path/to/repo",
  "agent": "agent",
  "seed": "/path/to/agent",
  "harness": "claude",
  "tasks": ["Fix the failing test in internal/apply.", "Add a table test for parseDuration."],
  "repeats": 2,
  "score": "sh examples/score-tests.sh",
  "mutators": [{ "name": "edit", "command": "sh examples/mutators/edit-llm.sh" }],
  "rounds": 6,
  "population": 4,
  "patience": 2,
  "max_variants": 60,
  "concurrency": 4,
  "timeout": "900s",
  "rng_seed": 1,
  "genes": { "dirs": ["skills", "tools", "subagents", "plugins", "mcp", "schedules"], "files": ["instructions.md"] }
}
```

```bash
python3 improve/evolve.py run --spec search.json
```

```bash
python3 improve/evolve.py lineage instructions-climb
```

```bash
python3 improve/evolve.py best instructions-climb
```

Genetic runs add `crossover_rate`, `mutation_rate`, and `tournament`;
`population` becomes the surviving population rather than the neighbour
count. There is no elitism knob: survivor selection is elitist over the union
of incumbents and candidates — the (mu+lambda) scheme — so the incumbent can
only be displaced by something that outscored it.

## The policies you can replace

**Variation mutators** run with the candidate genome directory as their
working directory and edit it in place. Declare them weighted, and name them:

```json
"mutators": [
  { "name": "edit",  "weight": 5, "command": "sh examples/mutators/edit-llm.sh" },
  { "name": "grow",  "weight": 3, "command": "sh examples/mutators/grow-skill.sh" },
  { "name": "prune", "weight": 2, "command": "python3 examples/mutators/prune-gene.py" }
]
```

`"mutate": "<command>"` is sugar for a single unweighted mutator.

The name is not bookkeeping. It lands in the lineage entry of every genome the
mutator made, so the record answers *which kind of change produced the gains*
with no extra instrumentation:

```
mutator                n    mean    best
crossover+grow         3   0.417   0.625
edit                   8   0.312   0.500
grow                   1   0.125   0.125
prune                  3   0.125   0.250
```

**Structural mutators change the genome's dimensionality**, and you need them.
`combine` can only shuffle loci the parents already hold, so without a `grow`
the search space is frozen at whatever the seed had — a four-gene seed gives
sixteen recombinations forever. Improving an agent usually means acquiring a
capability, not rewording an existing one. `prune` is the counterweight:
growth without pruning is bloat, instructions get longer every round, and
nothing ever removes a rule that stopped earning its place.

This makes the genome variable-length, which is fine here for the reason NEAT
needed historical markings: **locus** names are the marking. `skills/alpha` in
two genomes descends from one origin, so `combine`'s union-of-loci aligns
structurally different genomes correctly without extra machinery.

**Mutators are told about their parents.** Each is handed
`EVOLVE_PARENT_REPORT`, a JSON file carrying the parents' scores and every
variant they ran — status, agent output, patch path, per-task score — plus
`EVOLVE_GENES`, `EVOLVE_MUTATOR`, `EVOLVE_GENOME_DIR`, and `EVOLVE_RUN`. A
blind mutation wastes a full harness run; at this budget a mutator should be
able to see which task its parent failed and aim at that.

**`score`** receives one JSON object on stdin per variant:

```json
{
  "run": "...", "round": 3,
  "genome": "sha256:...", "genome_path": "/…/genomes/ab12cd34",
  "task_index": 0, "task": "Fix the failing test…",
  "record": { "status": "done", "turns": [...], "workspace": "/…", "text": "…", "patch": "/…/diff.patch" }
}
```

It prints one JSON object carrying `score` (higher is better). A non-zero exit
scores 0. `EVOLVE_WORKSPACE` is exported so a test-suite scorer can just `cd`
into the variant's checkout — see
[`examples/score-tests.sh`](examples/score-tests.sh).

**Three states are never handed to `score` at all**, because all three mean
the same thing: we learned nothing about this genome. A variant fanout marked
`errored` (tenon reported outcome `error`, so the environment failed rather
than the candidate); a variant `cancelled` by a fail-fast sibling before it
ran; and a variant missing from `collect` entirely. Each is logged as a
warning and dropped, and a genome left with no other sample stays unscored
(`-` in the log and `null` in the lineage) rather than being recorded as a
zero. Recording a zero would let an infrastructure outage read as "every
candidate is terrible" and quietly steer the search.

A variant that ran out of **time** is not in that set. A dispatch whose
wall-clock budget expires is terminated and reported as `timed_out`, which
fanout marks `failed`: it is a finding about the variant — the agent really
was slower than the budget — and it is scored like any other failed variant.
Dropping it instead would let a search drift toward whatever fits the budget
without ever paying for being slow. Set `timeout` to the budget you actually
mean; it is capped at 29m30s rather than at tenon's own 30 minutes, because
the adapter's clock must fire before tenon's backstop or the timeout arrives
as an environment error and is dropped after all.

The adapter's `iterate` — gate, compile, [drift], dispatch, drift — treats
the post-run drift the same way. It runs after a dispatch that completed AND
after one that timed out, because a half-finished agent is exactly the one
that may have rewritten its own configuration mid-run, and it is skipped only
when the dispatch failed at the gate, where no run happened. A timed-out pass
still reports `phase_failed="run"` with `outcome="timed_out"`: the drift is
evidence carried alongside the finding, never a replacement for it.

The three search policies work the same way — a named built-in, or a command
taking one JSON object on stdin and printing one on stdout. Evolve keeps the
mechanism; these decide the policy.

| Field | Built-in | Given | Returns |
| --- | --- | --- | --- |
| `pair` | `tournament` | the population and how many offspring are wanted | `{"pairs": [["gid"], ["gidA","gidB"], …]}` — one entry per offspring, one id to reproduce asexually, two or more to recombine |
| `combine` | `uniform` | the chosen parents, their loci, and an `out_dir` | `{"genes": {"skills/alpha": "gidA", …}}` — keyed by locus, naming which parent supplies the gene there — or `{"materialized": true}` when the hook wrote `out_dir` itself |
| `select` | `elitist` | the incumbents, this round's candidates, and `keep` | `{"population": ["gid", …]}`, best first |

Because `pair` returns tuples, it expresses *who breeds* and *how often
crossover happens* in one place — a policy that always returns two ids is a
fully sexual population regardless of `crossover_rate`.

Working policies to copy, all exercised in testing:

- [`policies/pair-roulette.py`](examples/policies/pair-roulette.py) —
  fitness-proportional selection that never pairs a genome with itself
- [`policies/combine-single-point.py`](examples/policies/combine-single-point.py) —
  single-point rather than uniform crossover over ordered loci
- [`policies/select-diverse.py`](examples/policies/select-diverse.py) —
  elitist with a diversity floor, so the population cannot collapse onto one
  lineage
- [`policies/combine-line-blend.py`](examples/policies/combine-line-blend.py) —
  the `materialized` escape hatch: recombination below component grain

## Slots, tags, and island models

The population is a list of **slots**, not of genomes. A slot holds a genome
plus a `tags` dict that policies read and write and evolve never interprets.

The split matters: a genome is content, addressed by its fingerprint and
immutable, so its *score* belongs to it. An island, a MAP-Elites niche, an age
layer — those belong to the *slot*, because the same content can occupy two of
them at once. A FunSearch island reset seeds the weakest island from the
strongest, and afterwards one genome sits in two islands with one score.

Tags flow three ways, and evolve stays ignorant of all of them:

- `pair` sets them on offspring — `{"parents": [0, 3], "tags": {"island": 2}}`
- `score` sets them from observed behaviour — `{"score": 0.7, "tags": {…}}`,
  which is how a behavioural descriptor arrives
- `select` sets them on survivors, which is how a reset relabels
- otherwise a child inherits its first parent's tags

Parents are referenced by **slot index** rather than genome id, because after a
reset an id no longer names one slot.

So yes — island evolution is a pair of policies, not a change to evolve:

```json
"pair":   "python3 examples/policies/pair-island.py",
"select": "python3 examples/policies/select-island.py"
```

[`pair-island.py`](examples/policies/pair-island.py) draws parents only from
within an island; [`select-island.py`](examples/policies/select-island.py)
keeps each island's own best and wipes the weakest island every third
round, reseeding it from the strongest. Between them that is the whole
model. MAP-Elites is the same shape: tag the niche in `score`, keep the best
per niche in `select`.

## Resuming a search

A round costs harness runs, and in the judged case a person's attention,
so finishing one and being unable to build on it is the worst failure this
tool has. Every round is checkpointed:

```bash
python3 improve/evolve.py run --spec search.json --resume
```

`--resume` rebuilds the search from its own record — `lineage.jsonl` carries
every genome ever admitted and what it scored, `checkpoint.json` carries the
slots that survived, their tags, the variant count and the RNG state — and
continues from the round after the last one completed. Nothing already
scored is run again.

The spec is re-read on resume, so this is also how you branch: judge one
round, then resume the same run with a different mutator mix, a wider
`offspring`, or a different `model`, all built on the judged winners rather
than starting over.

Judged verdicts are durable too. The judge writes each comparison to
`<state>/<run>/judge/verdicts-round-N.json` as it is given, so a restarted
server resumes mid-round instead of discarding the work.

## Noise and re-evaluation

Elitist selection on a noisy score keeps whichever genome drew luckiest and
never revisits it, so the population fills with over-estimated genomes. Because
the fingerprint names content, re-running an incumbent adds samples to the same
genome and its running mean tightens rather than staying frozen at first
contact.

`reevaluate` defaults to `incumbent` — one extra genome per round, which
buys the correction where it matters most, since the incumbent is what gets
reported and what a hill climb breeds from. `population` re-evaluates every
survivor and roughly doubles the cost; `none` restores the older behaviour.

The correction is visible in the log, and it is meant to be:

```
incumbent rescored 0.3634 -> 0.2522 (-0.1112) over 2 samples
```

## Defaults

Every policy that is *mechanism* has exactly one sane default, so a spec that
names none of them still runs: `pair: tournament`, `combine: uniform`,
`select: elitist`.

Every policy that is *intent* has none, deliberately. There is no default
`score` and no default mutator, because a default fitness function would be
evolve inventing an objective — the precise move that makes these loops report
progress they have not made. The other named policies in
[`examples/policies/`](examples/policies/) stay examples rather than built-ins
so that the API is what gets exercised.

## Is the gene grain right?

For `skills/`, `tools/`, `subagents/`, `plugins/`, `mcp/`, and `schedules/`,
yes: those directories are already the unit their author reasons about, each
one is independently valid, and splitting a skill from the scripts it calls
would manufacture broken offspring for no gain.

`instructions.md` is the awkward locus — it is usually the thing being
optimized, and it is one indivisible gene, so crossover there is all-or-
nothing. The temptation is to split it into sections or lines. Resist it by
default, for a reason that generalizes:

**The right grain is set by what your gate can check.** Tenon's gate proves
contracts, not coherence: it merges plugin skills under precedence, proves tool
schemas, prepares tools exactly as apply prepares them, accounts the aggregate
budgets, and dry-runs the same file generation apply would perform. That is a lot,
and it is all structural. At component grain, a bad recombination usually
breaks one of those contracts — a missing skill directory, absent frontmatter,
a tool whose schema no longer parses — and dies for free. Split
`instructions.md` into lines and most bad recombinations become *semantically*
incoherent while remaining perfectly well-formed: two contradictory rules,
both valid Markdown, every contract satisfied. Nothing structural is left to
catch, so you pay a full harness run to discover it. Finer grain moves cost
from the cheap gate to the expensive evaluator.

That showed up in testing. `combine-line-blend.py` blends `instructions.md`
line by line; the gate caught `instructions.frontmatter.missing` and
`instructions.body.empty`, but nothing it admitted was checked for coherence.

The other pressure runs the opposite way: with `n` loci, uniform crossover
explores `2^n` combinations, so a four-gene agent has a search space of 16 and
a GA degenerates almost immediately. If crossover has nowhere to go, the fix
is usually more genes — more skills, more tools — not finer slicing of the
ones you have.

So: component grain is the default because it matches what the gate can prove.
When you want finer, it is a policy question, not an evolve change — write a
`combine` hook that materializes the child itself.

## Choosing a strategy

| | hill-climb | genetic |
| --- | --- | --- |
| Population | 1 incumbent (mu is pinned), `offspring` neighbours per step | `population` survivors (mu), `offspring` candidates (lambda) |
| Good when | one axis is being tuned — the wording of `instructions.md`, one skill's body | genes are separable — several skills or tools that can be mixed |
| Cost per round | `population × tasks × repeats` runs | same |
| Failure mode | local optimum; add `rng_seed` restarts or widen the mutation | the population collapses onto one lineage; swap in a diversity-aware `select` |

Crossover is only worth its cost when the seed has more than one gene. With a
single `instructions.md`, use hill-climb.

## Budget

```
variants = (1 + rounds × population) × tasks × repeats
```

Each variant is a full harness run against a full checkout. The spec above
is `(1 + 6×4) × 2 × 2 = 100` runs. Set `max_variants` — the loop stops and
reports the best found so far rather than overrunning it.

Worktrees are reclaimed as each round is scored (branches deleted,
event streams and patches kept under
`<state>/<run>/rounds/round-N/`), so disk holds one round at a time.
Set `keep_worktrees: true` to inspect them instead.

## The verifier is the bottleneck

This is the part that decides whether the search means anything.

- **Use a held-out task set, not one task.** A single task's score is mostly
  noise, and a climb over noise is just a random walk that reports success.
- **Set `repeats ≥ 2`.** Every genome's score carries a standard deviation
  into `lineage.jsonl`, and evolve says so when a round's gain is smaller
  than the genome's own spread. Treat those as noise.
- **Prefer a mechanical scorer** — tests passing, a linter, a diff property —
  over an LLM judge. If you must judge with a model, keep the judge fixed for
  the whole search and out of the population, or you are optimizing the judge.
- **Expect overfitting to the task set.** A genome that wins on the search
  tasks has been selected on them. Re-run the winner against tasks it never
  saw before believing the number.

## State layout

```
<state>/<run>/
  search.json         the resolved config
  lineage.jsonl       one line per genome: scored, rejected, or duplicate
  best.json           the incumbent at exit
  genomes/<id>/       every admitted genome, kept for diffing
  rounds/round-N/     the fanout run for that round
```

`lineage.jsonl` is the record tenon deliberately does not keep: the edges
between fingerprints, the mutator that made each one, and the rule that
rejected the rest.
