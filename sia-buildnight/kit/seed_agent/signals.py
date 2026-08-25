"""Cross-generation signal extraction — the memory the feedback agent lacks.

`observability.py` describes ONE generation (this run's failure taxonomy).
`sia_history.py` names the incumbent (the best prior score). Neither tells the
feedback agent the thing it most needs to reason about a *directed* edit:

  * what did my last edit actually change in the failure profile?
  * which hypothesis families have already been tried, and did they pay off?
  * did the last generation's *prediction* about its edit come true?
  * given all that, which family should I try next?

Those are cross-generation questions. Answering them is pure bookkeeping over the
sibling ``gen_*`` directories, so — like incumbent computation — we do it in
deterministic stdlib code and hand the answer to the feedback agent through the
always-visible diagnostic channel, instead of hoping a small model re-derives it
by eye from prose history.

Boundary (same as ``sia_history``): under ``sandbox=none`` (SIA's default) a
generation can read its siblings, so every signal here is exact. Under
``--sandbox docker`` the siblings are hidden and every function degrades to
``None`` / empty; the docstring protocol then falls back to ``context.md``.

This module is re-copied pristine into every generation by SIA
(``copy_reference_into``), so the feedback agent cannot corrupt it.

Standard library only.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

from sia_history import GEN_RE, parse_score  # pristine, re-copied each generation

# Which edit family a dominant *crash* signature points at. Ordered by
# specificity; the first match wins. Keys are matched against the error_class
# and the stage of the worst failure cluster.
_CRASH_FAMILY = {
    # output the model produced could not be parsed / serialized
    ("format", None): "harden-output-parsing",
    (None, "JSONDecodeError"): "harden-output-parsing",
    (None, "KeyError"): "harden-output-parsing",
    (None, "ValueError"): "harden-output-parsing",
    (None, "IndexError"): "harden-output-parsing",
    # the task-model call itself failed / timed out / was rate-limited
    (None, "TimeoutError"): "retry-on-malformed",
    (None, "APITimeoutError"): "retry-on-malformed",
    (None, "RateLimitError"): "retry-on-malformed",
    (None, "APIError"): "retry-on-malformed",
    (None, "APIConnectionError"): "retry-on-malformed",
    # context assembly / dataset access
    ("load", None): "improve-retrieval",
    ("retrieval", None): "improve-retrieval",
    ("plan", None): "improve-retrieval",
}

# When crashes are negligible but the score is flat, the failures are *semantic*
# (answers that are simply wrong — invisible to a crash taxonomy). Reach for
# reasoning families in this order.
_SEMANTIC_FAMILIES = (
    "self-consistency-voting",
    "decompose-reasoning",
    "add-verification",
    "restructure-prompt",
)

_IMPROVEMENT_BLOCK_RE = re.compile(
    r"^##\s*Generation\s+(\d+)\s*$(.*?)(?=^##\s*Generation\s+\d+\s*$|\Z)",
    re.MULTILINE | re.DOTALL,
)
_FIELD_RE = re.compile(r"^\s*-\s*([A-Za-z_ ]+?)\s*:\s*(.*?)\s*$", re.MULTILINE)


def _gen_num(gen_dir: Path) -> int | None:
    m = GEN_RE.search(gen_dir.name)
    return int(m.group(1)) if m else None


def load_diagnostic(gen_dir: str | Path) -> dict | None:
    """Read a sibling generation's sort-first diagnostic file, or None."""
    p = Path(gen_dir) / "agent_execution" / "execution_q-diagnostic.json"
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None


def parse_improvement_ledger(gen_dir: str | Path) -> dict | None:
    """Parse the newest ``## Generation N`` block from a gen dir's
    ``improvement.md`` into a flat dict (hypothesis, predicted_effect, …).

    The feedback agent writes this file with the fixed schema from the
    ``target_agent.py`` docstring; we read it back deterministically.
    """
    p = Path(gen_dir) / "improvement.md"
    if not p.exists():
        return None
    try:
        text = p.read_text(encoding="utf-8")
    except OSError:
        return None
    blocks = _IMPROVEMENT_BLOCK_RE.findall(text)
    if not blocks:
        return None
    gen_str, body = blocks[-1]  # newest block in this file
    fields = {k.strip().lower().replace(" ", "_"): v for k, v in _FIELD_RE.findall(body)}
    fields["generation"] = int(gen_str)
    return fields


def _sibling_gens(working_dir: str | Path) -> list[tuple[int, Path]]:
    """Sorted ``(gen_num, dir)`` for every readable *prior* generation.

    ``working_dir`` is the CURRENT generation dir; it is excluded, so callers
    always see predecessors only. The current gen's own results/diagnostic don't
    exist yet at target-agent runtime anyway — excluding it keeps the tests (and
    any partial writes) honest.
    """
    here = Path(working_dir).resolve()
    run_dir = here.parent
    out: list[tuple[int, Path]] = []
    try:
        for sib in run_dir.glob("gen_*"):
            n = _gen_num(sib)
            if n is not None and sib.resolve() != here:
                out.append((n, sib))
    except OSError:
        return []
    return sorted(out, key=lambda t: t[0])


def failure_delta(current_summary: dict, prev_diag: dict | None) -> dict | None:
    """Compare this run's failure taxonomy against the immediate predecessor's.

    This is the "what did my last edit do to the failure profile" signal — the
    one thing the feedback agent cannot see today. Score isn't available for the
    current run at target-agent runtime (its ``results.json`` doesn't exist yet),
    so we compare the *failure classes* the agent can observe itself, not scores.
    """
    if not prev_diag:
        return None
    prev = prev_diag.get("failures_by_error_class") or {}
    curr = current_summary.get("failures_by_error_class") or {}
    keys = set(prev) | set(curr)
    by_class = {
        k: {"prev": prev.get(k, 0), "curr": curr.get(k, 0),
            "delta": curr.get(k, 0) - prev.get(k, 0)}
        for k in sorted(keys)
    }
    return {
        "prev_gen": prev_diag.get("gen"),
        "prev_failed": prev_diag.get("failed"),
        "curr_failed": current_summary.get("failed"),
        "new_failure_classes": sorted(k for k in curr if k not in prev),
        "cleared_failure_classes": sorted(k for k in prev if k not in curr),
        "by_error_class": by_class,
    }


def tried_digest(working_dir: str | Path) -> list[dict]:
    """One row per prior generation: what family it tried and whether the score
    moved. Feeds the "don't repeat a family that didn't pay off" guidance and is
    the in-loop, always-visible equivalent of the strategy bandit's ledger read.
    """
    gens = _sibling_gens(working_dir)
    rows: list[dict] = []
    prev_score: float | None = None
    for n, d in gens:
        score = parse_score_of(d)
        ledger = parse_improvement_ledger(d) or {}
        delta = (score - prev_score) if (score is not None and prev_score is not None) else None
        rows.append({
            "gen": n,
            "hypothesis": ledger.get("hypothesis"),
            "score": score,
            "delta_vs_prev": None if delta is None else round(delta, 4),
            "paid_off": (delta is not None and delta > 0),
        })
        if score is not None:
            prev_score = score
    return rows


def parse_score_of(gen_dir: str | Path, metric: str = "accuracy") -> float | None:
    """Score of a generation from its results.json, or None if not yet scored."""
    p = Path(gen_dir) / "results.json"
    if not p.exists():
        return None
    try:
        return parse_score(json.loads(p.read_text(encoding="utf-8")), metric)
    except (OSError, json.JSONDecodeError):
        return None


def prediction_check(working_dir: str | Path) -> dict | None:
    """Did the most recent prior generation's stated prediction come true?

    Reads the newest scored predecessor's ``improvement.md`` prediction and
    compares it to that generation's actual score movement. Closes the
    hypothesis→evidence loop the ledger's ``predicted_effect`` field opens but
    never checks.
    """
    gens = _sibling_gens(working_dir)
    scored = [(n, d, parse_score_of(d)) for n, d in gens]
    scored = [(n, d, s) for n, d, s in scored if s is not None]
    if len(scored) < 2:
        return None
    (n_prev, d_prev, s_prev) = scored[-1]
    (_, _, s_before) = scored[-2]
    ledger = parse_improvement_ledger(d_prev)
    if not ledger:
        return None
    actual = round(s_prev - s_before, 4)
    return {
        "gen": n_prev,
        "hypothesis": ledger.get("hypothesis"),
        "predicted_effect": ledger.get("predicted_effect"),
        "actual_delta": actual,
        "held": actual > 0,
    }


def recommend_hypothesis(current_summary: dict, working_dir: str | Path) -> dict:
    """Pick the next edit family — deterministically, from evidence.

    1. If crashes dominate, map the worst crash cluster to its family.
    2. Else the failures are semantic (score flat, few crashes): pick the first
       reasoning family not already tried without payoff.
    Always excludes families that were tried and did NOT pay off, so the loop
    explores instead of retrying a dud. This is the always-visible, in-loop
    analogue of the strategy bandit — no fork required.
    """
    digest = tried_digest(working_dir)
    tried_no_payoff = {r["hypothesis"] for r in digest
                       if r["hypothesis"] and not r["paid_off"]}

    failed = current_summary.get("failed") or 0
    total = current_summary.get("total_samples") or 0
    crash_rate = (failed / total) if total else 0.0

    # Score has been flat if the last two scored deltas are <= 0.
    recent_deltas = [r["delta_vs_prev"] for r in digest if r["delta_vs_prev"] is not None]
    plateaued = len(recent_deltas) >= 2 and all(d <= 0 for d in recent_deltas[-2:])

    spent_crash_family: str | None = None
    if crash_rate >= 0.05 and failed:
        worst_stage = current_summary.get("worst_stage")
        worst_class = _worst_error_class(current_summary)
        family = (_CRASH_FAMILY.get((None, worst_class))
                  or _CRASH_FAMILY.get((worst_stage, None)))
        if family and family not in tried_no_payoff:
            return {"family": family,
                    "reason": f"{failed}/{total} samples crashed; worst is "
                              f"{worst_class or worst_stage!r} → {family}"}
        if family:
            spent_crash_family = family  # the obvious family is already spent

    for family in _SEMANTIC_FAMILIES:
        if family not in tried_no_payoff:
            if spent_crash_family:
                reason = (f"'{spent_crash_family}' already tried without payoff; "
                          f"escalate to reasoning → {family}")
            elif plateaued or crash_rate < 0.05:
                reason = f"score is flat with few crashes — failures are semantic → {family}"
            else:
                reason = f"no crash family maps cleanly → {family}"
            return {"family": family, "reason": reason}

    # Everything reasonable has been tried without payoff; say so plainly.
    return {"family": None,
            "reason": "every family tried without payoff — revert to the "
                      "incumbent and revisit the prompt / task assumptions"}


def _worst_error_class(summary: dict) -> str | None:
    by_class = summary.get("failures_by_error_class") or {}
    return max(by_class, key=by_class.get) if by_class else None


def gather(working_dir: str | Path, current_summary: dict,
           incumbent: dict | None = None) -> dict:
    """Assemble every cross-generation signal into one ``extra`` payload for
    ``TrajectoryLogger.finalize`` — merged into the sort-first diagnostic the
    feedback agent always sees. Every field degrades to None/empty under
    sandboxing, so this is safe to call unconditionally.
    """
    gens = _sibling_gens(working_dir)
    prev_diag = None
    if gens:
        # newest sibling = the generation this code descended from
        prev_diag = load_diagnostic(gens[-1][1])
        if prev_diag is not None and "gen" not in prev_diag:
            prev_diag = {**prev_diag, "gen": gens[-1][0]}

    return {
        "recommended_hypothesis": recommend_hypothesis(current_summary, working_dir),
        "cross_gen": {
            "failure_delta": failure_delta(current_summary, prev_diag),
            "tried": tried_digest(working_dir),
            "prediction_check": prediction_check(working_dir),
        },
    }
