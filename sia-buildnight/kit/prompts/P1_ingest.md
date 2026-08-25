# P1 — Ingest the revealed challenge (do NOT solve it)

You are helping in a live competition. A challenge environment has just been
revealed at `<CHALLENGE_DIR>`. Your ONLY job in this step is to report its
contract. Do not write a solution, do not edit the agent yet.

Read and report:

1. **Task** — summarize `data/public/task.md`: what the agent must produce.
2. **Dataset** — the dataset file(s) under the dataset dir: filename(s), format
   (json/jsonl/csv), one example record, and the field names.
3. **Submission contract** — open `evaluate.py`. Report the EXACT submission
   filename it looks for and the EXACT format/columns it expects. This is what
   our `format_submission` and `SUBMISSION_FILENAME` must match.
4. **Metric** — what `evaluate.py` writes into `results.json` (the key name and
   whether it is a fraction or a percentage string).
5. **Baseline** — the recorded starting score, if shown.

Output a short structured report with these five headings. Flag anything that
differs from our seed agent's assumptions (`kit/seed_agent/target_agent.py`:
`load_dataset`, `format_submission`, `SUBMISSION_FILENAME`).
