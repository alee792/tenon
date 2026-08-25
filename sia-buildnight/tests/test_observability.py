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
