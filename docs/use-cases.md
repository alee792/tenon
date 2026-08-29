# Use cases

The other documents state the contract — what tenon binds, what it refuses,
and where its boundary sits. This one states what the contract is *for*: the
concrete jobs people bring to tenon, and what the tool actually does for each
of them.

Everything below is grounded in behavior this repository implements today.
Where something is unbuilt, partial, or unvalidated, it says so in place
rather than leaving a reader to discover it later. Nothing here binds: the
[north star](north-star.md), the [vision](vision.md), and the
[product specification](product-spec.md) win wherever this document
disagrees with them, and the [glossary](glossary.md) owns the vocabulary.

Each use case is written the same way — who it serves, the problem, what
tenon does concretely, and where the boundary is — and names which leg of
the north star's measure it serves: the first five minutes, the same folder
later, or the next revision.

## Author an agent as reviewable files

**Who.** A person who understands files, directories, and the common AI
concepts — instructions, skills, tools — and who should never have to learn
manifests, registration, or harness configuration.

**The problem.** That person's agent today lives as vendor-specific
configuration scattered through a workspace: hard to read, hard to review,
hard to diff, and bound to one harness. There is no single artifact to put
in front of a reviewer.

**What tenon does.** The agent is a directory and its layout is the API. The
directory name supplies the agent name; `instructions.md` carries YAML
frontmatter with one plain `description` and a Markdown body; a directory
under `skills/` is a skill; a `tools/*.ts`, `tools/*.py`, or
`tools/NAME/tool.go` file is one typed function; a directory under
`subagents/` is a subagent; a Markdown file under `mcp/` is an MCP server the
harness connects to; one under `schedules/` is a cron task. Adding capability
means adding a file — there is no manifest to update and nothing to
register. `tenon apply . --harness claude` (or `--harness codex`) validates
the whole project and compiles it into native harness files, and the author
then starts the harness normally. The README's
[quick start](../README.md#quick-start) is the whole
journey; the specification's
[authored project](product-spec.md#the-authored-project) is the full
convention, including the bounds every surface is held to.

The consequence is the point: the agent is one legible folder that goes
through code review like any other source, and its diff is plain language,
not generated configuration.

**The boundary.** Validation proves a project's contracts, never its
behavior. An authored tool is the author's own code, and accepting one into
`tools/` is a deliberate, reviewable act — tenon does not sandbox it or
claim to make it safe. Apply refuses to overwrite hand-authored native
files, and refuses tenon-owned files modified since the last apply unless
`--discard-local` is passed.

**Measure leg.** The first five minutes.

## Carry one source of truth across harnesses

**Who.** Anyone who does not want their agent locked to the harness it was
first written for — and anyone maintaining the same agent for two audiences
that chose differently.

**The problem.** Each harness reads its own native format. Maintaining the
same agent twice means two sources of truth, drifting apart silently.

**What tenon does.** One authored folder compiles to either harness through
thin vendor adapters. Claude Code receives `CLAUDE.md`, `.mcp.json`,
`.claude/skills/`, and `.claude/agents/`; Codex receives `AGENTS.md`,
`.codex/config.toml`, `.agents/skills/`, and `.codex/agents/`. Three
commands cover the crossing:

```sh
tenon validate AGENT --harness <claude|codex>
tenon apply AGENT --workspace WORKSPACE --harness <claude|codex>
tenon drift AGENT --workspace WORKSPACE --harness <claude|codex>
```

`validate` runs apply's own validation and writes nothing. `apply`
materializes the owned native files and records a source fingerprint over
every authored input, so stale or edited generated setup fails closed.
`drift` regenerates every tenon-owned file in memory and reports each one
unchanged, modified on disk with a unified diff, missing, or stale. The
agent source and the workspace are independent directories, so one source
tree can be applied into several workspaces. Genuinely nonportable native
files have an explicit escape hatch under `harnesses/claude/` and
`harnesses/codex/`, copied byte-for-byte to only the selected harness. See
[apply and handoff](product-spec.md#apply-and-handoff).

Nobody else owns the crossing: a harness vendor optimizes its own format,
and the compiler between formats is the thing tenon exists to be.

**The boundary.** What is portable is the agent's declared capability
surface — instructions, skills, tools, MCP servers. Context assembly,
pruning, approvals, and model-loop behavior stay the harness's, and always
will. Drift never adopts a workspace edit back into source: generation is
lossy in reverse, so tenon shows the diff and the author edits source and
reapplies. Recognized vendor-specific skill fields are preserved unchanged
with a warning when the selected harness does not document honoring them —
tenon never translates, strips, or enforces them.

**Measure leg.** The same folder later; it also underwrites the first five
minutes, since neither harness choice is a fork.

## Compose third-party components without a marketplace

**Who.** An author who wants a published Agent Plugin's skills, or a native
MCP server such as GitHub's, inside their agent — and who wants the
provenance of both to stay visible in version control.

**The problem.** Acquiring components usually means a registry, a resolver,
a lockfile, and a network path that runs at build time. Each of those is a
place where what ships stops matching what was reviewed.

**What tenon does.** A plugin is a complete publisher-authored Agent Plugin
v1 package. A consumer either vendors the reviewed directory intact beneath
`plugins/<storage-name>/`, or writes a plugin reference file,
`plugins/<name>.md`, naming a `source` and a full commit `rev`; review,
pinning, and provenance belong to the author's own version control either
way, and there is no dependency lock and no resolver. `tenon plugin fetch` is
the one explicitly online command, resolving a reference into an owner-only,
content-addressed cache; `tenon apply` and every other load stay offline and
fail, naming the fetch command, when a pin is not cached. Plugin
`plugin.json` and `mcp.json` are validated locally, without fetching, and
accepted skills and MCP servers map into native harness configuration with
deterministic collision handling. An authored MCP server is one
`mcp/<name>.md` whose filename is the native server name — a hosted
`streamable-http` endpoint, a `stdio` command in the agent tree, or an
installed integration-package capability — validated without contacting
anything, with the harness discovering and performing any authentication.
Machine-installed integrations go through exactly one trust journey,
`tenon integration install SOURCE --trust operator`, into an owner-only
content-addressed store that is re-verified before every use. Portable agent
source can never choose an install source, grant trust, or carry a
credential — apply gains no network path. See
[authored MCP servers](product-spec.md#the-authored-project) and
[the native GitHub MCP journey](github-native-mcp.md).

**The boundary.** Configuring or acquiring a third-party component does not
make it managed: the harness owns process lifecycle, credentials, approvals,
calls, and effects for everything a plugin or authored server launches.
Authentication is deliberately unmanaged — an OAuth grant the harness obtains
lives in harness-owned storage tenon neither writes nor reads, and a
`GITHUB_PERSONAL_ACCESS_TOKEN` injected for the deferred installed journey is
readable by the harness, the model-accessible execution tools, and any
process inheriting that environment. Nor is a remote server pinned: its tool
catalog can change under an unchanged fingerprint. Tenon is not a marketplace
or an updater. Today `tenon mcp add` writes remote `--url` servers only; the
stdio, installed, and masking forms are authored as the Markdown file
directly.

**Measure leg.** The first five minutes, extended up the ladder without a
second persona.

## Give an improvement loop a substrate

**Who.** An improvement loop — an agent or an optimizer revising an agent's
own files. The vision treats it as an author coequal with the person, and
[ADR 0018](adr/0018-add-the-revision-leg-to-the-measure.md) put its leg into
the measure.

**The problem.** A loop that edits an agent's configuration needs three
things the loop cannot give itself: proof that a revision is well-formed
before anything runs, failures it can act on mechanically rather than by
parsing prose, and attribution tying each run back to the exact
configuration that produced it. Harness-updating capability is roughly flat
across model sizes, so well-formedness — not drafting skill — is the binding
constraint.

**What tenon does.** The editable surface is files, bounded and legible:
`instructions.md`, `skills/`, `tools/`, `plugins/`, `subagents/`. A loop
mutates them and then gates itself:

```sh
tenon validate . --harness claude --diagnostics jsonl
tenon apply . --harness claude
```

In the machine-readable mode each failure is one JSON line carrying a stable
identifier, the authored path, and the exact rule violated; the identifiers
hold across releases and match apply's own failures, so a loop self-corrects
against an identifier rather than against prose. On success the stream ends
with one further object — no `id`, `path`, or `rule` field — carrying the
agent name and source fingerprint, so a consumer must expect it as a final,
distinct line. Apply records that fingerprint with the apply, and every
dispatch lifecycle event carries it too. An instructions-free project is a
legitimate candidate for a loop to try: a supplied manifest whose expected
fingerprint matches the directory also proves the agent root, and the
generated always-on surface is simply empty.

**The boundary.** Tenon proves a revision is well-formed; it never proves it
is an improvement. It collects no transcripts, no evaluations, and no
scores. Evaluation, selection among revisions, and lineage tracking are out
of scope absent a new ADR — lineage belongs to version control, a candidate
being a source revision crossed with a supplied manifest. Whether tenon
should nonetheless own the *unit* that lineage is built from, and report a
revision's change to the capability surface rather than to its bytes, is
open: see [ADR 0024](adr/0024-add-observation-to-the-revision-leg.md) and
[ADR 0025](adr/0025-make-the-fingerprint-the-unit-of-revision-identity.md),
both proposed. How variants are
isolated, whether worktrees, containers, or sandboxes, is the operator's
infrastructure choice; tenon requires only that each variant is a directory
that applies deterministically. The friction inbox is a supplementary
human-facing channel, not the loop's signal path, and automatic or
unreviewed promotion of agent-authored improvements is an explicit non-goal.

**Measure leg.** The next revision.

## Run the same folder headless and on a schedule

**Who.** An operator running an agent without a person at a terminal — from
a queue, a hook, or a clock.

**The problem.** The interactive setup and the unattended setup are usually
two different configurations, and the second is the one nobody reviews.

**What tenon does.** The same folder, unedited, runs headless. `tenon run`
is a turn dispatcher over bounded JSONL:

```sh
printf '%s\n' '{"input_id":"x-1","text":"..."}' \
  | tenon run AGENT --workspace WS --harness <claude|codex> --input jsonl
```

Input is durably accepted and queued while a turn is active, processed one
FIFO turn per conversation, mapped to a resumable native session, and
emitted as ordered JSONL events; a repeated input ID deduplicates within its
conversation. Schedules are Markdown files under `schedules/` whose
frontmatter holds one five-field cron string and whose body is the task
prompt. `tenon schedule trigger` dispatches a single occurrence under a
caller-owned stable ID, opening a fresh native task session and returning
the retained outcome for a duplicate. `tenon schedule run` is an explicit
foreground UTC clock: first occurrence strictly after startup, no overlap
for one schedule, a local lock excluding a second clock for the same
workspace, agent, and harness, and graceful drain on signals. Both paths
require current generated setup. See
[headless operation](product-spec.md#headless-operation).

**The boundary.** The dispatcher is not another chat UI or model loop.
There is no daemon, no downtime or clock-jump backfill, no missed-run
replay, and no hosted delivery runtime. After a restart, active work without
a proven terminal result is recorded uncertain and never silently retried,
and lifecycle output is bounded and never contains model text. A supplied
manifest is verified at `tenon run`'s session start rather than per turn
within that session; the recurring `schedule run` path does re-verify each
occurrence. The Claude and Codex drivers are proven by pure-function unit
tests plus manual `//go:build harness` integration tests that CI does not
run, and the Codex driver's successful-turn path has not been validated
live — only its credential-safe 401 classification has
([known limitations](product-spec.md#known-limitations)).

**Measure leg.** The same folder later.

## Stage an agent for containerized deployment

**Who.** An operator who already has an OCI build system and wants the agent
in it without hand-assembling a runtime.

**The problem.** Getting an agent into a container usually means recreating
its setup inside a Dockerfile — and quietly baking in build toolchains,
caches, or credentials along the way.

**What tenon does.** `tenon stage AGENT --harness <claude|codex> --output
DIR` prepares one complete runnable filesystem tree at canonical paths for
an existing builder to copy: tenon itself, the selected harness, immutable
agent source, the generated integration and apply record, an entrypoint, an
artifact manifest, and only the execution closure the agent's tools actually
need — no build toolchains, caches, credentials, login state, trust
decisions, or conversation state. Staging is deterministic for identical
pinned inputs, verifies that preparation did not mutate authored source, and
publishes with one rename only after the manifest is complete. The staged
entrypoint verifies runtime identity, generated integration, and source
fingerprint before it runs a turn. The README's
[staging section](../README.md#staging-for-deployment) shows the Dockerfile
shape; the contract is
[staged agent filesystems](product-spec.md#staged-agent-filesystems).

**The boundary.** Tenon does not construct OCI layers, contact registries,
publish, sign, deploy, or operate images — an existing builder owns all of
that. Real limitations, recorded rather than hidden: the native harness
runtime is not yet bundled into the staged tree and is expected on the base
image, and the authored-tool execution closure is staged whole rather than
minimized, both noted in the staging artifact manifest
([ADR 0021](adr/0021-execute-authored-tools-from-a-self-contained-closure.md)).
The `ghcr.io/alee792/tenon/<harness>` images are not yet published; build
locally from [`images/<harness>/Dockerfile`](../images/), per
[harness images](harness-images.md). The container gate is manual — CI does
not build or run a staged image, so
[`scripts/check-staged-images.sh`](../scripts/check-staged-images.sh) and
[staged acceptance](staged-acceptance.md) stand in its place before a
release.

**Measure leg.** The same folder later.

## Fix a baseline for evaluation and harness comparison

**Who.** Anyone measuring an agent — an eval harness, a loop scoring
successive revisions, or someone asking which harness behaves better on
identical starting state.

**The problem.** A measurement is only as good as the configuration it can
be attributed to. A folder alone cannot express harness version, model,
tenon version, or installed-package identities, so two runs that look
identical may not be.

**What tenon does.** Two artifacts pin the closure. The source fingerprint
travels with every apply record and every dispatch lifecycle event. The
optional [agent manifest](product-spec.md#agent-manifest) pins what the
directory cannot express — schema version, agent name, expected source
fingerprint, tenon version, and per harness the executable version, a model
identifier, integration package identities, and authored-tool runtime
versions. It is supplied at application rather than stored in source, so one
commit crosses with many pin sets:

```sh
tenon manifest write AGENT --harness <claude|codex> --output PATH
tenon apply AGENT --harness claude --manifest PATH
tenon apply AGENT --harness codex  --manifest PATH
tenon fingerprint show AGENT --diagnostics jsonl
```

A supplied manifest is verified before apply and before every tenon-owned
process open, failing closed and naming the exact drifted pin; writing the
manifest for an unchanged closure is byte-identical; supplying none changes
nothing. Applying the same source under the same manifest to both harnesses
gives two runs whose starting agent state is identical by construction, so
the difference observed is harness behavior. A pin is an axis of variation
in its own right: a loop may hold the files fixed and move the model or
harness version instead.

**The boundary.** Scoring stays outside. Tenon retains no transcripts,
evaluations, or scores — only the fingerprint and, when supplied, the
manifest identity travel with a run, so observation made elsewhere joins
back to the exact configuration that produced it. The model pin is emitted
through the harness's documented configuration and recorded in provenance;
the harness owns model selection, and tenon does not claim to verify which
model actually served a turn. None of this reaches the running agent: no
pin, fingerprint, or provenance value is rendered into generated
instructions or any other model-facing content.

**Measure legs.** The same folder later, and the next revision.

## What tenon is not

Stated plainly, because several of these are the natural next guess about a
tool that compiles agent configuration. The authoritative lists are the
specification's [explicit non-goals](product-spec.md#explicit-non-goals) and
the closing paragraph of [`AGENTS.md`](../AGENTS.md).

- **Not a model runtime.** No model loop, no context manager, no
  cross-harness chat UI. The harness owns intelligence.
- **Not a marketplace or an updater.** No registry, no dependency resolver,
  no network acquisition of components, no lockfile for vendored plugins.
- **Not a sandbox.** Tenon never claims to enforce instructions, inspect
  native harness effects, sandbox authored tool code, or make model behavior
  safe from outside the harness. Trust stays with the author.
- **Not a deployment system.** No OCI layer construction, image publication
  or signing, hosted image operation, background or distributed schedule
  clocks, or deployment orchestration.
- **Not an evaluator.** Evaluations, scoring, transcript retention,
  selection among revisions, and lineage tracking are out of scope unless
  re-decided by ADR. Tenon is an improvement loop's substrate, never the
  loop, and automatic promotion of agent-authored improvements is a
  non-goal.
- **Not governance over native harness tools.** Acquiring or configuring a
  third-party component does not make it managed.

## Status of the claims above

The specification's [acceptance list](product-spec.md#acceptance) is
implemented and proven by credential-free tests — fake harness processes, no
live model calls. The v0.1.0 release is not yet published, so today's
journey starts from a locally built binary. Read
[known limitations](product-spec.md#known-limitations) alongside any use
case here before depending on it; the boundary paragraphs above summarize
the limitations that bear on each, but that list is the record.
