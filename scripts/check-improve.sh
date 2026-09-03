#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required; the improve module needs 3.11 or newer" >&2
  exit 1
fi

# compileall is the syntax gate over the modules with no tests of their own.
python3 -m compileall -q improve
python3 improve/judge/test_scoring.py
