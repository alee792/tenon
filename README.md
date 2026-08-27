# Tenon

Tenon makes an agent something you can read: a folder of plain-language
files — instructions, skills, tools — that `tenon apply` compiles into
native configuration for the coding-agent harness you already trust, Claude
Code or Codex. One portable source of truth, proven valid before it touches
a workspace, kept honest afterward by drift detection. That guarantees
portability of an agent's declared capability surface — skills, tools, MCP
servers, instructions — across harnesses; the harness itself still owns
context assembly, pruning, and model-loop behavior, and always will.

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

## Staging for deployment

`tenon stage` prepares one complete runnable filesystem tree at canonical
paths for an existing OCI builder — only the execution closure the agent's
tools actually need, no build toolchains, credentials, or trust decisions:

```dockerfile
FROM <tenon harness image> AS build
COPY . /agent
RUN tenon stage /agent --harness codex --output /out/agent

FROM DOCUMENTED_COMPATIBLE_BASE
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/tenon/ /home/tenon/
USER 65532:65532
ENTRYPOINT ["/opt/tenon/bin/agent-entrypoint"]
```

See [staged agent filesystems](docs/product-spec.md#staged-agent-filesystems)
for the full contract.

## Revising your own files

If you are the loop — editing this agent's own `skills/`, `tools/`,
`plugins/`, or `instructions.md` to try a revision — the cycle is the same
one a person runs, without hands. Mutate the files, then validate:

```sh
tenon validate . --harness claude --diagnostics jsonl   # or: --harness codex
```

Each failure is one JSON line carrying a stable identifier and the authored
path; self-correct against the identifier, not by parsing prose — the
identifiers hold across releases and match apply's own failures. Once
validate reports nothing, apply the revision:

```sh
tenon apply . --harness claude   # or: --harness codex
```

Apply materializes the revision into the workspace and records a source
fingerprint with the apply, tying the revision to its exact configuration —
whatever runs next joins back to that fingerprint.

## Reproducible baselines

Every apply and dispatch event carries the source fingerprint, and an
optional [agent manifest](docs/product-spec.md) pins what the directory
alone cannot express — harness version, model, tenon version,
installed-package identities. Together they give RSI, eval, and
cross-harness-comparison work a fixed starting point: apply the same source
under the same manifest to Claude Code and Codex to replay identical
starting agent state and compare harness behavior, or give an improvement
loop a fixed baseline to score successive revisions against. Scoring itself
stays outside tenon — it collects no transcripts, evaluations, or scores;
only the fingerprint and, when supplied, the manifest identity travel with
each run, so whatever observation happens outside tenon joins back to the
exact configuration that produced it.

## Status

The [product specification](docs/product-spec.md)'s acceptance list is
implemented and credential-free tested, with staging's authored-tool
execution not yet runnable end to end (ADR 0021; see the specification's
[known limitations](docs/product-spec.md#known-limitations)). The
[north star](docs/north-star.md) governs every decision. Tenon was
prototyped as `hctl` in
[alee792/hctl](https://github.com/alee792/hctl), now the frozen, read-only
reference implementation.
