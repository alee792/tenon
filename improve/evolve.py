#!/usr/bin/env python3
"""evolve — hill-climbing and genetic search over tenon agent projects.

fanout runs one round. evolve is the loop around it: propose candidate
agent projects, gate them, evaluate them, select, repeat.

The design rests on two properties tenon already has, and adds nothing to
its contract:

  * An agent project is a folder of authored files, so a **genome is a
    directory** and a **gene is one authored component** — `instructions.md`,
    a `skills/<name>/` directory, a `tools/<file>`, a `subagents/<name>.md`.
    Crossover is therefore file-level recombination, not text surgery.

  * The adapter's `gate` is one call that both proves a candidate
    and mints its identity: stable diagnostic identifiers and the digest of
    the bytes that failed on rejection, the source fingerprint on success. So
    the fingerprint is the genome id — content-addressed, gate-proven, and
    free to dedupe against, which means a genome already evaluated is never
    paid for twice, and a rejection names what was rejected.

Tenon mints those units; this loop composes the chain. The lineage lives in
`lineage.jsonl` here, never in tenon.

What evolve does NOT do: define fitness, invent mutations, or promote a
winner. Fitness is your `score` command, mutation is your `mutate` command,
and promotion is a human act — tenon's product spec makes automatic or
unreviewed promotion of an agent-authored improvement an explicit non-goal,
and this tool holds that line. `evolve best` prints a branch to review.

Usage:
  evolve run     --spec FILE [--dry-run]   run the search
  evolve lineage RUN [--json]              every genome ever evaluated
  evolve best    RUN                       the incumbent, and how to review it
"""

from __future__ import annotations

import argparse
import json
import os
import random
import shutil
import statistics
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path

# Every tenon call goes through the adapter, which is the only module in
# improve/ that names a tenon subcommand or flag; improve/test_tenon.py greps
# this file to keep it that way.
sys.path.insert(0, str(Path(__file__).resolve().parent))
import tenon as tenon_api  # noqa: E402
from tenon import TenonEnvironment  # noqa: E402

SCHEMA_VERSION = 1

# The authored surface of a tenon agent project. instructions.md is one gene;
# each child of these directories is one gene. Anything else in the folder is
# carried along with its parent and never recombined on its own.
#
# This is a MIRROR of what tenon's loader inventories, and a mirror drifts:
# the day tenon adds a component directory, a search that does not know about
# it silently stops recombining that component and carries it along with the
# first parent instead. So it is spec config (`spec.genes.dirs` /
# `spec.genes.files`) rather than a constant — a run can track a newer tenon
# without waiting for this file — and EVOLVE.md says it must be kept level
# with the agent-project layout.
#
# The default is everything tenon's loader inventories today, `harnesses/`
# included: a per-harness override is authored surface like any other
# component, and a search that cannot vary it cannot find a harness-specific
# fix. Drop it from `spec.genes.dirs` to hold harness config fixed.
DEFAULT_GENE_DIRS = ("skills", "tools", "subagents", "plugins", "mcp", "schedules", "harnesses")  # not tenon argv: tools, mcp
DEFAULT_GENE_FILES = ("instructions.md",)


@dataclass(frozen=True)
class GeneLayout:
    """Which paths in an agent project are recombinable units."""

    dirs: tuple = DEFAULT_GENE_DIRS
    files: tuple = DEFAULT_GENE_FILES

    @staticmethod
    def of(raw) -> "GeneLayout":
        raw = raw or {}
        if not isinstance(raw, dict):
            raise EvolveError("spec: genes must be an object with dirs and files")
        return GeneLayout(
            dirs=_gene_names(raw, "dirs", DEFAULT_GENE_DIRS),
            files=_gene_names(raw, "files", DEFAULT_GENE_FILES),
        )


def _gene_names(raw: dict, key: str, default: tuple) -> tuple:
    """The loci named under `genes.<key>`, validated.

    A bare string here is the dangerous shape: `"skills"` iterates into
    `("s", "k", ...)` and the search silently recombines seven loci that do
    not exist instead of the one that does. So the type is checked rather
    than coerced, and the message names the key that is wrong."""
    if key not in raw:
        return tuple(default)
    value = raw[key]
    if not isinstance(value, (list, tuple)):
        raise EvolveError(f"spec: genes.{key} must be a list of names, not {type(value).__name__}")
    for name in value:
        if not isinstance(name, str) or not name.strip():
            raise EvolveError(f"spec: genes.{key} must be a list of non-empty names")
        # A locus is one component name under the agent root. Anything with a
        # separator or a dot-segment would make genes() read, and assemble()
        # write, outside the directories they are handed.
        if "/" in name or "\\" in name or name in (".", "..") or name.startswith("."):
            raise EvolveError(f"spec: genes.{key} entries must be single component names, not {name!r}")
    return tuple(value)


class EvolveError(Exception):
    """A failure that should print as one line, not a traceback."""


# TenonEnvironment is the adapter's, and fanout and evolve share it: one
# exception type for "the environment failed, not the candidate" — an
# unreadable pin set, an unwritable path, a harness that would not start. It
# must never be recorded as a rejection, so it propagates out of admit rather
# than returning None, and main() gives it its own exit code.


def shown(value) -> str:
    """Format a score for a human. An unscored genome prints as a dash.

    None is not zero here: it means every variant of that genome ended in an
    environment failure, so nothing was learned about it. Printing 0.0 would
    read as evidence that it is terrible."""
    return f"{value:.4f}" if value is not None else "-"


# --------------------------------------------------------------------------
# genome
# --------------------------------------------------------------------------


@dataclass
class Genome:
    gid: str
    path: Path
    round_no: int
    parents: list = field(default_factory=list)
    mutator: str = "seed"
    score: float | None = None
    scores: list = field(default_factory=list)
    stdev: float | None = None
    report: str = ""

    @property
    def short(self) -> str:
        return self.gid.split(":")[-1][:8]


@dataclass
class Member:
    """One slot in the population: a genome plus whatever a policy tagged it
    with.

    Tags live on the slot rather than on the genome because a genome is
    content, addressed by its fingerprint and immutable — the same content can
    occupy two islands or two behavioural niches at once. Its score belongs to
    the content; its niche belongs to the slot. Evolve never interprets a tag,
    it only carries and inherits it, which is what lets a policy implement
    island models, MAP-Elites niches, or age layers without evolve learning
    what any of those are."""

    genome: Genome
    tags: dict = field(default_factory=dict)


def genes(root: Path, layout: GeneLayout = GeneLayout()) -> dict:
    """Map each locus to the path of the gene at it. A locus is a component
    path — `instructions.md`, `skills/alpha` — and the gene is the content
    there; a genome is that map."""
    found: dict = {}
    for name in layout.files:
        if (root / name).is_file():
            found[name] = root / name
    for directory in layout.dirs:
        parent = root / directory
        if not parent.is_dir():
            continue
        for child in sorted(parent.iterdir()):
            found[f"{directory}/{child.name}"] = child
    return found


def copy_gene(source: Path, target: Path) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    if source.is_dir():
        shutil.copytree(source, target, symlinks=True, dirs_exist_ok=True)
    else:
        shutil.copy2(source, target)


def materialize(source: Path, target: Path) -> None:
    if target.exists():
        shutil.rmtree(target)
    shutil.copytree(source, target, symlinks=True)


def recombine(parents: list, target: Path, rng: random.Random, layout: GeneLayout = GeneLayout()) -> None:
    """Uniform crossover over the gene set, for any number of parents. Each
    locus is drawn independently from the parents that carry it. The offspring
    may well be incoherent — a skill referencing a tool that did not come
    along — which is exactly what the tenon gate is for."""
    pools = [genes(p, layout) for p in parents]
    loci = sorted({name for pool in pools for name in pool})
    plan = {}
    for name in loci:
        holders = [i for i, pool in enumerate(pools) if name in pool]
        plan[name] = holders[rng.randrange(len(holders))]
    assemble(parents, plan, target, layout)


def assemble(parents: list, plan: dict, target: Path, layout: GeneLayout = GeneLayout()) -> None:
    """Materialize an offspring from a per-locus plan of {locus: parent index}.
    This is the mechanism every combine policy shares: a policy decides which
    parent supplies the gene at each locus, and this puts the files where they
    belong."""
    pools = [genes(p, layout) for p in parents]
    target.mkdir(parents=True, exist_ok=True)
    for name, index in plan.items():
        if index >= len(pools) or name not in pools[index]:
            raise EvolveError(f"combine plan names {name!r} from a parent that does not carry it")
        copy_gene(pools[index][name], target / name)
    # Carry non-gene files (README, lockfiles, go.mod) from the first parent so
    # the offspring is a runnable directory, not just a bag of genes.
    for entry in sorted(parents[0].iterdir()):
        if entry.name in layout.files or entry.name in layout.dirs:
            continue
        if not (target / entry.name).exists():
            copy_gene(entry, target / entry.name)


def write_json(path: Path, payload: dict) -> None:
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
    tmp.replace(path)


def tuple_of(value):
    """random.getstate round-trips through JSON as nested lists."""
    return tuple(tuple_of(v) if isinstance(v, list) else v for v in value)


def hook(command: str, payload: dict, what: str) -> dict:
    """Run a policy hook: one JSON object in on stdin, one JSON object out on
    stdout. Policy lives in these commands; evolve keeps the mechanism."""
    proc = subprocess.run(
        ["/bin/sh", "-c", command],
        input=json.dumps(payload),
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise EvolveError(f"the {what} hook exited {proc.returncode}: {proc.stderr.strip()[:200]}")
    for line in reversed(proc.stdout.strip().splitlines()):
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict):
            return obj
    raise EvolveError(f"the {what} hook printed no JSON object")


def genome_view(m: Member, index: int = -1, layout: GeneLayout = GeneLayout()) -> dict:
    """What a policy hook is told about one population slot. Loci are included
    so a combine policy can plan without walking the filesystem, and `index` is
    the stable way to name a slot when two slots hold the same genome."""
    g = m.genome
    return {
        "index": index,
        "tags": dict(m.tags),
        "genome": g.gid,
        "short": g.short,
        "score": g.score,
        "scores": g.scores,
        "stdev": g.stdev,
        "round": g.round_no,
        "mutator": g.mutator,
        "parents": g.parents,
        "path": str(g.path),
        "genes": sorted(genes(g.path, layout)),
    }


# --------------------------------------------------------------------------
# config
# --------------------------------------------------------------------------


@dataclass
class Config:
    run: str
    strategy: str
    repo: Path
    agent: str
    seed: Path
    harness: str
    tenon: str
    fanout: str
    state_dir: Path
    genes: GeneLayout
    tasks: list
    repeats: int
    score: str
    mutators: list
    select_policy: str
    pair_policy: str
    combine_policy: str
    rounds: int
    population: int
    offspring: int
    crossover_rate: float
    mutation_rate: float
    tournament: int
    patience: int
    target: float | None
    max_variants: int
    concurrency: int
    timeout: str
    turn_timeout: str
    rng_seed: int
    keep_worktrees: bool
    reevaluate: str
    model: str

    def to_json(self) -> dict:
        payload = {k: v for k, v in self.__dict__.items()}
        for key in ("repo", "seed", "state_dir"):
            payload[key] = str(payload[key])
        # The gene layout round-trips as the same object shape the spec uses,
        # so a recorded search says which loci it recombined.
        payload["genes"] = {"dirs": list(self.genes.dirs), "files": list(self.genes.files)}
        payload["schema_version"] = SCHEMA_VERSION
        return payload


def load_mutators(raw: dict) -> list:
    """Variation mutators, weighted. `mutate` is sugar for a single one.

    Naming each mutator matters beyond bookkeeping: the mutator that made a
    genome lands in its lineage entry, so the record answers "which kind of
    change actually produced the gains" without extra instrumentation."""
    mutators = raw.get("mutators")
    if not mutators:
        if not raw.get("mutate"):
            raise EvolveError("spec: one of mutate or mutators is required")
        mutators = [{"name": "mutate", "weight": 1, "command": raw["mutate"]}]
    if not isinstance(mutators, list) or not mutators:
        raise EvolveError("mutators must be a non-empty list")
    out = []
    for entry in mutators:
        if not isinstance(entry, dict) or not entry.get("command"):
            raise EvolveError("each mutator needs a command")
        weight = float(entry.get("weight", 1))
        if weight <= 0:
            raise EvolveError("a mutator weight must be greater than zero")
        out.append({"name": entry.get("name") or "mutator", "weight": weight, "command": entry["command"]})
    return out


def load_config(path: Path) -> Config:
    raw = json.loads(path.read_text())
    if not isinstance(raw, dict):
        raise EvolveError("a spec file must be a JSON object")

    def need(key):
        if not raw.get(key):
            raise EvolveError(f"spec: {key} is required")
        return raw[key]

    strategy = raw.get("strategy", "hill-climb")
    if strategy not in ("hill-climb", "genetic"):
        raise EvolveError("strategy must be hill-climb or genetic")

    repo = Path(raw.get("repo") or ".").expanduser().resolve()
    agent = raw.get("agent") or "agent"
    if Path(agent).is_absolute():
        raise EvolveError("agent must be a path relative to the repo, so genomes land in each worktree")
    seed = Path(raw["seed"]).expanduser().resolve() if raw.get("seed") else (repo / agent).resolve()
    if not seed.is_dir():
        raise EvolveError(f"seed genome {seed} does not exist")

    tenon = shutil.which(raw.get("tenon") or os.environ.get("FANOUT_TENON") or "tenon") or raw.get("tenon", "")
    if not tenon or not Path(tenon).is_file():
        raise EvolveError("no tenon binary; set spec.tenon or FANOUT_TENON")

    fanout = raw.get("fanout") or str(Path(__file__).resolve().parent / "fanout.py")
    if not Path(fanout).is_file():
        raise EvolveError(f"fanout script {fanout} not found")

    tasks = need("tasks")
    if not isinstance(tasks, list) or not all(isinstance(t, str) and t.strip() for t in tasks):
        raise EvolveError("tasks must be a non-empty list of prompt strings")

    state_dir = Path(raw["state_dir"]).expanduser().resolve() if raw.get("state_dir") else Path.home() / ".evolve"
    reevaluate = raw.get("reevaluate", "incumbent")
    if reevaluate not in ("incumbent", "population", "none"):
        raise EvolveError("reevaluate must be incumbent, population, or none")

    # (mu + lambda): `population` is mu, the survivors carried forward, and
    # `offspring` is lambda, how many candidates each round proposes.
    # They were one knob, which made "keep the best two, breed five from them"
    # inexpressible.
    if strategy == "hill-climb":
        # A hill climb has one incumbent by definition, so mu is pinned and the
        # older meaning of `population` — how many neighbours to try — carries
        # over as lambda.
        population = 1
        offspring = int(raw.get("offspring", raw.get("population", 4)))
    else:
        population = int(raw.get("population", 4))
        offspring = int(raw.get("offspring", population))
    if population < 1 or offspring < 1:
        raise EvolveError("population and offspring must both be at least 1")

    return Config(
        run=raw.get("run") or f"search-{time.strftime('%Y%m%d-%H%M%S')}",
        strategy=strategy,
        repo=repo,
        agent=agent,
        seed=seed,
        harness=raw.get("harness", "claude"),
        tenon=tenon,
        fanout=fanout,
        genes=GeneLayout.of(raw.get("genes")),
        state_dir=state_dir,
        tasks=tasks,
        repeats=int(raw.get("repeats", 1)),
        score=need("score"),
        mutators=load_mutators(raw),
        select_policy=raw.get("select", "elitist"),
        pair_policy=raw.get("pair", "tournament"),
        combine_policy=raw.get("combine", "uniform"),
        rounds=int(raw.get("rounds", 5)),
        population=population,
        offspring=offspring,
        crossover_rate=float(raw.get("crossover_rate", 0.5)),
        mutation_rate=float(raw.get("mutation_rate", 1.0)),
        tournament=int(raw.get("tournament", 2)),
        patience=int(raw.get("patience", 0)),
        target=float(raw["target"]) if raw.get("target") is not None else None,
        max_variants=int(raw.get("max_variants", 0)),
        concurrency=int(raw.get("concurrency", 4)),
        timeout=str(raw.get("timeout", "900s")),
        turn_timeout=str(raw.get("turn_timeout", "")),
        rng_seed=int(raw.get("rng_seed", 1)),
        keep_worktrees=bool(raw.get("keep_worktrees", False)),
        reevaluate=reevaluate,
        model=str(raw.get("model", "")),
    )


# --------------------------------------------------------------------------
# the search
# --------------------------------------------------------------------------


class Search:
    def __init__(self, cfg: Config, dry_run: bool, resume: bool = False):
        self.cfg = cfg
        self.dry_run = dry_run
        self.resume = resume
        self.root = cfg.state_dir / cfg.run
        self.genomes_dir = self.root / "genomes"
        self.lineage_path = self.root / "lineage.jsonl"
        self.rng = random.Random(cfg.rng_seed)
        # Every tenon call this search makes goes through here, bound once to
        # the binary and the harness the spec chose.
        self.tenon = tenon_api.Tenon(cfg.tenon, cfg.harness)
        self.known: dict = {}
        # Tags a score hook observed at runtime, keyed by genome; merged onto
        # each slot so a behavioural descriptor can come from the evaluation
        # rather than from the pairing policy.
        self.observed: dict = {}
        self.variants_spent = 0

    # -- lineage -----------------------------------------------------------

    def record(self, payload: dict) -> None:
        with self.lineage_path.open("a") as handle:
            handle.write(json.dumps(payload, sort_keys=True) + "\n")

    def log(self, message: str) -> None:
        print(message, file=sys.stderr, flush=True)

    # -- candidate construction -------------------------------------------

    def admit(self, path: Path, round_no: int, parents: list, mutator: str):
        """Gate a materialized candidate and give it its identity. Returns the
        genome — the already-known one when the content is a duplicate, so it
        can fill a second slot — or None when tenon rejected it.

        None means *rejected*, and the caller counts it against the mutator.
        An environment failure is not that, so TenonEnvironment
        propagates: the candidate is neither admitted nor scored against."""
        verdict = self.tenon.gate(path)
        if not verdict.ok:
            rejected = list(verdict.rejected)
            self.record(
                {
                    "round": round_no,
                    "status": "rejected",
                    "mutator": mutator,
                    "parents": parents,
                    "diagnostics": rejected,
                    # The digest names the bytes that failed, and it is
                    # recorded before the directory is deleted two lines
                    # below — so a rejection is attributable afterwards. It
                    # lives in its own key and never in `genome`: a digest
                    # names bytes, a fingerprint names a proven
                    # configuration, and the two must not share a namespace.
                    "source_digest": verdict.source_digest,
                }
            )
            digest = verdict.source_digest.split(":")[-1][:8] or "unknown"
            self.log(f"  rejected {digest} ({mutator}): {', '.join(rejected[:3]) or 'no diagnostics'}")
            shutil.rmtree(path, ignore_errors=True)
            return None
        fingerprint = verdict.fingerprint
        if not fingerprint:
            # `home` is built by slicing the fingerprint, so an empty one
            # collapses it onto genomes_dir — and the next line deletes that
            # directory, taking every genome in the run with it. An ok
            # verdict that names nothing is a broken tenon, not a candidate.
            raise TenonEnvironment("the gate reported ok without a fingerprint")
        if verdict.warnings:
            self.log(f"  warnings ({mutator}): {', '.join(verdict.warned[:3])}")
        if fingerprint in self.known:
            self.record(
                {
                    "round": round_no,
                    "status": "duplicate",
                    "genome": fingerprint,
                    "mutator": mutator,
                    "parents": parents,
                    "score": self.known[fingerprint].score,
                }
            )
            self.log(f"  duplicate {fingerprint.split(':')[-1][:8]} ({mutator}) — reusing its score")
            shutil.rmtree(path, ignore_errors=True)
            # Return the known genome rather than dropping it: the content is
            # already scored, but it may legitimately fill a new slot — an
            # island reset seeds one island from another's best.
            return self.known[fingerprint]
        home = self.genomes_dir / fingerprint.split(":")[-1][:16]
        if home.exists():
            shutil.rmtree(home)
        path.rename(home)
        genome = Genome(gid=fingerprint, path=home, round_no=round_no, parents=parents, mutator=mutator)
        self.known[fingerprint] = genome
        return genome

    def vary(self, path: Path, parents: list, round_no: int) -> str:
        """Apply one weighted variation mutator in the candidate directory.

        The mutator is handed a report on the parents it came from — their
        scores and every variant they ran, failures included. A blind mutation
        wastes a variant; at this budget a mutator should be able to see
        why its parent scored what it did."""
        mutator = self.rng.choices(self.cfg.mutators, [m["weight"] for m in self.cfg.mutators])[0]
        report = self.root / "scratch" / f"{path.name}-parents.json"
        report.write_text(
            json.dumps(
                {
                    "round": round_no,
                    "mutator": mutator["name"],
                    "parents": [self.parent_report(p) for p in parents],
                },
                indent=2,
            )
        )
        proc = subprocess.run(
            ["/bin/sh", "-c", mutator["command"]],
            cwd=str(path),
            capture_output=True,
            text=True,
            env={
                **os.environ,
                "EVOLVE_GENOME_DIR": str(path),
                "EVOLVE_RUN": self.cfg.run,
                "EVOLVE_MUTATOR": mutator["name"],
                "EVOLVE_PARENT_REPORT": str(report),
                "EVOLVE_GENES": ",".join(sorted(genes(path, self.cfg.genes))),
                # A mutator's working directory is the genome it edits, so a
                # relative command path in the spec would resolve against the
                # genome rather than the project. This is the anchor to use.
                "EVOLVE_CWD": os.getcwd(),
            },
        )
        if proc.returncode != 0:
            raise EvolveError(f"mutator {mutator['name']} exited {proc.returncode}: {proc.stderr.strip()[:200]}")
        return mutator["name"]

    def parent_report(self, member: Member) -> dict:
        view = genome_view(member, layout=self.cfg.genes)
        if member.genome.report and Path(member.genome.report).is_file():
            view["variants"] = json.loads(Path(member.genome.report).read_text())
        return view

    def propose(self, round_no: int, population: list) -> list:
        """Three policies, in order: which slots pair up, which genes the child
        takes from which parent, and how the child is then varied. Each is a
        named built-in or a command; evolve only supplies the mechanism."""
        scratch = self.root / "scratch"
        scratch.mkdir(parents=True, exist_ok=True)
        candidates: list = []
        for i, (parents, tags) in enumerate(self.choose_pairs(round_no, population)):
            work = scratch / f"r{round_no}-c{i}"
            if work.exists():
                shutil.rmtree(work)
            if len(parents) > 1:
                self.combine(round_no, parents, work)
                mutator = "crossover"
            else:
                materialize(parents[0].genome.path, work)
                mutator = "copy"
            if mutator == "copy" or self.rng.random() < self.cfg.mutation_rate:
                try:
                    applied = self.vary(work, parents, round_no)
                except EvolveError as err:
                    self.log(f"  {err}")
                    shutil.rmtree(work, ignore_errors=True)
                    continue
                mutator = applied if mutator == "copy" else f"crossover+{applied}"
            genome = self.admit(work, round_no, [p.genome.gid for p in parents], mutator)
            if genome:
                # A child inherits its first parent's tags unless the pairing
                # policy said otherwise, so island membership descends by
                # default and evolve still never reads what a tag means.
                candidates.append(Member(genome, {**parents[0].tags, **self.observed.get(genome.gid, {}), **tags}))
        return candidates

    def choose_pairs(self, round_no: int, population: list) -> list:
        """Parent selection. Returns one entry per offspring: the parent slots
        and any tags to put on the child. A single parent reproduces asexually,
        two or more recombine, so one policy expresses both who breeds and how
        often crossover happens."""
        count = self.cfg.offspring
        if self.cfg.pair_policy != "tournament":
            out = hook(
                self.cfg.pair_policy,
                {
                    "round": round_no,
                    "count": count,
                    "strategy": self.cfg.strategy,
                    "population": [genome_view(m, i, self.cfg.genes) for i, m in enumerate(population)],
                },
                "pair",
            )
            return self.resolve_pairs(population, out.get("pairs"))
        pairs = []
        for _ in range(count):
            if self.cfg.strategy == "hill-climb":
                pairs.append(([population[0]], {}))
            elif len(population) > 1 and self.rng.random() < self.cfg.crossover_rate:
                pairs.append(([self.tournament(population), self.tournament(population)], {}))
            else:
                pairs.append(([self.tournament(population)], {}))
        return pairs

    def resolve_pairs(self, population: list, raw) -> list:
        """A pair entry is a list of parent references, or an object carrying
        `parents` and `tags`. A reference is a slot index — unambiguous when
        two slots hold the same genome — or a genome id for the common case."""
        if not isinstance(raw, list) or not raw:
            raise EvolveError("the pair hook must return a non-empty pairs list")
        resolved = []
        for entry in raw:
            tags = {}
            refs = entry
            if isinstance(entry, dict):
                refs, tags = entry.get("parents"), entry.get("tags") or {}
            if not isinstance(refs, list) or not refs:
                raise EvolveError("each pair needs a non-empty parents list")
            parents = []
            for ref in refs:
                if isinstance(ref, bool) or not isinstance(ref, (int, str)):
                    raise EvolveError("a parent reference must be a slot index or a genome id")
                if isinstance(ref, int):
                    if not 0 <= ref < len(population):
                        raise EvolveError(f"the pair hook named slot {ref}, which is out of range")
                    parents.append(population[ref])
                else:
                    match = [m for m in population if m.genome.gid == ref]
                    if not match:
                        raise EvolveError(f"the pair hook named an unknown genome {ref!r}")
                    parents.append(match[0])
            resolved.append((parents, tags))
        return resolved

    def combine(self, round_no: int, parents: list, work: Path) -> None:
        """Crossover policy. The built-in draws each locus uniformly. A hook
        either returns a plan — which parent supplies the gene at each locus,
        and evolve assembles it — or materializes the directory itself when it
        wants a grain finer than whole components."""
        if self.cfg.combine_policy == "uniform":
            recombine([p.genome.path for p in parents], work, self.rng, self.cfg.genes)
            return
        out = hook(
            self.cfg.combine_policy,
            {
                "round": round_no,
                "out_dir": str(work),
                "parents": [genome_view(p, i, self.cfg.genes) for i, p in enumerate(parents)],
            },
            "combine",
        )
        if out.get("materialized"):
            if not work.is_dir():
                raise EvolveError("the combine hook reported materialized but wrote no directory")
            return
        plan = out.get("genes")
        if not isinstance(plan, dict) or not plan:
            raise EvolveError("the combine hook must return a genes plan or materialized: true")
        index = {p.genome.gid: i for i, p in enumerate(parents)}
        resolved = {}
        for name, gid in plan.items():
            if gid not in index:
                raise EvolveError(f"the combine plan names {gid!r}, which is not one of this offspring's parents")
            resolved[name] = index[gid]
        assemble([p.genome.path for p in parents], resolved, work, self.cfg.genes)

    def tournament(self, population: list) -> Member:
        # Round 1 has only the seed to draw from, so the tournament can
        # never be wider than the population it samples without replacement.
        size = min(max(2, self.cfg.tournament), len(population))
        pick = self.rng.sample(population, size)
        return max(pick, key=lambda m: m.genome.score if m.genome.score is not None else -1e18)

    # -- evaluation --------------------------------------------------------

    def evaluate(self, round_no: int, members: list, rescore: tuple = ()) -> None:
        """One fanout run per round: every candidate crossed with every
        task, repeated `repeats` times. Fitness is the mean; the standard
        deviation travels with it so a 'win' inside the noise is visible."""
        # Two slots may hold the same content, and content already scored is
        # not re-run unless it was explicitly put up for rescoring.
        rescored = {m.genome.gid for m in rescore}
        candidates, seen = [], set()
        for member in list(members) + list(rescore):
            gid = member.genome.gid
            if gid in seen:
                continue
            if member.genome.scores and gid not in rescored:
                continue
            seen.add(gid)
            candidates.append(member.genome)

        variants = []
        for genome in candidates:
            for t, task in enumerate(self.cfg.tasks):
                for r in range(self.cfg.repeats):
                    variants.append((genome, t, task, r))
        if not variants:
            return
        if self.cfg.max_variants and self.variants_spent + len(variants) > self.cfg.max_variants:
            raise Budget(f"budget exhausted: {self.variants_spent} variants spent")

        spec = {
            "run": f"round-{round_no}",
            "repo": str(self.cfg.repo),
            "agent": self.cfg.agent,
            "harness": self.cfg.harness,
            "tenon": self.cfg.tenon,
            "concurrency": self.cfg.concurrency,
            "timeout": self.cfg.timeout,
            "state_dir": str(self.root / "rounds"),
            "branch_prefix": f"evolve/{self.cfg.run}",
            "variants": [
                {
                    "name": f"{g.short}-t{t}r{r}",
                    "task": task,
                    "mutate": f"{sys.executable} {Path(__file__).resolve()} _inject {g.path}",
                    **({"pins": self.pins_for(g)} if self.cfg.model else {}),
                }
                for g, t, task, r in variants
            ],
        }
        if self.cfg.turn_timeout:
            spec["turn_timeout"] = self.cfg.turn_timeout
        spec_path = self.root / f"round-{round_no}.fanout.json"
        spec_path.write_text(json.dumps(spec, indent=2) + "\n")

        self.log(f"  evaluating {len(candidates)} candidates over {len(variants)} runs")
        subprocess.run([sys.executable, self.cfg.fanout, "start", "--spec", str(spec_path)], check=False)
        self.variants_spent += len(variants)
        try:
            self.score_round(round_no, candidates, variants, rescored, members, rescore)
        finally:
            # Once the harness has run, the round's worktrees are owed
            # back whether or not scoring succeeded.
            self.reclaim(round_no)

    def score_round(self, round_no, candidates, variants, rescored, members, rescore) -> None:

        collected = subprocess.run(
            [
                sys.executable,
                self.cfg.fanout,
                "collect",
                f"round-{round_no}",
                "--json",
                "--text",
                "--state-dir",
                str(self.root / "rounds"),
            ],
            capture_output=True,
            text=True,
        )
        if collected.returncode != 0:
            raise EvolveError(f"fanout collect failed: {collected.stderr.strip()[:200]}")
        records = {r["variant"]: r for r in json.loads(collected.stdout)}
        # A variant that never ran silently shrinks the round — a judged
        # round loses an entry, and a comparison-based score loses its anchor.
        # Say so rather than scoring a smaller field as if it were the one asked
        # for.
        broken = [
            f"{name} ({records[name].get('status', 'missing')})"
            for name in (f"{g.short}-t{t}r{r}" for g, t, task, r in variants)
            if name not in records or records[name].get("status") != "done"
        ]
        if broken:
            self.log(f"  WARNING: {len(broken)} of {len(variants)} runs did not complete: {', '.join(broken[:4])}")
            for name in broken[:4]:
                detail = records.get(name.split(" ")[0], {}).get("detail", "")
                if detail:
                    self.log(f"    {detail.splitlines()[0][:160]}")

        reports = self.root / "reports"
        reports.mkdir(exist_ok=True)
        for genome in candidates:
            samples, log = [], []
            for t, task, r in [(t, task, r) for g, t, task, r in variants if g is genome]:
                variant = f"{genome.short}-t{t}r{r}"
                record = records.get(variant)
                # Three ways a variant can teach us nothing about the genome,
                # and all three are the same class. tenon reported outcome
                # "error" (the environment failed, not the candidate); a
                # sibling's fail-fast cancelled this one before it ran; or the
                # variant is missing from collect entirely. Every scorer
                # shipped here turns a non-done status into 0.0, so passing
                # any of them on would record infrastructure noise as evidence
                # about the genome. Drop the sample instead — a genome left
                # with no samples at all stays unscored and is evaluated
                # again, which is the honest outcome of having learned
                # nothing.
                # fanout's lifecycle vocabulary, read as data: evolve shells
                # out to fanout rather than importing it.
                unscorable = "missing" if record is None else record.get("status", "")
                if unscorable in ("missing", "errored", "cancelled"):
                    detail = ((record or {}).get("detail") or "").splitlines()
                    self.log(
                        f"  WARNING: {variant} {unscorable}, not scored "
                        f"(outcome {(record or {}).get('outcome') or 'none'})"
                        + (f": {detail[0][:160]}" if detail else "")
                    )
                    continue
                if record.get("fingerprint") and record["fingerprint"] != genome.gid:
                    self.log(
                        f"  warning: {genome.short} ran as {record['fingerprint'].split(':')[-1][:8]}; "
                        "the injected genome is not what tenon fingerprinted"
                    )
                value = self.fitness(round_no, genome, t, task, record)
                samples.append(value)
                log.append(
                    {
                        "task_index": t,
                        "task": task,
                        "repeat": r,
                        "score": value,
                        "status": record.get("status", ""),
                        "turns": record.get("turns", []),
                        "text": record.get("text", ""),
                        "patch": record.get("patch", ""),
                    }
                )
            # Written beside the genome, never inside it: anything added to the
            # genome directory would change the fingerprint that names it.
            report = reports / f"{genome.short}.json"
            report.write_text(json.dumps(log, indent=2))
            genome.report = str(report)
            # Tags the score hook observed belong to the slots holding this
            # genome now, not only to its future children.
            for member in list(members) + list(rescore):
                if member.genome is genome:
                    member.tags.update(self.observed.get(genome.gid, {}))
            # Rescoring appends: the fingerprint names the same content, so its
            # samples accumulate and the running mean tightens instead of
            # staying frozen at whatever the first draw happened to be.
            again = genome.gid in rescored and bool(genome.scores)
            genome.scores = (genome.scores if again else []) + samples
            genome.score = statistics.fmean(genome.scores) if genome.scores else None
            genome.stdev = statistics.pstdev(genome.scores) if len(genome.scores) > 1 else 0.0
            self.record(
                {
                    "round": round_no,
                    "status": "rescored" if again else "scored",
                    "genome": genome.gid,
                    "short": genome.short,
                    "mutator": genome.mutator,
                    "parents": genome.parents,
                    "score": genome.score,
                    "scores": genome.scores,
                    "stdev": genome.stdev,
                    "path": str(genome.path),
                }
            )
            self.log(f"  {genome.short}  score {shown(genome.score)}  sd {genome.stdev:.4f}  [{genome.mutator}]")

    def pins_for(self, genome: Genome) -> str:
        """One pin set per genome, so the model can be pinned across a moving
        population.

        A pin set binds an expected source fingerprint, and every mutation
        changes that fingerprint — so a single shared pin set would fail
        verification on every candidate but the seed. Writing one per genome is
        the same trick tenon documents for comparing harnesses, turned around:
        many genomes crossed with one pin set instead of one commit crossed
        with many.

        Pins are written by the gate: there is no ordering to get wrong
        between proving a source and pinning it."""
        out = self.root / "pins"
        out.mkdir(exist_ok=True)
        path = out / f"{genome.short}.json"
        if not path.exists():
            # A pin set records the agent name from its directory's basename,
            # and compile checks it. The genome directory is named after a
            # fingerprint, so writing the pins there and applying them to the
            # agent path inside a worktree drifts on the name
            # (pins.drift.agent) and fails closed. Stage the genome under the
            # name the worktree will see.
            staged = out / genome.short / Path(self.cfg.agent).name
            if staged.exists():
                shutil.rmtree(staged)
            staged.parent.mkdir(parents=True, exist_ok=True)
            shutil.copytree(genome.path, staged, symlinks=True)
            try:
                # The adapter confirms the terminator named the path it wrote
                # rather than trusting exit 0, and raises when it did not.
                verdict = self.tenon.gate(staged, write_pins=path, model=self.cfg.model)
            finally:
                shutil.rmtree(staged.parent, ignore_errors=True)
            if not verdict.ok:
                # This same content passed the gate on the way in, so a
                # rejection here is tenon contradicting itself, not a verdict
                # on the candidate. Name what it rejected.
                raise EvolveError(
                    f"the gate rejected {genome.short} while writing its pins, having "
                    f"already admitted it — a contract violation, not a finding about "
                    f"the genome: {', '.join(i for i in verdict.rejected if i) or 'no diagnostics'}"
                )
        return str(path)

    def reclaim(self, round_no: int) -> None:
        """A round is population x tasks x repeats full checkouts. Once it
        is scored, drop the worktrees and branches but keep the run's state, so
        the event streams and patches stay auditable without the disk cost."""
        if self.cfg.keep_worktrees:
            return
        subprocess.run(
            [
                sys.executable,
                self.cfg.fanout,
                "clean",  # not tenon argv: clean — fanout's own subcommand
                f"round-{round_no}",
                "--force",  # not tenon argv: --force — fanout's own flag
                "--keep-state",
                "--state-dir",
                str(self.root / "rounds"),
            ],
            capture_output=True,
            text=True,
        )

    def fitness(self, round_no: int, genome: Genome, task_index: int, task: str, record: dict) -> float:
        payload = json.dumps(
            {
                "run": self.cfg.run,
                "round": round_no,
                "genome": genome.gid,
                "genome_path": str(genome.path),
                "task_index": task_index,
                "task": task,
                "record": record,
            }
        )
        proc = subprocess.run(
            ["/bin/sh", "-c", self.cfg.score],
            input=payload,
            capture_output=True,
            text=True,
            env={**os.environ, "EVOLVE_WORKSPACE": record.get("workspace", "")},
        )
        if proc.returncode != 0:
            # A scorer that fails is an infrastructure failure, not evidence
            # about the genome. Counting it as zero turns a judge outage into
            # "every candidate is terrible" and quietly corrupts the search; a
            # scorer that genuinely means zero returns it with exit 0.
            raise EvolveError(
                f"the score command exited {proc.returncode} for {genome.short}: "
                f"{proc.stderr.strip()[:200]}"
            )
        for line in reversed(proc.stdout.strip().splitlines()):
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(obj, dict) and "score" in obj:
                if isinstance(obj.get("tags"), dict):
                    self.observed.setdefault(genome.gid, {}).update(obj["tags"])
                return float(obj["score"])
        raise EvolveError("the score command printed no JSON object carrying a score")

    # -- selection ---------------------------------------------------------

    def select(self, round_no: int, population: list, candidates: list) -> list:
        """Survivor selection. The built-in is elitist over the union of the
        incumbents and this round's candidates — the (mu+lambda) scheme —
        and keeps one slot per distinct genome so the default cannot collapse
        into copies of itself. A hook may return the same genome twice, which
        is how an island reset seeds one island from another's best."""
        keep = self.cfg.population
        if self.cfg.select_policy == "elitist":
            pool = sorted(
                population + candidates,
                key=lambda m: m.genome.score if m.genome.score is not None else -1e18,
                reverse=True,
            )
            kept, seen = [], set()
            for member in pool:
                if member.genome.gid in seen:
                    continue
                seen.add(member.genome.gid)
                kept.append(member)
                if len(kept) >= keep:
                    break
            return kept
        out = hook(
            self.cfg.select_policy,
            {
                "round": round_no,
                "keep": keep,
                "strategy": self.cfg.strategy,
                "population": [genome_view(m, i, self.cfg.genes) for i, m in enumerate(population)],
                "candidates": [genome_view(m, i, self.cfg.genes) for i, m in enumerate(candidates)],
            },
            "select",
        )
        chosen = out.get("population")
        if not isinstance(chosen, list) or not chosen:
            raise EvolveError("the select hook must return a non-empty population list")
        survivors = []
        for entry in chosen:
            tags = {}
            gid = entry
            if isinstance(entry, dict):
                gid, tags = entry.get("genome"), entry.get("tags") or {}
            if gid not in self.known:
                raise EvolveError(f"the select hook named an unknown genome {gid!r}")
            survivors.append(Member(self.known[gid], dict(tags)))
        return survivors

    # -- checkpoint --------------------------------------------------------

    def checkpoint(self, round_no: int, population: list) -> None:
        """Record everything needed to resume after this round.

        Rounds are expensive — harness runs, and a person's attention in
        the judged case — so finishing one and being unable to build on it is
        the worst failure this tool has. The genomes and their scores are
        already durable in lineage.jsonl; what is not is which slots survived,
        what they were tagged with, and where the RNG had got to."""
        write_json(
            self.root / "checkpoint.json",
            {
                "schema_version": SCHEMA_VERSION,
                "round": round_no,
                "variants": self.variants_spent,
                "population": [{"genome": m.genome.gid, "tags": m.tags} for m in population],
                "observed": self.observed,
                "rng_state": json.loads(json.dumps(self.rng.getstate())),
            },
        )

    def restore(self) -> tuple:
        """Rebuild a search from its own record: lineage.jsonl carries every
        genome ever admitted and what it scored, checkpoint.json carries the
        population that survived."""
        if not self.lineage_path.is_file():
            raise EvolveError(f"{self.root} has no lineage to resume from")
        for line in self.lineage_path.read_text().splitlines():
            entry = json.loads(line)
            if entry.get("status") not in ("scored", "rescored") or not entry.get("genome"):
                continue
            gid = entry["genome"]
            genome = self.known.get(gid) or Genome(
                gid=gid,
                path=Path(entry["path"]),
                round_no=entry["round"],
                parents=entry.get("parents", []),
                mutator=entry.get("mutator", ""),
            )
            genome.scores = entry.get("scores", [])
            genome.score = entry.get("score")
            genome.stdev = entry.get("stdev")
            report = self.root / "reports" / f"{genome.short}.json"
            if report.is_file():
                genome.report = str(report)
            self.known[gid] = genome
        if not self.known:
            raise EvolveError(f"{self.root} records no scored genome to resume from")

        saved = self.root / "checkpoint.json"
        if not saved.is_file():
            raise EvolveError(f"{self.root} has no checkpoint; it never finished a round")
        state = json.loads(saved.read_text())
        missing = [e["genome"] for e in state["population"] if e["genome"] not in self.known]
        if missing:
            raise EvolveError(f"the checkpoint names genomes the lineage does not carry: {missing[0]}")
        population = [Member(self.known[e["genome"]], dict(e.get("tags") or {})) for e in state["population"]]
        self.variants_spent = int(state.get("variants", 0))
        self.observed = state.get("observed", {})
        if state.get("rng_state"):
            self.rng.setstate(tuple_of(state["rng_state"]))
        return population, int(state["round"]) + 1

    # -- the loop ----------------------------------------------------------

    def go(self) -> int:
        if self.root.exists() and not self.resume:
            raise EvolveError(
                f"run {self.cfg.run!r} already exists at {self.root} (pass --resume to continue it)"
            )
        if self.resume and not self.root.exists():
            raise EvolveError(f"nothing to resume: {self.root} does not exist")
        self.genomes_dir.mkdir(parents=True, exist_ok=True)
        (self.root / "search.json").write_text(json.dumps(self.cfg.to_json(), indent=2, sort_keys=True) + "\n")

        if self.dry_run:
            print(json.dumps(self.cfg.to_json(), indent=2, sort_keys=True))
            return 0

        scratch = self.root / "scratch"
        scratch.mkdir(exist_ok=True)
        stale = 0

        if self.resume:
            population, first_round = self.restore()
            best = max(population, key=lambda m: m.genome.score if m.genome.score is not None else -1e18)
            self.log(
                f"resumed at round {first_round} — {len(self.known)} genomes known, "
                f"{len(population)} carried forward, incumbent {best.genome.short} "
                f"at {shown(best.genome.score)}"
            )
        else:
            work = scratch / "seed"
            materialize(self.cfg.seed, work)
            seed = self.admit(work, 0, [], "seed")
            if seed is None:
                raise EvolveError("the seed genome did not pass the gate; fix it before searching")
            self.log(f"round 0 — seed {seed.short}")
            first_round = 1

        try:
            if not self.resume:
                population = [Member(seed, {})]
                self.evaluate(0, population)
                best = population[0]
                self.checkpoint(0, population)

            for round_no in range(first_round, self.cfg.rounds + 1):
                self.log(f"round {round_no} — incumbent {best.genome.short} at {shown(best.genome.score)}")
                candidates = self.propose(round_no, population)
                if not candidates:
                    self.log("  no admissible candidates; stopping")
                    break
                # Re-evaluating the incumbent is the default because elitist
                # selection on a noisy score otherwise keeps whichever genome
                # got the luckiest draw and never revisits it.
                rescore = ()
                if self.cfg.reevaluate == "incumbent":
                    rescore = (best,)
                elif self.cfg.reevaluate == "population":
                    rescore = tuple(population)
                was = best.genome.score
                self.evaluate(round_no, candidates, rescore)
                if rescore and best.genome.score != was and None not in (was, best.genome.score):
                    drift = best.genome.score - was
                    self.log(
                        f"  incumbent rescored {was:.4f} -> {best.genome.score:.4f} "
                        f"({drift:+.4f}) over {len(best.genome.scores)} samples"
                    )
                population = self.select(round_no, population, candidates)
                # The incumbent is the best-scoring survivor, not the first one
                # listed: a select policy is free to order its population any
                # way it likes, and an island policy groups by island.
                leader = max(population, key=lambda m: m.genome.score if m.genome.score is not None else -1e18)
                # An unscored genome cannot take the incumbency, and it cannot
                # hold it against one that was measured: None is the absence
                # of a measurement, not a low one.
                if (
                    leader.genome.gid != best.genome.gid
                    and leader.genome.score is not None
                    and (best.genome.score is None or leader.genome.score > best.genome.score)
                ):
                    displaced = best
                    gain = leader.genome.score - (displaced.genome.score or 0.0)
                    best, stale = leader, 0
                    self.log(f"  new incumbent {best.genome.short} at {shown(best.genome.score)} (+{gain:.4f})")
                    # A genome with one sample has a spread of zero, so judging
                    # the gain only against the winner's own spread would never
                    # fire on a fresh candidate — which is exactly the case
                    # worth warning about. The displaced incumbent has been
                    # measured more than once; use the wider of the two.
                    spread = max(displaced.genome.stdev or 0.0, leader.genome.stdev or 0.0)
                    if spread and gain < spread:
                        self.log(
                            f"  note: the gain ({gain:.4f}) is smaller than the spread already seen "
                            f"({spread:.4f}) — treat it as noise"
                        )
                else:
                    best = leader if leader.genome.gid == best.genome.gid else best
                    stale += 1
                    self.log(f"  no improvement ({stale} round(s) stale)")
                self.checkpoint(round_no, population)
                if (
                    self.cfg.target is not None
                    and best.genome.score is not None
                    and best.genome.score >= self.cfg.target
                ):
                    self.log(f"target {self.cfg.target} reached")
                    break
                if self.cfg.patience and stale >= self.cfg.patience:
                    self.log(f"patience {self.cfg.patience} exhausted")
                    break
        except Budget as stop:
            self.log(str(stop))
            scored = [g for g in self.known.values() if g.score is not None]
            if not scored:
                self.log("no genome was scored before the budget ran out; nothing to report")
                return 1
            best = Member(max(scored, key=lambda g: g.score), {})

        summary = {
            "run": self.cfg.run,
            "strategy": self.cfg.strategy,
            "variants": self.variants_spent,
            "best": {
                "genome": best.genome.gid,
                "short": best.genome.short,
                "score": best.genome.score,
                "stdev": best.genome.stdev,
                "path": str(best.genome.path),
                "mutator": best.genome.mutator,
                "parents": best.genome.parents,
            },
        }
        (self.root / "best.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n")
        self.log("")
        self.log(f"best {best.genome.short} at {shown(best.genome.score)} after {self.variants_spent} variants")
        self.log(f"review it: diff -ru {self.cfg.seed} {best.genome.path}")
        self.log("promotion is yours to make — evolve never writes to the source agent")
        return 0


class Budget(Exception):
    """The variant budget ran out; report what was found so far."""


# --------------------------------------------------------------------------
# commands
# --------------------------------------------------------------------------


def cmd_run(args) -> int:
    cfg = load_config(Path(args.spec).expanduser())
    if args.rounds:
        cfg.rounds = args.rounds
    return Search(cfg, args.dry_run, args.resume).go()


def cmd_inject(args) -> int:
    """Replace the agent directory in a fanout worktree with a genome. Used as
    each variant's mutate command; cwd is the variant's agent directory."""
    target = Path.cwd().resolve()
    workspace = os.environ.get("FANOUT_WORKSPACE", "")
    if not workspace:
        raise EvolveError("_inject must run inside a fanout worktree")
    root = Path(workspace).resolve()
    if not target.is_relative_to(root):
        raise EvolveError("_inject must run inside a fanout worktree")
    # Clearing the agent directory is only safe below the worktree root: at the
    # root it would take .git and the checkout with it. An agent at the repo
    # root cannot be injected this way.
    if target == root:
        raise EvolveError("the agent directory must be below the worktree root, not the root itself")
    source = Path(args.genome).expanduser().resolve()
    if not source.is_dir():
        raise EvolveError(f"genome {source} does not exist")
    for entry in target.iterdir():
        if entry.is_dir() and not entry.is_symlink():
            shutil.rmtree(entry)
        else:
            entry.unlink()
    for entry in sorted(source.iterdir()):
        copy_gene(entry, target / entry.name)
    return 0


def cmd_lineage(args) -> int:
    root = (Path(args.state_dir).expanduser() if args.state_dir else Path.home() / ".evolve") / args.run
    path = root / "lineage.jsonl"
    if not path.exists():
        raise EvolveError(f"no lineage for run {args.run!r}")
    if args.json:
        sys.stdout.write(path.read_text())
        return 0
    for line in path.read_text().splitlines():
        entry = json.loads(line)
        gid = (entry.get("genome") or "").split(":")[-1][:8] or "-"
        parents = ",".join(p.split(":")[-1][:8] for p in entry.get("parents", [])) or "-"
        score = f"{entry['score']:.4f}" if entry.get("score") is not None else "-"
        extra = ",".join(entry.get("diagnostics", []))[:48]
        if entry.get("source_digest"):
            extra = f"src={entry['source_digest'].split(':')[-1][:8]} {extra}".rstrip()
        print(
            f"round{entry['round']:<3} {entry['status']:<10} {gid:<9} "
            f"parents={parents:<19} score={score:<9} {entry.get('mutator','')} {extra}"
        )
    return 0


def cmd_best(args) -> int:
    root = (Path(args.state_dir).expanduser() if args.state_dir else Path.home() / ".evolve") / args.run
    path = root / "best.json"
    if not path.exists():
        raise EvolveError(f"no result for run {args.run!r}")
    summary = json.loads(path.read_text())
    best = summary["best"]
    print(f"run        {summary['run']} ({summary['strategy']}, {summary['variants']} variants)")
    print(f"genome     {best['genome']}")
    print(f"score      {shown(best.get('score'))}  sd {shown(best.get('stdev'))}")
    print(f"mutator    {best['mutator']}")
    print(f"path       {best['path']}")
    print()
    print("Nothing has been promoted. Review the diff, then copy it into the")
    print("source agent yourself if it holds up.")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="evolve",
        description="Hill-climbing and genetic search over tenon agent projects, one fanout round per step.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    run = sub.add_parser("run", help="run the search")
    run.add_argument("--spec", required=True)
    run.add_argument("--dry-run", action="store_true", help="print the resolved config and exit")
    run.add_argument(
        "--rounds",
        type=int,
        help="override the spec's round count, which is how a resume asks for one more",
    )
    run.add_argument(
        "--resume",
        action="store_true",
        help="continue an existing run from its last checkpointed round, "
        "reusing every genome it already scored",
    )
    run.set_defaults(func=cmd_run)

    lineage = sub.add_parser("lineage", help="every genome ever proposed, with its fate")
    lineage.add_argument("run")
    lineage.add_argument("--json", action="store_true")
    lineage.add_argument("--state-dir")
    lineage.set_defaults(func=cmd_lineage)

    best = sub.add_parser("best", help="the incumbent and how to review it")
    best.add_argument("run")
    best.add_argument("--state-dir")
    best.set_defaults(func=cmd_best)

    inject = sub.add_parser("_inject", help=argparse.SUPPRESS)
    inject.add_argument("genome")
    inject.set_defaults(func=cmd_inject)

    return parser


def main(argv: list) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except TenonEnvironment as err:
        # Exit 3, not 1: the caller retries or escalates an environment
        # failure, and must not read it as "the search rejected everything".
        print(f"evolve: tenon environment failure: {err}", file=sys.stderr)
        return 3
    except EvolveError as err:
        print(f"evolve: {err}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
