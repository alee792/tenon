#!/usr/bin/env python3
"""tenon — the improve module's adapter over the tenon CLI.

This is the ONLY module in `improve/` that may name a tenon subcommand or a
tenon flag. `improve/test_tenon.py` enforces that with a grep over every
other `.py` here, so the next time tenon's surface moves the diff is one
file and the fixtures under `improve/testdata/` say whether it still parses.

The roles are named for what the caller wants, never for the subcommand that
currently provides it. When `fingerprint show` folded into `check`, the
caller's vocabulary should not have had to move; `gate`, `identity`,
`compile`, `drifted` and `dispatch` are the vocabulary that does not.

  gate       prove a source for a harness, and mint its fingerprint
  identity   the same proof without a harness — a portable fingerprint
  files      the authored inventory of a proven source
  catalog    the resolved capability inventory of a proven source
  compile    write the harness-native configuration into a workspace
  drifted    ask whether a workspace still matches what compile would write
  dispatch   run bounded turns against a compiled workspace
  clean      remove what compile wrote, using the workspace's own record
  iterate    the composite: gate, compile, [drift], dispatch, drift

Three rules hold across all of them.

**`outcome` is the authority.** Every jsonl-mode command terminates with one
object carrying `outcome`; `run`'s terminator is a `run.completed` event.
Exit 1 covers both a rejected source and a broken environment, so the exit
code cannot substitute, and neither can a field's presence — `fingerprint`
appears on more than one shape and a diagnostic `id` appears on warnings.
`read_terminator` is the single place that vocabulary is decoded.

**A finding is not an environment failure.** `gate_failed`, `drift` and
`blocked` are findings: they say something true about the source or the
workspace, and a search records them. `outcome: "error"` — and a stream that
ends with no terminator at all, which is indistinguishable from a truncated
pipe — is `TenonEnvironment`: it says nothing about the candidate and must
never reach a leaderboard.

**Diagnostic ids are opaque.** They are recorded, rendered, and handed to a
mutator. Nothing here branches on one, parses its dotted segments, or maps it
to a category.
"""

from __future__ import annotations

import json
import os
import signal
import subprocess
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Iterable

# How long a terminated `run` is given to exit on its own before it is
# killed. tenon flushes its terminator and closes its harness child on
# SIGTERM; the grace is for that, not for the model.
TERMINATE_GRACE_S = 10.0


class TenonEnvironment(Exception):
    """tenon terminated with `outcome: "error"`, or its stream carried no
    terminator at all.

    The environment failed, not the candidate: an unreadable pin set, an
    unwritable path, a harness that would not start. Retry or escalate. It is
    never a score and never a lineage rejection — a search whose leaderboard
    absorbs infrastructure noise is measuring the wrong thing."""

    def __init__(self, message: str, *, command: str = ""):
        self.command = command
        super().__init__(f"{command}: {message}" if command else message)


class Blocked(Exception):
    """`clean` refused because a recorded file was modified since apply.

    A FINDING about the workspace, not an environment failure: the workspace
    really does hold an edit nobody adopted, and the caller decides whether
    to force. Distinct from TenonEnvironment, which `clean` reserves for a
    workspace with no apply record for the harness at all."""

    def __init__(self, message: str, *, paths: tuple = ()):
        self.paths = paths
        super().__init__(message)


@dataclass(frozen=True)
class Diagnostic:
    """One machine-readable finding. `id` is opaque — record it, render it,
    hand it to a mutator; never branch on it."""

    id: str
    severity: str = ""  # "error" | "warning"
    path: str = ""
    rule: str = ""
    detail: str = ""

    @staticmethod
    def of(obj: dict) -> "Diagnostic":
        return Diagnostic(
            id=obj.get("id", ""),
            severity=obj.get("severity", ""),
            path=obj.get("path", ""),
            rule=obj.get("rule", ""),
            detail=obj.get("detail", ""),
        )


@dataclass(frozen=True)
class Verdict:
    """The gate's answer, as one record.

    Three outcomes do not fit in two fields. Exactly one of `fingerprint` and
    `source_digest` is meaningful — and `source_digest` is empty when the
    agent root itself could not be read, which is a rejection all the same.
    Warnings ride along on a *passing* verdict so a mutator can still see
    what the gate grumbled about."""

    ok: bool
    fingerprint: str = ""
    source_digest: str = ""
    errors: tuple = ()  # tuple[Diagnostic, ...], severity "error"
    warnings: tuple = ()  # tuple[Diagnostic, ...], severity "warning"
    pins_written: str = ""

    @property
    def rejected(self) -> tuple:
        """The opaque ids of what was rejected, for lineage and logs."""
        return tuple(d.id for d in self.errors)

    @property
    def warned(self) -> tuple:
        return tuple(d.id for d in self.warnings)


@dataclass(frozen=True)
class Applied:
    """What `compile` put in a workspace. `written`/`removed` enumerate it
    exactly, which is why `iterate` can skip the pre-run drift."""

    fingerprint: str = ""
    agent: str = ""
    harness: str = ""
    workspace: str = ""
    written: tuple = ()
    removed: tuple = ()
    managed_tools: tuple = ()


@dataclass(frozen=True)
class DriftReport:
    """Whether a workspace still carries what a fresh compile would write.

    `matched` is `outcome == "ok"`. A drifted workspace carries the per-path
    findings; a source that failed the gate carries `source_digest` and
    `gate_failed=True` instead — drift runs the same gate check does."""

    matched: bool
    fingerprint: str = ""
    unchanged: tuple = ()
    findings: tuple = ()
    source_digest: str = ""
    gate_failed: bool = False


@dataclass(frozen=True)
class Dispatch:
    """One `dispatch`, reduced.

    `outcome` is `"ok"`, `"gate_failed"`, or `"timed_out"`. `"ok"` means the
    dispatcher completed every turn it was given, whatever those turns did —
    score from `turns`, never from `outcome`. `"timed_out"` is this adapter's
    own: the wall clock expired and the process was terminated. It is a
    finding about the variant (see `dispatch`), so it is scored as a failed
    variant rather than raised."""

    outcome: str
    fingerprint: str = ""
    turns: dict = field(default_factory=dict)
    per_input: tuple = ()
    source_digest: str = ""
    events_path: str = ""
    exit_code: int | None = None


@dataclass(frozen=True)
class Iteration:
    """One full pass over a candidate: gate, compile, dispatch, drift.

    `phase_failed` names the phase whose FINDING ended the pass. An
    environment failure is not a phase result — it raises out of `iterate`
    entirely, so nothing here can be written to lineage as evidence."""

    ok: bool
    phase_failed: str = ""  # "" | "check" | "apply" | "pre_drift" | "run" | "post_drift"
    outcome: str = ""
    fingerprint: str = ""
    source_digest: str = ""
    diagnostics: tuple = ()
    verdict: Verdict | None = None
    applied: Applied | None = None
    pre_drift: DriftReport | None = None
    dispatch: Dispatch | None = None
    post_drift: DriftReport | None = None


# --------------------------------------------------------------------------
# the one decoder
# --------------------------------------------------------------------------


def _objects(text: str) -> list:
    out = []
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict):
            out.append(obj)
    return out


def read_terminator(text: str, *, command: str = "") -> tuple:
    """Split a jsonl-mode tenon stream into its terminator and everything
    before it.

    Returns `(terminator, [Diagnostic, ...])`. The terminator is the last
    object carrying `outcome`; objects before it that carry an `id` are
    diagnostics. Objects that are neither — `--emit` lines, clean's
    `blocked`/`removed` lines — are the caller's business and are reached
    through `read_records`.

    A stream with no terminator is a truncated pipe, and a truncated pipe is
    never a verdict about a candidate: it raises TenonEnvironment. So does
    `outcome: "error"`; this is the single place that decision is made, so
    every role inherits it by construction.
    """
    terminator: dict = {}
    diagnostics: list = []
    for obj in _objects(text):
        if obj.get("outcome"):
            terminator = obj
        elif obj.get("id"):
            diagnostics.append(Diagnostic.of(obj))
    if not terminator:
        raise TenonEnvironment("ended with no outcome; the stream is truncated", command=command)
    if terminator["outcome"] == "error":
        raise TenonEnvironment(terminator.get("error", "") or "unknown", command=command)
    return terminator, diagnostics


def read_records(text: str) -> tuple:
    """Every non-diagnostic, non-terminator object in a stream, in order:
    `--emit files`/`--emit catalog` entries, clean's per-path lines."""
    return tuple(o for o in _objects(text) if not o.get("outcome") and not o.get("id"))


def read_run_completion(text: str, *, command: str = "") -> dict:
    """The last `run.completed` event in a dispatch stream.

    `run` has no `--format`: its stdout *is* the event stream, and its
    terminator is an event like any other, carrying the dispatch's outcome,
    the turn counts, and on failure the prose or the rejected source's
    digest. Absent, the stream is truncated. `outcome: "error"` raises, for
    the same reason it does in `read_terminator`."""
    completion: dict = {}
    for obj in _objects(text):
        if obj.get("type") == "run.completed":
            completion = obj
    if not completion:
        raise TenonEnvironment("emitted no run.completed event; the stream is truncated", command=command)
    if completion.get("outcome") == "error":
        raise TenonEnvironment(completion.get("error", "") or "unknown", command=command)
    return completion


def summarize_turns(text: str) -> tuple:
    """Reduce a dispatch event stream to one terminal record per input id.

    `run.completed`'s `turns` is counts only, so the per-input reduction is
    still needed and is cross-checked against those counts.

    A dispatcher terminalizes any input a previous one abandoned before it
    accepts an input of its own, emitting `turn.uncertain` for each, and
    deliberately leaves those out of `run.completed`'s counts: they belong to
    the run that abandoned them. This reduction draws the same line — the
    first `input.accepted` — or a complete stream reads as a truncated one."""
    terminal = {
        "turn.completed": "completed",
        "turn.failed": "failed",
        "turn.cancelled": "cancelled",
        "turn.uncertain": "uncertain",
        "driver.process_failed": "driver_failed",
    }
    order: list = []
    seen: dict = {}
    accepted = False
    for event in _objects(text):
        kind = event.get("type")
        if kind == "input.accepted":
            accepted = True
            continue
        if kind not in terminal or not accepted:
            continue
        input_id = event.get("input_id") or ""
        if input_id not in seen:
            order.append(input_id)
        seen[input_id] = {
            "input_id": input_id,
            "status": terminal[kind],
            "reason": event.get("reason", ""),
            "session_id": event.get("session_id", ""),
            "fingerprint": event.get("fingerprint", ""),
        }
    return tuple(seen[i] for i in order)


def reduce_counts(per_input: Iterable[dict]) -> dict:
    """The per-input reduction as `run.completed`-shaped counts."""
    counts: dict = {}
    for record in per_input:
        key = "process_failed" if record["status"] == "driver_failed" else record["status"]
        counts[key] = counts.get(key, 0) + 1
    return counts


# --------------------------------------------------------------------------
# the adapter
# --------------------------------------------------------------------------


class Tenon:
    """A tenon binary bound to a harness.

    `spawn` exists so a caller that supervises its own children — fanout
    tracks every live process so a cancelled run leaves nothing behind — can
    keep doing that while still routing the argv through here."""

    def __init__(
        self,
        binary: str,
        harness: str = "",
        *,
        env: dict | None = None,
        cwd: Path | None = None,
        spawn=None,
    ):
        self.binary = str(binary)
        self.harness = harness
        self.env = env
        self.cwd = cwd
        self._spawn = spawn

    # -- process plumbing --------------------------------------------------

    def _popen(
        self, argv: list, *, cwd: Path | None, stdout, stderr, stdin=None, env=None,
        new_session: bool = False,
    ):
        if self._spawn is not None:
            return self._spawn(
                argv, cwd=cwd, stdout=stdout, stderr=stderr, stdin=stdin, env=env,
                new_session=new_session,
            )
        return subprocess.Popen(
            argv,
            cwd=str(cwd) if cwd else None,
            stdout=stdout,
            stderr=stderr,
            stdin=stdin,
            env=env,
            start_new_session=new_session,
        )

    def _capture(
        self,
        argv: list,
        *,
        cwd: Path | None = None,
        out_path: Path | None = None,
        err_path: Path | None = None,
    ) -> tuple:
        """Run one tenon command to completion and return `(stdout, stderr,
        exit code)` as text.

        `out_path`/`err_path` tee the streams to files instead of a pipe, for
        a caller that keeps the raw stream as the audit record. The text is
        read back either way, so every role parses the same thing."""
        out_handle = Path(out_path).open("wb") if out_path else subprocess.PIPE
        err_handle = Path(err_path).open("wb") if err_path else subprocess.PIPE
        try:
            proc = self._popen(
                argv,
                cwd=cwd if cwd is not None else self.cwd,
                stdout=out_handle,
                stderr=err_handle,
                env=self.env,
            )
            out, err = proc.communicate()
        finally:
            for handle in (out_handle, err_handle):
                if hasattr(handle, "close"):
                    handle.close()
        if out_path:
            out = Path(out_path).read_text(errors="replace")
        if err_path:
            err = Path(err_path).read_text(errors="replace")
        return _text(out), _text(err), proc.returncode

    def _harness_args(self, harness: str | None) -> list:
        name = self.harness if harness is None else harness
        return ["--harness", name] if name else []

    # -- gate --------------------------------------------------------------

    def _check_argv(
        self,
        agent: Path,
        *,
        harness: str | None,
        pins: Path | None,
        write_pins: Path | None,
        model: str,
        emit: str,
    ) -> list:
        argv = [self.binary, "check", str(agent), *self._harness_args(harness)]
        if emit:
            argv += ["--emit", emit]
        if pins:
            argv += ["--pins", str(pins)]
        if write_pins:
            argv += ["--write-pins", str(write_pins)]
        if model:
            argv += ["--model", model]
        return argv + ["--format", "jsonl"]

    def _verdict(self, text: str, stderr: str, *, command: str) -> Verdict:
        try:
            terminator, diagnostics = read_terminator(text, command=command)
        except TenonEnvironment as err:
            # tenon's own prose on stderr is the only detail an environment
            # failure has when the stream itself was cut short.
            raise TenonEnvironment(_with_detail(err.args[0], stderr), command="") from None
        errors = tuple(d for d in diagnostics if d.severity == "error")
        warnings = tuple(d for d in diagnostics if d.severity == "warning")
        if terminator["outcome"] == "ok":
            return Verdict(
                ok=True,
                fingerprint=terminator.get("fingerprint", ""),
                warnings=warnings,
                pins_written=terminator.get("pins_written", ""),
            )
        if terminator["outcome"] == "gate_failed":
            return Verdict(
                ok=False,
                source_digest=terminator.get("source_digest", ""),
                errors=errors,
                warnings=warnings,
            )
        raise TenonEnvironment(f"unknown outcome {terminator['outcome']!r}", command=command)

    def gate(
        self,
        agent: Path,
        *,
        pins: Path | None = None,
        write_pins: Path | None = None,
        model: str = "",
        harness: str | None = None,
        cwd: Path | None = None,
        log: Path | None = None,
        errlog: Path | None = None,
    ) -> Verdict:
        """Prove a source for this harness and mint its identity, in one call.

        Pins are written by the gate when `write_pins` is given, so there is
        no ordering to get wrong between proving a source and pinning it —
        and the terminator names the path it wrote, which is the claim that
        matters rather than exit 0. tenon requires `--harness` alongside
        `--write-pins`, and `--write-pins` alongside `--model`.

        Raises TenonEnvironment on `outcome: "error"` or a truncated stream.
        """
        argv = self._check_argv(
            agent, harness=harness, pins=pins, write_pins=write_pins, model=model, emit=""
        )
        out, err, _ = self._capture(argv, cwd=cwd, out_path=log, err_path=errlog)
        verdict = self._verdict(out, err, command=f"gate {agent}")
        if write_pins and verdict.ok and verdict.pins_written != str(write_pins):
            raise TenonEnvironment(
                f"wrote pins to {verdict.pins_written or 'nothing'} rather than {write_pins}",
                command=f"gate {agent}",
            )
        return verdict

    def identity(self, agent: Path, *, cwd: Path | None = None) -> Verdict:
        """The portable gate: no harness. Same command, same terminator, and
        a fingerprint that names the source rather than a compilation of it.
        Use it when the caller wants an identity and has not chosen a harness
        yet."""
        argv = self._check_argv(
            agent, harness="", pins=None, write_pins=None, model="", emit=""
        )
        out, err, _ = self._capture(argv, cwd=cwd)
        return self._verdict(out, err, command=f"identity {agent}")

    def files(self, agent: Path, *, harness: str | None = None, cwd: Path | None = None) -> tuple:
        """The authored inventory: one record per authored file, with its
        hash. A separate call because a loop gating thousands of candidates
        must not pay to serialize an inventory it discards."""
        argv = self._check_argv(
            agent, harness=harness, pins=None, write_pins=None, model="", emit="files"
        )
        out, err, _ = self._capture(argv, cwd=cwd)
        verdict = self._verdict(out, err, command=f"files {agent}")
        return read_records(out) if verdict.ok else ()

    def catalog(self, agent: Path, *, harness: str | None = None, cwd: Path | None = None) -> tuple:
        """The resolved capability inventory: skills, tools, MCP servers as
        the gate resolved them. Nothing calls this yet — it is what a
        coherence check over a crossover child will read."""
        argv = self._check_argv(
            agent, harness=harness, pins=None, write_pins=None, model="", emit="catalog"
        )
        out, err, _ = self._capture(argv, cwd=cwd)
        verdict = self._verdict(out, err, command=f"catalog {agent}")
        return read_records(out) if verdict.ok else ()

    # -- compile -----------------------------------------------------------

    def compile(
        self,
        agent: Path,
        workspace: Path,
        *,
        pins: Path | None = None,
        discard_local: bool = False,
        harness: str | None = None,
        cwd: Path | None = None,
        log: Path | None = None,
        errlog: Path | None = None,
    ) -> Applied:
        """Write the harness-native configuration into a workspace.

        A gate failure here, on a source `gate` accepted moments ago with the
        same harness and pins, is a contract violation rather than a finding:
        check and apply run the same gate. It is raised as GateContradiction
        so a caller cannot mistake it for a verdict."""
        argv = [self.binary, "apply", str(agent), *self._harness_args(harness),
                "--workspace", str(workspace)]
        if pins:
            argv += ["--pins", str(pins)]
        if discard_local:
            argv += ["--discard-local"]
        argv += ["--format", "jsonl"]
        out, err, _ = self._capture(argv, cwd=cwd, out_path=log, err_path=errlog)
        terminator, diagnostics = self._terminator_or_env(out, err, command=f"compile {agent}")
        if terminator["outcome"] == "ok":
            return Applied(
                fingerprint=terminator.get("fingerprint", ""),
                agent=terminator.get("agent", ""),
                harness=terminator.get("harness", ""),
                workspace=terminator.get("workspace", ""),
                # tenon omits an empty list as JSON null, not [].
                written=tuple(terminator.get("written") or ()),
                removed=tuple(terminator.get("removed") or ()),
                managed_tools=tuple(terminator.get("managed_tools") or ()),
            )
        raise GateContradiction(
            f"compile rejected a source the gate accepted with the same harness and pins: "
            f"{','.join(d.id for d in diagnostics if d.severity == 'error') or 'no diagnostics'}; "
            f"source_digest={terminator.get('source_digest') or 'unknown'}",
            source_digest=terminator.get("source_digest", ""),
            diagnostics=tuple(d for d in diagnostics if d.severity == "error"),
        )

    def _terminator_or_env(self, text: str, stderr: str, *, command: str) -> tuple:
        try:
            return read_terminator(text, command=command)
        except TenonEnvironment as err:
            raise TenonEnvironment(_with_detail(err.args[0], stderr), command="") from None

    # -- drift -------------------------------------------------------------

    def drifted(
        self,
        agent: Path,
        workspace: Path,
        *,
        pins: Path | None = None,
        harness: str | None = None,
        cwd: Path | None = None,
        log: Path | None = None,
        errlog: Path | None = None,
    ) -> DriftReport:
        """Does the workspace still carry exactly what a fresh compile would
        write? `matched=False` with findings means it does not; a source that
        fails the gate reports `gate_failed` with its digest instead."""
        argv = [self.binary, "drift", str(agent), *self._harness_args(harness),
                "--workspace", str(workspace)]
        if pins:
            argv += ["--pins", str(pins)]
        argv += ["--format", "jsonl"]
        out, err, _ = self._capture(argv, cwd=cwd, out_path=log, err_path=errlog)
        terminator, diagnostics = self._terminator_or_env(out, err, command=f"drifted {agent}")
        outcome = terminator["outcome"]
        if outcome == "ok":
            return DriftReport(
                matched=True,
                fingerprint=terminator.get("fingerprint", ""),
                unchanged=tuple(terminator.get("unchanged") or ()),
            )
        if outcome == "drift":
            # tenon states plainly that a drift terminator carries no digest:
            # the source passed the gate, so it has a fingerprint instead.
            return DriftReport(matched=False, findings=tuple(diagnostics))
        if outcome == "gate_failed":
            return DriftReport(
                matched=False,
                gate_failed=True,
                source_digest=terminator.get("source_digest", ""),
                findings=tuple(d for d in diagnostics if d.severity == "error"),
            )
        raise TenonEnvironment(f"unknown outcome {outcome!r}", command=f"drifted {agent}")

    # -- clean -------------------------------------------------------------

    def clean(
        self,
        workspace: Path,
        *,
        force: bool = False,
        harness: str | None = None,
        cwd: Path | None = None,
    ) -> tuple:
        """Remove what compile wrote, from the workspace's own apply record.

        Returns the removed paths. `blocked` — a recorded file was modified
        since apply — raises Blocked, which is a finding about the workspace
        and has a remedy (`force=True`). A workspace with no record for the
        harness is `outcome: "error"` and raises TenonEnvironment: those two
        refusals have different remedies, so they are different exceptions."""
        argv = [self.binary, "clean", "--workspace", str(workspace), *self._harness_args(harness)]
        if force:
            argv += ["--force"]
        argv += ["--format", "jsonl"]
        out, err, _ = self._capture(argv, cwd=cwd)
        terminator, _ = self._terminator_or_env(out, err, command=f"clean {workspace}")
        records = read_records(out)
        if terminator["outcome"] == "ok":
            return tuple(r.get("removed", "") for r in records if r.get("removed"))
        if terminator["outcome"] == "blocked":
            blocked = tuple(r for r in records if r.get("blocked"))
            names = ", ".join(r.get("blocked", "") for r in blocked[:4]) or "unknown"
            raise Blocked(
                f"clean refused {workspace}: {names} changed since apply; pass force=True to overwrite",
                paths=blocked,
            )
        raise TenonEnvironment(
            f"unknown outcome {terminator['outcome']!r}", command=f"clean {workspace}"
        )

    # -- dispatch ----------------------------------------------------------

    def dispatch(
        self,
        agent: Path,
        workspace: Path,
        inputs: Iterable[dict],
        *,
        pins: Path | None = None,
        conversation: str = "",
        timeout_s: int = 600,
        turn_timeout_s: int = 0,
        events_path: Path | None = None,
        stderr_path: Path | None = None,
        harness: str | None = None,
        cwd: Path | None = None,
    ) -> Dispatch:
        """Run bounded turns against a compiled workspace.

        **The wall clock is enforced here, not by tenon.** tenon's own
        `--timeout` overrun ends the stream with `outcome: "error"` and the
        prose "context deadline exceeded" — structurally identical to an
        unreadable pin set or a harness that would not start. Treating that
        as an environment failure drops a merely-slow agent off the
        leaderboard instead of penalising it, and a search then drifts toward
        whatever fits the budget; treating it as a finding by matching on the
        prose would make the policy a string comparison against text tenon is
        free to reword.

        So this adapter owns the deadline: it waits `timeout_s`, then
        terminates the process (SIGTERM, then SIGKILL after a grace) and
        returns `Dispatch(outcome="timed_out", ...)` with whatever turns were
        reduced before the clock ran out. tenon is still given a `--timeout`,
        a little larger, as the backstop for the case where this process dies
        first — but the verdict never depends on it.

        `outcome: "ok"` means the dispatcher completed every turn it was
        given, whatever those turns' statuses. Score from `turns`."""
        argv = [self.binary, "run", str(agent), "--workspace", str(workspace),
                *self._harness_args(harness), "--input", "jsonl"]
        if conversation:
            argv += ["--conversation", conversation]
        # tenon's deadline is the backstop for a supervisor that died, not the
        # verdict; give it headroom so the adapter's clock is the one that
        # fires, and stay inside tenon's own 30-minute cap.
        backstop = min(int(timeout_s) + 30, 30 * 60)
        argv += ["--timeout", f"{backstop}s"]
        if turn_timeout_s:
            argv += ["--turn-timeout", f"{int(turn_timeout_s)}s"]
        if pins:
            argv += ["--pins", str(pins)]

        payload = "".join(json.dumps(i) + "\n" for i in inputs).encode()
        events = Path(events_path) if events_path else None
        out_handle = events.open("wb") if events else subprocess.PIPE
        err_handle = Path(stderr_path).open("wb") if stderr_path else subprocess.PIPE
        try:
            proc = self._popen(
                argv,
                cwd=cwd if cwd is not None else self.cwd,
                stdout=out_handle,
                stderr=err_handle,
                stdin=subprocess.PIPE,
                env=self.env,
                # Its own process group, so the deadline can take down the
                # harness the dispatcher started and whatever that started in
                # turn. Signalling the dispatcher alone leaves the model
                # process — and any tool server under it — orphaned and
                # burning budget after the run this adapter reported is over.
                new_session=True,
            )
            try:
                proc.stdin.write(payload)
                proc.stdin.close()
            except (BrokenPipeError, OSError):
                pass
            # communicate() would flush a stdin already closed above; the
            # payload is fully written, so drop the handle rather than let it
            # raise on a stream nothing more will be sent to.
            proc.stdin = None
            timed_out, captured_out, captured_err = self._wait_with_clock(proc, timeout_s)
        finally:
            for handle in (out_handle, err_handle):
                if hasattr(handle, "close"):
                    handle.close()

        text = events.read_text(errors="replace") if events else captured_out
        stderr_text = (
            Path(stderr_path).read_text(errors="replace") if stderr_path else captured_err
        )
        per_input = summarize_turns(text)
        events_name = str(events) if events else ""
        if timed_out:
            # Whatever the stream got to say before the clock fired is kept:
            # the turns that did complete are still evidence about the
            # variant, and the caller scores this as a failed variant.
            return Dispatch(
                outcome="timed_out",
                turns=reduce_counts(per_input),
                per_input=per_input,
                events_path=events_name,
                exit_code=proc.returncode,
            )
        try:
            completion = read_run_completion(text, command=f"dispatch {agent}")
        except TenonEnvironment as err:
            raise TenonEnvironment(_with_detail(err.args[0], stderr_text), command="") from None
        counts = completion.get("turns") or {}
        reduced = reduce_counts(per_input)
        # run.completed's counts are the dispatcher's own tally. If the
        # per-input reduction disagrees, the stream this reduction read was
        # truncated, and every count derived from it is wrong.
        if counts and {k: v for k, v in counts.items() if v} != reduced:
            raise TenonEnvironment(
                f"reported turns {counts} but the stream reduces to {reduced}; "
                f"the event stream in {events_name or 'stdout'} is truncated",
                command=f"dispatch {agent}",
            )
        return Dispatch(
            outcome=completion.get("outcome", ""),
            fingerprint=completion.get("fingerprint", ""),
            turns=counts,
            per_input=per_input,
            source_digest=completion.get("source_digest", ""),
            events_path=events_name,
            exit_code=proc.returncode,
        )

    def _wait_with_clock(self, proc, timeout_s: int) -> tuple:
        """Wait for a dispatch, terminating it when the wall clock expires.

        SIGTERM first so tenon can close its harness child and flush, then
        SIGKILL after a grace so a wedged process cannot outlive the run."""
        captured_out, captured_err = "", ""
        if timeout_s and timeout_s > 0:
            try:
                captured_out, captured_err = proc.communicate(timeout=timeout_s)
            except subprocess.TimeoutExpired:
                self._terminate(proc)
                try:
                    captured_out, captured_err = proc.communicate(timeout=TERMINATE_GRACE_S)
                except subprocess.TimeoutExpired:
                    captured_out, captured_err = "", ""
                return True, _text(captured_out), _text(captured_err)
        else:
            captured_out, captured_err = proc.communicate()
        return False, _text(captured_out), _text(captured_err)

    @staticmethod
    def _terminate(proc) -> None:
        terminate_tree(proc)

    # -- the composite -----------------------------------------------------

    def iterate(
        self,
        agent: Path,
        workspace: Path,
        inputs: Iterable[dict],
        *,
        pins: Path | None = None,
        write_pins: Path | None = None,
        model: str = "",
        verify_pre_drift: bool = False,
        harness: str | None = None,
        cwd: Path | None = None,
        **dispatch_kw,
    ) -> Iteration:
        """gate -> compile -> [drift] -> dispatch -> drift, as one record.

        `phase_failed` records FINDINGS only. An environment failure at any
        phase raises out of here entirely rather than setting `phase_failed`:
        it is not a phase result, and the caller must not write it to
        lineage.

        The pre-run drift is OFF by default. `compile` just enumerated
        exactly what it wrote and removed, so a drift immediately afterwards
        against an untouched workspace cannot differ — it is a process and a
        full projection regeneration spent to re-derive a list the previous
        line already returned. Turn it on for a paranoid mode where something
        else may touch the workspace between apply and run. The POST-run
        drift always runs: it is the only check that the agent did not
        rewrite its own configuration mid-run, and it is the one that earns
        its process.

        Passing `pins` through every phase keeps all of them resolving the
        same closure — including both drifts, so neither re-resolves it.

        TODO(I4-followup): the two drifts still load and project the agent
        project twice, once each, and at search scale that is the dominant
        non-model cost. Collapsing them needs a tenon-side facility for two
        drift reports from one load (a before/after pair around a dispatch,
        or a drift that accepts a previously loaded projection). That is a
        tenon change, not an improve change; nothing here can do better than
        skipping the pre-run one.
        """
        verdict = self.gate(
            agent, pins=pins, write_pins=write_pins, model=model, harness=harness, cwd=cwd
        )
        if not verdict.ok:
            # Nothing downstream ran; no workspace was touched.
            return Iteration(
                ok=False,
                phase_failed="check",
                outcome="gate_failed",
                source_digest=verdict.source_digest,
                diagnostics=verdict.errors,
                verdict=verdict,
            )

        try:
            applied = self.compile(agent, workspace, pins=pins, harness=harness, cwd=cwd)
        except GateContradiction as err:
            # The gate passed on this same source moments ago with the same
            # harness and the same pins, and tenon asserts check and apply
            # fail identically on the same source. Record it loudly as its
            # own phase rather than papering over it as an ordinary finding.
            return Iteration(
                ok=False,
                phase_failed="apply",
                outcome="gate_failed",
                fingerprint=verdict.fingerprint,
                source_digest=err.source_digest,
                diagnostics=err.diagnostics,
                verdict=verdict,
            )

        pre = None
        if verify_pre_drift:
            pre = self.drifted(agent, workspace, pins=pins, harness=harness, cwd=cwd)
            if not pre.matched:
                return Iteration(
                    ok=False,
                    phase_failed="pre_drift",
                    outcome="gate_failed" if pre.gate_failed else "drift",
                    fingerprint=verdict.fingerprint,
                    source_digest=pre.source_digest,
                    diagnostics=pre.findings,
                    verdict=verdict,
                    applied=applied,
                    pre_drift=pre,
                )

        ran = self.dispatch(
            agent, workspace, inputs, pins=pins, harness=harness, cwd=cwd, **dispatch_kw
        )
        if ran.outcome != "ok":
            return Iteration(
                ok=False,
                phase_failed="run",
                outcome=ran.outcome,
                fingerprint=verdict.fingerprint,
                source_digest=ran.source_digest,
                verdict=verdict,
                applied=applied,
                pre_drift=pre,
                dispatch=ran,
            )

        post = self.drifted(agent, workspace, pins=pins, harness=harness, cwd=cwd)
        if not post.matched:
            return Iteration(
                ok=False,
                phase_failed="post_drift",
                outcome="gate_failed" if post.gate_failed else "drift",
                fingerprint=verdict.fingerprint,
                source_digest=post.source_digest,
                diagnostics=post.findings,
                verdict=verdict,
                applied=applied,
                pre_drift=pre,
                dispatch=ran,
                post_drift=post,
            )
        return Iteration(
            ok=True,
            fingerprint=verdict.fingerprint,
            verdict=verdict,
            applied=applied,
            pre_drift=pre,
            dispatch=ran,
            post_drift=post,
        )


class GateContradiction(Exception):
    """`compile` (or another phase) rejected a source the gate accepted with
    the same harness and pin set. tenon asserts check and apply fail
    identically on the same source, so this is a contract violation, not a
    finding about the candidate."""

    def __init__(self, message: str, *, source_digest: str = "", diagnostics: tuple = ()):
        self.source_digest = source_digest
        self.diagnostics = diagnostics
        super().__init__(message)


def terminate_tree(proc, grace_s: float = TERMINATE_GRACE_S) -> None:
    """End a process and everything it started: SIGTERM to its group so it can
    close its harness child and flush, then SIGKILL after a grace so a wedged
    tree cannot outlive the run that owns it.

    The group is what matters. A dispatcher signalled on its own leaves the
    model process it spawned — and any tool server under that — running,
    which is the difference between a timeout that ends and one that only
    stops being watched. When the process was not started in its own session
    (a caller supplied its own spawn), this falls back to the process."""
    for signum in (signal.SIGTERM, signal.SIGKILL):
        if proc.poll() is not None:
            return
        try:
            os.killpg(os.getpgid(proc.pid), signum)
        except (OSError, AttributeError):
            try:
                proc.terminate() if signum == signal.SIGTERM else proc.kill()
            except OSError:
                return
        if signum == signal.SIGKILL:
            return
        deadline = time.time() + grace_s
        while time.time() < deadline:
            if proc.poll() is not None:
                return
            time.sleep(0.1)


def _with_detail(message: str, stderr: str) -> str:
    """tenon's prose on stderr is the only detail an environment failure has
    when the stream itself was cut short — but it is usually the same
    sentence the terminator already carried, so append it only when it adds
    something."""
    detail = stderr.strip().splitlines()[-1].strip() if stderr.strip() else ""
    # tenon prefixes its stderr with the subcommand it ran; the terminator
    # carries the same sentence without it, so compare past the prefix.
    bare = detail.split(": ", 1)[-1] if detail.startswith("tenon ") else detail
    if not bare or bare in message:
        return message
    return f"{message}; {detail[:200]}"


def _text(value) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode(errors="replace")
    return value


def child_env(base: dict | None = None, **extra) -> dict:
    """An environment for a child tenon (or a hook that calls one).

    TENON_HARNESS supplies `--harness` wherever a caller omits it. Everything
    here passes `--harness` explicitly, so an inherited one can only retarget
    a hook's own tenon calls; drop it rather than let it silently do that."""
    env = dict(os.environ if base is None else base)
    env.pop("TENON_HARNESS", None)
    env.update({k: v for k, v in extra.items() if v is not None})
    return env
