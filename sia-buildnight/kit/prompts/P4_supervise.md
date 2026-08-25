# P4 — Supervise the loop and assemble the submission

While the loop runs:

1. **Watch the ledger.** Tail `ledger.jsonl` (Posture B, at the workspace) or the
   per-generation `improvement.md` + `results.json` (Posture A). After each
   generation confirm the rule held: the next edit branched from the incumbent,
   not from a regression. If a generation regressed and the loop still built on
   it, correct the guidance and continue.

2. **Track the incumbent curve.** Keep a running note of gen → score and which
   hypothesis produced each accepted gain. This is the experiment history the
   judges grade.

3. **If it stalls** (no accepted improvement ~3 generations): apply the
   `HANDOFF.md` fallback — switch selector (greedy→annealed) or, if the strategy
   bandit is on, read its arm ranking and force the top untried family. Keep the
   incumbent; never restart.

4. **Never** let an edit hard-code answers or fit specific samples. If you see
   the feedback agent doing that, reject the generation and note it — the gain
   must be a general capability improvement produced by the loop.

At ~7:30, freeze. Assemble the submission:
- the final incumbent agent (`target_agent.py`);
- the experiment history (`ledger.jsonl` + per-gen `improvement.md`);
- verified baseline score and best score, with the reproducing commands;
- one paragraph — the central insight (PLAN.md §1): SIA had a generator and no
  selector; we added the incumbent-based selector in the scaffold, chose the
  algorithm by measuring the environment, and here is the resulting curve.
