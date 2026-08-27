---
description: Maintain Tenon with its accepted product boundaries and quality gates.
---

# Repository guidance

Tenon compiles filesystem-authored agent projects into native configuration
for Claude Code and Codex, and is the reproducibility substrate for agent
improvement loops. Read `docs/north-star.md`, `docs/vision.md`,
`docs/product-spec.md`, `docs/glossary.md`, and
`docs/workbench/skill-compatibility.md` before changing product behavior.
The north star governs: every other document is evidence of decisions
already made and is overruled by it wherever they disagree.

Apply the north-star-review skill when accepting an ADR or when a change
mints a new author-facing concept, adds a subsystem or dependency, moves a
product boundary, or invests in an unvalidated future; other changes need no
reconciliation commentary. Keep the native harness responsible for its model
loop, context, native tools, approvals, and interactive interface. Tenon owns
agent-project discovery and validation, generated harness files,
dispatcher-managed sessions, and explicitly managed tools.

Keep packages organized by concrete responsibility; do not introduce generic
core, services, adapters, or utilities layers. Prefer the standard library.
Validate and bound filesystem, process, protocol, and model-visible inputs.
Never overwrite hand-authored harness files or claim governance over native
harness effects. Keep harness-specific protocols inside their harness
package. Validation diagnostics are a stable surface: keep their identifiers
stable and their machine-readable form in parity with prose.

Tests and the literal CLI journey define completion. Record consequential
architecture choices as short ADRs and run `./scripts/check.sh` for affected
work. Credentials, live external actions, publication, and deployment require
explicit human authorization.

The `hctl` prototype (alee792/hctl) is the frozen, read-only reference
implementation: consult its tests and acceptance records when a specified
behavior is in doubt, and port intent, never code.
