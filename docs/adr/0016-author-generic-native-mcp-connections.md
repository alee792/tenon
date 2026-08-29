# ADR 0016: Author generic native MCP connections

- Status: accepted
- Re-records: prototype ADR 0034 (alee792/hctl)
- Extends: [ADR 0001](0001-use-native-harnesses.md),
  [ADR 0013](0013-bound-authored-projects-with-aggregate-budgets.md), and
  [ADR 0014](0014-use-process-isolated-integration-packages.md)
- Reuses:
  [ADR 0010](0010-map-plugin-mcp-through-native-harness-configuration.md)
- Amendment proposed by:
  [ADR 0023](0023-relay-managed-connections-through-per-connection-shims.md)
  (proposed), for the reference rendering of stdio connections
- Amended by:
  [ADR 0026](0026-author-remote-first-spec-aligned-mcp.md), which replaces
  the authored format below with spec-aligned `mcp/<name>.md` files, drops
  the credential-free restriction on remote targets, and narrows the
  no-command rule rather than keeping it; the name grammar, bounds,
  guidance body, and rendering contract below are retained

## Plain-English summary

An author adds a standalone MCP server by creating one readable Markdown file
under `connections/`, directly or through a connection command. Frontmatter
either selects an exact operator-installed stdio capability or names one
credential-free HTTPS Streamable HTTP endpoint; the optional body gives the
agent usage context. Tenon validates and compiles either form into native
Claude Code or Codex project configuration; it does not add a provider
adapter or become the MCP runtime.

## Decision

**Authored format (exact).** Each connection is one immediate, real, regular
UTF-8 file `connections/<name>.md`, bounded per
[ADR 0013](0013-bound-authored-projects-with-aggregate-budgets.md) (at most
128 connections of at most 8 KiB each). Symlinks, directories, nested
entries, and other extensions reject the project before workspace mutation.
The filename supplies the connection and native server name: 1-64 characters,
a lowercase ASCII letter first, then lowercase letters, digits, underscores,
or hyphens; `managed` is reserved for tenon. Every file starts with one
closed YAML frontmatter mapping whose plain string field `type` is exactly
`mcp`, then selects exactly one target.

An installed target has exactly these fields:

```md
---
type: mcp
package: github-mcp-server
capability: github
---

Use the discovered GitHub tools for repository, issue, and pull-request work.
```

`package` and `capability` use ADR 0014's validated identifiers and select
one installed, enabled, trusted, compatible `native-mcp` version-1
capability, which already fixes transport, executable, launch data, ambient
environment names, startup, trust ownership, and supported harnesses;
authored source never repeats or overrides those values. The capability's
stable server name must equal the filename-derived connection name.

A remote target has exactly these fields:

```md
---
type: mcp
transport: streamable-http
url: https://example.com/mcp
---

Use this connection for the public reference catalog.
```

The URL is an absolute HTTPS URL with a nonempty host and no user
information, query, or fragment, retained exactly after validation. The first
slice has no headers, credential or environment references, OAuth, timeouts,
tool filters, approval settings, provider names, or frozen tool catalogs; an
endpoint that requires authentication is not a supported journey even if a
harness could separately authenticate it.

All fields are required for their selected form; unknown, duplicate, or mixed
fields fail, as do YAML aliases, tags, merge keys, non-string keys or values,
and multiple documents. The optional trimmed body holds at most 1,024 Unicode
characters of model-facing context. Exact source bytes join the project
fingerprint.

**Validation without contact.** Tenon never contacts the endpoint, resolves
DNS, inspects TLS, follows a redirect, discovers authentication, or proves
server compatibility during authoring, validation, apply, or staging.
Installed targets resolve offline through ADR 0014's store.

**Generated instructions.** When at least one connection exists, generated
instructions contain one bounded connections section: names in lexical order,
each nonempty body rendered once without frontmatter, and one statement that
the native harness owns MCP startup, trust, approval, authentication,
discovery, calls, and effects. The body is trusted project guidance for the
agent; it is not sent upstream and does not replace tool descriptions or
server-returned instructions.

**Native generation and staging.** Installed targets are emitted from
ADR 0014's verified launch descriptor; remote targets are emitted as the
harness's native HTTP server entry with the exact URL and no auth or header
fields. Connections are startup-optional with the harness's native approval;
per-harness encodings (Claude project `.mcp.json`, Codex
`.codex/config.toml`) are reference renderings. Selective staging carries the
installed capability closure only for installed targets; remote targets
contribute configuration and source only, and agents without connections
stage no server or package closure. Tenon-owned headless process opens
re-resolve installed targets through the current-state guard; remote runtime
health remains native-harness state.

**Collisions fail closed.** `managed`, another standalone connection, or a
plugin MCP server can never own the same generated server name, and an
installed capability whose server name differs from its connection filename
is a target mismatch; both fail before workspace mutation, never renamed,
shadowed, or skipped. Names a harness's native project surface reserves are
rejected for that harness. Tenon cannot preflight harness-owned
higher-precedence configuration; native precedence and diagnostics govern
there.

**Authoring assistance.** Authors need not hand-edit native configuration.
The reference rendering is:

```text
tenon connection add AGENT NAME --package PACKAGE --capability CAPABILITY [--context TEXT]
tenon connection add AGENT NAME --url HTTPS_URL [--context TEXT]
tenon connection status AGENT [NAME]
tenon connection remove AGENT NAME
```

The binding responsibilities: every command takes the exact positional agent
root, proven per the product specification's Instructions section, and never
searches ancestors or selects a workspace or harness. Add validates
everything it can offline — including exact installed resolution and the
filename/server-name match — then creates the file atomically, never
overwriting; it neither applies a workspace nor changes package state. Status
reports the declared target and its offline resolution health without
executing a package or contacting an endpoint, and any malformed or
unresolved connection yields a nonzero result with bounded authored-path
diagnostics. Remove deletes exactly the named real connection file and
nothing else, without requiring the target to be healthy. After add or
remove, the author is directed to run the ordinary explicit apply for each
intended workspace. There is no update command: the file is ordinary
versioned source, edited directly. Plugin-bundled MCP remains solely in the
publisher's `mcp.json`; connection commands never synthesize or modify plugin
files.

**Diagnostics.** Malformed schema, bad name, package-resolution,
harness-target, reserved-name, collision, and staging errors name the
connection's authored path and fail before workspace mutation. Diagnostics
never contain body text, credential or environment values, remote response
bodies, or resolved redirect targets. A file without the required frontmatter
fails naming the required `type: mcp` declaration and one supported target.

## Evidence contract

Acceptance uses two credential-free, provider-neutral fixtures — an installed
fake stdio capability and a remote HTTPS declaration — proving for both
harnesses: exact parsing and bounds, union rejection, fingerprinting, lexical
instruction rendering with prose-once behavior, collision and target-mismatch
failure, offline resolution, native mapping, selective staging and remote
closure omission, current-state guards, and atomic authoring-command
behavior. Live PAT acceptance remains separately authorized and is not
required by this decision.

## Consequences

- Standards-compatible standalone MCP servers do not require one tenon
  adapter or provider switch each.
- Non-developers can author supported connections without editing native
  harness configuration.
- Installed process metadata remains operator/package-owned, while portable
  source selects only an exact package capability.
- Public remote endpoints are useful without opening a credential or OAuth
  design; header-bearing, authenticated, and tenon-managed HTTP remain
  deferred.
- Native harnesses continue to own runtime MCP behavior and may expose tools
  with effects beyond tenon's managed workspace boundary.

## Sources

- [MCP Streamable HTTP transport](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)
- [Claude Code MCP configuration](https://code.claude.com/docs/en/mcp)
- [Codex MCP configuration](https://developers.openai.com/codex/mcp/)
