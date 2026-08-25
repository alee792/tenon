"""Observability for the target agent — the evidence base the SIA feedback
agent reasons over.

SIA surfaces, to the feedback agent, only: the first 3 files in
``agent_execution/`` (glob ``execution_q*.json``, sorted), a success/fail count,
the full ``results.json``, and the target agent's last 10 stdout lines. Whatever
we log is the *entire* signal driving diagnosis — so we log a structured failure
taxonomy per sample, and we guarantee an aggregate diagnostic reaches the two
always-visible channels:

  1. stdout tail  — printed as the final lines of the run (always shown);
  2. a sort-first trajectory file ``execution_q-diagnostic.json`` — '-' (0x2d)
     sorts before any digit, so it lands in the first-3 window SIA shows.

Standard library only.
"""

from __future__ import annotations

import json
import time
from collections import Counter
from pathlib import Path
from typing import Any

EXECUTION_DIRNAME = "agent_execution"
DIAGNOSTIC_FILENAME = "execution_q-diagnostic.json"  # sorts before execution_q0.json


class SampleRecord(dict):
    """One sample's structured trace. A dict subclass so it JSON-dumps cleanly
    and the feedback agent can read it without a schema."""

    def __init__(
        self,
        sample_id: Any,
        *,
        stage: str = "solve",
        ok: bool = True,
        error_class: str | None = None,
        expected: Any = None,
        got: Any = None,
        confidence: float | None = None,
        latency_ms: float | None = None,
        tokens: int | None = None,
        messages: list | None = None,
        notes: str = "",
    ):
        super().__init__(
            sample_id=sample_id,
            stage=stage,
            ok=ok,
            error_class=error_class,
            expected=expected,
            got=got,
            confidence=confidence,
            latency_ms=latency_ms,
            tokens=tokens,  # per-sample token cost, if the client reports usage
            messages=messages or [],
            notes=notes,
        )


class TrajectoryLogger:
    """Per-sample structured logging + an aggregate diagnostic summary.

    Usage in target_agent.py:

        log = TrajectoryLogger(working_dir)
        for i, sample in enumerate(samples):
            with log.sample(i) as rec:
                rec["got"] = solve_one(sample)         # fill fields as you go
        log.finalize()
    """

    def __init__(self, working_dir: str | Path):
        self.dir = Path(working_dir) / EXECUTION_DIRNAME
        self.dir.mkdir(parents=True, exist_ok=True)
        self.records: list[SampleRecord] = []

    # Context manager per sample so a crash is captured as a failure record,
    # never lost — robustness the feedback agent can see and fix.
    def sample(self, sample_id: Any, stage: str = "solve") -> "_SampleCtx":
        return _SampleCtx(self, sample_id, stage)

    def add(self, rec: SampleRecord) -> None:
        self.records.append(rec)
        idx = len(self.records) - 1
        (self.dir / f"execution_q{idx}.json").write_text(
            json.dumps(rec, indent=2, default=str), encoding="utf-8"
        )

    def summary(self) -> dict:
        total = len(self.records)
        failed = [r for r in self.records if not r.get("ok", True)]
        by_stage = Counter(r.get("stage", "?") for r in failed)
        by_error = Counter(r.get("error_class") or "?" for r in failed)
        confidences = [r.get("confidence") for r in self.records if isinstance(r.get("confidence"), (int, float))]
        latencies = [r.get("latency_ms") for r in self.records if isinstance(r.get("latency_ms"), (int, float))]
        token_counts = [r.get("tokens") for r in self.records if isinstance(r.get("tokens"), (int, float))]
        return {
            "type": "DIAGNOSTIC_SUMMARY",
            "total_samples": total,
            "failed": len(failed),
            "success_rate": round((total - len(failed)) / total, 4) if total else None,
            "failures_by_stage": dict(by_stage),
            "failures_by_error_class": dict(by_error),
            "mean_confidence": round(sum(confidences) / len(confidences), 4) if confidences else None,
            "worst_stage": by_stage.most_common(1)[0][0] if by_stage else None,
            # Cost — so the feedback agent can weigh a hypothesis's gain against its
            # cost (Self-Harness Pareto): don't keep a tactic that buys little for a
            # lot of tokens/latency. `total_tokens` is None if the client reports no
            # usage; log it from solve_one when available.
            "total_latency_ms": round(sum(latencies), 1) if latencies else None,
            "mean_latency_ms": round(sum(latencies) / len(latencies), 1) if latencies else None,
            "total_tokens": int(sum(token_counts)) if token_counts else None,
            "hint": (
                f"Most failures are in the '{by_stage.most_common(1)[0][0]}' stage "
                f"({by_stage.most_common(1)[0][1]}/{len(failed)}); focus there."
                if by_stage else "No failures recorded; look for low-confidence correct samples."
            ),
        }

    def finalize(self, extra: dict | None = None) -> dict:
        """Write the diagnostic to the sort-first file AND print it to stdout so
        it reaches both always-visible feedback channels. Returns the summary.

        ``extra`` merges deterministic, non-per-sample facts into the diagnostic —
        notably ``{"incumbent": {...}}`` from ``sia_history.surface_incumbent``,
        so the feedback agent is handed the best-so-far generation and its score
        instead of having to derive them. ``incumbent`` may be None (sandboxed or
        gen 1); we surface that explicitly so the protocol's fallback is obvious.
        """
        s = self.summary()
        if extra:
            s.update(extra)
        (self.dir / DIAGNOSTIC_FILENAME).write_text(
            json.dumps(s, indent=2), encoding="utf-8"
        )
        incumbent = (extra or {}).get("incumbent") if extra else None
        # stdout tail — keep it compact (<= 10 lines) and last.
        print("=== DIAGNOSTIC SUMMARY ===")
        print(f"samples={s['total_samples']} failed={s['failed']} "
              f"success_rate={s['success_rate']}")
        print(f"failures_by_stage={s['failures_by_stage']}")
        print(f"failures_by_error_class={s['failures_by_error_class']}")
        print(f"worst_stage={s['worst_stage']} mean_confidence={s['mean_confidence']}")
        print(f"COST: total_tokens={s['total_tokens']} total_latency_ms={s['total_latency_ms']} "
              f"mean_latency_ms={s['mean_latency_ms']}")
        if incumbent:
            print(f"INCUMBENT: gen={incumbent.get('gen')} score={incumbent.get('score')} "
                  f"(branch the next edit from gen_{incumbent.get('gen')} if it beats this run)")
        else:
            print("INCUMBENT: none visible here — read context.md for prior scores")
        print(f"HINT: {s['hint']}")
        print("=== END DIAGNOSTIC SUMMARY ===")
        return s


class _SampleCtx:
    def __init__(self, logger: TrajectoryLogger, sample_id: Any, stage: str):
        self.logger = logger
        self.rec = SampleRecord(sample_id, stage=stage)
        self._t0 = 0.0

    def __enter__(self) -> SampleRecord:
        self._t0 = time.perf_counter()
        return self.rec

    def __exit__(self, exc_type, exc, tb) -> bool:
        self.rec["latency_ms"] = round((time.perf_counter() - self._t0) * 1000, 2)
        if exc is not None:
            self.rec["ok"] = False
            self.rec["error_class"] = exc_type.__name__
            self.rec["notes"] = (self.rec.get("notes") or "") + f" exception: {exc}"
        self.logger.add(self.rec)
        return True  # swallow the exception: one bad sample must not kill the run
