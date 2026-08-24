# Tenon as the outer loop's substrate

How an improvement loop uses tenon to optimize an agent's skills and context,
read against Lilian Weng's *Harness Engineering for Self-Improvement*
([Lil'Log, Jul 2026](https://lilianweng.github.io/posts/2026-07-04-harness/)).

This is evidence, not contract. It reads the existing product against an
outside frame; where it and the [north star](north-star.md),
[vision](vision.md), or [product spec](product-spec.md) disagree, those win.
[ADR 0018](adr/0018-add-the-revision-leg-to-the-measure.md) already records
the load-bearing claim — the revision leg of the measure exists partly
*because* Weng's post names the properties this contract provides. This
document expands that one paragraph into a working account.

## Weng's frame, in brief

Weng's argument is that the near-term path to recursive self-improvement runs
not through a model rewriting its own weights, but through improvements to the
**harness** — "the system surrounding a base model that orchestrates execution
and decides how the model thinks and plans, calls tools and acts, perceives
and manages context, stores artifacts, and evaluates results." Tool use,
planning, context, memory, sub-agent delegation, and evaluation are all harness
concerns, and all are cheaper to change than weights.

Two ideas from the post do the work for us:

1. **Inner loop vs. outer loop.** The inner loop is the agent doing a task —
   largely autonomous, using its current tools, skills, and context policy. The
   outer loop improves the harness itself so the next inner loop goes further.
   For context specifically, Weng separates two layers: an outer layer that
   evolves *the skill of managing context*, and an inner layer that uses that
   skill to decide *what to include in the context* for a specific task.

2. **The verifier is the bottleneck.** A self-improvement loop is only as
   honest as its evaluator. Without a fast, precise verifier, the loop hacks
   whatever proxy it is given. The scarce ingredient is a trustworthy signal —
   and, alongside it, the ability to attribute a measured gain to the exact
   change that produced it.

Tenon takes no position on how strong any given verifier is. It addresses the
second half of that sentence: making the change legible, well-formed, and
exactly attributable, so that whatever verifier the loop does have is measuring
one known configuration rather than a moving target.

## Where tenon sits

Tenon is **the loop's substrate, never the loop.** It proves a revision is
well-formed; it never claims the revision is an improvement, and it collects no
transcripts, evaluations, or scores. Evaluation and selection stay outside, by
design ([product spec non-goals](product-spec.md#explicit-non-goals)).

The boundary matters for reading Weng honestly. Not every harness concern in
her list is tenon's to optimize. The native harness — Claude Code or Codex —
owns the model loop, planning, approvals, the interactive interface, and
**runtime context assembly and pruning**. That is Weng's *inner* context layer,
and it stays with the harness and always will (README; [north star](north-star.md)
tenet 2).

What tenon exposes to the outer loop is the **authored capability surface**: the
durable, file-represented policy the harness reads *before* it starts thinking —
instructions, skills, tools, subagents, connections, schedules. In Weng's terms
tenon is where the *outer* layer lives: the loop edits the durable skill and
context policy as files; the harness applies that policy, per turn, at runtime.
Tenon does not sit inside the model loop deciding what goes in this turn's
window. It makes the thing the loop *does* control into legible, validated,
attributable source.

## The four properties, made concrete

ADR 0018 names the properties Weng identifies as the missing infrastructure of
self-improving agent systems. Each is a concrete tenon mechanism, not an
aspiration:

| Weng's requirement | Tenon mechanism |
| --- | --- |
| File-represented editable components | The agent is a folder — `instructions.md`, `skills/`, `tools/`, `subagents/`, `connections/`. Capability is added by adding a file, never by registering anything ([glossary](glossary.md); [product spec](product-spec.md#the-authored-project)). |
| Bounded editable surfaces | Every surface has an implementation-owned safety ceiling; symlinks are rejected so source cannot escape the project ([product spec](product-spec.md#the-authored-project), "Bounds"). The loop's search space is finite and knowable. |
| Attribution of gains to exact configurations | One source fingerprint per apply; an optional [agent manifest](product-spec.md#agent-manifest) pins harness version, model, tenon version, and package identities. Every apply and dispatch event carries the fingerprint, so an outside observation joins back to the exact configuration that produced it. |
| Permission control outside the loop | Apply, acquisition, trust, and credentials stay deliberate human acts. Nothing mutates a workspace unvalidated; there is no automatic or unreviewed promotion of agent-authored changes ([north star](north-star.md) tenet 5; non-goals). |

A fifth property is implied by the others and matters most for a *cheap* loop:
**well-formedness is validated for the loop the same way it is for a person.**
Harness-updating capability is roughly flat across model sizes (ADR 0018), so
the loop that edits these files is often a small, cheap model. The binding
constraint is therefore not drafting skill but a machine-checkable answer to
"is this revision even valid." Tenon's diagnostics are that answer.

## The outer-loop cycle on tenon

The improvement loop runs the same cycle a person runs, without hands. Mutate
files, prove them valid, apply, run, and attribute the result — then let a
verifier that lives *outside* tenon decide whether to keep the revision.

```text
        ┌──────────────────────────────────────────────────┐
        │  mutate            validate            apply       │
        │  skills/,          --diagnostics        (fingerprint│
        │  instructions.md,  jsonl                recorded)   │
        │  tools/, ...          │                    │        │
        │     ▲                 ▼                    ▼        │
        │     │            stable ids            run / dispatch│
        │     │            authored paths        (fingerprint  │
        │     │                                   on every event)│
        │     │                                        │       │
        │     └────────  verifier + selection  ◀───────┘       │
        │              (OUTSIDE tenon — the loop owns it)      │
        └──────────────────────────────────────────────────┘
```

1. **Mutate.** Edit the authored files — add a skill directory, rewrite
   `instructions.md`, add a typed tool, drop a connection. No registration
   step; the folder *is* the inventory. An empty `instructions.md` is a
   legitimate candidate to try — its optionality exists precisely so the loop's
   search space includes "no always-on prompt" ([product spec](product-spec.md#the-authored-project)).

2. **Validate.** Run `tenon validate . --harness claude --diagnostics jsonl`.
   Each failure is one JSON line with a **stable identifier** and the authored
   path. The loop self-corrects against the identifier, not by parsing prose;
   the identifiers hold across releases and match apply's own failures
   ([product spec](product-spec.md#apply-and-handoff)). This is the cheap-model
   safety rail: a malformed revision never reaches a workspace.

3. **Apply.** `tenon apply` compiles the folder to native harness files and
   records one **source fingerprint** over all authored inputs. Reapplying
   identical source is deterministic; stale or edited generated setup fails
   closed.

4. **Run and attribute.** Whether interactive, headless (`tenon run`), or
   scheduled, every dispatch event carries the source fingerprint and, when
   supplied, the manifest identity. This is the join key: a transcript or score
   collected by the loop's own harness points back to exactly the skills,
   instructions, tools, and pinned closure that produced it.

5. **Verify and select — outside tenon.** The loop's evaluator scores the run
   and decides whether to keep the revision. Tenon retains none of this: no
   transcripts, no evals, no lineage, no selection. A candidate is a source
   revision crossed with a manifest, versioned wherever the loop keeps it
   ([product spec](product-spec.md#agent-manifest)).

## Optimizing skills

A skill is a directory under `skills/` following the open Agent Skills
specification. For the outer loop this shape is the point:

- **Editing is adding and removing files.** The loop grows or prunes the skills
  library by adding or deleting directories — Weng's "skill library" as a
  literal folder. No manifest to keep in sync, no second inventory to drift
  ([product spec](product-spec.md#the-authored-project), "Skills").
- **The edit is bounded and validated.** 256 skills aggregate, ceilings per
  file and per set; a skill whose `SKILL.md` frontmatter `name` does not match
  its directory fails validation with a stable id. The loop learns *why* a
  candidate is malformed without a human reading logs.
- **The gain is attributable.** Because the skill set folds into the source
  fingerprint, "added a `context-compaction` skill and win-rate rose" is a
  claim about a named configuration, not a vibe. That is the honest signal
  Weng's verifier needs, delivered to whatever evaluator the loop runs.
- **Composition is portable.** The same skills library compiles to Claude Code
  and Codex. The loop optimizes one artifact; portability is not another axis it
  has to search.

A worked example of Weng's "evolve the skill of managing context": the loop
authors `skills/context-budget/` whose `SKILL.md` tells the agent how to decide
what to keep in its working context. The loop mutates that skill, validates,
applies, runs a task suite through the native harness, and — using its own
verifier — keeps the version that scored best. Tenon never assembled a context
window; it made the *policy for assembling one* into a legible, attributable
file the loop could iterate on.

## Optimizing context

"Context" splits along tenon's boundary, and keeping the two halves distinct is
what keeps this document honest.

- **Runtime context (the harness's job).** What actually enters the model's
  window this turn — retrieval, pruning, compaction, ordering — is the native
  harness's inner loop. Tenon does not touch it and makes no claim over it.
- **Authored context policy (the loop's job, via tenon).** The durable inputs
  that shape every future window:
  - `instructions.md` — the always-on system prompt. The loop rewrites it,
    shrinks it, or empties it entirely; each variant is a fingerprinted
    configuration.
  - Per-connection `--context` and skill descriptions — the model-facing usage
    text rendered once into generated instructions, telling the agent *when* to
    reach for a tool or skill.
  - Subagent instructions and effort — how work is delegated and split, which
    is itself a context-management strategy (a subagent gets a fresh window).

Mapping this back to Weng's two layers: the loop edits the **outer** layer — the
durable skill and policy for managing context — as tenon files; the harness runs
the **inner** layer — this turn's actual selection — untouched. The separation
is not a limitation to apologize for; it is the reason a cheap loop can iterate
safely. It can only ever change the reviewable, bounded, attributable policy,
never reach inside a running model loop.

## Why attribution is the load-bearing contribution

Weng's sharpest warning is about the verifier: a loop with a weak or ambiguous
signal optimizes the proxy and drifts. Tenon cannot make a verifier strong —
that is squarely outside its scope. What it removes is a *different* failure that
looks the same from the outside: attributing a gain to the wrong change.

Without exact attribution, a loop that measures an improvement cannot be sure
which of its edits, which model, or which harness version caused it — and a
signal you cannot attribute is nearly as useless as a signal you cannot trust.
The source fingerprint and the agent manifest close that gap. Every observation
the loop's evaluator makes joins back to one configuration: these files, this
model pin, this harness version, this tenon version. The verifier stays the
loop's problem; tenon guarantees the verifier is scoring a known, reproducible
target rather than a blur.

Two further guarantees keep the loop from fooling itself:

- **The agent is not told how it was set up.** Tenon never renders the manifest,
  its pins, model identity, or provenance into model-facing content. A revision
  cannot read its own fingerprint and condition on it — no channel to game the
  attribution ([product spec](product-spec.md#agent-manifest)).
- **A pin is an axis, not an editable surface.** The loop may *try* a different
  model or harness version by changing a manifest pin, while the components it
  can *edit* remain the authored files. Model choice and file content stay
  separable axes of the same experiment.

## What tenon deliberately does not do

Stated plainly so the frame is not oversold. Tenon is the substrate; these
belong to the loop, the harness, or the human:

- It does not evaluate, score, rank, or select among revisions.
- It does not retain transcripts, lineage, or a candidate population — version
  control holds those.
- It does not manage the runtime context window, plan, or run the model loop.
- It does not promote an agent-authored change automatically; permission stays a
  deliberate human act.
- It does not claim to enforce instructions, sandbox authored tool code, or make
  model behavior safe from outside the harness.

The measure of the fit is [ADR 0018](adr/0018-add-the-revision-leg-to-the-measure.md)'s
third leg: *a revision applies, runs, and attributes to its exact configuration
without human hands.* Weng describes the outer loop; tenon is the part of it that
has to be true before any of the rest can be trusted.

## Sources

- Lilian Weng, *Harness Engineering for Self-Improvement*, Lil'Log, Jul 2026 —
  <https://lilianweng.github.io/posts/2026-07-04-harness/>
- [ADR 0018](adr/0018-add-the-revision-leg-to-the-measure.md), which records the
  measure amendment against that post.
