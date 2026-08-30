# A combine policy at a finer grain than whole components: it writes the child
# itself, blending instructions.md line by line from both parents.
import json, pathlib, random, shutil, sys
req = json.load(sys.stdin)
out = pathlib.Path(req["out_dir"])
a, b = req["parents"][0], req["parents"][1]
shutil.copytree(a["path"], out, symlinks=True)
la = pathlib.Path(a["path"], "instructions.md").read_text().splitlines(keepends=True)
lb = pathlib.Path(b["path"], "instructions.md").read_text().splitlines(keepends=True)
head = [l for l in la[: la.index("---\n", 1) + 1]]
body_a, body_b = la[len(head):], lb[lb.index("---\n", 1) + 1:]
blended = head + [l for l in body_a if random.random() < 0.5] + [l for l in body_b if random.random() < 0.5]
pathlib.Path(out, "instructions.md").write_text("".join(blended) or "".join(la))
print(json.dumps({"materialized": True}))
