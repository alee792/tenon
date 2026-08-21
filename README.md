# Tenon

Tenon makes an agent something you can read: a folder of plain-language
files — instructions, skills, tools — that `tenon apply` compiles into
native configuration for the coding-agent harness you already trust, Claude
Code or Codex. One portable source of truth, proven valid before it touches
a workspace, kept honest afterward by drift detection.

The artifact has two authors, and neither outranks the other: a person
writing and reviewing files, and an improvement loop revising an agent's own
files. Both get the same contract — a legible diff, validation before
anything runs, and attribution of every run to its exact configuration.
Tenon is the loop's substrate, never the loop: it proves a revision is
well-formed, not that it is an improvement, and it collects no transcripts,
evaluations, or scores.

## The first five minutes

Start from an empty directory and finish inside your harness with a working
agent:

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

Add capability by adding files, never by registering anything: a directory
under `skills/` is a skill, a typed function file under `tools/` is a tool, a
Markdown file under `connections/` is a native MCP connection, one under
`schedules/` is a cron task. See the
[product specification](docs/product-spec.md) for the full authored
convention and the [glossary](docs/glossary.md) for the vocabulary.

## The same folder, later

The measure of the product is that the journey above is only the first leg:

- the same folder runs headless — `tenon run` dispatches bounded JSONL turns
  through the native harness;
- runs scheduled — `tenon schedule run` is an explicit foreground UTC clock
  over Markdown cron files;
- stages for deployment — `tenon stage` prepares a complete runnable
  filesystem tree for an existing OCI builder;
- and a revision applies, runs, and attributes to its exact configuration
  without human hands — an optional agent manifest pins the runtime closure,
  and every apply and dispatch event carries the source fingerprint so
  outside observation joins back to the exact configuration that produced
  it.

All of it without editing the folder.

## Status

Pre-implementation. The [product specification](docs/product-spec.md) is the
binding contract, [docs/workbench/status.md](docs/workbench/status.md) tracks
the gap, and the [north star](docs/north-star.md) governs every decision.
Tenon was prototyped as `hctl` in
[alee792/hctl](https://github.com/alee792/hctl), now the frozen, read-only
reference implementation.
