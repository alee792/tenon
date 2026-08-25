"""Selector library + a factory the triage judge and orchestrator share."""

from __future__ import annotations

from .annealed import Annealed
from .base import (
    HYPOTHESIS_CLASSES,
    Candidate,
    IncumbentRecord,
    IncumbentStore,
    Ledger,
    LedgerEntry,
    Selector,
    parse_score,
    read_score_from_gen,
)
from .beam_hill_climb import BeamHillClimb
from .strategy_bandit import StrategyBandit

__all__ = [
    "Annealed",
    "BeamHillClimb",
    "Candidate",
    "HYPOTHESIS_CLASSES",
    "IncumbentRecord",
    "IncumbentStore",
    "Ledger",
    "LedgerEntry",
    "Selector",
    "StrategyBandit",
    "make_selector",
    "parse_score",
    "read_score_from_gen",
]


def make_selector(
    kind: str,
    *,
    noise_margin: float = 0.0,
    beam_width: int = 1,
    with_bandit: bool = False,
    **kwargs,
) -> Selector:
    """Build a selector by name.

    kind: "beam-hill-climb" | "greedy" | "annealed".
    with_bandit wraps the base selector in the StrategyBandit meta-layer.
    """
    if kind in ("beam-hill-climb", "greedy", "hill-climb"):
        base: Selector = BeamHillClimb(
            noise_margin=noise_margin,
            beam_width=1 if kind == "greedy" else beam_width,
        )
    elif kind == "annealed":
        base = Annealed(noise_margin=noise_margin, **kwargs)
    else:
        raise ValueError(f"unknown selector kind: {kind!r}")

    return StrategyBandit(base) if with_bandit else base
