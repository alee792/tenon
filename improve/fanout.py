#!/usr/bin/env python3
"""fanout — dispatch and supervise k isolated tenon agents.

fanout is a separate tool that *consumes* tenon's CLI; it is not a tenon
subcommand and adds nothing to tenon's contract. Tenon's north star keeps
evaluation, scoring, and selection among revisions out of scope, and its
use-cases document states plainly that how variants are isolated —
worktrees, containers, sandboxes — is the caller's infrastructure choice.
fanout is one such choice: git worktrees, one per variant.

Per variant it does exactly this, and nothing more:

  1. git worktree add        an isolated workspace on its own branch
  2. mutate (optional)       a caller-supplied command over the agent files
  3. gate                    prove the variant and record its fingerprint
  4. compile                 write the agent's configuration into that workspace
  5. dispatch                run the task as bounded JSONL turns

Steps 3-5 are the tenon adapter's roles (`improve/tenon.py`), which is the
only module here that names a tenon subcommand or flag.

It then reports. It does not mutate, score, or select — `fanout collect`
emits one JSON record per variant (fingerprint, terminal turn status,
branch, patch, agent text) for whatever ranks top-k downstream.

Usage:
  fanout start   [--spec FILE] [flags]     prepare, apply, and run k variants
  fanout list                              runs known to the state dir
  fanout status  RUN [--json]              per-variant lifecycle state
  fanout logs    RUN VARIANT [-f] [--text] event stream or agent text
  fanout stop    RUN                       terminate a detached run
  fanout collect RUN [--json]              per-variant result records
  fanout clean   RUN [--force]             remove worktrees, branches, state

Run `fanout <command> --help` for each command's flags.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from pathlib import Path

# Every tenon call goes through the adapter, which is the only module in
# improve/ that names a tenon subcommand or flag; improve/test_tenon.py greps
# this file to keep it that way. Import it by directory rather than by package
# so fanout runs the same whatever the cwd.
sys.path.insert(0, str(Path(__file__).resolve().parent))
import tenon as tenon_api  # noqa: E402
from tenon import GateContradiction, TenonEnvironment  # noqa: E402

SCHEMA_VERSION = 1

# The largest per-variant budget the adapter can enforce. NOT tenon's own
# 30-minute cap: the adapter owns the verdict and hands tenon a backstop of
# the budget plus headroom, so a budget at tenon's cap makes both clocks fire
# at once and a timed-out variant is reported as an environment error rather
# than the finding it is. The relationship lives in the adapter; this is the
# same number, imported rather than restated.
MAX_RUN_TIMEOUT = tenon_api.MAX_DISPATCH_TIMEOUT_S

# Lifecycle states a variant passes through. Only the terminal ones are
# reported by collect.
PENDING = "pending"
PREPARING = "preparing"
MUTATING = "mutating"
CHECKING = "checking"
APPLYING = "applying"
RUNNING = "running"
DONE = "done"
FAILED = "failed"
CANCELLED = "cancelled"
# ERRORED is not a finding about the variant. tenon reports outcome "error"
# when the environment, not the source, is at fault — an unreadable pin set,
# an unwritable path, a harness that would not start. FAILED says "this
# variant is bad" and downstream scores it; ERRORED says "we learned nothing
# about this variant" and downstream must not. Terminal all the same: the run
# is over for this variant either way.
ERRORED = "errored"
TERMINAL = {DONE, FAILED, CANCELLED, ERRORED}


class FanoutError(Exception):
    """A failure that should print as one line, not a traceback."""


# TenonEnvironment is the adapter's, and fanout and evolve share it: one
# exception type for "the environment failed, not the variant", so a caller
# that spans both tools catches one thing. It deliberately does NOT descend
# from FanoutError — an environment failure is not a fanout failure, and
# main() prints it on its own line.


# --------------------------------------------------------------------------
# small helpers
# --------------------------------------------------------------------------


def parse_duration(value, what: str) -> int:
    """Accept Go-style durations (600s, 10m, 1h) and bare seconds."""
    if isinstance(value, (int, float)):
        seconds = int(value)
    else:
        text = str(value).strip()
        match = re.fullmatch(r"(\d+(?:\.\d+)?)\s*(s|m|h)?", text)
        if not match:
            raise FanoutError(f"{what}: {value!r} is not a duration (e.g. 600s, 10m)")
        scale = {"s": 1, "m": 60, "h": 3600, None: 1}[match.group(2)]
        seconds = int(float(match.group(1)) * scale)
    if seconds <= 0:
        raise FanoutError(f"{what} must be greater than zero")
    return seconds


def run_git(repo: Path, *args: str, check: bool = True) -> str:
    proc = subprocess.run(
        ["git", "-C", str(repo), *args],
        capture_output=True,
        text=True,
    )
    if check and proc.returncode != 0:
        raise FanoutError(f"git {' '.join(args)}: {proc.stderr.strip() or proc.stdout.strip()}")
    return proc.stdout


def write_json_atomic(path: Path, payload) -> None:
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
    tmp.replace(path)


def read_json(path: Path):
    return json.loads(path.read_text())


def default_state_dir() -> Path:
    env = os.environ.get("FANOUT_HOME")
    if env:
        return Path(env).expanduser()
    return Path.home() / ".fanout"


def slug(text: str) -> str:
    cleaned = re.sub(r"[^A-Za-z0-9._-]+", "-", text).strip("-")
    return cleaned or "variant"


# --------------------------------------------------------------------------
# spec
# --------------------------------------------------------------------------


@dataclass
class Variant:
    name: str
    index: int
    task: list
    mutate: str = ""
    pins: str = ""
    env: dict = field(default_factory=dict)

    def to_json(self) -> dict:
        return {
            "name": self.name,
            "index": self.index,
            "task": self.task,
            "mutate": self.mutate,
            "pins": self.pins,
            "env": self.env,
        }


@dataclass
class Spec:
    run: str
    repo: Path
    base: str
    agent: str
    agent_is_relative: bool
    harness: str
    tenon: str
    state_dir: Path
    branch_prefix: str
    concurrency: int
    timeout: int
    turn_timeout: int
    keep_going: bool
    variants: list

    def to_json(self) -> dict:
        return {
            "schema_version": SCHEMA_VERSION,
            "run": self.run,
            "repo": str(self.repo),
            "base": self.base,
            "agent": self.agent,
            "agent_is_relative": self.agent_is_relative,
            "harness": self.harness,
            "tenon": self.tenon,
            "state_dir": str(self.state_dir),
            "branch_prefix": self.branch_prefix,
            "concurrency": self.concurrency,
            "timeout": self.timeout,
            "turn_timeout": self.turn_timeout,
            "keep_going": self.keep_going,
            "variants": [v.to_json() for v in self.variants],
        }


def as_task_list(value, what: str) -> list:
    """A task is one prompt or an ordered list of prompts (one turn each)."""
    if value is None:
        return []
    if isinstance(value, str):
        return [value] if value.strip() else []
    if isinstance(value, list) and all(isinstance(t, str) for t in value):
        return [t for t in value if t.strip()]
    raise FanoutError(f"{what} must be a string or a list of strings")


def build_spec(args, spec_file: dict) -> Spec:
    def pick(flag_value, key, fallback=None):
        if flag_value not in (None, ""):
            return flag_value
        if key in spec_file and spec_file[key] not in (None, ""):
            return spec_file[key]
        return fallback

    repo_raw = pick(args.repo, "repo")
    if repo_raw:
        repo = Path(repo_raw).expanduser().resolve()
    else:
        top = run_git(Path.cwd(), "rev-parse", "--show-toplevel").strip()
        repo = Path(top).resolve()
    if not (repo / ".git").exists():
        raise FanoutError(f"{repo} is not a git worktree or repository")

    agent_raw = pick(args.agent, "agent", ".")
    agent_path = Path(agent_raw).expanduser()
    agent_is_relative = not agent_path.is_absolute()
    agent = str(agent_path) if agent_is_relative else str(agent_path.resolve())
    if not agent_is_relative and not Path(agent).is_dir():
        raise FanoutError(f"agent directory {agent} does not exist")

    harness = pick(args.harness, "harness", "claude")
    if harness not in ("claude", "codex"):
        raise FanoutError("harness must be exactly claude or codex")

    tenon = pick(args.tenon, "tenon") or os.environ.get("FANOUT_TENON") or "tenon"
    resolved_tenon = shutil.which(tenon) or (tenon if Path(tenon).is_file() else "")
    if not resolved_tenon:
        raise FanoutError(
            f"tenon binary {tenon!r} not found; pass --tenon PATH or set FANOUT_TENON "
            "(build one with: go build -o ./tenon ./cmd/tenon)"
        )

    shared_task = as_task_list(pick(args.task, "task"), "task")
    shared_mutate = pick(args.mutate, "mutate", "") or ""
    shared_pins = pick(args.pins, "pins", "") or ""  # not tenon argv — a spec key

    raw_variants = spec_file.get("variants") or []
    if raw_variants and args.k:
        raise FanoutError("--k and an explicit variants list are mutually exclusive")

    variants: list = []
    if raw_variants:
        if not isinstance(raw_variants, list):
            raise FanoutError("variants must be a list of objects")
        for i, raw in enumerate(raw_variants, start=1):
            if not isinstance(raw, dict):
                raise FanoutError("each variant must be an object")
            name = slug(str(raw.get("name") or f"v{i}"))
            task = as_task_list(raw.get("task"), f"variant {name} task") or shared_task
            variants.append(
                Variant(
                    name=name,
                    index=i,
                    task=task,
                    mutate=raw.get("mutate", shared_mutate) or "",
                    pins=raw.get("pins", shared_pins) or "",  # not tenon argv — a spec key
                    env={str(k): str(v) for k, v in (raw.get("env") or {}).items()},
                )
            )
    else:
        k = int(pick(args.k, "k", 1))
        if k < 1:
            raise FanoutError("k must be at least 1")
        variants = [
            Variant(name=f"v{i}", index=i, task=shared_task, mutate=shared_mutate, pins=shared_pins)
            for i in range(1, k + 1)
        ]

    names = [v.name for v in variants]
    if len(set(names)) != len(names):
        raise FanoutError("variant names must be unique")
    for v in variants:
        if not v.task:
            raise FanoutError(f"variant {v.name} has no task; pass --task or set it in the spec")

    timeout = parse_duration(pick(args.timeout, "timeout", "600s"), "timeout")
    if timeout > MAX_RUN_TIMEOUT:
        raise FanoutError(
            f"timeout must be at most {MAX_RUN_TIMEOUT}s: tenon run's cap is "
            f"{tenon_api.TENON_RUN_TIMEOUT_CAP_S}s and the adapter's clock needs "
            f"{tenon_api.TIMEOUT_BACKSTOP_HEADROOM_S}s of headroom under it"
        )
    turn_timeout_raw = pick(args.turn_timeout, "turn_timeout", 0)
    turn_timeout = 0 if not turn_timeout_raw else parse_duration(turn_timeout_raw, "turn-timeout")

    concurrency = int(pick(args.concurrency, "concurrency", 0) or 0) or min(len(variants), 4)
    if concurrency < 1:
        raise FanoutError("concurrency must be at least 1")

    run_name = slug(pick(args.run, "run") or f"run-{time.strftime('%Y%m%d-%H%M%S')}")
    state_dir_raw = pick(args.state_dir, "state_dir")
    state_dir = Path(state_dir_raw).expanduser().resolve() if state_dir_raw else default_state_dir()

    return Spec(
        run=run_name,
        repo=repo,
        base=pick(args.base, "base", "HEAD"),
        agent=agent,
        agent_is_relative=agent_is_relative,
        harness=harness,
        tenon=resolved_tenon,
        state_dir=state_dir,
        branch_prefix=pick(args.branch_prefix, "branch_prefix", "fanout"),
        concurrency=concurrency,
        timeout=timeout,
        turn_timeout=turn_timeout,
        keep_going=not bool(pick(args.fail_fast or None, "fail_fast", False)),
        variants=variants,
    )


# --------------------------------------------------------------------------
# run directory
# --------------------------------------------------------------------------


class RunDir:
    """The on-disk home of one fanout run."""

    def __init__(self, root: Path):
        self.root = root
        self.run_json = root / "run.json"
        self.state_json = root / "state.json"
        self.supervisor_pid = root / "supervisor.pid"
        self.supervisor_log = root / "supervisor.log"
        self.variants_dir = root / "variants"

    @classmethod
    def locate(cls, run: str, state_dir: Path | None) -> "RunDir":
        base = state_dir or default_state_dir()
        root = base / run
        if not (root / "run.json").exists():
            raise FanoutError(f"no run {run!r} under {base}")
        return cls(root)

    def variant_dir(self, name: str) -> Path:
        return self.variants_dir / name

    def spec_json(self) -> dict:
        return read_json(self.run_json)

    def state(self) -> dict:
        return read_json(self.state_json)


# --------------------------------------------------------------------------
# supervisor
# --------------------------------------------------------------------------


class Supervisor:
    """Drives every variant's lifecycle and owns the run's mutable state."""

    def __init__(self, rundir: RunDir, spec: dict, verbose: bool):
        self.rundir = rundir
        self.spec = spec
        self.verbose = verbose
        self.lock = threading.Lock()
        self.cancelled = threading.Event()
        self.children: dict = {}
        self.state = rundir.state() if rundir.state_json.exists() else self._fresh_state()
        self._flush()

    def _fresh_state(self) -> dict:
        return {
            "schema_version": SCHEMA_VERSION,
            "run": self.spec["run"],
            "status": "running",
            "started_at": time.time(),
            "finished_at": None,
            "variants": {
                v["name"]: {
                    "name": v["name"],
                    "index": v["index"],
                    "status": PENDING,
                    "detail": "",
                    "fingerprint": "",
                    "source_digest": "",
                    "diagnostics": [],
                    "outcome": "",
                    "branch": "",
                    "workspace": "",
                    "agent": "",
                    "started_at": None,
                    "finished_at": None,
                    "turns": [],
                }
                for v in self.spec["variants"]
            },
        }

    # -- state -------------------------------------------------------------

    def _flush(self) -> None:
        write_json_atomic(self.rundir.state_json, self.state)

    def set(self, name: str, **fields) -> None:
        with self.lock:
            self.state["variants"][name].update(fields)
            self._flush()
        if self.verbose and "status" in fields:
            detail = fields.get("detail", "")
            suffix = f" — {detail}" if detail else ""
            print(f"[{name}] {fields['status']}{suffix}", file=sys.stderr, flush=True)

    # -- process plumbing --------------------------------------------------

    def _spawn(
        self, name: str, argv: list, cwd=None, stdout=None, stderr=None, stdin=None,
        env=None, new_session: bool = False,
    ):
        if self.cancelled.is_set():
            raise FanoutError("cancelled")
        proc = subprocess.Popen(
            argv,
            cwd=str(cwd) if cwd is not None else None,
            stdout=stdout,
            stderr=stderr,
            stdin=stdin,
            env=env,
            text=False,
            # A dispatch runs in its own process group so cancelling it takes
            # down the harness it started, not just the dispatcher.
            start_new_session=new_session,
        )
        with self.lock:
            self.children[name] = proc
        return proc

    def _reap(self, name: str) -> None:
        with self.lock:
            self.children.pop(name, None)

    def cancel(self) -> None:
        """Terminate every live child; workers observe the cancelled event."""
        self.cancelled.set()
        with self.lock:
            live = list(self.children.values())
        for proc in live:
            # Group-wide, so a cancelled variant leaves no model process
            # behind burning budget for a run that is already over.
            tenon_api.terminate_tree(proc, grace_s=10)

    # -- the per-variant pipeline -----------------------------------------

    def drive(self, variant: dict) -> None:
        name = variant["name"]
        vdir = self.rundir.variant_dir(name)
        vdir.mkdir(parents=True, exist_ok=True)
        self.set(name, status=PREPARING, started_at=time.time())
        try:
            workspace = self._prepare_worktree(name, vdir)
            agent = self._resolve_agent(name, vdir, workspace)
            self._mutate(name, variant, vdir, agent, workspace)
            fingerprint = self._check(name, variant, vdir, agent, workspace)
            self.set(name, fingerprint=fingerprint)
            self._apply(name, variant, vdir, agent, workspace)
            self._dispatch(name, variant, vdir, agent, workspace)
            self.set(name, status=DONE, detail="", finished_at=time.time())
        except TenonEnvironment as err:
            # Not the variant's fault, so not FAILED: an environment failure
            # must not reach whatever scores this run as evidence about the
            # configuration under test.
            self.set(
                name,
                status=ERRORED,
                # Whatever phase we reached, the outcome that ended this
                # variant is the environment one; leaving the previous
                # phase's "ok" on the record would read as a clean run.
                outcome="error",
                detail=str(err),
                finished_at=time.time(),
            )
            if not self.spec["keep_going"]:
                self.cancel()
        except FanoutError as err:
            terminal = CANCELLED if self.cancelled.is_set() else FAILED
            self.set(name, status=terminal, detail=str(err), finished_at=time.time())
            if not self.spec["keep_going"] and terminal == FAILED:
                self.cancel()
        finally:
            self._reap(name)

    def _prepare_worktree(self, name: str, vdir: Path) -> Path:
        repo = Path(self.spec["repo"])
        # git keys its worktree bookkeeping on the LEAF name of the path, so
        # every variant needs a distinct one: a shared leaf makes concurrent
        # `worktree add` calls race on the same .git/worktrees/<leaf> entry.
        workspace = vdir / f"{self.spec['run']}-{name}"
        branch = f"{self.spec['branch_prefix']}/{self.spec['run']}/{name}"
        if workspace.exists():
            raise FanoutError(f"workspace {workspace} already exists")
        # Record the branch before creating it: `worktree add` can create the
        # branch and still fail, and a branch this run does not know about is a
        # branch clean will not remove.
        self.set(name, workspace=str(workspace), branch=branch)
        run_git(repo, "worktree", "add", "-b", branch, str(workspace), self.spec["base"])
        self.set(name, base_sha=run_git(workspace, "rev-parse", "HEAD").strip())
        return workspace

    def _resolve_agent(self, name: str, vdir: Path, workspace: Path) -> Path:
        """A relative agent path lives inside the worktree, so mutations to the
        agent's own files land in the variant's branch and show up in its
        patch. An absolute path is copied out, leaving the source untouched."""
        if self.spec["agent_is_relative"]:
            agent = (workspace / self.spec["agent"]).resolve()
            if not agent.is_relative_to(workspace.resolve()):
                raise FanoutError("a relative agent path must stay inside the workspace")
        else:
            agent = vdir / "agent"
            if agent.exists():
                shutil.rmtree(agent)
            shutil.copytree(self.spec["agent"], agent, symlinks=True)
        if not agent.is_dir():
            raise FanoutError(f"agent directory {agent} does not exist")
        self.set(name, agent=str(agent))
        return agent

    def child_env(self, variant: dict, vdir: Path, agent: Path, workspace: Path) -> dict:
        """The environment every child of this variant runs in.

        The TENON_HARNESS drop is the adapter's `child_env`: fanout always
        names the harness explicitly (a harness sweep is one of its use
        cases), so an inherited one can only change what a mutate hook's own
        tenon calls target. The FANOUT_* additions are fanout's own contract
        with those hooks, and the variant's `env` wins over both."""
        env = tenon_api.child_env(
            FANOUT_RUN=self.spec["run"],
            FANOUT_VARIANT=variant["name"],
            FANOUT_INDEX=str(variant["index"]),
            FANOUT_AGENT_DIR=str(agent),
            FANOUT_WORKSPACE=str(workspace),
            FANOUT_VARIANT_DIR=str(vdir),
            FANOUT_HARNESS=self.spec["harness"],
            FANOUT_TENON=self.spec["tenon"],
        )
        # Last, so a variant's own env wins over both — including over a
        # FANOUT_* name it deliberately overrides.
        env.update(variant.get("env") or {})
        return env

    def _mutate(self, name: str, variant: dict, vdir: Path, agent: Path, workspace: Path) -> None:
        command = variant.get("mutate") or ""
        if not command.strip():
            return
        self.set(name, status=MUTATING)
        log = vdir / "mutate.log"
        with log.open("wb") as out:
            proc = self._spawn(
                name,
                ["/bin/sh", "-c", command],
                cwd=agent,
                stdout=out,
                stderr=subprocess.STDOUT,
                env=self.child_env(variant, vdir, agent, workspace),
            )
            code = proc.wait()
        self._reap(name)
        if code != 0:
            raise FanoutError(f"mutate exited {code}; see {log}")

    def _tenon(self, name: str, variant: dict, vdir: Path, agent: Path, workspace: Path):
        """The adapter, bound to this variant's harness, environment, and to
        fanout's own child tracking — so a cancelled run still terminates
        every tenon process it started."""
        return tenon_api.Tenon(
            self.spec["tenon"],
            self.spec["harness"],
            env=self.child_env(variant, vdir, agent, workspace),
            spawn=lambda argv, **kw: self._spawn(name, argv, **kw),
        )

    def _check(self, name: str, variant: dict, vdir: Path, agent: Path, workspace: Path) -> str:
        """Gate the variant and mint its identity in one process.

        The harness and the pin set are passed explicitly so this is the same
        gate compile runs three lines later: a harness-specific or
        pin-specific rejection costs one process here instead of two, and it
        is recorded as a rejection of this variant rather than surfacing
        later as a compile contract violation. No inventory is emitted:
        fanout wants a fingerprint, and a per-file listing it would discard is
        hot-path I/O.

        The exit code cannot tell a rejection from a broken environment —
        both exit 1 — so the adapter reads the terminator's outcome, and the
        difference is whether downstream scores this variant at all."""
        self.set(name, status=CHECKING)
        log, errlog = vdir / "check.jsonl", vdir / "check.err"
        try:
            verdict = self._tenon(name, variant, vdir, agent, workspace).gate(
                agent,
                pins=variant.get("pins") or None,
                cwd=agent,
                log=log,
                errlog=errlog,
            )
        finally:
            self._reap(name)
        if verdict.ok:
            self.set(name, outcome="ok")
            return verdict.fingerprint
        # The diagnostic ids are the finding. They go on the record, not only
        # into the exception text, so `collect` can say what was rejected
        # without anyone reading the log.
        ids = list(verdict.rejected)
        self.set(
            name,
            outcome="gate_failed",
            source_digest=verdict.source_digest,
            diagnostics=ids,
        )
        raise FanoutError(
            f"the gate rejected {agent}: {','.join(ids) or 'no diagnostics'}; "
            f"source_digest={verdict.source_digest or 'unknown'}; see {log}"
        )

    def _apply(self, name: str, variant: dict, vdir: Path, agent: Path, workspace: Path) -> None:
        self.set(name, status=APPLYING)
        log, errlog = vdir / "apply.jsonl", vdir / "apply.err"
        try:
            applied = self._tenon(name, variant, vdir, agent, workspace).compile(
                agent,
                workspace,
                pins=variant.get("pins") or None,
                cwd=workspace,
                log=log,
                errlog=errlog,
            )
        except GateContradiction as err:
            # The gate passed on this same source moments ago, with the same
            # harness and the same pin set, so compile ran the same gate and
            # this is a contract violation worth naming loudly. Naming it
            # means recording what it rejected, not only that it did.
            ids = [d.id for d in err.diagnostics]
            self.set(
                name,
                outcome="gate_failed",
                source_digest=err.source_digest,
                diagnostics=ids,
            )
            raise FanoutError(f"{err}; see {log}") from None
        finally:
            self._reap(name)
        self.set(
            name,
            outcome="ok",
            written=list(applied.written),
            removed=list(applied.removed),
            managed_tools=list(applied.managed_tools),
        )

    def _dispatch(self, name: str, variant: dict, vdir: Path, agent: Path, workspace: Path) -> None:
        self.set(name, status=RUNNING)
        events, errlog = vdir / "events.jsonl", vdir / "run.err"
        inputs = [
            {"input_id": f"{self.spec['run']}-{name}-{i}", "text": text}
            for i, text in enumerate(variant["task"], start=1)
        ]
        try:
            result = self._tenon(name, variant, vdir, agent, workspace).dispatch(
                agent,
                workspace,
                inputs,
                pins=variant.get("pins") or None,
                timeout_s=self.spec["timeout"],
                turn_timeout_s=self.spec["turn_timeout"],
                events_path=events,
                stderr_path=errlog,
                cwd=workspace,
            )
        finally:
            self._reap(name)
        self.set(
            name,
            turns=list(result.per_input),
            run_exit_code=result.exit_code,
            outcome=result.outcome,
        )
        if result.outcome == "ok":
            # A completed dispatch, whatever its turns did. turns.failed is a
            # score input, not a fanout failure.
            return
        if result.outcome == "timed_out":
            # A variant slower than its budget is a finding ABOUT the variant,
            # so it is FAILED and scored, not ERRORED and dropped. Dropping it
            # would let a search drift toward whatever fits the budget without
            # ever paying for being slow.
            raise FanoutError(
                f"the variant's {self.spec['timeout']}s budget expired and the dispatch "
                f"was terminated; {sum(result.turns.values())} of {len(inputs)} turns "
                f"finished; see {events}"
            )
        if result.outcome == "gate_failed":
            self.set(name, source_digest=result.source_digest)
            raise FanoutError(
                f"dispatch rejected the agent at the gate; "
                f"source_digest={result.source_digest or 'unknown'}; see {events}"
            )
        raise TenonEnvironment(f"dispatch ended {result.outcome!r}; see {errlog}")

    # -- driving the pool --------------------------------------------------

    def go(self) -> int:
        variants = self.spec["variants"]
        with ThreadPoolExecutor(max_workers=self.spec["concurrency"]) as pool:
            futures = [pool.submit(self.drive, v) for v in variants]
            for future in futures:
                future.result()
        with self.lock:
            for record in self.state["variants"].values():
                if record["status"] not in TERMINAL:
                    record["status"] = CANCELLED
                    record["finished_at"] = time.time()
            failed = sum(1 for r in self.state["variants"].values() if r["status"] != DONE)
            self.state["status"] = "finished"
            self.state["finished_at"] = time.time()
            self._flush()
        return 1 if failed else 0


def summarize_turns(events: Path) -> list:
    """Reduce a dispatch event stream to one terminal record per input id.

    The reduction itself lives in the adapter, beside the terminator reader it
    is cross-checked against; this reads the file `collect` and `logs` name."""
    if not events.exists():
        return []
    return list(tenon_api.summarize_turns(events.read_text(errors="replace")))


def agent_text(events: Path) -> str:
    """Reassemble the model text tenon streamed as agent.output.delta."""
    if not events.exists():
        return ""
    parts = []
    for line in events.read_text(errors="replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "agent.output.delta":
            parts.append(event.get("delta", ""))
    return "".join(parts)


# --------------------------------------------------------------------------
# commands
# --------------------------------------------------------------------------


def cmd_start(args) -> int:
    spec_file = {}
    if args.spec:
        spec_file = read_json(Path(args.spec).expanduser())
        if not isinstance(spec_file, dict):
            raise FanoutError("a spec file must be a JSON object")
    spec = build_spec(args, spec_file)

    root = spec.state_dir / spec.run
    if root.exists():
        raise FanoutError(f"run {spec.run!r} already exists at {root}; pick --run or clean it")
    rundir = RunDir(root)
    rundir.variants_dir.mkdir(parents=True)
    write_json_atomic(rundir.run_json, spec.to_json())

    if args.dry_run:
        print(json.dumps(spec.to_json(), indent=2, sort_keys=True))
        return 0

    if args.detach:
        log = rundir.supervisor_log.open("ab")
        child = subprocess.Popen(
            [sys.executable, str(Path(__file__).resolve()), "_supervise", str(root)],
            stdout=log,
            stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL,
            start_new_session=True,
        )
        rundir.supervisor_pid.write_text(f"{child.pid}\n")
        print(f"{spec.run}\t{root}\tsupervisor pid {child.pid}")
        return 0

    return supervise(rundir, verbose=True)


def supervise(rundir: RunDir, verbose: bool) -> int:
    spec = rundir.spec_json()
    supervisor = Supervisor(rundir, spec, verbose=verbose)

    def handle(signum, _frame):
        print(f"fanout: signal {signum}; cancelling", file=sys.stderr, flush=True)
        supervisor.cancel()

    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, handle)
    try:
        return supervisor.go()
    finally:
        rundir.supervisor_pid.unlink(missing_ok=True)


def cmd_supervise(args) -> int:
    rundir = RunDir(Path(args.root).resolve())
    if not rundir.run_json.exists():
        raise FanoutError(f"{args.root} is not a fanout run directory")
    return supervise(rundir, verbose=True)


def cmd_list(args) -> int:
    base = Path(args.state_dir).expanduser() if args.state_dir else default_state_dir()
    if not base.is_dir():
        return 0
    rows = []
    for entry in sorted(base.iterdir()):
        if not (entry / "run.json").exists():
            continue
        state = read_json(entry / "state.json") if (entry / "state.json").exists() else {}
        variants = state.get("variants", {})
        tally = {}
        for record in variants.values():
            tally[record["status"]] = tally.get(record["status"], 0) + 1
        summary = " ".join(f"{k}={v}" for k, v in sorted(tally.items())) or "-"
        rows.append((entry.name, state.get("status", "unknown"), summary, str(entry)))
    for name, status, summary, path in rows:
        print(f"{name}\t{status}\t{summary}\t{path}")
    return 0


def cmd_status(args) -> int:
    rundir = RunDir.locate(args.run, Path(args.state_dir).expanduser() if args.state_dir else None)
    state = rundir.state()
    if args.json:
        print(json.dumps(state, indent=2, sort_keys=True))
        return 0
    print(f"run {state['run']}  {state['status']}  {rundir.root}")
    for record in sorted(state["variants"].values(), key=lambda r: r["index"]):
        started, finished = record.get("started_at"), record.get("finished_at")
        elapsed = f"{(finished or time.time()) - started:6.1f}s" if started else "     -"
        turns = ",".join(t["status"] for t in record.get("turns", [])) or "-"
        fingerprint = (record.get("fingerprint") or "-")[:16]
        detail = f"  {record['detail']}" if record.get("detail") else ""
        print(f"  {record['name']:<12} {record['status']:<14} {elapsed}  fp={fingerprint:<16} turns={turns}{detail}")
    return 0


def cmd_logs(args) -> int:
    rundir = RunDir.locate(args.run, Path(args.state_dir).expanduser() if args.state_dir else None)
    vdir = rundir.variant_dir(args.variant)
    if not vdir.is_dir():
        raise FanoutError(f"no variant {args.variant!r} in run {args.run!r}")
    path = vdir / ("run.err" if args.stderr else "events.jsonl")
    if args.text:
        sys.stdout.write(agent_text(vdir / "events.jsonl"))
        if not sys.stdout.isatty():
            return 0
        sys.stdout.write("\n")
        return 0
    if not path.exists():
        return 0
    if not args.follow:
        sys.stdout.write(path.read_text(errors="replace"))
        return 0
    with path.open("r", errors="replace") as handle:
        while True:
            chunk = handle.read()
            if chunk:
                sys.stdout.write(chunk)
                sys.stdout.flush()
                continue
            state = rundir.state()
            record = state["variants"].get(args.variant, {})
            if record.get("status") in TERMINAL:
                return 0
            time.sleep(0.4)


def cmd_stop(args) -> int:
    rundir = RunDir.locate(args.run, Path(args.state_dir).expanduser() if args.state_dir else None)
    if not rundir.supervisor_pid.exists():
        print(f"fanout: run {args.run!r} has no live supervisor", file=sys.stderr)
        return 1
    pid = int(rundir.supervisor_pid.read_text().strip())
    try:
        os.killpg(os.getpgid(pid), signal.SIGTERM)
    except ProcessLookupError:
        rundir.supervisor_pid.unlink(missing_ok=True)
        print(f"fanout: supervisor {pid} is already gone", file=sys.stderr)
        return 1
    print(f"fanout: sent SIGTERM to supervisor {pid}")
    return 0


def cmd_collect(args) -> int:
    """Emit one record per variant. This is the handoff to whatever selects
    top-k; fanout deliberately computes no score and picks no winner."""
    state_dir = Path(args.state_dir).expanduser() if args.state_dir else None
    rundir = RunDir.locate(args.run, state_dir)
    spec, state = rundir.spec_json(), rundir.state()
    records = []
    for record in sorted(state["variants"].values(), key=lambda r: r["index"]):
        vdir = rundir.variant_dir(record["name"])
        workspace = Path(record["workspace"]) if record.get("workspace") else None
        result = {
            "run": state["run"],
            "variant": record["name"],
            "index": record["index"],
            "status": record["status"],
            "detail": record.get("detail", ""),
            "fingerprint": record.get("fingerprint", ""),
            # A rejected variant has no fingerprint — tenon mints one only for
            # a source that passes. source_digest names the bytes that failed,
            # so a rejection is attributable; it is not a fingerprint and never
            # joins with one.
            "source_digest": record.get("source_digest", ""),
            # The error-severity diagnostic ids from whichever gate rejected
            # it, so a rejection says what was wrong without anyone opening
            # the log.
            "diagnostics": record.get("diagnostics", []),
            "outcome": record.get("outcome", ""),
            "harness": spec["harness"],
            "branch": record.get("branch", ""),
            "base_sha": record.get("base_sha", ""),
            "workspace": record.get("workspace", ""),
            "agent": record.get("agent", ""),
            "turns": record.get("turns", []),
            "events": str(vdir / "events.jsonl"),
            "duration_seconds": round((record.get("finished_at") or 0) - (record.get("started_at") or 0), 2)
            if record.get("started_at") and record.get("finished_at")
            else None,
        }
        if args.text:
            result["text"] = agent_text(vdir / "events.jsonl")
        if workspace and workspace.is_dir():
            result.update(collect_worktree(workspace, vdir, record, commit=not args.no_commit))
        records.append(result)
    if args.json:
        print(json.dumps(records, indent=2, sort_keys=True))
        return 0
    for r in records:
        turns = ",".join(t["status"] for t in r["turns"]) or "-"
        print(
            f"{r['variant']}\t{r['status']}\tfp={r['fingerprint'][:16]}\tturns={turns}"
            f"\tfiles={r.get('files_changed', 0)}\t{r['branch']}"
        )
    return 0


def collect_worktree(workspace: Path, vdir: Path, record: dict, commit: bool) -> dict:
    """Snapshot the variant's work. With commit on (the default) the whole
    result — including files the agent created — lands on the variant's
    branch, so checking out a winner is a plain git operation."""
    out: dict = {}
    dirty = run_git(workspace, "status", "--porcelain", check=False).strip()
    if dirty and commit:
        run_git(workspace, "add", "-A")
        run_git(
            workspace,
            "-c",
            "commit.gpgsign=false",
            "commit",
            "--quiet",
            "--no-verify",
            "-m",
            f"fanout: {record['name']} result",
            check=False,
        )
    base = record.get("base_sha") or ""
    head = run_git(workspace, "rev-parse", "HEAD", check=False).strip()
    out["head_sha"] = head
    if base and head:
        patch = run_git(workspace, "diff", f"{base}..{head}", check=False)
        if patch:
            (vdir / "diff.patch").write_text(patch)
            out["patch"] = str(vdir / "diff.patch")
        stat = run_git(workspace, "diff", "--numstat", f"{base}..{head}", check=False)
        lines = [line for line in stat.splitlines() if line.strip()]
        out["files_changed"] = len(lines)
        out["uncommitted"] = bool(run_git(workspace, "status", "--porcelain", check=False).strip())
    return out


def cmd_clean(args) -> int:
    state_dir = Path(args.state_dir).expanduser() if args.state_dir else None
    rundir = RunDir.locate(args.run, state_dir)
    spec, state = rundir.spec_json(), rundir.state() if rundir.state_json.exists() else {"variants": {}}
    live = [r for r in state.get("variants", {}).values() if r["status"] not in TERMINAL]
    if live and not args.force:
        names = ", ".join(r["name"] for r in live)
        raise FanoutError(f"variants still active ({names}); stop the run or pass --force")
    if rundir.supervisor_pid.exists() and args.force:
        cmd_stop(args)
        time.sleep(1)
    repo = Path(spec["repo"])
    for record in state.get("variants", {}).values():
        workspace = record.get("workspace")
        if workspace and Path(workspace).exists():
            run_git(repo, "worktree", "remove", "--force", workspace, check=False)  # not tenon argv: --force
        if record.get("branch") and not args.keep_branches:
            run_git(repo, "branch", "-D", record["branch"], check=False)
    run_git(repo, "worktree", "prune", check=False)
    if not args.keep_state:
        shutil.rmtree(rundir.root, ignore_errors=True)
    print(f"fanout: cleaned {args.run}")
    return 0


# --------------------------------------------------------------------------
# argument parsing
# --------------------------------------------------------------------------


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="fanout",
        description="Dispatch and supervise k isolated tenon agents, one git worktree each.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    def add_state_dir(p):
        p.add_argument("--state-dir", help="run state root (default: $FANOUT_HOME or ~/.fanout)")

    start = sub.add_parser("start", help="prepare, apply, and run k variants")
    start.add_argument("--spec", help="JSON spec file; flags override its fields")
    start.add_argument("--run", help="run name (default: run-<timestamp>)")
    start.add_argument("--repo", help="git repository to fan out from (default: cwd's toplevel)")
    start.add_argument("--base", help="commit-ish each worktree starts from (default: HEAD)")
    start.add_argument(
        "--agent",
        help="agent project; relative resolves inside each worktree (agent edits land in the "
        "variant's branch), absolute is copied per variant",
    )
    start.add_argument("--harness", choices=["claude", "codex"], help="target harness")
    start.add_argument("--task", help="prompt dispatched as one turn")
    start.add_argument("--k", type=int, help="number of variants (mutually exclusive with a spec variants list)")
    start.add_argument("--mutate", help="shell command run in each variant's agent dir before the gate")
    start.add_argument("--pins", help="pin set the gate, compile, and dispatch all resolve against")
    start.add_argument("--concurrency", type=int, help="variants in flight at once (default: min(k, 4))")
    start.add_argument("--timeout", help=f"whole-process deadline per variant (default: 600s, max {MAX_RUN_TIMEOUT}s)")
    start.add_argument("--turn-timeout", help="per-turn deadline (default: none)")
    start.add_argument("--branch-prefix", help="branch namespace (default: fanout)")
    start.add_argument("--tenon", help="tenon binary (default: $FANOUT_TENON or PATH)")
    start.add_argument(
        "--fail-fast",
        action="store_true",
        help="cancel the remaining variants after the first failure (default: keep going)",
    )
    start.add_argument("--detach", action="store_true", help="background the supervisor and return immediately")
    start.add_argument("--dry-run", action="store_true", help="print the resolved spec and exit")
    add_state_dir(start)
    start.set_defaults(func=cmd_start)

    listing = sub.add_parser("list", help="runs known to the state dir")
    add_state_dir(listing)
    listing.set_defaults(func=cmd_list)

    status = sub.add_parser("status", help="per-variant lifecycle state")
    status.add_argument("run")
    status.add_argument("--json", action="store_true")
    add_state_dir(status)
    status.set_defaults(func=cmd_status)

    logs = sub.add_parser("logs", help="one variant's event stream")
    logs.add_argument("run")
    logs.add_argument("variant")
    logs.add_argument("-f", "--follow", action="store_true")
    logs.add_argument("--stderr", action="store_true", help="show the dispatch's stderr instead")
    logs.add_argument("--text", action="store_true", help="print reassembled agent output text")
    add_state_dir(logs)
    logs.set_defaults(func=cmd_logs)

    stop = sub.add_parser("stop", help="terminate a detached run")
    stop.add_argument("run")
    add_state_dir(stop)
    stop.set_defaults(func=cmd_stop)

    collect = sub.add_parser("collect", help="per-variant result records")
    collect.add_argument("run")
    collect.add_argument("--json", action="store_true")
    collect.add_argument("--text", action="store_true", help="include reassembled agent output")
    collect.add_argument("--no-commit", action="store_true", help="do not commit each worktree's result")
    add_state_dir(collect)
    collect.set_defaults(func=cmd_collect)

    clean = sub.add_parser("clean", help="remove worktrees, branches, and state")
    clean.add_argument("run")
    clean.add_argument("--force", action="store_true", help="stop a live run first")
    clean.add_argument("--keep-branches", action="store_true")
    clean.add_argument("--keep-state", action="store_true")
    add_state_dir(clean)
    clean.set_defaults(func=cmd_clean)

    supervise_cmd = sub.add_parser("_supervise", help=argparse.SUPPRESS)
    supervise_cmd.add_argument("root")
    supervise_cmd.set_defaults(func=cmd_supervise)

    return parser


def main(argv: list) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except TenonEnvironment as err:
        print(f"fanout: tenon environment failure: {err}", file=sys.stderr)
        return 1
    except FanoutError as err:
        print(f"fanout: {err}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
