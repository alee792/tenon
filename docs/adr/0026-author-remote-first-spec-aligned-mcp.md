# ADR 0026: Author remote-first, spec-aligned MCP servers

- Status: accepted for the MCP authoring decision. The plugin-acquisition
  and composition-policy sections are marked below as direction — binding
  appetite, not architecture (tenet 4) — each with its own acceptance
  trigger and falsifier
- Amends: [ADR 0016](0016-author-generic-native-mcp-connections.md) — its
  authored format is replaced: the directory, the field vocabulary, the
  target discriminators, and the credential-free restriction go, while its
  name grammar, bounds, guidance body, and rendering contract are retained;
  [ADR 0015](0015-use-the-official-github-server-as-native-unmanaged-mcp.md)
  — its curated PAT/stdio package stops being the reference GitHub journey;
  [ADR 0014](0014-use-process-isolated-integration-packages.md) — its role
  as the *authoring* path for third-party servers is deferred, and the store
  primitive is re-targeted;
  [ADR 0010](0010-map-plugin-mcp-through-native-harness-configuration.md)
  — its "exact name collisions are skipped with a warning" outcome changes
  for the author↔plugin case, where the authored server wins with a
  warning, and a masking form suppresses a plugin-declared server outright
- Proposes amending:
  [ADR 0017](0017-vendor-components-manually.md) (§ plugin acquisition)
- Reuses:
  [ADR 0013](0013-bound-authored-projects-with-aggregate-budgets.md)
- Re-points:
  [ADR 0023](0023-relay-managed-connections-through-per-connection-shims.md)
  (proposed) — its authored source of truth moves from
  `connections/<name>.md` to `mcp/<name>.md`, and its apply-time refusal of
  OAuth-authorized remote transports is scoped to *relayed stdio*
  connections, not to the unrelayed remote entries this record makes primary
- Bears on:
  [ADR 0025](0025-make-the-fingerprint-the-unit-of-revision-identity.md)
  (proposed), for the scope of the fingerprint's determinism claim
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
grammar and reservation (`managed` is tenon's). ADR 0016's bounds, recorded
in the specification's bounds table, carry over unchanged: at most 128
files of at most 8 KiB each, with a trimmed body of at most 1,024 Unicode
characters. Only the table's key moves, from "Standalone MCP connections"
to the `mcp/` directory; no number moves with it. Symlinks, directories,
nested entries, YAML aliases, tags, merge keys, non-string keys or values,
multiple documents, and unknown or duplicate fields reject the project
before workspace mutation. Exact source bytes join the project fingerprint.

An existing `connections/` directory fails validation before workspace
mutation, with a diagnostic naming the `mcp/` migration. Nothing is
silently ignored and nothing is auto-migrated: the rename is the author's
one-line act, and a project caught mid-migration says so rather than
applying a workspace missing half its servers.

Frontmatter field names and values are the
[Agent Plugins 1.0](https://agent-plugins.org/specification) `mcp.json`
server-entry vocabulary, used verbatim: `type: streamable-http` with `url`
and optional `headers`; `type: stdio` with `command` and optional `args`,
`env`, and `cwd`. The *server-declaring* forms add no field to that
vocabulary and rename none. The masking form below is tenon's own — a
distinct, closed third union arm whose fields are exactly `override` and
`enabled` — because the specification deliberately defines no composition
and so has no vocabulary to borrow; its exact format and acceptance are
issue #53's slice. Where the spec and this record could diverge on a
server-declaring field, the spec wins and this record is the one that
changes.

```md
---
type: streamable-http
url: https://api.githubcopilot.com/mcp/
---

Use the discovered GitHub tools for repository, issue, and pull-request
work.
```

`type: sse` is spec vocabulary tenon does not support: an authored file
declaring it is rejected before workspace mutation with a named diagnostic.
It does not inherit the plugin path's warn-and-skip (ADR 0010). A plugin's
MCP component is an optional part of a package the author did not write, so
dropping one server keeps the rest of the package useful; an authored
`mcp/<name>.md` is a first-class request, and silently skipping it would
leave an agent short of a capability its own source says it has.

**Authentication is discovered, never declared.** There is no `auth`
field, no OAuth configuration, no token, and no credential reference in
authored source. Per the
[MCP authorization specification (2025-11-25)](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization),
an HTTP server answering `401` advertises its authorization server, and a
client implementing that specification — here, the native harness —
performs OAuth 2.1 with PKCE and owns the resulting tokens. Which harnesses
implement it, and at which revision, is compatibility-matrix territory and
not this record's guarantee. What this record binds is the other side:
tenon renders the URL and stops. It writes no provider adapter, holds no
token, refreshes nothing, and adds no credential store; ADR 0006 remains
untouched because nothing here makes tenon secret-bearing.

A `headers` value is exactly one of two shapes: (a) a literal containing no
`$`; or (b) an optional literal prefix containing no `$`, followed by
exactly one `${VAR}` reference whose `VAR` matches `[A-Z_][A-Z0-9_]*`, with
nothing after the reference. `${PLUGIN_ROOT}` and `${PLUGIN_DATA}` are
rejected by name: they are plugin-root machinery (ADR 0010) with no meaning
in an authored file. Any other use of `$` fails validation naming the file.
The reference is expanded by nothing tenon owns and resolves, if at all, in
the harness's own process environment — the same 12-factor contract
ADR 0015 recorded for `GITHUB_PERSONAL_ACCESS_TOKEN`. Tenon never reads,
resolves, copies, or persists a *value*; the variable *name* is necessarily
written into generated configuration, since emitting the reference is the
entire point.

Literal header values are package-visible configuration and must not
contain secrets. That is the author's responsibility, stated as prose —
the same posture the product specification already takes for plugin
`mcp.json` headers. Tenon claims no heuristic for recognizing a secret and
validation attempts none; the grammar above is the whole of what is
enforced.

**Validation without contact is unchanged.** Tenon never dials the
endpoint, resolves DNS, inspects TLS, follows a redirect, discovers
authorization metadata, or proves server compatibility during authoring,
validation, apply, or staging. The URL is an absolute HTTPS URL with a
nonempty host and no user information, query, or fragment, retained exactly
— ADR 0016's rule and ADR 0014's HTTPS requirement, carried over as they
stand.

**Stdio only for bytes already in the tree.** ADR 0016's "no command or
args in authored source" rule is narrowed rather than kept: an author MAY
declare `type: stdio` with an agent-root-relative `command` (`./…`),
containment-validated after resolution the way plugin roots are, for a
server whose source or executable lives in the agent tree. Bare
PATH-resolved names and absolute paths are rejected before workspace
mutation: PATH lookup is exactly the drift this design refuses. `cwd`, when
present, carries the same containment rule as `command` — absolute paths
and paths escaping the agent root after resolution are rejected on the same
terms. `env` values follow the same value grammar as `headers`, literal
without `$` or an optional prefix plus exactly one `${VAR}` reference, and
literal `env` values are package-visible configuration that must not
contain secrets, the author's responsibility on the same terms.

The executable itself lives in the agent tree *outside* `mcp/`, which holds
only the 8 KiB declaration files: the declaration points at the bytes, it
does not carry them. Those bytes are ordinary agent source — they join the
project fingerprint and stage byte-for-byte with executable intent
preserved, the same contract skill resources already have — so the git
repository is the pin and no package store participates. What no record
yet bounds is the aggregate byte budget for tree-resident server
executables. That is an open item, assigned explicitly to the #50 slice:
#50's acceptance must include a recorded bound, and the slice is not
accepted without one.

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

**Plugin acquisition by pointer and pin (direction).** *Direction —
binding appetite, not architecture (tenet 4).* Acceptance trigger: issue
#52's implementation slice, whose own acceptance completes this section.
Falsifier: if the fetch cannot be kept a separate online step with apply
fully offline, or if pointer plus pin cannot preserve the review-and-pin
discipline, the direction is rejected with reasons and manual vendoring
stays the only journey.

`plugins/<name>.md` carrying `source` plus a full commit `rev` declares a
plugin by pointer; a directory under `plugins/` continues to mean a
vendored plugin, and the two forms colliding on a name fails closed. An
explicitly online `tenon plugin fetch` resolves pointers into the
content-addressed cache; `tenon apply` stays fully offline and fails,
naming the fetch command, when pinned content is absent. Vendoring intact
remains the fallback. The pinned digest joins the project fingerprint, so
composition identity covers exact plugin bytes exactly as vendored content
does today. This record binds the direction; the format, commands, and
acceptance are issue #52's slice.

Terminology: "pointer" here means a *plugin reference file* — the
`plugins/<name>.md` naming a source and a pinned revision. It is unrelated
to ADR 0023's relay pointer, which is a generated harness command line;
where the two records sit near each other, prefer "plugin reference file".

This direction proposes amending ADR 0017, which decided that manual
vendoring is the only acquisition journey — no acquisition commands, no
dependency lock file, no network acquisition — and required a new ADR with
evidence before any of it returns. The evidence this record offers is that
0017's actual concern, implicit and unreviewed acquisition, is preserved
rather than traded away. A pointer plus a full-commit-SHA pin keeps the
review-and-pin discipline 0017 protects: review moves from reading a
vendored directory to reading the diff of exact pinned content at fetch and
update time, in the author's own version control, and an unchanged pin can
acquire nothing new. The fetch is an explicitly online command, separate
from apply, so apply stays offline and no project load acquires anything
(tenet 5). And vendoring intact remains supported, so the reintroduced
machinery is an addition an author may decline rather than a lifecycle they
must adopt. What 0017 rejected — a 4,000-line resolution engine with a lock
file and per-load drift checks — is not what this proposes: there is no
resolution, no transitive graph, and no lock file, only a file the author
wrote and a digest the fingerprint already covers.

**Composition policy splits by relationship (direction).** *Direction —
binding appetite, not architecture (tenet 4).* Acceptance trigger: issue
#53's implementation slice, whose own acceptance completes this section.
Falsifier: if author-wins masking proves incompatible with the harnesses'
native precedence — if tenon cannot make the authored server the one the
harness actually starts, without claiming enforcement it does not have —
the direction is rejected with reasons and author↔plugin returns to
failing closed.

Plugin↔plugin server-name collisions remain fail-closed peers.
Author↔plugin becomes a hierarchy: the author's `mcp/<name>.md` wins over a
plugin-provided server of the same name, with a warning naming both
sources, and a masking form (`override` naming the contributing plugin plus
`enabled: false`) suppresses a plugin's server without replacing it. A
dangling `override` fails validation — it is a load-bearing expectation,
not a comment. `managed` stays reserved and unmaskable. Details and
acceptance are issue #53's slice.

## What this amends, and what it only scopes

ADR 0016's authored format is replaced rather than adjusted, but not
wholesale: what goes is the directory name, the field vocabulary, the
target discriminators, and the credential-free restriction on remote
targets. What ADR 0016 decided that still stands is the part worth keeping
— validation without contact, the name grammar and its `managed`
reservation, the bounds, bounded guidance bodies rendered once into
generated instructions in lexical order with the native-ownership boundary
statement, fingerprinted exact source, and offline authoring commands that
write files and direct the author to apply. The reference command rendering
becomes `tenon mcp add|status|remove`, superseding `tenon connection …`.

Fail-closed naming stands, but not wholesale either. It stands for
`managed`, for author↔author collisions, for plugin↔plugin collisions, and
for names a harness's native project surface reserves. It does not stand
for author↔plugin, which becomes the hierarchy described above:
author-wins-with-warning, with an explicit masking form. That is the one
place ADR 0010's warn-and-skip outcome changes, and it changes in the
direction of the authored file being the request tenon honors.

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

ADR 0023, still proposed, is re-pointed rather than merely tolerated. Its
authored source of truth becomes `mcp/<name>.md` — the relay resolves the
real command, arguments, and environment from the file this record defines,
in the directory this record names. Its apply-time refusal of a connection
requiring credentials tenon would have to hold (an OAuth-authorized remote
transport) is scoped to *relayed stdio* connections, where the relay must
dial the transport itself. It does not reach an unrelayed remote entry,
which the harness dials directly with credentials tenon never sees — and
unrelayed remote entries are the primary journey this record establishes.

ADR 0025's determinism claim, also proposed, is scoped by the paragraph
below rather than weakened: the fingerprint is the unit of revision
identity for *declared source*, and a remote server's behavior was never
inside that boundary.

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
authoring-command behavior — and prove the fake environment *value* appears
in no generated, staged, diagnostic, or retained artifact. Live OAuth
acceptance against a hosted server remains separately authorized and is not
required by this decision; ADR 0015's live PAT acceptance stays optional on
its own terms.

## Acceptance sketch

1. A remote entry with a `${VAR}` header renders into both harness
   renderings with the variable *name* emitted intact and the value never
   read or resolved — the claim proved is tenon's rendering, not the
   harness's resolution. Codex's configuration format documents no
   `${VAR}` expansion, so headers on a codex-target connection are a
   warned, omitted rendering, mirroring ADR 0010's existing plugin-header
   warning, until evidence says otherwise. Validation contacts nothing.
2. Header values violating the value grammar — a bare `$`, two references,
   text after the reference, a malformed variable name, `${PLUGIN_ROOT}`,
   or `${PLUGIN_DATA}` — each fail validation naming the file, before
   workspace mutation; a literal containing no `$`, and a literal prefix
   followed by exactly one reference, each pass.
3. A repo-relative stdio server renders with the exact staged path in both
   harnesses, keeps its executable bit through staging, and changes the
   fingerprint when its bytes change.
4. A bare-name `command`, an absolute `command`, a `command` escaping the
   agent root, and an absolute or escaping `cwd` each fail before
   workspace mutation, naming the file.
5. Guidance bodies render lexically and prose-once, with the native
   ownership statement, exactly as connection bodies do today.
6. `managed`, another authored server, and a harness-reserved name each
   fail closed; a plugin-provided server of the same name is masked or
   overridden per the composition policy, never renamed or silently
   skipped.
7. A `type: sse` declaration and a surviving `connections/` directory each
   fail before workspace mutation with their named diagnostics — the
   second naming the `mcp/` migration.

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
  changes that only for the stdio form it relays.
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
- [MCP authorization specification (2025-11-25)](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)
- [MCP transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)
- [Claude Code MCP configuration](https://code.claude.com/docs/en/mcp)
- [Codex MCP configuration](https://developers.openai.com/codex/mcp/)
