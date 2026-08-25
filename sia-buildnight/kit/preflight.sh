#!/usr/bin/env bash
# Setup-window checks (run at 5:00 PM). Verifies the environment before the
# challenge reveal so we are not debugging plumbing during the sprint.
set -u

pass() { printf '  ✓ %s\n' "$1"; }
fail() { printf '  ✗ %s\n' "$1"; FAILED=1; }
FAILED=0

echo "== SIA Build Night preflight =="

command -v sia >/dev/null 2>&1 && pass "sia CLI on PATH" || fail "sia CLI not found"
python -c "import sia" 2>/dev/null && pass "sia importable" || fail "cannot import sia"

# Our kit is pure-stdlib; this must pass with no credentials.
python -c "import sys, pathlib; sys.path.insert(0, str(pathlib.Path('.').resolve())); import algorithms; algorithms.make_selector('beam-hill-climb')" \
  && pass "algorithms library imports + factory works" \
  || fail "algorithms library broken"

python -m pytest -q sia-buildnight/tests >/dev/null 2>&1 && pass "kit tests green" || fail "kit tests failing"

# Credentials (names per the setup kit; adjust on the night).
for var in ANTHROPIC_API_KEY; do
  if [ -n "${!var:-}" ]; then pass "$var set"; else fail "$var not set"; fi
done

# Profiles resolve.
[ -f sia-buildnight/kit/profiles/target-buildnight.json ] && pass "target profile present" || fail "target profile missing"

echo
if [ "$FAILED" -eq 0 ]; then
  echo "PREFLIGHT OK — ready for the reveal."
else
  echo "PREFLIGHT HAS FAILURES — visit the setup desk."
  exit 1
fi
