# Aligning tenon with the Agent Client Protocol

- Status: research record answering one question — should tenon be
  reoriented to be native to the Agent Client Protocol (ACP)? — and
  proposing where to align. Overtaken: Stage 1 was built as an ACP driver
  behind `--driver acp` (ADR 0028), and then the maintainer asked the
  question Stage 3 deferred — does `tenon run` need to exist at all? — and
  answered no. [ADR 0029](../adr/0029-stop-driving-the-harness.md) removes
  the dispatcher, every harness driver, and schedule execution; the
  headless leg is now the operator's client launched in an applied
  workspace, exactly "What interop already costs nothing" below.
- Last verified: 2026-09-04. External claims are cited to their source;
  the protocol and both adapters release often, so re-verify before acting.

## The answer in short

Reorient the **runtime seam**, not the product. ACP is a session protocol
between a client (an editor, a headless driver, a channel gateway) and an
agent process. It carries prompts, streamed updates, permission requests,
and per-session MCP servers. It has no authoring format, no plugin concept,
no configuration compile, and no notion of a reproducible source. Everything
tenon's north star holds constant — the legible folder, the crossing into
native files, validation before mutation, drift, the fingerprint — sits on
the side ACP does not cover, and the open standards on *that* side are Agent
Skills and Agent Plugins 1.0, which tenon already consumes.

The one place ACP and tenon overlap is `internal/harness`: two bespoke
vendor drivers (Claude's `stream-json` stdin/stdout, Codex's `app-server`
JSON-RPC) behind the `Driver` seam. That is where alignment subtracts
machinery, and it is the only place. The rest of this document is the
evidence for that boundary and a staged proposal with an appetite, a
falsifier, and a review date (tenet 4).

## What ACP is, as it stands

Facts a decision rests on, from the protocol repository
([agentclientprotocol/agent-client-protocol](https://github.com/agentclientprotocol/agent-client-protocol)):

- JSON-RPC 2.0 over stdio, newline-delimited; the client launches the agent
  as a subprocess and its stderr is for logs. A streamable-HTTP transport is
  a draft RFD, not stable.
- `protocolVersion` 1 is stable (schema v1.21.0, 2026-08-20); v2 is alpha.
  Governance moved from Zed to the `agentclientprotocol` organization,
  Apache-2.0.
- Agent-side methods: `initialize`, `authenticate`, `session/new`,
  `session/prompt`, optional `session/load`, `session/set_mode`,
  `session/set_config_option`; notification `session/cancel`. Client-side:
  `session/request_permission`, optional `fs/read_text_file`,
  `fs/write_text_file`, `terminal/*`; notification `session/update`.
- A prompt turn returns one `stopReason` (`end_turn`, `max_tokens`,
  `max_turn_requests`, `refusal`, `cancelled`); updates stream as
  `agent_message_chunk`, `tool_call`, `tool_call_update`, `plan`,
  `usage_update`, and friends.
- `session/new` takes an absolute `cwd` and `mcpServers[]` (stdio always;
  `http` when the agent advertises it; `sse` deprecated). The agent still
  reads its own native configuration from `cwd` — ACP does not replace it.
- Extensibility is `_meta` on every object and `_`-prefixed custom methods.
  There is no "agent plugin" concept in the protocol.
- Official SDKs: Rust, TypeScript, Python, Kotlin, Java. Go is community
  ([coder/acp-go-sdk](https://github.com/coder/acp-go-sdk)). The protocol is
  small enough that a stdlib client is the same size as today's Codex driver.

The adapters that matter to tenon:

| Adapter | What it is | Reads the files tenon applies? |
| --- | --- | --- |
| [claude-agent-acp](https://github.com/agentclientprotocol/claude-agent-acp) | TypeScript on the Claude Agent SDK; `npx @agentclientprotocol/claude-agent-acp`; v0.74.0, near-daily releases | Yes: sessions are created with `settingSources: user, project, local`, so `CLAUDE.md`, `.claude/settings.json` (including the injected model pin), and `.mcp.json` load. Caveat: [issue #94](https://github.com/agentclientprotocol/claude-agent-acp/issues/94) — when the client advertises `fs` capabilities, reads and writes route through the client and can bypass `settings.json` deny rules. |
| [codex-acp](https://github.com/agentclientprotocol/codex-acp) | TypeScript over the Codex App Server (the Rust `zed-industries/codex-acp` is archived); `npx -y @agentclientprotocol/codex-acp`; `CODEX_PATH`, `CODEX_CONFIG`, `INITIAL_AGENT_MODE` | Yes, by construction: it drives the same `codex app-server` tenon's driver drives today, so `AGENTS.md`, `.codex/config.toml`, and `.agents/skills/` behave as they do now. |

Beyond those two, the [registry](https://github.com/agentclientprotocol/registry)
lists roughly forty agents (Gemini CLI, Copilot CLI, Cursor, Goose,
OpenCode, Kimi, Qwen Code, and more), each as an `agent.json` with a pinned
distribution: `npx` package, `uvx`, or per-platform binary with SHA-256.

The clients the question names:

- **acpx** ([openclaw/acpx](https://github.com/openclaw/acpx), MIT, pre-1.0,
  Node 22+): a headless ACP client. One-shot `acpx exec`, persistent
  sessions under `~/.acpx/sessions/`, `--format json` emitting the raw
  `session/update` stream as NDJSON, a permission policy
  (`--approve-all`, `--approve-reads`, `--deny-all`,
  `--non-interactive-permissions`, per-tool `--policy` JSON), `--cwd`, and
  `--mcp-config` for session-scoped servers.
- **OpenClaw** ([openclaw/openclaw](https://github.com/openclaw/openclaw),
  MIT): a locally run assistant whose gateway bridges chat channels
  (Discord, Slack, Telegram, WhatsApp, and others). It is an ACP client
  through the `@openclaw/acpx` runtime plugin — `/acp spawn <harness>`
  launches Claude, Codex, Gemini, Copilot, Cursor, or OpenCode sessions on
  the host — and separately exposes itself as an ACP agent.

And the definition-side standards, for contrast: Agent Skills
(`SKILL.md`) and Agent Plugins 1.0 (2026-08-06: `plugin.json`, `skills/`,
`mcp.json`, reverse-domain vendor folders; maintained by Amazon, Cursor,
Microsoft, OpenAI, Vercel). Claude Code did **not** adopt Agent Plugins 1.0
and keeps `.claude-plugin/plugin.json`; Codex additionally has its own
`.codex-plugin` marketplace. Neither format has any relationship to ACP.

## Mapping tenon's subsystems against the protocol

| Subsystem | Package(s) | ACP replaces it? |
| --- | --- | --- |
| Project load and validation | `internal/agentproject`, `frontmatter`, `diagnostics` | No. ACP has no definition format. |
| Compile to native files | `internal/claude`, `internal/codex`, `internal/apply` | No. Adapters read native files from `cwd`; the crossing between Agent Plugins 1.0 and Claude's own plugin format is a wider gap after August 2026, not a narrower one. |
| Fingerprint and pins | `internal/manifest`, `version` | No, but ACP adds a pin: `initialize` returns `agentInfo{name, version}`, and the registry's `agent.json` pins a distribution by exact package version or SHA-256. |
| Managed tool boundary | `internal/mcp`, `toolruntime` | No. The managed server is an MCP server either way; ACP could pass it per session through `session/new.mcpServers` instead of through `.mcp.json`, but see "Not proposed". |
| Turn dispatcher | `internal/dispatch`, `dispatchstate` | Not the dispatcher — its durable accept/dedupe/uncertain discipline and the fingerprint-stamped wire stream have no ACP equivalent. Its `harness.Driver` dependency is what changes. |
| Harness drivers | `internal/harness/claude` (186 lines), `internal/harness/codex` (346 lines) | **Yes.** Two vendor protocols become one ACP client. |
| Schedules | `internal/schedule`, `cron` | Indirectly: they ride the dispatcher. |
| Stage | `internal/stage` | No. A staged image still expects a harness on the base image; under ACP that is the adapter plus the harness. |
| Integration store, plugin references | `internal/integration`, `pluginref` | No. The registry's binary distribution (per-platform archive, SHA-256, `cmd`, `args`, `env`) is the same shape as the store's metadata-first manifest ([ADR 0014](../adr/0014-use-process-isolated-integration-packages.md)); a later slice could accept `agent.json` directly. |
| `improve/` fan-out and evolve | Python, consumes the CLI | Unchanged as long as `tenon run`'s JSONL stream keeps its schema. |

Two things the mapping makes plain. First, the deletion on offer is small in
lines (about 530 of driver code, replaced by a client of similar size) and
large in obligation: tenon stops tracking two vendor wire protocols that
each vendor changes at will, and every registry agent becomes a runtime
harness at zero driver cost. Second, "native to ACP" would cost nothing on
the authoring side because there is nothing there to become native to.

## What interop already costs nothing

Because tenon's output is files in a workspace, an applied workspace already
works under any ACP client that launches the adapter with that workspace as
`cwd`:

```sh
tenon apply my-agent --workspace WS --harness claude
acpx claude --cwd WS "review the open pull request"
```

Zed's custom `agent_servers` entries, JetBrains' `acp.json`, Neovim and
Emacs clients, and OpenClaw's `/acp spawn` are the same story. This is the
crossing doing its job, and it is worth stating in the use cases rather
than building anything: **OpenClaw is the conversational channel product
the vision reserved for the prototype**, run by someone else over an open
protocol. Tenon should retire that ambition outright and point at OpenClaw.

What that path lacks is the join key: acpx's NDJSON carries no source
fingerprint, so a loop scoring an acpx-driven run must record
`tenon digest` beside it. That is a documentation line, not machinery.

## Proposal

Staged, each stage disposable if its falsifier fires.

### Stage 0 — document the crossing (no code)

Add the recipe above to [use cases](../use-cases.md), retire the
channel-product sentence in the [vision](../vision.md) in favor of
OpenClaw, and record the fingerprint-join caveat. Verify by hand that
claude-agent-acp in a tenon-applied workspace honors `CLAUDE.md`,
`.mcp.json` (including the managed server), and the injected model pin in
`.claude/settings.json`; do the same for codex-acp. Those two checks are
the evidence Stage 1 needs and cost an afternoon.

### Stage 1 — an ACP driver behind the existing seam (spike)

Appetite: one week. Implement `internal/harness/acp` as a stdlib JSON-RPC
client satisfying `harness.Driver` unchanged:

| Today's driver does | ACP driver does |
| --- | --- |
| open fresh or resume by native id | `session/new`, or `session/load` when the agent advertises `loadSession`; replayed history is discarded, not emitted |
| one text turn in | `session/prompt` with one text `ContentBlock` |
| stream text deltas | `agent_message_chunk` text → `agent.output.delta`; every other update kind is ignored |
| classify the terminal | `stopReason` → `completed` (`end_turn`), `failed` (`refusal`, `max_tokens`, `max_turn_requests`), `cancelled`; a transport error stays a process failure |
| abort | `session/cancel`, then kill on the existing grace |
| swallow stderr, bound frames | unchanged (`harness.Process`) |

Design constraints the spike must hold, each traceable to the north star:

- **Advertise no client capabilities.** No `fs`, no `terminal`. The harness
  keeps its native tools, its own approval rules apply to them, and the
  claude-agent-acp deny-rule bypass in issue #94 cannot arise. This is
  commitment 2 stated as a protocol choice.
- **Answer permissions by explicit operator policy, never by judgment.**
  `session/request_permission` is the one obligation the old drivers never
  had. `--permissions deny` (the default) or `allow`, or a policy file of
  ordered first-match rules over the call's kind, title, paths, and tool
  name — the maintainer asked for granular allow and deny, so the two-state
  policy this record first proposed was widened before it shipped. A
  denied call does not end the turn; the agent decides what to do without
  it, and the turn's status is what the agent reports. Which calls are
  asked at all remains the harness's native mode, authored under
  `harnesses/<harness>/`. Tenon still enforces nothing (commitment 2); it
  declines to be asked (tenet 5).
- **Never copy protocol text into an event or a reason.** The Codex
  driver's lesson — a turn error that echoed a live API key — applies to
  every `error.message` and every `_meta` field. `safeReason` moves up
  into the shared package.
- **Launch is pinned, not discovered.** The driver takes an explicit
  launch spec (`command`, `args`, `env` names) and the pin set records
  `agentInfo.version` per harness alongside the harness executable version
  it records now. No registry fetch on any tenon path: acquiring the
  adapter is the operator's act, like installing `claude` is today.
- **Tests stay credential-free and get better.** One fake ACP agent
  process replaces the per-vendor fakes, and it exercises the real wire
  path rather than only the seam. The `//go:build harness` live tests move
  to the adapters.

Falsifier, any of: a live claude-agent-acp or codex-acp session in an
applied workspace does not honor the applied native files; permission
handling cannot be kept to the two-state policy above without the turn
becoming unusable for a real task; or the adapters' release churn breaks
the pinned-launch path twice inside the appetite. Review date: two weeks
after the spike merges.

### Stage 2 — delete the vendor drivers (on a passing Stage 1)

Remove `internal/harness/claude` and `internal/harness/codex`; `--harness`
keeps selecting the compile target and the driver is ACP for every target.
Record it as an ADR that amends [ADR 0001](../adr/0001-use-native-harnesses.md)'s
dispatcher sentence and rewords the "Claude Agent SDK" non-goal in the
[product spec](../product-spec.md#explicit-non-goals): tenon still never
embeds an SDK or hosts a runtime; consuming an adapter that a vendor built
on one is the same relationship tenon has with the `claude` binary. Add a
known limitation: the headless leg now runs the adapter's runtime rather
than the interactive CLI, so an interactive-only behavior difference is
possible and is the adapter's to fix.

Also decide there whether `--harness` grows a third value. It should not
grow one per registry agent; a generic target that compiles to `AGENTS.md`
plus `.agents/skills/` (the convention Codex, Gemini, Copilot, Cursor, and
Amp now share for instructions and skills) is the shape worth a separate
bet, and ACP is what makes it runnable everywhere once it exists.

### Stage 3 — ask whether `tenon run` still earns its keep (later)

Once the driver is ACP, the honest tenet-1 question is whether acpx makes
the dispatcher redundant. Today's answer is no: durable accept and
deduplicate, the uncertain-after-restart discipline, the fingerprint on
every event, and the `run.completed` outcome are what `improve/` scores,
and acpx has none of them. Revisit if acpx (or the `session/list`,
`session/resume` RFDs) grows a stable equivalent.

## Not proposed, and why

- **Reorienting authoring around ACP.** There is nothing to orient toward;
  the definition-side standards are Agent Skills and Agent Plugins 1.0,
  and tenon already speaks them.
- **Passing configuration through `session/new.mcpServers` instead of
  files.** It would make the headless and interactive legs configure the
  same agent two different ways, and drift detection reads files. The
  folder stays the single source (commitment 1).
- **A tenon ACP proxy** (the `proxy-chains` RFD) that stamps fingerprints
  onto sessions for other clients. Machinery for an unvalidated future
  (tenet 4); the join is a recorded digest until someone needs more.
- **Depending on acpx or a Go ACP SDK in-process.** acpx is pre-1.0 and
  Node; the Go SDK is community-maintained. The protocol surface tenon
  needs is six methods, and the Codex driver already proves a stdlib
  JSON-RPC client is the right size. Revisit when the Go SDK is official.
- **Any acquisition path for adapters.** `tenon apply` stays offline
  (tenet 5); the registry's pinned distributions are a later input to the
  integration store, not a new fetch.

## Open questions for the maintainer

1. Answered: the policy is granular (kind, title, path, tool), so it
   matches acpx's `--policy` in reach while staying a flat first-match
   list rather than a per-tool map.
2. claude-agent-acp's registry entry lists its license as proprietary
   while its README accepts Apache-2.0 contributions. Pinning it as the
   headless Claude runtime is a dependency decision worth a line in
   `go.mod`-style justification even though it is not a Go module.
3. Should the pin set record the adapter's `agentInfo.version` in place of
   the harness executable version, or beside it? Beside, until an adapter
   reports the underlying harness version it bundles.
