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
  python3 improve/judge/server.py [--port 8917]

Then point a search's score command at improve/judge/score-pairwise.py.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from itertools import combinations
from pathlib import Path

HERE = Path(__file__).parent
PAGE = (HERE / "ui.html").read_text()
ABOUT = (HERE / "about.html").read_text()
ASSETS = HERE / "assets"


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
            self.entries.append(
                {
                    "key": name,
                    "short": name.split("-t")[0],
                    "text": read_text(events),
                    "instructions": "",
                }
            )
        self.entries.sort(key=lambda e: e["key"])
        paths = genome_paths(self.run_root)
        for entry in self.entries:
            entry["instructions"] = read_instructions(paths.get(entry["short"], ""))
        # Verdicts are a person's time. Reload any already given for this
        # generation so a restarted server resumes mid-round instead of
        # throwing the work away.
        saved = self.run_root / "judge" / f"verdicts-gen-{self.generation}.json"
        if saved.is_file():
            self.verdicts = json.loads(saved.read_text())
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

    def tally(self) -> tuple:
        """Wins and pair counts. A tie is half a win to each side."""
        keys = [e["key"] for e in self.entries]
        index = {k: i for i, k in enumerate(keys)}
        n = len(keys)
        wins = [0.0] * n
        counts = [[0] * n for _ in range(n)]
        for pair in self.pairs:
            verdict = self.verdicts.get(pair["id"])
            if verdict is None:
                continue
            a, b = index[pair["a"]], index[pair["b"]]
            counts[a][b] += 1
            counts[b][a] += 1
            if verdict == "a":
                wins[a] += 1
            elif verdict == "b":
                wins[b] += 1
            else:
                wins[a] += 0.5
                wins[b] += 0.5
        return keys, wins, counts

    def strengths(self, iterations: int = 500, tol: float = 1e-10) -> list:
        """Bradley-Terry maximum likelihood: fit a latent strength per entry
        such that P(i beats j) = p_i / (p_i + p_j).

        This is what turns ordinal verdicts into cardinal scores. A raw win
        rate treats every comparison as equally informative, so beating the
        weakest entry counts the same as beating the strongest, and it has no
        answer at all when the comparison graph is incomplete. Fitting
        strengths uses who beat whom.

        Solved by Zermelo's iteration, with one virtual draw against a
        unit-strength phantom opponent so an undefeated or winless entry still
        gets a finite strength instead of running off to zero or infinity."""
        keys, wins, counts = self.tally()
        n = len(keys)
        if n == 0:
            return []
        p = [1.0] * n
        for _ in range(iterations):
            updated = []
            for i in range(n):
                denominator = 1.0 / (p[i] + 1.0)  # the phantom
                for j in range(n):
                    if j != i and counts[i][j]:
                        denominator += counts[i][j] / (p[i] + p[j])
                updated.append((wins[i] + 0.5) / denominator)
            total = sum(updated) or 1.0
            updated = [x * n / total for x in updated]
            delta = max(abs(a - b) for a, b in zip(updated, p))
            p = updated
            if delta < tol:
                break
        return p

    def scores(self) -> dict:
        """Cardinal fitness on [0, 1]: the fitted probability that an entry
        beats a uniformly drawn opponent from its own round. Directly
        comparable to the 0.5 coin-flip prior an unjudged entry receives."""
        keys, _, _ = self.tally()
        if not keys:
            return {}
        if len(keys) == 1:
            # Nothing to compare against — generation 0 holds only the seed.
            # The coin flip is the honest answer; a zero would mean the seed is
            # beaten by anything at all.
            return {keys[0]: 0.5}
        p = self.strengths()
        out = {}
        for i, key in enumerate(keys):
            others = [p[j] for j in range(len(keys)) if j != i]
            out[key] = sum(p[i] / (p[i] + q) for q in others) / len(others)
        return out

    def board(self) -> list:
        """The generation's scoreboard, strongest first."""
        keys, wins, counts = self.tally()
        scores = self.scores()
        p = self.strengths() if len(keys) > 1 else [1.0] * len(keys)
        rows = [
            {
                "entry": key,
                "genome": key.split("-t")[0],
                "score": round(scores.get(key, 0.0), 4),
                "strength": round(p[i], 4),
                "wins": wins[i],
                "played": sum(counts[i]),
            }
            for i, key in enumerate(keys)
        ]
        rows.sort(key=lambda r: r["score"], reverse=True)
        return rows


def genome_paths(run_root: Path) -> dict:
    """Map each genome's short id to where its files live, from the lineage the
    search writes as it goes."""
    out = {}
    path = run_root / "lineage.jsonl"
    if not path.is_file():
        return out
    for line in path.read_text().splitlines():
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        if entry.get("genome") and entry.get("path"):
            out[entry["genome"].split(":")[-1][:8]] = entry["path"]
    return out


def read_instructions(genome_path: str) -> str:
    """The gene the search is actually editing, so a reviewer can see what
    changed rather than only what it produced."""
    if not genome_path:
        return ""
    f = Path(genome_path) / "instructions.md"
    return f.read_text(errors="replace") if f.is_file() else ""


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


def fit(nodes: list, comparisons: list) -> dict:
    """Bradley-Terry over an arbitrary comparison list. `comparisons` is
    (winner, loser, weight) triples; a tie contributes half to each side."""
    index = {k: i for i, k in enumerate(nodes)}
    n = len(nodes)
    if n == 0:
        return {}
    if n == 1:
        # Same shape as every other return: one entry with nothing to compare
        # against takes the coin flip, but callers still index it as a record.
        return {nodes[0]: {"score": 0.5, "strength": 1.0}}
    wins = [0.0] * n
    counts = [[0.0] * n for _ in range(n)]
    for a, b, weight in comparisons:
        i, j = index[a], index[b]
        wins[i] += weight
        counts[i][j] += 1
        counts[j][i] += 1
    p = [1.0] * n
    for _ in range(500):
        updated = []
        for i in range(n):
            denominator = 1.0 / (p[i] + 1.0)
            for j in range(n):
                if j != i and counts[i][j]:
                    denominator += counts[i][j] / (p[i] + p[j])
            updated.append((wins[i] + 0.5) / denominator)
        total = sum(updated) or 1.0
        updated = [x * n / total for x in updated]
        delta = max(abs(a - b) for a, b in zip(updated, p))
        p = updated
        if delta < 1e-10:
            break
    out = {}
    for i, key in enumerate(nodes):
        others = [p[j] for j in range(n) if j != i]
        out[key] = {
            "score": sum(p[i] / (p[i] + q) for q in others) / len(others),
            "strength": p[i],
        }
    return out


class Judge:
    def __init__(self):
        self.lock = threading.Lock()
        self.ready = threading.Condition(self.lock)
        self.rounds: dict = {}
        # Set only when the server was started with --spec. Without it the
        # server is read-only and cannot start anything.
        self.spec: Path | None = None
        self.evolve: Path | None = None
        self.child = None
        self.child_log: Path | None = None

    def can_advance(self) -> bool:
        return self.spec is not None

    def running(self) -> bool:
        return self.child is not None and self.child.poll() is None

    def advance(self, run_root: Path) -> dict:
        """Ask the search for one more generation.

        The command is fixed at startup and the request carries no arguments,
        so a page cannot ask this server to run something of its choosing."""
        if not self.can_advance():
            return {"error": "this judge was started without --spec, so it cannot run the search"}
        if self.running():
            return {"error": "a generation is already running"}
        checkpoint = run_root / "checkpoint.json"
        if not checkpoint.is_file():
            return {"error": "the run has no checkpoint to resume from"}
        nxt = int(json.loads(checkpoint.read_text())["generation"]) + 1
        self.child_log = run_root / f"generation-{nxt}.log"
        handle = self.child_log.open("ab")
        self.child = subprocess.Popen(
            [sys.executable, str(self.evolve), "run", "--spec", str(self.spec),
             "--resume", "--generations", str(nxt)],
            cwd=str(self.evolve.parent.parent),
            stdout=handle,
            stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL,
        )
        return {"started": nxt}

    def child_state(self) -> dict:
        if self.child is None:
            return {"running": False, "exited": None, "tail": ""}
        code = self.child.poll()
        tail = ""
        if self.child_log and self.child_log.is_file():
            tail = "\n".join(self.child_log.read_text(errors="replace").splitlines()[-4:])
        return {"running": code is None, "exited": code, "tail": tail}

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
            if entry_key not in {e["key"] for e in rnd.entries}:
                raise KeyError(f"{entry_key} is not in generation {rnd.generation}'s round")
            genome = entry_key.split("-t")[0]
            overall = self.global_fit()
            if genome in overall:
                return overall[genome]["score"]
            return rnd.scores().get(entry_key, 0.5)

    def verdict(self, pair_id: str, winner: str) -> None:
        with self.ready:
            for rnd in self.rounds.values():
                if any(p["id"] == pair_id for p in rnd.pairs):
                    rnd.verdicts[pair_id] = winner
                    self.save_verdicts(rnd)
                    if rnd.done:
                        self.persist(rnd)
                    break
            self.ready.notify_all()

    def save_verdicts(self, rnd: Round) -> None:
        out = rnd.run_root / "judge"
        out.mkdir(exist_ok=True)
        (out / f"verdicts-gen-{rnd.generation}.json").write_text(json.dumps(rnd.verdicts, indent=2) + "\n")

    def persist(self, rnd: Round) -> None:
        """Write the generation's scoreboard beside the search's own state, so
        the judging survives the server and can be read after the fact."""
        out = rnd.run_root / "judge"
        out.mkdir(exist_ok=True)
        (out / f"gen-{rnd.generation}.json").write_text(
            json.dumps(
                {
                    "generation": rnd.generation,
                    "task": rnd.task,
                    "comparisons": len(rnd.pairs),
                    "board": rnd.board(),
                },
                indent=2,
            )
            + "\n"
        )

    def global_fit(self) -> dict:
        """One fit over every comparison ever made, keyed by genome rather than
        by round entry.

        Per-round scores are normalised inside their own field, so they are not
        comparable across generations: a genome that went 5/5 against weak
        siblings and 1/5 against strong ones has not changed, its opposition
        has. The incumbent appears in consecutive rounds, and that shared node
        is exactly what makes a single fit across all of them identifiable —
        which is the point of anchoring in the first place."""
        nodes, comparisons = [], []
        seen = set()
        for rnd in self.rounds.values():
            by_key = {e["key"]: e["key"].split("-t")[0] for e in rnd.entries}
            for key, genome in by_key.items():
                if genome not in seen:
                    seen.add(genome)
                    nodes.append(genome)
            for pair in rnd.pairs:
                verdict = rnd.verdicts.get(pair["id"])
                if verdict is None:
                    continue
                a, b = by_key[pair["a"]], by_key[pair["b"]]
                if verdict == "a":
                    comparisons.append((a, b, 1.0))
                elif verdict == "b":
                    comparisons.append((b, a, 1.0))
                else:
                    comparisons.append((a, b, 0.5))
                    comparisons.append((b, a, 0.5))
        return fit(nodes, comparisons)

    def summary(self) -> dict:
        """Everything the review screen shows: each generation's board and
        outputs, the lineage the search recorded, and the one global fit."""
        with self.lock:
            rounds = sorted(self.rounds.values(), key=lambda r: r.generation)
        if not rounds:
            return {"run": "", "finished": False, "pending": None, "generations": [],
                    "overall": [], "can_advance": self.can_advance(), "child": self.child_state()}
        root = rounds[0].run_root
        lineage = {}
        path = root / "lineage.jsonl"
        if path.is_file():
            for line in path.read_text().splitlines():
                entry = json.loads(line)
                if entry.get("genome"):
                    short = entry["genome"].split(":")[-1][:8]
                    lineage[short] = {
                        "parents": [p.split(":")[-1][:8] for p in entry.get("parents", [])],
                        "operator": entry.get("operator", ""),
                        "path": entry.get("path", ""),
                        "generation": entry.get("generation"),
                    }
        overall = self.global_fit()
        pending = next((r for r in rounds if not r.done), None)
        return {
            "run": root.name,
            "finished": (root / "best.json").is_file(),
            "can_advance": self.can_advance(),
            "child": self.child_state(),
            "pending": None if pending is None else {
                "generation": pending.generation,
                "done": len(pending.verdicts),
                "total": len(pending.pairs),
            },
            "overall": [
                {"genome": g, "score": v["score"], "strength": v["strength"],
                 "generations": sorted({r.generation for r in rounds
                                        if any(e["key"].startswith(g) for e in r.entries)})}
                for g, v in sorted(overall.items(), key=lambda kv: -kv[1]["score"])
            ],
            # A round with no pairs — generation 0 holds only the seed — has
            # nothing to show, and a tab leading to nothing is noise.
            "generations": [
                {
                    "generation": r.generation,
                    "task": r.task,
                    "done": r.done,
                    "judged": len(r.verdicts),
                    "comparisons": len(r.pairs),
                    "board": r.board(),
                    "entries": [
                        {
                            "genome": e["key"].split("-t")[0],
                            "text": e["text"],
                            "instructions": e.get("instructions", ""),
                            "operator": lineage.get(e["key"].split("-t")[0], {}).get("operator", ""),
                            "parents": lineage.get(e["key"].split("-t")[0], {}).get("parents", []),
                        }
                        for e in r.entries
                    ],
                }
                for r in rounds
                if r.pairs
            ],
        }

    def boards(self) -> list:
        with self.lock:
            return [
                {"generation": r.generation, "done": r.done, "board": r.board()}
                for r in sorted(self.rounds.values(), key=lambda r: r.generation)
                if r.verdicts or r.done
            ]

    def pending(self):
        """The round currently being judged, if any."""
        for rnd in sorted(self.rounds.values(), key=lambda r: r.generation):
            if not rnd.done:
                return rnd
        return None

    def undo(self) -> dict:
        """Take back the last verdict in the round being judged.

        Scoped to that one round on purpose: this used to walk every round and
        pop a verdict from each, so a single undo silently damaged generations
        that were already finished."""
        with self.ready:
            rnd = self.pending()
            if rnd is None or not rnd.verdicts:
                return {"undone": False}
            pair = list(rnd.verdicts)[-1]
            rnd.verdicts.pop(pair, None)
            self.save_verdicts(rnd)
            self.ready.notify_all()
            return {"undone": True, "generation": rnd.generation, "left": len(rnd.verdicts)}

    def reset(self, generation: int) -> dict:
        """Throw away a generation's verdicts and judge it again from scratch."""
        with self.ready:
            match = [r for r in self.rounds.values() if r.generation == generation]
            if not match:
                return {"error": f"generation {generation} is not loaded"}
            rnd = match[0]
            rnd.verdicts = {}
            self.save_verdicts(rnd)
            board = rnd.run_root / "judge" / f"gen-{generation}.json"
            board.unlink(missing_ok=True)
            self.ready.notify_all()
            return {"reset": generation, "comparisons": len(rnd.pairs)}


JUDGE = Judge()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def send_html(self, body: bytes) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        # The page and its copy change while the tool is being worked on, and a
        # stale cached copy looks exactly like a bug that has not been fixed.
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def send_json(self, payload, status=200):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        # Route on the path alone: a query string (?theme=dark) is for the page,
        # not part of what is being addressed.
        path = self.path.split("?", 1)[0]
        if path == "/":
            self.send_html(PAGE.encode())
            return
        if path == "/api/about":
            self.send_html(ABOUT.encode())
            return
        if path.startswith("/assets/"):
            # Only ever serve a plain filename out of the assets directory:
            # a path from a request must not be able to walk out of it.
            name = Path(path[len("/assets/"):]).name
            asset = ASSETS / name
            if not asset.is_file() or asset.parent != ASSETS:
                self.send_json({"error": "not found"}, 404)
                return
            body = asset.read_bytes()
            self.send_response(200)
            self.send_header("Content-Type", "image/png")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if path == "/api/summary":
            self.send_json(JUDGE.summary())
            return
        if path == "/api/scores":
            self.send_json({"generations": JUDGE.boards()})
            return
        if path == "/api/state":
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
                        # The gene behind each answer, for a reader who wants
                        # to know why they differ. The UI keeps it hidden until
                        # asked, so the default comparison stays on the output.
                        "a_gene": by_key[pair["a"]]["instructions"],
                        "b_gene": by_key[pair["b"]]["instructions"],
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
        if self.path == "/api/advance":
            rounds = sorted(JUDGE.rounds.values(), key=lambda r: r.generation)
            if not rounds:
                self.send_json({"error": "no run is loaded"}, 400)
                return
            self.send_json(JUDGE.advance(rounds[0].run_root))
            return
        if self.path == "/api/undo":
            self.send_json(JUDGE.undo())
            return
        if self.path == "/api/reset":
            generation = payload.get("generation")
            if not isinstance(generation, int):
                self.send_json({"error": "a generation number is required"}, 400)
                return
            self.send_json(JUDGE.reset(generation))
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
    parser.add_argument(
        "--spec",
        help="an evolve spec; supplying it lets the page ask for one more generation",
    )
    args = parser.parse_args()
    if args.spec:
        JUDGE.spec = Path(args.spec).expanduser().resolve()
        JUDGE.evolve = HERE.parent / "evolve.py"
        if not JUDGE.spec.is_file():
            print(f"judge: no spec at {JUDGE.spec}")
            return 2
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"judge listening on http://127.0.0.1:{args.port}")
    if JUDGE.spec:
        print(f"judge: can run further generations from {JUDGE.spec}")
    print("open that page, then start the search in another terminal")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 130
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
