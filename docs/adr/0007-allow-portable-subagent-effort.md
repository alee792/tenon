# ADR 0007: Allow portable subagent effort requests

- Status: accepted
- Re-records: prototype ADR 0010 (alee792/hctl)

## Plain-English summary

Subagent authors need one portable way to request how much reasoning a child
uses without choosing a model or creating a separate runtime. An immediate
subagent may set `effort` to `low`, `medium`, or `high`; tenon validates it
and writes the matching native Claude or Codex field. The harness still decides
whether the request is honored. This does not add child-owned tools, skills,
permissions, sandboxing, nesting, lifecycle management, or runtime observation.

## Decision

Amend [ADR 0004](0004-use-native-inherited-subagents.md) so an immediate
subagent's `instructions.md` frontmatter may contain optional string `effort`
beside required string `description`. Accept exactly `low`, `medium`, or
`high`.

Emit the value through each selected harness's documented native effort
field (currently `effort` in Claude's generated agent frontmatter and
`model_reasoning_effort` in Codex's generated custom-agent TOML). Omit the
native field when source effort is absent, preserving description-only
output. Keep root instructions description-only.

## Consequence

The source fingerprint and generated child file change with the effort request,
so ordinary apply ownership and stale-source checks cover additions, changes,
and removal. Tenon owns validation and native mapping only; model availability,
account settings, harness version, and policy may ignore or constrain the
request without changing tenon's additive boundary.
