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
import os
import re
import subprocess
import sys
import tempfile
import threading
import time
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
SUBCOMMANDS = (
    "check",
    "apply",
    "drift",
    "run",
    "clean",
    "mcp",
    "stage",
    "pins",
    "schedule",
    "fingerprint",
    "validate",
    "plugin",
    "tools",
    "hooks",
)
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
    "--timeout",
    "--force",
    "--input-id",
    "--max-active-turns",
)

# An argv element is a quoted literal followed by whatever ends it: a comma
# when it has a successor, or the bracket that closes the list when it is
# last. `[binary, "clean"]` is as much a tenon invocation as
# `[binary, "clean", ...]`, and the pattern must see both.
#
# Accepting a closing bracket means the pattern must then refuse the two
# shapes that look identical and are not argv: a subscript, `spec["run"]`,
# and a call argument, `record.get("fingerprint", "")`. Both are excluded by
# what precedes the quote — a `[` that follows a name, or a `(` — because
# neither ever precedes an element of a list being built as argv.
_SUBCOMMAND_RE = re.compile(
    r"""(?<![\w.(])(?<![\w\]]\[)(["'])(%s)\1\s*[,\]]""" % "|".join(SUBCOMMANDS)
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
#
# The in-place marker is SCOPED TO THE WORDS IT NAMES. A blanket line
# exemption is the one thing this grep cannot afford: a line marked because
# it mentions the spec key `pins` would go on excusing a real `"apply"` argv
# that lands beside it later. Bare, the marker excuses only the two words
# that are ordinary English or ordinary spec vocabulary; anything else must
# be named, `# not tenon argv: tools, mcp`, and then only those are excused.
_ARGPARSE_RE = re.compile(r"\badd_(argument|parser)\(")
MARKER = "# not tenon argv"
MARKER_DEFAULT = ("run", "pins")
_MARKER_RE = re.compile(re.escape(MARKER) + r"(\s*:\s*([^\u2014]*))?")


def _excused(line: str) -> set:
    """The tokens an in-place marker on this line excuses."""
    found = _MARKER_RE.search(line)
    if not found:
        return set()
    named = (found.group(2) or "").strip()
    if not named:
        return set(MARKER_DEFAULT)
    return {word.strip() for word in named.split(",") if word.strip()}


def confinement_violations(text: str) -> list:
    """Every line of `text` that names a tenon subcommand or flag in argv."""
    out = []
    for number, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if stripped.startswith("#") or _ARGPARSE_RE.search(line):
            continue
        named = {m.group(2) for m in _SUBCOMMAND_RE.finditer(line)}
        named |= {m.group(2) for m in _FLAG_RE.finditer(line)}
        if named - _excused(line):
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
        'argv += ["--timeout", "600s"]',
        'argv += ["--force"]',
        'argv += ["--input-id", ident]',
        'argv += ["--max-active-turns", "4"]',
        'argv = [binary, "schedule", "run", str(agent)]',
        'argv = [binary, "plugin", "status"]',
        'argv = [binary, "validate", str(agent)]',
        'argv = [binary, "fingerprint", "show"]',
        'argv = [binary, "tools", "list"]',
        'argv = [binary, "hooks", "list"]',
        # A subcommand in LAST position closes the list rather than taking a
        # comma. It is the same invocation and must be caught the same way.
        'argv = [binary, "clean"]',
        'proc = spawn([self.binary, "check"])',
    ]
    for line in planted:
        assert confinement_violations(line), f"the confinement grep no longer matches {line!r}"
    # And it does not fire on ordinary prose or identifiers.
    for line in ["def run(self):", "self.run_git(repo)", "# tenon check is the gate", "runs = []"]:
        assert not confinement_violations(line), f"the confinement grep over-matches {line!r}"
    # Nor on the two shapes that a last-position match would otherwise catch:
    # a subscript and a call argument are not argv.
    for line in ['self.spec["run"]', 'record.get("fingerprint", "")', "summary['run']"]:
        assert not confinement_violations(line), f"the confinement grep over-matches {line!r}"
    # The two exemptions apply, and only where they are written.
    assert not confinement_violations('start.add_argument("--harness", help="target harness")')


def test_the_in_place_marker_excuses_only_the_words_it_names():
    """A blanket line exemption is what this grep cannot afford: the line
    marked for its `pins` spec key would go on excusing a real argv that
    lands beside it later."""
    # Bare, the marker covers the two ordinary words and nothing else.
    assert not confinement_violations('pins = args.pins or ""  ' + MARKER)
    assert confinement_violations('argv = [self.fanout, "clean", name]  ' + MARKER)
    # Named, it covers exactly what it names.
    assert not confinement_violations('argv = [self.fanout, "clean", name]  ' + MARKER + ": clean")
    assert not confinement_violations('D = ("skills", "tools", "mcp")  ' + MARKER + ": tools, mcp")
    # And a word it does not name still fires, on the very same line.
    assert confinement_violations(
        'argv = [self.fanout, "clean", "--workspace", ws]  ' + MARKER + ": clean"
    )
    # Prose after an em dash is commentary, not a name.
    assert not confinement_violations(
        'argv = [self.fanout, "clean"]  ' + MARKER + ": clean \u2014 fanout's own subcommand"
    )


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
# replaying a fixture THROUGH the adapter
# --------------------------------------------------------------------------


class FakeProc:
    """A finished tenon process whose output is a recorded fixture.

    It honours the two stream shapes `Tenon` uses: a file handle, which the
    adapter reads back from disk, and a PIPE, which it takes from
    `communicate`. Nothing else about a process is simulated — these tests
    are about the parse, and the process plumbing has tests of its own."""

    def __init__(self, out: bytes, err: bytes, code: int, stdout, stderr):
        self._out, self._err, self.returncode = out, err, code
        self._stdout, self._stderr = stdout, stderr

    def communicate(self, input=None, timeout=None):
        out = err = None
        if hasattr(self._stdout, "write"):
            self._stdout.write(self._out)
        else:
            out = self._out
        if hasattr(self._stderr, "write"):
            self._stderr.write(self._err)
        else:
            err = self._err
        return out, err

    def poll(self):
        return self.returncode

    def wait(self, timeout=None):
        return self.returncode


class Replay:
    """A `spawn` that answers each call with the next recorded fixture.

    The point is that the fixture goes through `Tenon.compile`,
    `Tenon.drifted` and `Tenon.clean` — argv construction, stream capture,
    terminator decoding, dataclass — rather than through a second parser
    written in the test, which would only prove the test agrees with itself."""

    def __init__(self, *names, exit_code: int = 0, err: bytes = b""):
        # A name is a recorded fixture; raw bytes are for the one thing no
        # recording can supply — a terminator word tenon does not emit yet.
        self.streams = [n if isinstance(n, bytes) else fixture(n).encode() for n in names]
        self.exit_code = exit_code
        self.err = err
        self.argv = []

    def __call__(self, argv, *, cwd=None, stdout=None, stderr=None, stdin=None,
                 env=None, new_session=False):
        self.argv.append(list(argv))
        return FakeProc(self.streams.pop(0), self.err, self.exit_code, stdout, stderr)


def replaying(*names, **kw) -> adapter.Tenon:
    return adapter.Tenon("tenon", "claude", spawn=Replay(*names, **kw))


# --------------------------------------------------------------------------
# compile
# --------------------------------------------------------------------------


def test_apply_terminator_becomes_applied():
    applied = replaying("apply-ok").compile(Path("agent"), Path("/work/ws"))
    assert applied.harness == "claude"
    assert applied.fingerprint.startswith("sha256:")
    assert "CLAUDE.md" in applied.written
    # tenon serializes an empty removed list as null, not [].
    assert applied.removed == ()
    assert applied.managed_tools == ("echo",)


def test_apply_gate_failure_is_a_contract_violation_not_a_verdict():
    """check and apply run the same gate, so a source the gate accepted and
    apply rejects is tenon contradicting itself — never a finding about the
    candidate, and never something a caller can mistake for one."""
    t = replaying("check-gate-failed", exit_code=1)
    try:
        t.compile(Path("agent"), Path("/work/ws"))
    except adapter.GateContradiction as err:
        assert err.source_digest.startswith("sha256:")
        assert err.diagnostics and all(d.severity == "error" for d in err.diagnostics)
        assert err.diagnostics[0].id in str(err)
    else:
        raise AssertionError("an apply-side gate failure must raise GateContradiction")


# --------------------------------------------------------------------------
# drift
# --------------------------------------------------------------------------


def drift_report(name: str, **kw) -> adapter.DriftReport:
    return replaying(name, **kw).drifted(Path("agent"), Path("/work/ws"))


def test_drift_ok():
    r = drift_report("drift-ok")
    assert r.matched and "CLAUDE.md" in r.unchanged and r.source_digest == ""


def test_drift_reports_the_changed_paths_and_carries_no_digest():
    r = drift_report("drift-drift", exit_code=1)
    assert not r.matched and not r.gate_failed
    assert r.findings and r.findings[0].path == "CLAUDE.md"
    assert r.source_digest == "", "a drift terminator carries no digest: the source passed"


def test_drift_gate_failed_is_distinguishable_from_drift():
    r = drift_report("drift-gate-failed", exit_code=1)
    assert not r.matched and r.gate_failed
    assert r.source_digest.startswith("sha256:")


def test_drift_with_an_unknown_outcome_is_an_environment_failure():
    """The vocabulary is tenon's to extend. A word this adapter has never
    seen says nothing about the workspace, so it is never reduced to
    `matched=False` — which would read as a drift finding and be scored."""
    t = adapter.Tenon("tenon", "claude", spawn=Replay(b'{"outcome":"rearranged"}\n'))
    try:
        t.drifted(Path("agent"), Path("/work/ws"))
    except adapter.TenonEnvironment as err:
        assert "rearranged" in str(err)
    else:
        raise AssertionError("an unknown drift outcome must raise TenonEnvironment")


# --------------------------------------------------------------------------
# clean
# --------------------------------------------------------------------------


def test_clean_ok_lists_what_it_removed():
    removed = replaying("clean-ok").clean(Path("/work/ws"))
    assert "CLAUDE.md" in removed and len(removed) == 5


def test_clean_blocked_is_a_finding_with_named_paths():
    try:
        replaying("clean-blocked", exit_code=1).clean(Path("/work/ws"))
    except adapter.Blocked as err:
        assert err.paths and err.paths[0]["blocked"] == "CLAUDE.md"
        assert err.paths[0]["reason"] == "modified"
        assert "force=True to overwrite" in str(err), "modified IS forceable; say so"
        assert err.removed == ()
    else:
        raise AssertionError("a blocked clean must raise Blocked")


def test_clean_blocked_on_containment_does_not_advise_forcing():
    """tenon refuses --force for the containment reasons: forcing widens what
    it removes inside a workspace and never where it removes. Advising it
    would be advice that cannot work."""
    try:
        replaying("clean-blocked-containment", exit_code=1).clean(Path("/work/ws"))
    except adapter.Blocked as err:
        message = str(err)
        assert "escapes-workspace" in message
        assert "does not override" in message
        assert "pass force=True" not in message
    else:
        raise AssertionError("a containment-blocked clean must raise Blocked")


def test_a_blocked_clean_carries_what_it_removed_first():
    """tenon re-classifies each path immediately before removing it, so a
    clean can stop partway: the workspace is in neither the before state nor
    the after one, and the removals that already happened are the only record
    of which."""
    try:
        replaying("clean-blocked-partial", exit_code=1).clean(Path("/work/ws"))
    except adapter.Blocked as err:
        assert ".mcp.json" in err.removed and len(err.removed) == 4
        assert err.paths[0]["blocked"] == "CLAUDE.md"
        assert "4 file(s) were removed before it stopped" in str(err)
    else:
        raise AssertionError("a partially blocked clean must raise Blocked")


def test_clean_without_a_record_is_an_environment_failure():
    try:
        replaying("clean-error", exit_code=1).clean(Path("/work/ws"))
    except adapter.TenonEnvironment:
        pass
    else:
        raise AssertionError("clean against a workspace with no record must raise")


def test_clean_passes_force_through_only_when_asked():
    replay = Replay("clean-ok", "clean-ok")
    t = adapter.Tenon("tenon", "claude", spawn=replay)
    t.clean(Path("/work/ws"))
    t.clean(Path("/work/ws"), force=True)
    assert "--force" not in replay.argv[0] and "--force" in replay.argv[1]


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


def test_dispatch_times_out_when_nothing_would_drain_its_pipes():
    """`events_path` and `stderr_path` are optional, and with neither, stdout
    and stderr are PIPEs. A dispatch that wrote the whole payload to stdin
    before arming the clock deadlocks against a child that fills stderr
    first: the parent blocks in write, the child blocks in write, and the
    deadline never fires because it never started. One `communicate` writes
    the input and drains every pipe under one clock, so it cannot happen."""
    binary = sys.executable
    # Floods stderr past a pipe buffer BEFORE reading a line of stdin, then
    # hangs: nothing to parse, nothing to match on but the clock.
    script = (
        "import sys,time;"
        "sys.stderr.write('x' * 400000);"
        "sys.stderr.flush();"
        "sys.stdin.read();"
        "time.sleep(60)"
    )
    spawned = []

    def spawn(argv, cwd=None, new_session=False, **kw):
        proc = subprocess.Popen(
            [binary, "-c", script], start_new_session=new_session, **kw
        )
        spawned.append(proc)
        return proc

    t = adapter.Tenon(binary, "claude", spawn=spawn)
    # A payload larger than a pipe buffer, so the stdin write cannot complete
    # on its own either.
    inputs = [{"input_id": f"i{i}", "text": "y" * 4000} for i in range(50)]
    box = {}

    def run():
        box["started"] = time.time()
        box["result"] = t.dispatch(Path("agent"), Path("/work/ws"), inputs, timeout_s=1)
        box["elapsed"] = time.time() - box["started"]

    worker = threading.Thread(target=run, daemon=True)
    worker.start()
    worker.join(30)
    if worker.is_alive():
        for proc in spawned:
            proc.kill()
        raise AssertionError(
            "dispatch never returned: a pipe nobody drains, or a clock armed "
            "after the payload was written"
        )
    assert box["result"].outcome == "timed_out"
    assert box["elapsed"] < 20, f"the clock took {box['elapsed']:.1f}s to fire"
    for proc in spawned:
        assert proc.poll() is not None, "the dispatch left an orphan behind"


# --------------------------------------------------------------------------
# terminating a tree
# --------------------------------------------------------------------------


def alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def test_terminate_tree_does_not_kill_the_process_doing_the_terminating():
    """A child spawned WITHOUT its own session shares this process's group,
    and `killpg` on that group is suicide: the supervisor SIGTERMs itself,
    its whole thread pool, and in the foreground case the user's shell job.

    The proof runs inside a helper that leads its own session, so a
    regression fails this one test instead of taking the test runner with
    it — which is exactly what the bug does."""
    helper = (
        "import os,subprocess,sys,time\n"
        "sys.path.insert(0, sys.argv[1])\n"
        "import tenon\n"
        "slow = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)'])\n"
        "assert os.getpgid(slow.pid) == os.getpgid(0), 'the child must share our group'\n"
        "tenon.terminate_tree(slow, grace_s=1)\n"
        "print(slow.poll())\n"
        "print('survived', flush=True)\n"
    )
    done = subprocess.run(
        [sys.executable, "-c", helper, str(HERE)],
        capture_output=True, text=True, timeout=60, start_new_session=True,
    )
    assert "survived" in done.stdout, (
        "terminate_tree killed the process that called it; stdout="
        f"{done.stdout!r} stderr={done.stderr!r}"
    )
    status = int(done.stdout.splitlines()[0])
    assert status is not None and status < 0, f"the shared-group child was not signalled: {status}"


def test_terminate_tree_takes_down_a_grandchild_of_a_session_leader():
    """The group is the whole point when the child DOES own one: signalling
    the dispatcher alone leaves the harness it started — and any tool server
    under that — running and burning budget."""
    src = (
        "import subprocess,sys,time;"
        "g=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)']);"
        "print(g.pid, flush=True);"
        "time.sleep(60)"
    )
    proc = subprocess.Popen(
        [sys.executable, "-c", src], stdout=subprocess.PIPE, start_new_session=True
    )
    grandchild = int(proc.stdout.readline())
    try:
        adapter.terminate_tree(proc, grace_s=2)
        assert proc.poll() is not None, "the session leader survived"
        deadline = time.time() + 10
        while alive(grandchild) and time.time() < deadline:
            time.sleep(0.05)
        assert not alive(grandchild), "the grandchild outlived the run that owned it"
    finally:
        proc.stdout.close()
        if alive(grandchild):
            os.kill(grandchild, 9)


# --------------------------------------------------------------------------
# the child environment fanout hands its hooks
# --------------------------------------------------------------------------


def test_fanout_child_env_is_the_adapters():
    """fanout used to keep a private copy of `child_env`. It now calls the
    adapter's, and this pins the behaviour that copy had: the TENON_HARNESS
    pop, the FANOUT_* additions, and the variant's own env winning last.

    It lives with the adapter's tests because it is the adapter's function
    that has to keep the promise."""
    sys.modules.pop("fanout", None)
    import fanout  # noqa: E402

    supervisor = fanout.Supervisor.__new__(fanout.Supervisor)
    supervisor.spec = {"run": "r1", "harness": "claude", "tenon": "/bin/tenon"}
    variant = {"name": "v1", "index": 2, "env": {"FANOUT_HARNESS": "mine", "EXTRA": "1"}}
    os.environ["TENON_HARNESS"] = "codex"
    os.environ["KEEP_ME"] = "yes"
    try:
        env = supervisor.child_env(variant, Path("/work/v1"), Path("/work/a"), Path("/work/ws"))
    finally:
        os.environ.pop("TENON_HARNESS", None)
        os.environ.pop("KEEP_ME", None)
    assert "TENON_HARNESS" not in env, "an inherited harness must not retarget a hook's tenon"
    assert env["KEEP_ME"] == "yes", "the rest of the environment is inherited"
    assert env["FANOUT_RUN"] == "r1" and env["FANOUT_VARIANT"] == "v1"
    assert env["FANOUT_INDEX"] == "2" and env["FANOUT_TENON"] == "/bin/tenon"
    assert env["FANOUT_AGENT_DIR"] == "/work/a" and env["FANOUT_WORKSPACE"] == "/work/ws"
    assert env["FANOUT_VARIANT_DIR"] == "/work/v1"
    assert env["EXTRA"] == "1"
    assert env["FANOUT_HARNESS"] == "mine", "the variant's own env wins last"


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


TIMED_OUT = adapter.Dispatch(outcome="timed_out", turns={"completed": 0})


def test_iterate_records_a_timed_out_run_as_a_finding():
    t = FakeTenon(gate=OK_VERDICT, compile=OK_APPLIED, dispatch=TIMED_OUT, drifted=OK_DRIFT)
    it = t.iterate(Path("a"), Path("w"), [])
    assert not it.ok and it.phase_failed == "run" and it.outcome == "timed_out"


def test_a_timed_out_run_still_gets_its_post_run_drift():
    """A half-finished agent is exactly the one that may have rewritten its
    own configuration, so the drift runs — but the run is still what failed,
    and `timed_out` is still the scored finding."""
    t = FakeTenon(gate=OK_VERDICT, compile=OK_APPLIED, dispatch=TIMED_OUT, drifted=OK_DRIFT)
    it = t.iterate(Path("a"), Path("w"), [])
    assert t.calls == ["gate", "compile", "dispatch", "drifted"]
    assert it.phase_failed == "run" and it.outcome == "timed_out"
    assert it.post_drift is OK_DRIFT


def test_a_timed_out_run_that_also_drifted_reports_both():
    drifted = adapter.DriftReport(matched=False, findings=(adapter.Diagnostic("d", "error"),))
    t = FakeTenon(gate=OK_VERDICT, compile=OK_APPLIED, dispatch=TIMED_OUT, drifted=drifted)
    it = t.iterate(Path("a"), Path("w"), [])
    # The run is what failed; the drift is evidence, not a second phase.
    assert it.phase_failed == "run" and it.outcome == "timed_out"
    assert it.post_drift is drifted and not it.post_drift.matched


def test_a_gate_failed_dispatch_skips_the_post_run_drift():
    """No run happened, so there is nothing for a drift to have observed —
    and a process spent to learn that is a process wasted at search scale."""
    t = FakeTenon(
        gate=OK_VERDICT,
        compile=OK_APPLIED,
        dispatch=adapter.Dispatch(outcome="gate_failed", source_digest="sha256:dd"),
    )
    it = t.iterate(Path("a"), Path("w"), [])
    assert t.calls == ["gate", "compile", "dispatch"]
    assert it.phase_failed == "run" and it.outcome == "gate_failed"
    assert it.post_drift is None and it.source_digest == "sha256:dd"


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
