#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required; the improve module needs 3.11 or newer" >&2
  exit 1
fi

# The message above names a version, so check it rather than assume it: an
# older interpreter otherwise fails inside compileall with a syntax error
# that says nothing about why.
if ! python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)'; then
  echo "python3 is required; the improve module needs 3.11 or newer" >&2
  exit 1
fi

# compileall is the syntax gate over the modules with no tests of their own.
python3 -m compileall -q improve
python3 improve/judge/test_scoring.py
