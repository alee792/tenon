#!/usr/bin/env python3
"""A human-in-the-loop pairwise judge for an evolve search.

Absolute scoring is the wrong thing to ask a person for. "How good is this
agent's answer, from 0 to 1?" produces a number that drifts with mood, order,
and fatigue. "Which of these two is better?" is the judgement people are
actually reliable at, so this serves pairwise comparisons and derives the
scalar fitness from the outcomes — the PAPRIKA-style preference framing.

The awkward part is the shape of evolve's scoring API: `score` is called once
per trial, sequentially, after the whole generation has run. A pairwise judge
cannot answer the first call without seeing the others, and evolve is blocked
waiting, so no others are coming. This server breaks that deadlock by not
depending on the clients at all: fanout has already finished writing the
generation's state by the time scoring starts, so on the first request the
server reads every variant of that generation off disk, runs the full
round-robin in the browser, and answers all the blocked clients from the
result.

Usage:
  python3 fanout/judge/server.py [--port 8917]

Then point a search's score command at fanout/judge/score-pairwise.py.
"""

from __future__ import annotations

import argparse
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from itertools import combinations
from pathlib import Path

PAGE = (Path(__file__).parent / "ui.html").read_text()


class Round:
    """One generation's judging: the entries, the pairs, and the verdicts."""

    def __init__(self, run_root: Path, generation: int, task_index: int):
        self.run_root = run_root
        self.generation = generation
        self.task_index = task_index
        self.entries: list = []
        self.pairs: list = []
        self.verdicts: dict = {}
        self.task = ""
        self.load()

    def load(self) -> None:
        gen_dir = self.run_root / "generations" / f"gen-{self.generation}" / "variants"
        if not gen_dir.is_dir():
            raise FileNotFoundError(f"no generation state at {gen_dir}")
        for variant in sorted(gen_dir.iterdir()):
            # Variant names are <short>-t<task>r<repeat>; only compare like
            # with like, since a different task is a different question.
            name = variant.name
            if f"-t{self.task_index}r" not in name:
                continue
            events = variant / "events.jsonl"
            if not events.exists():
                continue
            self.entries.append({"key": name, "short": name.split("-t")[0], "text": read_text(events)})
        self.entries.sort(key=lambda e: e["key"])
        # Full round robin. With k=5 that is ten comparisons per generation,
        # which a person can actually finish.
        self.pairs = [
            {"id": f"{a['key']}|{b['key']}", "a": a["key"], "b": b["key"]}
            for a, b in combinations(self.entries, 2)
        ]

    @property
    def done(self) -> bool:
        return len(self.verdicts) >= len(self.pairs)

    def next_pair(self):
        for pair in self.pairs:
            if pair["id"] not in self.verdicts:
                return pair
        return None

    def scores(self) -> dict:
        """Win rate over the comparisons each entry took part in. A tie counts
        half. With a full round robin this is the Copeland score, which is all
        the resolution five candidates can support."""
        wins = {e["key"]: 0.0 for e in self.entries}
        played = {e["key"]: 0 for e in self.entries}
        for pair in self.pairs:
            verdict = self.verdicts.get(pair["id"])
            if verdict is None:
                continue
            played[pair["a"]] += 1
            played[pair["b"]] += 1
            if verdict == "a":
                wins[pair["a"]] += 1
            elif verdict == "b":
                wins[pair["b"]] += 1
            else:
                wins[pair["a"]] += 0.5
                wins[pair["b"]] += 0.5
        # An entry nobody could compare — generation 0 holds only the seed —
        # gets the coin-flip prior rather than a zero. Scoring it zero would
        # mean the seed is beaten by anything at all, and the first question
        # this search has to answer is whether evolution beat the seed.
        return {k: (wins[k] / played[k] if played[k] else 0.5) for k in wins}


def read_text(events: Path) -> str:
    """Reassemble the model text tenon streamed as agent.output.delta."""
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
    return "".join(parts).strip() or "(this variant produced no output)"


class Judge:
    def __init__(self):
        self.lock = threading.Lock()
        self.ready = threading.Condition(self.lock)
        self.rounds: dict = {}

    def round_for(self, run_root: Path, generation: int, task_index: int, task: str) -> Round:
        key = (str(run_root), generation, task_index)
        with self.lock:
            if key not in self.rounds:
                rnd = Round(run_root, generation, task_index)
                rnd.task = task
                self.rounds[key] = rnd
            return self.rounds[key]

    def active(self):
        with self.lock:
            for rnd in self.rounds.values():
                if not rnd.done:
                    return rnd
            return None

    def wait_for(self, rnd: Round, entry_key: str) -> float:
        with self.ready:
            while not rnd.done:
                self.ready.wait(timeout=1.0)
            return rnd.scores().get(entry_key, 0.0)

    def verdict(self, pair_id: str, winner: str) -> None:
        with self.ready:
            for rnd in self.rounds.values():
                if any(p["id"] == pair_id for p in rnd.pairs):
                    rnd.verdicts[pair_id] = winner
                    break
            self.ready.notify_all()

    def undo(self) -> None:
        with self.ready:
            for rnd in self.rounds.values():
                if rnd.verdicts:
                    rnd.verdicts.pop(list(rnd.verdicts)[-1], None)
            self.ready.notify_all()


JUDGE = Judge()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def send_json(self, payload, status=200):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/":
            body = PAGE.encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path == "/api/state":
            rnd = JUDGE.active()
            if rnd is None:
                self.send_json({"waiting": True})
                return
            pair = rnd.next_pair()
            by_key = {e["key"]: e for e in rnd.entries}
            self.send_json(
                {
                    "waiting": False,
                    "generation": rnd.generation,
                    "task": rnd.task,
                    "done": len(rnd.verdicts),
                    "total": len(rnd.pairs),
                    "pair": None
                    if pair is None
                    else {
                        "id": pair["id"],
                        # Deliberately unlabelled: the judge should not know
                        # which lineage produced which answer.
                        "a": by_key[pair["a"]]["text"],
                        "b": by_key[pair["b"]]["text"],
                    },
                }
            )
            return
        self.send_json({"error": "not found"}, 404)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        payload = json.loads(self.rfile.read(length) or b"{}")
        if self.path == "/api/verdict":
            JUDGE.verdict(payload.get("id", ""), payload.get("winner", "tie"))
            self.send_json({"ok": True})
            return
        if self.path == "/api/undo":
            JUDGE.undo()
            self.send_json({"ok": True})
            return
        if self.path == "/score":
            try:
                genome_path = Path(payload["genome_path"])
                run_root = genome_path.parent.parent
                generation = int(payload["generation"])
                task_index = int(payload["task_index"])
                entry_key = f"{genome_path.name[:8]}-t{task_index}r0"
                rnd = JUDGE.round_for(run_root, generation, task_index, payload.get("task", ""))
                score = JUDGE.wait_for(rnd, entry_key)
            except Exception as err:  # a judge failure must not look like a zero
                self.send_json({"error": str(err)}, 500)
                return
            self.send_json({"score": score})
            return
        self.send_json({"error": "not found"}, 404)


def main() -> int:
    parser = argparse.ArgumentParser(prog="judge", description="Human pairwise judge for an evolve search.")
    parser.add_argument("--port", type=int, default=8917)
    args = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"judge listening on http://127.0.0.1:{args.port}")
    print("open that page, then start the search in another terminal")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 130
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
