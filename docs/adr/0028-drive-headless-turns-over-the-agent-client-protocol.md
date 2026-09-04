# ADR 0028: Drive headless turns over the Agent Client Protocol

- Status: proposed — the driver ships behind an explicit `--driver acp`
  flag; the native drivers remain the default until the falsifier below
  has been checked against live adapters
- Amends: [ADR 0001](0001-use-native-harnesses.md) (its optional turn
  dispatcher gains a second, protocol-generic driver); the product
  specification's "Claude Agent SDK" non-goal is reworded, not removed
- Research record: [docs/workbench/acp-alignment.md](../workbench/acp-alignment.md)

## Decision

The turn dispatcher can drive any Agent Client Protocol (ACP) agent process
through one client behind the existing `harness.Driver` seam, selected with
`--driver acp` on `tenon run`, `tenon schedule trigger`, and `tenon schedule
run`. `--harness` keeps selecting the compile target and stamping the wire
stream; the agent launched is the harness's reference adapter
(`claude-agent-acp` or `codex-acp`, resolved on `PATH` like the native
executables) unless `--acp-command` names another, which may be any
registry agent.

Tenon is a deliberately minimal client:

- It advertises no file-system or terminal capability and passes no MCP
  servers. The agent keeps its own native tools, reads the applied native
  files from the workspace exactly as an interactive session would, and its
  own permission rules apply to those tools.
- It answers `session/request_permission` only from an operator-supplied
  policy — `--permissions allow`, `--permissions deny` (the default), or a
  policy file of ordered first-match rules over the call's kind, title,
  reported file paths, and harness tool name. The policy is supplied to the
  process that opens the session, never read from agent source, and it
  selects the agent's *once* option so the agent remembers nothing.
- It copies no protocol text — no title, raw input, error message, or
  `_meta` value — into an event, a reason, an error, or a log. A prompt the
  agent rejects is a failed turn with a fixed-vocabulary reason; stop
  reasons are the protocol's closed vocabulary and are carried verbatim.
- A resume against an agent that does not advertise `loadSession` is
  refused rather than silently started fresh.

## Context

The native drivers speak two vendor wire protocols — Claude Code's
`stream-json` and Codex's `app-server` JSON-RPC — that each vendor changes
at will, and adding a third harness means a third. ACP is the same job
standardized: one stdio JSON-RPC session protocol that Claude Code, Codex,
Gemini CLI, Copilot CLI, Cursor, and some forty registry agents already
speak, and that Zed, JetBrains, Neovim, Emacs, acpx, and OpenClaw drive.
Its surface for tenon's purpose is six methods, the same size as the Codex
driver; a stdlib client keeps the dependency rule.

The one obligation the native drivers never had is answering permission
requests. Interactive harnesses ask a person; a headless client must answer
by rule. The north star keeps approval *enforcement* with the harness, and
this decision holds that line: the harness decides which calls need asking
(claude-agent-acp reads `permissions.defaultMode` from the applied
`.claude/settings.json`; codex-acp reads its approval mode), and tenon only
answers what is asked, from a policy the operator wrote. Declining to be
asked is tenet 5 — explicit at a boundary — not enforcement.

## Consequences

- Every registry agent becomes a runtime harness at zero driver cost, and
  the headless leg of an applied workspace is the same wire path any ACP
  client uses, so interop with acpx, OpenClaw, Zed, and JetBrains is proven
  by the same tests.
- The headless Claude leg runs the adapter's runtime (the Claude Agent
  SDK) rather than the `claude` CLI. Tenon still embeds no SDK and hosts no
  runtime; the relationship is the one it has with the `claude` binary.
  An interactive-only behavior difference is possible and is the adapter's
  to fix.
- Tests get better: one fake ACP agent exercises the real wire path for
  every harness, credential-free, in CI.
- Not yet done, deliberately: pinning the adapter's `agentInfo.version` in
  the pin set, and deleting the native drivers. Both wait on the falsifier.

## Falsifier and review

Falsified if a live claude-agent-acp or codex-acp session in an applied
workspace does not honor the applied native files (instructions, skills,
`.mcp.json` or `.codex/config.toml`, and the injected model pin), or if the
adapters' release churn breaks the pinned-launch path twice inside a
month. Review after two weeks of use. On a pass, a follow-up ADR flips the
default to `acp` and deletes `internal/harness/claude` and
`internal/harness/codex`.
