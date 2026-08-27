# Changelog

All notable changes to this project are documented in this file.

## [0.1.0] - UNRELEASED

The first release, v0.1.0, ships the core described in
[the product specification](docs/product-spec.md).

### Added

- Python authored tools run from a self-contained execution closure (ADR
  0021): preparation installs a pinned, checksum-verified standalone
  CPython plus the project's `uv export --locked` dependencies flat beside
  it, with no venv, no `pyvenv.cfg`, and no interpreter symlink — `uv` never
  runs at serve time, and launch execs the closure's own interpreter
  directly, identically for `tenon apply`/`tenon mcp serve` and for a
  staged tree. `tenon stage` now stages and serves Python-tool agents (Go
  and Python both stage today; TypeScript remains refused with a named
  diagnostic pending its own rendering spike). The staged manifest records
  the interpreter's identity and ABI (for example
  `cpython-3.11.13-linux-x86_64-gnu`). Preparing a Python-tool agent
  requires the network on every run, not only the first (`uv` does not
  cache the interpreter download itself); the exact interpreter installed
  is the floor of `pyproject.toml`'s `requires-python` range, or a
  `.python-version` file's exact pin when present, which takes precedence.
  The build-machine-path scan staging runs before publishing now routes by
  a file's provenance — component matching for text tenon itself generates
  or renders, joined-path matching for a carried-in runtime payload (the
  interpreter tree, the dependency directory, a compiled host binary, the
  tenon executable) — rather than by whether the file's own bytes happen to
  look binary, which false-positived on CPython's thousands of ordinary
  stdlib and header text files. The agent manifest's `tool_runtimes` gains
  a `python` pin: the resolved Python version specification a project's
  own `requires-python`/`.python-version` names (not the exact installed
  interpreter identity, which is not yet known at manifest-resolve time —
  manifest verification runs before tool preparation — and which the
  staged artifact manifest already carries once preparation has run).
  Python closure symlink removal now walks the whole `cpython/` install
  root exhaustively (deleting any symlink whose target resolves inside the
  closure, failing closed on one that does not, and asserting none survive)
  instead of enumerating specific paths a known uv release produces: an
  unpinned CI `setup-uv` step resolving uv 0.12.6 instead of this repo's
  pinned 0.8.17 surfaced a minor-version symlink beside the versioned
  interpreter directory that the narrower, enumerated walk missed. CI's
  `setup-uv` step is now pinned to `0.8.17`, matching `images/inputs.json`.

- `tenon drift` reports per-file divergence between a workspace and its
  apply record without mutating anything, and `tenon apply --discard-local`
  explicitly overwrites modified tenon-owned files (hand-authored files stay
  refused).

- Release archives cover `darwin-arm64`, `linux-amd64`, and `linux-arm64`
  (ADR 0022), each `tenon_X.Y.Z_<os>_<arch>.tar.gz` holding exactly one
  `tenon` executable at its root, alongside one `tenon_X.Y.Z_SHA256SUMS`
  covering all three. Archives are byte-identical for a given tag and Go
  toolchain — build timestamps derive from the tagged commit rather than
  the clock — and CI's "Release build is reproducible" job builds the real
  release path twice against a throwaway tag and fails if the checksums
  differ. `scripts/release.sh` builds the archives; the `Release` GitHub
  Actions workflow runs on an exact `vX.Y.Z` tag push and publishes them,
  marking a pre-release-suffixed tag (`v0.1.0-rc.1`) as a GitHub
  pre-release automatically. `tenon version` reports the version stamped
  at build time from the tag, closing the gap where every binary
  previously reported a hardcoded `0.1.0-dev` regardless of what an agent
  manifest's `tenon_version` pin expected.

- `docs/harness-images.md` and `images/<claude|codex>/Dockerfile` define
  the compatible-base contract, the two build journeys (direct apply, and
  two-stage selective staging), the credential boundary, and what gates
  publication of each harness image; neither image is published yet, and
  publication of the Claude image additionally awaits an Anthropic terms
  review (issue #19).

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

- Staging serves Go and Python authored tools end to end from the staged
  tree today (ADR 0021); TypeScript remains refused with a named
  diagnostic (`stage.tools.runtime-unsupported`) pending its own bounded
  rendering spike (issue #16).
- Python tool preparation fetches the pinned standalone CPython
  interpreter on every `validate`/`apply` run, not only the first, because
  `uv` does not cache the interpreter download itself; a network-restricted
  machine needs it reachable through whatever channel supplies tenon's
  other pinned inputs.
- A supplied manifest is verified at `tenon run`'s session start, not
  re-verified per turn within that session (`schedule run` re-verifies
  each occurrence).
- The Codex driver's successful-turn path has not been validated live —
  only its credential-safe 401 classification has.
- Neither harness image (`docs/harness-images.md`) is published; the Claude
  image additionally awaits an Anthropic terms review before it may be
  published at all (issue #19).

### Compatibility policy (0.x)

Within the 0.x series: the authored folder convention (`instructions.md`
and the component directories above) and diagnostic identifiers are stable.
Command names, flags, and generated-file mechanics are the reference
rendering of the specification's responsibilities, not the contract itself,
and may change with a changelog entry.
