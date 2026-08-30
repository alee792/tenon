# Human pairwise judge

A `score` policy that asks a person which of two agent outputs is better,
instead of asking anyone for an absolute number.

Absolute scoring is the wrong question to put to a human. "How good is this,
from 0 to 1?" drifts with mood, order and fatigue. "Which of these two is
better?" is the judgement people are reliable at, so this serves pairwise
comparisons and derives the scalar fitness from the outcomes.

```bash
python3 fanout/judge/server.py
```

```bash
python3 fanout/evolve.py run --spec fanout/examples/search-paprika.json
```

Open <http://127.0.0.1:8917>, and judge. `←` picks A, `→` picks B, `space` is
a tie. The search blocks until the generation's comparisons are done, then
takes the win rates as fitness and proposes the next generation.

## How it fits evolve's API

`score` is called once per trial, sequentially, after the whole generation has
run — so a pairwise judge cannot answer the first call without seeing the
others, and evolve is blocked, so no others are coming.

The server sidesteps that by not depending on the clients: fanout has finished
writing the generation's state before scoring starts, so on the first request
the server reads every variant of that generation off disk, runs the whole
round robin in the browser, and answers all the blocked clients from the
result. No change to evolve was needed.

## What it scores

Win rate over the comparisons an entry took part in, ties counting half — the
Copeland score, which is about all the resolution five candidates support.

Two details that matter:

- **The incumbent is in every round.** `reevaluate: incumbent` puts it back in
  each generation's comparisons, so win rates are anchored across generations.
  Without that, a candidate winning 0.75 against its own generation's peers
  could be worse than last generation's 0.80 and still displace it.
- **An unjudgeable entry scores 0.5, not 0.** Generation 0 holds only the seed,
  so there is nothing to compare it against. Scoring it zero would mean the
  seed is beaten by anything at all, and whether evolution beat the seed is the
  first question the search has to answer.

Comparisons are blind — the panels are labelled A and B, never by lineage.

## The bundled search

[`examples/search-paprika.json`](../examples/search-paprika.json) is k=5 over
two generations on one task, with the agent pinned to Haiku:

| | |
| --- | --- |
| Task | explain the repository to a new contributor, under 120 words |
| Candidates | 5 per generation, hill climb (one gene, so crossover has nowhere to go) |
| Agent model | `claude-haiku-4-5-20251001`, pinned per genome |
| Harness runs | 13 |
| Comparisons you make | 30 — fifteen per generation, six entries each |

Swap `model` to `claude-sonnet-5` to ask the same question of a bigger model,
or to compare the two on identical starting state.

**Model pinning is per genome.** A tenon manifest binds an expected source
fingerprint, and every mutation changes that fingerprint, so one shared
manifest would fail verification on every candidate but the seed. Setting
`model` in the spec makes evolve write one manifest per genome — the same
trick tenon documents for comparing harnesses, turned around: many genomes
crossed with one pin set, rather than one commit crossed with many.

## Honest limits

- 30 comparisons is one person's opinion on one task. It is enough to steer a
  demo and not enough to conclude anything about the agent.
- The judge sees output text only, not the diff or the turn record.
- Fitness is within-run: win rates are comparable inside a search, not across
  searches.
