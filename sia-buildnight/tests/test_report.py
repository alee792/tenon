"""Run-report generator — verify it derives improvement, curve, failure modes,
and history from a run tree on disk. Credential-free; builds a synthetic run.
"""

import json
from pathlib import Path

import report


def _gen(run, n, score=None, worst_stage=None, tokens=None, hypothesis=None):
    g = run / f"gen_{n}"
    (g / "agent_execution").mkdir(parents=True, exist_ok=True)
    if score is not None:
        (g / "results.json").write_text(json.dumps({"accuracy": score}), encoding="utf-8")
    if worst_stage is not None or tokens is not None:
        (g / "agent_execution" / "execution_q-diagnostic.json").write_text(
            json.dumps({"worst_stage": worst_stage, "failures_by_error_class": {"ParseError": 2},
                        "success_rate": 0.8, "total_tokens": tokens}), encoding="utf-8")
    if hypothesis is not None:
        (g / "improvement.md").write_text(f"## Generation {n}\n- hypothesis: {hypothesis}\n", encoding="utf-8")
    return g


def _make_run(tmp_path) -> Path:
    run = tmp_path / "run_1"
    _gen(run, 1, 0.50, worst_stage="solve", tokens=1000, hypothesis="baseline")
    _gen(run, 2, 0.71, worst_stage="parse", tokens=1200, hypothesis="harden-output-parsing")  # best
    _gen(run, 3, 0.64, worst_stage="solve", tokens=1500, hypothesis="self-consistency-voting")  # regression
    return run


def test_report_measures_improvement(tmp_path):
    r = report.build_report(_make_run(tmp_path), config={"mode": "A", "run_id": 1})
    res = r["result"]
    assert res["baseline_score"] == 0.50
    assert res["best_score"] == 0.71
    assert res["best_gen"] == 2
    assert abs(res["absolute_improvement"] - 0.21) < 1e-9
    assert abs(res["relative_improvement_pct"] - 42.0) < 1e-9
    assert [c["gen"] for c in res["curve"]] == [1, 2, 3]
    assert res["curve"][1]["cost_tokens"] == 1200


def test_report_captures_failure_modes_and_history(tmp_path):
    r = report.build_report(_make_run(tmp_path), config={"mode": "A"})
    stages = {f["gen"]: f["worst_stage"] for f in r["failure_modes"]}
    assert stages[2] == "parse"
    # history falls back to improvement.md blocks when no ledger.jsonl
    hist = {h["gen"]: h["hypothesis"] for h in r["experiment_history"]}
    assert hist[2] == "harden-output-parsing"


def test_report_prefers_ledger_jsonl(tmp_path):
    run = _make_run(tmp_path)
    (run / "ledger.jsonl").write_text(
        json.dumps({"gen": 2, "hypothesis": "harden-output-parsing", "accepted": True}) + "\n",
        encoding="utf-8")
    r = report.build_report(run, config={"mode": "A"})
    assert r["experiment_history"][0].get("accepted") is True  # from ledger, not improvement.md


def test_report_integrity_and_techniques_are_present(tmp_path):
    r = report.build_report(_make_run(tmp_path), config={"mode": "A"})
    assert r["integrity"]["eval_untouched"] is True
    assert r["integrity"]["gains_produced_by_loop"] is True   # mode A
    assert r["integrity"]["baseline_is_first_generation"] is True
    assert any("ACE" in t["citation"] for t in r["techniques"])
    assert any("MCE" in t["citation"] for t in r["techniques"])


def test_render_markdown_is_complete(tmp_path):
    r = report.build_report(_make_run(tmp_path), config={"mode": "A"}, label="gpqa")
    md = report.render_markdown(r)
    assert "Improvement run report — gpqa" in md
    assert "baseline (gen 1): **0.5**" in md
    assert "best: **0.71** (gen 2)" in md
    assert "Techniques (cited)" in md
