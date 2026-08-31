# tenon-improve: known gaps and improvements

Working notes for the companion repo being split out of PR #61 (`fanout/`,
`evolve`, `judge`). Evidence, not contract. Each entry: what, where, why it
matters, and the proposed move.

## Confirmed by experiment

### G1. The validate gate does NOT catch dangling references (high impact)

`recombine`'s docstring claims the gate handles incoherent offspring:

> "The offspring may well be incoherent — a skill referencing a tool that did
> not come along — which is exactly what the validate gate is for."

It does not. Tested against tenon built from `4b416bf`: an agent whose
`instructions.md` cites a nonexistent tool and whose `skills/summarize/SKILL.md`
references a missing `./helper.md` validated clean —

    {"agent":"dangle","fingerprint":"sha256:6ba46103..."} exit=0

`skill.resource.invalid` checks UTF-8, symlinks, regular-file-ness and size
bounds (`internal/agentproject/skills.go:205-240`), not reference resolution.

**What the gate does catch:** malformed frontmatter, invalid tool schemas,
bounds exceeded, `plugin.reference.unresolved`, `mcp.override.dangling` (a mask
naming a server that isn't there), `tool.dependencies.missing`,
`subagent.instructions.missing`.

**Why it matters.** The central cost argument for depending on tenon is
"invalid offspring die before a model is opened." That holds for malformed
genomes but NOT for the incoherent ones uniform crossover produces most often.
Those survive to a paid session.

**Proposed:** a cheap reference linter, in tenon-improve not tenon (tenon
proves contracts, not coherence). Grep each gene's prose for backticked names
and `./` paths; warn on ones no gene provides. Pre-gate, no model. Decide
whether it rejects or only annotates — annotating is safer, since a skill may
legitimately name a harness-native tool.

## Design gaps

### G2. Non-gene files are inherited from `parents[0]` only

`lay_out` (`evolve.py:665-682`) copies README, `go.mod`, `pyproject.toml`,
`uv.lock`, `deno.json` from the first parent, and only when absent. So:

- Dependency manifests and lockfiles are shared infrastructure treated as inert
  baggage. A mutator that adds `tools/foo.py` needing a new dep gets parent 0's
  unchanged `pyproject.toml` — `tool.dependencies.missing` should catch it, so
  it dies cheap, but such a mutation can never *succeed* unless the operator
  also edits files that are not genes and are not listed in `EVOLVE_GENES`.
- Nothing tells an operator those files exist. Its cwd is the genome dir so it
  *can* edit them; it just has no signal that it should.
- "Parent 0 is privileged" is real and undocumented.

**Proposed:** name a `CARRIED` set explicitly, export it as `EVOLVE_CARRIED`
alongside `EVOLVE_GENES`, and document the asymmetry. Optionally a post-combine
repair hook. Do not make lockfiles genes — recombining them is meaningless.

### G3. `lay_out` is a poor name

It is the shared mechanism every combine policy ends in: take a
`{locus: parent index}` plan, put the files where they belong. Reads oddly
beside `recombine` / `materialize` / `copy_gene`. **Proposed:** `assemble`.

### G4. An enumerating combine hook has no ordinal

`propose` calls `combine` once per pair entry with `out_dir = g{gen}-c{i}`, but
`i` is passed only inside the path string. A hook that wants to enumerate
combinations deterministically (child 0 = plan A, child 1 = plan B) must parse
`out_dir` or keep its own state. **Proposed:** pass `index` in the combine
hook's stdin object.

### G5. Pin fusion contradicts the docs

`judge/README.md:110-113` has evolve write one manifest per genome binding the
expected fingerprint, so pin and content vary together. `product-spec.md:459`
says a pin is an axis, not an editable surface. The implementation has voted:
the genome IS tenon's "candidate" (source revision x manifest,
`product-spec.md:460`). **Proposed:** say so, drop the separate-axis claim, and
treat varying the model as a sweep across searches, not an axis within one.

## Vocabulary (from the glossary audit)

Do before the repo goes public — JSON key changes are free now:

- `operators` -> `mutators`. tenon's `operator` is a **person**
  (`--trust operator`, "the operator's infrastructure choice"). Full homonym.
- `generation` -> `round`. tenon's `generation` means compiling native harness
  files ("generation is lossy in reverse"). `judge` already says round.
- `run` is overloaded four ways (tenon dispatch / fanout batch / whole search /
  harness run). Keep `--run` as the identifier; in prose say "a search" or
  "a fan-out".
- `judge/about.html:11-13` calls itself "the substrate" and denies being the
  loop, while scoring. It IS an improvement loop per `glossary.md:21`. Claim it.
- `EVOLVE.md:290` "tenon's gate is syntactic" is wrong — it merges plugin skills
  under precedence, validates tool schemas, accounts budgets.
- Retire `locus` as a separate term OR define it: in the code a locus is the
  dict key (a name), a gene is the content at it. They are not synonyms.
- One thing at three names: `variant` (fanout) / `trial` / `evaluation`.
  Pick `variant`; budget math counts variants.

### Optional: drop the GA metaphor entirely

`genes()`'s own docstring already says "A gene is one authored component."
A biology-free vocabulary that costs nothing:

| GA term | Plain term |
| --- | --- |
| genome | configuration |
| gene | component |
| locus | component path |
| crossover / recombine | merge |
| mutation | edit |
| generation | round |
| fitness | score |
| offspring | candidate |
| population | pool |

Argument for: the metaphor misleads here — there is no genotype/phenotype
split, and genes are never recombined below file level. Argument against: EA
readers lose a familiar map, and `(mu+lambda)` / island / MAP-Elites keep their
literature names regardless.

## Not gaps — deliberate and worth keeping

- Depth-1 gene namespace. `skills/review/` is atomic; crossover explores
  combinations, mutation explores content. Clean split.
- Score belongs to the genome (content-addressed); island/niche belongs to the
  slot. `EVOLVE.md:186-188`. Correct and non-obvious.
- Asexual reproduction always mutates (`propose`: `if operator == "copy" or
  rng.random() < mutation_rate`), so a copy is never wasted on a duplicate.
- Nothing is promoted automatically. A search ends by printing a diff.

## Round 2

### G6. Keep tenon fungible without building the abstraction

Goal: tenon's shape must not over-inform the improvement API. Four disciplines
that cost nothing now and make a substrate interface a refactor rather than a
rewrite. Do these; do NOT build the interface yet (tenet 4).

- Confine every tenon subprocess call to one adapter module. Today they are
  scattered across `fanout.py` (4 call sites) and `evolve.py` (2).
- Treat diagnostic identifiers as opaque tokens. Never branch search logic on a
  specific `id` string.
- `GENE_DIRS` / `GENE_FILES` become spec configuration, not module constants —
  a different substrate has a different component layout.
- Name by role, not by product: gate, identity, compile, dispatch. Not
  `tenon_validate`.

TODO (not now): a substrate interface — gate -> fingerprint, compile ->
workspace, dispatch -> transcript.

### G7. Path-keyed loci are brittle under rename

A locus is the path string, so renaming `skills/review` to `skills/code-review`
reads as one deletion plus one addition. Crossover can then hand a child BOTH
copies: two near-duplicate components, each a full context cost, neither
recognized as the other's descendant.

This is what NEAT's historical markings solve, and `EVOLVE.md:126-129` already
cites historical marking while the code does plain name matching.

**Proposed (80%):** assign a stable component id on first sighting, inherit it
through copy and mutation, and keep the id -> current path map in evolve's run
state keyed by genome id. It must live OUTSIDE the genome directory — a sidecar
inside it would perturb the fingerprint. A rename then reads as a rename.

**Out of scope (100%):** semantic or embedding-based joining of components with
different names and similar content. Record as a known limit.

### G8. Naming corrections

- `merge` is wrong for crossover. Merge implies union with conflict resolution;
  crossover is selection — one parent's version wins, the other is discarded,
  and a locus can be dropped entirely. Keep **crossover** / **recombine**.
- `agent source` is wrong for genome. The genome carries a pin (G5), so it
  equals tenon's **candidate** (source revision x manifest), not agent source.
  Keep **genome**: unambiguous, and the one place the biology metaphor earns
  its keep — content that is scored and inherited.

### G9. Ask tenon for a capability-surface export (highest value)

`tenon manifest write` emits pins only — `schema_version`, `agent`,
`source_fingerprint`, `tenon_version`, `harnesses{harness_version,
tool_runtimes}`. No inventory of what the agent can actually do. Nothing else
in the CLI dumps one (`inspect` is integrations-only).

But tenon must compute the resolved surface to compile, and
[ADR 0024](../adr/0024-add-observation-to-the-revision-leg.md) already puts
"its change to the capability surface legible before it runs" in the measure.
Emitting it is arguably already owed.

**Superseded proposal.** An earlier draft here proposed a new
`tenon surface show` subcommand. That adds an author-facing command where a
field would do. Prefer:

**Proposed:** a `--surface` flag on `tenon validate`. The JSONL stream already
ends with a distinct final object carrying `{agent, fingerprint}`; under
`--surface` that object also carries the resolved inventory — skill names
(including plugin skills merged under precedence), tool names with schemas,
MCP server names, subagents, schedules.

Why this shape and not an expanded manifest: direction. The manifest is an
*input that constrains* — supplied with `--manifest`, verified against reality,
pinning what the directory cannot express. A surface is an *output derived from*
the directory; it adds no information and nothing can disagree with it. Fusing
them makes one file half-authored and half-derived, so a consumer cannot tell
which fields it supplies and which tenon fills, and verification loses meaning
for the derived half. It would also drag a small, stable pinning contract into
churning whenever the authoring convention gains a component type. The
fingerprint already covers the derived part: if the surface changed, the
fingerprint changed.

Why not a new subcommand: zero new commands and schemas (tenet 1); validate
already derives rather than verifies, so the direction is right; and it lands on
the call the consumer already makes — evolve's `gate()` parses exactly this
stream today, so coherence checking costs one flag, not one more process.

Then the G1 coherence check is a set operation instead of prose grepping, and
it serves any consumer. It also gives a diffable answer to "what did this
revision change about what the agent can do" — the ADR 0024 leg.

Still widens a documented output contract, so it wants an ADR — a smaller one
than a new command would need.

### G2 update: bad assembly does die cheap

Confirmed against the built binary: adding `tools/wordcount.py` to an agent
with no `pyproject.toml` / `uv.lock` is rejected before anything runs —

    error: pyproject.toml: tool.dependencies.missing: python tools require
           pyproject.toml at the agent root; none was found

So a dependency-adding crossover or mutation fails the gate rather than burning
a session. The remaining problem is only that such a mutation can never
*succeed*: the manifests are not genes and are not announced to mutators.
Export `EVOLVE_CARRIED` alongside `EVOLVE_GENES`. Do not build dependency
repair — leave it to a post-combine hook.

### G10. Explain the two variation mechanisms (docs TODO)

The README needs this on page one; the algorithm names do not convey it.

|  | recombine (crossover) | mutate (vary) |
| --- | --- | --- |
| Needs | 2+ parents | 1 genome |
| Changes | *which* components a child has | *what is inside* them, or adds/removes one |
| Granularity | whole components | anything, usually sub-file |
| Runs | file copying | a command, usually a model call |
| Costs | nothing | money |
| Invents new material | **no** | **yes** |

Crossover only reshuffles components already present somewhere in the pool, so
mutation is the sole source of new material and crossover is the sole free
operator. Per child, `propose()` gives exactly one of `crossover`,
`crossover+<mutator>`, or `<mutator>` — never a bare copy, since copying a
parent unchanged reproduces its fingerprint.

Two consequences to state explicitly: generation 1 is all mutation (only the
seed exists, nothing to recombine), and the depth-1 gene rule is what keeps the
two from overlapping — crossover works strictly between components, mutation
strictly within them.

### G11. validate vs manifest — the split to respect

`internal/manifest/manifest.go:1-8` states the invariant: the manifest "PINS the
runtime closure the authored directory alone cannot express ... It IDENTIFIES
and PINS; it **never lists components** — the authored directory stays the sole
registry."

So the line is content vs environment, not input vs output (the manifest is
already both: `manifest write` derives one, `--manifest` consumes it):

- **validate** answers "is this definition well-formed" over the authored
  directory — components, schemas, budgets, bounds. Output: fingerprint or
  diagnostic identifiers.
- **manifest** answers "what environment did this run in" — harness binary
  version, deno/uv/go/python runtime versions, integration package identities,
  optional model. Facts the directory cannot express. Verified fail-closed,
  naming the drifted pin.

The fingerprint is the join between them.

A component inventory is derivable from the directory alone, which is the test
for "does not belong in the manifest". Hence G9's `validate --surface`.

Also relevant to G5: `Verify` deliberately ignores the model field — the harness
owns model selection and tenon never checks which model served a turn. A
per-genome model pin is a declaration, not a guarantee.

### G12. Component identity can be deterministic (no classifier)

NEAT's historical markings are pure bookkeeping — a counter incremented when a
structural change first occurs, assigned by the process making the change. No
inference, no model. That property carries over if identity is recorded at the
moment of the edit:

1. **Mutators declare edits** — `{created, renamed, deleted}`. Exact and free.
   The right default: put bookkeeping where the information exists rather than
   reconstructing it later.
2. **Fallback, still deterministic:** exact content-hash match across a
   generation — same bytes at a new path is a rename.
3. Everything else is a new component; accept some redundancy.

An agentic classifier is only needed for renamed-and-rewritten-in-one-step,
which is where identity is a judgment call anyway. If wanted later it is a
policy hook, not core machinery.

Limit, same as NEAT's: innovation numbers are per-run. Component ids would be
too; cross-search comparison still goes through the fingerprint.

### G13. The catalog workflow already exists, split across three commands

A natural guess is that `manifest` should emit a component catalog and
`validate` should diff it against the applied target. Both halves have homes
already, just not those:

| Command | Inputs | Answers |
| --- | --- | --- |
| `validate` | source only | is this definition well-formed? -> fingerprint or diagnostics |
| `drift` | source x workspace | does the applied target still match? -> unchanged / modified / missing / stale |
| `manifest` | closure | what environment does this run in? -> harness, runtime, package pins |

`cmd/tenon/drift.go` already regenerates every tenon-owned file in memory on
apply's own generation path and classifies each owned path — that is the
additive/subtractive diff against the applied target. The missing piece is only
the catalog itself, which is derived from source and therefore belongs with
`validate` (G9).

**Naming finding for tenon.** Everywhere else in software a manifest IS a list
of contents (shipping, package, Android, K8s). Tenon's manifest is the one that
explicitly never lists contents, so the word actively misleads — anyone guessing
from the name guesses wrong. `closure`, `pins`, or `lock` would say what it
does. Recording as an observation, not a proposal; renaming a documented
author-facing artifact needs its own justification.

### G12 revision: ship path-as-id, keep NEAT as a noted alternative

NEAT-style component ids are too much machinery for now. Path as ID is already
implemented, so it is zero work, and the failure mode is survivable at this
scale: a rename costs one redundant component and one wasted evaluation, not a
corrupted search. Whether renames matter at all is unknown until real mutators
are observed.

Ship path-as-id. Note NEAT-style inherited ids and agentic classification as
more sophisticated alternatives, with the rename duplicate as the known limit.

**Cheap insurance, do now:** record each component's content hash per genome in
the run state. If renames turn out to be common, identity can be reconstructed
retroactively from logs already written, instead of re-running the search. Buys
the option without buying the machinery.

### G14. CLI consolidation proposal (tenon-side, needs an ADR)

Empirical finding that reframes G13: `tenon fingerprint show --diagnostics jsonl`
already gates (exit 1 on an invalid project) AND already emits a per-file
inventory — `{path, hash, executable}` per authored file, then the fingerprint.
The "list what is in the source" job is half-built, and it lives in
`fingerprint show`, not `manifest`.

So there are not three overlapping read commands. There is one question asked at
three depths, split across three names:

| Today | Inputs | Adds |
| --- | --- | --- |
| `fingerprint show` | source | gate + file inventory + fingerprint |
| `validate` | source + `--harness` | harness-specific validity |
| `drift` | source + `--harness` + `--workspace` | applied-target diff |

Each is the previous plus one input — a ladder the CLI currently makes an author
climb by switching commands. Tenet 2 is "one ladder, no cliffs".

**Proposed:**

    tenon check AGENT                                    gate -> file inventory + fingerprint
    tenon check AGENT --harness claude                   + harness validity
    tenon check AGENT --harness claude --workspace DIR   + applied-target diff
                      [--catalog]    component-level inventory instead of file-level
                      [--lock PATH]  + pin verification

Depth follows the inputs supplied — the same pattern `--manifest` already uses,
where supplying it widens the check. Alternative name: `inspect` (reads better
for "give me facts") but collides with `integration inspect`.

**Rename `manifest` to `lock`.** It is a lockfile: it pins a runtime closure, is
supplied at application, and verifies fail-closed naming the drifted pin.
Everyone knows what a lockfile is and nobody expects one to list components.
`--manifest PATH` -> `--lock PATH`; `manifest.json` -> `tenon.lock.json`. This
also retires the G13 naming problem: nothing called "manifest" misleads anymore,
and the thing that lists contents is `--catalog`.

**Do not** give the catalog its own command (e.g. `tenon manifest AGENT` meaning
the contents listing). Same load, same gate, same source — a separate command
re-splits what this consolidation merges.

Caveats:

- `lock` has one impurity: `Verify` deliberately ignores the model field (the
  harness owns model selection). A lockfile with an unverified field is false
  advertising — verify it, drop it, or mark it advisory.
- A flag-widened check can be under-run: forgetting `--harness` yields a weaker
  answer that still exits 0. The output must name what it actually checked.

Breaking change to a documented surface, so it needs an ADR — but tenon is
pre-release, which is the cheapest this will ever be.

Consumer impact in this repo: fanout calls `fingerprint show`, evolve calls
`validate` and `manifest write`. All three move; all three are ours.

### G15. What `--catalog` means, and why the file inventory is not enough

`fingerprint show` emits a FILE inventory: `{path, hash, executable}` per
authored file. A catalog is a CAPABILITY inventory. They diverge in three ways:

- A skill is many files but one capability (`skills/review/SKILL.md` plus
  scripts and references -> one entry).
- Plugin skills appear in the catalog but in none of the source's files — they
  are merged in from a vendored or pinned plugin under precedence rules.
- Names and schemas come from file CONTENTS, not paths. `tools/shout.ts`
  declares its own description and Zod input/output schemas;
  `tools/reverse/tool.go` declares `Description` as a Go const. The tool's
  identity cannot be derived from its filename.

That last point is why G1's coherence check needs the catalog specifically:
answering "does this skill reference a tool that exists" requires the tool's
name, which lives inside the file. Grepping paths cannot do it.

### G16. Why the lock cannot be verified by default

Two reasons, one structural and one about cost.

Structural: the product spec puts the manifest outside agent source on purpose —
"the same source directory applies under different manifests — one commit
crossed with many pin sets ... it lives wherever its operator or loop versions
it." There is no canonical location to auto-discover, and giving it one would
collapse the many-pin-sets property that evolve depends on (one manifest per
genome).

Cost: verifying pins resolves the CURRENT closure, which shells out to the
harness for its version and to deno/uv/go/python for theirs. Mandatory
verification would put toolchain requirements on every check and break the
five-minute measure.

**But skipping it should not be silent.** A check that exits 0 without verifying
pins currently looks identical to one that did. Fix it in the output, not the
default: report `pins: not verified` rather than omitting the field, and do the
same for an absent `--harness` or `--workspace`. Then "did I check what I think
I checked" is answerable from the output instead of from recalling one's own
flags. This subsumes the under-run caveat in G14.

### G17. Proposed help text for `check` and `lock` (G14 detail)

House style, from the existing flags: lowercase, terse, no trailing period,
parenthetical qualifiers.

Global usage lines — three replaced by one, plus the rename:

    tenon check AGENT [--harness <claude|codex>] [--workspace DIR] [--lock PATH] [--catalog] [--diagnostics <prose|jsonl>]
    tenon lock write AGENT --harness <claude|codex> [--output PATH] [--verify PATH] [--model VALUE]

`tenon check --help`:

    Usage of check:
      -catalog
            report the resolved capability inventory: skills, tools with schemas,
            MCP servers, subagents, schedules
      -diagnostics string
            diagnostic rendering: prose or jsonl (default "prose")
      -harness string
            also check the source compiles for a target harness: claude or codex
      -lock string
            also verify a supplied lock against the current runtime closure
      -workspace string
            also compare an applied workspace against a fresh generation (requires -harness)

    With no flags, check gates the source and reports its file inventory and
    fingerprint. Each flag adds one further check; the result names every check
    that did not run.

The "also" prefix on every optional flag makes the ladder readable from the help
itself — nobody has to learn separately that drift is validate plus a workspace.
The closing paragraph is the G16 fix: it commits the output to naming what it
skipped, so a bare `check` cannot be mistaken for a full one.

`tenon lock write --help`:

    Usage of lock write:
      -harness string
            harness whose executable version to pin: claude or codex
      -model string
            optional model to record for the selected harness (advisory: operator-supplied,
            never resolved automatically, and never verified — the harness owns model selection)
      -output string
            output path (defaults to stdout)
      -verify string
            optional existing lock to verify against the current closure before writing

Two changes beyond the rename:

- `--manifest` becomes `--verify`. `lock write --lock PATH` reads like an output
  path when it is an input to check first.
- `-model` gains "and never verified". The current text says "never resolved
  automatically", which implies the operator supplies it but leaves the reader
  to assume it is then checked. `Verify` ignores the field entirely.

Open: `lock write` keeps its verb on the assumption a standalone `lock verify`
may want to exist. If `write` stays the only verb, `tenon lock AGENT` is simpler.

### G18. G14 revised — drift keeps its own command

G14 folded `fingerprint show`, `validate` and `drift` into one `check` on the
grounds that each adds one input. That over-unified. The argument that kills it:
`apply` also validates first, and nobody would merge `apply` into `validate`.
"X does Y's work internally" is not a reason to merge commands — it mistakes
*more inputs* for *the same question*.

Sorted by what the answer is about:

| | Subject | Answer |
| --- | --- | --- |
| fingerprint | source | an identity |
| validate | source | a verdict |
| catalog | source | a report |
| drift | source x workspace | a comparison |
| lock | environment | a verdict on a different subject |

The first three are one load, one gate, three projections of one result — those
merge. `drift` changes subject and deserves its name.

    tenon check AGENT [--harness <claude|codex>] [--catalog] [--lock PATH]
    tenon drift AGENT --workspace DIR --harness <claude|codex> [--lock PATH]
    tenon lock write AGENT --harness <claude|codex> [--output PATH] [--verify PATH] [--model VALUE]

Still 2 -> 1, which was the real redundancy. Amend G17's help text accordingly:
`check` loses `-workspace`, and its closing paragraph drops the workspace clause.

**Nothing in check or drift touches runtime.** `--harness` on the source check
asks whether the source can compile to that harness's native configuration —
static; no harness is launched. The workspace comparison reads files apply
already wrote. And the model is never checked anywhere: `Verify` ignores it and
the harness owns model selection, so there is no rung for it at all.

**Two different diffs, two different customers.** This was under-explained in
G15:

- `drift` diffs source against an applied workspace, at FILE level (unchanged /
  modified / missing / stale per owned path). Question: did someone tamper with
  or stale out the applied output? Operator-facing.
- A catalog diff is REVISION against REVISION — `catalog(A)` vs `catalog(B)` —
  at capability level. Question: what did this revision add or remove from what
  the agent can do? Author- and loop-facing, and exactly the ADR 0024 leg. No
  workspace is involved.

G1's coherence check consumes the catalog, not drift.

**Why `--catalog` stays a flag rather than its own command:** an ungated catalog
would be a lie — it would list a tool whose schema does not parse. The catalog is
only meaningful for a source that gates, so it rides on the gate rather than
being obtainable without it.

### G19. The three subjects, and what drift does not report

The clarifying frame: each command binds the source to a different subject.

| | Subject | Question |
| --- | --- | --- |
| `check` | source alone | is this definition sound, and what is it? |
| `drift` | source x workspace | does the applied output still match? |
| `lock` | source x environment | what machine state does this run against? |

`lock write` reads the source (fingerprint, which runtimes are needed) and probes
the machine (harness executable version, deno/uv/go/python versions, integration
package identities). It never touches a workspace and has no `--workspace` flag.
So lock is to the environment what drift is to the workspace: both bind a source
fingerprint to something outside the source that could move underneath it.

**Drift does not report catalog drift.** It classifies tenon-owned PATHS as
unchanged / modified / missing / stale. Deleting
`.claude/skills/review/SKILL.md` from a workspace reports `missing:
.claude/skills/review/SKILL.md` — a lost skill described as a lost file. Drift
never says "the review skill is gone." The event is detected; the vocabulary is
wrong for the question. A `drift --catalog` reporting in capability terms is a
coherent extension (the generated files ARE the compiled surface) but does not
exist. Noting, not proposing.

**`check` is a linter that also issues a certificate.** The linter framing is
right as far as it goes, but check also mints identity: per ADR 0025 the
fingerprint is only emitted for a project that loads and whose tools prepare.
That is what makes a scored run attributable later, and it is why the
fingerprint is not merely a hash of the directory.

### G20. Why `--catalog` is opt-in (it is a versioning choice, not a cost one)

The resolution work is already paid for: gate-proven means plugin skills are
merged under precedence and tool schemas are parsed before any fingerprint is
emitted. Emitting the catalog costs serialization, not computation.

So the flag is about who parses, not what runs. Two real reasons to leave it off:

- A loop gating thousands of candidates. evolve's `gate()` reads exactly two
  fields, `id` and `fingerprint`, and discards the rest; a full inventory per
  candidate is parse overhead for data it throws away.
- Contract stability. The validate success line is documented as a final,
  distinct object; fattening it by default changes what every existing consumer
  parses.

If tenon were greenfield, catalog-by-default is probably right — the work is
already done and it is what the ADR 0024 legibility leg needs. Opt-in is the
compatible path there, and flipping a default later is a far easier ADR than
widening a contract now and walking it back.

### G21. Surface proposal from a cold review (supersedes G14/G17/G18 details)

Independent design pass over the functional responsibilities, told to ignore the
current names and any ADRs. Converged with G18 on keeping the workspace
comparison as its own verb, and went further on two points.

**Writing pins becomes a flag on the gate — the ordering problem disappears.**

    tenon check ./agent --harness claude --write-pins tenon.pins.json --model claude-opus-5

Writing pins needs a proven fingerprint plus a resolved environment; the gate
already produced the fingerprint. A separate `pin` command must either re-run the
gate (so pins can be written from a gate result the user never saw, with an edit
possibly interleaved) or accept a fingerprint by hand — which is the ordering
burden itself. Verification is the symmetric flag, `--pins FILE`, failing closed
on the first drifted pin. `env verify --pins FILE [PATH]` still exists for the
no-source case: verifying a shipped image at boot, or a CI runner checking its
own closure before cloning.

**Gate-and-report is one job, not two.** The inventory's names and schemas come
from file contents, and the gate is what parsed them — the inventory is the
gate's own working set, serialized. Framing: "prove this source and describe the
proven thing"; the description is warranted only by the proof. Enforce in the
output contract: **inventory fields exist only when `ok` is true**, so no
consumer can hold a half-proven inventory. Default `--emit` empty keeps the
loop's success object small.

Hot commands stay one word (`check`, `apply`, `diff`, `run`); the ~18 cold
operations go behind namespaces (`env`, `stage`, `mcp`, `plugin`, `pkg`) so
`tenon --help` stays readable.

**Genuinely missing capabilities:**

- `tenon explain ID` — stable diagnostic identifiers are promised, but nothing
  turns one back into a cause and fix. Rated the highest-value addition, and it
  is a table. The loop needs it to classify a failure as authorial (edit files)
  or environmental (do not spend another revision).
- `tenon schema [NAME]` — emit JSON Schema per jsonl output, so a consumer can
  pin the shape it parses. Without it, "stable machine-readable" has no artifact.
- A specified exit-code contract: source gate failure, workspace drift, pin
  drift, and dispatch failure as distinct codes, so a loop branches without
  parsing.
- `diff` against a nonexistent workspace should classify every owned path as
  missing rather than erroring — a safe universal preflight, and it removes any
  need for `apply --dry-run`.

Also folds `mcp status` and `plugin status` into `--emit caps` (both are filtered
views of the same inventory and can currently report on a source that does not
gate), and renames `--diagnostics` to `--format` since it governs all output.

**Rejected from the proposal:** `drift` -> `diff` (drift names the condition
being checked for; `diff` invites "against what?"), `integration` -> `pkg`, and
`stage` -> `stage build`. Churn without payoff.

### G22. Cold onboarding test of the proposed surface

An agent given only the proposed help text, asked to sequence one improvement-loop
iteration and audit the friction. Findings, worst first.

**The loop needs 6 invocations, 5 subcommands** — not the 3 assumed. Beyond
check/apply/run it needs:

- `env verify` against the PREVIOUS candidate's pins. Pins bind to a source
  fingerprint, and mutation changes the fingerprint, so pins cannot certify that
  the environment held still BETWEEN candidates. Without this, an environment
  move silently makes scores before and after incomparable.
- `drift` TWICE. Before the run, to prove the workspace is exactly the compiled
  config (otherwise the score is not attributable). After the run, to catch the
  agent self-modifying owned files mid-run — the output would then come from
  something that is no longer the fingerprinted configuration.

**Loop correctness lives entirely in the exit codes, which are one unlabeled
line.** Exit 2 = discard the mutation; exit 5 = retry it, never score it.
Conflating them scores infrastructure flakes as bad mutations. A reader who
skims and writes `set -e` corrupts the experiment. The most important contract
in the help is the least prominent thing in it.

**Gap introduced by "inventory fields present only when ok".** On a cold read a
candidate that FAILS check therefore has no fingerprint — so rejected mutations
cannot be attributed by tenon's own identifier, and rejected mutations are data.
Either emit an identity for failed candidates or say plainly that the loop must
hash them itself.

**No output field names are documented anywhere.** Every jq expression the tester
wrote was fabricated. That is the whole integration surface. `tenon schema` is
proposed but the help never says what NAMEs exist.

**`id` means three things on one page:** `--emit id` (source fingerprint),
`explain ID` (diagnostic code), `--input-id` (schedule input). Rename at least
one.

**Two ambiguities to fix regardless:**

- `apply --workspace` defaults to PATH, so the safest-looking invocation compiles
  the source into the source directory — and `--discard-local` then overwrites
  there. Bad default for anyone who has not yet learned what "owned" means.
- Exit 3 "workspace drift" and exit 4 "pin/environment drift" are both called
  drift, and the `drift` command can return either. One discards a candidate;
  the other invalidates the whole experiment.

**Friction:** of ~20 flag tokens per iteration, 3 carry information (candidate
path, input file, timeouts). `--harness` is typed 4x and is a property of the
experiment, not the invocation. Six concepts are mandatory before iteration one
(source vs workspace, file ownership, fingerprint, pin sets, the exit taxonomy,
the input turn format) and the help presents them as peers with no "start here".

**Verdict: too much — but because of concept count, not flag count.**

### G23. The composite belongs in tenon-improve, not tenon

The cold test proposed `tenon iterate PATH --input FILE` doing check -> apply ->
pre-drift -> run -> post-drift, emitting one attributed record with a
`phase_failed` field. Before/after: 6 invocations and ~20 flag tokens become 1
and 3; concepts required before the first iteration drop from six to two.

The objection that a composite hides the proved/ran boundary is answered by
`phase_failed` — the boundary moves into the output instead of the command
structure, which is better, since the loop branches on it programmatically
anyway.

But it does not belong in tenon: sequencing compile and dispatch is
orchestration, which the north star refuses. **That composite is precisely the
adapter module G6 already calls for in tenon-improve.** tenon keeps five sharp
commands; tenon-improve ships the one-call cycle on top. Neither grows the wrong
responsibility, and a loop author never learns tenon's surface at all.

Belongs in tenon instead: a `tenon.toml` resolved upward from PATH carrying
`harness`, `format`, and `workspace` defaults (serves every user, not just
loops), and `--format jsonl` implied when stdout is not a TTY.

### G24. apply's workspace default — keep it, but stop teaching it

`apply --workspace` defaulting to PATH compiles the source into the source
directory, which G22 flagged as a bad default for a newcomer. It is not worth
removing: the five-minute measure depends on it (`tenon apply . --harness claude
&& claude` from an empty directory).

The fix is in the examples, not the flag. The quickstart keeps the default;
**every example after the quickstart applies to a distinct workspace
directory**, so "the agent source and the workspace are independent"
(product-spec.md:352) is taught by demonstration rather than assertion. An
improvement loop must never reuse the source as its workspace, and the docs
should never show a shape it cannot copy.

### G25. Answers verified against the binary

**`run` does NOT apply implicitly.** Tested (this was the cold reader's #3
unanswered question):

    tenon run ./agent --harness claude --workspace ./ws --input jsonl
    tenon run: dispatch: the workspace ./ws carries no claude apply record;
               run tenon apply

`apply` writes `.tenon/apply-claude.json` beside `CLAUDE.md`, `.mcp.json` and
`.claude/skills/`; that record is what `run` requires and what `drift` reads.

Keep this behavior. Tenet 5 makes apply a deliberate act, and a `run` that
applied silently would overwrite a workspace someone is mid-debug on. The error
already names the fix, so the cost is one failed command, once. Document it in
`run`'s help so it is not discovered by failure.

**Pins are fully optional.** Verified: `tenon apply ./agent --harness claude
--workspace ./ws` with no pins writes the workspace normally. Pins matter only
when "this ran against exactly that toolchain" must be checkable. Setting an
agent up needs `apply` and nothing else.

**Config: env var, not a file.** Supersedes the `tenon.toml` half of G23. Use
`TENON_HARNESS` (and `TENON_WORKSPACE` only if it earns it). A config file
format must be designed, documented, versioned, and resolved upward through
directories — machinery for a problem one variable solves (tenet 1). A file
stays an easy later addition; a file shipped early is hard to remove.

**Why `pins` and not `manifest`, restated for the ADR:** `internal/manifest`'s
own doc says "It IDENTIFIES and PINS; it never lists components." Everywhere
else in software a manifest IS a list of contents. The one word guaranteed to
make a reader expect an inventory names the file that categorically refuses to
be one — which is exactly the wrong expectation this project keeps producing.
