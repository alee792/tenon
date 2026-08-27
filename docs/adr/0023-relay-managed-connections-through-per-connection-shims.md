# ADR 0023: Relay managed connections through per-connection shims

- Status: proposed
- Amends: the reference rendering of
  [ADR 0016](0016-author-generic-native-mcp-connections.md) for stdio
  connections; the product specification's managed-boundary claims as
  scoped below
- Context: the design exploration recorded in
  [docs/workbench/managed-middleware.md](../workbench/managed-middleware.md)
  (issue #37)

## Decision

Every authored stdio connection is generated, by default, as a native
harness MCP entry whose command is a tenon relay rather than the
connection's own command:

```json
"github": { "command": "tenon", "args": ["mcp", "relay", "github", "--agent", "..."] }
```

The relay invocation is a pointer, never a copy: the generated line carries
the connection name and the agent source, and the relay resolves the real
command, arguments, and environment at launch from the authored
`connections/<name>.md` — the same single source of truth apply renders.
Before opening anything the relay runs the same fingerprint and
verification gate as `tenon mcp serve`, so a stale workspace refuses to
relay rather than launching yesterday's server. It then spawns the
connection's real server as its child and sits between the two stdio
streams with the managed server's bounded line scanner.

The author journey does not change: edit files, `tenon apply`, start the
harness. No human runs the relay; the harness spawns it exactly as it
spawns the managed server today.

### Interpose on tools, pass everything else through

The relay is a transparent bidirectional relay by default and interposes
only on methods it recognizes:

- `tools/call` and `tools/list` cross the same internal middleware chain as
  built-in and authored calls — audit, trace propagation (re-emitting trace
  context in `_meta` downstream), and whatever else the chain mounts.
- Every other method — `resources/*`, `prompts/*`, `sampling/createMessage`,
  elicitation, notifications, and any method minted after this tenon binary
  shipped — is forwarded verbatim in both directions, at most audited as an
  uninterpreted method name after a grammar check. Forward compatibility is
  by construction: an unknown method flows through an old binary untouched.
- Refusal shrinks to the one thing passthrough cannot cover: a transport the
  relay cannot dial. A connection requiring credentials tenon would have to
  hold (an OAuth-authorized remote transport) is refused at apply time with
  a named diagnostic naming the escape hatch, per
  [ADR 0006](0006-use-a-local-secretless-operation-broker.md).

Tool names, `tools/list` contents, request ids, and per-server approval
prompts all survive byte-identical to the native rendering: the harness
sees the same server shape one hop later. Codex's approval delegation
remains scoped to the actual managed server and never covers relayed
third-party tools.

### Default on, agent-level escape hatch

Relaying is the default because the product claim — one audited crossing
for the agent's whole declared surface — should not require a knob per
connection (tenet 1). Root `instructions.md` frontmatter accepts
`managed-connections: false` (the `friction-notes` precedent) to flip the
whole agent back to native rendering; the choice is a pure apply-time
rendering decision with no runtime branching, and re-apply plus diff shows
exactly which crossing each connection uses. Per-connection and per-tool
granularity is deferred: the per-connection slot exists in each
connection's own frontmatter when wanted, and per-tool policy is a
permissions question that belongs with issue #10's investigation wherever
the call routes.

Plugin MCP entries stay native ([ADR 0010](0010-map-plugin-mcp-through-native-harness-configuration.md));
extending the relay to them is a separate decision on its own evidence.

## What this amends, and what it only scopes

The specification's statement that the native harness owns MCP startup and
trust, and that tenon does not proxy MCP calls, is amended for authored
connections: startup of a relayed connection's server is delegated by the
harness to the relay the harness itself launched, and trust remains the
author's — declaring a connection is the same trust act it was natively,
now with an audited crossing.

The claim "inputs and outputs are schema-validated" is *scoped*, not
silently kept: it holds for the built-in and authored surface. Relayed
traffic is bounded (line size) and audited (lifecycle, grammar-checked
method and tool names, content-free identifiers) but passed through
unvalidated — the relay forwards content it does not interpret, and the
specification must say so rather than let the boundary claim enforcement
it does not deliver (north star #3). Audit output remains content-free
everywhere, including for relayed calls.

## Alternatives considered

- **One aggregated managed server fronting every connection.** Workable —
  it is what MCP gateways ship — but the HTTP gateway economics do not
  transfer to local child processes: there is no shared endpoint, TLS, or
  pool to amortize, the harness is natively multi-server, and the
  downstream servers dominate cost in either design. What aggregation
  changes is contracts: tool names must be prefixed (breaking native
  permission and hook matchers and skill references), N trust domains
  collapse into one approval domain, capability negotiation must be merged,
  and pass-through-what-you-don't-recognize dies because a mux must
  understand every method to route it. Aggregation stays the plausible
  rendering for *remote* transports, where the traffic is already HTTP and
  routable by URL; nothing here forecloses it, and per the ADR index the
  process layout is reference rendering.
- **Interposing without changing the generated command** (PATH shims,
  preload tricks, a resident supervisor). Rejected: the harness is the
  spawner and the generated config line is the only interposition point
  tenon owns; anything else is implicit interception at a trust boundary
  (tenet 5) and runtime supervision the north star refuses. The visible
  `tenon mcp relay` diff is the honest form.
- **Leaving connections native and offering middleware only to built-in and
  authored tools.** The fallback if this ADR is rejected; the internal
  middleware chain stands on its own without it.

## Consequences

- One relay process per relayed connection: a static Go binary blocked on
  pipe I/O, single-digit-MB resident, beside downstream servers that cost
  an order of magnitude more and run in every design including native.
  Process-per-responsibility is the existing pattern
  ([ADR 0014](0014-use-process-isolated-integration-packages.md),
  [ADR 0021](0021-execute-authored-tools-from-a-self-contained-closure.md)).
- Default-on changes what apply generates for existing projects with
  connections; the migration story is re-apply and read the diff.
- Audit becomes several stderr streams joined by agent, connection, and
  fingerprint fields rather than one stream; events stay joinable to the
  exact configuration by the existing fingerprint contract.
- The relay adds MCP-client-side responsibilities to tenon only to the
  depth of the interposed methods; the verbatim path deliberately learns
  nothing.

## Acceptance sketch

Credential-free, with fake connection servers:

1. A relayed connection round-trips `tools/call` byte-identically in names
   and results, with audit lines for the lifecycle and nothing of the
   arguments.
2. An unrecognized method round-trips verbatim in both directions,
   including a server-initiated request.
3. A stale or drifted workspace refuses to relay, naming the failure.
4. `managed-connections: false` renders the native entry byte-identically
   to today's output.
5. A connection requiring an undialable transport fails apply with the
   named diagnostic.
