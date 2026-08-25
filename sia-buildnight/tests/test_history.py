"""Deterministic incumbent computation — the model-independent half of selection.

Credential-free: simulates a run directory tree of gen_*/results.json and checks
sia_history picks the best prior generation exactly, mirroring SIA's scoring.
"""

import json
from pathlib import Path

from observability import TrajectoryLogger
from sia_history import compute_incumbent, parse_score, surface_incumbent


def _write_gen(run_dir: Path, n: int, score, *, agent: bool = True) -> Path:
    g = run_dir / f"gen_{n}"
    g.mkdir(parents=True, exist_ok=True)
    if score is not None:
        (g / "results.json").write_text(json.dumps({"accuracy": score}), encoding="utf-8")
    if agent:
        (g / "target_agent.py").write_text(f"# gen {n}\n", encoding="utf-8")
    return g


# -- parse_score mirrors SIA -------------------------------------------------- #
def test_parse_score_handles_percent_and_fallback():
    assert parse_score({"accuracy": "48.99%"}) == 48.99
    assert parse_score({"accuracy": 0.7}) == 0.7
    assert parse_score({"f1": 0.62}) == 0.62  # fallback to first numeric scalar
    assert parse_score({"note": "n/a"}) is None
    assert parse_score({"accuracy": True}) is None  # bool is not a score


# -- incumbent selection ------------------------------------------------------ #
def test_incumbent_is_highest_prior_generation(tmp_path):
    run = tmp_path / "run_1"
    _write_gen(run, 1, 0.50)
    _write_gen(run, 2, 0.71)   # best
    _write_gen(run, 3, 0.64)
    current = _write_gen(run, 4, None)  # current gen: not yet scored

    inc = compute_incumbent(current)
    assert inc is not None
    assert inc["gen"] == 2
    assert inc["score"] == 0.71
    assert inc["agent_path"].endswith("gen_2/target_agent.py")


def test_current_generation_excluded_and_none_when_no_scores(tmp_path):
    run = tmp_path / "run_1"
    current = _write_gen(run, 1, None)  # only the current gen exists, unscored
    assert compute_incumbent(current) is None
    # surface_incumbent is the same value, no side effects
    assert surface_incumbent(current) is None


def test_unreadable_results_are_skipped(tmp_path):
    run = tmp_path / "run_1"
    _write_gen(run, 1, 0.40)
    bad = run / "gen_2"
    bad.mkdir(parents=True)
    (bad / "results.json").write_text("{ not json", encoding="utf-8")
    current = _write_gen(run, 3, None)
    inc = compute_incumbent(current)
    assert inc["gen"] == 1  # the corrupt gen_2 is skipped, not fatal


# -- the incumbent reaches the always-visible diagnostic ---------------------- #
def test_finalize_surfaces_incumbent(tmp_path, capsys):
    log = TrajectoryLogger(tmp_path)
    with log.sample(0) as rec:
        rec["got"] = "A"
    summary = log.finalize(extra={"incumbent": {"gen": 2, "score": 0.71}})

    assert summary["incumbent"] == {"gen": 2, "score": 0.71}
    out = capsys.readouterr().out
    assert "INCUMBENT: gen=2 score=0.71" in out

    # and it is persisted in the sort-first diagnostic file SIA shows first
    diag = json.loads((tmp_path / "agent_execution" / "execution_q-diagnostic.json").read_text())
    assert diag["incumbent"]["gen"] == 2


def test_finalize_notes_missing_incumbent(tmp_path, capsys):
    log = TrajectoryLogger(tmp_path)
    with log.sample(0) as rec:
        rec["got"] = "A"
    log.finalize(extra={"incumbent": None})
    out = capsys.readouterr().out
    assert "INCUMBENT: none visible here" in out
