# Tenon

Tenon makes an agent something you can read: a folder of plain-language
files — instructions, skills, tools — that compiles into native
configuration for Claude Code or Codex. One portable source of truth,
validated before it touches a workspace, with drift detection after.

You get a legible diff for every change, whether the author is a person or
an automated loop revising the agent's own files.

## Quick start

Start from an empty directory and finish inside your harness with a
working agent:

```sh
mkdir my-agent && cd my-agent
cat > instructions.md <<'EOF'
---
description: Reviews pull requests for this repository.
---

You review pull requests. Be specific, cite files and lines, and prefer the
smallest correct suggestion.
EOF

tenon apply . --harness claude   # or: --harness codex
claude                           # or: codex
```

Add capability by adding files — there is nothing to register:

| Add | Get |
| --- | --- |
| a directory under `skills/` | a skill |
| a typed function file under `tools/` | a tool |
| a Markdown file under `connections/` | a native MCP connection |
| a Markdown file under `schedules/` | a scheduled task |

The [product specification](docs/product-spec.md) covers the full authoring
convention; the [glossary](docs/glossary.md) defines the vocabulary.

## The same folder, later

The same directory keeps working as your needs grow — without edits:

- **Run headless.** `tenon run` dispatches bounded JSONL turns through the
  native harness.
- **Run on a schedule.** `tenon schedule run` executes the Markdown cron
  files under `schedules/`.
- **Stage for containers.** `tenon stage` prepares a complete runnable
  filesystem tree for your OCI builder — see
  [staged agent filesystems](docs/product-spec.md#staged-agent-filesystems).
- **Automate revision.** `tenon validate . --diagnostics jsonl` emits one
  JSON line per failure with a stable identifier, so a loop can mutate the
  agent's files, self-correct, and apply — and every apply carries a source
  fingerprint tying each run back to the exact configuration that produced
  it. See
  [the improvement-loop use case](docs/use-cases.md#give-an-improvement-loop-a-substrate).

For the full set of jobs tenon serves — and the boundary of each — see
[use cases](docs/use-cases.md).

## Install

Tenon is pre-release. Until the first binaries land on the
[releases page](https://github.com/alee792/tenon/releases), build from
source with Go 1.26+:

```sh
go build -o tenon ./cmd/tenon
```

## Learn more

- [Product specification](docs/product-spec.md) — the product contract,
  including [known limitations](docs/product-spec.md#known-limitations)
- [Use cases](docs/use-cases.md) — the concrete jobs tenon does today
- [North star](docs/north-star.md) — what the project holds constant
- [Contributing](CONTRIBUTING.md)
