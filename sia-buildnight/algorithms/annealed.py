"""Simulated-annealing-lite — the LOCAL-OPTIMA HEDGE selector.

Single track (no beam), but occasionally accepts a slightly-worse candidate with
a probability that decays as the run cools. Escapes deceptive local optima when
a beam is unaffordable because each evaluation is too expensive to run K-wide.

Unlike hill climbing, annealing tracks a *current* state that may differ from the
incumbent: it can wander downhill temporarily, while the incumbent store still
records the best-ever agent so we never lose it.

Determinism: an injectable ``rng`` (``random.Random``) keeps the tests exact.
"""

from __future__ import annotations

import math
import random
from typing import Sequence

from .base import Candidate, IncumbentRecord, Ledger, Selector


class Annealed(Selector):
    name = "annealed"

    def __init__(
        self,
        noise_margin: float = 0.0,
        t0: float = 1.0,
        cooling: float = 0.85,
        t_min: float = 0.05,
        rng: random.Random | None = None,
    ):
        super().__init__(noise_margin=noise_margin)
        self.t0 = t0
        self.cooling = cooling
        self.t_min = t_min
        self._rng = rng or random.Random()
        # Current track state (distinct from the incumbent).
        self.current_score: float | None = None
        self.current_path: str | None = None
        self.step = 0

    def temperature(self) -> float:
        return max(self.t_min, self.t0 * (self.cooling ** self.step))

    def seed_from(
        self,
        ledger: Ledger,
        incumbent: IncumbentRecord | None,
        candidates: Sequence[Candidate],
    ) -> str | None:
        """Branch from the current track position (which may be below the
        incumbent mid-wander), not from the incumbent."""
        if self.current_path:
            return self.current_path
        if incumbent is not None and incumbent.agent_path:
            return incumbent.agent_path
        best = self.best(candidates)
        return best.agent_path if best else None

    def accept(self, candidate: Candidate, incumbent: IncumbentRecord | None) -> bool:
        """Metropolis rule: always take an improvement over the current track;
        take a regression with probability exp(-Δ/T). Advances internal state."""
        self.step += 1
        if candidate.score is None:
            return False

        take: bool
        if self.current_score is None or candidate.score > self.current_score + self.noise_margin:
            take = True
        else:
            delta = self.current_score - candidate.score  # >= 0, how much worse
            prob = math.exp(-delta / self.temperature())
            take = self._rng.random() < prob

        if take:
            self.current_score = candidate.score
            self.current_path = candidate.agent_path
        return take

    def next_hypothesis_hint(self, ledger: Ledger) -> str | None:
        # Hotter early runs explore broadly; cooler late runs consolidate.
        if self.temperature() > (self.t0 * 0.5):
            return "exploration phase: try a different edit family than last time"
        return "consolidation phase: refine what has been working"
