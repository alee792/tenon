"""Put the kit on sys.path so tests import the modules by top-level name,
mirroring how they are used at runtime. Credential-free — nothing here touches
a model or the network."""

import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]  # sia-buildnight/
for p in (ROOT, ROOT / "kit", ROOT / "kit" / "seed_agent"):
    sys.path.insert(0, str(p))
