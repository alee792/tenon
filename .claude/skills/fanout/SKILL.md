---
name: fanout
description: Dispatch and supervise k isolated tenon agents — one git worktree, one source fingerprint, and one branch each — then report per-variant results for top-k selection. Use when the user wants to run the same task across several agent variants, sweep prompt or skill mutations, compare harnesses or pin sets on identical starting state, or manage the lifecycle (start, status, logs, stop, collect, clean) of a fan-out already running.
---

# fanout

`improve/fanout.py` runs `k` variants of a tenon agent project concurrently,
each in its own git worktree. Per variant: `git worktree add` → optional
mutation command → `tenon check` → `tenon apply` → `tenon run`.

It is a separate tool over tenon's CLI, not a tenon subcommand. Tenon's
north star keeps evaluation, scoring, and selection out of scope; fanout
holds that same line — it manages lifecycle and reports, and never scores or
picks a winner. Read `improve/README.md` before changing its behavior.

## Running it

Prefer a spec file for anything with more than one distinct variant; use
flags for a uniform sweep. Flags override spec fields.

```bash
python3 improve/fanout.py start --run RUN --agent ./agent --harness claude --k 3 --task "..."
```

```bash
python3 improve/fanout.py start --spec sweep.json --detach
```

A `tenon` binary must be resolvable — `--tenon PATH`, `FANOUT_TENON`, or on
`PATH`. Build one with `go build -o ./tenon ./cmd/tenon`.

`improve/example-spec.json` is a working three-variant sweep to copy.

## Lifecycle commands

| Want | Command |
| --- | --- |
| what is running | `fanout.py status RUN` (`--json` for the raw state) |
| one variant's events | `fanout.py logs RUN VARIANT -f` |
| one variant's agent text | `fanout.py logs RUN VARIANT --text` |
| stop a detached run | `fanout.py stop RUN` |
| results for selection | `fanout.py collect RUN --json --text` |
| tear down | `fanout.py clean RUN` |

## Guidance

- **Check `--dry-run` first** when composing a spec; it prints the fully
  resolved run without touching git.
- **Mutations are the caller's.** `mutate` is a shell command run in the
  variant's agent directory with `FANOUT_VARIANT`, `FANOUT_INDEX`,
  `FANOUT_AGENT_DIR`, `FANOUT_WORKSPACE`, and `FANOUT_RUN` exported. Do not
  invent mutation strategies unless the user asked for them.
- **Selection is the caller's too.** Hand `collect --json` to the user or to
  whatever evaluator they name; each record carries the variant's source
  fingerprint, so a score joins back to the exact configuration.
- **A relative `--agent` lives inside each worktree**, so the agent's own
  files can be mutated and the edits land in the variant's branch. An
  absolute `--agent` is copied per variant and the source is untouched.
- **Concurrency costs real processes.** Each variant is a full harness
  against a full checkout; the default is `min(k, 4)`. Do not raise it
  without the user's say-so.
- **Report failures plainly.** One variant failing does not fail the run;
  `status` carries the reason and the log path for each.
- **Clean up when the user is done**, and say which branches survive if they
  pass `--keep-branches`.
