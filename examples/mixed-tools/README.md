# mixed-tools example

A minimal agent project that authors two tools in two languages and serves them
through tenon's own `managed` MCP server. It exists to demonstrate — and prove
end to end — that a tool is just a file: no protocol code, no tenon dependency.

```
examples/mixed-tools/
  instructions.md          # agent instructions (frontmatter description + body)
  go.mod                   # Go tools require a go.mod at the agent root
  pyproject.toml           # Python tools require pyproject.toml + uv.lock
  uv.lock                  # locked Python dependencies (pydantic)
  tools/
    reverse/tool.go        # Go tool  -> exposed as "reverse"
    wordcount.py           # Python tool -> exposed as "wordcount"
```

## The authored contract

- **Go** (`tools/NAME/tool.go`): export `Description` (string), `Input` and
  `Output` structs, and `Execute(context.Context, Input) (Output, error)`.
  Standard library only; the exposed name is the directory name. Input/Output
  are reflected into strict JSON Schemas.
- **Python** (`tools/NAME.py`): a module-level `description` string, Pydantic
  `Input` and `Output` models, and `execute(input, context)` (sync or async).
  The exposed name is the filename stem. Dependencies come from your own
  `pyproject.toml` / `uv.lock`.

In both, underscores in the name are exposed as hyphens, and files/dirs
starting with `_` or `.` are not tools.

## Run it

Build tenon, apply the agent to a workspace, then start your harness there:

```sh
go build -o ./tenon ./cmd/tenon

# Claude Code
./tenon apply ./examples/mixed-tools --harness claude --workspace /tmp/mixed-ws
# or Codex
./tenon apply ./examples/mixed-tools --harness codex  --workspace /tmp/mixed-ws
```

`apply` prepares one host per language (a Go build and a `uv sync --locked`),
inspects each catalog before writing anything, and generates the native
harness files plus the `managed` MCP server entry. It reports:

```
managed tools: echo, reverse, wordcount via MCP; native harness tools remain unmanaged
```

## Drive the managed boundary directly

`tenon mcp serve` speaks MCP 2025-06-18 on stdin/stdout (audit on stderr):

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"demo","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"reverse","arguments":{"text":"hello world"}}}' \
  '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"wordcount","arguments":{"text":"the quick brown fox"}}}' \
  | ./tenon mcp serve ./examples/mixed-tools --harness claude --workspace /tmp/mixed-ws
```

`reverse` returns `{"reversed":"dlrow olleh"}` and `wordcount` returns
`{"words":4,"chars":19}` — one Go host process and one Python host process,
the same tool surface on both harnesses.
