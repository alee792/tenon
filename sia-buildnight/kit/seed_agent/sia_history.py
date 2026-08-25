"""Deterministic incumbent surfacing — the model-independent half of selection.

The feedback agent *deciding* to branch from the incumbent is an LLM act we can
only guide. But *computing* which prior generation is the incumbent is pure
bookkeeping, so we do it here in deterministic stdlib code and hand the answer to
the feedback agent through the always-visible diagnostic channel. That removes
the least-reliable step (a small model correctly scanning + comparing every
sibling's results.json) and leaves the LLM only the decision.

This module is re-copied pristine into every generation by SIA
(`copy_reference_into`), so the feedback agent cannot corrupt it.

Boundary (be honest about it):
  - Under sandbox=none (SIA's default), a generation can read its sibling
    `../gen_*/results.json`, so the incumbent is computed exactly.
  - Under `--sandbox docker`, the target agent is isolated to its own gen dir and
    cannot see siblings; `surface_incumbent` then returns None, and the docstring
    protocol falls back to `context.md` (which the un-sandboxed feedback agent can
    always read). Either way the loop still selects — deterministically when it
    can, guided when it cannot.

Standard library only.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

GEN_RE = re.compile(r"gen_(\d+)$")


def parse_score(results: dict, metric: str = "accuracy") -> float | None:
    """Extract a scalar score from a results.json dict.

    Mirrors SIA's context_manager parsing: honor a named metric, accept
    percentage strings like "48.99%", and fall back to the first numeric
    top-level scalar so an unknown metric key still yields a comparable number.
    """
    if not isinstance(results, dict):
        return None
    val = results.get(metric)
    if val is None:
        for v in results.values():
            if isinstance(v, (int, float)) and not isinstance(v, bool):
                return float(v)
        return None
    if isinstance(val, bool):
        return None
    if isinstance(val, (int, float)):
        return float(val)
    if isinstance(val, str):
        try:
            return float(val.strip().rstrip("%"))
        except ValueError:
            return None
    return None


def _gen_num(gen_dir: Path) -> int | None:
    m = GEN_RE.search(gen_dir.name)
    return int(m.group(1)) if m else None


def compute_incumbent(working_dir: str | Path, metric: str = "accuracy") -> dict | None:
    """Return the best-scoring prior generation as
    {"gen", "score", "metric", "agent_path"}, or None if none is readable.

    `working_dir` is the CURRENT generation directory (…/run_K/gen_N). We scan
    every sibling `gen_*/results.json`, which under the default sandbox includes
    all already-evaluated generations. The current generation's own results.json
    does not exist yet at target-agent runtime, so it is naturally excluded.
    """
    gen_dir = Path(working_dir)
    run_dir = gen_dir.parent
    best: dict | None = None
    try:
        siblings = sorted(run_dir.glob("gen_*"))
    except OSError:
        return None
    for sib in siblings:
        results = sib / "results.json"
        if not results.exists():
            continue
        try:
            data = json.loads(results.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        score = parse_score(data, metric)
        if score is None:
            continue
        if best is None or score > best["score"]:
            agent = sib / "target_agent.py"
            best = {
                "gen": _gen_num(sib),
                "score": score,
                "metric": metric,
                "agent_path": str(agent) if agent.exists() else None,
            }
    return best


def surface_incumbent(working_dir: str | Path, metric: str = "accuracy") -> dict | None:
    """Compute the incumbent and return it. Kept as the single call site the seed
    agent uses so the feedback agent has one obvious symbol to preserve.

    The value is threaded into the diagnostic summary
    (`observability.TrajectoryLogger.finalize(extra=...)`), which lands in the
    always-visible channels the feedback prompt shows. This function has no side
    effects of its own, so it is safe to call before the run does any work.
    """
    return compute_incumbent(working_dir, metric)
