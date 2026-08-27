# Examples

Runnable agent projects that exercise tenon end to end. Each subdirectory is
an ordinary authored agent project — `tenon apply` and `tenon validate` run
against it exactly as they would against your own.

## `mixed-tools`

A minimal agent that authors one tool per supported language — Go
(`tools/reverse/tool.go`), Python (`tools/wordcount.py`), and TypeScript
(`tools/shout.ts`) — and serves all three through tenon's own `managed` MCP
server. It exists to prove, end to end, that a tool is just a file: no
protocol code, no tenon dependency. See its own
[README](mixed-tools/README.md) for the authored contract and how to run it,
and [`instructions.md`](mixed-tools/instructions.md) for what the agent is
told.

Applying and serving `mixed-tools` locally (`tenon apply`, `tenon mcp
serve`) already exercises all three languages. Staging it into a deployable
OCI image does not yet run any of them end to end: per
[ADR 0021](../docs/adr/0021-execute-authored-tools-from-a-self-contained-closure.md),
staging cannot yet serve authored tools end to end in any language. Issues
#14–#17 land the per-language closures, starting with Go reachability and
named refusals for Python and TypeScript. This is recorded honestly rather
than hidden, per the specification's failure-and-safety principle.
