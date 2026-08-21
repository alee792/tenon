# ADR 0013: Bound authored projects with aggregate budgets

- Status: accepted
- Re-records: prototype ADR 0029 (alee792/hctl)
- Extends:
  [ADR 0010](0010-map-plugin-mcp-through-native-harness-configuration.md)

## Plain-English summary

Tenon keeps hard safety ceilings for authored projects, but ordinary agents
should not encounter them. Skills, tools, subagents, schedules, plugins,
plugin MCP servers, and harness-specific files receive high count ceilings.
Aggregate file and byte budgets, bounded generated configuration, and separate
tool catalog and call limits control actual resource use. The ceilings remain
internal product constants rather than author configuration.

## Decision

Set the authored-project cardinality ceilings to 256 aggregate root and plugin
skills, 128 tools, 128 immediate subagents, 256 schedules, 128 plugin
directory entries, 1,024 entries in each plugin `skills/` location, 128
accepted plugin MCP servers, and 1,024 selected harness-specific files. A
skill and the tool-source inventory may each contain at most 1,024 files.

Root and imported skills share one 8,192-file and 64 MiB budget. Each skill
may contain at most 64 MiB, `SKILL.md` is limited to 128 KiB, and another
resource may contain at most 16 MiB. Tool source and native dependency files
share a 64 MiB budget. Subagent sources share 16 MiB, schedule sources share
16 MiB, and selected harness-specific files share an 8 MiB aggregate budget.

The authored-tool language-host protocol uses separate response ceilings. Tool
calls, results, and individual schemas are bounded at 64 KiB; the complete
inspection catalog may contain up to 8 MiB. Generous discovery therefore does
not weaken the managed invocation boundary.

Generated Claude `.mcp.json` and Codex `.codex/config.toml` files may contain
at most 8 MiB, and verification applies that same ceiling. Other
generated-file verification accepts the largest supported 16 MiB skill
resource, so apply and later verification use compatible bounds.

These limits are not configurable through files, environment variables, or CLI
flags. Root and project-level directory violations fail before workspace
mutation. Optional plugin components keep their isolation behavior: invalid or
excess skills and servers warn and skip at their authored paths when the
containing plugin remains independently valid.

## Context

Ceilings exist because load and apply read, validate, fingerprint, retain, and
copy authored bytes; removing bounds would be unsafe. But bounding only item
counts multiplies worst-case memory and generated-state work: 256
independently maximal 8 MiB skills would admit 2 GiB before generated copies.
Aggregate budgets express the resource boundary directly while allowing many
small, composed skills. The ceilings are safety limits, not ordinary-use
quotas — an authored project that meets one has almost certainly stopped being
a legible document first.

A single shared line bound for both catalog inspection and individual tool
calls would make a larger tool count ineffective even when each tool is valid,
and generated MCP configuration needs an explicit aggregate ceiling that
verification shares; both downstream limits move with discovery.

## Consequences

- Normal projects and composed plugin skill sets can grow far beyond typical
  size without adding configuration.
- A project can trade a few large skill resources for many small skills while
  remaining inside the same aggregate skill-content ceiling.
- A larger tool catalog does not permit larger tool arguments, schemas, or
  results.
- Large generated MCP configurations fail before workspace mutation and remain
  verifiable when accepted.
- [ADR 0009](0009-import-vendored-agent-plugin-skills.md) remains
  authoritative for plugin discovery, precedence, collision, and component
  isolation; this decision owns every count and byte ceiling.
