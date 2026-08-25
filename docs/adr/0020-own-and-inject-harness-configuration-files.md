# ADR 0020: Own and inject harness configuration files

- Status: accepted
- Amends: [north star](../north-star.md) commitment 2 (dedicated amendment,
  per that file's change process)

## Decision

Tenon owns the harness's native configuration files and injects compiled
configuration into them, the same way it already owns and generates
`.codex/config.toml` and `.claude/.mcp.json`. This extends to any config file a
pin needs to reach — beginning with the model pin, which lands in
`.codex/config.toml` (a `model` key) and `.claude/settings.json` (a `model`
key).

The author still controls the content. Vendor-specific configuration is
authored under `harnesses/<harness>/` (for example
`harnesses/claude/.claude/settings.json`); tenon reads that authored file as
the base, injects the pinned values on top, and writes the owned generated
file. Where the author provides no base, tenon writes one carrying only the
injected values. Config files therefore shift from pure byte-for-byte
passthrough to passthrough-with-injection; an injected key overrides the same
authored key, and the generated file is ownership-protected and disposable
like every other generated file.

The harness keeps runtime ownership of what those values govern: it selects the
model, enforces approvals, manages context, and runs the loop. Tenon emits an
authored preference; it does not enforce it. `.claude/settings.local.json`
(Claude's personal, unversioned settings) is never read or written by tenon, so
an author retains a purely hand-managed surface.

## Context

The agent manifest pins a model that must reach the running harness to be real
(a self-improvement variant that changes only the model pin must actually run
under that model). The model is emitted through each harness's documented
configuration file. `.codex/config.toml` is already tenon-owned, so the Codex
path is a new key on an existing owned file. `.claude/settings.json` was
previously author-only passthrough, so honoring the pin there means tenon owns
and injects that file.

The apparent tension — commitment 2 lists "approvals" among what tenon never
absorbs, and `settings.json` can carry approvals — is resolved by the
distinction the amendment draws: emitting an authored configuration *value* is
not absorbing the runtime *behavior* that value governs. The harness still
decides whether to prompt. The north star is amended in the same spirit, not
weakened.

## North-star reconciliation

Serves commitment 2 (tenon owns the crossing: compiling portable source,
including pins, into native integration) and commitment 1 (the configuration
stays authored source a person reads and diffs; the generated file is owned and
disposable). It tensions nothing ranked higher. The boundary it moves — tenon
owning config files it previously passed through — is recorded here and in the
amended commitment 2 rather than assumed.

## Consequences

- Model pins are emitted into `.codex/config.toml` and `.claude/settings.json`;
  a supplied manifest that pins no model changes nothing.
- `.claude/settings.json` becomes a tenon-owned generated destination; an
  authored base under `harnesses/claude/` is read and injected into, not copied
  blindly, and reserved-destination and ownership rules apply to it.
- The model value stays out of model-facing content (it is native
  configuration, not instructions) and, per the manifest contract, is recorded
  but not drift-verified — the harness owns which model actually serves a turn.
- A portable format for the intersection of popular harness settings
  (permissions and the like), translated per harness, is deferred to
  investigation (issue #10), not decided here.
