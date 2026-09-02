# Product specification

- Status: the product contract. The `hctl` prototype (alee792/hctl) is the
  frozen, read-only reference implementation.
- Scope: the core product. The conversational channel runtime is a second
  product, specified in the prototype repository.
- Product: Tenon; the binary is `tenon`.
- Initial harnesses: Claude Code and Codex CLI.

## User and job

Two consumers author the same artifact, and both receive balanced
consideration: a person, and an improvement loop revising an agent's
files. A decision may favor one when the tradeoff is evaluated and
recorded — never by default. The human author understands basic files and
directories and common AI concepts such as instructions, skills, and
tools, and should not need to understand registration, manifests, or
harness configuration. The improvement loop needs validation before
anything runs, a reproducible pinned closure, and attribution of every run
to its exact configuration; the pin set and the machine-consumable gate
serve it directly. There is one capability ladder, not an
author/developer split: the author starts with instructions and composed
skills, and may climb to typed tool functions — written directly or
drafted by their harness. Validation proves a tool's contract, not its
behavior; adopting one remains the author's deliberate, reviewable act,
like any other code.

Operating is a distinct role on the same artifact: credentials, integration
packages, schedules, and staged filesystems carry their own explicit
guardrails. The author defines one filesystem-authored agent project, applies
it to a chosen workspace, and proves it interactively in Claude Code or
Codex; the operator runs the same setup headlessly, which is where
portability is proven.

## Product principles

1. The agent project is legible, versionable, portable source and is not
   coupled to the repository that stores it.
2. Common behavior is portable; harness-specific differences are explicit.
3. Compilation and validation happen before harness files are written or a
   turn dispatcher starts.
4. Generated native files are disposable and visibly tool-owned.
5. Native harness tools remain available and explicitly unmanaged.
6. Policy applies only at managed-tool and durable-state boundaries.
7. Interactive users remain in the native harness interface.
8. Unsupported harness behavior is reported without rewriting valid authored
   source or pretending that tenon enforces it.
9. Conventional files register behavior without a second inventory.
10. Author-facing language stays concrete; runtime terminology remains
    internal.

## What this specification binds

This specification binds authored formats and functional responsibilities,
not interface shape. Authored project formats are exact — the folder is
the portable convention, and its filenames and schemas are the contract.
The tool's own surface is specified by responsibility: validate before
mutation, compile to native integration, pin and verify the runtime
closure, attribute every run to its exact configuration, dispatch turns,
and stage filesystems. The command names, flags, file encodings, and
output framing shown below are the reference rendering of those
responsibilities: a conforming implementation may expose them as a CLI, a
library, or a protocol server, provided every responsibility and
acceptance item holds and every machine-facing output stays stable and
parseable. The reference rendering is deliberately file-and-process
shaped because files, processes, and exit codes are the one interface a
person, a harness, and an improvement loop already share.

## The authored project

Authoring is filesystem-forward and convention-driven. A project is a
directory; the directory name supplies the agent name, normalized to
lowercase hyphenated words. The full component set:

```text
my-agent/
  instructions.md          # optional; see the root rule below
  skills/                  # Agent Skills directories
  plugins/                 # vendored Agent Plugin packages, or <name>.md pointer+pin references
  tools/                   # one typed function per TS/Python file or Go dir
  subagents/               # one instructions.md per immediate subagent
  mcp/                     # one <name>.md per authored MCP server, or a mask
  schedules/               # nested Markdown cron tasks
  harnesses/               # literal harness-specific native files
  channels/                # second product; specified separately
```

**Instructions.** An agent root is proven one of two ways: by a present
`instructions.md`, or by a supplied pin set whose expected source
fingerprint matches the directory. A directory with neither proof is not
an agent project. When present, `instructions.md` starts
with YAML frontmatter carrying one plain `description` (and an optional
Boolean `friction-notes` opting into the friction inbox below), followed
by a non-empty Markdown body; generated always-on instructions contain the
body, not the frontmatter. When absent, the agent generates no always-on
instructions — an empty system prompt is a legitimate candidate for an
improvement loop to try — and the directory name still supplies the agent
name. This is a recorded tradeoff under the balance rule: the file remains
the human journey's front door, while its requiredness no longer
forecloses part of the loop's search space.

**Skills.** Each immediate directory under `skills/` is one skill following
the open [Agent Skills specification](https://agentskills.io/specification):
a `SKILL.md` whose frontmatter `name` matches the directory, plus arbitrary
regular-file resources. Adding or removing a directory updates the compiled
project without registration. Resources copy byte-for-byte with executable
intent preserved. Portable fields validate to the standard's rules;
recognized vendor fields are preserved unchanged with a warning when the
selected harness does not document honoring them — tenon never translates,
strips, or enforces them. The dated per-field behavior matrix is
[skill compatibility](workbench/skill-compatibility.md).

**Plugins.** An [Agent Plugin v1](https://agent-plugins.org/specification) is
one complete publisher-authored package. A consumer vendors the reviewed
directory intact beneath `plugins/<storage-name>/`; review, pinning, and
provenance belong to the author's version control. Tenon records no dependency
lock, resolves no transitive graph, and acquires nothing during any project
load — the one online step is the explicit `tenon plugin fetch` described
below. Each plugin requires a bounded
`plugin.json` targeting the canonical v1.0.0 schema, validated locally
without fetching. Skills import only from the plugin's fixed `skills/`
location; root skills load first and the first skill name wins, with later
collisions skipped and warned, never renamed. Invalid components are skipped
independently with authored-path diagnostics.

An accepted plugin may carry a bounded `mcp.json` (canonical v1.0.0 MCP
schema; `stdio` and `streamable-http` supported, SSE warned and skipped).
Accepted servers are emitted as native project MCP configuration — the
harness owns startup, approval, transport, authentication, and runtime
behavior; tenon does not proxy, supervise, or audit plugin MCP calls. `managed`
is reserved; exact name collisions between two plugins are skipped with a
warning (first-wins, never renamed). A collision with an authored `mcp/`
server is different — the authored server wins instead, and a mask suppresses
a plugin's server outright (see Authored MCP servers). Plugin-relative
commands stay inside the real plugin tree; tenon expands exactly
`${PLUGIN_ROOT}` and `${PLUGIN_DATA}` once and provides an owner-only
persistent data directory per agent and plugin. Remote URLs are absolute
HTTPS (loopback excepted), without user info or fragments; headers are
literal package-visible values and must not contain secrets.

A plugin may also be declared by pointer and pin: `plugins/<name>.md`
carries closed frontmatter naming a `source` (an absolute HTTPS URL) and a
full 40-character commit `rev`, plus an optional bounded body of
informational provenance prose that is never rendered into instructions
(ADR 0026). `tenon plugin fetch` is the one explicitly online command that
resolves a reference into the owner-only, content-addressed plugin cache;
`tenon apply` and every other load stay fully offline and fail, naming
`tenon plugin fetch`, when a pin's cached tree is absent or no longer
matches its recorded digest. A resolved reference loads through the exact
same manifest, `skills/`, and `mcp.json` validation a vendored directory
uses, and its resolved bytes join the project fingerprint exactly as
vendored bytes do. A directory sharing a reference's name is not a
collision but that reference's pinned content materialized in place — the
shape `tenon stage` publishes — and it takes precedence over the cache, so
the same reference loads identically with no cache and no network; because
there is no `git` there to re-check the pin, the materialized bytes are
trusted exactly as all other authored source is, with the project
fingerprint (and so `tenon stage verify` and drift detection) as their
integrity check. `tenon plugin update AGENT NAME
--rev REV` fetches the new revision, prints a bounded added/removed/changed
component-path diff against the currently pinned revision, and only then
rewrites the reference file's `rev`; `tenon plugin status` reports each
reference's declared pin and offline resolution health.

**Tools.** Visible `tools/*.ts` and `tools/*.py` files and `tools/NAME/tool.go`
directories each declare one tool; filenames supply tool names, with
underscores exposed as hyphens. TypeScript exports a default object with
`description`, strict Zod `inputSchema` and `outputSchema`, and `execute`;
Python exports `description`, Pydantic `Input`/`Output`, and `execute`; Go
exports `Description`, `Input`, `Output`, and `Execute`. Dependencies use the
native lockfiles (`deno.json`/`deno.lock`, `pyproject.toml`/`uv.lock`,
`go.mod`/`go.sum`); there is no authored manifest, registry, or duplicated
tool inventory.

**Subagents.** Each immediate directory under `subagents/` contains only an
`instructions.md` with the same description-and-body contract plus optional
`effort: low|medium|high`, emitted to each harness's native effort field and
omitted when absent. One level, native inheritance of the parent's generated
instructions, skills, managed tools, and permissions. Child skills, tools,
dependencies, and nested subagents are rejected, not ignored. Subagent and
tool names may not collide.

**Authored MCP servers.** Each `mcp/<name>.md` authors one native MCP server
(the CLI verb, the diagnostic identifiers, and the older term for it are all
`mcp`/`connection`; they name the same thing). The filename supplies the
native server name under a fixed grammar, and `managed` is reserved for
tenon's own managed server. Frontmatter is closed YAML whose `type` field
selects exactly one of four arms (ADR 0026); three declare a server in the
[Agent Plugins 1.0](https://agent-plugins.org/specification) `mcp.json`
server-entry vocabulary used verbatim, and the fourth is tenon's own
composition form:

- `type: streamable-http` — an absolute HTTPS `url` (nonempty host; no user
  information, query, or fragment; validated without contact) plus optional
  `headers`, a string-to-string map.
- `type: stdio` — an agent-root-relative `command` (`./…`,
  containment-validated the way a plugin-relative command is but anchored at
  the agent root) naming an existing regular file whose bytes and executable
  bit join the fingerprint, plus optional `args` (plain strings;
  `${PLUGIN_ROOT}`/`${PLUGIN_DATA}` rejected by name, any other `$` left
  literal), `env`, and `cwd` (the same containment rule as `command`,
  defaulting to the agent root when absent). Bare PATH-resolved names and
  absolute paths are refused: PATH lookup is exactly the drift this design
  declines. The executable is ordinary agent source living outside `mcp/`,
  so the repository is its pin and no package store participates.
- `type: installed` — `package` + `capability`, resolved offline through the
  integration store, whose stable server name must equal the filename. This
  arm is tenon's own, not spec vocabulary, and `headers` is an unknown field
  on it.
- A mask — exactly `override: plugins/<name>` (the plugin's storage
  directory) and `enabled: false`, no `type`, no other field, and no body. It
  suppresses a plugin-declared server of this file's name without replacing
  it.

`headers` and `env` share one value grammar: a literal containing no `$`, or
an optional literal prefix containing no `$` followed by exactly one `${VAR}`
reference whose name matches `[A-Z_][A-Z0-9_]*`, with nothing after it.
`${PLUGIN_ROOT}` and `${PLUGIN_DATA}` are rejected by name — they are
plugin-root machinery with no meaning in an authored file. Tenon never reads,
resolves, copies, or persists a *value*; only the variable *name* reaches
generated configuration, and the harness's own process environment is what
resolves it, if anything does. Literal header and `env` values are
package-visible configuration that must not contain secrets — the author's
responsibility, since tenon claims no heuristic for recognizing one.

Authentication is discovered, never declared: there is no `auth` field, no
OAuth configuration, no token, and no credential reference in authored
source. Declaring a remote endpoint that requires OAuth is fine — an HTTP
server answering `401` advertises its authorization server and the native
harness performs the flow and owns the resulting tokens. Tenon renders the
URL and stops. `type: sse` fails as a deprecated, unsupported transport,
before workspace mutation, rather than being warned and skipped the way a
plugin's SSE server is: an authored file is a first-class request, and
silently dropping it would leave an agent short of a capability its own
source says it has. A leftover `connections/` directory (the prior name)
fails closed with a migration diagnostic naming `mcp/`
(`mcp.migration.connections-dir`) rather than being silently ignored, and
nothing is auto-migrated.

Optional trimmed Markdown after the frontmatter (at most 1,024 characters) is
model-facing usage context rendered once into generated instructions in
lexical order, with one boundary statement that the native harness owns MCP
startup, trust, approval, authentication, discovery, calls, and effects. A
mask carries no body: it declares absence, not guidance.

Composition splits by relationship (ADR 0026), and the composed set is
computed once so both drivers render an identical result:

| Collision | Outcome |
| --- | --- |
| Any server claiming `managed` | Fails closed; `managed` is reserved and unmaskable |
| Two plugins declaring one name | First wins, with a warning (ADR 0010, unchanged) |
| Authored server vs. plugin server | The authored server wins and is emitted; the plugin's is not; a warning (`mcp.name.shadowed`) names both sources |
| A mask vs. the plugin server it names | The plugin's server is suppressed, silently — masking is deliberate, and the mask file is the record |

An authored `mcp/<name>.md` file is one per name, so two authored servers
cannot otherwise collide; the `mcp.name.collision` check exists only as
defense-in-depth against a future change to that structure. A dangling
override — the named plugin absent, or present but not actually contributing
a server named for this file — fails before mutation
(`mcp.override.dangling`), as does `enabled: true` (`mcp.override.enabled`; a
true mask would be a no-op, since the plugin's server is already emitted) and
a non-empty body (`mcp.override.body`).

Authors need not hand-edit native configuration:

```text
tenon mcp add AGENT NAME --url HTTPS_URL [--header 'K: V'] [--context TEXT] [--pins FILE]
tenon mcp status AGENT [NAME] [--pins FILE]
tenon mcp remove AGENT NAME [--pins FILE]
```

Commands take the exact positional agent root, proven either way the
Instructions section names — so a supplied pin set proves an
instructions-free root here exactly as it does for check and apply — never
search ancestors or choose a harness, and finish by directing the author to
run `tenon apply` for each intended workspace. There is no update command;
the Markdown is ordinary versioned source. `mcp add` writes the
`streamable-http` arm only; the stdio, installed, and masking arms are typed
by hand, which every other command here treats identically (see Known
limitations). `mcp status` is the one offline view of the agent's entire
composed MCP surface (issue #54): one row per authored server, one per
accepted plugin-provided server, one per plugin server an authored server
shadows, and one per masking declaration; `check --emit catalog` is the
wider gate-proven view the same resolution feeds. It contacts nothing — a remote
entry reports `runtime=unchecked` — and required ambient environment
variable names are reported by name only, matching `integration inspect`'s
convention.

GitHub is the reference journey, and it is a hosted remote server: four lines
of `mcp/github.md` naming `https://api.githubcopilot.com/mcp/`, one apply,
and one browser consent the harness itself conducts. Tenon holds no token and
writes no credential store. **The authorization the operator grants lives in
harness-owned storage tenon neither writes nor reads, and the harness,
model-accessible execution tools, and processes with access to that storage
may use it; a read-only workspace does not constrain GitHub effects.** The
curated `github/github-mcp-server` integration package with a
`GITHUB_PERSONAL_ACCESS_TOKEN` in the harness environment remains fully
specified, validated, resolved, and emitted as the `type: installed` arm, and
remains the answer for air-gapped or org-policy operators; ADR 0026 deferred
it as the *reference* journey without withdrawing it. Both journeys, their
lifecycles, and their troubleshooting live in
[the native GitHub MCP journey](github-native-mcp.md).

**Schedules.** Nested Markdown files under `schedules/`; the relative path
without `.md` is the schedule name. Strict frontmatter carries exactly one
`cron` string (standard five-field, bounded printable ASCII); the non-empty
body is the task prompt. Apply validates and fingerprints schedules but
starts no clock. See headless operation below for execution.

**Harness-specific files.** `harnesses/claude/.claude/` and
`harnesses/codex/.codex/` carry intentionally nonportable native project
files, copied byte-for-byte to only the selected harness at the same
workspace-relative paths. Tenon does not parse, merge, or validate their
semantics. Tenon-owned destinations remain reserved (Claude `.claude/skills/`
and `.claude/agents/`; Codex `.codex/config.toml` and `.codex/agents/`),
including case-folded aliases. Authors must not place credentials in these
files; tenon does not claim reliable secret detection.

**Bounds.** Authored source is bounded by implementation-owned safety
ceilings rather than ordinary-use quotas; exceeding a ceiling fails before
workspace mutation:

| Surface | Count ceiling | File and aggregate ceilings |
| --- | ---: | --- |
| Root instructions | One optional file (the root needs instructions or a pin set) | 128 KiB |
| Root and imported skills | 256 aggregate | 1,024 files per skill; 8,192 files and 64 MiB across the set; `SKILL.md` 128 KiB; other resources 16 MiB each |
| Authored tools | 128 | 1,024 source and dependency files; 1 MiB each and 64 MiB aggregate |
| Immediate subagents | 128 | 128 KiB each and 16 MiB aggregate |
| Schedules | 256 | 128 KiB per source, including a 32 KiB prompt; 16 MiB aggregate |
| Plugins (`plugins/`: vendored directories and reference files) | 128 entries, combined | `plugin.json` and `mcp.json` 128 KiB each; 1,024 entries per plugin `skills/` location; a reference file 8 KiB, body at most 1,024 characters |
| Fetched plugin reference tree (`tenon plugin fetch`'s cache) | Not aggregate-bounded across references | 64 MiB and 8,192 files per fetched tree |
| Accepted plugin MCP servers | 128 aggregate | Generated native MCP configuration at most 8 MiB |
| Selected harness-specific files | 1,024 | 1 MiB each and 8 MiB aggregate |
| Authored MCP servers (`mcp/`) | 128 | 8 KiB per source; context at most 1,024 characters |
| Declared `type: stdio` command executables | 16 declared stdio servers | 16 MiB per resolved command file; 64 MiB aggregate across every distinct one |
| Pin set | One optional file, supplied at application | 32 KiB |

Everywhere: authored entries are bounded regular files and real directories
with valid UTF-8 relative paths; symlinks are never followed and are rejected
even where a native harness supports them, so portable source cannot escape
the agent project.

## Apply and handoff

```sh
tenon apply AGENT --workspace WORKSPACE --harness <claude|codex>
```

Apply validates the authored project, the target harness, tool definitions,
and locked dependencies, then materializes owned native files in the selected
workspace so the user starts the harness normally. `--workspace` defaults to
the agent directory; the agent source and the workspace are independent.
Claude receives `CLAUDE.md`, `.mcp.json`, `.claude/skills/`, and
`.claude/agents/`; Codex receives `AGENTS.md`, `.codex/config.toml`,
`.agents/skills/`, and `.codex/agents/`. Generated files are visibly
tool-owned and disposable. Apply refuses to overwrite hand-authored native
files or any tenon-owned file modified since the previous apply, and
reapplying identical source is deterministic. An explicit `--discard-local`
flag lets apply overwrite a tenon-owned file modified since the previous
apply instead of refusing it; it never widens the hand-authored-file
refusal; a file apply never recorded as its own is refused with or without
the flag. All authored inputs join one source fingerprint recorded with the
apply, so stale or edited generated setup fails closed. Codex project trust
remains the user's native decision; apply never edits global harness
configuration or trusts a project on the user's behalf.

```sh
tenon check AGENT [--harness <claude|codex>] [--emit files,catalog] \
  [--pins FILE] [--write-pins FILE] [--model VALUE] [--format <prose|jsonl>]
```

`tenon check` is the one gate over an agent project, and it writes nothing
to any workspace. Without `--harness` it is the portable gate: the project
loads and is bounded, tool contracts are proven, and tool preparation runs
exactly as apply runs it, against a throwaway cache that is discarded
afterwards. With `--harness` it additionally verifies a supplied pin set
before any generation and then performs a generation dry-run against the
same target apply would build, so check and apply fail identically on the
same source. It exits nonzero on failure.

Diagnostics are bounded prose by default, with a machine-readable mode (one
JSON diagnostic per line in the reference rendering) carrying a stable
identifier, the authored path, and the exact rule violated; apply's own
failures carry the same identifiers. Prose stays primary for people; the
stable identifiers exist because a drafting harness or an improvement loop
correcting its own files cannot reliably parse prose. The binding
requirements are parity with apply, stability of the identifiers, and
machine readability — not the flag or the framing.

`--emit` names the inventories the gate has already resolved, and they are
emitted only once the gate passes: an ungated inventory would describe a
source that may not run at all.

- `--emit files` reports every authored file feeding the fingerprint — its
  path, its own content hash, and its executable bit — in the rollup's own
  order.
- `--emit catalog` reports the resolved capability inventory: skills
  (including plugin-merged ones, with their descriptions), tools with their
  language, MCP servers, subagents, and schedules, exactly as the load
  resolved them. An MCP entry's `transport` speaks one vocabulary whichever
  side declared the server — `stdio` for a locally spawned process,
  `streamable-http` for a remote HTTPS endpoint, `installed` for a server
  relayed through an installed integration package — so an authored
  connection's kind and a plugin-declared server's transport are directly
  comparable; `source` is what distinguishes where an entry came from, as it
  does for every other kind. The catalog is a projection of the gate's own working set;
  tenon never accepts one as input, because an authored capability
  inventory is exactly the second inventory principle 9 forbids.

`--write-pins FILE` resolves the current runtime closure once the gate has
passed and writes the pin set bound to the fingerprint just proven, so a
pin set is only ever minted by a project that gates now; `--model VALUE`
records the operator's advisory model choice into that file, and is
meaningless without `--write-pins`. Writing pins without a supplied
`--pins` loads for write, which accepts an instructions-free root — the
gate mints the very pin set that later proves that root. Both flags require
`--harness`, since the closure a pin set pins is the one generation for a
named harness resolves.

On success, in the machine-readable mode, every command emits one further
JSON object after any diagnostic and inventory lines: a result summary. Its
one constant field is `outcome`; the rest describe what that command
produced and therefore vary by command. Check's carries the agent name, the
source fingerprint, and the path `--write-pins` wrote; apply's adds the
harness, workspace, and the written/removed/managed-tool lists; stage's
carries the agent, the fingerprint, and the output directory; clean's
carries only the number of files removed, because clean has no agent and no
source to name. This
object is shaped differently from a diagnostic line — it has no `id`,
`path`, or `rule` field — so a consumer parsing the stream must expect it
as the stream's final, distinct object rather than mistake it for a
malformed diagnostic. A failing run ends the stream the same way, so a
consumer reading objects until end of stream never infers failure from the
absence of a summary. A `gate_failed` object additionally carries
`source_digest`, a `sha256:` content hash over the authored files that
failed, so a rejected candidate is attributable without a consumer hashing
the tree itself. It is explicitly not a fingerprint and never joins with
one: a digest names bytes, a fingerprint names a configuration the gate
proved. The two are separated by construction — the digest is hashed under
its own domain prefix, so a source's digest always differs from that tree's
fingerprint — but both render as `sha256:` and 64 hex characters, so what
tells them apart is the field a value arrives in, never the value alone.
They never appear together: a passing run carries a fingerprint and no
digest, a failing one the reverse. `check`, `apply`, `drift`, `stage`, and
`run` all carry it on a gate failure; `stage verify` does not, having no
source to name. The digest covers the authored inputs the loader itself
reads — `instructions.md`, the component directories, and the native tool
dependency files at the agent root — and nothing else, so generated output,
version-control state, and vendored dependency trees cannot move it. The
field is omitted only when the agent root itself cannot be read. The outcome
vocabulary is
`ok / gate_failed / drift / blocked / error`: `gate_failed` when the source
itself is invalid, `drift` when the workspace no longer matches, `blocked`
when clean refuses to remove what it found, and `error` when the run could
not complete for a reason that is not the source's fault — an unreadable
pin set, an unwritable path, a closure that would not resolve, an os error
mid-clean, a harness that would not start. That distinction is the point of
the field: the first four are findings about the source or the workspace,
which a loop scores; an `error` is a statement about the environment, which
the loop retries or escalates and never scores. An `error` object carries
an `error` field with the same prose stderr carries, bounded, so a consumer
reading only the stream still learns what went wrong. The one deliberate
silence is a usage error: exit 2, no outcome object, because a malformed
invocation never ran. The `outcome` field is
the authoritative machine signal; the process exit code is its coarse
projection.

Two conventions apply to every command above and below. `--format` governs
output rendering everywhere it appears — `prose` for people, `jsonl` for a
consumer — and `TENON_HARNESS` supplies the default for an unset
`--harness`, with an explicit flag always winning and an invalid
environment value named honestly as coming from the environment. On `check`
that default is load-bearing rather than cosmetic: it selects the harness
gate, so a `check` invoked with no `--harness` in a shell where
`TENON_HARNESS` is set runs the full harness gate — pin verification and
the generation dry-run — and not the portable one. Omitting `--harness`
means the portable gate only when `TENON_HARNESS` is unset, which is what
the flag's own help text says. The one
exception is `clean`, which ignores `TENON_HARNESS` deliberately.

`tenon drift AGENT --workspace WORKSPACE --harness <claude|codex>` reports
whether a workspace still carries exactly what a fresh apply would produce,
writing nothing at all: it regenerates every tenon-owned file in memory on
apply's own generation path, then compares each against both the workspace
and the apply record — the same ownership rule apply's conflict check
enforces, not merely a byte comparison against the fresh regeneration — and
reports it unchanged, modified on disk (with a unified diff), missing, or
stale (recorded by a previous apply but no longer generated). A workspace
that does not exist is not a gate failure — the source is fine, the
environment is what is missing — so it classifies as what it is: no record,
every generated path missing, and the run ends in the ordinary `drift`
outcome; drift's own gate (including authored-tool preparation) runs
against the source, not against the workspace, so a missing workspace can
never be reported as a source failure. A path passed as `--workspace` that
exists but is a regular file is neither of those: it is a mistake in the
invocation, reported as a usage error with no outcome object at all. Drift
deliberately never adopts a workspace edit back into source: generation is
lossy in reverse, so tenon never guesses author intent from a diff. Drift
only shows the diff; the author edits source and reapplies, optionally with
`--discard-local` to explicitly discard the workspace edit. Its
machine-readable mode carries the same stable per-finding identifiers and
diagnostics discipline as check and apply.

**Clean.** `tenon clean --workspace WORKSPACE [--harness <claude|codex>]
[--force] [--format <prose|jsonl>]` is the inverse of apply: it removes the
files an apply record says tenon wrote, prunes the directories that
emptying them leaves behind, and drops the record itself. It takes no
AGENT, because it acts on the workspace's own records — so it still works
when the source that produced them is gone (uninstall) or when only a
previously applied harness's files need removing (switching harnesses
otherwise leaves the first one's files behind). Ownership discipline is
apply's, run in reverse: a file recorded but modified since that apply
refuses the whole clean, all-or-nothing, rather than half-uninstalling a
workspace, and `--force` overrides exactly that refusal; a file tenon never
recorded as its own is never touched, with or without the flag. That
all-or-nothing is decided at plan time, before anything is removed. Because
the workspace can change underneath a running clean, every path is
re-classified immediately before its own removal, and one that no longer
classifies as removable stops the clean where it stands: the files already
removed stay removed, the jsonl stream ends `{"outcome":"blocked"}` and the
prose names the offending path and what changed about it, and the record is
deliberately kept so a re-run can finish the job. An omitted
`--harness` means every harness recorded in the workspace, which is why
clean alone ignores `TENON_HARNESS`: an environment default would silently
narrow a full reset. A workspace with no records succeeds trivially, and a
record owning no files is still dropped. That trivial success is the bare
clean's alone. `clean --harness H` over a workspace holding no `apply-H.json`
record exits 1 — `tenon clean: no claude record in WORKSPACE/.tenon; nothing
to clean for that harness`, and the `error` outcome in jsonl — because the
operator named a harness this workspace was never applied for, and
reporting success would read as "that harness is now clean" for files tenon
never wrote. It is an argument that does not match the environment, not a
refusal to remove something, which is why it is `error` and not `blocked`.

An apply record is durable state on disk, so the paths in it are an input
like any other and are never trusted verbatim. A recorded path that is not
workspace-local, one that would be reached through a parent that is a
symlink rather than a real directory, or one whose parent chain cannot be
read at all, blocks the clean (`escapes-workspace`, `symlink-parent`,
`unreadable-parent`) and is removed by nothing, with or without `--force`:
the flag widens what tenon removes inside a workspace, never where it
removes. The directory pruning that follows a removal is bounded by the
workspace and never removes the workspace itself. Apply enforces the
identical rule on both sides of its own file handling — on the removal of
stale recorded files and on every file it writes — refusing the apply
rather than acting outside the workspace, so a generated parent directory
replaced by a symlink cannot make an atomic write land on a file the
workspace does not contain. A file in `.tenon` whose name resolves to no
harness tenon knows is reported and left alone rather than acted on: in
jsonl mode as `{"ignored":NAME,"reason":"unknown-harness"}`, which is a
report and not a block — the clean continues and still ends `ok`.

## Pins

An optional bounded pin set (`pins.json` in the reference rendering, and
formerly called the agent manifest) pins the runtime closure that the
directory alone cannot express. It belongs to application, not to the
definition: the same source directory applies under different pin sets —
one commit crossed with many of them — without the definition changing. The
pin set is therefore supplied to check, apply, and run rather than stored
inside the agent source, and it lives wherever its operator or loop
versions it. Its responsibility, not its encoding or location, is the
contract: it identifies and pins; it never lists. The directory remains the
sole registry of the agent's components, and a supplied pin set whose
expected fingerprint matches the directory also proves the agent root, so a
generated candidate need not carry instructions it does not want. The name
follows the responsibility: a manifest is universally a list of contents,
and this file categorically refuses to be one
([ADR 0027](adr/0027-consolidate-the-read-surface.md)).

Its closed schema records a schema version, the agent name, the expected
source fingerprint, the tenon version, and — per selected harness — the
harness executable version, a model identifier, integration package
identities (package id plus manifest SHA-256), and authored-tool runtime
versions (Deno, uv, Go) where the project uses them.

`tenon check AGENT --harness ... --write-pins FILE` is the writer: it
records the currently resolved closure to a caller-chosen path, but only
after the gate it is a flag on has passed, so the pins are bound to a
fingerprint just proven and no ordering between proving and pinning can
arise. The result is an ordinary versioned file and may be edited directly.
`--pins FILE` is the verifier: when a pin set is supplied, check, apply,
and every tenon-owned process open verify the resolved closure against it
and fail closed naming the exact drifted pin; when none is supplied,
behavior is unchanged. The model pin is
emitted through the selected harness's documented configuration and
recorded in provenance; the harness owns model selection, and tenon does not
claim to verify which model actually served a turn.

Every apply record and dispatch lifecycle event carries the source
fingerprint and, when present, the pin set's identity, so observation made
outside tenon — transcripts, evaluations, selection among revisions — can be
joined to the exact configuration that produced it. Tenon retains none of
that observation: no transcripts, no evaluations, no scores. An improvement
loop revising the agent's files is an author like any other: its revision
is validated for form before anything runs, and its merit is judged outside
tenon. The friction inbox remains a supplementary human-facing channel, not
the loop's signal path.

The applied agent is not told how it was set up. Tenon never renders the
pin set, its pins, model identity, or provenance into generated
instructions or any other model-facing content: setup metadata exists for
the operator and the loop, not for the running agent. Whether a harness's
native tools can read files an operator leaves on disk remains native
behavior, and tenon does not claim to blind a harness to its environment.

A pin is an axis of variation, not an editable surface: a loop may try a
different model or harness version by changing a pin, while the components
it can edit remain the authored files. Lineage and population management
belong to version control: a candidate is a source revision crossed with a
supplied pin set, each versioned wherever its owner keeps it, and tenon
neither records lineage nor selects among candidates. How variants are
isolated — worktrees, containers, or sandboxes — is the operator's
infrastructure choice; tenon requires only that each variant is a directory
that applies deterministically.

## Managed tool boundary

One stdio MCP server exposes the bounded built-in `echo` tool, the optional
`record-friction` built-in, and the authored tools to both harnesses. Inputs
and outputs are schema-validated; audit output carries a safe request
identifier, tool name, and lifecycle outcome — never arguments or output. One
long-lived host per authored language serves calls; authors never write
protocol code. The boundary is additive: it does not disable, authorize,
observe, or retry harness-native tools.

Codex treats the generated managed server as required and delegates its tool
approval to tenon, so an authorized managed call does not draw a second
harness prompt; every other generated MCP entry — plugin-provided or
authored, remote or stdio or installed — keeps native per-call prompt
approval. This exemption applies
only to the managed server and does not affect native or unrelated MCP
tools.

`record-friction` is advertised only when root instructions opt in with
`friction-notes: true`. It accepts one bounded UTF-8 note and stores it in a
private, owner-only, per-agent local inbox outside both agent source and
workspace, write-only to models, never automatically read, transmitted, or
applied. It is not telemetry, memory, or evidence. At most 256 records are
retained per agent, and the store never overwrites or silently evicts.

Secret-bearing managed operations do not exist yet. Before one ships, the
secretless operation broker boundary applies
([ADR 0006](adr/0006-use-a-local-secretless-operation-broker.md)): opaque
references resolve only at authorized managed invocations, values never reach
tool hosts, harnesses, models, generated files, or audit, and no backend or
broker code is scaffolded until a concrete operation is selected.

## Integration packages

The integration store is machinery, not the front door. ADR 0026 moved the
reference journey for a third-party server to the hosted remote endpoint, so
no authoring journey requires an installed package any more. What the store
kept is what it was actually good at: an owner-only, immutable,
content-addressed, offline-verifiable place to put bytes an operator has
reviewed and trusted. Two consumers use it today — the `type: installed` arm,
for an air-gapped or org-policy operator who wants an exact pinned executable,
and, as a sibling built on the same properties rather than the same code, the
plugin-reference cache `tenon plugin fetch` fills. The commands below remain
supported; they are no longer the way an author adds a server.

Machine-installed third-party integrations use a metadata-first package
contract distinct from vendored `plugins/`. A bounded schema-version-1
manifest carries identity, provenance, a tenon compatibility range, exact
platform artifacts (size and SHA-256 pinned), the expected executable
identity, and closed versioned capability declarations. Tenon validates
metadata without opening artifacts, fetching URLs, or executing package code.

`tenon integration install SOURCE --trust operator` is the only trust and
installation journey: a local directory or archive containing
`integration.json`, or artifacts fetched only from exact pinned HTTPS URLs
without redirects. There is no registry, package script, dependency
resolver, or signature claim. Installed state lives in one owner-only,
content-addressed, offline-verified store shared across agents and
workspaces; `inspect`, `verify`, `list`, `enable`, `disable`, `update`, and
`remove` operate on that store, and verification is re-run before every use.
Portable agent source can never choose an install source, install or enable a
package, grant trust, or carry a credential; apply gains no network path.

Recognized capabilities are closed schemas. The core implements `native-mcp`
v1: a stable native server name, executable, bounded literal launch data,
required ambient environment names without values, and supported harness
targets — consumed by the `type: installed` arm. `channel-adapter` v1 belongs to
the channel product, specified in the prototype repository: the core does
not implement its recognition and it is not acceptance-gating; it is
reintroduced only if the channel product is ported onto this core. The native harness owns
process lifecycle, credentials, approvals, calls, and effects for everything
a package launches. Required ambient names are diagnostic metadata, not a
credential channel; resolved values never enter generated files, package
state, staging, or evidence.

## Headless operation

```sh
printf '%s\n' '{"input_id":"x-1","text":"..."}' \
  | tenon run AGENT --workspace WS --harness <claude|codex> --input jsonl
```

The turn dispatcher accepts bounded JSONL input, each line carrying a
caller-owned `input_id` and `text`. Input is durably accepted and queued
while a turn is active, processed one FIFO turn per conversation, and mapped
to a resumable native session; ordered JSONL events are emitted. A repeated
input ID deduplicates within its conversation. After restart, active work
without a proven terminal result is uncertain and never silently retried.
Dispatch state is one owner-only file per workspace.

Schedules execute two ways, both requiring current generated setup:

```sh
tenon schedule trigger AGENT NAME --workspace WS --harness codex \
  --input-id OCCURRENCE_ID --turn-timeout 90s --timeout 2m
tenon schedule run AGENT --workspace WS --harness codex
```

`trigger` dispatches one occurrence under a caller-owned stable ID: each
accepted occurrence opens a fresh native task session, duplicates return the
retained outcome without opening a harness, and the bounded turn deadline
aborts a stalled process while durably recording the occurrence as uncertain
with its reason. `run` is an explicit foreground UTC clock: standard cron
evaluated in UTC, first occurrence strictly after startup, no downtime or
clock-jump backfill, no overlap for one schedule, a local lock excluding a
second clock for the same workspace/agent/harness, and graceful drain on
signals. Lifecycle output is bounded and never contains model text. No
daemon, missed-run replay, or hosted delivery runtime.

## Staged agent filesystems

`tenon stage AGENT --harness <claude|codex> --output DIR [--format
<prose|jsonl>]` prepares one
complete runnable filesystem tree at canonical paths for an existing OCI
builder:

```dockerfile
FROM ghcr.io/alee792/tenon/codex:${TENON_VERSION} AS build   # not yet published; see docs/harness-images.md
COPY . /agent
RUN tenon stage /agent --harness codex --output /out/agent

FROM docker.io/library/ubuntu:24.04
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/tenon/ /home/tenon/
USER 65532:65532
ENTRYPOINT ["/opt/tenon/bin/agent-entrypoint"]
```

`ghcr.io/alee792/tenon/codex` is not yet published; build the harness image
locally from [`images/codex/Dockerfile`](../images/codex/Dockerfile) (or
[`images/claude/Dockerfile`](../images/claude/Dockerfile)) instead. The
final base is a pinned Ubuntu LTS platform manifest, `linux/amd64`, glibc —
never Alpine or another musl base without a separately built musl payload.
See [harness images](harness-images.md) for the full compatible-base
contract, both journeys, and what gates publication.

The staged tree carries tenon, the selected harness, immutable agent source,
the generated integration and apply record, an entrypoint, an artifact
manifest, and only the execution closure the agent's tools actually need —
no build toolchains, caches, credentials, login state, trust decisions, or
conversation state. Staging is deterministic for identical pinned inputs,
verifies that preparation did not mutate authored source, and publishes with
one rename only after the manifest is complete. The entrypoint verifies
runtime identity, generated integration, and source fingerprint before a
turn; the same verification is available directly as `tenon stage verify
--artifact PATH [--prefix DIR] [--format <prose|jsonl>]`, whose jsonl run
ends with `{"outcome":"ok","artifact":PATH}` on a clean tree and the
`gate_failed` object on a tampered one. Tenon does not construct OCI layers, contact registries, publish, sign,
deploy, or operate images, and publishing a harness image requires current
permission to redistribute that harness.

## Installation and distribution

The supported platforms are `darwin-arm64`, `linux-amd64`, and
`linux-arm64`. The exact `vX.Y.Z` tag names, for each platform,
`tenon_X.Y.Z_<os>_<arch>.tar.gz` (one executable at the archive root), plus
one `tenon_X.Y.Z_SHA256SUMS` manifest covering every archive of that
release; the user verifies, extracts to a stable `PATH` location, and runs
`tenon apply`. Generated MCP configuration records the resolved absolute
executable path, so moving the binary requires reapplying. `go install` is
not a supported end-user journey, and there is no `tenon package` command:
agent source and lockfiles are inputs to `apply`, while generated hosts and
dependency environments remain disposable workspace-local caches.

## Deferred bets

Recorded once here; none is scaffolded until its trigger arrives:

- **Proposals** — inert, workspace-local, human-reviewed improvement
  records; a future capture tool must remain additive, never apply a diff,
  and never claim reliable secret detection. The prototype's ADR 0008
  records the full convention. Reviewed when an author or improvement loop
  needs a mutation record richer than a branch; falsified — and deleted —
  if version-control review proves sufficient.
- **Secretless operation broker** — the boundary for the first secret-bearing
  managed operation ([ADR 0006](adr/0006-use-a-local-secretless-operation-broker.md)).
  Reviewed when that operation is selected; falsified if native harnesses
  ship an equivalent credential boundary first.
- **Post-run summaries** — must reference native logs rather than duplicate
  transcripts. Reviewed when a harness exposes stable runtime IDs;
  falsified if native logs remain sufficient on their own.

## Failure and safety behavior

- Missing, stale, ambiguous, or edited generated harness integrations fail
  closed.
- Input, output, queue, process lifetime, state size, and protocol lines are
  bounded.
- Durable state is owner-readable only and written atomically.
- Process failure is distinct from a completed or failed model turn.
- An uncertain external effect is never described as exactly-once or retried
  without a target idempotency contract.
- Tenon-owned diagnostics never expose credentials, private prompts, or raw
  process output; native harness and external-server diagnostics remain
  outside that claim.
- Tenon never claims to enforce instructions, inspect native effects, sandbox
  authored code, or make model behavior safe from outside the harness.

## Acceptance

Every stated behavior in this specification binds; the list below is the
proof skeleton, not the whole contract. The core is complete when
credential-free tests (fake harness processes; no live model calls) prove:

1. One authored project compiles deterministically for both harnesses, and
   apply produces native, discoverable files while refusing conflicts and
   modified-file overwrites; a directory proven by neither instructions
   nor a supplied matching pin set is refused as not an agent project,
   and an instructions-free project generates an empty always-on surface
   rather than failing.
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
6. Authored MCP servers generate exact native configuration for the remote,
   stdio, and installed arms without contacting anything; a name claiming
   `managed` fails before mutation, a collision with a plugin server resolves
   author-wins with a warning, a mask suppresses exactly the plugin server it
   names and a dangling one fails before mutation; and a conspicuous fake
   ambient value never appears in generated files, state, staging, or
   evidence.
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
11. A supplied pin set is verified before apply and before every
    tenon-owned process open: a drifted harness version, package identity,
    or source fingerprint fails closed naming the exact pin; writing the
    pin set for an unchanged closure is byte-identical; an unsupplied pin
    set changes nothing; and no pin, fingerprint, or provenance value
    appears in model-facing generated content.
12. Check reports the same failures as apply without mutating anything, and
    its structured diagnostics carry stable identifiers and authored paths
    that match apply's own failures; its inventories are emitted only for a
    source that passes the gate.
13. Clean removes exactly the files an apply record owns and then the
    record itself, refusing the whole removal when one of them was modified
    since that apply unless `--force` is passed, and never touching a file
    tenon did not record.

## Known limitations

Recorded here rather than hidden, per the failure and safety principle
above:

- **Remote server behavior is not pinned by the fingerprint.** The project
  fingerprint covers *declared source* — a URL, its headers, the guidance
  body, and the staged bytes of anything local — and has never covered what a
  hosted endpoint does. A remote server's tool catalog, schemas, and results
  can change under an unchanged fingerprint, and tenon will not notice.
  Reproducibility is therefore asymmetric on purpose (ADR 0026): local bytes
  are pinned by the repository, remote behavior is not pinned at all. Probing
  remote catalogs for drift is a possible future `tenon mcp status --probe`
  and is deliberately out of scope.
- **`mcp add` authors the `streamable-http` arm only.** The stdio, installed,
  and masking arms are fully specified, validated, resolved, and emitted for
  both harnesses; only the authoring convenience is missing, so
  `tenon mcp add --package ... --capability ...` is refused with a
  diagnostic rather than silently writing a file it cannot prove. Authors
  write those `mcp/<name>.md` files by hand — the frontmatter is shown above
  and in [the native GitHub MCP journey](github-native-mcp.md) — and `mcp
  status`, `check`, and `apply` treat the result exactly as they treat a
  generated one.
- **`tenon plugin fetch` and `tenon plugin update` shell out to the system
  `git` executable.** Resolving a `plugins/<name>.md` reference's pinned
  revision is the one online operation in tenon's plugin story (ADR 0026),
  and it is an operator-owned tool dependency rather than a vendored git
  implementation: a machine without `git` on `PATH` cannot fetch a plugin
  reference, though it can still `apply` a project whose references are
  already cached from an earlier fetch. `tenon plugin status` and `tenon
  apply` never invoke `git`.
- **A plugin reference's resolved content lives in two different places,
  deliberately, depending on the journey.** `tenon stage`
  ([issue #58](https://github.com/alee792/tenon/issues/58)) materializes a
  `plugins/<name>.md` reference's resolved cache tree into the staged
  filesystem at `plugins/<name>/`, re-anchored exactly like a vendored
  plugin: a staged image is self-contained, and its generated configuration
  never points outside the tree it ships. An ordinary `tenon apply` does
  not copy that content into the workspace — it keeps pointing the
  resolved reference's `mcp.json` servers at the operator's plugin cache,
  including `PLUGIN_ROOT`, exactly as a plain apply always has, because
  copying multi-megabyte plugin trees into every workspace on every apply
  is the wrong default when the cache already exists for this. The
  consequence is real and operator-visible: pruning the plugin cache
  silently breaks an already-applied workspace's reference-declared servers
  until the next `tenon plugin fetch` re-populates it. `tenon mcp status`
  and `tenon plugin status` name this dependency explicitly for every
  reference-declared server and every resolved reference, respectively.
  Vendored plugins are unaffected in both journeys: their content already
  lives in the agent tree, so there is no cache to depend on.
- **Staging.** Per [ADR 0021](adr/0021-execute-authored-tools-from-a-self-contained-closure.md),
  the native harness runtime is not yet bundled into the staged tree
  (expected on the base image), and the authored-tool execution closure is
  staged whole rather than minimized, both recorded in the staging artifact
  manifest. Go, Python, and TypeScript authored tools all stage and serve
  from the staged tree today: the closure is a self-contained Go host
  binary, a pinned standalone CPython interpreter with the project's locked
  dependencies laid flat beside it (no venv), or the `deno` executable
  itself beside a pruned, cached-only `DENO_DIR` (issue #16), reachable from
  the staged apply record's `closure_root`. The container gate is manual: CI does not build or run
  a staged image, so run
  [`scripts/check-staged-images.sh`](../scripts/check-staged-images.sh)
  (see [`docs/staged-acceptance.md`](staged-acceptance.md)) before a
  release.
- **Python and TypeScript runtimes are fetched once per machine, not once
  per prepare.** `tenon check` and `tenon apply` for a Python-tool agent
  install the pinned standalone CPython interpreter (`uv python install`,
  roughly 90MB) through a shared, content-addressed runtime cache under
  `os.UserCacheDir()/tenon/runtimes/` (issue #38): the first agent on a
  machine to resolve a given interpreter identity installs, normalizes, and
  locks it down read-only, and every later resolution of that same identity
  — any other agent, or `check`'s own throwaway prepare, or a repeat
  `apply` — hardlinks it out rather than reinstalling. The `deno` executable
  TypeScript tools carry into their closure is shared the same way, keyed by
  its own content hash. A network-restricted machine still needs the pinned
  runtime artifact reachable through whatever channel supplies tenon's other
  pinned inputs the first time any agent resolves a given version; every
  later prepare of any agent needs no network for that runtime at all. A
  `requires-python` constraint in `pyproject.toml` installs the *floor* of
  the range (`>=3.11,<3.13` installs 3.11, not 3.12); a `.python-version`
  file, when present, names the version exactly, takes precedence over
  `requires-python`, and — being an exact pin — can resolve to a cache hit
  without invoking `uv` at all. What still runs on every prepare regardless
  of the shared runtime cache: `uv export`/`uv pip install` for a project's
  own locked Python dependencies (unshared across agents, since independent
  projects rarely lock identical dependency sets — a stated future
  extension of issue #38) and `deno check` against a project's own tools.
- **Real harness drivers.** The Claude and Codex drivers are validated by
  pure-function unit tests plus manual `//go:build harness` integration
  tests against live binaries; CI does not run the latter, so CI green means
  "dispatcher and drivers correct as specified," not "verified against
  today's Claude/Codex." The Codex driver's successful-turn path has not
  been validated live — only its credential-safe 401 classification has.
- **Pin verification scope.** A supplied pin set is verified at
  `tenon run`'s session start, not re-verified per turn within that
  session; the recurring `schedule run` path does re-verify each
  occurrence.
- **Not in scope (no ADR).** Evaluations, scoring, transcript retention,
  selection among revisions, lineage tracking, a marketplace, and any
  acquisition beyond `tenon plugin fetch`'s explicit pointer-and-pin
  resolution; the conversational channel product stays in the prototype.

## Explicit non-goals

- A model loop, context manager, or cross-harness chat UI
- A marketplace, automatic updater, dependency resolver, or lock file; the
  one acquisition path is an explicit `tenon plugin fetch` of a pointer the
  author wrote and pinned, and no project load ever acquires anything
- Claude Agent SDK or hosted OpenAI agent runtimes
- Background or distributed schedule clocks, workflows, independently
  configured nested subagents, or deployment orchestration
- Building OCI manifests or layers, publishing or signing images, or hosted
  image operation
- Governance claims over native harness tools
- Evaluations, scoring, transcript retention, or selection among agent
  revisions — tenon is an improvement loop's substrate, never the loop
- Hosted secret managers and model-visible secret-bearing managed operations
- GitHub OAuth or GitHub App enrollment, a managed MCP proxy, credential
  brokering, or per-call tenon authorization
- Automatic or unreviewed promotion of agent-authored improvements
