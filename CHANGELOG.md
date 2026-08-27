# Changelog

All notable changes to this project are documented in this file. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

The first release, v0.1.0, ships the core described in
[the product specification](docs/product-spec.md): `tenon apply` and
`tenon validate` compile one filesystem-authored agent project
(`instructions.md`, `skills/`, `plugins/`, `tools/`, `subagents/`,
`connections/`, `schedules/`, `harnesses/`) deterministically into native
configuration for Claude Code and Codex, refusing hand-authored and
modified-owned files before any mutation and reporting failures as prose or
stable-identifier JSONL diagnostics. Authored TypeScript, Python, and Go
tools and vendored Agent Plugin skills and MCP declarations join subagents
and native connections on one managed MCP boundary, with content-free
lifecycle audit. `tenon run` dispatches bounded JSONL turns through the real
Claude and Codex drivers with FIFO queuing, dedup, and session resume;
`tenon schedule run` is a foreground UTC cron clock; `tenon stage` prepares
a deterministic runnable filesystem tree for an OCI builder; and an optional
agent manifest pins and verifies the runtime closure (harness version,
model, tenon version, integration-package identities) so every apply and
dispatch event attributes back to its exact configuration. See
[known limitations](docs/product-spec.md#known-limitations) for what remains
honestly deferred.

### Compatibility policy (0.x)

Within the 0.x series: the authored folder convention (`instructions.md`
and the component directories above) and diagnostic identifiers are stable.
Command names, flags, and generated-file mechanics are the reference
rendering of the specification's responsibilities, not the contract itself,
and may change with a changelog entry.
