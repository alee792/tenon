# Glossary

Durable author-facing terms, defined once. The glossary is a budget, not an
index: a term earns its entry by being load-bearing across documents, and
growth requires stated payment.

- **Agent project** — a directory whose layout is the API: optional
  `instructions.md` plus conventional component directories (`skills/`,
  `plugins/`, `tools/`, `subagents/`, `mcp/`, `schedules/`,
  `harnesses/`). The directory name supplies the agent name. A directory is
  proven an agent project by a present `instructions.md` or by a supplied
  agent manifest whose expected source fingerprint matches it.
- **Agent source** — the authored files of an agent project: portable,
  legible, versionable, and independent of any repository that stores it or
  workspace that runs it.
- **Agent manifest** — an optional bounded file, supplied at application
  rather than stored in agent source, that pins the runtime closure the
  directory alone cannot express: harness version, model, tenon version, and
  installed-package identities. It identifies and pins; it never lists
  components.
- **Improvement loop** — an agent or optimizer revising an agent's files; an
  author coequal with the person. Tenon is its substrate — validation,
  reproducible application, and attribution — never the loop: evaluation and
  selection stay outside.
- **Workspace** — the directory where generated harness files, apply records,
  and dispatch state live and where the harness and authored tools operate.
  Defaults to the agent source directory; always independently selectable.
- **Harness** — the native coding agent that owns intelligence: model loop,
  context, native tools, approvals, and interactive interface. Initially
  Claude Code and Codex. Tenon compiles to it and never replaces it.
- **Skill** — one directory under `skills/` following the open Agent Skills
  specification: a `SKILL.md` plus arbitrary resources, copied byte-for-byte
  into the selected harness's native skill location.
- **Plugin** — one complete publisher-authored Agent Plugin v1 package,
  vendored intact beneath `plugins/` and validated locally; its skills and
  MCP declarations map into native harness configuration.
- **Connection** — one `mcp/<name>.md` authoring a standalone native MCP
  server: either an installed integration-package capability or a remote
  HTTPS endpoint (with optional headers). The harness owns everything at
  runtime, including any authentication the endpoint requires.
- **Schedule** — one Markdown file under `schedules/` whose path is its name,
  whose frontmatter holds one five-field cron string, and whose body is the
  task prompt. Apply validates and fingerprints it; only an explicit
  foreground clock or trigger executes it.
