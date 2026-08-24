# mixed-tools example

A minimal agent project that authors three tools in three languages and serves
them through tenon's own `managed` MCP server. It exists to demonstrate — and
prove end to end — that a tool is just a file: no protocol code, no tenon
dependency.

```
examples/mixed-tools/
  instructions.md          # agent instructions (frontmatter description + body)
  go.mod                   # Go tools require a go.mod at the agent root
  pyproject.toml           # Python tools require pyproject.toml + uv.lock
  uv.lock                  # locked Python dependencies (pydantic)
  deno.json                # TypeScript tools require deno.json + deno.lock
  deno.lock                # locked TypeScript dependencies (zod)
  tools/
    reverse/tool.go        # Go tool         -> exposed as "reverse"
    wordcount.py           # Python tool     -> exposed as "wordcount"
    shout.ts               # TypeScript tool -> exposed as "shout"
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
- **TypeScript** (`tools/NAME.ts`): a default export
  `{ description, inputSchema, outputSchema, execute }`, where the schemas are
  strict Zod object schemas (`.strict()` / `z.strictObject`) and `execute`
  receives the already-parsed input. Dependencies (here Zod v4) resolve through
  your own `deno.json` import map and `deno.lock`.

In all three, underscores in the name are exposed as hyphens, and files/dirs
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

`apply` prepares one host per language (a Go build, a `uv sync --locked`, and a
`deno check`), inspects each catalog before writing anything, and generates the
native harness files plus the `managed` MCP server entry. It reports:

```
managed tools: echo, reverse, shout, wordcount via MCP; native harness tools remain unmanaged
```

Running the tools requires each language's toolchain on `PATH` (`go`, `uv`,
`deno`). In an environment that intercepts TLS with a custom certificate
authority, point the toolchains at it the way each expects — `SSL_CERT_FILE`
for the OpenSSL-based tools and `DENO_CERT` for Deno; tenon forwards those to
the toolchains it runs.

## Drive the managed boundary directly

`tenon mcp serve` speaks MCP 2025-06-18 on stdin/stdout (audit on stderr):

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"demo","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"reverse","arguments":{"text":"hello world"}}}' \
  '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"wordcount","arguments":{"text":"the quick brown fox"}}}' \
  '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"shout","arguments":{"text":"hi there"}}}' \
  | ./tenon mcp serve ./examples/mixed-tools --harness claude --workspace /tmp/mixed-ws
```

`reverse` returns `{"reversed":"dlrow olleh"}`, `wordcount` returns
`{"words":4,"chars":19}`, and `shout` returns `{"shouted":"HI THERE!"}` — one
Go host process, one Python host process, and one TypeScript host process, the
same tool surface on both harnesses.
