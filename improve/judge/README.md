# Human pairwise judge

A `score` policy that asks a person which of two agent outputs is better,
instead of asking anyone for an absolute number.

Absolute scoring is the wrong question to put to a human. "How good is this,
from 0 to 1?" drifts with mood, order and fatigue. "Which of these two is
better?" is the judgement people are reliable at, so this serves pairwise
comparisons and derives the scalar fitness from the outcomes.

```bash
python3 improve/judge/server.py
```

```bash
python3 improve/evolve.py run --spec improve/examples/search-paprika.json
```

Open <http://127.0.0.1:8917>, and judge. The theme follows your system by default; the control
in the header cycles light, dark and auto, and `?theme=dark` pins it in a link. `←` picks A, `→` picks B, `space` is
a tie. The search blocks until the round's comparisons are done, then
takes the win rates as fitness and proposes the next round.

## How it fits evolve's API

`score` is called once per variant, sequentially, after the whole round has
run — so a pairwise judge cannot answer the first call without seeing the
others, and evolve is blocked, so no others are coming.

The server sidesteps that by not depending on the clients: fanout has finished
writing the round's state before scoring starts, so on the first request
the server reads every variant of that round off disk, runs the whole
round robin in the browser, and answers all the blocked clients from the
result. No change to evolve was needed.

## The three screens

**Judge** shows one comparison at a time. `←` picks A, `→` picks B, `space` is
a tie, `g` reveals the gene behind each answer — the `instructions.md` that
produced it. That stays hidden by default: reading it before you decide biases
the comparison toward the instructions rather than the output, which is not
what you are trying to measure.

**About** is a written introduction served by the same page — how the loop works, the API it
exposes, what swapping each injection point buys you, what tenon contributes, and how a human
gets wired into a machine scoring contract. It carries the walkthrough screenshots.

**Review** is where a finished round goes. It carries each round's
leaderboard, the answer and the gene for any genome you click, and the lineage
showing which parent and which mutator produced it. An **All rounds** tab
puts every genome on one scale.

When the next round finishes running, a banner offers to start judging it
and a desktop notification fires if you have granted permission — but the
search waits on you either way, so you can stay on the review screen as long as
you like.

## What it scores

Bradley-Terry maximum likelihood: a latent strength per genome such that
P(i beats j) = p_i / (p_i + p_j), solved by Zermelo's iteration with one
virtual draw against a phantom opponent so an undefeated or winless entry
still gets a finite strength. Fitness is the fitted probability of beating a
uniformly drawn opponent, on [0, 1].

A raw win rate would be simpler and worse: it treats every comparison as
equally informative, so beating the weakest entry counts the same as beating
the strongest, and it has no answer at all when the comparison graph is
incomplete. `test_scoring.py` pins the difference — two genomes with identical
1-1 records score 0.669 and 0.358 when their opponents differed in strength.

**The fit is global, across every round.** A round's own scores are
normalised inside its own field, so they do not compare across rounds: a
genome that went 5/5 against weak siblings and 1/5 against strong ones has not
changed, its opposition has. The incumbent appears in consecutive rounds, and
that shared node is exactly what makes one fit over all comparisons
identifiable — which is the point of anchoring in the first place.

Two details that matter:

- **The incumbent is in every round.** `reevaluate: incumbent` puts it back in
  each round's comparisons, so win rates are anchored across rounds.
  Without that, a candidate winning 0.75 against its own round's peers
  could be worse than last round's 0.80 and still displace it.
- **An unjudgeable entry scores 0.5, not 0.** Round 0 holds only the seed,
  so there is nothing to compare it against. Scoring it zero would mean the
  seed is beaten by anything at all, and whether evolution beat the seed is the
  first question the search has to answer.

Comparisons are blind — the panels are labelled A and B, never by lineage.

## The bundled search

[`examples/search-paprika.json`](../examples/search-paprika.json) is k=5 over
two rounds on one task, with the agent pinned to Haiku:

| | |
| --- | --- |
| Task | explain the repository to a new contributor, under 120 words |
| Candidates | 5 per round, hill climb (one gene, so crossover has nowhere to go) |
| Agent model | `claude-haiku-4-5-20251001`, pinned per genome |
| Harness runs | 13 |
| Comparisons you make | 30 — fifteen per round, six entries each |

Swap `model` to `claude-sonnet-5` to ask the same question of a bigger model,
or to compare the two on identical starting state.

**Model pinning is per genome.** A tenon pin set binds an expected source
fingerprint, and every mutation changes that fingerprint, so one shared
pin set would fail verification on every candidate but the seed. Setting
`model` in the spec makes evolve write one pin set per genome — the same
trick tenon documents for comparing harnesses, turned around: many genomes
crossed with one pin set, rather than one commit crossed with many.

## Honest limits

- 30 comparisons is one person's opinion on one task. It is enough to steer a
  demo and not enough to conclude anything about the agent.
- The judge sees output text only, not the diff or the turn record.
- Fitness is within-run: win rates are comparable inside a search, not across
  searches.
