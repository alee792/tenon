# Use cases

The concrete jobs tenon does today, and where each one stops.

Tenon is pre-release: build from source (Go 1.26+) as the
[README](../README.md#install) shows. The
[product specification](product-spec.md) is the contract wherever this
document is less precise, and its
[known limitations](product-spec.md#known-limitations) are the record of
what is still rough.

## Author an agent as reviewable files

**For** anyone who understands files, directories, instructions, skills,
and tools — and should never have to learn pin sets, registration, or
harness configuration.

An agent authored the usual way lives as vendor-specific configuration
scattered through a workspace: hard to read, hard to review, hard to diff,
and bound to one harness. There is no single artifact to put in front of a
reviewer.

Tenon makes the agent a directory whose layout is the API. The directory
name is the agent name; `instructions.md` carries a one-line `description`
in frontmatter and a Markdown body. Everything else is a file you add:

| Add | Get |
| --- | --- |
| a directory under `skills/` | a skill |
| `tools/*.ts`, `tools/*.py`, or `tools/NAME/tool.go` | one typed function |
| a directory under `subagents/` | a subagent |
| a Markdown file under `mcp/` | an MCP server the harness connects to |
| a Markdown file under `schedules/` | a cron task |

```sh
tenon apply . --harness claude   # or: --harness codex
```

Apply validates the whole project and compiles it into native harness
files; you then start the harness normally. There is no inventory to update
and nothing to register. The [README's quick start](../README.md#quick-start)
walks the whole path; [the authored project](product-spec.md#the-authored-project)
is the full convention, including the bounds every surface is held to.

The consequence is the point: the agent is one legible folder that goes
through code review like any other source, and its diff is plain language,
not generated configuration.

**The boundary.** Validation proves a project's contracts, never its
behavior. An authored tool is your own code, and accepting one into
`tools/` is a deliberate act — tenon does not sandbox it or claim to make
it safe. Apply refuses to overwrite hand-authored native files, and refuses
tenon-owned files edited since the last apply unless `--discard-local` is
passed.

## Carry one source of truth across harnesses

**For** anyone who does not want their agent locked to the harness it was
first written for, or who maintains the same agent for two audiences that
chose differently.

Each harness reads its own native format, so maintaining the same agent
twice means two sources of truth drifting apart silently.

One authored folder compiles to either harness through thin vendor
adapters. Claude Code receives `CLAUDE.md`, `.mcp.json`, `.claude/skills/`,
and `.claude/agents/`; Codex receives `AGENTS.md`, `.codex/config.toml`,
`.agents/skills/`, and `.codex/agents/`. Three commands cover the crossing:

```sh
tenon check AGENT --harness <claude|codex>
tenon apply AGENT --workspace WORKSPACE --harness <claude|codex>
tenon drift AGENT --workspace WORKSPACE --harness <claude|codex>
tenon clean --workspace WORKSPACE [--harness <claude|codex>]
```

- `check` is the gate: it runs apply's own validation and writes nothing.
- `apply` materializes the owned native files and records a source
  fingerprint over every authored input, so stale or edited generated setup
  fails closed.
- `drift` regenerates every tenon-owned file in memory and reports each one
  unchanged, modified on disk with a unified diff, missing, or stale.
- `clean` is apply's inverse: it removes the files the apply record owns,
  then the record, so switching harnesses or uninstalling leaves nothing
  behind.

Agent source and workspace are independent directories, so one source tree
applies into several workspaces. Genuinely nonportable native files have an
escape hatch under `harnesses/claude/` and `harnesses/codex/`, copied
byte-for-byte to only the selected harness. See
[apply and handoff](product-spec.md#apply-and-handoff).

**The boundary.** What is portable is the agent's declared capability
surface — instructions, skills, tools, MCP servers. Context assembly,
pruning, approvals, and model-loop behavior stay the harness's. Drift never
adopts a workspace edit back into source: generation is lossy in reverse,
so tenon shows the diff and you edit source and reapply. Recognized
vendor-specific skill fields are preserved unchanged with a warning when
the selected harness does not document honoring them — tenon never
translates, strips, or enforces them.

## Compose third-party components without a marketplace

**For** an author who wants a published Agent Plugin's skills, or a native
MCP server such as GitHub's, inside their agent — with the provenance of
both visible in version control.

Acquiring components usually means a registry, a resolver, a lockfile, and
a network path that runs at build time. Each is a place where what ships
stops matching what was reviewed.

Tenon has none of them. A plugin is a complete publisher-authored Agent
Plugin v1 package. A consumer either vendors the reviewed directory intact
beneath `plugins/<storage-name>/`, or writes a plugin reference file,
`plugins/<name>.md`, naming a `source` and a full commit `rev`; review,
pinning, and provenance belong to the author's own version control either
way, and there is no dependency lock and no resolver. `tenon plugin fetch`
is the one explicitly online command, resolving a reference into an
owner-only, content-addressed cache; `tenon apply` and every other load
stay offline and fail, naming the fetch command, when a pin is not cached.
Plugin `plugin.json` and `mcp.json` are validated locally, without
fetching, and accepted skills and MCP servers map into native harness
configuration with deterministic collision handling. An authored MCP
server is one `mcp/<name>.md` whose filename is the native server name — a
hosted `streamable-http` endpoint, a `stdio` command in the agent tree, or
an installed integration-package capability — validated without contacting
anything, with the harness discovering and performing any authentication.
Machine-installed integrations go through exactly one trust journey:

```sh
tenon integration install SOURCE --trust operator
```

That places the package in an owner-only content-addressed store,
re-verified before every use. Portable agent source can never choose an
install source, grant trust, or carry a credential, so apply gains no
network path. See
[authored MCP servers](product-spec.md#the-authored-project) and
[the native GitHub MCP journey](github-native-mcp.md).

**The boundary.** Configuring or acquiring a third-party component does not
make it managed: the harness owns process lifecycle, credentials,
approvals, calls, and effects for everything a plugin or authored server
launches. Authentication is deliberately unmanaged — an OAuth grant the
harness obtains lives in harness-owned storage tenon neither writes nor
reads, and a `GITHUB_PERSONAL_ACCESS_TOKEN` injected for the deferred
installed journey is readable by the harness, the model-accessible
execution tools, and any process inheriting that environment. Nor is a
remote server pinned: its tool catalog can change under an unchanged
fingerprint. Tenon is not a marketplace or an updater. Today
`tenon mcp add` writes remote `--url` servers only; the stdio, installed,
and masking forms are authored as the Markdown file directly.

## Give an improvement loop a substrate

**For** an improvement loop — an agent or optimizer revising an agent's own
files, treated as an author coequal with the person.

A loop that edits an agent's configuration needs three things it cannot
give itself: proof that a revision is well-formed before anything runs,
failures it can act on mechanically rather than by parsing prose, and
attribution tying each run back to the exact configuration that produced
it.

The editable surface is files, bounded and legible: `instructions.md`,
`skills/`, `tools/`, `plugins/`, `subagents/`. A loop mutates them and then
gates itself:

```sh
tenon check . --harness claude --format jsonl
tenon apply . --harness claude
```

In the machine-readable mode each failure is one JSON line carrying a
stable identifier, the authored path, and the exact rule violated. The
identifiers hold across releases and match apply's own failures, so a loop
self-corrects against an identifier rather than against prose. The
stream always ends with one further object — no `id`, `path`, or `rule`
field — carrying an `outcome` (`ok` or `gate_failed`) and, on success, the
agent name and source fingerprint, so a consumer must expect it as a final,
distinct line and never has to infer failure from a missing summary. Apply
records that fingerprint, and every dispatch lifecycle event carries it
too. `check --emit catalog` additionally reports the resolved capability
inventory the gate has already computed — skills, tools, MCP servers,
subagents, schedules — but only for a source that passes, and tenon never
accepts such an inventory as input. An instructions-free project is a
legitimate candidate for a loop to try: a supplied pin set whose expected
fingerprint matches the directory also proves the agent root, and the
generated always-on surface is simply empty.

[Tenon as the outer loop's substrate](outer-loop.md) works this through in
depth, including how the fingerprint and the diagnostic identifier become
the units a lineage is built from.

**The boundary.** Tenon proves a revision is well-formed; it never proves
it is an improvement. It collects no transcripts, evaluations, or scores.
Evaluation, selection among revisions, and lineage tracking are out of
scope — lineage belongs to version control, a candidate being a source
revision crossed with a supplied pin set. How variants are isolated —
worktrees, containers, sandboxes — is your infrastructure choice; tenon
requires only that each variant is a directory that applies
deterministically. Automatic or unreviewed promotion of an agent-authored
improvement is an explicit non-goal.

## Run the same folder headless and on a schedule

**For** an operator running an agent without a person at a terminal — from
a queue, a hook, or a clock.

The interactive setup and the unattended setup are usually two different
configurations, and the second is the one nobody reviews.

The same folder, unedited, runs headless. `tenon run` is a turn dispatcher
over bounded JSONL:

```sh
printf '%s\n' '{"input_id":"x-1","text":"..."}' \
  | tenon run AGENT --workspace WS --harness <claude|codex> --input jsonl
```

Input is durably accepted and queued while a turn is active, processed one
FIFO turn per conversation, mapped to a resumable native session, and
emitted as ordered JSONL events; a repeated input ID deduplicates within
its conversation.

Schedules are Markdown files under `schedules/` whose frontmatter holds one
five-field cron string and whose body is the task prompt.
`tenon schedule trigger` dispatches a single occurrence under a caller-owned
stable ID, opening a fresh native task session and returning the retained
outcome for a duplicate. `tenon schedule run` is an explicit foreground UTC
clock: first occurrence strictly after startup, no overlap for one
schedule, a local lock excluding a second clock for the same workspace,
agent, and harness, and graceful drain on signals. Both paths require
current generated setup. See
[headless operation](product-spec.md#headless-operation).

**The boundary.** The dispatcher is not another chat UI or model loop.
There is no daemon, no downtime or clock-jump backfill, no missed-run
replay, and no hosted delivery runtime. After a restart, active work
without a proven terminal result is recorded uncertain and never silently
retried, and lifecycle output is bounded and never contains model text. A
supplied pin set is verified at `tenon run`'s session start rather than
per turn within that session; the recurring `schedule run` path re-verifies
each occurrence.

## Stage an agent for containerized deployment

**For** an operator who already has an OCI build system and wants the agent
in it without hand-assembling a runtime.

Getting an agent into a container usually means recreating its setup inside
a Dockerfile, and quietly baking in build toolchains, caches, or
credentials along the way.

```sh
tenon stage AGENT --harness <harness> --output DIR
```

That prepares one complete runnable filesystem tree at canonical paths for
an existing builder to copy: tenon itself, the selected harness, immutable
agent source, the generated integration and apply record, an entrypoint, an
artifact manifest, and only the execution closure the agent's tools
actually need — no build toolchains, caches, credentials, login state,
trust decisions, or conversation state. Staging is deterministic for
identical pinned inputs, verifies that preparation did not mutate authored
source, and publishes with one rename only after the manifest is complete.
The staged entrypoint verifies runtime identity, generated integration, and
source fingerprint before it runs a turn. The contract is
[staged agent filesystems](product-spec.md#staged-agent-filesystems).

Prebuilt images are not published yet; build one locally from
[`images/<harness>/Dockerfile`](../images/), per
[harness images](harness-images.md).

**The boundary.** Tenon does not construct OCI layers, contact registries,
publish, sign, deploy, or operate images — your builder owns all of that.
Two gaps are recorded in every staging artifact manifest: the native
harness runtime is expected on the base image rather than bundled into the
staged tree, and the authored-tool execution closure is staged whole rather
than minimized.

## Fix a baseline for evaluation and harness comparison

**For** anyone measuring an agent — an eval harness, a loop scoring
successive revisions, or someone asking which harness behaves better on
identical starting state.

A measurement is only as good as the configuration it can be attributed to.
A folder alone cannot express harness version, model, tenon version, or
installed-package identities, so two runs that look identical may not be.

Two artifacts pin the closure. The **source fingerprint** travels with
every apply record and every dispatch lifecycle event. The optional
[pin set](product-spec.md#pins) pins what the directory
cannot express — schema version, agent name, expected source fingerprint,
tenon version, and per harness the executable version, a model identifier,
integration package identities, and authored-tool runtime versions. It is
supplied at application rather than stored in source, so one commit crosses
with many pin sets:

```sh
tenon check AGENT --harness claude --write-pins PATH
tenon apply AGENT --workspace WORKSPACE-CLAUDE --harness claude --pins PATH
tenon apply AGENT --workspace WORKSPACE-CODEX  --harness codex  --pins PATH
tenon check AGENT --emit files --format jsonl
```

The pin set is written by the gate itself, so it can only ever be minted by
a source that passes right now, bound to the fingerprint just proven. A
supplied pin set is verified before apply and before every tenon-owned
process open, failing closed and naming the exact drifted pin; writing it
for an unchanged closure is byte-identical, and supplying none changes
nothing. Applying the same source under the same pin set to both harnesses
gives two runs whose starting agent state is identical by construction, so
the difference observed is harness behavior. A pin is an axis of variation
in its own right: a loop may hold the files fixed and move the model or
harness version instead.

**The boundary.** Scoring stays outside. Tenon retains no transcripts,
evaluations, or scores — only the fingerprint and, when supplied, the pin
set's identity travel with a run, so observation made elsewhere joins
back to the exact configuration that produced it. The model pin is emitted
through the harness's documented configuration and recorded in provenance;
the harness owns model selection, and tenon does not claim to verify which
model actually served a turn. None of this reaches the running agent: no
pin, fingerprint, or provenance value is rendered into generated
instructions or any other model-facing content.

## What tenon is not

Stated plainly, because several of these are the natural next guess about a
tool that compiles agent configuration. The authoritative list is
[explicit non-goals](product-spec.md#explicit-non-goals).

- **Not a model runtime.** No model loop, no context manager, no
  cross-harness chat UI. The harness owns intelligence.
- **Not a marketplace or an updater.** No registry, no dependency resolver,
  no network acquisition of components, no lockfile for vendored plugins.
- **Not a sandbox.** Tenon never claims to enforce instructions, inspect
  native harness effects, sandbox authored tool code, or make model
  behavior safe from outside the harness. Trust stays with the author.
- **Not a deployment system.** No OCI layer construction, image publication
  or signing, hosted image operation, background or distributed schedule
  clocks, or deployment orchestration.
- **Not an evaluator.** Evaluations, scoring, transcript retention,
  selection among revisions, and lineage tracking are out of scope. Tenon
  is an improvement loop's substrate, never the loop.
- **Not governance over native harness tools.** Acquiring or configuring a
  third-party component does not make it managed.
