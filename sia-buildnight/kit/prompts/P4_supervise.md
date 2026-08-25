# P4 — Supervise the loop and assemble the submission

While the loop runs:

1. **Watch the memory channels.** For Mode A / A+fork, read `context.md` (SIA's
   per-generation score history) and each gen's `improvement.md` (the ledger the
   feedback agent writes). For Mode B offline scouting, tail `ledger.jsonl` at the
   workspace. After each generation confirm the rule held: the next edit branched
   from the **incumbent**, not from a regression. In Mode A this depends on the
   model obeying the docstring — if a regression got built on anyway, the meta
   model is too weak; switch to a stronger one or to Mode A+fork.

2. **Track the incumbent curve.** Keep a running note of gen → score and which
   hypothesis produced each accepted gain. This is the experiment history the
   judges grade. Remember SIA's own summary reports gen-1→last, not the best —
   report the true best generation.

3. **If it stalls** (no accepted improvement ~3 generations): apply the
   `HANDOFF.md` fallback — switch selector (greedy→annealed) or, if the strategy
   bandit is on, force its top untried family. Keep the incumbent; never restart.

4. **Never** let an edit hard-code answers or fit specific samples. If you see
   the feedback agent doing that, treat that generation as rejected and note it —
   the gain must be a general capability improvement produced by the loop.

At ~7:30, freeze. Assemble the submission:
- the best-scoring agent (`target_agent.py` from the incumbent generation);
- the experiment history (`context.md` + per-gen `improvement.md`; plus
  `ledger.jsonl` if we scouted with Mode B);
- verified baseline score and best score, with the reproducing commands;
- one paragraph — the central insight (PLAN.md §1): SIA had a generator and no
  selector; we added the incumbent-based selector in the scaffold (deterministic
  via the fork where allowed, docstring-driven otherwise), chose the algorithm by
  measuring the environment, and here is the resulting curve.
