# ADR 0015: Use the official GitHub server as native unmanaged MCP

- Status: accepted
- Re-records: prototype ADR 0031 (alee792/hctl)
- Specializes:
  [ADR 0014](0014-use-process-isolated-integration-packages.md)
- Reuses:
  [ADR 0010](0010-map-plugin-mcp-through-native-harness-configuration.md)
- Authored selection defined by:
  [ADR 0016](0016-author-generic-native-mcp-connections.md)
- Amended by: [ADR 0026](0026-author-remote-first-spec-aligned-mcp.md),
  which makes the hosted remote GitHub server with harness-discovered OAuth
  the reference journey and defers this curated PAT/stdio path; every
  credential and effect boundary recorded here still holds of that path

## Plain-English summary

`connections/github.md` requests an installed copy of GitHub's official MCP
server. Claude Code or Codex starts that external server directly and gives
it the ambient `GITHUB_PERSONAL_ACCESS_TOKEN`. Tenon verifies and selects the
package and writes native configuration, but it does not resolve, consume,
store, or protect the token and does not govern the server's GitHub calls.

**The harness, model-accessible shell or execution tools, plugins, and other
processes inheriting the launch environment may read or transmit the PAT.**
Repository scope, permissions, expiration, runtime isolation, native-harness
trust, and operator judgment are the security boundary for this delivery.

## Decision

**Authored selection.** Per ADR 0016, `connections/github.md` explicitly
selects capability id `github` from stable integration package id
`github-mcp-server` in closed frontmatter, with an optional bounded Markdown
body rendered as model-facing connection guidance. The file contains no
credential value or reference, installed version, executable path, repository
grant, tool allowlist, or approval decision, and its body does not rewrite or
freeze the official server's tool catalog or schemas. Installation,
enablement, exact version selection, and operator trust are machine state
owned by ADR 0014's package journey.

**Curated distribution.** The initial curated distribution pins an exact
official release (initially `v1.8.0`, `darwin-arm64` and `linux-amd64`) by
archive and executable identity, with a separate declarative source lock
pinning the official release URLs and archive layout. A one-command
materializer performs the download outside tenon and supplies the result to
the generic explicit-trust installer, preserving the installer's no-redirect
fetch contract and adding no GitHub downloader, cache, or vendor switch to
tenon. Runtime verification is offline.

**Division of ownership.** The official `github/github-mcp-server` executable
supplies GitHub's tool catalog, schemas, protocol behavior, authentication,
requests, results, and failures; this delivery selects only its PAT path, in
which the server reads `GITHUB_PERSONAL_ACCESS_TOKEN` from its own process
environment. Tenon owns package verification and immutable selection
evidence, selective runtime and staging closure, offline resolution during
apply, deterministic collision checks, and native configuration generation.
The harness owns process startup and lifecycle, project trust, tool approval
and discovery, calls and effects, and runtime diagnostics. Tenon does not
route this server through its managed MCP server and does not proxy,
supervise, filter, authorize, confirm, retry, observe, normalize, or audit
its calls.

**Native mapping responsibilities.** Both harnesses receive the exact native
server name `github` (collision policy: reject), the exact verified installed
executable launched in the exact prepared package root, and startup-optional
policy, so an unavailable GitHub server never takes down the rest of the
session or tenon's managed server. Generated configuration carries the
required environment-variable *name* only — never a value — and each
harness's native approval journey remains in force. Per-harness encodings
(Claude's project `.mcp.json` entry with its exec-adapter working directory,
Codex's `.codex/config.toml` server table with prompt approval and
environment-name forwarding) are reference renderings of those
responsibilities.

**Runtime injection, not persistence.** A shell, service manager, container
runtime, or external secret manager injects the PAT when it launches the
harness; tenon never copies it from the apply environment or persists it.
Long-lived tenon-owned processes pass their own unchanged environment to
harness children, so rotating an externally injected credential requires
restarting the owning process. Unattended use requires the operator to
deliberately establish the harness's native project, server, and tool trust
first — `apply` grants no trust, and `connections/github.md` is not an
approval. Plain native launches use the exact installed path embedded at the
last apply, so package updates require reapply (and image rebuilds) before
restart, and safe removal removes the connection, reapplies and rebuilds
consumers, then removes package state.

**Failure ownership.** Apply fails with bounded, credential-free diagnostics
when the requested package is absent, disabled, untrusted, incompatible,
wrong-platform, missing its verified executable, or colliding in the
generated closure; `managed` remains reserved and tenon never renames,
shadows, or silently skips the requested server. Connection resolution during
apply is offline and never requires the PAT. Missing, invalid, expired, or
insufficient credentials are official-server and GitHub failures surfaced
through the harness; tenon does not intercept, reclassify, or copy upstream
bodies. Harness-owned higher-precedence configuration remains a native
diagnostic; tenon does not claim to preflight it.

### Credential and effect boundary

Tenon may retain the required variable *name* as non-secret diagnostic
metadata but never reads or writes its resolved value into agent source,
package state, generated files, apply records, caches, images, staged
filesystems, logs, diagnostics, or retained evidence. This is a 12-factor
environment contract, not a credential broker or secret-isolation guarantee.
The PAT may authorize everything the official server exposes and GitHub
accepts; tenon enforces no repository or tool allowlists, and a read-only
workspace constrains local workspace effects only, never GitHub effects.
Operators should use fine-grained, short-lived PATs and treat untrusted model
input as having that authority. Native Git and `gh` use separately
operator-owned authentication; the MCP PAT authenticates neither, and the
official MCP surface does not promise exact local branch publication.

## Evidence contract

Acceptance uses a credentialless native-MCP fixture through ADR 0014's
vendor-neutral selection path — a deterministic fake stdio executable and a
conspicuous fake environment marker — proving generation, working directory,
startup policy, trust behavior, collision rejection, and calls for both
harnesses without a GitHub credential, network request, or GitHub-specific
code path, and proving the fake value appears in no generated, staged,
diagnostic, or retained artifact. Live GitHub acceptance is optional and
requires explicit authorization and a temporary least-privilege credential.
The [operator journey](../github-native-mcp.md) records the literal local and
service/container paths, package lifecycle, native trust, troubleshooting,
and runtime-only secret injection.

## Context

[ADR 0006](0006-use-a-local-secretless-operation-broker.md) remains accepted
and unamended: it applies before tenon ships a secret-bearing *managed* tool
or connection. This delivery is deliberately native and unmanaged — the
credential enters the harness environment and the external server consumes it
outside tenon's managed boundary — so it neither satisfies nor weakens the
broker decision. A tenon-implemented GitHub integration could not match the
official server's tool surface without duplicating vendor code; ADR 0014's
operator-installed external executable is the smaller long-term dependency
boundary.

## Consequences

- Agents without an installed `github-mcp-server`/`github` connection
  generate and stage no GitHub package entry or runtime artifact.
- This specialization adds no GitHub-specific installer, cache, or downloader
  to tenon core and no credential store, broker, proxy, Git client, or GitHub
  API client anywhere; downloaded official binaries are never vendored in
  this repository.
- Native tool names and catalogs are discovered from the official server and
  are not frozen into tenon's portable contract.

## Sources

- [Official GitHub MCP server](https://github.com/github/github-mcp-server)
- [Claude Code MCP configuration](https://code.claude.com/docs/en/mcp)
- [Codex MCP configuration](https://developers.openai.com/codex/mcp/)
