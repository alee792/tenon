# Managed middleware

- Status: exploration record for
  [issue #37](https://github.com/alee792/tenon/issues/37); design direction,
  not yet an accepted contract. The one piece that amends a spec'd boundary —
  relaying managed connections — is proposed separately in
  [ADR 0023](../adr/0023-relay-managed-connections-through-per-connection-shims.md).
- Last verified: 2026-08-27

## The question

`internal/mcp/server.go`'s `callTool` is the single chokepoint every managed
tool call crosses — built-in and authored alike — and its `handler` type
already resembles Go HTTP middleware. Issue #37 asks what an extension point
there should be, and whether author-supplied Go code belongs in it.

The answer this exploration converged on: **tenon's process becomes the one
audited, instrumented crossing for every tool call the agent's declared
surface makes** — built-in, authored, and (per ADR 0023) third-party MCP
connections — via one internal middleware chain that is tenon's own code.
Arbitrary author middleware is declined: author code that gates calls
already has a home in harness hooks, and author code that runs already has a
home in authored tools. What middleware adds is tenon-owned cross-cutting
behavior — audit, tracing, metrics, later limits — plus, when concretely
wanted, a small declarative settings surface.

## The layer map

Which middleware are even possible depends on what each layer can see.

**Visible at the chokepoint (tenon's Go process):** JSON-RPC method, tool
name, validated raw arguments, the content-free request identifier, whether
the tool is built-in or authored, agent name and source fingerprint,
wall-clock latency, lifecycle outcome, `initialize` client identity, and the
whole `_meta` object.

**Isolated to the per-language host, by design
([ADR 0021](../adr/0021-execute-authored-tools-from-a-self-contained-closure.md)):**
everything inside a tool — its logs, its outbound calls, its
sub-operations. Host stderr is drained into a bounded ring and never
forwarded. Boundary middleware can time and gate a call; it can never see
inside one.

**Isolated to the harness, permanently (north star #2):** the model loop,
prompts, token usage, turn boundaries, approvals, and every harness-native
tool call. Middleware framed as "observe what the agent did" is unbuildable
here and stays with the harness's own hooks and telemetry.

Two seams connect the layers:

- **`_meta` inbound.** The boundary decodes all of `_meta` and exposes it on
  the per-call context verbatim; the request line's existing byte bound
  settles size. Discipline lives at the sinks, not the parse: a value enters
  audit output only grammar-validated into an identifier (the W3C
  `traceparent` shape, the same trick as the tool-name grammar); forwarding
  downstream travels in tenon's own envelope, never merged into the author's
  arguments; and any middleware that *acts* on a `_meta` key validates that
  key itself, because `_meta` is client-controlled data at a trust boundary.
  A traceparent that fails the grammar is treated as absent per W3C's own
  tolerance rule; tenon then mints its own span context, so the audit
  `trace=` field always carries a minted or grammar-proven identifier and
  never client text.
- **The host line protocol outbound.** `internal/toolruntime`'s protocol is
  tenon's own, not MCP, so trace context propagates to authored tools as an
  envelope member beside `id` and `method` — no author schema is touched,
  and hosts that do not know the field ignore it.

## Ecosystem

- **The shape is unanimous; there is no standard yet.** FastMCP's
  `on_call_tool(context, call_next)`, the official Go SDK's
  `AddReceivingMiddleware` (`func(next MethodHandler) MethodHandler`), and
  the gateway interceptors (Docker MCP Gateway, Envoy AI Gateway) all wrap a
  next-handler. SEP-1763 proposes interceptors as a first-class MCP
  primitive but is a draft without a sponsor: build shaped like it, never on
  it (tenet 4). Matching the Go SDK's signature keeps its middleware
  ecosystem reusable if tenon ever adopts that SDK for the relay's client
  side.
- **Context is carried in the call, not a context type.** FastMCP needs a
  context object because Python lacks `context.Context`; the Go idiom passes
  context and request as handler arguments. Tenon's existing `call` struct
  is the middleware context — widened with `context.Context`, agent
  identity, fingerprint, backend kind, the resolved definition, decoded
  `_meta`, trace context, and start time.
- **Telemetry vocabulary is real but unstable.** GenAI and MCP semantic
  conventions live in OpenTelemetry's dedicated GenAI repository and every
  attribute is still marked Development. Borrow the attribute *names* for
  structured output; take no OTel SDK dependency.
- **Tenon composes behind gateways; it is not one.** Gateways own
  multi-server policy, OAuth, and org-wide governance. Tenon owns one
  project's crossing — and becomes the propagation link between an upstream
  gateway's trace and an authored tool's own telemetry.

## Use-case catalog

| Use case | Arbitrary code? | Disposition |
| --- | --- | --- |
| Structured audit sink (JSONL beside the fixed line) | No | Middleware |
| Latency and outcome metrics per tool | No | Middleware |
| Trace propagation in and out | No | Middleware + host envelope |
| Per-tool rate or concurrency limits | No | Declarative, deferred until wanted |
| Circuit-breaking a misbehaving host | No | Middleware, deferred until wanted |
| Allow/deny tool lists | No | Cut: harness permissions and issue #10 |
| Request-scoped auth injection | Yes-ish | Cut: ADR 0006's broker owns secrets |
| Argument rewriting/rejection | Yes | Declined as middleware; see below |
| Agent evals | — | Cut: out of scope by charter; middleware emits content-free, fingerprint-joinable events for outside evaluators and retains nothing |

Argument gating in author code already has two homes that need no new trust
decision: harness hooks (Claude's `PreToolUse`/`PostToolUse` match managed
tool names and can block, reachable through the existing vendor-field
passthrough), and an authored tool designated as a gate, which reuses the
existing hosts and pays the double-hop only for calls that opt in. Compiling
author Go into the tenon binary is rejected outright: it breaks
one-binary-per-project and moves author code inside the process that audits
and bounds every call — the exact inversion of ADR 0021. A dedicated
middleware host process stays in reserve; it taxes every call to serve a
use case not yet demonstrated.

## The internal chain

`type Middleware func(handler) handler`, chained around dispatch between the
`requested` audit event and the handler, so a middleware rejection lands
before `authorize`. Dispatch consolidates on one backend contract — today's
`Caller` widened to roughly `Call(ctx, call)` plus `Definitions()` — with
the built-ins, the tool runtime, and the relay's passthrough as its
implementations. One chain, written once, agnostic to which backend
answers. Consolidation is the interface and the chain, never the packages:
`toolruntime`'s closure contract, `mcp`'s framing, and the relay stay
distinct responsibilities, and the host line protocol stays simpler than
MCP on purpose.

The server also does not implement MCP's own `logging` capability: it
routes observability to the model's client, the wrong direction for an
audit surface.

## Slices

Build order is demand-gated, not scheduled: slices 1 and 2 land together as
one change the day slice 2 has a named consumer (the first improvement loop
or operator that would read the structured audit); nothing below it is
built before its own trigger. A chain with one middleware is ceremony, and
every observability case has a cheaper harness-side substitute until the
fingerprint-joined audit stream has a reader.

1. **Internal seam.** The middleware type, the widened `call`, the backend
   interface; existing behavior becomes the chain's base; proven by existing
   tests, zero behavior change.
2. **Observability.** Optional structured JSONL audit sink *beside* the
   fixed `agent=… tool=… request=… outcome=…` line (that format is spec'd
   surface — additive only), plus per-call latency; field names borrowed
   from GenAI semconv.
3. **Trace propagation.** `_meta` ingestion per the seam rules, minted span
   contexts, the host-envelope member, and re-emission upstream by the
   relay. Deferred hardest: its value requires an upstream sender or a
   collector actually running, and today there is neither.
4. **The relay** (ADR 0023, proposed with its own acceptance trigger) —
   where the chain starts covering third-party connections.
5. **Deferred until a concrete case exists:** rate limits, circuit breaking,
   any declarative middleware settings. Agent-level settings have a home
   when needed: root `instructions.md` frontmatter, the `friction-notes`
   precedent.

## Falsifiers

- If nothing beyond structured audit and latency is wanted by 2027-03,
  delete the seam and hard-code the two.
- If SEP-1763 gains a sponsor and lands, re-decide the surface against it.
- If harness hooks plus harness telemetry cover every case authors actually
  raise, close #37 as answered by the harness.

## Verified references

- [SEP-1763: Interceptors for MCP](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1763)
  — draft, unsponsored.
- [FastMCP middleware](https://gofastmcp.com/python-sdk/fastmcp-server-middleware-middleware)
  and the
  [official Go SDK](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp)
  — the convergent next-handler shape.
- [Docker MCP Gateway interceptors](https://www.ajeetraina.com/a-quick-look-at-docker-mcp-gateway-interceptors/)
  and [Envoy AI Gateway MCP](https://aigateway.envoyproxy.io/docs/0.5/capabilities/mcp/)
  — gateway-side interposition and the relay-verbatim posture.
- [OpenTelemetry GenAI semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai)
  — attribute vocabulary, all still Development stability.
- [W3C Trace Context](https://www.w3.org/TR/trace-context/) — the
  `traceparent` grammar and the treat-malformed-as-absent rule.
- [Claude Code hooks](https://code.claude.com/docs/en/hooks) — the harness
  home for author-written gating.
