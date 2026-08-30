#!/usr/bin/env python3
"""evolve `score` command that defers to the human pairwise judge.

It forwards the trial to the judge server and blocks until that generation's
round robin is finished, then returns the entry's win rate. Start the server
first; a search whose judge is not running should fail loudly rather than
quietly scoring everything zero.
"""

import json
import os
import sys
import urllib.error
import urllib.request

PORT = os.environ.get("EVOLVE_JUDGE_PORT", "8917")


def main() -> int:
    trial = sys.stdin.read()
    # A variant whose dispatch never completed has no output to compare, so it
    # is scored zero here rather than silently going missing from the round.
    record = json.loads(trial).get("record", {})
    if record.get("status") != "done":
        print(f"variant did not complete ({record.get('status') or 'unknown'}); scoring 0", file=sys.stderr)
        print(json.dumps({"score": 0.0}))
        return 0
    request = urllib.request.Request(
        f"http://127.0.0.1:{PORT}/score",
        data=trial.encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        # No timeout: the whole point is to wait for a person.
        with urllib.request.urlopen(request) as response:
            payload = json.load(response)
    except urllib.error.URLError as err:
        print(f"judge unreachable on port {PORT}: {err}", file=sys.stderr)
        return 1
    if "score" not in payload:
        print(f"judge error: {payload.get('error', 'no score returned')}", file=sys.stderr)
        return 1
    print(json.dumps({"score": payload["score"]}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
