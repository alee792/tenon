# ADR 0017: Vendor components manually, without an acquisition engine

- Status: accepted
- Re-records: prototype ADR 0036 (alee792/hctl)
- Amended by:
  [ADR 0026](0026-author-remote-first-spec-aligned-mcp.md)
  (§ plugin acquisition), whose pointer-plus-commit-SHA plugin references
  and explicitly online fetch, separate from an offline apply, are the
  evidence this record required; the amendment is now in effect as of
  issue #58

## Context

The prototype built and then removed a component-neutral acquisition engine:
`plugin|skill add/status/update/remove` commands that fetched complete Agent
Plugin and Agent Skill directories from exact local, HTTPS Git, and
digest-pinned archive sources, recorded provenance in a dependency lock file,
enforced offline drift checks on every project load, and protected mutations
with a write-ahead journal, cross-process locks, and prospective full-project
validation. The machinery was roughly 4,000 lines — for behavior an author can
perform with `cp` or `git`: copying a reviewed directory into conventional
source.

## Decision

Manual vendoring is the only acquisition journey: any complete reviewed
directory copied beneath `plugins/` or `skills/` is discovered by convention.
Tenon has no `plugin` or `skill` acquisition commands, no dependency lock
file, and no network acquisition of components. Review, version pinning, and
provenance belong to the author's own version control, where they already live
for every other authored file.

## Consequences

- A dependency lock file such as the external `skills-lock.json` convention is
  inert: tenon neither reads, validates, nor deletes it, like any other
  unrecognized root file.
- Project loading takes no per-root operation lock and verifies no
  acquired-tree identity; the source fingerprint covers every discovered
  conventional file byte-for-byte, so drift in vendored directories still
  changes the fingerprint and triggers ordinary stale-setup handling.
- Reintroducing acquisition machinery requires a new ADR with evidence that
  version-control review is insufficient.
