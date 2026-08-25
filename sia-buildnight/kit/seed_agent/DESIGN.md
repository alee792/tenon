# Seed design notes (read by the meta agent at generation 1)

SIA copies this directory into the meta agent's working directory at generation
1 and tells it to read the files before writing the first `target_agent.py`. This
note orients that first build. It is **not** the binding contract for later
generations — the feedback agent never receives this file.

> **The binding protocol for every generation lives in the module docstring at
> the top of `target_agent.py`.** That file is the only artifact SIA embeds
> verbatim in the feedback prompt each generation, so all durable guidance must
> live there. Keep that docstring intact and at the top of the file.

## Files in this seed

- `target_agent.py` — the agent to build and iterate. Modular stages
  (`load_dataset → plan → solve_one → format_submission → write`) so edits stay
  local. Its docstring carries the selection protocol and hypothesis families.
- `observability.py` — pristine, re-copied every generation. Per-sample failure
  taxonomy + an aggregate diagnostic written to both always-visible channels
  (stdout tail and a sort-first `execution_q-diagnostic.json`). Do not inline it
  into `target_agent.py`; import it.
- `sia_history.py` — pristine, re-copied every generation. Deterministically
  computes the incumbent (best prior generation) from sibling `results.json` and
  threads it into the diagnostic. Import and call `surface_incumbent`.
- `requirements.txt` — third-party deps (anthropic, …) SIA installs before the
  run.

## What "good" looks like for generation 1

- Keep the CLI contract (`--dataset_dir`, `--working_dir`) and the stage
  boundaries.
- Wire the `# HANDOFF:` points to the revealed task (dataset loader, `solve_one`
  prompt/parse, `format_submission` to match `evaluate.py`, submission filename).
- Preserve the `TrajectoryLogger` and `surface_incumbent` calls — they are how
  every later generation diagnoses and selects.
- Do not optimize for specific samples; the score gain must come from general
  capability improvements produced by the loop.
