# ADR 0026: Author remote-first, spec-aligned MCP servers

- Status: accepted
- Amends: [ADR 0016](0016-author-generic-native-mcp-connections.md) — its
  authored format is replaced in full;
  [ADR 0015](0015-use-the-official-github-server-as-native-unmanaged-mcp.md)
  — its curated PAT/stdio package stops being the reference GitHub journey;
  [ADR 0014](0014-use-process-isolated-integration-packages.md) — its role
  as the *authoring* path for third-party servers is deferred, and the store
  primitive is re-targeted
- Reuses:
  [ADR 0010](0010-map-plugin-mcp-through-native-harness-configuration.md),
  [ADR 0013](0013-bound-authored-projects-with-aggregate-budgets.md)
- Compatible with: [ADR 0023](0023-relay-managed-connections-through-per-connection-shims.md),
  which remains proposed and, if accepted, relays the stdio form recorded
  here
- Context: issues #47 (direction) and #48 (this record); slices #49, #50,
  #51, #52, #53

## Plain-English summary

An author declares an MCP server by creating one readable Markdown file
under `mcp/`. The frontmatter is the Agent Plugins 1.0 `mcp.json` server
entry — the same field names every other client already reads — and the
body is bounded model-facing guidance. Remote servers no longer have to be
credential-free: authentication is the harness's job, discovered at connect
time, and tenon declares none of it. Stdio stays available only for a
server whose bytes already live in the agent tree. Acquiring a third-party
binary in order to *author* a connection is deferred, not rejected; the
hosted remote server with harness-discovered OAuth becomes the reference
journey, including for GitHub.

## Decision

**One directory, one vocabulary (exact).** Authored MCP servers live under
`mcp/`, one immediate, real, regular UTF-8 file `mcp/<name>.md` per
declared server, replacing `connections/` and ADR 0016's closed two-field
schema. The filename supplies the server name under ADR 0016's unchanged
grammar and reservation (`managed` is tenon's). ADR 0013's bounds carry
over unchanged: at most 128 files of at most 8 KiB each, with a trimmed
body of at most 1,024 Unicode characters. Symlinks, directories, nested
entries, YAML aliases, tags, merge keys, non-string keys or values,
multiple documents, and unknown or duplicate fields reject the project
before workspace mutation. Exact source bytes join the project
fingerprint.

Frontmatter field names and values are the
[Agent Plugins 1.0](https://agent-plugins.org/specification) `mcp.json`
server-entry vocabulary, used verbatim: `type: streamable-http` with `url`
and optional `headers`; `type: stdio` with `command` and optional `args`,
`env`, and `cwd`. Tenon adds no field of its own and renames none. Where
the spec and this record could diverge, the spec wins and this record is
the one that changes.

```md
---
type: streamable-http
url: https://api.githubcopilot.com/mcp/
---

Use the discovered GitHub tools for repository, issue, and pull-request
work.
```

**Authentication is discovered, never declared.** There is no `auth`
field, no OAuth configuration, no token, and no credential reference in
authored source. Per the
[MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization),
an HTTP server answering `401` advertises its authorization server and the
MCP *client* — the native harness — performs OAuth 2.1 with PKCE and owns
the resulting tokens. Tenon renders the URL and stops. It writes no
provider adapter, holds no token, refreshes nothing, and adds no
credential store; ADR 0006 remains untouched because nothing here makes
tenon secret-bearing.

`headers` values may reference environment variable *names* only, as
`${VAR}`, expanded by nothing tenon owns and resolved in the harness's own
process environment — the same 12-factor contract ADR 0015 recorded for
`GITHUB_PERSONAL_ACCESS_TOKEN`. A literal secret-shaped value fails
validation naming the file, under the heuristics plugin `mcp.json` headers
already use. Tenon never reads, resolves, copies, or persists a value.

**Validation without contact is unchanged.** Tenon never dials the
endpoint, resolves DNS, inspects TLS, follows a redirect, discovers
authorization metadata, or proves server compatibility during authoring,
validation, apply, or staging. The URL is an absolute HTTPS URL with a
nonempty host and no user information or fragment, retained exactly.

**Stdio only for bytes already in the tree.** ADR 0016's "no command or
args in authored source" rule is narrowed rather than kept: an author MAY
declare `type: stdio` with an agent-root-relative `command` (`./…`),
containment-validated after resolution the way plugin roots are, for a
server whose source or executable lives in the agent tree. Bare
PATH-resolved names and absolute paths are rejected before workspace
mutation: PATH lookup is exactly the drift this design refuses. The
executable's bytes are ordinary agent source — fingerprinted and staged
byte-for-byte with executable intent preserved, the same contract skill
resources already have — so the git repository is the pin and no package
store participates.

**Acquired third-party stdio is deferred, not rejected.** ADR 0014's store
as the *authoring* path for a third-party server, and ADR 0015's curated
PAT/stdio GitHub package as the reference GitHub journey, both step out of
the primary journey. Neither is withdrawn: the code paths, the manifest
contract, and the operator journey remain valid and remain the answer for
air-gapped or org-policy operators, at whatever priority evidence later
earns. The reference GitHub journey becomes the hosted remote server with
harness-discovered OAuth — a four-line file, one apply, one browser
consent — and the documentation re-scoping is issue #51's slice, not this
record's.

ADR 0014's store primitive survives on its own merits: owner-only,
immutable, content-addressed, offline-verifiable. It is re-targeted at
plugin acquisition, where the authoring pressure actually is.

**Plugin acquisition by pointer and pin (direction).** `plugins/<name>.md`
carrying `source` plus a full commit `rev` declares a plugin by pointer; a
directory under `plugins/` continues to mean a vendored plugin, and the two
forms colliding on a name fails closed. An explicitly online
`tenon plugin fetch` resolves pointers into the content-addressed cache;
`tenon apply` stays fully offline and fails, naming the fetch command, when
pinned content is absent. Vendoring intact remains the fallback. The pinned
digest joins the project fingerprint, so composition identity covers exact
plugin bytes exactly as vendored content does today. This record binds the
direction; the format, commands, and acceptance are issue #52's slice.

**Composition policy splits by relationship (direction).** Plugin↔plugin
server-name collisions remain fail-closed peers. Author↔plugin becomes a
hierarchy: the author's `mcp/<name>.md` wins over a plugin-provided server
of the same name, with a warning naming both sources, and a masking form
(`override` naming the contributing plugin plus `enabled: false`) suppresses
a plugin's server without replacing it. A dangling `override` fails
validation — it is a load-bearing expectation, not a comment. `managed`
stays reserved and unmaskable. Details and acceptance are issue #53's
slice.

## What this amends, and what it only scopes

ADR 0016's authored format is replaced, not adjusted: the directory name,
the field vocabulary, the installed-target form, and the credential-free
restriction on remote targets all go. What ADR 0016 decided that still
stands is the part worth keeping — validation without contact, bounded
guidance bodies rendered once into generated instructions in lexical order
with the native-ownership boundary statement, fingerprinted exact source,
fail-closed naming, and offline authoring commands that write files and
direct the author to apply. The reference command rendering becomes
`tenon mcp add|status|remove`, superseding `tenon connection …`.

ADR 0015 is amended in emphasis, not in its security analysis. Every claim
it makes about the PAT path remains exactly true of that path, including
the boundary statement that the harness and anything inheriting its
environment may read or transmit the credential. What changes is which
journey the documentation leads with. The remote journey does not remove
that exposure so much as relocate it: an OAuth token the harness obtains
lives in harness-owned storage tenon neither writes nor inspects, and the
authority it carries is whatever the operator consented to in the browser.
Tenon claims no isolation of it.

ADR 0014 keeps every property it decided. Its consequence that "remote HTTP
MCP capabilities, credentials and OAuth" are deferred is not satisfied by
this record — those remain outside the *package* envelope. What is scoped
is the store's consumer: no authoring journey requires it after this
record, and the plugin cache is its named next one.

**What is knowingly given up.** A remote server's behavior is not pinned.
A pinned executable freezes a tool catalog; a hosted endpoint does not, so
a server's tools, schemas, and results can change under an unchanged
fingerprint. The fingerprint contract covers *declared source* — the URL,
the headers, the guidance, the staged bytes of anything local — and has
never covered remote behavior, and this record makes that gap load-bearing
rather than incidental. Per north star #3 the specification must say so
plainly rather than let the fingerprint imply reproducibility it does not
deliver; probing remote catalogs for drift is a possible future
`tenon mcp status --probe` and is deliberately out of scope here.

## Context

Two ecosystem shifts postdate ADRs 0014–0016. The Agent Plugins 1.0
specification, governed by a vendor-neutral technical steering committee
(Amazon, Cursor, Microsoft, OpenAI, Vercel, with Google joining),
standardizes the plugin package format including `mcp.json`. Tenon already
consumes that format for plugin-bundled servers (ADR 0010); maintaining a
second, tenon-owned MCP dialect for authored servers is now a liability
rather than a simplification — an author who learns one shape has to learn
the other, and every field tenon renames is a translation only tenon
maintains. And the MCP authorization model makes HTTP authentication
discovered by the client at connect time, which means the credential-free
restriction ADR 0016 imposed was buying nothing: the harness could always
authenticate a server tenon refused to let an author declare.

The cost side is equally concrete. The acquired-stdio machinery — platform
matrices, archive and executable hash pinning, operator-authored manifests,
an install-and-trust lifecycle — was the entire cost of the old story, and
it is why exactly one curated package ever shipped. Remote-first trades
behavioral pinning tenon could not realistically maintain across
third-party servers for an authoring journey a non-developer can finish:
four lines of standard vocabulary plus prose, then one apply and one
consent click (tenets 1 and 3, and the five-minute measure).

The 1.0 specification deliberately defines no dependency resolution, no
inter-package references, and no pinning. That silence is the layer tenon
owns and this record claims: collision and masking policy, aggregate
budgets, fingerprint-addressed composition identity, and one status view of
the composed surface. Adopting the package format costs tenon nothing it
was differentiating on.

## Evidence contract

Acceptance stays credential-free and provider-neutral, with fixture
servers and no live model, network, or GitHub call. Two fixtures carry it:
a remote HTTPS declaration whose header references a conspicuous fake
environment name, and a repo-relative stdio server built from bytes in the
fixture tree. Both prove, for both harnesses, exact parsing and bounds,
unknown-field and union rejection, fingerprinting, lexical prose-once
instruction rendering, reserved-name and collision behavior byte-compatible
with today's connections tests, native mapping, staging, and atomic
authoring-command behavior — and prove the fake environment value appears
in no generated, staged, diagnostic, or retained artifact. Live OAuth
acceptance against a hosted server remains separately authorized and is not
required by this decision; ADR 0015's live PAT acceptance stays optional on
its own terms.

## Acceptance sketch

1. A remote entry with a `${VAR}` header round-trips into both harness
   renderings with the name unresolved and unpersisted, and validation
   contacts nothing.
2. A header carrying a literal secret-shaped value fails validation naming
   the file, before workspace mutation.
3. A repo-relative stdio server renders with the exact staged path in both
   harnesses, keeps its executable bit through staging, and changes the
   fingerprint when its bytes change.
4. A bare-name `command`, an absolute `command`, and a path escaping the
   agent root each fail before workspace mutation, naming the file.
5. Guidance bodies render lexically and prose-once, with the native
   ownership statement, exactly as connection bodies do today.
6. `managed`, another authored server, and a harness-reserved name each
   fail closed; a plugin-provided server of the same name is masked or
   overridden per the composition policy, never renamed or silently
   skipped.

## Consequences

- Authors write one industry-standard shape. A server entry copied from a
  vendor's README works in `mcp/<name>.md` unchanged, and tenon's
  contribution is the prose and the composition around it, not a dialect.
- The five-minute measure survives contact with third-party servers for the
  first time: no download, no manifest, no trust ceremony before the first
  useful call.
- Authenticated remote servers become authorable, which widens the effect
  surface an agent can reach. Tenon does not mediate, approve, filter, or
  audit those calls; native harness trust, the operator's consent grant,
  and the author's judgment are the boundary, and ADR 0023 — if accepted —
  changes that only for the stdio form.
- Reproducibility is asymmetric and now documented as such: local bytes are
  pinned by the repository, remote behavior is not pinned at all.
- ADR 0014's machinery keeps running with no authoring journey consuming
  it, which is honest only while the plugin cache (#52) is genuinely next;
  if that slice is rejected, the store's continued existence should be
  re-litigated rather than assumed.
- The product specification's connections section, the bounds table, and
  the GitHub operator journey now describe a superseded shape. Re-scoping
  them is issue #51's slice and is deliberately not done here.

## Sources

- [Agent Plugins specification](https://agent-plugins.org/specification)
- [MCP authorization](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization)
- [MCP transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)
- [Claude Code MCP configuration](https://code.claude.com/docs/en/mcp)
- [Codex MCP configuration](https://developers.openai.com/codex/mcp/)
