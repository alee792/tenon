# FunSearch-style island survival: each island keeps its own best, so a strong
# lineage cannot crowd out the others. Periodically the weakest island is wiped
# and reseeded from the strongest — the move that makes the model work, and the
# reason a genome has to be able to occupy two slots at once.
import collections, json, sys

ISLANDS, PER_ISLAND, RESET_EVERY = 3, 2, 3
req = json.load(sys.stdin)
pool = req["population"] + req["candidates"]

by = collections.defaultdict(list)
for m in pool:
    by[m["tags"].get("island", 0)].append(m)

islands = []
for island in range(ISLANDS):
    ranked = sorted(by.get(island, []), key=lambda m: m["score"] or 0.0, reverse=True)
    kept, seen = [], set()
    for m in ranked:
        if m["genome"] in seen:
            continue
        seen.add(m["genome"])
        kept.append(m)
        if len(kept) >= PER_ISLAND:
            break
    islands.append(kept)

if req["round"] % RESET_EVERY == 0:
    strength = [max([m["score"] or 0.0 for m in isl], default=-1.0) for isl in islands]
    worst, best = strength.index(min(strength)), strength.index(max(strength))
    if worst != best and islands[best]:
        islands[worst] = list(islands[best][:PER_ISLAND])
        print(f"island {worst} reset from island {best}", file=sys.stderr)

population = [
    {"genome": m["genome"], "tags": {"island": island}}
    for island, members in enumerate(islands)
    for m in members
]
print(json.dumps({"population": population or [{"genome": pool[0]["genome"], "tags": {"island": 0}}]}))
