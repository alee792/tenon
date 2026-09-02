# Tenon as the outer loop's substrate

How an improvement loop uses tenon to optimize an agent's skills and
context, read against Lilian Weng's *Harness Engineering for
Self-Improvement*
([Lil'Log, Jul 2026](https://lilianweng.github.io/posts/2026-07-04-harness/)).

This is evidence, not contract: where it disagrees with the
[north star](north-star.md), [vision](vision.md), or
[product spec](product-spec.md), those win.
[ADR 0018](adr/0018-add-the-revision-leg-to-the-measure.md) records why the
revision leg of the measure exists;
[ADR 0024](adr/0024-add-observation-to-the-revision-leg.md) and
[ADR 0025](adr/0025-make-the-fingerprint-the-unit-of-revision-identity.md)
carry the lineage framing this document uses.

## Weng's frame, in brief

Weng argues the near-term path to recursive self-improvement runs not
through a model rewriting its weights but through the **harness** — the
system around the model that orchestrates execution: how it plans, calls
tools, manages context, stores artifacts, and evaluates results. All of
that is cheaper to change than weights. Two ideas do the work here:

1. **Inner loop vs. outer loop.** The inner loop is the agent doing a
   task with its current tools, skills, and context policy. The outer loop
   improves that harness so the next inner loop goes further.
2. **The verifier is the bottleneck.** A loop is only as honest as its
   evaluator — and an evaluator is only useful if a measured gain can be
   attributed to the exact change that produced it.

Tenon takes no position on how strong a verifier is. It serves the second
half: making each change legible, well-formed, and exactly identifiable, so
whatever verifier the loop has is scoring a known configuration, not a blur.

## Where tenon sits

Tenon is **the loop's substrate, never the loop**. It proves a revision is
well-formed, never that it is an improvement, and it collects no
transcripts, evaluations, or scores
([non-goals](product-spec.md#explicit-non-goals)).

The boundary matters for reading Weng honestly: the native harness — Claude
Code or Codex — owns the model loop, planning, approvals, and runtime
context assembly, Weng's *inner* context layer. What tenon exposes to the
outer loop is the **authored capability surface**: the durable,
file-represented policy the harness reads before it starts thinking —
instructions, skills, tools, subagents, connections, schedules. The loop
edits that policy as files; the harness applies it at runtime.

## The units of lineage

Weng's outer loop needs a lineage: a record of which revisions existed, how
each performed, and which change produced which gain. Tenon does not store
that chain — and this is the sharpest way to state its contribution:
**tenon mints the units lineage is built from; the loop composes the
chain** ([ADR 0025](adr/0025-make-the-fingerprint-the-unit-of-revision-identity.md)).

There are two units:

- **The source fingerprint is the unit of revision identity.** One
  deterministic, content-addressed digest per apply, over all authored
  inputs. It is *commit-free* — defined on the working tree with no commit
  or staging, exactly when a mid-revision loop has no git identity to use —
  and *gate-proven*: `tenon check` reports a fingerprint only for a project
  that loads and whose tools prepare, so it certifies a runnable agent
  existed, which a tree hash does not.
- **The stable diagnostic identifier is the unit of revision rejection.**
  A revision that never becomes a fingerprint is named by the identifier
  set that rejected it — stable across releases, matching apply's own
  failures, parseable without reading prose.

Every apply and dispatch event carries the fingerprint, so observation made
outside tenon — transcripts, scores, whatever the loop's evaluator
collects — joins back to the exact configuration that produced it, with no
mapping for the loop to maintain. The fingerprint is parentless by
construction: it answers "same or different", never ancestry. A loop that
wants the edge fingerprints the source before mutating it; two node kinds
and that edge are the whole graph, and tenon holds none of it.

This is more than validation of form for the harness. Validation is the
gate a revision must pass; identity is what makes the revision a citable
experimental unit afterward. A loop needs both, and needs them from the
same substrate so they cannot disagree.

## The cycle

The loop runs the same cycle a person runs, without hands:

1. **Mutate.** Edit the authored files — add a skill directory, rewrite
   `instructions.md`, add a typed tool. The folder is the inventory; an
   empty `instructions.md` is a legitimate candidate.
2. **Gate.** `tenon check . --harness claude --format jsonl` emits one
   JSON line per failure with the stable identifier and authored path; the
   stream always ends with a final object carrying the run's `outcome` and,
   on success, the agent name and `fingerprint`. The loop self-corrects
   against identifiers, not prose.
3. **Apply.** `tenon apply` compiles the folder to native harness files and
   records the fingerprint. Identical source reapplies deterministically.
4. **Run and attribute.** Interactive, headless (`tenon run`), or
   scheduled — every dispatch event carries the fingerprint and, when a
   pin set is supplied, the pinned harness version, model, and package
   identities. A headless run's stream ends with a terminal `run.completed`
   event carrying the same envelope, the run's `outcome`, and `turns`: the
   outcome says whether the dispatcher finished the work it was given, the
   counts say how those turns went, and the loop scores the counts.
5. **Verify and select — outside tenon.** The loop's evaluator scores the
   run and keeps or discards the revision, building its lineage from the
   units above. See
   [the improvement-loop use case](use-cases.md#give-an-improvement-loop-a-substrate)
   for the full boundary.

## Weng's requirements, mapped

| Weng's requirement | Tenon mechanism |
| --- | --- |
| File-represented editable components | The agent is a folder; capability is added by adding a file, never by registering anything ([product spec](product-spec.md#the-authored-project)). |
| Bounded editable surfaces | Every surface has a safety ceiling and symlinks are rejected, so the loop's search space is finite and knowable. |
| Attribution of gains to exact configurations | The fingerprint on every apply and dispatch event; the optional [pin set](product-spec.md#pins) pins what the directory cannot express. |
| Permission control outside the loop | Apply, acquisition, trust, and credentials stay deliberate human acts; nothing mutates a workspace unvalidated. |

A fifth property matters most for a *cheap* loop: well-formedness is
validated for the loop the same way it is for a person. Harness-updating
capability is roughly flat across model sizes
([ADR 0018](adr/0018-add-the-revision-leg-to-the-measure.md)), so the loop
is often a small model whose binding constraint is not drafting skill but a
machine-checkable answer to "is this revision even valid."

## Optimizing skills and context

A skill is a directory under `skills/`; the loop grows or prunes the
library by adding or deleting directories, each candidate validated with
stable identifiers and folded into the fingerprint. "Added a
`context-compaction` skill and win-rate rose" is then a claim about a named
revision, not a vibe — and the same library compiles to both harnesses, so
portability is not another axis to search.

Context splits along tenon's boundary. Runtime context — what enters the
model's window this turn — is the harness's inner loop, untouched. What the
loop optimizes through tenon is the durable authored policy that shapes
every future window: `instructions.md`, connection and skill usage text,
subagent instructions. In Weng's terms, the loop edits the *skill of
managing context* as files; the harness runs this turn's selection. That
separation is why a cheap loop can iterate safely: it only ever changes
reviewable, bounded, identifiable policy, never the inside of a running
model loop.

Two guarantees keep the experiment honest:

- **The agent is not told how it was set up.** The pin set, its pins, and
  the fingerprint are never rendered into model-facing content, so a
  revision cannot condition on its own identity.
- **A pin is an axis, not an editable surface.** The loop tries a
  different model or harness version by changing a pin; what it
  *edits* stays the authored files. The two axes remain separable.

## What tenon deliberately does not do

- Evaluate, score, rank, or select among revisions.
- Store the lineage chain — transcripts, scores, ancestry, and the
  candidate population live with the loop; tenon mints only the units.
- Manage the runtime context window or run the model loop.
- Promote an agent-authored change automatically; permission stays a
  deliberate human act.
- Enforce instructions, sandbox authored tool code, or make model behavior
  safe from outside the harness.

The measure of the fit is the revision leg
([ADR 0018](adr/0018-add-the-revision-leg-to-the-measure.md), amended by
[ADR 0024](adr/0024-add-observation-to-the-revision-leg.md)): a revision
applies, runs, and attributes to its exact configuration without human
hands, with its change to the capability surface legible before it runs.
Weng describes the outer loop; tenon is the part that has to be true before
any of the rest can be trusted.

## Sources

- Lilian Weng, *Harness Engineering for Self-Improvement*, Lil'Log,
  Jul 2026 — <https://lilianweng.github.io/posts/2026-07-04-harness/>
- [ADR 0018](adr/0018-add-the-revision-leg-to-the-measure.md),
  [ADR 0024](adr/0024-add-observation-to-the-revision-leg.md),
  [ADR 0025](adr/0025-make-the-fingerprint-the-unit-of-revision-identity.md)
