# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

The first release, v0.1.0, ships the core described in
[the product specification](docs/product-spec.md).

### Added

- `tenon drift` reports per-file divergence between a workspace and its
  apply record without mutating anything, and `tenon apply --discard-local`
  explicitly overwrites modified tenon-owned files (hand-authored files stay
  refused).

- `tenon apply` and `tenon validate` compile one filesystem-authored agent
  project (`instructions.md`, `skills/`, `plugins/`, `tools/`,
  `subagents/`, `connections/`, `schedules/`, `harnesses/`)
  deterministically into native configuration for Claude Code and Codex,
  refusing hand-authored and modified-owned files before any mutation and
  reporting failures as prose or stable-identifier JSONL diagnostics.
- Authored TypeScript, Python, and Go tools, and vendored Agent Plugin
  skills and MCP declarations, join subagents and native connections on
  one managed MCP boundary, with content-free lifecycle audit.
- `tenon run` dispatches bounded JSONL turns through the real Claude and
  Codex drivers with FIFO queuing, dedup, and session resume.
- `tenon schedule run` is a foreground UTC cron clock.
- `tenon stage` prepares a deterministic runnable filesystem tree for an
  OCI builder.
- An optional agent manifest pins the runtime closure (harness version,
  model, tenon version, integration-package identities); apply and every
  tenon-owned process open verify the pinned harness version,
  integration-package identities, and source fingerprint, and every apply
  and dispatch event attributes back to its exact configuration. The
  model field is pinned and emitted into harness configuration but is not
  itself verified — the harness owns model selection, and tenon does not
  claim to check which model actually served a turn.

### Known limitations

See [the specification's known limitations](docs/product-spec.md#known-limitations)
for the full list. Notably:

- Staging cannot yet serve authored tools end to end in any language
  (ADR 0021); the per-language closures land in issues #14–#17.
- A supplied manifest is verified at `tenon run`'s session start, not
  re-verified per turn within that session (`schedule run` re-verifies
  each occurrence).
- The Codex driver's successful-turn path has not been validated live —
  only its credential-safe 401 classification has.

### Compatibility policy (0.x)

Within the 0.x series: the authored folder convention (`instructions.md`
and the component directories above) and diagnostic identifiers are stable.
Command names, flags, and generated-file mechanics are the reference
rendering of the specification's responsibilities, not the contract itself,
and may change with a changelog entry.
