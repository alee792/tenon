# Working status

- Updated: 2026-08-20

## Implemented

The first apply slice, credential-free tested: `tenon validate` and
`tenon apply` load an instructions-only project (strict closed frontmatter,
bounds, symlink rejection, name normalization, source fingerprint), render
the always-on surface for Claude (`CLAUDE.md`) and Codex (`AGENTS.md`) with
visible tool ownership, refuse hand-authored and modified-owned files before
any mutation, remove stale owned files, keep an owner-only atomic apply
record per workspace and harness, and report failures as prose or JSONL
diagnostics with stable identifiers, authored paths, and validate/apply
parity. Recognized component directories that are not yet compiled fail
validation rather than being silently dropped.

## Gaps

The complete [product specification](../product-spec.md) acceptance list, to
be built in journey order — apply and the five-minute journey first, then
authored tools, connections, headless dispatch, schedules, staging — each
slice credential-free tested:

1. One authored project compiles deterministically for both harnesses, and
   apply produces native, discoverable files while refusing conflicts and
   modified-file overwrites; a directory proven by neither instructions nor a
   supplied matching manifest is refused as not an agent project, and an
   instructions-free project generates an empty always-on surface rather than
   failing.
2. Both generated integrations expose the same managed MCP tool surface, and
   a mixed TypeScript/Python/Go project is prepared once per apply with one
   host process per language.
3. Agent source applies outside its own directory: generated files and
   execution use the workspace while discovery stays rooted in source.
4. Subagents generate natively with inheritance and exact effort mapping,
   and child skills, tools, dependencies, and nested subagents are rejected
   rather than ignored; skills and their resources round-trip byte-for-byte
   with executable intent, vendor metadata preserved and warned;
   harness-specific files round-trip only to their selected harness with
   full ownership protection.
5. Plugin skills and plugin MCP declarations import with deterministic
   collision handling and isolated component failure.
6. Connections generate exact native configuration for installed and remote
   targets without contacting anything; a name collision with `managed`,
   another connection, or a plugin server fails before mutation; and a
   conspicuous fake ambient value never appears in generated files, state,
   staging, or evidence.
7. Headless dispatch durably queues FIFO input, deduplicates input IDs,
   resumes sessions, and marks unproven restart work uncertain.
8. Schedules validate and fingerprint identically for both harnesses;
   triggers deduplicate stable occurrence IDs, open fresh sessions, honor
   turn deadlines with retained uncertainty, and the UTC clock admits only
   current non-overlapping occurrences and drains on shutdown.
9. Staging produces a deterministic, credential-free, minimal runnable tree
   whose entrypoint verifies identity and fingerprint before a turn;
   preparation never mutates authored source, and publication is one rename
   only after the manifest is complete.
10. Managed audit output remains content-free.
11. A supplied agent manifest is verified before apply and before every
    tenon-owned process open: a drifted harness version, package identity,
    or source fingerprint fails closed naming the exact pin; writing the
    manifest for an unchanged closure is byte-identical; an unsupplied
    manifest changes nothing; and no pin, fingerprint, or provenance value
    appears in model-facing generated content.
12. Validate reports the same failures as apply without mutating anything,
    and its structured diagnostics carry stable identifiers and authored
    paths that match apply's own failures.

The list is the proof skeleton, not the whole contract: every stated behavior
in the specification binds.
