# How SIA improves (mechanism reference)

Read from `hexo-ai/sia@8c50303`. Harness mode (`--focus harness`, the default);
weights mode is a separate RL path (`train.py` + Tinker) and out of scope here.

## The mechanism is one thing: rewrite `target_agent.py`

Each generation, the **feedback agent rewrites `target_agent.py` wholesale** into
the next gen dir, informed by the previous version + evidence. That code edit
*is* the improvement. There is no diff, no patch object, no typed edit — the
agent saves a full new file (`prompts.py` STEP 4: "MUST save to
`{next_gen_dir}/target_agent.py`").

## Tools are generic — there is no structured improvement protocol

The meta/feedback agent runs via `run_agent(agent_impl=…)` with plain file/shell
tools only:

| impl | tools |
| --- | --- |
| `claude` | `Bash, Read, Write, Edit, Glob` |
| `pydantic-ai` | `write_file, read_file, bash` |
| `openhands` | `TerminalTool, FileEditorTool` |

There is **no typed `propose_edit` / diff / hypothesis / accept-reject tool** and
no function-calling schema for improvement. The agent just reads and writes
files. Any structure we want is structure *we* impose via file conventions.

## What carries across generations

The feedback prompt is rebuilt fresh each generation and re-injects:

1. **`target_agent.py`** — the evolving code (embedded verbatim as `agent_py`).
2. **`improvement.md`** — free-form markdown rationale the agent writes each gen;
   the prompt tells it to read prior ones. No schema by default.
3. **`context.md`** — SIA's own accumulated memory, appended per gen by
   `context_manager`. The closest thing to structure: it holds the
   **`results.json` metrics + computed deltas** (structured), plus **insights
   parsed out of `improvement.md`** and an **LLM-generated prose summary** of the
   changes (SIA makes an extra LLM call, `_generate_llm_summary`).
4. **`results.json`** — the only fully-structured signal (the scores), injected
   verbatim; the incumbent-relevant history lives here + in `context.md`.

So cross-generation state ≈ **two markdown files + one JSON of scores + the code
file**. The only structured data is `results.json` and the metrics table derived
from it.

## Consequences for our kit

- Nothing forces the feedback agent to behave, so loop quality rides entirely on
  (a) how legible the **evidence** is (execution logs + our diagnostic) and
  (b) how legible the **carried memory** is (`context.md` / `improvement.md`).
  Those are exactly the observability + guidance surfaces we build.
- `context_manager` *re-parses `improvement.md`* into `context.md`, so a
  fixed-schema `improvement.md` is the one way to impose structure on the memory
  **without a fork**. Keep that schema tight (see `reflection-structure.md`).
- Real structured tool-calls for improvement (typed hypotheses, an apply-diff
  tool, an accept/reject tool) do **not** exist in SIA. Adding them is a
  fork-level change to `prompts.py` + the agent_impl toolset — out of scope for
  the injection path, in scope for `fork/` if we go that route.
