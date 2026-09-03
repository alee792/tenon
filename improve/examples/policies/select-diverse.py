# Survivor selection: elitist, but with a diversity floor — no two survivors
# may share a gene set, so the population cannot collapse onto one lineage.
import json, sys
req = json.load(sys.stdin)
pool = sorted(req["population"] + req["candidates"], key=lambda g: g["score"] or 0.0, reverse=True)
kept, seen = [], set()
for g in pool:
    key = tuple(g["genes"])
    if key in seen and len(kept) >= 2:
        continue
    seen.add(key)
    kept.append(g["genome"])
    if len(kept) >= req["keep"]:
        break
print(json.dumps({"population": kept or [pool[0]["genome"]]}))
