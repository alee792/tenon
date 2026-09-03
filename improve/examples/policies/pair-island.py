# FunSearch-style island pairing: parents are drawn only from within an island,
# and the child is tagged with that island so membership descends.
#
# Evolve has no idea what an island is. It carries the tag and inherits it;
# this file is the entire definition of the model.
import collections, json, random, sys

ISLANDS = 3
req = json.load(sys.stdin)
pop = req["population"]

by = collections.defaultdict(list)
for i, m in enumerate(pop):
    by[m["tags"].get("island", i % ISLANDS)].append(m)

pairs = []
for k in range(req["count"]):
    island = k % ISLANDS
    members = by.get(island) or [random.choice(pop)]

    def draw():
        return max(random.sample(members, min(2, len(members))), key=lambda m: m["score"] or 0.0)

    # Slot indices, not genome ids: after a reset two slots hold the same
    # genome, and only the index says which one is meant.
    pairs.append({"parents": [draw()["index"], draw()["index"]], "tags": {"island": island}})

print(json.dumps({"pairs": pairs}))
