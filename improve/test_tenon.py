#!/usr/bin/env python3
"""Tests for improve/tenon.py — the adapter, and the confinement it promises.

Two jobs, and they are different in kind.

The **confinement** test greps every other `.py` under `improve/` for a tenon
subcommand or flag in an argv position. The adapter's whole value is that the
next surface change costs one file; nothing enforces that except a test that
fails the moment a second file starts naming the CLI. It is checked against a
planted violation so a rewrite of the pattern cannot quietly stop matching.

The **fixture** tests feed streams recorded from a real tenon binary through
`read_terminator` and the role parsers, and assert the dataclass that comes
out. Recorded rather than hand-written: a fixture someone typed proves the
parser agrees with the spec, and the spec is not what runs.

Stdlib self-runner, no pytest: `python3 improve/test_tenon.py`.
"""

from __future__ import annotations

import json
import re
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import tenon as adapter  # noqa: E402

HERE = Path(__file__).resolve().parent
DATA = HERE / "testdata"


def fixture(name: str) -> str:
    return (DATA / f"{name}.jsonl").read_text()


# --------------------------------------------------------------------------
# confinement
# --------------------------------------------------------------------------

# A tenon subcommand as an argv element: a bare quoted string in a list, which
# is how every one of these was written before the adapter existed. The word
# "run" appears constantly as English and as a Python identifier, so the
# pattern deliberately matches only the quoted-literal form.
SUBCOMMANDS = ("check", "apply", "drift", "run", "clean", "mcp", "stage", "pins")
# The flags are unambiguous: nothing else in this module spells them.
FLAGS = (
    "--format",
    "--pins",
    "--write-pins",
    "--harness",
    "--workspace",
    "--emit",
    "--turn-timeout",
    "--discard-local",
    "--input",
    "--conversation",
    "--model",
)

_SUBCOMMAND_RE = re.compile(
    r"""(?<![\w.])(["'])(%s)\1\s*,""" % "|".join(SUBCOMMANDS)
)
_FLAG_RE = re.compile(
    r"""(["'])(%s)\1""" % "|".join(re.escape(f) for f in FLAGS)
)


# fanout and evolve have command lines of their own, and some of their
# subcommands and flags are spelled the same as tenon's. Two exemptions, both
# narrow and both visible:
#
#   * an argparse line defines OUR CLI, never tenon's argv;
#   * anything else says so in place, on the line, so the exemption is read
#     next to the code it excuses rather than in a list somewhere else.
_ARGPARSE_RE = re.compile(r"\badd_(argument|parser)\(")
MARKER = "# not tenon argv"


def confinement_violations(text: str) -> list:
    """Every line of `text` that names a tenon subcommand or flag in argv."""
    out = []
    for number, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if stripped.startswith("#") or MARKER in line or _ARGPARSE_RE.search(line):
            continue
        if _SUBCOMMAND_RE.search(line) or _FLAG_RE.search(line):
            out.append((number, stripped))
    return out


def module_sources() -> list:
    """Every `.py` under improve/ except the adapter, this test, and the
    fixtures. A hook under examples/ is a user's own program and may call
    tenon however it likes — but nothing here does, and if one starts to, the
    adapter is what it should be reaching for."""
    exempt = {HERE / "tenon.py", HERE / "test_tenon.py"}
    return [
        p
        for p in sorted(HERE.rglob("*.py"))
        if p not in exempt and "__pycache__" not in p.parts
    ]


def test_no_module_but_the_adapter_names_the_cli():
    offenders = []
    for path in module_sources():
        for number, line in confinement_violations(path.read_text()):
            offenders.append(f"{path.relative_to(HERE)}:{number}: {line}")
    assert not offenders, (
        "only improve/tenon.py may name a tenon subcommand or flag; move these "
        "through the adapter:\n  " + "\n  ".join(offenders)
    )


def test_confinement_fires_on_a_planted_violation():
    """The grep is only worth having if it still matches. Plant each shape."""
    planted = [
        'argv = [self.binary, "check", str(agent)]',
        'argv = [tenon, "apply", str(agent)]',
        'argv += ["drift", str(agent)]',
        'proc = spawn([binary, "run", str(agent)])',
        'argv = [binary, "clean", "--workspace", str(ws)]',
        'argv = [binary, "mcp", "serve"]',
        'argv = [binary, "stage", str(agent)]',
        'argv = [binary, "pins", "write"]',
        'argv += ["--format", "jsonl"]',
        'argv += ["--pins", str(pins)]',
        'argv += ["--write-pins", str(path)]',
        'argv += ["--harness", harness]',
        'argv += ["--workspace", str(workspace)]',
        'argv += ["--emit", "files"]',
    ]
    for line in planted:
        assert confinement_violations(line), f"the confinement grep no longer matches {line!r}"
    # And it does not fire on ordinary prose or identifiers.
    for line in ["def run(self):", "self.run_git(repo)", "# tenon check is the gate", "runs = []"]:
        assert not confinement_violations(line), f"the confinement grep over-matches {line!r}"
    # The two exemptions apply, and only where they are written.
    assert not confinement_violations('start.add_argument("--harness", help="target harness")')
    assert not confinement_violations('argv = [self.fanout, "clean", name]  ' + MARKER)
    assert confinement_violations('argv = [self.fanout, "clean", name]')


def test_the_adapter_itself_still_names_the_cli():
    """Inverse of the above: if tenon.py stopped naming the CLI, the grep
    above would pass for the wrong reason."""
    assert confinement_violations((HERE / "tenon.py").read_text())


# --------------------------------------------------------------------------
# the terminator reader — the one decoder
# --------------------------------------------------------------------------


def test_terminator_ok():
    terminator, diagnostics = adapter.read_terminator(fixture("check-ok"))
    assert terminator["outcome"] == "ok"
    assert terminator["fingerprint"].startswith("sha256:")
    assert diagnostics == []


def test_terminator_gate_failed_carries_the_digest_and_the_diagnostics():
    terminator, diagnostics = adapter.read_terminator(fixture("check-gate-failed"))
    assert terminator["outcome"] == "gate_failed"
    assert terminator["source_digest"].startswith("sha256:")
    assert [d.severity for d in diagnostics] == ["error"]
    assert diagnostics[0].id and diagnostics[0].path == "instructions.md"


def test_terminator_error_is_an_environment_failure():
    try:
        adapter.read_terminator(fixture("check-error"), command="gate x")
    except adapter.TenonEnvironment as err:
        assert "gate x" in str(err)
    else:
        raise AssertionError('outcome "error" must raise TenonEnvironment')


def test_a_truncated_stream_is_an_environment_failure_not_a_rejection():
    """The absence of a terminator is indistinguishable from a truncated
    pipe, so it is never read as a verdict about the candidate."""
    try:
        adapter.read_terminator(fixture("check-truncated"))
    except adapter.TenonEnvironment:
        pass
    else:
        raise AssertionError("a stream with no terminator must raise TenonEnvironment")


# --------------------------------------------------------------------------
# gate
# --------------------------------------------------------------------------


def gate_verdict(name: str) -> adapter.Verdict:
    return adapter.Tenon("tenon", "claude")._verdict(fixture(name), "", command="gate")


def test_gate_ok():
    v = gate_verdict("check-ok")
    assert v.ok and v.fingerprint.startswith("sha256:")
    assert v.source_digest == "" and v.errors == () and v.warnings == ()


def test_gate_pins_written_is_echoed():
    v = gate_verdict("check-pins-written")
    assert v.ok and v.pins_written.endswith(".json")


def test_gate_failed_records_the_digest_and_only_error_severities():
    v = gate_verdict("check-gate-failed")
    assert not v.ok
    assert v.source_digest.startswith("sha256:")
    assert len(v.errors) == 1 and v.rejected[0]


def test_gate_failed_with_an_unreadable_root_has_no_digest():
    """`source_digest` is omitted when the agent root itself could not be
    read. That is still a rejection, not an environment failure."""
    v = gate_verdict("check-gate-failed-unreadable")
    assert not v.ok and v.source_digest == "" and len(v.errors) == 1


def test_a_warning_does_not_reject_and_rides_a_passing_verdict():
    v = gate_verdict("check-ok-warning")
    assert v.ok, "a candidate that merely warns must not be discarded"
    assert v.errors == ()
    assert len(v.warnings) == 1 and v.warned[0]


def test_files_records_are_the_authored_inventory():
    records = adapter.read_records(fixture("check-emit-files"))
    assert records and all("path" in r and "hash" in r for r in records)
    assert all("outcome" not in r for r in records)


# --------------------------------------------------------------------------
# compile
# --------------------------------------------------------------------------


def test_apply_terminator_becomes_applied():
    terminator, _ = adapter.read_terminator(fixture("apply-ok"))
    assert terminator["outcome"] == "ok"
    applied = adapter.Applied(
        fingerprint=terminator["fingerprint"],
        agent=terminator.get("agent", ""),
        harness=terminator.get("harness", ""),
        workspace=terminator.get("workspace", ""),
        # tenon serializes an empty removed list as null, not [].
        written=tuple(terminator.get("written") or ()),
        removed=tuple(terminator.get("removed") or ()),
        managed_tools=tuple(terminator.get("managed_tools") or ()),
    )
    assert applied.harness == "claude"
    assert "CLAUDE.md" in applied.written
    assert applied.removed == ()


# --------------------------------------------------------------------------
# drift
# --------------------------------------------------------------------------


def drift_report(name: str) -> adapter.DriftReport:
    """The parse `drifted` performs, over a recorded stream."""
    terminator, diagnostics = adapter.read_terminator(fixture(name))
    outcome = terminator["outcome"]
    if outcome == "ok":
        return adapter.DriftReport(
            matched=True,
            fingerprint=terminator.get("fingerprint", ""),
            unchanged=tuple(terminator.get("unchanged") or ()),
        )
    if outcome == "drift":
        return adapter.DriftReport(matched=False, findings=tuple(diagnostics))
    return adapter.DriftReport(
        matched=False,
        gate_failed=True,
        source_digest=terminator.get("source_digest", ""),
        findings=tuple(d for d in diagnostics if d.severity == "error"),
    )


def test_drift_ok():
    r = drift_report("drift-ok")
    assert r.matched and "CLAUDE.md" in r.unchanged and r.source_digest == ""


def test_drift_reports_the_changed_paths_and_carries_no_digest():
    r = drift_report("drift-drift")
    assert not r.matched and not r.gate_failed
    assert r.findings and r.findings[0].path == "CLAUDE.md"
    assert r.source_digest == "", "a drift terminator carries no digest: the source passed"


def test_drift_gate_failed_is_distinguishable_from_drift():
    r = drift_report("drift-gate-failed")
    assert not r.matched and r.gate_failed
    assert r.source_digest.startswith("sha256:")


# --------------------------------------------------------------------------
# clean
# --------------------------------------------------------------------------


def test_clean_ok_lists_what_it_removed():
    terminator, _ = adapter.read_terminator(fixture("clean-ok"))
    removed = [r["removed"] for r in adapter.read_records(fixture("clean-ok")) if r.get("removed")]
    assert terminator["outcome"] == "ok"
    assert terminator["removed"] == len(removed) and "CLAUDE.md" in removed


def test_clean_blocked_is_a_finding_with_named_paths():
    terminator, _ = adapter.read_terminator(fixture("clean-blocked"))
    blocked = [r for r in adapter.read_records(fixture("clean-blocked")) if r.get("blocked")]
    assert terminator["outcome"] == "blocked"
    assert blocked[0]["blocked"] == "CLAUDE.md" and blocked[0]["reason"] == "modified"


def test_clean_without_a_record_is_an_environment_failure():
    try:
        adapter.read_terminator(fixture("clean-error"))
    except adapter.TenonEnvironment:
        pass
    else:
        raise AssertionError("clean against a workspace with no record must raise")


# --------------------------------------------------------------------------
# dispatch
# --------------------------------------------------------------------------


def test_run_completion_and_the_reduction_agree():
    text = fixture("run-recovered-uncertain")
    completion = adapter.read_run_completion(text)
    per_input = adapter.summarize_turns(text)
    assert completion["outcome"] == "ok"
    reduced = adapter.reduce_counts(per_input)
    assert {k: v for k, v in completion["turns"].items() if v} == reduced


def test_a_startup_recovered_uncertain_is_excluded_from_the_reduction():
    """A dispatcher terminalizes the turns a previous one abandoned before it
    accepts an input of its own, and leaves them out of run.completed's
    counts. The reduction must draw the same line, or a complete stream reads
    as a truncated one."""
    text = fixture("run-recovered-uncertain")
    events = [json.loads(l) for l in text.splitlines() if l.strip()]
    assert events[0]["type"] == "turn.uncertain", "fixture must lead with a recovered turn"
    recovered = events[0]["input_id"]
    per_input = adapter.summarize_turns(text)
    assert recovered not in [r["input_id"] for r in per_input]
    assert [r["status"] for r in per_input] == ["completed"]


def test_run_gate_failed_carries_the_digest_and_no_fingerprint():
    completion = adapter.read_run_completion(fixture("run-gate-failed"))
    assert completion["outcome"] == "gate_failed"
    assert completion["source_digest"].startswith("sha256:")
    assert completion.get("fingerprint", "") == ""


def test_a_tenon_side_deadline_is_an_environment_failure_here():
    """tenon's own --timeout overrun ends `outcome: "error"` with prose this
    adapter refuses to parse. That is exactly why the adapter enforces the
    wall clock itself and reports `timed_out` — see `Tenon.dispatch`."""
    try:
        adapter.read_run_completion(fixture("run-error-deadline"))
    except adapter.TenonEnvironment as err:
        assert "deadline" in str(err), "the fixture should be tenon's own deadline path"
    else:
        raise AssertionError('run outcome "error" must raise TenonEnvironment')


def test_dispatch_times_out_without_reading_any_error_prose():
    """The wall clock is the adapter's. A process that outlives it is
    terminated and reported as a finding about the variant."""
    binary = sys.executable
    with tempfile.TemporaryDirectory() as tmp:
        events = Path(tmp) / "events.jsonl"
        t = adapter.Tenon(binary, "claude")
        # A stand-in for tenon that emits one event and then hangs: no
        # terminator, no prose, nothing to match on but the clock.
        script = (
            "import sys,time,json;"
            "sys.stdin.read();"
            "print(json.dumps({'type':'input.accepted','input_id':'i1'}),flush=True);"
            "time.sleep(60)"
        )
        def spawn(argv, cwd=None, new_session=False, **kw):
            return __import__("subprocess").Popen(
                [binary, "-c", script], start_new_session=new_session, **kw
            )

        t._spawn = spawn
        result = t.dispatch(
            Path("agent"), Path(tmp), [{"input_id": "i1", "text": "x"}],
            timeout_s=1, events_path=events,
        )
    assert result.outcome == "timed_out"
    assert result.per_input == () and result.turns == {}


def test_a_truncated_event_stream_is_an_environment_failure():
    try:
        adapter.read_run_completion("")
    except adapter.TenonEnvironment:
        pass
    else:
        raise AssertionError("no run.completed must raise TenonEnvironment")


# --------------------------------------------------------------------------
# iterate
# --------------------------------------------------------------------------


class FakeTenon(adapter.Tenon):
    """An adapter whose roles are scripted, so `iterate`'s sequencing can be
    asserted without a binary. It overrides the roles, never the parsers —
    those are covered by the fixtures above."""

    def __init__(self, **scripted):
        super().__init__("tenon", "claude")
        self.scripted = scripted
        self.calls = []

    def gate(self, agent, **kw):
        self.calls.append("gate")
        return self._answer("gate")

    def compile(self, agent, workspace, **kw):
        self.calls.append("compile")
        return self._answer("compile")

    def drifted(self, agent, workspace, **kw):
        self.calls.append("drifted")
        return self._answer("drifted")

    def dispatch(self, agent, workspace, inputs, **kw):
        self.calls.append("dispatch")
        return self._answer("dispatch")

    def _answer(self, role):
        value = self.scripted[role]
        if isinstance(value, list):
            value = value.pop(0)
        if isinstance(value, Exception):
            raise value
        return value


OK_VERDICT = adapter.Verdict(ok=True, fingerprint="sha256:aa")
OK_APPLIED = adapter.Applied(fingerprint="sha256:aa", written=("CLAUDE.md",))
OK_DRIFT = adapter.DriftReport(matched=True, fingerprint="sha256:aa")
OK_RUN = adapter.Dispatch(outcome="ok", fingerprint="sha256:aa", turns={"completed": 1})


def test_iterate_default_skips_the_pre_run_drift():
    """apply just enumerated exactly what it wrote, so a drift immediately
    after it against an untouched workspace cannot differ."""
    t = FakeTenon(gate=OK_VERDICT, compile=OK_APPLIED, dispatch=OK_RUN, drifted=OK_DRIFT)
    it = t.iterate(Path("a"), Path("w"), [])
    assert it.ok and it.phase_failed == ""
    assert t.calls == ["gate", "compile", "dispatch", "drifted"]
    assert it.pre_drift is None and it.post_drift is OK_DRIFT


def test_iterate_paranoid_mode_runs_both_drifts():
    t = FakeTenon(
        gate=OK_VERDICT, compile=OK_APPLIED, dispatch=OK_RUN, drifted=[OK_DRIFT, OK_DRIFT]
    )
    it = t.iterate(Path("a"), Path("w"), [], verify_pre_drift=True)
    assert it.ok and t.calls == ["gate", "compile", "drifted", "dispatch", "drifted"]


def test_iterate_stops_at_check_and_touches_no_workspace():
    rejected = adapter.Verdict(
        ok=False, source_digest="sha256:bb", errors=(adapter.Diagnostic("x.y", "error"),)
    )
    t = FakeTenon(gate=rejected)
    it = t.iterate(Path("a"), Path("w"), [])
    assert not it.ok and it.phase_failed == "check" and it.outcome == "gate_failed"
    assert it.source_digest == "sha256:bb" and t.calls == ["gate"]


def test_iterate_names_an_apply_gate_failure_as_a_contract_violation():
    t = FakeTenon(
        gate=OK_VERDICT,
        compile=adapter.GateContradiction("boom", source_digest="sha256:cc"),
    )
    it = t.iterate(Path("a"), Path("w"), [])
    assert it.phase_failed == "apply" and it.source_digest == "sha256:cc"


def test_iterate_records_a_timed_out_run_as_a_finding():
    t = FakeTenon(
        gate=OK_VERDICT,
        compile=OK_APPLIED,
        dispatch=adapter.Dispatch(outcome="timed_out", turns={"completed": 0}),
    )
    it = t.iterate(Path("a"), Path("w"), [])
    assert not it.ok and it.phase_failed == "run" and it.outcome == "timed_out"


def test_iterate_lets_an_environment_failure_raise_out():
    """phase_failed records findings. An environment failure is not a phase
    result and must never be written to lineage."""
    t = FakeTenon(gate=OK_VERDICT, compile=adapter.TenonEnvironment("disk full"))
    try:
        t.iterate(Path("a"), Path("w"), [])
    except adapter.TenonEnvironment:
        pass
    else:
        raise AssertionError("an environment failure must raise out of iterate")


def test_iterate_reports_post_run_drift():
    t = FakeTenon(
        gate=OK_VERDICT,
        compile=OK_APPLIED,
        dispatch=OK_RUN,
        drifted=adapter.DriftReport(matched=False, findings=(adapter.Diagnostic("d", "error"),)),
    )
    it = t.iterate(Path("a"), Path("w"), [])
    assert it.phase_failed == "post_drift" and it.outcome == "drift"
    assert it.dispatch is OK_RUN


if __name__ == "__main__":
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for test in tests:
        test()
        print(f"ok  {test.__name__}")
    print(f"\n{len(tests)} passed")
