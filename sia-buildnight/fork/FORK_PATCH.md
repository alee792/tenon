# SIA Fork — Incumbent-Seeded Feedback (the missing selector)

> ## ⚠️ APPLY ONLY IF FORKING IS PERMITTED AND SUBMITTABLE
> This document + `orchestrator_incumbent_seed.patch` are an **optional** fork of
> the SIA framework. Apply it **only** if the Build Night scenario explicitly
> allows forking the SIA repo *and* accepts a forked framework as a submission.
> If forking is not allowed, **ignore this entire `fork/` directory** and use the
> injection-only kit instead — nothing else in `sia-buildnight/` depends on it.
>
> **Why we might NOT be allowed:** the standard SIA task ships a fixed
> `orchestrator.py` and only lets us evolve the *target agent* (`target_agent.py`)
> / the seed reference. If the harness pins the framework (installed from a wheel,
> pinned commit, or graded by re-running upstream `sia run`), a framework patch is
> out of scope and would be rejected. In that case the selector must live inside
> the agent code we *are* allowed to change (the injection kit), not here.

---

## 1. Summary (one paragraph)

SIA has a **generator** (an LLM feedback agent proposing directed edits) but **no
selector**: generation `N+1` is always derived from generation `N` regardless of
score, so a regression at gen `N` is carried forward and the best agent ever found
is never the one the next edit starts from (upstream only computes a "best_gen" for
the final *summary*, and never feeds it back into the loop — see
`context_manager.py` `finalize`, lines 276–289). This fork adds the one missing
step: **seed the feedback agent from the incumbent (best-scoring prior generation)
instead of the current generation**, and record an accept/reject decision. It is
minimal and low-risk because it changes exactly **one code path** in one file
(`orchestrator.py`), touches **no prompt text** (so the golden-master prompt tests
still pass) and **no scoring** (it *reads* `results.json` using SIA's own score
semantics), adds **no new imports or dependencies** (stdlib only), and **collapses
to upstream behavior** whenever no generation has a score. It is the deterministic
equivalent of what the injection-only kit does via a docstring protocol.

---

## 2. The subtlety we verified: eval runs BEFORE feedback

**Claim:** at feedback time for gen `N`, `results.json` exists for gens `1..N`, so
the incumbent over `1..N` is well-defined.

**Verified by reading `run_generation` (`orchestrator.py`, def at line 635).** The
ordering inside a single generation is, in source order:

| Step | Line | What happens |
|------|------|--------------|
| Run target agent for gen `N` | `680` | `_run_target_agent(...)` |
| **Evaluate gen `N`** | `695` | `run_evaluation(gen_dir, dataset_dir, ...)` → **writes `gen_N/results.json`** |
| Add gen `N` to context | `700` | `run_setup.context_mgr.add_generation(...)` |
| Guard: not last gen | `716` | `if current_gen < max_gen:` |
| **Run feedback agent** | `736` | `_run_feedback_agent(current_gen=N, ...)` |

So `run_evaluation` (line 695) writes `gen_N/results.json` **before**
`_run_feedback_agent` is called (line 736). Every earlier generation was evaluated
in its own pass. Therefore when `_run_feedback_agent` runs for gen `N`, all of gens
`1..N` have a `results.json`, and scanning them for the max score gives a
well-defined incumbent. (One caveat, handled below: if gen `N` crashed, its
`results.json` may be missing — `_parse_gen_score` returns `None` for it and it is
simply skipped, so the incumbent is still well-defined over whatever *did* score.)

---

## 3. The blind linear chain we are replacing

`_run_feedback_agent` (`orchestrator.py`, def at line 554). The current code reads
the **current** generation's agent file unconditionally:

```python
# orchestrator.py, lines 575–582 (BEFORE)
    # Read the appropriate agent file based on focus mode
    gen_dir = os.path.join(run_dir, f"gen_{current_gen}")
    if focus == "weights":
        agent_file = os.path.join(gen_dir, Names.TRAIN_SCRIPT)
    else:
        agent_file = os.path.join(gen_dir, Names.TARGET_AGENT)   # <-- always gen N

    agent_py = Path(agent_file).read_text(encoding="utf-8")
```

`agent_py` is then handed to `build_feedback_prompt(..., agent_py=agent_py, ...)`
(line 596) and embedded **verbatim** into the feedback prompt (`prompts.py` line
791 in the harness branch: a bare `{agent_py}`). So the *only* thing that decides
what code the next edit branches from is **which file we read into `agent_py`** —
we can redirect the seed without altering a single character of prompt text.

---

## 4. The change (AFTER)

Two edits, both in `orchestrator.py`, both in the `focus != "weights"` (harness)
path only.

### 4a. New helpers (inserted just before `_run_feedback_agent`, at line 554)

Three small stdlib-only functions. `os`, `json`, `datetime`, `Path`, `logger`, and
`Names` are **already imported** at the top of `orchestrator.py` (lines 45–60), so
the fork adds **no new imports**.

- `_parse_gen_score(gen_dir) -> float | None` — reads `<gen_dir>/results.json`,
  returns the top-level `"accuracy"`, accepting percentage strings like `"48.99%"`
  via `float(val.strip().rstrip("%"))`, else the first numeric top-level scalar,
  else `None`. **This deliberately mirrors `context_manager._extract_metrics` +
  `finalize` (lines 276–289 and 338–383)** so the fork's numbers are identical to
  the numbers SIA already reports.
- `_compute_incumbent(run_dir, current_gen) -> (int, float | None)` — scans
  `gen_1..gen_current_gen` for the max score, strict `>` (ties keep the earlier
  gen), returns `(current_gen, None)` when nothing scored (→ upstream behavior).
- `_log_selection(...)` — appends one JSON line per generation to
  `<run_dir>/selection_log.jsonl` with `gen`, `incumbent_gen`, `current_score`,
  `incumbent_score`, `delta_vs_incumbent`, `accepted`, `timestamp`. Best-effort;
  a write failure logs a warning and never fails the run.

### 4b. Seed from the incumbent (replaces lines 575–582)

```python
# orchestrator.py, harness branch of _run_feedback_agent (AFTER)
    # Read the appropriate agent file based on focus mode
    gen_dir = os.path.join(run_dir, f"gen_{current_gen}")
    if focus == "weights":
        # Weights mode chains train.py linearly; incumbent-seeding is harness-only.
        agent_file = os.path.join(gen_dir, Names.TRAIN_SCRIPT)
    else:
        # SELECTOR FORK: seed the feedback agent from the INCUMBENT (best-scoring
        # prior generation) instead of always from the current generation.
        incumbent_gen, incumbent_score = _compute_incumbent(run_dir, current_gen)
        current_score = _parse_gen_score(gen_dir)
        accepted = incumbent_gen == current_gen and current_score is not None
        _log_selection(run_dir, current_gen, incumbent_gen, current_score, incumbent_score, accepted)
        agent_file = os.path.join(run_dir, f"gen_{incumbent_gen}", Names.TARGET_AGENT)

    agent_py = Path(agent_file).read_text(encoding="utf-8")
```

**Semantics.** `accepted == True` means gen `N` is the new best and the next edit
branches from gen `N` (the upstream behavior, when it happened to be an
improvement). `accepted == False` means gen `N` regressed (or tied), so the next
edit branches from the earlier incumbent instead of from the regression — a
**greedy hill-climb-with-revert**. Nothing is deleted or overwritten; the "revert"
is purely a choice of which existing file to feed the feedback agent as the seed.

**Why the seed change is sufficient.** `_run_feedback_agent` does not copy the
current gen's `target_agent.py` into `next_gen_dir`; it only copies the reference
helpers (`copy_reference_into`, line 614). The feedback agent writes the new
`target_agent.py` from scratch guided by the prompt, and the prompt's only view of
the "current code to improve" is the embedded `agent_py`. Redirecting that one read
therefore redirects the whole branch.

---

## 5. Composing with the local selector library (`algorithms/`)

The patch inlines its score parsing and its ledger so the diff is **self-contained**
and applies to a bare SIA checkout that does not have our package on its path. But
the logic is a one-to-one match for the shared substrate in
`/home/user/tenon/sia-buildnight/algorithms/`, and if the forked checkout *can*
import `algorithms` (add it to `PYTHONPATH`, or `pip install -e` it), the helpers
can be replaced by direct calls — same names, same semantics:

- `algorithms/base.py :: parse_score(results: dict, metric="accuracy") -> float | None`
  — identical `"48.99%"` handling; `_parse_gen_score` above is
  `parse_score(json.load(results.json))`.
- `algorithms/base.py :: read_score_from_gen(gen_dir, metric="accuracy") -> float | None`
  — reads `<gen_dir>/results.json` and parses it; a drop-in for `_parse_gen_score`.
- `algorithms/base.py :: Candidate(gen, agent_path, score, metric, hypothesis, edit_summary, meta)`
  — one evaluated generation.
- `algorithms/base.py :: IncumbentStore(path)` with `.get()/.set(IncumbentRecord(gen, score, metric, agent_path, timestamp))`
  — persists the incumbent's metadata (our `_compute_incumbent` recomputes it each
  time instead, which is cheap and stateless; `IncumbentStore` is the caching form).
- `algorithms/base.py :: Ledger(path)` + `LedgerEntry.from_candidate(candidate, incumbent_score, accepted)`
  — the append-only `ledger.jsonl`; a richer form of `_log_selection`'s
  `selection_log.jsonl` (adds `hypothesis` / `edit_summary` / `delta_vs_incumbent`).
- `algorithms/beam_hill_climb.py :: BeamHillClimb(noise_margin, beam_width)` with
  `.seed_from(ledger, incumbent, candidates)`, `.accept(candidate, incumbent)`,
  `.select_beam(candidates)`, `.next_hypothesis_hint(ledger)` — its `seed_from`
  docstring literally states the same rule this fork implements: *"Always branch
  the next edit from the incumbent — never from a regression."* Its
  `noise_margin` (via `beats_incumbent`) adds the noise guard our inline `accepted`
  omits; wiring `BeamHillClimb.accept` in place of the bare
  `incumbent_gen == current_gen` test upgrades the fork to a noise-margined
  selector for free.
- `algorithms/__init__.py :: make_selector("beam-hill-climb" | "greedy" | "annealed", noise_margin=, beam_width=, with_bandit=)`
  — the factory the outer driver would call to pick a strategy.

**Recommended composition if the package is importable:** replace `_parse_gen_score`
with `read_score_from_gen`, keep `_compute_incumbent` (or swap it for
`IncumbentStore` + `Candidate` + `Selector.best`), and replace the
`incumbent_gen == current_gen` test with
`BeamHillClimb(noise_margin=M).accept(candidate, incumbent)` so accepts require a
real, above-noise improvement. This costs a few more lines in the same hunk and no
new prompt/scoring changes.

---

## 6. Single-track vs beam

A single forked `sia run` is **inherently single-track**. The loop keeps exactly
one `gen_{N}` chain, so the most this patch can express is a **greedy
hill-climb-with-revert**: one incumbent, one challenger per generation, branch the
next edit from whichever is better. That already fixes the core defect (regressions
no longer propagate; the reported best is the one that seeds forward).

**True parallel beam** (width `B > 1`: keep the top-`B` candidates, expand each,
select best-of-`K` per round) needs an **outer driver** that launches and reconciles
several `sia run` tracks — that lives in `kit/orchestrate.py`, not in this patch.
`BeamHillClimb.select_beam(candidates)` and `make_selector(..., beam_width=B)` in
`algorithms/` are the pieces that driver calls; this fork provides the per-track
"never seed from a regression" invariant that makes a beam over tracks meaningful.
Keep both in mind: **the fork = the intra-run selector; the driver = the inter-run
beam.**

---

## 7. Rollback / footprint

- **Files changed:** exactly one — `sia/orchestrator.py`.
- **Functions changed:** `_run_feedback_agent` (the harness branch of its
  agent-file read, ~6 lines → ~13 lines) plus three new module-level helpers
  (`_parse_gen_score`, `_compute_incumbent`, `_log_selection`) inserted just above
  it. Net `+125 / -1` lines (verified by `git apply` + `git diff --stat`).
- **Prompt text:** untouched. `prompts.py` is not in the diff, so
  `build_feedback_prompt` and its golden-master tests are unaffected.
- **Scoring:** untouched. The fork only *reads* `results.json` with the same
  parsing SIA already uses; it never writes a score or changes `run_evaluation`
  or `context_manager` metrics.
- **Dependencies / imports:** none added — stdlib only, all names already imported.
- **Weights mode (`focus == "weights"`):** unchanged — still a linear `train.py`
  chain; the fork guards its logic behind the harness branch.
- **New artifact:** `<run_dir>/selection_log.jsonl` (append-only; ignore or delete
  freely). No existing artifact is overwritten.
- **Rollback:** `git apply -R orchestrator_incumbent_seed.patch`, or just discard
  the `fork/` directory. There is no state to migrate.

---

## 8. How to apply

```bash
cd <your-SIA-checkout>          # repo root: the dir containing sia/orchestrator.py
git apply --check /home/user/tenon/sia-buildnight/fork/orchestrator_incumbent_seed.patch  # dry run
git apply         /home/user/tenon/sia-buildnight/fork/orchestrator_incumbent_seed.patch  # apply
```

The patch uses `a/sia/orchestrator.py` / `b/sia/orchestrator.py` paths, so apply it
from the **repo root** (the parent of `sia/`). It was generated against and
verified to apply on commit `8c50303`; on a drifted tree use
`git apply --3way` and re-check the two hunk anchors (lines 551 and 575).

---

## 9. Risks / things a human should double-check on the night

1. **Metric name & direction.** The fork assumes the primary metric is `"accuracy"`
   and that **higher is better** (matching `context_manager.finalize`). If the
   night's task reports a different key (e.g. `"rmse"`, `"loss"`) or a
   lower-is-better metric, change the field and flip the comparison in
   `_parse_gen_score` / `_compute_incumbent` (or pass the right `metric=` if you
   wire in `algorithms.read_score_from_gen`). **Check `results.json` first.**
2. **Noise.** The inline accept test requires only a strict `>`; a noisy eval can
   accept a lucky +0.01. If evals are noisy, swap in
   `BeamHillClimb(noise_margin=M).accept(...)` (Section 5) with a sensible `M`.
3. **Crashed / unscored generations.** If gen `N` produced no `results.json`,
   `_parse_gen_score` returns `None`, gen `N` is skipped, and the incumbent is the
   best of the gens that *did* score — good default, but confirm it is the behavior
   you want (a crashed challenger is treated as "no improvement", i.e. reject).
4. **Framework-patch legality.** Re-read Section 0's banner. If the grader re-runs
   an unpatched upstream `sia run`, this fork does nothing for the score — use the
   injection kit instead.
5. **`git apply` anchor drift.** If the SIA checkout is not at `8c50303`, confirm
   the two hunks still land in `_run_feedback_agent` (agent-file read) and just
   before it; the hunk context quotes the real surrounding lines to make drift
   obvious.
