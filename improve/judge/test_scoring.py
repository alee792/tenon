#!/usr/bin/env python3
"""Tests for the pairwise judge's cardinal projection.

Run: python3 improve/judge/test_scoring.py
"""

import itertools
import pathlib
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import server  # noqa: E402


def round_of(keys, verdicts):
    r = server.Round.__new__(server.Round)
    r.entries = [{"key": k} for k in keys]
    r.pairs = [{"id": f"{a}|{b}", "a": a, "b": b} for a, b in itertools.combinations(keys, 2)]
    r.verdicts = verdicts
    return r


def test_unjudged_entry_gets_the_coin_flip():
    """Round 0 holds only the seed. Scoring it zero would mean anything
    at all beats it, and whether the search beat the seed is the first
    question it has to answer."""
    assert round_of(["only"], {}).scores() == {"only": 0.5}


def test_dominance_order_is_preserved():
    keys = list("ABCDE")
    verdicts = {f"{a}|{b}": "a" for a, b in itertools.combinations(keys, 2)}
    assert [r["entry"] for r in round_of(keys, verdicts).board()] == keys


def test_all_ties_are_all_even():
    keys = list("ABCDE")
    verdicts = {f"{a}|{b}": "tie" for a, b in itertools.combinations(keys, 2)}
    assert all(abs(r["score"] - 0.5) < 1e-6 for r in round_of(keys, verdicts).board())


def test_a_harder_field_counts_for_more():
    """X and Y both go 1-1, but X's opponents were the strong pair. A raw win
    rate scores both 0.500; this is the reason to fit strengths instead."""
    keys = ["A", "B", "C", "F", "X", "Y"]
    verdicts = {
        "A|B": "a", "A|C": "a", "A|F": "a", "B|C": "a", "B|F": "a", "C|F": "a",
        "A|X": "a", "B|X": "b",   # X: lost to A, beat B
        "C|Y": "a", "F|Y": "b",   # Y: lost to C, beat F
    }
    board = {r["entry"]: r for r in round_of(keys, verdicts).board()}
    assert board["X"]["wins"] == board["Y"]["wins"] == 1
    assert board["X"]["played"] == board["Y"]["played"] == 2
    assert board["X"]["score"] > board["Y"]["score"]


def test_one_and_one_against_the_same_pair_is_uninformative():
    """Beating A and losing to F, versus beating F and losing to A, are equally
    likely under the model — the likelihoods differ only by a constant. The
    estimator says so rather than inventing a difference."""
    keys = ["A", "B", "C", "F", "X", "Y"]
    verdicts = {
        "A|B": "a", "A|C": "a", "A|F": "a", "B|C": "a", "B|F": "a", "C|F": "a",
        "A|X": "b", "F|X": "a",   # X beat A, lost to F
        "A|Y": "a", "F|Y": "b",   # Y lost to A, beat F
    }
    board = {r["entry"]: r for r in round_of(keys, verdicts).board()}
    assert abs(board["X"]["score"] - board["Y"]["score"]) < 1e-6


def test_an_incomplete_graph_still_ranks():
    """Not every pair has to be judged for the fit to be defined."""
    keys = ["A", "B", "C"]
    board = round_of(keys, {"A|B": "a", "B|C": "a"}).board()
    assert [r["entry"] for r in board] == ["A", "B", "C"]


def test_scores_stay_inside_the_unit_interval():
    keys = list("ABCDE")
    verdicts = {f"{a}|{b}": "a" for a, b in itertools.combinations(keys, 2)}
    assert all(0.0 <= r["score"] <= 1.0 for r in round_of(keys, verdicts).board())


def test_the_global_fit_has_one_shape():
    """A single-node fit is the round-0 case, and callers index every
    result the same way — so it cannot quietly return a bare number."""
    for nodes, comparisons in ([["only"], []], [["a", "b"], [("a", "b", 1.0)]]):
        out = server.fit(nodes, comparisons)
        for key, value in out.items():
            assert isinstance(value, dict), f"{key} came back as {type(value).__name__}"
            assert "score" in value and "strength" in value
    assert server.fit(["only"], [])["only"]["score"] == 0.5


def _judge_with_two_rounds(root):
    """A finished round 1 and a half-judged round 2."""
    import json

    for rno, names in ((1, ["aaaaaaaa", "bbbbbbbb"]), (2, ["cccccccc", "dddddddd", "eeeeeeee"])):
        for name in names:
            d = root / "rounds" / f"round-{rno}" / "variants" / f"{name}-t0r0"
            d.mkdir(parents=True)
            (d / "events.jsonl").write_text(
                json.dumps({"type": "agent.output.delta", "delta": f"answer {name}"}) + "\n"
            )
    (root / "judge").mkdir()
    (root / "judge" / "verdicts-round-1.json").write_text(json.dumps({"aaaaaaaa-t0r0|bbbbbbbb-t0r0": "a"}))
    (root / "judge" / "verdicts-round-2.json").write_text(json.dumps({"cccccccc-t0r0|dddddddd-t0r0": "a"}))

    judge = server.Judge()
    for rno in (1, 2):
        rnd = server.Round(root, rno, 0)
        judge.rounds[(str(root), rno, 0)] = rnd
    return judge


def test_undo_only_touches_the_round_being_judged():
    """One undo used to walk every round and pop a verdict from each, silently
    damaging rounds that were already finished."""
    import shutil, tempfile

    root = pathlib.Path(tempfile.mkdtemp())
    try:
        judge = _judge_with_two_rounds(root)
        finished = judge.rounds[(str(root), 1, 0)]
        pending = judge.rounds[(str(root), 2, 0)]
        assert finished.done and not pending.done

        out = judge.undo()
        assert out["undone"] and out["round"] == 2
        assert len(finished.verdicts) == 1, "a finished round must not lose a verdict"
        assert len(pending.verdicts) == 0
    finally:
        shutil.rmtree(root, ignore_errors=True)


def test_reset_clears_one_round():
    import shutil, tempfile

    root = pathlib.Path(tempfile.mkdtemp())
    try:
        judge = _judge_with_two_rounds(root)
        out = judge.reset(1)
        assert out["reset"] == 1
        assert judge.rounds[(str(root), 1, 0)].verdicts == {}
        assert judge.rounds[(str(root), 2, 0)].verdicts != {}, "other rounds are untouched"
    finally:
        shutil.rmtree(root, ignore_errors=True)


def test_verdicts_survive_a_server_restart():
    """Verdicts are a person's attention. Losing them to a process restart is
    the worst thing this server can do, and it has done it once."""
    import json, shutil, tempfile

    root = pathlib.Path(tempfile.mkdtemp())
    try:
        variants = root / "rounds" / "round-1" / "variants"
        for name in ("aaaaaaaa-t0r0", "bbbbbbbb-t0r0"):
            d = variants / name
            d.mkdir(parents=True)
            (d / "events.jsonl").write_text(
                json.dumps({"type": "agent.output.delta", "delta": f"answer {name}"}) + "\n"
            )
        judge = root / "judge"
        judge.mkdir()
        pair_id = "aaaaaaaa-t0r0|bbbbbbbb-t0r0"
        (judge / "verdicts-round-1.json").write_text(json.dumps({pair_id: "b"}))

        rnd = server.Round(root, 1, 0)
        assert rnd.verdicts == {pair_id: "b"}, "a restarted server must reload its verdicts"
        assert rnd.done, "a round whose only pair was judged is finished"
        board = rnd.board()
        assert board[0]["genome"] == "bbbbbbbb", "the reloaded verdict must decide the ranking"
    finally:
        shutil.rmtree(root, ignore_errors=True)



# --------------------------------------------------------------------------
# the vocabulary guard
# --------------------------------------------------------------------------
#
# The rename from operator/generation/trial/evaluation to mutator/round/
# variant was a pre-public breaking change with no compatibility shim. This
# test is what keeps it swept: it greps the module's own sources for the
# retired identifiers, keys, flags and path shapes, so a reintroduction fails
# here instead of in someone's search.
#
# This file is the one source the sweep skips: it has to name the retired
# words to ban them, and a file that must contain them cannot also be checked
# for them. test_the_guard_would_notice covers what that exemption gives up,
# by proving every rule still fires against a line that breaks it.

_OP = "oper" + "ator"
_GEN = "gener" + "ation"

BANNED = [
    (r'"' + _OP + r's"', 'the spec key is "mutators"'),
    (r'"' + _OP + r'"', 'the record key is "mutator"'),
    ("EVOLVE_" + "OPERATOR", "the hook environment variable is EVOLVE_MUTATOR"),
    (r"\b" + _OP + r"s?\b", "tenon's operator is a person; a variation is a mutator"),
    (r'"' + _GEN + r's"', 'the spec key is "rounds"'),
    (r'"' + _GEN + r'"', 'the record key is "round"'),
    ("--" + "generations", "the flag is --rounds"),
    (r"\b" + _GEN + r"s?\b", "tenon generates harness files; a search step is a round"),
    ("max_" + "evaluations", "the budget key is max_variants"),
    (r'"tri' + r'als"', 'the parent-report key is "variants"'),
    (r"\blay" + r"_out\b", "the function is assemble()"),
    ("examples/" + "operators", "the directory is examples/mutators"),
    (r"\bgen-[0-9{]", "the on-disk layout is rounds/round-N"),
    ("verdicts-" + "gen-", "the judge writes verdicts-round-N.json"),
    (_GEN + "-[0-9{]", "the resumed search logs to round-N.log"),
]

# (path, substring) pairs deliberately kept: tenon's own meaning of the word,
# quoted as tenon's.
ALLOWED = {
    ("improve/EVOLVE.md", "the same file generation apply would perform"),
}


def _swept_files():
    root = pathlib.Path(__file__).resolve().parents[2]
    paths = []
    for pattern in ("improve/**/*.py", "improve/**/*.json", "improve/**/*.md",
                    "improve/judge/*.html", ".claude/skills/*/SKILL.md"):
        paths += [p for p in root.glob(pattern)
                  if "__pycache__" not in p.parts and "assets" not in p.parts
                  and p != pathlib.Path(__file__).resolve()]
    assert paths, "the guard found no files to sweep; the glob roots moved"
    return root, sorted(set(paths))


def test_the_retired_vocabulary_is_gone():
    import re

    root, paths = _swept_files()
    survivors = []
    for path in paths:
        rel = path.relative_to(root).as_posix()
        for number, line in enumerate(path.read_text().splitlines(), 1):
            if any(rel == where and text in line for where, text in ALLOWED):
                continue
            for pattern, why in BANNED:
                if re.search(pattern, line, re.IGNORECASE):
                    survivors.append(f"{rel}:{number}: {pattern} — {why}")
    assert not survivors, "retired vocabulary survives:\n" + "\n".join(survivors)


def test_the_guard_would_notice():
    """A guard that matches nothing anywhere is indistinguishable from a guard
    that works, so prove each rule still fires against a line that breaks it."""
    import re

    samples = [
        '"' + _OP + 's": [{"name": "edit"}]',
        '{"' + _OP + '": "prune"}',
        'EVOLVE_' + 'OPERATOR=edit',
        'a variation ' + _OP,
        '"' + _GEN + 's": 6',
        '{"' + _GEN + '": 3}',
        'evolve run --resume --' + 'generations 4',
        'the next ' + _GEN,
        'max_' + 'evaluations: 60',
        '{"tri' + 'als": []}',
        'lay' + '_out(parents, plan, target)',
        'sh examples/' + 'operators/edit-llm.sh',
        'state/run/generations/gen-1/variants',
        'judge/verdicts-' + 'gen-2.json',
        _GEN + '-3.log',
    ]
    assert len(samples) == len(BANNED)
    for (pattern, _), sample in zip(BANNED, samples):
        assert re.search(pattern, sample, re.IGNORECASE), f"{pattern} no longer matches {sample!r}"


if __name__ == "__main__":
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for test in tests:
        test()
        print(f"ok  {test.__name__}")
    print(f"\n{len(tests)} passed")
