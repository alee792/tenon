# Crossover: single-point rather than uniform. Loci are ordered, everything
# before the cut comes from the fitter parent, everything after from the other.
import json, random, sys
req = json.load(sys.stdin)
parents = sorted(req["parents"], key=lambda p: p["score"] or 0.0, reverse=True)
loci = sorted({g for p in parents for g in p["genes"]})
cut = random.randrange(1, len(loci)) if len(loci) > 1 else 1
plan = {}
for i, name in enumerate(loci):
    preferred = parents[0] if i < cut else parents[1]
    fallback = parents[1] if i < cut else parents[0]
    plan[name] = preferred["genome"] if name in preferred["genes"] else fallback["genome"]
print(json.dumps({"genes": plan}))
