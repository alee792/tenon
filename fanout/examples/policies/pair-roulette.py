# Parent selection: fitness-proportional (roulette), always sexual, and it
# refuses to pair a genome with itself.
import json, random, sys
req = json.load(sys.stdin)
pop = req["population"]
weights = [max(g["score"] or 0.0, 0.0) + 0.05 for g in pop]
pairs = []
for _ in range(req["count"]):
    a = random.choices(pop, weights)[0]
    rest = [g for g in pop if g["genome"] != a["genome"]] or [a]
    b = random.choices(rest, [max(g["score"] or 0.0, 0.0) + 0.05 for g in rest])[0]
    pairs.append([a["genome"], b["genome"]])
print(json.dumps({"pairs": pairs}))
