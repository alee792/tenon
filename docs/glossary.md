# Glossary

Durable author-facing terms, defined once. The glossary is a budget, not an
index: a term earns its entry by being load-bearing across documents, and
growth requires stated payment.

- **Agent project** — a directory whose layout is the API: optional
  `instructions.md` plus conventional component directories (`skills/`,
  `plugins/`, `tools/`, `subagents/`, `mcp/`, `schedules/`,
  `harnesses/`). The directory name supplies the agent name. A directory is
  proven an agent project by a present `instructions.md` or by a supplied
  pin set whose expected source fingerprint matches it.
- **Agent source** — the authored files of an agent project: portable,
  legible, versionable, and independent of any repository that stores it or
  workspace that runs it.
- **Pin set** — an optional bounded file (the *pins file*; formerly called
  the *agent manifest*), supplied at application rather than stored in agent
  source, that pins the runtime closure the directory alone cannot express:
  harness version, model, tenon version, and installed-package identities.
  It is minted by the gate (`tenon check --write-pins FILE`) and supplied to
  later commands as `--pins FILE`, where it is verified fail-closed against
  the resolved closure. It identifies and pins; it never lists components.
- **Catalog** — the resolved capability inventory `tenon check --emit
  catalog` reports: skills (including plugin-merged ones, with their
  descriptions), tools with their language, MCP servers, subagents, and
  schedules, exactly as the load resolved them. It is derived from the
  source, never authored: tenon emits a catalog only for a source that
  passes the gate and never accepts one as input, because an authored
  catalog would be precisely the second inventory the directory-is-the-API
  convention exists to abolish.
- **Source digest** — the `sha256:` content hash over an agent source's
  authored files that a failing gate reports as `source_digest`, so a
  rejected candidate is attributable. It is deliberately not a fingerprint:
  a digest names bytes, a fingerprint names a configuration the gate proved.
  A consumer joins failures by digest and successes by fingerprint: the
  digest is hashed under its own domain prefix, so a source's digest always
  differs from that tree's fingerprint. Both render as `sha256:` plus 64 hex
  characters, so the field a value arrives in carries the meaning, never the
  value alone.
- **Outcome** — the field every machine-readable stream's final object
  carries, from the vocabulary `ok / gate_failed / drift / blocked / error`.
  `ok`: the command did what it was asked. `gate_failed`: the source itself
  is invalid, and the object names the bytes that failed with a source
  digest. `drift`: the source is fine but the workspace no longer matches a
  fresh apply. `blocked`: clean refused to remove what it found. `error`:
  the run could not complete for a reason that is not the source's fault —
  an unreadable pin set, an unwritable path, a closure that would not
  resolve, a harness that would not start. The first four are findings a
  loop scores; **`error` is never scored** — it is a statement about the
  environment, which the loop retries or escalates. The field is the
  authoritative machine signal; the process exit code is a coarse
  projection of it, since one integer cannot carry both what happened and
  what was produced.
- **Improvement loop** — an agent or optimizer revising an agent's files; an
  author coequal with the person. Tenon is its substrate — the gate
  (`tenon check`), reproducible application, and attribution — never the
  loop: evaluation and selection stay outside.
- **Workspace** — the directory where generated harness files and apply
  records live and where the harness and authored tools operate.
  Defaults to the agent source directory; always independently selectable.
- **Harness** — the native coding agent that owns intelligence: model loop,
  context, native tools, approvals, and interactive interface. Initially
  Claude Code and Codex. Tenon compiles to it and never replaces it.
- **Skill** — one directory under `skills/` following the open Agent Skills
  specification: a `SKILL.md` plus arbitrary resources, copied byte-for-byte
  into the selected harness's native skill location.
- **Plugin** — one complete publisher-authored Agent Plugin v1 package,
  validated locally; its skills and MCP declarations map into native harness
  configuration. It is either vendored intact beneath `plugins/<name>/` or
  declared by a **plugin reference file**, `plugins/<name>.md`, whose closed
  frontmatter names a `source` and a full commit `rev`. Only the explicitly
  online `tenon plugin fetch` resolves a reference into the content-addressed
  plugin cache; every other command stays offline.
- **Authored MCP server** — one `mcp/<name>.md` declaring a native MCP server
  in the Agent Plugins `mcp.json` server-entry vocabulary: `streamable-http`
  (an HTTPS `url` with optional `headers`), `stdio` (a command in the agent
  tree), or tenon's own `installed` (an integration-package capability). The
  filename is the server name. The harness owns everything at runtime,
  including any authentication the endpoint requires — tenon renders the
  declaration and stops. The CLI verb and diagnostics call it `mcp`; older
  documents call it a *connection*.
- **Mask** — the fourth `mcp/<name>.md` form: `override: plugins/<name>` with
  `enabled: false`, suppressing a plugin-declared server of that name without
  replacing it. An authored server of the same name instead wins outright,
  with a warning naming both sources.
- **Schedule** — one Markdown file under `schedules/` whose path is its name,
  whose frontmatter holds one five-field cron string, and whose body is the
  task prompt. Apply validates and fingerprints it; only an explicit
  foreground clock or trigger executes it.
