# Working status

- Updated: 2026-08-22

## Implemented

The first apply slice, credential-free tested: `tenon validate` and
`tenon apply` load an instructions-only project (strict closed frontmatter,
bounds, symlink rejection, name normalization, source fingerprint), render
the always-on surface for Claude (`CLAUDE.md`) and Codex (`AGENTS.md`) with
visible tool ownership, refuse hand-authored and modified-owned files before
any mutation, remove stale owned files, keep an owner-only atomic apply
record per workspace and harness, and report failures as prose or JSONL
diagnostics with stable identifiers, authored paths, and validate/apply
parity. Recognized component directories that are not yet compiled fail
validation rather than being silently dropped.

Skills, per the skill-compatibility matrix: `skills/NAME/SKILL.md` validates
against the Agent Skills standard through one strict YAML frontmatter
engine, resources round-trip byte-for-byte to both native skill locations
with executable intent preserved and fingerprinted, generated SKILL.md
carries one provenance-free ownership marker line, recognized vendor fields
and `agents/openai.yaml` pass through unchanged with per-harness warnings,
and ADR 0013's count and byte ceilings fail before mutation. All six
acceptance-evidence items are credential-free tested, and generation-time
warnings report identically from validate and apply.

Subagents, per ADRs 0004 and 0007: each immediate `subagents/NAME/`
directory carries only an `instructions.md` (description, optional
`low|medium|high` effort, non-empty body); anything else — child skills,
tools, dependency files, nested subagents — is rejected, not ignored.
Generation relies on native inheritance and emits only the child's routing
metadata and body: `.claude/agents/NAME.md` frontmatter with the exact
native effort field, and `.codex/agents/NAME.toml` with the underscored
name value, valid-TOML string escaping, and `model_reasoning_effort`,
each field omitted when effort is absent. Reserved built-in tool names are
refused, ADR 0013 ceilings enforced, and effort changes and subagent
deletion round-trip through ownership-checked reapply.

Harness-specific files: `harnesses/claude/.claude/` and
`harnesses/codex/.codex/` subtrees copy byte-for-byte, unparsed and
unmarked, to only the selected harness; tenon-owned destinations
(`.claude/skills/`, `.claude/agents/`, `.codex/agents/`,
`.codex/config.toml`) are refused including case-folded aliases, unknown
harness directories fail, and per-harness ADR 0013 ceilings (1,024 files,
1 MiB each, 8 MiB aggregate) hold. With skills, subagents, and this
round-trip landed, acceptance item 4 is complete.

The managed MCP boundary, first half: apply generates the `managed` stdio
server into Claude's `.mcp.json` and Codex's `.codex/config.toml` (required
and pre-approved for Codex, per spec) with the absolute resolved tenon
executable, and `tenon mcp serve` fails closed unless the workspace's
generated setup matches the apply record exactly. The server speaks MCP
2025-06-18 with strict decoding and bounded lines, exposes built-in `echo`
and the opt-in `record-friction` (owner-only per-agent inbox, 256-record
cap, never evicting), and writes content-free lifecycle audit — agent,
tool, hashed request ID, outcome; never arguments or output — proving
acceptance item 10. Authored tools are discovered and statically validated
and, as of the hosts slice below, fully served.

Authored tools, completing the managed boundary: `tools/*.ts`, `tools/*.py`,
and `tools/NAME/tool.go` compile through one fresh embedded host per
language speaking a bounded JSONL protocol (64 KiB call lines, 8 MiB
catalog, deadline and overflow kill the host, stderr never forwarded).
Apply prepares once per workspace — `deno check --frozen`,
`uv sync --locked`, an offline standard-library-only generated Go host —
records absolute executable receipts, and inspects every catalog before
mutation; validate runs the identical preparation against a throwaway
cache so parity holds while writing nothing. Authored tools join the
managed MCP surface behind the existing content-free audit, and the
mixed-language end-to-end proves one host process per language with the
same tool surface on both harnesses (acceptance item 2). Tool/subagent
name collisions fail before mutation, and a stale tool cache fails closed
at serve with a run-apply message.

Plugin skills, per ADR 0009: vendored Agent Plugins v1 directories under
`plugins/` validate their `plugin.json` locally against the exact v1.0.0
schema identifier without any fetch, and contribute skills from their fixed
`skills/` location through the same shared loader and aggregate budgets as
root skills. Component failure is isolated — an invalid plugin, skill, or
manifest warns and skips at its authored path while valid siblings
continue — root skills load first, and the first name wins with collisions
warned and never renamed. Plugin manifests and consumed resources join the
fingerprint, and imported skills round-trip byte-for-byte like root skills.

Plugin MCP, per ADR 0010, completing acceptance item 5: an accepted
plugin's `mcp.json` validates locally against the exact v1.0.0 schema,
`stdio` and `streamable-http` servers map into native configuration with
first-wins collision handling, `managed` reserved, and SSE warned and
skipped; `${PLUGIN_ROOT}` and `${PLUGIN_DATA}` expand exactly once with an
owner-only persistent per-agent-and-plugin data directory that never
enters the apply record; Claude's exec-adapter preserves working
directories its format lacks and placeholder-bearing values skip Claude
with a warning rather than risk ambient substitution; Codex renders remote
servers without headers and warns about the drop; and the 8 MiB rendered
configuration ceiling fails before mutation.

Remote connections, per ADR 0016 (installed-package target still gated): a
`connections/<name>.md` with closed `type: mcp` frontmatter and an
HTTPS-only `streamable-http` target generates a native http server entry
into both harnesses without contacting anything, and its optional context
renders once into a bounded "Native MCP connections" instructions section
that states the harness owns MCP startup, trust, and effects. Names
collide-check against `managed`, other connections, and plugin servers
before mutation; Claude-reserved names fail for Claude only; and
`tenon connection add/status/remove` author the files offline, never
overwriting and directing the author to reapply. The installed
`package`/`capability` target is honestly gated (connection.target.unsupported)
until the integration store wires in. Known refinement: `connection add`
currently validates the whole project for its collision check rather than a
connections-and-plugins-scoped load.

Integration package store, per ADR 0014 (native-mcp capability): a bounded
metadata-first `integration.json` validates strictly without reading any
artifact, and `tenon integration install/inspect/verify/list/enable/disable/
update/remove` operate an owner-only, content-addressed store guarded by a
flock. Install requires an explicit operator trust decision, verifies raw
and prepared executable identities offline, and writes blobs before state so
interruptions leave inert bytes; remove retains shared blobs; verify rehashes
offline. An unknown capability type or version is a typed rejection, required
ambient environment names are metadata never carrying values, and remote
https artifacts fail closed as unavailable in this offline slice. Store.Resolve
returns a credential-free launch descriptor for the connection-wiring
follow-up; nothing here changes generation yet.


Staged agent filesystems, per ADR 0012 (acceptance item 9): `tenon stage`
re-applies an agent as if its immutable source lived at
`/opt/tenon/agents/<name>` and its workspace at `/workspace`, so the staged
apply record and generated MCP configuration embed those final runtime paths
rather than the physical build directory. The tree is built in a temporary
sibling and published with a single rename only after a schema-versioned
artifact manifest — recording every staged file's final path, hash, mode, and
intended ownership — is complete, so a failure leaves no output. Staging
verifies authored source is byte-identical before and after, reads no
credential (a conspicuous environment value never reaches the tree), and its
fail-closed `agent-entrypoint` runs `tenon stage verify` before any harness
handoff. Two honest limitations are recorded in the manifest, not hidden: the
native harness runtime is not yet bundled (expected on the base image), and
the tool execution closure is staged whole rather than minimized.

Headless dispatch core, per acceptance item 7 (proven credential-free with a
behavioral harness stub): `tenon run` reads bounded JSONL input, durably
accepts and FIFO-queues one turn at a time per conversation on the
dispatchstate store while still accepting input during an active turn,
deduplicates input IDs against the live queue and retained outcomes, maps
each turn to a resumable native session (capturing and replaying the session
id; task mode always opens fresh), streams a typed JSONL event surface, and
marks unproven work uncertain on restart — never re-executed. A stale or
missing generated setup fails closed before serving, process failure is
distinct from a failed model turn, and a task turn deadline aborts and
records uncertainty. The harness driver seam (Driver/Session) is defined; the
real Claude and Codex protocol drivers are the next slice, so `tenon run`
against a real harness reports "not yet implemented" until then — the
orchestration itself is fully proven.

## Gaps

The complete [product specification](../product-spec.md) acceptance list, to
be built in journey order — apply and the five-minute journey first, then
authored tools, connections, headless dispatch, schedules, staging — each
slice credential-free tested:

1. One authored project compiles deterministically for both harnesses, and
   apply produces native, discoverable files while refusing conflicts and
   modified-file overwrites; a directory proven by neither instructions nor a
   supplied matching manifest is refused as not an agent project, and an
   instructions-free project generates an empty always-on surface rather than
   failing.
2. Both generated integrations expose the same managed MCP tool surface, and
   a mixed TypeScript/Python/Go project is prepared once per apply with one
   host process per language.
3. Agent source applies outside its own directory: generated files and
   execution use the workspace while discovery stays rooted in source.
4. Subagents generate natively with inheritance and exact effort mapping,
   and child skills, tools, dependencies, and nested subagents are rejected
   rather than ignored; skills and their resources round-trip byte-for-byte
   with executable intent, vendor metadata preserved and warned;
   harness-specific files round-trip only to their selected harness with
   full ownership protection.
5. Plugin skills and plugin MCP declarations import with deterministic
   collision handling and isolated component failure.
6. Connections generate exact native configuration for installed and remote
   targets without contacting anything; a name collision with `managed`,
   another connection, or a plugin server fails before mutation; and a
   conspicuous fake ambient value never appears in generated files, state,
   staging, or evidence.
7. Headless dispatch durably queues FIFO input, deduplicates input IDs,
   resumes sessions, and marks unproven restart work uncertain.
8. Schedules validate and fingerprint identically for both harnesses;
   triggers deduplicate stable occurrence IDs, open fresh sessions, honor
   turn deadlines with retained uncertainty, and the UTC clock admits only
   current non-overlapping occurrences and drains on shutdown.
9. Staging produces a deterministic, credential-free, minimal runnable tree
   whose entrypoint verifies identity and fingerprint before a turn;
   preparation never mutates authored source, and publication is one rename
   only after the manifest is complete.
10. Managed audit output remains content-free.
11. A supplied agent manifest is verified before apply and before every
    tenon-owned process open: a drifted harness version, package identity,
    or source fingerprint fails closed naming the exact pin; writing the
    manifest for an unchanged closure is byte-identical; an unsupplied
    manifest changes nothing; and no pin, fingerprint, or provenance value
    appears in model-facing generated content.
12. Validate reports the same failures as apply without mutating anything,
    and its structured diagnostics carry stable identifiers and authored
    paths that match apply's own failures.

The list is the proof skeleton, not the whole contract: every stated behavior
in the specification binds.
