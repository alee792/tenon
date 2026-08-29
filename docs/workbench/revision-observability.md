# Revision observability

- Status: exploration record; design direction, not yet an accepted
  contract. The north star amendment it argues for is proposed separately
  in [ADR 0024](../adr/0024-add-observation-to-the-revision-leg.md), and
  the mechanism in
  [ADR 0025](../adr/0025-make-the-fingerprint-the-unit-of-revision-identity.md).
- Last verified: 2026-08-28

## The question

An improvement loop is two loops. The inner loop is an agent inside a
harness editing its own `instructions.md`, `skills/`, and `tools/`. The
outer loop drives that inner loop, decides which revisions to keep, and
attributes observed behavior to the configuration that produced it.

Tenon serves the inner loop well: `tenon validate` gates well-formedness
before anything runs, and `tenon apply` records a source fingerprint. The
outer loop is served much worse. It can see *that* a revision happened and
*that* it was well-formed, but nothing tenon offers tells it **what the
revision changed about the agent**. For that the outer loop reaches for
`git diff` and shell.

The question is whether it should — whether tenon can serve the outer loop
better than raw git and bash, or whether that reach is correct and tenon
should stay out of it.

## What git already does, completely

Git versions and diffs the authored folder. It is content-addressed,
deterministic, universally understood, and already in every workspace we
care about. `computeFingerprint` (`internal/agentproject/agentproject.go`)
sorts authored inputs by path and hashes path, length, content hash, and
executable bit into one rollup — which is, to a first approximation, a git
tree hash reimplemented.

So the honest starting position is: **for diffing authored source text, git
wins and tenon should not compete.** Any proposal here that amounts to
reimplementing `git diff` over the same bytes is subtraction's opposite
(tenet 1) and should be rejected on sight.

The interesting question is narrower: what does the outer loop need that is
not a function of the bytes alone?

## Where git structurally cannot reach

Four gaps, in increasing order of how much they matter.

**1. Git identity requires a commit and a clean tree; the inner loop has
neither.** Apply already records `git_commit` best-effort, and its own
doc comment states the condition: recorded "only when the source sits
inside a git repository with a clean working tree at apply time"
(`internal/apply/apply.go`). An inner loop mid-revision has an uncommitted,
dirty tree by construction. Git identity is therefore unavailable at
precisely the moment the RSI system needs to name what it is about to run.
The fingerprint is defined on the working tree at every instant, with no
commit, no staging, and no ceremony.

**2. Git hashes trees that cannot run.** `tenon fingerprint show` runs the
same tool-preparation gate as validate and apply — a project whose tools
cannot be built never reports a fingerprint. Git will happily name a broken
revision, and an outer loop that scores by git SHA has no structural
guarantee that the thing it scored was ever a valid agent.

**3. The runtime closure is not in the tree.** The same tree behaves
differently under a different harness version, model, or installed-package
identity. Git has no opinion about what interpreted the files. This is what
the agent manifest already exists to pin, and it is the difference between
"revision 41 scored worse than 40" and "revision 41 scored worse than 40,
and also the harness updated between them."

**4. The effective agent is not the folder.** This is the decisive one.
`Project.Skills` is not `ls skills/`: it merges root `skills/` with every
valid plugin's imported skills, and root skills always win a name
collision, and among plugins the lexically first plugin and skill directory
wins (ADR 0009). Tools carry validated schemas. Connections, subagents, and
schedules each have their own acceptance rules. To learn the effective
capability surface from a textual diff, the outer loop would have to
reimplement tenon's merge, precedence, and validation rules — and stay in
sync with them across releases. That is not a diff the outer loop can
correctly compute from bytes. Tenon already computes it, on every load.

## Textual churn is not capability change

The two are only loosely correlated, and the correlation fails in both
directions. Rewording an instruction body produces a large textual diff and
zero capability change. Adding one line to `mcp/` produces a
one-line diff and gives the agent a whole new MCP server. Reordering
frontmatter keys or reflowing a skill's prose is textually noisy and
semantically null.

An outer loop attributing a score change to a revision wants the
independent variable it actually manipulated. Feeding it `+47 −12 across
four files` is feeding it noise with a signal buried in it. The delta it
wants reads more like:

```
skill  code-review   added
tool   fetch_issue   schema changed: +repo (required)
mcp    github        removed
instructions          4.1KB → 9.7KB (budget 71% → 94%)
```

Tenon can produce that and git cannot, because tenon is the thing that
knows what a skill, a tool schema, and a budget are. Budgets are the
clearest case: ADR 0013 bounds authored projects with aggregate budgets, so
tenon can report that a revision moved the agent from comfortable to nearly
bounded. Nothing in a git diff knows a budget exists.

## The lineage line

`AGENTS.md` places lineage tracking out of scope unless re-decided by ADR,
alongside evaluations, scoring, transcript retention, and selection among
revisions. Everything above brushes against that boundary, so the line
needs stating precisely rather than eroding.

The distinction that holds: **tenon mints the unit of lineage; the outer
loop composes the chain.** A fingerprint names one configuration. A stable
diagnostic identifier names one way a configuration is malformed. A delta
names the distance between two configurations, computed on demand as a pure
function of both. None of those is a history. Tenon stores no sequence of
revisions, no parent pointers, no scores, and no transcripts, and it never
decides which revision supersedes which.

That is not a weakening of the non-goal — it is what makes the non-goal
affordable. An outer loop needs a trustworthy atom far more than it needs
tenon's opinion about chains, and every RSI system will want a different
chain. Supplying the atom and refusing the chain is the smaller commitment,
not the larger one.

## Why the measure has to change first

The north star's measure says a revision "applies, runs, and attributes to
its exact configuration without human hands." Three verbs, all of which
tenon serves. None of them is *observe*.

That absence is why revision observability keeps reading as a nice-to-have
rather than a gap: no leg of the measure is failing, because no leg asks
for it. Meanwhile north star 1 already promises a folder a person can
"read, review, and diff" — the promise exists, but only in its textual
sense, and only for the human author. ADR 0018 established that the loop is
a coequal author owed what the person is owed. A person diffs the folder
and understands it because they know what a skill is. The loop diffs the
folder and does not.

So the honest sequence is: amend the measure to name observation
([ADR 0024](../adr/0024-add-observation-to-the-revision-leg.md)), then
build the mechanism against the amended measure
([ADR 0025](../adr/0025-make-the-fingerprint-the-unit-of-revision-identity.md)).
The north star's own rule requires that separation — it changes "only
through a dedicated ADR naming the evidence that changed, never in the same
change that benefits from the amendment."

## Capturing the edge

Lineage is cheap to keep and impossible to reconstruct, so the sequence
matters more than the storage. An outer loop driving one revision:

```sh
# 1. Name the parent BEFORE the inner loop touches anything.
parent=$(tenon fingerprint show "$AGENT" --diagnostics jsonl | tail -1 |
         jq -r .fingerprint)

# 2. Let the inner loop mutate the source. Tenon is not present for this.

# 3. Gate the result. A revision that fails here has no fingerprint, so
#    record it by the identifiers that rejected it and stop.
if ! rejected=$(tenon validate "$AGENT" --harness claude \
                  --diagnostics jsonl); then
  record_rejection "$parent" "$(jq -rs 'map(select(.id).id)|unique' \
                                 <<<"$rejected")"
  exit 0
fi

# 4. Name the child, apply, and record the edge.
child=$(tenon apply "$AGENT" --harness claude --diagnostics jsonl |
        tail -1 | jq -r .fingerprint)
record_edge "$parent" "$child"
```

The whole discipline is step 1. Steps 3 and 4 read identities tenon hands
back anyway; step 1 is the only one that captures something tenon will
never be able to supply again, because after step 2 the parent tree no
longer exists anywhere. A loop that fingerprints only after mutating has
well-formed nodes and no edges, and no later inspection recovers them.

Two details worth getting right. The rollup arrives on the stream's final
line, which is shaped differently from the per-file entries — it carries
`fingerprint` and no `path` — so `tail -1` is reading a documented shape
rather than guessing. And a rejected revision is still a node: recording
the parent alongside the identifier set is what distinguishes "the loop
explored this direction and it was malformed" from "the loop never tried
it," which an outer loop scoring exploration wants to tell apart.

Everything above is the consumer's code, not tenon's. Tenon's obligation
is only that the identities are there to capture, which
[ADR 0025](../adr/0025-make-the-fingerprint-the-unit-of-revision-identity.md)
states as the four properties of the unit.

## What this does not argue for

- Not a replacement for git. The outer loop should keep using git for
  history, branching, and textual review. The delta is a semantic overlay,
  not a version control system.
- Not lineage, scoring, selection, or transcript retention. Those stay out
  per `AGENTS.md`.
- Not a new persona. The delta is one more thing the same author asks about
  the same folder (tenet 2).
- Not a large subsystem. If the mechanism cannot be built as a pure
  function over the already-parsed `Project` model, it is too much
  machinery for what it buys (tenets 1 and 3), and this record should be
  rejected rather than scaled up.
