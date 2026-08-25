import random

from algorithms import (
    Annealed,
    BeamHillClimb,
    Candidate,
    IncumbentRecord,
    Ledger,
    LedgerEntry,
    StrategyBandit,
    make_selector,
    parse_score,
)


# -- score parsing ---------------------------------------------------------- #
def test_parse_score_percentage_string():
    assert parse_score({"accuracy": "48.99%"}) == 48.99


def test_parse_score_float_and_fallback():
    assert parse_score({"accuracy": 0.85}) == 0.85
    assert parse_score({"f1": 0.7}) == 0.7  # fallback to first numeric
    assert parse_score({"note": "x"}) is None
    assert parse_score({"accuracy": True}) is None  # bool is not a score


# -- ledger ----------------------------------------------------------------- #
def test_ledger_roundtrip(tmp_path):
    led = Ledger(tmp_path / "ledger.jsonl")
    c = Candidate(gen=2, agent_path="a.py", score=0.6, hypothesis="restructure-prompt")
    led.append(LedgerEntry.from_candidate(c, incumbent_score=0.5, accepted=True))
    rows = led.all()
    assert len(rows) == 1
    assert abs(rows[0].delta_vs_incumbent - 0.1) < 1e-9  # 0.6 - 0.5
    assert rows[0].accepted is True


# -- beam hill climb -------------------------------------------------------- #
def test_beam_seeds_from_incumbent_not_regression():
    sel = BeamHillClimb(noise_margin=0.0, beam_width=2)
    inc = IncumbentRecord(gen=1, score=0.7, agent_path="/inc/target_agent.py")
    assert sel.seed_from(Ledger("x"), inc, []) == "/inc/target_agent.py"


def test_beam_accept_respects_noise_margin():
    sel = BeamHillClimb(noise_margin=0.05)
    inc = IncumbentRecord(gen=1, score=0.70, agent_path="i")
    assert sel.accept(Candidate(2, "a", 0.76), inc) is True     # +0.06 > margin
    assert sel.accept(Candidate(2, "a", 0.73), inc) is False    # +0.03 < margin


def test_beam_select_beam_top_b():
    sel = BeamHillClimb(beam_width=2)
    cs = [Candidate(1, "a", 0.1), Candidate(1, "b", 0.9), Candidate(1, "c", 0.5)]
    picked = [c.agent_path for c in sel.select_beam(cs)]
    assert picked == ["b", "c"]


# -- annealed --------------------------------------------------------------- #
def test_annealed_always_takes_improvement():
    sel = Annealed(rng=random.Random(0))
    inc = IncumbentRecord(gen=1, score=0.5, agent_path="i")
    assert sel.accept(Candidate(2, "a", 0.6), inc) is True
    assert sel.current_score == 0.6


def test_annealed_can_take_regression_but_is_deterministic():
    # With a fixed RNG the sequence is reproducible; a hot start accepts some
    # downhill moves. Same seed => same decisions.
    def run():
        sel = Annealed(t0=1.0, cooling=0.9, rng=random.Random(42))
        sel.current_score, sel.current_path = 0.8, "start"
        return [sel.accept(Candidate(i, f"c{i}", 0.5), None) for i in range(5)]

    assert run() == run()  # determinism
    # at least one downhill accept happens while hot
    assert any(run())


# -- strategy bandit -------------------------------------------------------- #
def test_bandit_cold_start_tries_each_arm():
    base = BeamHillClimb()
    bandit = StrategyBandit(base, arms=["x", "y", "z"])
    led = Ledger("nonexistent.jsonl")
    assert bandit.next_hypothesis_hint(led) == "x"  # first untried


def test_bandit_prefers_higher_reward_arm(tmp_path):
    base = BeamHillClimb()
    bandit = StrategyBandit(base, arms=["x", "y"], c=0.0)  # c=0 → pure exploit
    led = Ledger(tmp_path / "l.jsonl")
    # x paid off, y did not; each pulled twice so no cold-start arm remains.
    led.append(LedgerEntry(1, "x", "", 0.6, 0.2, True))
    led.append(LedgerEntry(2, "x", "", 0.6, 0.2, True))
    led.append(LedgerEntry(3, "y", "", 0.4, -0.1, False))
    led.append(LedgerEntry(4, "y", "", 0.4, -0.1, False))
    assert bandit.next_hypothesis_hint(led) == "x"


def test_bandit_delegates_accept_to_base():
    base = BeamHillClimb(noise_margin=0.0)
    bandit = StrategyBandit(base)
    inc = IncumbentRecord(gen=1, score=0.5, agent_path="i")
    assert bandit.accept(Candidate(2, "a", 0.6), inc) is True


# -- factory ---------------------------------------------------------------- #
def test_make_selector_variants():
    assert make_selector("greedy").beam_width == 1
    assert isinstance(make_selector("annealed"), Annealed)
    assert isinstance(make_selector("beam-hill-climb", with_bandit=True), StrategyBandit)
