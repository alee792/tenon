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

# A sample slower than this is flagged as an efficiency risk. Some SIA challenges
# score partly on runtime, so latency is a first-class signal, not a footnote.
DEFAULT_LATENCY_BUDGET_MS = 20_000.0
EXEMPLARS_PER_CLUSTER = 2          # concrete failing cases per cluster
MAX_CLUSTERS = 4                   # keep the diagnostic small (SIA size-caps it)
_FIELD_TRUNC = 240                 # cap any single expected/got string


def _truncate(val: Any, limit: int = _FIELD_TRUNC) -> Any:
    """Shorten long strings so the diagnostic stays inside SIA's size cap while
    still carrying a readable exemplar."""
    if isinstance(val, str) and len(val) > limit:
        return val[:limit] + f"…(+{len(val) - limit} chars)"
    return val


def _percentile(sorted_vals: list[float], pct: float) -> float | None:
    if not sorted_vals:
        return None
    k = (len(sorted_vals) - 1) * pct
    lo = int(k)
    hi = min(lo + 1, len(sorted_vals) - 1)
    frac = k - lo
    return round(sorted_vals[lo] + (sorted_vals[hi] - sorted_vals[lo]) * frac, 2)


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

    def __init__(self, working_dir: str | Path,
                 latency_budget_ms: float = DEFAULT_LATENCY_BUDGET_MS):
        self.dir = Path(working_dir) / EXECUTION_DIRNAME
        self.dir.mkdir(parents=True, exist_ok=True)
        self.records: list[SampleRecord] = []
        self.latency_budget_ms = latency_budget_ms

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
            # Concrete failing cases, grouped, so the feedback agent sees WHAT to
            # fix — not just counts. SIA's own first-3 trajectory window is
            # positional; these are chosen for being informative.
            "clusters": self._clusters(failed),
            # Calibration: are low-confidence samples the ones going wrong, and is
            # confidence even a live signal or a hardcoded constant?
            "confidence": self._confidence_signal(),
            # Efficiency: p50/p95 and the count of samples over budget (complements
            # the total/mean latency above with distribution + a budget breach count).
            "latency": self._latency_signal(),
            "hint": (
                f"Most failures are in the '{by_stage.most_common(1)[0][0]}' stage "
                f"({by_stage.most_common(1)[0][1]}/{len(failed)}); focus there."
                if by_stage else "No failures recorded; if the score is still low the "
                "failures are SEMANTIC (wrong answers a crash taxonomy can't see) — "
                "reach for reasoning families and wire confidence to a real signal."
            ),
        }

    def _clusters(self, failed: list[SampleRecord]) -> list[dict]:
        """Group failures by (stage, error_class) and attach a few real
        exemplars per cluster, biggest cluster first."""
        buckets: dict[tuple[str, str], list[SampleRecord]] = {}
        for r in failed:
            key = (r.get("stage", "?"), r.get("error_class") or "?")
            buckets.setdefault(key, []).append(r)
        ranked = sorted(buckets.items(), key=lambda kv: len(kv[1]), reverse=True)
        out: list[dict] = []
        for (stage, error_class), recs in ranked[:MAX_CLUSTERS]:
            out.append({
                "stage": stage,
                "error_class": error_class,
                "count": len(recs),
                "examples": [
                    {
                        "sample_id": r.get("sample_id"),
                        "expected": _truncate(r.get("expected")),
                        "got": _truncate(r.get("got")),
                        "notes": _truncate(r.get("notes")),
                    }
                    for r in recs[:EXEMPLARS_PER_CLUSTER]
                ],
            })
        return out

    def _confidence_signal(self) -> dict:
        vals = [r.get("confidence") for r in self.records
                if isinstance(r.get("confidence"), (int, float))]
        if not vals:
            return {"available": False, "degenerate": True,
                    "note": "no confidence logged — solve_one must return one"}
        lo = sum(1 for v in vals if v < 0.34)
        mid = sum(1 for v in vals if 0.34 <= v < 0.67)
        hi = sum(1 for v in vals if v >= 0.67)
        degenerate = len(set(round(v, 4) for v in vals)) == 1
        return {
            "available": True,
            "degenerate": degenerate,
            "buckets": {"low<0.34": lo, "mid": mid, "high>=0.67": hi},
            "note": ("confidence is a constant — wire it to logprobs / vote "
                     "agreement so low-confidence samples become actionable"
                     if degenerate else
                     f"{lo} low-confidence samples are the best retry/vote targets"),
        }

    def _latency_signal(self) -> dict:
        vals = sorted(r.get("latency_ms") for r in self.records
                      if isinstance(r.get("latency_ms"), (int, float)))
        over = sum(1 for v in vals if v > self.latency_budget_ms)
        return {
            "budget_ms": self.latency_budget_ms,
            "p50_ms": _percentile(vals, 0.50),
            "p95_ms": _percentile(vals, 0.95),
            "over_budget": over,
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
        extra = extra or {}
        incumbent = extra.get("incumbent")
        rec = extra.get("recommended_hypothesis") or {}
        cross = extra.get("cross_gen") or {}
        delta = cross.get("failure_delta")
        pred = cross.get("prediction_check")
        # stdout tail — SIA shows only the last ~10 lines, so print the most
        # actionable signals last. Full detail lives in the diagnostic JSON.
        print("=== DIAGNOSTIC SUMMARY ===")
        print(f"samples={s['total_samples']} failed={s['failed']} "
              f"success_rate={s['success_rate']}")
        print(f"failures_by_stage={s['failures_by_stage']}")
        print(f"failures_by_error_class={s['failures_by_error_class']}")
        print(f"worst_stage={s['worst_stage']} mean_confidence={s['mean_confidence']}")
        lat = s.get("latency", {})
        print(f"COST: total_tokens={s['total_tokens']} total_latency_ms={s['total_latency_ms']} "
              f"| latency p50={lat.get('p50_ms')}ms p95={lat.get('p95_ms')}ms "
              f"over_budget={lat.get('over_budget')}")
        if incumbent:
            print(f"INCUMBENT: gen={incumbent.get('gen')} score={incumbent.get('score')} "
                  f"(branch the next edit from gen_{incumbent.get('gen')} if it beats this run)")
        else:
            print("INCUMBENT: none visible here — read context.md for prior scores")
        if delta:
            print(f"DELTA vs gen_{delta.get('prev_gen')}: "
                  f"new_failures={delta.get('new_failure_classes')} "
                  f"cleared={delta.get('cleared_failure_classes')}")
        if pred:
            print(f"LAST PREDICTION ({pred.get('hypothesis')}): "
                  f"predicted improvement, actual_delta={pred.get('actual_delta')} "
                  f"→ {'held' if pred.get('held') else 'FAILED — revert & change family'}")
        if rec.get("family"):
            print(f"RECOMMEND: try '{rec['family']}' — {rec.get('reason')}")
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
