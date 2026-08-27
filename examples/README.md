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
serve`) exercises all three languages, and so does staging it into a
deployable OCI image: per
[ADR 0021](../docs/adr/0021-execute-authored-tools-from-a-self-contained-closure.md),
`tenon stage` produces a self-contained execution closure for each
language and the staged tree serves real tool calls end to end, with no
build toolchain and no network. Issues #14–#17 landed the per-language
closures, Go through Python through TypeScript.
