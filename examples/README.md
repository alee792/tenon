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
OCI image is narrower today: per
[ADR 0021](../docs/adr/0021-execute-authored-tools-from-a-self-contained-closure.md),
only the Go closure is landed end to end. The Python closure (a pinned,
checksum-verified standalone CPython) and the TypeScript closure (`deno
compile` or a pruned self-contained `deno` executable) are staged with the
ADR's own per-language work; until each lands, staging refuses that
language with a named diagnostic rather than emitting a tree that cannot
run. This is recorded honestly rather than hidden, per the specification's
failure-and-safety principle.
