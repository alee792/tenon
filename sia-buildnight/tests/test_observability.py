import json
from pathlib import Path

from observability import DIAGNOSTIC_FILENAME, TrajectoryLogger


def test_logger_writes_per_sample_and_sortfirst_diagnostic(tmp_path):
    log = TrajectoryLogger(tmp_path)
    with log.sample(0) as rec:
        rec["got"] = "A"
        rec["confidence"] = 0.9
    with log.sample(1) as rec:
        raise ValueError("boom")  # a crashing sample must be captured, not fatal

    summary = log.finalize()

    exec_dir = tmp_path / "agent_execution"
    files = sorted(p.name for p in exec_dir.glob("execution_q*.json"))
    assert "execution_q0.json" in files
    assert "execution_q1.json" in files
    assert DIAGNOSTIC_FILENAME in files

    # the diagnostic sorts FIRST so SIA's first-3 window always shows it
    assert sorted(files)[0] == DIAGNOSTIC_FILENAME

    # the crashing sample was recorded as a failure with its error class
    q1 = json.loads((exec_dir / "execution_q1.json").read_text())
    assert q1["ok"] is False
    assert q1["error_class"] == "ValueError"

    assert summary["total_samples"] == 2
    assert summary["failed"] == 1
    assert summary["worst_stage"] == "solve"


def test_diagnostic_filename_sorts_before_numbered():
    # Guards the naming trick: '-' (0x2d) < '0' (0x30).
    names = ["execution_q0.json", "execution_q10.json", DIAGNOSTIC_FILENAME]
    assert sorted(names)[0] == DIAGNOSTIC_FILENAME


def test_summary_aggregates_cost(tmp_path, capsys):
    log = TrajectoryLogger(tmp_path)
    with log.sample(0) as rec:
        rec["got"] = "A"
        rec["tokens"] = 100
    with log.sample(1) as rec:
        rec["got"] = "B"
        rec["tokens"] = 150
    s = log.finalize()
    assert s["total_tokens"] == 250
    assert s["total_latency_ms"] is not None
    assert s["mean_latency_ms"] is not None
    assert "COST: total_tokens=250" in capsys.readouterr().out


def test_summary_cost_is_none_without_tokens(tmp_path):
    log = TrajectoryLogger(tmp_path)
    with log.sample(0) as rec:
        rec["got"] = "A"  # no tokens recorded
    s = log.finalize()
    assert s["total_tokens"] is None


def test_clusters_carry_exemplars_biggest_first(tmp_path):
    log = TrajectoryLogger(tmp_path)
    for i in range(3):
        with log.sample(i, stage="format") as rec:
            rec["expected"] = "42"
            rec["got"] = "forty-two"
            raise ValueError("unparseable")
    with log.sample(9, stage="solve") as rec:
        raise TimeoutError("slow")

    s = log.summary()
    clusters = s["clusters"]
    # biggest cluster (the 3 ValueErrors) comes first, with concrete exemplars
    assert clusters[0]["error_class"] == "ValueError"
    assert clusters[0]["count"] == 3
    assert clusters[0]["examples"][0]["expected"] == "42"
    assert clusters[0]["examples"][0]["got"] == "forty-two"
    # exemplars are capped per cluster
    assert len(clusters[0]["examples"]) <= 2


def test_degenerate_confidence_is_flagged(tmp_path):
    log = TrajectoryLogger(tmp_path)
    for i in range(3):
        with log.sample(i) as rec:
            rec["confidence"] = 1.0  # the seed's hardcoded constant
    s = log.summary()
    assert s["confidence"]["degenerate"] is True
    assert "constant" in s["confidence"]["note"]


def test_latency_percentiles_and_over_budget(tmp_path):
    # Build records directly so latency_ms is deterministic (the context manager
    # measures wall-clock and would overwrite a value set inside the block).
    from observability import SampleRecord

    log = TrajectoryLogger(tmp_path, latency_budget_ms=5.0)
    for i, ms in enumerate([1.0, 2.0, 100.0]):
        log.add(SampleRecord(i, latency_ms=ms))
    s = log.summary()
    assert s["latency"]["over_budget"] == 1
    assert s["latency"]["p95_ms"] is not None


def test_finalize_surfaces_recommendation_and_delta(tmp_path, capsys):
    log = TrajectoryLogger(tmp_path)
    with log.sample(0) as rec:
        rec["got"] = "x"
    log.finalize(extra={
        "incumbent": {"gen": 2, "score": 0.5},
        "recommended_hypothesis": {"family": "harden-output-parsing", "reason": "crashes"},
        "cross_gen": {
            "failure_delta": {"prev_gen": 2, "new_failure_classes": ["TimeoutError"],
                              "cleared_failure_classes": []},
            "prediction_check": {"hypothesis": "decompose-reasoning",
                                 "actual_delta": -0.3, "held": False},
        },
    })
    out = capsys.readouterr().out
    assert "RECOMMEND: try 'harden-output-parsing'" in out
    assert "new_failures=['TimeoutError']" in out
    assert "FAILED" in out  # the prediction check that did not hold
