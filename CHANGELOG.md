# Changelog

All notable changes to this project are documented in this file.

## [0.1.0] - UNRELEASED

The first release, v0.1.0, ships the core described in
[the product specification](docs/product-spec.md).

### Added

- `tenon stage` never emits a Go tool tree that verifies but cannot serve
  (issue #14): the staged apply record now names the closure root relative
  to the workspace, and `tenon mcp serve` honors it when present, so a
  staged Go-tool tree serves tool calls directly with no workspace tool
  cache ever prepared. The generated Go host's `go.mod` replace directive
  now names the agent source relative to its own build directory, so the
  build-machine path is never embedded in the built binary in the first
  place (previously visible via `go version -m` even after a `-trimpath`
  build). Staging refused a TypeScript-bearing agent with a named
  diagnostic (`stage.tools.runtime-unsupported`) before any mutation, until
  TypeScript's own closure landed (see below); `apply`/`validate`/`mcp
  serve` were unaffected throughout and continued to work locally for
  every language.

- Python authored tools run from a self-contained execution closure (ADR
  0021): preparation installs a pinned, checksum-verified standalone
  CPython plus the project's `uv export --locked` dependencies flat beside
  it, with no venv, no `pyvenv.cfg`, and no interpreter symlink — `uv` never
  runs at serve time, and launch execs the closure's own interpreter
  directly, identically for `tenon apply`/`tenon mcp serve` and for a
  staged tree. `tenon stage` now stages and serves Python-tool agents too
  (Go and Python both stage and serve; TypeScript follows below). The
  staged manifest records the interpreter's identity and ABI (for example
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

- TypeScript authored tools run from a self-contained execution closure too
  (ADR 0021, issue #16), lifting the `stage.tools.runtime-unsupported`
  refusal: a bounded spike weighed `deno compile` against the
  prototype-proven fallback and landed the fallback (the spike's decisive
  `deno compile` unknown — whether `--include`d source resolves once the
  closure relocates — could not be verified in a sandboxed environment
  that denies Deno's own egress). Preparation copies the resolved `deno`
  executable whole into the closure, then, once inspection's own launch
  has started and stopped the host (itself a `deno run` that regenerates
  whatever derived caches it needs, keyed to preparation's own paths),
  prunes `DENO_DIR` back down to its actually-downloaded package cache —
  hctl's prototype prune list ported forward and corrected for a newer
  Deno release's on-disk cache format (flat `check_cache_v2`-style files
  rather than a `gen/`-and-per-module-`registry.json` tree), still
  discarding `node_compat_bin`'s link back to the build-time executable.
  Launch execs the closure's own `deno` directly, at a fixed
  closure-relative path resolved fresh from the closure's current
  location rather than a path recorded at preparation, matching Go's and
  Python's closures (the `deno`-path entry in the tool cache's recorded
  executables — a preparation-machine absolute path, unusable once a
  closure relocates — is gone along with the mechanism that carried it).
  `tenon stage` now stages and serves TypeScript-tool agents too (Go,
  Python, and TypeScript all stage and serve today). The build-machine-path
  scan's carried-payload routing and its one-file size bound both extend to
  cover the copied `deno` executable and the pruned `DENO_DIR`.

- The pinned CPython interpreter and `deno` executable are shared across
  every agent project on one machine through a content-addressed runtime
  cache under `os.UserCacheDir()/tenon/runtimes/` (issue #38), rather than
  each agent's closure installing or copying its own: the first prepare to
  resolve a given interpreter identity or `deno` content hash installs it
  once, normalizes it (Python) or validates it (TypeScript), and locks it
  read-only, guarded by a namespace-scoped advisory lock so two concurrent
  `tenon` processes never race the same install; every later resolution —
  another agent, a repeat `apply`, or `validate`'s own throwaway prepare,
  which previously reinstalled a full CPython on every single call — gets
  it via hardlink (falling back to a real copy across a filesystem
  boundary) rather than reinstalling or recopying. An exact `.python-version`
  pin can resolve to a cache hit without invoking `uv` at all; a floor
  `requires-python` spec still calls `uv python install` to resolve the
  exact patch, but only reaches the network the first time any agent
  resolves that identity on the machine. Fixed a real correctness gap this
  surfaced: CPython's standalone build bakes its own install directory into
  `_sysconfigdata_*.py`, so a shared, multiply-referenced install can never
  be hardlinked into a closure like every other interpreter file — this one
  file is copied instead and rewritten to the per-agent closure's own path
  (the same rewrite `tenon stage` already performs for its own
  prepared-to-final-staged relocation, now shared as one exported
  function). Every downstream consumer of a per-agent closure —
  `pythonClosureLayout`, `hostCommand`, `verifyCache` — is unchanged: a
  hardlinked file satisfies every existing regular-file check identically
  to an independently installed one, so only how a closure gets populated
  changed, never its on-disk shape or what staging or serving read from it.

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
  Actions workflow triggers on any pushed tag matching `v*.*.*` (which
  also matches a pre-release-suffixed rehearsal tag like `v0.1.0-rc.1`,
  not only a clean `vX.Y.Z`) and publishes them, marking the suffixed
  case as a GitHub pre-release automatically. `tenon version` reports the
  version stamped at build time from the tag, closing the gap where every
  binary previously reported a hardcoded `0.1.0-dev` regardless of what an
  agent manifest's `tenon_version` pin expected.

- `docs/harness-images.md` and `images/<claude|codex>/Dockerfile` define
  the compatible-base contract, the two build journeys (direct apply, and
  two-stage selective staging), the credential boundary, and what gates
  publication of each harness image; neither image is published yet, and
  publication of the Claude image additionally awaits an Anthropic terms
  review (issue #19).

- `tenon fingerprint show` reports an agent's source fingerprint and its
  per-file digests without applying anything; `tenon apply` records the
  clean working tree's git commit SHA alongside the apply record when the
  source is a clean git checkout; and `apply`/`validate` accept
  `--diagnostics jsonl` to emit a single structured JSON summary line
  instead of prose, for scripted and outer-loop consumption —
  `{agent, fingerprint}` for `validate`, and `{agent, harness, workspace,
  fingerprint, written, removed, managed_tools}` for `apply`.

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

- Locked Python dependencies (`uv export`/`uv pip install`) and TypeScript
  type-checking (`deno check`) still run on every `validate`/`apply`, since
  they are specific to each project's own source; only the pinned CPython
  interpreter and `deno` executable themselves are shared machine-wide
  (issue #38), not a project's locked dependencies.
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
