# P2 — Wire the seed agent to the challenge contract

Using the P1 report, fill ONLY the `# HANDOFF:` marked glue in
`kit/seed_agent/target_agent.py` so a generation-1 run produces a valid
submission. Do not remove or weaken anything else.

Fill exactly these, keeping the stage boundaries and all `TrajectoryLogger`
calls intact:

1. `load_dataset(dataset_dir)` — read the real dataset file and return a list of
   sample dicts with a stable `id` field.
2. `solve_one(client, sample)` — the minimal correct prompt + answer parsing for
   this task. Return `{"answer": ..., "confidence": <0..1>}`. Keep it simple;
   the SIA loop will improve it.
3. `format_submission(results)` and `SUBMISSION_FILENAME` — match `evaluate.py`'s
   expected filename and format EXACTLY (from the P1 report).
4. `make_client()` / `MODEL` — match the provider and model in
   `kit/profiles/target-buildnight.json`.

Then fill the two profile files' `HANDOFF-*` fields with the model/provider IDs
and confirm `kit/seed_agent/requirements.txt` lists any new deps.

Validate before handing off: run the seed agent directly on a 2–3 sample slice
(`python kit/seed_agent/target_agent.py --dataset_dir <D> --working_dir /tmp/g1`)
and confirm it writes the submission file and an `agent_execution/` folder with a
`execution_q-diagnostic.json`. Report the sanity-check output. Do NOT tune for
score — that is the SIA loop's job.
