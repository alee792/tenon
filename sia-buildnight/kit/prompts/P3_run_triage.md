# P3 — Triage and launch the loop

1. **Measure the environment.** Run one baseline generation and time it. If the
   sandbox allows, run a second identical baseline to estimate score noise.
   Record: eval seconds, the 1–2 baseline scores, whether parallel runs are
   allowed, and minutes left until 7:40 submission.

2. **Confirm the rules questions with a challenge lead** (PLAN.md §8):
   - Is the SIA repo **forkable AND the fork submittable**? (enables Mode A+fork)
   - Is re-seeding a new run from an evolved agent allowed? (enables Mode B offline scouting)
   - Are parallel runs permitted?

3. **Run the judge** with what you learned:
   ```sh
   python kit/triage.py --eval-seconds <S> --scores <s1> <s2> \
     [--parallel-ok] [--forkable] [--reseed-allowed] \
     --minutes-left <M> --task-dir <TASK_DIR>
   ```
   It writes `kit/HANDOFF.md` with the chosen mode, selector, and exact command.

4. **Launch** the command from `HANDOFF.md`.
   - **Mode A**: a single `sia run`; the feedback agent follows the selection
     protocol in `target_agent.py`'s module docstring to hill-climb from the
     incumbent. Pick the meta profile by the impl dial (fill model/provider from
     the setup kit first):
     - `--meta-agent-profile meta-buildnight-openhands` — OpenHands impl; explores
       the working dir, so it also uses `GUIDANCE.md` + a `ledger.jsonl` (richer).
     - `--meta-agent-profile meta-buildnight` — claude impl; relies on the
       docstring floor. Use a capable model either way.
   - **Mode A+fork**: `git apply fork/orchestrator_incumbent_seed.patch` in the
     SIA checkout first, then the same `sia run`. Deterministic — no reliance on
     the model obeying the docstring.
   - **Mode B** (only if scouting): `kit/orchestrate.py` on our own machine to
     find good hypotheses; fold the winners into the seed we submit. Never the
     submission itself.

Paste `HANDOFF.md` and the launch command you ran.
