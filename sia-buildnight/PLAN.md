# SIA Build Night — Plan

Frontier Build Night (Hexo Labs SIA), AWS Builder Loft, Aug 25 2026.
Five challenge environments revealed at 5:30 PM; build sprint 5:50–7:40 PM.

This directory is **branch-isolated experimental scaffolding** for the event.
It is not a Tenon product change and makes no claim on the north star; it is a
separate kit that happens to live in this repo for convenience.

---

## 1. The thesis (what we tell the judges)

> SIA has a strong *generator* (an LLM that proposes directed edits) and **no
> *selector*** — its loop is a blind linear chain: generation *N+1* is always
> derived from generation *N*'s code regardless of score, so a regression
> propagates and the best agent it finds is never even the one it reports.

Our contribution is the missing selector, added **entirely in the scaffold
surface SIA already exposes** (no core edits), plus the observability that lets
the generator diagnose better. We implement **three interchangeable selection
algorithms** behind one interface and a **triage judge** that picks the right
one for the revealed challenge at competition time.

This directly answers the four judging questions:

- *What evidence helps the system diagnose its own failures?* → structured
  trajectory logging + failure taxonomy (§4.1).
- *What architecture allows precise changes without breaking unrelated
  behavior?* → modular seed agent with clean stage boundaries (§4.1).
- *What should persist between iterations?* → the **incumbent** (best-so-far
  agent + score) and an experiment ledger (§4.2).
- *What guidance helps the improvement agent reason?* → the selection protocol
  in the `target_agent.py` module docstring — the one artifact SIA embeds
  verbatim in the feedback prompt every generation (§4.3).

---

## 2. Verified constraints (read from the SIA source, 8c50303)

What we can touch **without forking core** — confirmed in `hexo-ai/sia`:

| Surface | Mechanism | Source |
| --- | --- | --- |
| **Seed / target agent** | `agent_reference` in a target profile → our own file **or directory**; copied into *every* generation | `orchestrator.py:613` `copy_reference_into` |
| **Profiles** | drop-in JSON via `./profiles` or `$SIA_PROFILES_DIR`, no code change | `profiles.py` docstring |
| **Observability** | the target agent writes its *own* `agent_execution.json` / `agent_execution/execution_qN.json` | `_shared/reference_target_agent.py` `MultiTrajectoryLogger` |
| **Run driving** | `sia run --task_dir … --target-agent-profile … --max_gen G --run_id K` (CLI) | `cli.py` |

What we **cannot** override without a fork (so we route around it):

- **Meta / feedback prompt text** — locked, golden-master tested
  (`prompts.py` header). Our guidance rides in via the **`target_agent.py`
  module docstring**, which SIA embeds verbatim in the feedback prompt every
  generation. (The feedback prompt does NOT tell the agent to read the seed
  directory — only the *meta* prompt does, at gen 1 — so a side file like
  `GUIDANCE.md` is invisible to the feedback agent. This was confirmed
  empirically: a small model ignored it.)
- **The scored `evaluate.py`** — frozen ("fixed evaluation harness"). We never
  touch scoring; all observability comes from the agent's own logs.

What the feedback agent sees each generation (this is the whole evidence base):
first 3 trajectories (truncated at `TRAJECTORY_PREVIEW_LIMIT`), a success/fail
count, and `results.json` (`orchestrator.py:_build_feedback_context`). **Design
implication:** put a computed diagnostic *summary* where the first-3 window will
show it.

> ⚠️ **Confirm with challenge leads on the night** (§8): whether re-seeding a
> *new* `sia run` from a previously-evolved agent counts as "produced through
> the SIA loop." Our default mode (below) does not depend on it.

---

## 3. Deployment modes & determinism (keep all options open)

We don't know the night's scenario, so the kit ships every mode and picks at
handoff. All three add the *same* missing selector; they differ only in how much
of it is deterministic vs. model-guided, and in what the environment must allow.

- **Mode A — In-loop, injection-only (default; submittable as a stock `sia run`).**
  The selection protocol lives in the `target_agent.py` module docstring — the
  one artifact SIA embeds verbatim in the feedback prompt every generation. A
  capable feedback model executes hill-climb-with-revert; `sia_history.py`
  computes the incumbent deterministically and hands it over; `context.md` (SIA's
  own score history) and `improvement.md` (the one ledger the prompt lets the
  agent write) are the memory. Zero core edits, zero rules risk.
- **Mode A+fork — In-loop, deterministic (only if the repo is forkable & submittable).**
  A ~30-line `orchestrator.py` patch makes the feedback agent *provably* seed from
  the incumbent (ready-to-apply reference in `fork/`). Removes the dependence on
  model compliance.
- **Mode B — Outer driver (offline scouting only; NOT submitted).** `orchestrate.py`
  drives many `sia run`s on our own machine to discover which hypotheses / seed
  structures help; we fold the winners into the Mode-A scaffold we submit. Needs
  no ruling because it never touches the submission.

Determinism, honestly — the hard boundary is that the feedback agent is the only
writer of the next generation's code, so *enforcing* the branch needs the fork:

| Layer | Deterministic without a fork? |
| --- | --- |
| Observability + diagnostic summary (the evidence the agent sees) | ✅ always, sandbox-proof |
| Incumbent *computation* (`sia_history`) | ✅ when the run can read sibling gens (sandbox=none, the default); else falls back to `context.md` |
| Incumbent *enforcement* (that gen N+1 is actually seeded from it) | ❌ needs Mode A+fork; injection-only relies on a capable feedback model obeying the docstring |

The triage judge (§6) picks the mode + algorithm from what the night allows.

---

## 4. The pre-written kit (challenge-agnostic — write ALL of this before 5:30)

Everything here is generic across "code / scientific reasoning / professional
knowledge" challenges. Only the thin task-shaped glue is deferred to handoff.

### 4.1 Seed agent — `kit/seed_agent/`
A modular, heavily-instrumented reference `target_agent.py` the meta/feedback
agent studies and edits. Structured for *precise* edits:

- clean stage boundaries: `load → plan → solve(sample) → format → write`, each a
  small documented function the feedback agent can edit in isolation;
- `--dataset_dir` / `--working_dir` CLI contract (matches orchestrator);
- writes a submission file **and** rich per-sample trajectories via our logger;
- a task-model client seam (`solve_one`) left as the one obvious hook for the
  improvement agent to iterate on.

`kit/seed_agent/observability.py` — the logging library:
- `TrajectoryLogger` (multi-sample, one file per sample, matching SIA's
  `agent_execution/execution_qN.json` convention);
- a **failure taxonomy**: every sample tagged `{stage, error_class, expected,
  got, confidence, latency_ms}` rather than a raw message dump;
- `write_diagnostic_summary()` — computes aggregate failure modes and writes
  them into the first trajectory slot so the feedback agent always sees them.

### 4.2 Persistence — the **incumbent** + ledger convention
> **Source finding (matters):** `copy_reference_into` (`agent_reference.py:126`)
> copies the *pristine* seed dir into every generation, so a ledger/incumbent
> file placed in the seed dir is **reset each generation**, and the locked
> feedback prompt bars the feedback agent from writing files other than
> `target_agent.py` + `improvement.md`. Persistence therefore differs by mode:
> - **Mode A / A+fork (in-loop):** the sanctioned score history is `context.md`
>   (SIA writes every generation's score + deltas there) and per-gen
>   `improvement.md` (the feedback agent's ledger, using the fixed schema in the
>   `target_agent.py` docstring). `sia_history.py` recomputes the incumbent
>   deterministically from `../gen_*/results.json` when the run can see siblings.
> - **Mode B (offline scouting, our own process):** `incumbent.json` +
>   `ledger.jsonl` live at the run-root workspace, owned by `orchestrate.py` —
>   free to write because it is not the sandboxed in-loop agent.

Ledger schema (all modes): `{gen, hypothesis, edit_summary, score,
delta_vs_incumbent, accepted}` — the experiment history the judges grade and the
strategy-bandit reads from. In-loop it is expressed as `improvement.md` blocks;
offline as `ledger.jsonl` lines.

### 4.3 Selection protocol — the `target_agent.py` docstring (NOT a side file)
The binding guidance lives in the **module docstring of `target_agent.py`**,
because that file is the only artifact SIA embeds verbatim in the feedback prompt
every generation. A separate `GUIDANCE.md` is invisible to the feedback agent
(the prompt never references the seed dir) and was empirically ignored by a small
model — it is **removed**. Verified working with a capable meta model. The
docstring encodes, as instructions the feedback agent follows:
1. **Find the incumbent** — prefer the deterministic `incumbent` field in the
   diagnostic (from `sia_history`); else read scores from `context.md`.
2. **Never build on a regression** — if the handed generation scored below the
   incumbent, edit `<run_dir>/gen_<M>/target_agent.py` (the incumbent) instead.
3. **Change one hypothesis** (families listed in the docstring); keep it local to
   one stage.
4. **Preserve** the CLI contract, the logging, the incumbent surfacing, and the
   docstring itself.
5. **Record** the decision in `improvement.md` using a fixed schema — the ONE
   ledger the locked prompt permits, and one SIA folds into `context.md` next gen.

The forbidden `../ledger.jsonl` / `incumbent.json` writes are gone from the
in-loop path (the prompt bars the feedback agent from writing extra files); that
file-based ledger/incumbent store now lives only in `algorithms/` for Mode B and
the fork. `DESIGN.md` orients the meta agent at gen 1 (which *does* read the seed
dir) and points it at the docstring as the source of truth.

### 4.4 Profiles — `kit/profiles/`
`target-buildnight.json` (points `agent_reference` at `kit/seed_agent/`) and a
`meta-buildnight.json`. Model/provider fields are filled from the setup kit
credentials at handoff (deferred — see §7).

### 4.5 Outer driver — `kit/orchestrate.py` (Mode B, offline scouting)
CLI wrapper: maintain incumbent across runs, launch N `sia run`s (beam width),
parse `runs/run_*/gen_*/results.json`, promote the argmax, repeat until the time
budget. Reuses the exact accuracy-parsing from SIA's `context_manager.py`
(handles `"48.99%"` strings).

### 4.6 Algorithm library — `algorithms/` (§5)

### 4.7 Triage judge — `kit/triage.py` (§6)

### 4.8 Harness checks — `kit/preflight.sh`
Verifies `sia` importable, credentials present, a trivial `sia run --max_gen 1`
completes, and our profile resolves. Run during the 5:00 PM setup window.

---

## 5. The three algorithms (one interface, `algorithms/base.py`)

**Scope note.** A single stock `sia run` (Mode A) is inherently *single-track*:
SIA owns the generation loop, so the in-loop selector is greedy hill-climb (or
annealed) encoded in the docstring, plus the bandit for hypothesis choice. The
`algorithms/` library is the **executable reference** for that logic and the
engine for Mode B (offline scouting) and the fork — it is what the docstring
protocol mirrors, so our numbers match across modes. **Parallel beam (width > 1)
is Mode B / fork only**; it cannot run inside one `sia run`.

Common interface: given the ledger + the set of evaluated candidates so far, a
selector answers two questions — **which agent to seed the next edit from**, and
**accept or reject** the last candidate.

```python
class Selector(Protocol):
    def seed_from(self, ledger, candidates) -> AgentRef: ...
    def accept(self, candidate, incumbent, noise_margin) -> bool: ...
    def next_hypothesis_hint(self, ledger) -> str | None: ...
```

1. **`beam_hill_climb.py` — Steepest-ascent hill climbing (PRIMARY).**
   Seed from the incumbent; each round propose K candidates, keep the best B
   (beam). Accept only past a **noise margin**. Most sample-efficient; the right
   default for expensive, noisy evals with few affordable generations. Directly
   the missing selector.
2. **`annealed.py` — Simulated-annealing-lite (LOCAL-OPTIMA HEDGE).**
   Single track; occasionally accept a slightly-worse candidate with a decaying
   probability to escape deceptive local optima. For a tight eval budget where a
   beam is unaffordable but the landscape looks bumpy.
3. **`strategy_bandit.py` — UCB bandit over *hypothesis classes* (META-LAYER).**
   Arms = edit families ("harden output parsing", "add self-consistency
   voting", "retry on malformed", "restructure retrieval", …). Allocates the
   next hypothesis toward families that have paid off in the ledger. Governs
   *what kind* of neighbor to propose; composes on top of (1) or (2). This is
   our originality differentiator and the direct answer to "what can experiment
   history teach us about the system."

All three read/write the same ledger + incumbent, so switching is a one-line
config change and the experiment history is comparable across them.

---

## 6. The handoff judge (picks the algorithm at competition time)

`kit/triage.py` — run once, right after the challenge is revealed and preflight
passes. It **probes the revealed environment** and emits a recommendation +
the night-of prompt.

Probe (cheap, ≈2 baseline runs):
- **eval wall-clock** — how long one candidate evaluation takes;
- **score noise** — variance across 2 identical baseline runs (seed if
  possible);
- **parallelizability** — does the sandbox allow concurrent runs;
- **affordable generations** — `time_budget / (eval + edit) time`.

Decision table (encoded, not vibes):

| Observation | Recommendation |
| --- | --- |
| repo forkable & submittable | **Mode A+fork** — deterministic incumbent seeding |
| stock `sia run` only, capable model, low noise | **Mode A**, greedy hill-climb docstring |
| stock `sia run` only, high noise | **Mode A**, annealed variant + large noise margin |
| ≥ ~15 affordable gens, edit-family matters | layer the **strategy-bandit** hypothesis choice |
| cheap eval **and** parallel runs allowed | also run **Mode B offline** to scout, fold wins into the seed |

Output: a filled-in `HANDOFF.md` naming the mode, algorithm, hyperparameters
(noise margin, cooling, bandit on/off), the exact `sia run` command (and, for the
fork, the `git apply` line), and the docstring-protocol variant to load into the
seed. That doc is the single thing we hand to the build agent at 5:50.

> This triage step is itself a submission highlight: "we don't guess the search
> strategy, we measure the environment and let a judge select it."

---

## 7. Pre-written vs. deferred-to-handoff

**Pre-write fully (before 5:30):** everything in §4 and §5; the triage probe
and decision table; the HANDOFF.md template; preflight; unit tests for the
algorithm interface and ledger/incumbent logic against a **fake `sia run`** (no
credentials, mirrors the repo's own test posture).

**Deferred to handoff (thin, prompt-driven):**
- the task-shaped glue in the seed agent: dataset filename(s), submission
  format, the `solve_one` body — filled by the build agent from the revealed
  `task.md`;
- model/provider IDs + API keys from the setup kit (`kit/profiles/*.json`);
- running triage → choosing the algorithm → launching the loop.

**Handoff prompts** (`kit/prompts/`), pre-written so we paste, not compose:
- `P1_ingest.md` — "read the revealed `task.md`, dataset, and `evaluate.py`;
  report the submission contract and dataset shape; do NOT solve the task."
- `P2_wire_seed.md` — "fill `solve_one` / loader / formatter in the seed agent
  to satisfy that contract, keeping all logging and stage boundaries intact."
- `P3_run_triage.md` — "run `kit/triage.py`, paste its HANDOFF.md, and start the
  chosen loop."
- `P4_supervise.md` — "watch `context.md` / `improvement.md`; confirm the feedback
  agent is branching from the incumbent (docstring protocol); if the loop stalls
  N gens, switch to the triage's second-choice algorithm."

---

## 8. Open questions to resolve on the night (with challenge leads)

1. Is re-seeding a new `sia run` from a previously-evolved agent allowed
   (enables Mode B offline scouting)? If no → skip Mode B; Mode A/A+fork still ship.
2. May the seed agent emit extra diagnostic files beyond the submission, and are
   any output-size caps enforced?
3. Concurrency: are parallel `sia run`s / parallel evals permitted on the
   provided compute?
4. Is the provided repo forkable **and submittable** (enables Mode A+fork — the
   deterministic incumbent-seeding patch, ready in `fork/`), or scaffold-only?

Answers set the mode (§3) and algorithm (§6). The kit runs under any ruling:
stock `sia run` → Mode A; forkable → Mode A+fork; either way Mode B can scout
offline. This is why we keep all three paths open rather than betting on one.

---

## 9. Night-of runbook

- **5:00** arrive, `kit/preflight.sh`, fix env at the setup desk.
- **5:30** challenge reveal → pick our challenge.
- **5:50** `P1_ingest` → `P2_wire_seed` (target ~20 min to a running gen-1).
- **~6:15** `P3_run_triage` → HANDOFF.md → launch loop.
- **6:15–7:30** `P4_supervise`; keep the ledger clean; let it climb.
- **7:30** freeze incumbent; assemble submission: final agent, ledger
  (experiment history), baseline vs best score, the one-paragraph central
  insight (§1).
- **7:40** submit.

---

## 10. Repo layout

```
sia-buildnight/
  PLAN.md                     ← this file
  kit/
    seed_agent/               ← reference dir (copied into every generation)
      target_agent.py         ← modular seed; docstring carries the protocol (§4.1, §4.3)
      observability.py        ← TrajectoryLogger + diagnostic summary (§4.1)
      sia_history.py          ← deterministic incumbent computation (§4.2)
      DESIGN.md               ← gen-1 orientation for the meta agent (§4.3)
      requirements.txt        ← seed runtime deps
    profiles/                 ← drop-in SIA profile JSON (§4.4)
    prompts/                  ← P1–P4 handoff prompts (§7)
    orchestrate.py            ← outer driver, Mode B offline scouting (§4.5)
    triage.py                 ← handoff judge (§6)
    preflight.sh              ← setup-window checks (§4.8)
    HANDOFF.md.tmpl           ← template triage fills in
  algorithms/                 ← selector reference for Mode B + the fork (§5)
    base.py                   ← Selector interface + ledger/incumbent
    beam_hill_climb.py        ← (beam = Mode B / fork only)
    annealed.py
    strategy_bandit.py
  fork/                       ← Mode A+fork reference (apply only if permitted)
    FORK_PATCH.md
    orchestrator_incumbent_seed.patch
  tests/                      ← credential-free, fake `sia run`
```
