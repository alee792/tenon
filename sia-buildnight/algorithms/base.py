"""Selection algorithms for SIA Build Night — shared substrate.

SIA ships a *generator* (an LLM that proposes directed edits) but no *selector*:
its loop derives generation N+1 from generation N regardless of score. This
module is the missing selector, factored so three interchangeable strategies
(beam hill climbing, simulated annealing, strategy bandit) share one interface,
one experiment ledger, and one incumbent store.

Pure standard library so the credential-free tests run anywhere. The seed
agent's runtime dependencies (anthropic, pandas, …) live in the seed package,
never here.

Vocabulary (kept academic on purpose):
  incumbent — the best-scoring agent found so far; challengers are compared to
              it. (Not "champion".)
  candidate — one evaluated agent version for a generation.
  ledger    — the append-only experiment history (one line per generation).
"""

from __future__ import annotations

import json
import time
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Iterable, Sequence

# Edit families the strategy bandit arms over and the guidance doc references.
# Deliberately task-agnostic; the build agent may append task-specific ones.
HYPOTHESIS_CLASSES: tuple[str, ...] = (
    "harden-output-parsing",     # make the submission-formatting stage robust
    "self-consistency-voting",   # sample k times, take a majority / best
    "retry-on-malformed",        # detect a bad sample result and retry it
    "restructure-prompt",        # rewrite the task-model prompt / instructions
    "decompose-reasoning",       # split solve_one into explicit sub-steps
    "improve-retrieval",         # better select / order context given to solve
    "add-verification",          # a check pass that validates before writing
)


# --------------------------------------------------------------------------- #
# Score parsing — mirrors SIA's context_manager.py so our numbers match theirs.
# --------------------------------------------------------------------------- #
def parse_score(results: dict, metric: str = "accuracy") -> float | None:
    """Extract a scalar score from a results.json dict.

    Handles percentage strings like "48.99%" exactly as SIA does, and falls back
    to the first numeric top-level scalar when the named metric is absent.
    """
    if not isinstance(results, dict):
        return None
    val = results.get(metric)
    if val is None:
        for v in results.values():
            if isinstance(v, (int, float)) and not isinstance(v, bool):
                return float(v)
        return None
    if isinstance(val, str):
        try:
            return float(val.strip().rstrip("%"))
        except ValueError:
            return None
    if isinstance(val, bool):
        return None
    if isinstance(val, (int, float)):
        return float(val)
    return None


def read_score_from_gen(gen_dir: str | Path, metric: str = "accuracy") -> float | None:
    """Read results.json from a generation directory and parse its score."""
    p = Path(gen_dir) / "results.json"
    if not p.exists():
        return None
    try:
        return parse_score(json.loads(p.read_text(encoding="utf-8")), metric)
    except (json.JSONDecodeError, OSError):
        return None


# --------------------------------------------------------------------------- #
# Data model
# --------------------------------------------------------------------------- #
@dataclass(frozen=True)
class Candidate:
    """One evaluated agent version."""

    gen: int
    agent_path: str
    score: float | None
    metric: str = "accuracy"
    hypothesis: str = ""          # a HYPOTHESIS_CLASSES value, when known
    edit_summary: str = ""
    meta: dict = field(default_factory=dict)


@dataclass
class LedgerEntry:
    gen: int
    hypothesis: str
    edit_summary: str
    score: float | None
    delta_vs_incumbent: float | None
    accepted: bool
    timestamp: str = ""

    @classmethod
    def from_candidate(cls, c: Candidate, incumbent_score: float | None, accepted: bool) -> "LedgerEntry":
        delta = None
        if c.score is not None and incumbent_score is not None:
            delta = c.score - incumbent_score
        return cls(
            gen=c.gen,
            hypothesis=c.hypothesis,
            edit_summary=c.edit_summary,
            score=c.score,
            delta_vs_incumbent=delta,
            accepted=accepted,
            timestamp=time.strftime("%Y-%m-%d %H:%M:%S"),
        )


class Ledger:
    """Append-only experiment history at ledger.jsonl."""

    def __init__(self, path: str | Path):
        self.path = Path(path)

    def append(self, entry: LedgerEntry) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with self.path.open("a", encoding="utf-8") as f:
            f.write(json.dumps(asdict(entry)) + "\n")

    def all(self) -> list[LedgerEntry]:
        if not self.path.exists():
            return []
        out: list[LedgerEntry] = []
        for line in self.path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                out.append(LedgerEntry(**json.loads(line)))
            except (json.JSONDecodeError, TypeError):
                continue
        return out


@dataclass
class IncumbentRecord:
    gen: int
    score: float | None
    metric: str = "accuracy"
    agent_path: str = ""
    timestamp: str = ""


class IncumbentStore:
    """The best-so-far agent's metadata at incumbent.json.

    Only tracks metadata here; copying the actual agent file forward is the
    orchestrator's job (see kit/orchestrate.py) so this stays dependency-free.
    """

    def __init__(self, path: str | Path):
        self.path = Path(path)

    def get(self) -> IncumbentRecord | None:
        if not self.path.exists():
            return None
        try:
            return IncumbentRecord(**json.loads(self.path.read_text(encoding="utf-8")))
        except (json.JSONDecodeError, TypeError, OSError):
            return None

    def set(self, rec: IncumbentRecord) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        rec.timestamp = rec.timestamp or time.strftime("%Y-%m-%d %H:%M:%S")
        self.path.write_text(json.dumps(asdict(rec), indent=2), encoding="utf-8")


# --------------------------------------------------------------------------- #
# Selector base class
# --------------------------------------------------------------------------- #
class Selector:
    """Common selection interface. Subclasses answer three questions.

    seed_from(...)            -> path of the agent the next edit should start from
    accept(candidate, ...)    -> should this candidate replace / advance state?
    next_hypothesis_hint(...) -> optional steer on *what kind* of edit to try

    ``noise_margin`` is the score improvement required to call a change real,
    guarding against noisy evaluations accepting a lucky-bad move.
    """

    name = "selector"

    def __init__(self, noise_margin: float = 0.0):
        self.noise_margin = noise_margin

    def seed_from(
        self,
        ledger: Ledger,
        incumbent: IncumbentRecord | None,
        candidates: Sequence[Candidate],
    ) -> str | None:
        raise NotImplementedError

    def accept(self, candidate: Candidate, incumbent: IncumbentRecord | None) -> bool:
        raise NotImplementedError

    def next_hypothesis_hint(self, ledger: Ledger) -> str | None:
        return None

    # -- helpers shared by subclasses -------------------------------------- #
    @staticmethod
    def best(candidates: Iterable[Candidate]) -> Candidate | None:
        scored = [c for c in candidates if c.score is not None]
        return max(scored, key=lambda c: c.score) if scored else None

    def beats_incumbent(self, candidate: Candidate, incumbent: IncumbentRecord | None) -> bool:
        if candidate.score is None:
            return False
        if incumbent is None or incumbent.score is None:
            return True
        return candidate.score > incumbent.score + self.noise_margin
