# A structural mutator: removes a locus.
#
# Growth without pruning is bloat — the classic failure of variable-length
# representations. Instructions get longer every round, context cost
# climbs, and nothing ever removes a rule that stopped earning its place. This
# is the counterweight, not symmetry for its own sake.
#
# instructions.md is never a candidate: tenon requires it, so removing it makes
# the directory stop being an agent project rather than a worse one.
import os, pathlib, random, shutil, sys

genes = [g for g in os.environ.get("EVOLVE_GENES", "").split(",") if g and g != "instructions.md"]
if not genes:
    sys.exit(1)  # nothing safe to drop; the candidate is discarded

victim = pathlib.Path(random.choice(genes))
if victim.is_dir():
    shutil.rmtree(victim)
else:
    victim.unlink()
