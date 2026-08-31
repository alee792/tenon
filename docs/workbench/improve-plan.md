# Plan: tenon surface consolidation and the tenon-improve split

Actionable compaction of [improve-notes.md](improve-notes.md). Evidence lives
there; decisions live here. Where the two disagree, this file is later.

## The idea

**Tenon answers three questions about an agent source, each bound to a different
subject, and every answer says what it did not check.** `check` binds the source
to itself — is this definition sound, and what is it? `drift` binds it to a
workspace — does the applied output still match? `pins` binds it to a machine —
what environment does this run against?

Everything confusing about today's CLI is a symptom of those three subjects
being split across five names, one of which (`manifest`) is the single word in
software guaranteed to make a reader expect the inventory it categorically
refuses to carry. The redesign is not new capability: it puts each answer under
the name of its subject, adds the one output always owed — a capability-level
description of the source, not a file-level one — and stops a bare invocation
from looking identical to a thorough one. Surface work, not identity work.

**tenon-improve is the other half and tenon's hardest customer.** The north star
says the artifact has two authors, a person and an improvement loop, neither
outranking the other. tenon-improve is that second author made real. It owns
everything tenon refuses: sequencing compile and dispatch, judging whether a
recombined agent is *coherent* rather than merely *valid*, scoring, selection.
The relationship is one-directional and stays that way: **tenon exports facts;
tenon-improve composes them.**

## Settled — state, do not re-argue

| Decision | Note |
| --- | --- |
| `manifest` -> `pins`. It pins a closure and never lists components. | G14, G25, G29 |
| No "true manifest". Pins is verified fail-closed; a catalog in it would either fail on every skill edit or be an unmarked unverified half. | G27 |
| The catalog is a flag on the gate, never its own command. An ungated catalog would list a tool whose schema does not parse. | G18, G20 |
| `drift` keeps its own command. "X does Y's work internally" is not a merge argument — `apply` validates too. | G18 over G14 |
| `check` absorbs `validate` and `fingerprint show`. One load, three projections. | G18 |
| `run` does not apply implicitly. Tenet 5; the error already names the fix. | G25 |
| `apply --workspace` keeps its PATH default. The five-minute measure depends on it. Fix the examples. | G24, G26 |
| The apply record stays. Without it `Unowned` and `Modified` collapse and stale files are undetectable. | G26 |
| `TENON_HARNESS`, not a config file. | G25 over G23 |
| Path-as-id in tenon-improve. | G13 over G12 |
| Keep `genome` / `crossover` / `recombine`. | G8 |
| No substrate interface yet. | G6, tenet 4 |

## tenon — sequenced

| # | Item | Size | Breaking |
| --- | --- | --- | --- |
| T1 | **Consolidation ADR.** check absorbs validate + fingerprint show; pins renamed and written by the gate; clean, explain, schema added; outcome contract specified. Must state the invariant the notes never do: **tenon never accepts a catalog as input** — that is what keeps north star #1 true. Also settles two open questions below. | M | — |
| T2 | **Outcome contract** (G33): `outcome` as an authoritative body field, exit codes derived from it. Do this early — tenon-improve's correctness lives here and it is currently one unlabeled line. | S–M | yes |
| T3 | **Ship `tenon check`.** Merge validate + fingerprint show; `--emit {id,files,catalog}`; output names every check that did NOT run. Fold `mcp status` / `plugin status` in — they are filtered views of the same inventory that today can report on a source that does not gate. | L | yes |
| T4 | **`--emit catalog`** — resolved capability inventory: skills incl. plugin skills merged under precedence, tools with schemas, MCP servers, subagents, schedules. Highest-value item: it is the ADR 0024 legibility leg and the only thing making tenon-improve's coherence check possible. | L | additive |
| T5 | **`manifest` -> `pins`**; `--manifest` -> `--pins`; pins written by `check --write-pins`. Keep `env verify --pins FILE` for the no-source case. Mark `--model` advisory, never verified. | M | yes |
| T6 | **Machine contract:** `tenon schema [NAME]`, `tenon explain ID`, and — most urgently — write down the output field names anywhere at all. A cold tester fabricated every jq expression they wrote. | M | additive |
| T7 | **`tenon clean PATH [--harness H] --workspace DIR`.** Bare `clean` resets everything. Not about staleness (apply already prunes) — it is the harness-removal gap and the uninstall story. | S | additive |
| T8 | **Docs rewrite.** glossary, product-spec, ADRs naming the manifest or `--diagnostics`, `run`'s help. Every example past the quickstart uses a distinct workspace. | M | — |
| T9 | **Ergonomics:** `TENON_HARNESS`; `--diagnostics` -> `--format`; disambiguate the three `id`s; `drift` on a nonexistent workspace classifies all-missing rather than erroring. | S | yes |
| T10 | **`check --emit all`** — one regenerated document. Last: weakest-evidenced, cheapest once T4 exists. | S | additive |

T1 gates everything. T2 early despite being unglamorous. T3 precedes T4 and T5.

## tenon-improve — sequenced

| # | Item | Size |
| --- | --- | --- |
| I1 | **Vocabulary sweep before public.** `operators`->`mutators`, `generation`->`round`, one name for variant/trial/evaluation, `lay_out`->`assemble`, retire-or-define `locus`, fix EVOLVE.md:290's false "syntactic gate", let about.html claim being an improvement loop. JSON keys are free now, never again. | S |
| I2 | **Export `EVOLVE_CARRIED`** beside `EVOLVE_GENES`; document that parent 0 is privileged for non-gene files. | S |
| I3 | **Build the tenon adapter module.** Six call sites into one; name by role; diagnostic ids opaque; GENE_DIRS to spec config. **Before tenon's renames land** — then T3/T5/T9 cost one file instead of six. | M |
| I4 | **`iterate` in the adapter:** check -> apply -> pre-drift -> run -> post-drift, one record with `phase_failed`. Do the drifts **in-process against one loaded source** — at search scale two extra process launches plus two full projection regenerations per candidate is the dominant non-model cost. | M |
| I5 | **Coherence linter over `check --emit catalog`.** Set operation, pre-gate, no model. Annotate, never reject. Blocks on T4. | M |
| I6 | **Record per-component content hashes in run state.** Retroactive identity reconstruction if renames turn out to matter. | S |
| I7 | **README page one: the two variation mechanisms** (G10), and that the genome carries its pin and therefore is tenon's candidate. | S |

## Tenet 1 accounting, stated plainly

Commands: -2 (`validate`, `fingerprint show` fold in), +3 (`clean`, `explain`,
`schema`) = **net +1**. Flags: **net +2**. Concepts an author must hold: -1 to -2.

**This is not a subtraction and should not be described as one.** Each addition
answers a documented gap — no way to turn a diagnostic identifier back into a
cause, no artifact behind "stable machine-readable", no uninstall story at all.
If the plan must be cut to hold tenet 1, cut T10 and T6's `explain` first.

## Open questions for T1's ADR

1. **`pins` or `lock`?** The strongest sentence in the naming discussion —
   "everyone knows what a lockfile is and nobody expects one to list
   components" — was dropped without counterargument when `pins` landed. Against
   `lock`: a lockfile connotes resolved transitive dependencies, while this pins
   environment versions; and one field (`model`) is never verified, so the
   fail-closed connotation is not quite honest. Air it once. Cheapest now.
2. **Identity for candidates that fail the gate.** Do not weaken the
   certificate — per ADR 0025 a fingerprint means the project loaded and its
   tools prepared. Instead emit a distinct `source_digest` on failure, a plain
   content hash explicitly not a fingerprint, so rejected mutations stay
   attributable without diluting identity.

## Corrections to the notes

- **Do not build G1's grep-based coherence linter, even as a stopgap.** G15
  proves why: tool names and schemas live in file contents, so grepping
  backticked names false-positives on exactly the case the linter exists to
  catch. Wait for T4.
- **`--format jsonl` implied when stdout is not a TTY (G23) is a hazard.** For a
  tool whose product is a stable machine contract, piping to `head` or a pager
  would silently change the output shape. TTY-sniffing affects prose rendering
  only; use `TENON_FORMAT` if the ergonomics matter.
- **G5's "the implementation has voted" is not an argument.** Use G11's instead:
  `Verify` deliberately ignores the model field, so a per-genome model pin is a
  declaration with no enforcement behind it. Same conclusion, sound reasoning.
- **G20 understates the catalog's cost.** "Serialization, not computation" is
  true per candidate and false in aggregate — marshalling a full inventory for
  thousands of gated candidates that read two fields is measurable hot-path I/O.
  Opt-in is right for a cost reason as well as a versioning one.

## Do not do

**Boundary:**

1. No `tenon iterate` or any composite sequencing compile and dispatch — that is
   orchestration, refused by north star #2. It belongs in tenon-improve (G23).
2. **Never accept a catalog as input, and never let one be hand-edited.** The
   moment it is authored rather than derived it becomes the second inventory
   north star #1 forbids.
3. No coherence linter in tenon. Tenon proves contracts, not coherence (G1).
4. `run` never applies implicitly (G25).

**Structure:**

5. No fusing pins and catalog (G27).
6. No separate catalog command (G18).
7. No folding `drift` into `check` (G18).
8. No replacing the apply record with a projection diff (G26).
9. No removing `apply --workspace`'s PATH default (G24).
10. `--emit catalog` not default-on yet — flipping a default later is easier than
    walking back a widened contract (G20).

**Speculative machinery:**

11. No substrate interface in tenon-improve yet (G6, tenet 4).
12. No NEAT-style inherited ids, no agentic rename classifier (G13).
13. No semantic joining of similarly-named components (G7).
14. Lockfiles and dependency manifests are not genes (G2).
15. No dependency repair inside combine (G2).

**Churn:**

16. No `drift`->`diff`, `integration`->`pkg`, `stage`->`stage build` (G21).
17. No wholesale GA-vocabulary removal (G8).
18. The genome is not "agent source" — it carries a pin, so it is tenon's
    candidate (G5, G8, G11).
19. No `drift --catalog` (G19).
20. The coherence linter annotates, never rejects (G1).
21. No north star amendment. Glossary entry for `catalog`, rename in the
    glossary, one ADR (G29).
