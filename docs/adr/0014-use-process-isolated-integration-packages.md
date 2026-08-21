# ADR 0014: Use metadata-first process-isolated integration packages

- Status: accepted
- Re-records: prototype ADR 0030 (alee792/hctl); the prototype's
  `channel-adapter` capability belongs to the channel product and is not
  re-recorded here
- Extends: [ADR 0001](0001-use-native-harnesses.md) and
  [ADR 0010](0010-map-plugin-mcp-through-native-harness-configuration.md)
- Specialized by:
  [ADR 0015](0015-use-the-official-github-server-as-native-unmanaged-mcp.md)
- Extended by:
  [ADR 0016](0016-author-generic-native-mcp-connections.md)

## Plain-English summary

Third-party integrations that are not portable authored source are exact,
operator-installed packages containing external executables. Tenon validates
a small manifest without executing the package, then gives each recognized
capability to its own narrow consumer. The first capability is a native stdio
MCP server: its process belongs to Claude Code or Codex after tenon writes
native configuration, and it never becomes a tenon-managed tool.

## Decision

**One metadata envelope, exactly pinned.** A package is described by one
bounded schema-versioned manifest carrying identity (exact semantic version,
stable id, name, description, license), source and revision provenance, a
half-open tenon compatibility range, and one or more platform artifacts. Each
artifact is pinned by exact platform, format, size, and SHA-256, along with
the expected executable's path, size, and SHA-256 after preparation; artifact
sources are a closed union of a package-relative payload or one exact HTTPS
URL without credentials, query, fragment, or redirects. The SHA-256 of the
exact manifest bytes is the package's immutable identity: any change,
including reformatting, is a different identity. Capability selection always
derives from those retained exact bytes, never from a caller-mutable copy.

**Validation never executes.** Metadata validation reads no artifact: it
resolves no symlink, fetches no URL, inspects no archive, loads no library,
and starts no process. Installation and content verification are a separate
phase, and an unknown capability type or version rejects the manifest with a
typed error rather than invoking package code to discover behavior.

**Closed capabilities, narrow consumers.** Capabilities are closed, tagged,
integer-versioned schemas; schema 1 recognizes `native-mcp` version 1. A
`native-mcp` declaration binds one stable native server name (collision
policy: reject), the exact artifact closure and executable identity, bounded
literal launch data (arguments, package-relative working directory,
non-secret environment defaults), required ambient environment-variable
*names* with prose descriptions — never values, references, or
placeholder-bearing defaults — and per-harness targets with declared startup
policy and `native-project` trust ownership. Later capability schemas share
the envelope without leaking fields into one another or growing a generic
runtime interface.

**Operator-only trust.** Installation state is operator-owned, separate from
the manifest, and bound back to it: package identity, exact manifest hash,
explicit `operator` trust, enablement, verified artifact and executable
identities, and declared capability ids. Portable agent source may request a
known capability but can never choose a source or version, install or enable
a package, grant trust, or carry a credential; apply gains no network path. A
different manifest under an installed id requires an explicit update with a
fresh operator trust decision — ordinary install never drifts.

**Immutable, offline-verifiable storage.** One owner-only per-OS-user store,
shared across agents and workspaces, holds exact manifests, raw artifacts,
and prepared executable trees content-addressed so that distinct
transformations never alias and a valid entry is never replaced. Install
validates compatibility, platform, containment, and every pinned identity
before atomically recording installation state, and interrupted work leaves
only inert bytes no valid state selects. Inspection reports non-secret
metadata without opening a process; verification rehashes everything offline
and re-runs before every use; disable blocks future resolution, enable
verifies first, and removal retires the record while retaining shared
immutable content.

**Selection evidence, not runtime behavior.** Offline lookup verifies
enablement, compatibility, and identity, then hands the consumer exact
prepared paths and immutable selection evidence. The native-MCP consumer may
derive a credential-free launch descriptor from it, but the store and
installer never infer MCP, credential, policy, or process behavior, never
resolve ambient values, and never start what they install. Once configured,
the native harness owns process lifecycle, authentication, approvals,
discovery, calls, effects, and errors; tenon does not proxy, supervise,
filter, retry, observe, or audit that traffic, and resolved ambient values
must never enter package state, generated files, staged filesystems, or
retained evidence — while remaining visible, as documented, to the harness
and whatever inherits its environment.

**Dependency direction.** Consumers depend inward on these validated data
contracts. Vendor packages run as separate executables and depend inward on
the manifest schema or a later capability protocol; they cannot import
themselves into tenon, register an in-process lifecycle, or make core switch
on a vendor name.

## Context

Vendored Agent Plugins under authored `plugins/` are portable project source;
their installation and update model is deliberately outside
[ADR 0009](0009-import-vendored-agent-plugin-skills.md) and ADR 0010.
Machine-installed integration executables need different trust, provenance,
reuse, and staging ownership. The official GitHub MCP server illustrates the
dependency problem: importing a vendor implementation couples releases and
grows the trusted in-process graph, while a universal plugin runtime would
trade that coupling for an unbounded dynamic authority surface. A metadata
envelope plus narrow process contracts lets operators add exact integrations
without rebuilding tenon.

## Consequences

- A credentialless native-MCP fixture and the official `github-mcp-server`
  metadata install, validate, and select through the same vendor-neutral
  envelope without sharing a capability runtime.
- Root tenon dependencies remain independent of package SDKs.
- Registry search, package scripts, automatic updates, signature claims,
  remote HTTP MCP capabilities, credentials and OAuth, arbitrary hooks, and
  in-process plugins remain deferred; broad cache garbage collection is
  deferred with them.
