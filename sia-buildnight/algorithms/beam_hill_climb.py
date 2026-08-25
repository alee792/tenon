"""Steepest-ascent (beam) hill climbing — the PRIMARY selector.

The most sample-efficient way to hold a selector, which is exactly what SIA
lacks. Each round the orchestrator proposes K candidates seeded from the
incumbent; this selector promotes the best-of-K only if it clears the incumbent
by the noise margin. Beam width B>1 (kept by the orchestrator) hedges local
optima without abandoning eval efficiency.

Best fit: expensive, noisy evaluations with few affordable generations — the
default Build Night regime.
"""

from __future__ import annotations

from typing import Sequence

from .base import Candidate, IncumbentRecord, Ledger, Selector


class BeamHillClimb(Selector):
    name = "beam-hill-climb"

    def __init__(self, noise_margin: float = 0.0, beam_width: int = 1):
        super().__init__(noise_margin=noise_margin)
        self.beam_width = beam_width

    def seed_from(
        self,
        ledger: Ledger,
        incumbent: IncumbentRecord | None,
        candidates: Sequence[Candidate],
    ) -> str | None:
        """Always branch the next edit from the incumbent — never from a
        regression. This single rule is the fix for SIA's blind linear chain."""
        if incumbent is not None and incumbent.agent_path:
            return incumbent.agent_path
        # Cold start (generation 1 has no incumbent yet): use the best evaluated
        # candidate if one exists, else let the caller fall back to the seed.
        best = self.best(candidates)
        return best.agent_path if best else None

    def accept(self, candidate: Candidate, incumbent: IncumbentRecord | None) -> bool:
        """Accept only a genuine improvement over the incumbent."""
        return self.beats_incumbent(candidate, incumbent)

    def select_beam(self, candidates: Sequence[Candidate]) -> list[Candidate]:
        """Top-B scored candidates, for an orchestrator that carries a beam."""
        scored = sorted(
            (c for c in candidates if c.score is not None),
            key=lambda c: c.score,
            reverse=True,
        )
        return scored[: self.beam_width]

    def next_hypothesis_hint(self, ledger: Ledger) -> str | None:
        """Nudge away from repeating the last accepted edit family, so a beam
        explores rather than piling onto one stage."""
        entries = ledger.all()
        for e in reversed(entries):
            if e.accepted and e.hypothesis:
                return f"avoid repeating '{e.hypothesis}'; vary the stage you touch"
        return None
