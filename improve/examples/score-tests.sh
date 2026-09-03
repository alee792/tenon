#!/bin/sh
# Fitness from the repository's own test suite, run inside the variant's
# worktree. Mechanical scorers like this are far safer than an LLM judge:
# nothing in the loop can talk the test suite into a better number.
#
# stdin:  one trial JSON object (see EVOLVE.md)
# stdout: {"score": <0..1>}
set -eu
trial=$(cat)
workspace=${EVOLVE_WORKSPACE:-}
[ -n "$workspace" ] || { echo '{"score": 0}'; exit 0; }

# A variant whose dispatch never reached a completed turn scores nothing.
status=$(printf '%s' "$trial" | python3 -c 'import json,sys; print(json.load(sys.stdin)["record"]["status"])')
[ "$status" = "done" ] || { echo '{"score": 0}'; exit 0; }

cd "$workspace"
total=$(go test ./... 2>&1 | grep -c '^\(ok\|FAIL\|---\)' || true)
passed=$(go test ./... 2>&1 | grep -c '^ok' || true)
[ "$total" -gt 0 ] || { echo '{"score": 0}'; exit 0; }
python3 -c "print('{\"score\": %f}' % ($passed / $total))"
