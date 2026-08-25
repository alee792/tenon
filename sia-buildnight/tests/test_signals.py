"""Cross-generation signal tests — built on a fake ``run_K/gen_N`` tree, no
model or network. Mirrors what SIA lays down under ``sandbox=none``: each prior
generation has a ``results.json``, an ``improvement.md`` block, and the
sort-first diagnostic file our own logger writes.
"""

import json
from pathlib import Path

import signals


def _write_gen(run_dir: Path, n: int, *, score, hypothesis=None,
               predicted="it will help", failures_by_error_class=None):
    gen = run_dir / f"gen_{n}"
    (gen / "agent_execution").mkdir(parents=True, exist_ok=True)
    if score is not None:
        (gen / "results.json").write_text(json.dumps({"accuracy": score}))
    if hypothesis is not None:
        (gen / "improvement.md").write_text(
            f"## Generation {n}\n"
            f"- incumbent_gen: {n-1}\n"
            f"- hypothesis: {hypothesis}\n"
            f"- edit_summary: did a thing\n"
            f"- predicted_effect: {predicted}\n"
        )
    if failures_by_error_class is not None:
        diag = {
            "type": "DIAGNOSTIC_SUMMARY",
            "gen": n,
            "failed": sum(failures_by_error_class.values()),
            "failures_by_error_class": failures_by_error_class,
        }
        (gen / "agent_execution" / "execution_q-diagnostic.json").write_text(json.dumps(diag))
    return gen


def test_tried_digest_tracks_family_and_payoff(tmp_path):
    run = tmp_path / "run_1"
    _write_gen(run, 0, score=10.0, hypothesis="restructure-prompt")
    _write_gen(run, 1, score=15.0, hypothesis="self-consistency-voting")   # +5 paid
    _write_gen(run, 2, score=12.0, hypothesis="decompose-reasoning")       # -3 dud
    current = run / "gen_3"
    (current / "agent_execution").mkdir(parents=True)

    digest = signals.tried_digest(current)
    by_gen = {r["gen"]: r for r in digest}
    assert by_gen[1]["paid_off"] is True
    assert by_gen[1]["delta_vs_prev"] == 5.0
    assert by_gen[2]["paid_off"] is False
    assert by_gen[2]["delta_vs_prev"] == -3.0


def test_prediction_check_detects_failed_prediction(tmp_path):
    run = tmp_path / "run_1"
    _write_gen(run, 0, score=20.0, hypothesis="restructure-prompt")
    # gen 1 predicted improvement but the score dropped 20 -> 18
    _write_gen(run, 1, score=18.0, hypothesis="decompose-reasoning",
               predicted="splitting the reasoning will raise accuracy")
    current = run / "gen_2"
    (current / "agent_execution").mkdir(parents=True)

    pred = signals.prediction_check(current)
    assert pred["gen"] == 1
    assert pred["hypothesis"] == "decompose-reasoning"
    assert pred["actual_delta"] == -2.0
    assert pred["held"] is False


def test_failure_delta_flags_new_and_cleared_classes(tmp_path):
    run = tmp_path / "run_1"
    _write_gen(run, 0, score=10.0, hypothesis="harden-output-parsing",
               failures_by_error_class={"JSONDecodeError": 5, "KeyError": 2})
    current = run / "gen_1"
    (current / "agent_execution").mkdir(parents=True)

    # this run cleared JSONDecodeError but introduced a TimeoutError regression
    current_summary = {"failed": 4, "failures_by_error_class": {"KeyError": 1, "TimeoutError": 3}}
    out = signals.gather(current, current_summary, incumbent=None)
    delta = out["cross_gen"]["failure_delta"]
    assert delta["prev_gen"] == 0
    assert "TimeoutError" in delta["new_failure_classes"]
    assert "JSONDecodeError" in delta["cleared_failure_classes"]
    assert delta["by_error_class"]["KeyError"]["delta"] == -1


def test_recommend_maps_crash_cluster_to_family(tmp_path):
    run = tmp_path / "run_1"
    current = run / "gen_1"
    (current / "agent_execution").mkdir(parents=True)
    # dominant crash is a JSONDecodeError -> harden-output-parsing
    summary = {"total_samples": 20, "failed": 8, "worst_stage": "solve",
               "failures_by_error_class": {"JSONDecodeError": 6, "ValueError": 2}}
    rec = signals.recommend_hypothesis(summary, current)
    assert rec["family"] == "harden-output-parsing"


def test_recommend_excludes_tried_without_payoff(tmp_path):
    run = tmp_path / "run_1"
    # harden-output-parsing already tried and did NOT pay off
    _write_gen(run, 0, score=10.0, hypothesis="restructure-prompt")
    _write_gen(run, 1, score=9.0, hypothesis="harden-output-parsing")  # -1 dud
    current = run / "gen_2"
    (current / "agent_execution").mkdir(parents=True)
    summary = {"total_samples": 20, "failed": 8, "worst_stage": "solve",
               "failures_by_error_class": {"JSONDecodeError": 6}}
    rec = signals.recommend_hypothesis(summary, current)
    # the obvious crash family is spent, so it must pick something else
    assert rec["family"] != "harden-output-parsing"


def test_recommend_semantic_when_few_crashes(tmp_path):
    run = tmp_path / "run_1"
    _write_gen(run, 0, score=40.0, hypothesis="restructure-prompt")
    _write_gen(run, 1, score=40.0, hypothesis="improve-retrieval")  # flat
    current = run / "gen_2"
    (current / "agent_execution").mkdir(parents=True)
    # no crashes, but score is flat -> semantic failures -> reasoning family
    summary = {"total_samples": 50, "failed": 0, "worst_stage": None,
               "failures_by_error_class": {}}
    rec = signals.recommend_hypothesis(summary, current)
    assert rec["family"] in signals._SEMANTIC_FAMILIES


def test_gather_is_empty_under_sandbox(tmp_path):
    # No sibling gens visible (docker sandbox): everything degrades gracefully.
    current = tmp_path / "isolated_gen"
    current.mkdir()
    summary = {"total_samples": 5, "failed": 0, "failures_by_error_class": {}}
    out = signals.gather(current, summary, incumbent=None)
    assert out["cross_gen"]["failure_delta"] is None
    assert out["cross_gen"]["prediction_check"] is None
    assert out["cross_gen"]["tried"] == []
    assert "family" in out["recommended_hypothesis"]
