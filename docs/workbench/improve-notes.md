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
