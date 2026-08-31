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
