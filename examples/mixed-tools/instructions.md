---
description: Demo agent exposing authored Go, Python, and TypeScript tools through the managed MCP boundary.
---

# Mixed-language tool demo

This agent ships three authored tools, each in a different language, all served
through tenon's own `managed` MCP server:

- `reverse` (Go, `tools/reverse/tool.go`) — reverse a UTF-8 string.
- `wordcount` (Python, `tools/wordcount.py`) — count words and characters.
- `shout` (TypeScript, `tools/shout.ts`) — uppercase a string.

Apply prepares one host process per language (a Go build, a `uv sync`, and a
`deno check`), inspects each catalog, and exposes all three tools on the same
managed surface for Claude Code and Codex alike. Native harness tools remain
unmanaged.
