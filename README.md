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
| a Markdown file under `mcp/` | an MCP server the harness connects to |
| a directory or `<name>.md` reference under `plugins/` | a vendored or pinned Agent Plugin |

An `mcp/<name>.md` is four lines of the standard Agent Plugins `mcp.json`
server entry plus prose — a hosted `type: streamable-http` URL, a tree-local
`type: stdio` command, or an installed package — and the harness owns
authentication, so an OAuth server needs no credential in your source. See
[the GitHub journey](docs/github-native-mcp.md). A `plugins/<name>.md` names
a `source` and a full commit `rev` instead of vendoring the package;
`tenon plugin fetch` is the one explicitly online command, and `tenon apply`
stays offline.

The [product specification](docs/product-spec.md) covers the full authoring
convention; the [glossary](docs/glossary.md) defines the vocabulary.

## The same folder, later

The same directory keeps working as your needs grow — without edits:

- **Run headless.** Launch the harness's headless mode, or any Agent
  Client Protocol client such as acpx or OpenClaw, in the applied workspace;
  tenon proves the workspace and stays out of the run.
- **Run on a schedule.** The same launch under cron or any scheduler,
  one fresh session per occurrence.
- **Undo it.** `tenon clean --workspace DIR` is apply's inverse: it removes
  the files tenon recorded as its own, and nothing else.
- **Stage for containers.** `tenon stage` prepares a complete runnable
  filesystem tree for your OCI builder — see
  [staged agent filesystems](docs/product-spec.md#staged-agent-filesystems).
- **Automate revision.** `tenon check . --format jsonl` emits one JSON line
  per failure with a stable identifier, so a loop can mutate the agent's
  files, self-correct, and apply — and the stream's closing object carries
  the run's `outcome` and a source fingerprint tying each run back to the
  exact configuration that produced it. See
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
- [The improve module](improve/README.md) — fan-out and search over agent
  projects, built on tenon's CLI rather than inside it
- [Contributing](CONTRIBUTING.md)
