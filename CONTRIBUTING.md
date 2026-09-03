# Contributing

Read [`AGENTS.md`](AGENTS.md) first; its authority order governs which
document wins when documents disagree, and applies to every contribution
here.

## Local gate

Run `./scripts/check.sh` before sending a change; it formats, vets, and
races the test suite, and is the same gate expected of any affected work.

The `improve/` module is Python, and has its own gate: `./scripts/check-improve.sh`
compiles it and runs the judge's scoring tests. It needs only a Python 3.11
interpreter, and the Go gate does not depend on it — a Go-only change never
needs Python installed, and a broken judge test never reads as "repository
checks failed".

## Tests

Tests are credential-free: fake harness processes stand in for Claude Code
and Codex, and no test makes a live model call. New behavior is proven the
same way.

## Architecture decisions

Record consequential architecture choices as short ADRs under `docs/adr/`,
following the existing entries' shape.

## Dependencies

Dependencies are rare. Justify each one inline in `go.mod` — what the
module is for and why the standard library cannot cover it — rather than by
ADR, and do not re-litigate a dependency whose justification already
stands.
