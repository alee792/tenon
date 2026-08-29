# ADR 0010: Map plugin MCP through native harness configuration

- Status: accepted
- Re-records: prototype ADR 0020 (alee792/hctl)
- Extended by:
  [ADR 0013](0013-bound-authored-projects-with-aggregate-budgets.md)
- Reused by:
  [ADR 0015](0015-use-the-official-github-server-as-native-unmanaged-mcp.md)
- Amended by: [ADR 0026](0026-author-remote-first-spec-aligned-mcp.md),
  which changes the "exact name collisions are skipped with a warning"
  outcome for the author↔plugin case — an authored server of the same name
  wins with a warning, and an authored masking form suppresses a
  plugin-declared server; plugin↔plugin collisions are unchanged

## Plain-English summary

Valid MCP servers declared by a vendored Agent Plugins v1 dependency are added
to the selected harness's native project configuration. Claude Code and Codex
remain responsible for starting, approving, authenticating, and operating
those servers. Tenon validates and translates the package declaration; it does
not proxy or manage plugin MCP traffic.

## Decision

An accepted plugin may contain an optional bounded `mcp.json` targeting the
exact Agent Plugins v1.0.0 MCP schema identifier. Tenon implements the
supported schema locally without fetching it. A malformed top-level document
disables only that plugin's MCP component. Invalid individual servers,
unsupported SSE servers, duplicate names, and the reserved `managed` name warn
and are skipped without suppressing valid sibling servers or skills.

Tenon supports `stdio` and `streamable-http`. Stdio commands are either a bare
executable name or a plugin-relative `./` path. Plugin-relative commands and
plugin-root working directories must be bounded real paths without symlinks.
`${PLUGIN_ROOT}` and `${PLUGIN_DATA}` are expanded once in arguments,
environment values, and working directories only. Tenon supplies both
variables to every stdio server and creates one private, persistent
workspace-local data directory per agent-and-plugin identity before writing
native configuration. Existing data-root permissions are normalized to
owner-only and verified on later use.

Remote URLs must be absolute HTTP(S) URLs without credentials or fragments.
Non-loopback endpoints require HTTPS. Headers must have valid HTTP names and
values, may not collide case-insensitively, are copied literally, and
therefore must not contain secrets.

Plugin directories and server names are considered in lexical order. The
first exact server name wins and names are never rewritten. Accepted server
values, plugin-relative command content, and executable intent join the
source fingerprint. Accepted servers are emitted into the selected harness's
native project MCP configuration; plugin servers are optional to start and
keep the harness's native per-server approval, while tenon's own `managed`
server retains its required-and-approved policy. The declared portable
working directory is preserved exactly for every stdio server even where a
harness's project format lacks a working-directory field — the reference
rendering wraps such commands in a system exec adapter that sets the
directory before replacing itself with the declared command.

A harness that performs its own environment-expansion pass over project MCP
values must never receive text that could substitute an ambient secret or
change a value the portable specification treats as literal: when
placeholder-like text would survive portable expansion, tenon skips that
server for that harness with a warning. A harness without such expansion
receives the text unchanged.

## Consequences

- Tenon does not start, proxy, supervise, authorize, observe, retry, or audit
  plugin MCP server calls.
- Plugin data survives server or plugin removal; tenon never treats it as an
  owned generated file.
- Package headers are visible source configuration, not a credential channel.
- SSE, OAuth, portable credentials, client extensions, installation, updates,
  downloads, and marketplaces remain unsupported.
- Native clients may still reject a valid portable declaration if their own
  capabilities or policies differ.

## Sources

- [Agent Plugins specification v1.0.0](https://agent-plugins.org/specification)
- [Canonical MCP schema](https://agent-plugins.org/schemas/1.0.0/mcp.schema.json)
- [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp)
- [Codex MCP documentation](https://developers.openai.com/codex/mcp/)
- [Product specification](../product-spec.md)
