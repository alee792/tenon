---
description: Demo agent exposing authored Go and Python tools through the managed MCP boundary.
---

# Mixed-language tool demo

This agent ships two authored tools, each in a different language, both served
through tenon's own `managed` MCP server:

- `reverse` (Go, `tools/reverse/tool.go`) — reverse a UTF-8 string.
- `wordcount` (Python, `tools/wordcount.py`) — count words and characters.

Apply prepares one host process per language (a Go build and a `uv sync`),
inspects each catalog, and exposes both tools on the same managed surface for
Claude Code and Codex alike. Native harness tools remain unmanaged.
