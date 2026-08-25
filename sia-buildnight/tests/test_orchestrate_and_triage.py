import json
from pathlib import Path

import orchestrate
import triage
from algorithms import BeamHillClimb, Candidate, IncumbentStore, Ledger


# -- orchestrate.evaluate_round -------------------------------------------- #
def _write_agent(p: Path, marker: str) -> str:
    p.write_text(f"# {marker}\n", encoding="utf-8")
    return str(p)


def test_evaluate_round_promotes_best_and_logs_all(tmp_path):
    ws = tmp_path / "ws"
    ws.mkdir()
    store = IncumbentStore(ws / "incumbent.json")
    ledger = Ledger(ws / "ledger.jsonl")
    sel = BeamHillClimb(noise_margin=0.0)

    a = _write_agent(tmp_path / "a.py", "cand-a")
    b = _write_agent(tmp_path / "b.py", "cand-b")
    cands = [Candidate(1, a, 0.4), Candidate(2, b, 0.8)]

    inc = orchestrate.evaluate_round(cands, sel, store, ledger, ws)

    assert inc is not None and inc.score == 0.8
    assert Path(inc.agent_path).exists()
    assert "cand-b" in Path(inc.agent_path).read_text()
    # both candidates recorded, exactly one accepted
    rows = ledger.all()
    assert len(rows) == 2
    assert sum(r.accepted for r in rows) == 1


def test_evaluate_round_keeps_incumbent_when_no_improvement(tmp_path):
    ws = tmp_path / "ws"
    ws.mkdir()
    store = IncumbentStore(ws / "incumbent.json")
    ledger = Ledger(ws / "ledger.jsonl")
    sel = BeamHillClimb(noise_margin=0.0)

    good = _write_agent(tmp_path / "good.py", "incumbent")
    orchestrate.evaluate_round(
        [Candidate(1, good, 0.9)], sel, store, ledger, ws)
    worse = _write_agent(tmp_path / "worse.py", "worse")
    inc = orchestrate.evaluate_round(
        [Candidate(2, worse, 0.5)], sel, store, ledger, ws)

    assert inc.score == 0.9  # unchanged
    assert "incumbent" in Path(inc.agent_path).read_text()


def test_best_gen_in_run(tmp_path):
    run_dir = tmp_path / "run_1"
    for i, score in enumerate([0.3, 0.7, 0.5], start=1):
        g = run_dir / f"gen_{i}"
        g.mkdir(parents=True)
        (g / "results.json").write_text(json.dumps({"accuracy": score}))
    best_dir, best_score = orchestrate.best_gen_in_run(run_dir, "accuracy")
    assert best_score == 0.7
    assert best_dir.name == "gen_2"


# -- triage.recommend decision table --------------------------------------- #
def test_triage_cheap_parallel_is_posture_b_beam():
    rec = triage.recommend(triage.Probe(
        eval_seconds=10, noise_std=0.001, parallel_ok=True, minutes_left=90))
    assert rec.posture == "B"
    assert rec.selector == "beam-hill-climb"
    assert rec.beam_width >= 3


def test_triage_expensive_noisy_is_annealed():
    rec = triage.recommend(triage.Probe(
        eval_seconds=300, noise_std=0.05, parallel_ok=False, minutes_left=90))
    assert rec.posture == "A"
    assert rec.selector == "annealed"
    assert rec.noise_margin > 0.05  # widened past one sigma


def test_triage_expensive_lownoise_is_greedy():
    rec = triage.recommend(triage.Probe(
        eval_seconds=300, noise_std=0.001, parallel_ok=False, minutes_left=30))
    assert rec.posture == "A"
    assert rec.selector == "greedy"


def test_triage_bandit_layers_when_many_gens():
    rec = triage.recommend(triage.Probe(
        eval_seconds=5, noise_std=0.001, parallel_ok=False, minutes_left=120))
    assert rec.with_bandit is True  # affordable_gens >= threshold


def test_render_handoff_fills_all_placeholders():
    tmpl = (Path(triage.__file__).parent / "HANDOFF.md.tmpl").read_text()
    probe = triage.Probe(eval_seconds=12, noise_std=0.002, parallel_ok=True, minutes_left=90)
    rec = triage.recommend(probe)
    out = triage.render_handoff(probe, rec, "tasks/mychallenge", tmpl)
    assert "{{" not in out  # every placeholder replaced
    assert "Posture" in out
