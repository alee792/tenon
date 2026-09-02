# Changelog

All notable changes to this project are documented in this file.

## [0.1.0] - UNRELEASED

The first release, v0.1.0, ships the core described in
[the product specification](docs/product-spec.md).

### Added

- `tenon check` is now the single gate over an agent project, absorbing
  `tenon validate` and `tenon fingerprint show`
  ([ADR 0027](docs/adr/0027-consolidate-the-read-surface.md)). Without
  `--harness` it is a portable gate — load, bound, prove tool contracts,
  prepare tools — that validate never offered; with `--harness` it is
  everything apply does short of writing, so check and apply still fail
  identically on the same source. `--emit` names the inventories the gate
  already resolved and emits them only once it passes, always files before
  catalog: `--emit files` is the per-file fingerprint stream
  `fingerprint show` emitted, and `--emit catalog` is the resolved
  capability inventory (skills including plugin-merged ones with their
  descriptions, tools with their language, MCP servers, subagents,
  schedules). A catalog is derived only, never accepted as input.

- The supplied agent manifest is renamed to the **pin set**, and the gate
  writes it. `--manifest PATH` becomes `--pins FILE` on `apply`, `drift`,
  `run`, `schedule`, `mcp`, `plugin`, and `check`; the `manifest` command is
  gone, replaced by `tenon check AGENT --harness H --write-pins FILE`, so a
  pin set can only be minted by a project that passes the gate right now,
  bound to the fingerprint just proven. `--model VALUE`, valid only with
  `--write-pins`, records the operator's advisory model. With `--write-pins`
  and no `--pins`, check loads for write, so an instructions-free
  loop-generated directory can still mint the pin set that later proves it.
  Diagnostic identifiers rename from `manifest.*` to `pins.*`
  (`manifest.drift.agent` → `pins.drift.agent`, and so on) — a one-time
  pre-release break in an otherwise stable surface. Integration *package*
  manifests keep their `manifest.*` identifiers: that document really is a
  list of contents.

- `tenon clean --workspace DIR [--harness <claude|codex>] [--force]
  [--format <prose|jsonl>]` is the inverse of apply: it removes the files
  the workspace's apply record(s) own, prunes the directories emptying them
  leaves behind, and drops the record(s) — the harness-switch and uninstall
  stories neither apply nor drift covered. It takes no AGENT, so it works
  when the source is gone. A file modified since its apply refuses the whole
  clean unless `--force` is passed; a file tenon never recorded is never
  touched either way; a workspace with no records succeeds trivially, and a
  record owning no files is still dropped. That trivial success belongs to
  the bare clean alone: `clean --harness H` over a workspace holding no
  `apply-H.json` record exits 1 with
  `tenon clean: no claude record in WORKSPACE/.tenon; nothing to clean for
  that harness` and the `error` outcome, because the operator named a
  harness this workspace was never applied for and reporting success would
  read as "that harness is now clean". Clean never trusts the paths in a
  record verbatim: one that is not workspace-local, or one reached through a
  parent that is a symlink rather than a real directory, blocks the clean
  (`escapes-workspace`, `symlink-parent`) with or without `--force`, every
  path is re-classified immediately before its own removal, and the
  directory pruning is bounded by the workspace itself. A parent chain that
  cannot be read at all blocks the same way (`unreadable-parent`), naming
  what was actually observed rather than claiming a symlink. Clean's
  all-or-nothing refusal is decided at plan time: a path that changes
  between the plan and its own removal stops the clean where it stands, the
  jsonl stream ends `{"outcome":"blocked"}`, the prose names the path, and
  the record is kept so a re-run can finish. A `.tenon` file naming no
  harness tenon knows is reported as
  `{"ignored":NAME,"reason":"unknown-harness"}` and left alone; the clean
  continues. Apply enforces the same containment rule on its own removal of
  stale recorded files (`apply.record.unsafe-path`) and on every file it
  writes (`apply.workspace.unsafe-path`), so a generated parent directory
  swapped for a symlink cannot make an atomic write land outside the
  workspace.

- `--diagnostics` is renamed `--format` everywhere, since the flag governs
  all output rendering rather than diagnostics alone; there is no deprecated
  alias. An unset `--harness` now falls back to the `TENON_HARNESS`
  environment variable — the explicit flag always wins, and an invalid
  environment value is reported as coming from the environment — except in
  `clean`, which ignores it deliberately so that a bare clean still means
  "every harness recorded here". Every jsonl stream now ends with one
  distinct object carrying an `outcome` field: `ok` from check, apply,
  drift, clean, and stage (including `stage verify`), `gate_failed` when the
  source itself is invalid, `drift`
  when the workspace no longer matches a fresh apply, `blocked` when
  clean refuses, and `error` when the run could not complete for a reason
  that is not the source's fault — an unreadable pin set, an unwritable
  path, a closure that would not resolve, an os error mid-clean, a harness
  that would not start. The full vocabulary is
  `ok / gate_failed / drift / blocked / error`. The first four are findings
  a loop scores; `error` is a statement about the environment, which the
  loop retries or escalates and never scores. An `error` object carries the
  same prose stderr carries, bounded, so a consumer reading only the stream
  still learns what went wrong. Usage errors remain the one deliberate
  exception: exit 2, and no outcome object at all, because a malformed
  invocation never ran. `tenon run`, whose stdout IS the wire event stream,
  ends it the same way — `{"outcome":"ok"}` after a clean dispatch, and the
  error or `gate_failed` object on failure. Check's success object keeps `agent` and `fingerprint` and
  adds `pins_written` when `--write-pins` wrote one; `outcome` is the only
  field every result object carries, and the rest vary by command. `stage`
  honors `--format` too, ending a jsonl run with its own result object
  (agent, fingerprint, output directory) instead of prose, and `stage verify`
  honors it as well, ending a jsonl run with
  `{"outcome":"ok","artifact":PATH}` or the `gate_failed` object. `drift`
  against a workspace that does not exist now reports `drift` with every
  generated path missing, rather than `gate_failed` for a source that is
  fine — its gate, authored-tool preparation included, runs against the
  source rather than the workspace, so a tool-bearing agent reports the same
  thing a tool-free one does. A path passed as `--workspace` that exists but
  is a regular file is neither drift nor a gate failure: it is a usage
  error, exit 2 with no outcome object.

- `--emit catalog` reports an MCP entry's `transport` in one vocabulary
  whichever side declared the server — `stdio`, `streamable-http`, or
  `installed` — so an authored connection's kind and a plugin-declared
  server's transport are directly comparable; `source` remains what
  distinguishes where an entry came from.

- Standalone MCP connections move from `connections/` to `mcp/`, re-shaped to
  the Agent Plugins 1.0 `mcp.json` server-entry vocabulary (issue #49): a
  remote connection now declares `type: streamable-http` (replacing
  `type: mcp` + `transport: streamable-http`) with an optional `headers` map,
  whose values may end with exactly one `${VAR}` environment-variable
  reference (never resolved by tenon, and never `${PLUGIN_ROOT}`/
  `${PLUGIN_DATA}`); an installed connection now declares `type: installed`.
  `type: sse` fails as a deprecated transport (repo-relative `type: stdio`
  landed separately, below, issue #50). The credential-free-only restriction
  on remote targets is dropped — an OAuth-requiring endpoint is fine to
  declare, since the harness alone discovers and performs authentication. A
  leftover `connections/` directory fails closed with a migration diagnostic
  (`mcp.migration.connections-dir`) naming `mcp/`, rather than being silently
  ignored. Declared headers render verbatim into Claude's `.mcp.json`; Codex,
  whose generated configuration has no header support, warns and omits them
  (`mcp.header.not-honored`). The CLI verb renames from `connection` to
  `mcp`: `tenon mcp add|status|remove` replace `tenon connection
  add|status|remove`, and `add` gained a repeatable `--header 'Name: Value'`
  flag. Diagnostic identifiers rename from `connection.*` to `mcp.*`
  (`connection.entry.invalid` → `mcp.entry.invalid`, and so on), plus new
  identifiers `mcp.transport.invalid`, `mcp.header.invalid`, and
  `mcp.migration.connections-dir`.

- Repo-relative `type: stdio` authored MCP servers (ADR 0026, issue #50): an
  `mcp/<name>.md` may now declare `command` (agent-root-relative, `./…`,
  containment-validated the way a plugin-relative command is but anchored at
  the agent root — a bare PATH-resolved name or an absolute or escaping path
  is refused before workspace mutation), plus optional `args` (plain
  strings; a value naming `${PLUGIN_ROOT}` or `${PLUGIN_DATA}` is refused by
  name, since authored stdio args carry no placeholder expansion of any
  kind), `env` (the identical `${VAR}` value grammar `headers` already
  enforces), and `cwd` (the same containment rule as `command`, defaulting
  to the agent root when absent). The resolved command file's exact bytes
  and executable bit join the project fingerprint, exactly like a
  plugin-relative stdio command. A project may declare at most 16 `type:
  stdio` servers, with at most 64 MiB combined across every distinct
  resolved command file (ADR 0026's previously open executable-budget item,
  now recorded). Claude's `.mcp.json` renders the resolved command wrapped
  in the same `/usr/bin/env -C` working-directory adapter a plugin stdio
  server with a declared `cwd` already uses, with `env` verbatim. Codex's
  `.codex/config.toml` renders `command`/`args`/`cwd` directly; an `env`
  value that is a literal is emitted verbatim, a bare `${VAR}` reference is
  forwarded by name only through `env_vars` (the same mechanism an installed
  connection's required ambient name already uses, so the ambient value
  itself is still never read or copied), and a value carrying a literal
  prefix before its `${VAR}` reference cannot be represented that way and is
  reported and omitted (`mcp.env.not-honored`). New diagnostic identifiers:
  `mcp.command.invalid`, `mcp.command.not-executable`, `mcp.cwd.invalid`,
  `mcp.args.invalid`, `mcp.env.invalid`, `mcp.env.not-honored`, and
  `mcp.stdio.bounds.exceeded`.

- Plugin acquisition by pointer and pin (issue #52, ADR 0026 § plugin
  acquisition): `plugins/<name>.md` may now declare a plugin by reference
  instead of vendoring it, with closed frontmatter naming an absolute HTTPS
  `source` and a full 40-character commit `rev`, plus an optional bounded
  provenance body never rendered into instructions. A new `tenon plugin`
  verb adds three commands: `tenon plugin fetch AGENT [NAME]`, the one
  explicitly online step, resolves each reference into a new owner-only,
  content-addressed plugin cache (`internal/pluginref`) by shelling out to
  the system `git` executable; `tenon plugin update AGENT NAME --rev REV`
  fetches the new revision, prints a bounded added/removed/changed
  component-path diff against the currently pinned revision, and only then
  rewrites the reference file's `rev`; `tenon plugin status AGENT [NAME]`
  reports each reference's declared pin and offline resolution health.
  `tenon apply`, `tenon validate`, and every other project load stay fully
  offline: a reference file resolves against the cache with an offline
  digest re-verification and fails, naming `tenon plugin fetch`, when the
  pin is not cached or no longer matches its recorded digest. A resolved
  reference's plugin tree loads through the identical manifest, `skills/`,
  and `mcp.json` validation a vendored `plugins/<name>/` directory uses, and
  its resolved bytes join the project fingerprint on the same terms as
  vendored bytes; the reference file's own bytes always join the
  fingerprint too. (A reference and a directory sharing a name failed the
  project as `plugin.entry.collision` when this slice landed; issue #58,
  below, replaced that rule with materialized references and retired the
  identifier.) Vendoring a complete directory beneath
  `plugins/<name>/` remains fully supported and requires none of this. New
  diagnostic identifiers: `plugin.entry.invalid` (extended to cover
  malformed reference filenames),
  `plugin.reference.invalid`, `plugin.reference.frontmatter.missing`,
  `plugin.reference.frontmatter.invalid`,
  `plugin.reference.frontmatter.unknown-field`,
  `plugin.reference.source.invalid`, `plugin.reference.rev.invalid`,
  `plugin.reference.body.too-long`, and `plugin.reference.unresolved`.
  `agentproject.PluginCache.Resolve` now takes both the declared source and
  the rev (previously rev alone), so a rev reused under a different
  declared source is caught at `Load` time itself — a rev is
  content-addressed, not source-addressed, so this is a swap the digest
  check alone cannot catch — rather than only by a later `tenon plugin
  fetch`/`status`, which already re-checked it independently.

- Composition policy split by relationship (issue #53, ADR 0026 §
  composition policy): an authored `mcp/<name>.md` server declaration
  colliding with an accepted plugin server of the same name now wins
  instead of failing the project — the authored server is emitted, the
  plugin's is not, and a warning (`mcp.name.shadowed`) names both sources.
  Plugin-to-plugin collisions are unchanged (still ADR 0010's
  first-wins-with-warning, never fail-closed). A new closed frontmatter
  form masks a plugin's server outright, with no authored replacement:
  exactly `override: plugins/<storage-name>` and `enabled: false`, no
  `type` and no other field, and no body — masking is deliberate, so it
  produces no warning; the
  mask file is the record. A dangling override (the named plugin absent, or
  present but not actually contributing a server named for that file) fails
  before workspace mutation (`mcp.override.dangling`), as does `enabled:
  true` (`mcp.override.enabled` — a true mask would be a no-op) and a
  non-empty body (`mcp.override.body` — a mask declares absence, not
  guidance); a malformed `override` value fails as `mcp.override.invalid`.
  `managed` remains reserved and unmaskable. Suppression is computed once in
  `internal/agentproject`, so both native drivers render the identical
  composed server set with no per-driver collision logic. New diagnostic
  identifiers: `mcp.name.shadowed`, `mcp.override.invalid`,
  `mcp.override.dangling`, `mcp.override.enabled`, and `mcp.override.body`.

- `tenon mcp status` is now the one offline view of an agent's entire
  composed MCP surface (issue #54), not just its authored connections: it
  loads plugins alongside `mcp/`, reusing the identical composition
  `tenon apply`/`validate` already perform, and reports one row per
  authored connection (unchanged), one row per accepted plugin-provided
  server (`target=plugin`), one row per plugin server an authored
  connection shadows (`shadowed-by=<path>`), and one row per masking
  declaration (`target=mask`). An authored connection's `${VAR}`-backed
  header or stdio env values are also named — never their values — matching
  `tenon integration inspect`'s existing convention. A handful of review
  findings from issue #53 land alongside this rework: a bare `mcp status`
  no longer reports a false `mcp.override.dangling` on a legitimate mask
  (a regression in the prior, plugin-blind status path); the masking union
  arm now triggers on the presence of `override` alone, not `override` or
  `enabled`, so a type-less server declaration missing `enabled` is
  reported as a missing `type` rather than a misleading masking error; a
  wrong-typed `enabled` now fails as `mcp.frontmatter.invalid`, the
  identifier already established for a present field of the wrong YAML
  type, rather than `mcp.override.invalid`; `override: plugins/x.md` now
  fails as `mcp.override.invalid` with a hint to name the plugin
  (`plugins/x`), not its reference file; and a mask naming a plugin that
  did declare the overridden server but lost a plugin-to-plugin naming
  collision (ADR 0010, unchanged) now names the winning plugin explicitly
  instead of reporting a generic dangling override.

- A plugin reference's resolved content is materialized into the staged
  tree, and the loader learned to read it back (issue #58, ADR 0026 §
  plugin acquisition, now accepted): `tenon stage` copies each
  `plugins/<name>.md` reference's resolved cache tree — re-resolved and
  digest-re-verified immediately before the copy, so a cache pruned or
  tampered with between load and staging fails the stage closed — into the
  staged filesystem at `plugins/<name>/`, and the generated configuration
  anchors `PLUGIN_ROOT` and any plugin-relative command inside the staged
  tree instead of at the operator's cache. A reference file with a
  directory of the same name beside it is now a *materialized reference*
  rather than a collision: the directory is that reference's pinned
  content, it takes precedence over the plugin cache, and its components
  load through the identical path a cache-resolved reference uses, under
  the same synthetic authored root — so the fingerprint is byte-identical
  whether the content came from the cache at build time or from the tree
  afterward, which is what lets a staged tree re-load, verify, and start.
  A materialized reference needs no plugin cache and no network at all,
  which is exactly the container's situation. There is no `git` in the
  tree to re-check the pin against those bytes: the fingerprint is their
  integrity check, so any change to them is caught by `tenon stage verify`
  and by drift detection, and a plain load trusts them as it trusts all
  other authored source. `plugin.entry.collision` is retired with the rule
  it reported. `tenon mcp status` marks a cache-resolved reference's
  servers `cache-dependent=true` (a vendored or materialized plugin's
  servers carry no marker), and `tenon plugin status` reports each
  reference as `resolved cache-dependent=true` or `materialized
  cache-dependent=false`, with the prose caveat printed once after the
  listing rather than on every row. A plain `tenon apply` still points a
  cache-resolved reference at the operator's cache by design. Staging now
  re-loads the staged agent source and proves it reproduces the fingerprint
  the artifact manifest is about to record, before writing that manifest and
  before publishing: a tree whose bytes are not the bytes that were loaded —
  a plugin cache entry mutated in the unlocked window between staging's
  re-verification of a reference and its copy, say — fails the stage closed
  with the new `stage.tree.fingerprint-mismatch` diagnostic and no output
  directory, rather than publishing a tree that only fails later at
  container open.

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
  `subagents/`, `mcp/`, `schedules/`, `harnesses/`)
  deterministically into native configuration for Claude Code and Codex,
  refusing hand-authored and modified-owned files before any mutation and
  reporting failures as prose or stable-identifier JSONL diagnostics.
- Authored TypeScript, Python, and Go tools reach both harnesses through one
  managed MCP boundary with content-free lifecycle audit; Agent Plugin
  skills and MCP declarations, subagents, and authored `mcp/` servers compile
  alongside it into ordinary native configuration the harness owns.
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
- A remote MCP server's behavior is not pinned by the source fingerprint: a
  hosted endpoint's tool catalog can change under an unchanged fingerprint.
- A plain `tenon apply` renders a cache-resolved plugin reference's servers
  against the operator's plugin cache, so pruning the cache breaks an
  already-applied workspace until the next `tenon plugin fetch` (issue #58).
  `tenon stage` is unaffected: it materializes the content into the tree.
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
