# P3 — Triage and launch the loop

1. **Measure the environment.** Run one baseline generation and time it. If the
   sandbox allows, run a second identical baseline to estimate score noise.
   Record: eval seconds, the 1–2 baseline scores, whether parallel runs are
   allowed, and minutes left until 7:40 submission.

2. **Run the judge:**
   ```sh
   python kit/triage.py --eval-seconds <S> --scores <s1> <s2> \
     [--parallel-ok] --minutes-left <M> --task-dir <TASK_DIR>
   ```
   It writes `kit/HANDOFF.md` with the chosen posture, selector, and command.

3. **Confirm the two rules questions with a challenge lead** (PLAN.md §8):
   re-seeding a new run from an evolved agent (needed for Posture B), and
   parallel-run permission. If Posture B is disallowed, force Posture A.

4. **Launch** the command from `HANDOFF.md`.
   - Posture A: a single `sia run`; the feedback agent follows
     `kit/seed_agent/GUIDANCE.md` to hill-climb from the incumbent.
   - Posture B: `kit/orchestrate.py` drives the incumbent loop.

Paste `HANDOFF.md` and the launch command you ran.
