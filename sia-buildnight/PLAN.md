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
- *What guidance helps the improvement agent reason?* → the guidance doc that
  encodes the selection rule the feedback agent executes (§4.3).

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
  (`prompts.py` header). Our guidance rides in via the **seed directory**,
  which the feedback prompt tells the agent to read.
- **The scored `evaluate.py`** — frozen ("fixed evaluation harness"). We never
  touch scoring; all observability comes from the agent's own logs.

What the feedback agent sees each generation (this is the whole evidence base):
first 3 trajectories (truncated at `TRAJECTORY_PREVIEW_LIMIT`), a success/fail
count, and `results.json` (`orchestrator.py:_build_feedback_context`). **Design
implication:** put a computed diagnostic *summary* where the first-3 window will
show it.

> ⚠️ **Confirm with challenge leads on the night** (§8): whether re-seeding a
> *new* `sia run` from a previously-evolved agent counts as "produced through
> the SIA loop." Our default posture (below) does not depend on it.

---

## 3. Two postures, chosen at handoff

- **Posture A — In-loop selector (default, safest).** Our selection rule ships
  as guidance + memory files in the seed directory. The feedback agent executes
  the hill-climb-with-revert itself, inside a single `sia run`. Zero outer
  scripts, zero rules risk: the gain is produced by the loop.
- **Posture B — Outer driver (if re-seeding is permitted / evals are cheap &
  parallel).** `orchestrate.py` runs many short `sia run`s, keeps the incumbent
  across runs, and fans out a beam. Higher ceiling, needs the §8 ruling.

We pre-build both. The triage judge (§6) recommends which to use.

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
> file placed in the seed dir is **reset each generation**. Persistence therefore
> works differently per posture:
> - **Posture B (outer driver):** `incumbent.json` + `ledger.jsonl` live at the
>   run-root workspace (outside gen dirs), owned by `orchestrate.py` — never
>   clobbered. `incumbent_agent.py` is copied into the seed dir before each run.
> - **Posture A (in-loop):** there are no persistent seed-dir data files; the
>   feedback agent **reconstructs** the incumbent by scanning `../gen_*/results.json`
>   for the max score and appends to a run-root `../ledger.jsonl`. GUIDANCE.md
>   spells out the reconstruction.

Ledger schema (both postures): `{gen, hypothesis, edit_summary, score,
delta_vs_incumbent, accepted}` — the experiment history the judges grade and the
strategy-bandit reads from.

### 4.3 Guidance — `kit/seed_agent/GUIDANCE.md`
Lives *inside* the seed/reference dir so it rides into every generation and the
feedback agent reads it. It is
The selection rule, written *as instructions the feedback agent will follow*:
1. Read `incumbent.json` and `ledger.jsonl` before editing.
2. If the last generation regressed vs the incumbent → **revert** to
   `incumbent_agent.py` and try a *different* hypothesis class.
3. If it improved → promote it to the incumbent.
4. Record `{hypothesis, edit_summary, score, delta, accepted}` to the ledger.
5. Change one hypothesis at a time; keep edits local to one stage.

The active algorithm (§5) is injected by swapping the algorithm's guidance
block into this file at handoff.

### 4.4 Profiles — `kit/profiles/`
`target-buildnight.json` (points `agent_reference` at `kit/seed_agent/`) and a
`meta-buildnight.json`. Model/provider fields are filled from the setup kit
credentials at handoff (deferred — see §7).

### 4.5 Outer driver — `kit/orchestrate.py` (Posture B)
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
| cheap eval **and** parallel sandbox | Posture B outer driver, **beam** width 3–4 |
| expensive eval, low noise | Posture A, **greedy hill climb** (beam=1) |
| expensive eval, high noise | Posture A, **annealed** + large noise margin |
| ≥ ~15 affordable gens, signal that edit-family matters | layer **strategy-bandit** on top |

Output: a filled-in `HANDOFF.md` naming the posture, algorithm, hyperparameters
(K, B, noise margin, cooling), the exact `sia run` / `orchestrate.py` command,
and the guidance block to drop into `GUIDANCE.md`. That doc is the single thing
we hand to the build agent at 5:50.

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
- `P4_supervise.md` — "watch the ledger; enforce the guidance rule; if the loop
  stalls N gens, switch to the triage's second-choice algorithm."

---

## 8. Open questions to resolve on the night (with challenge leads)

1. Is re-seeding a new `sia run` from a previously-evolved agent allowed
   (Posture B)? If no → Posture A only.
2. May the seed agent emit extra diagnostic files beyond the submission, and are
   any output-size caps enforced?
3. Concurrency: are parallel `sia run`s / parallel evals permitted on the
   provided compute?
4. Is the provided repo forkable (could we edit `orchestrator.py` for the clean
   incumbent-seeding patch), or scaffold-only?

Answers set posture (§3) and algorithm (§6). The kit runs under either ruling.

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
      target_agent.py         ← modular instrumented seed (§4.1)
      observability.py        ← TrajectoryLogger + diagnostic summary (§4.1)
      GUIDANCE.md             ← selection rule the feedback agent runs (§4.3)
      requirements.txt        ← seed runtime deps
    profiles/                 ← drop-in SIA profile JSON (§4.4)
    prompts/                  ← P1–P4 handoff prompts (§7)
    orchestrate.py            ← outer driver, Posture B (§4.5)
    triage.py                 ← handoff judge (§6)
    preflight.sh              ← setup-window checks (§4.8)
    HANDOFF.md.tmpl           ← template triage fills in
  algorithms/
    base.py                   ← Selector interface + ledger/incumbent (§5)
    beam_hill_climb.py
    annealed.py
    strategy_bandit.py
  tests/                      ← credential-free, fake `sia run`
```
