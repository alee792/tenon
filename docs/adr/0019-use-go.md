# ADR 0019: Use Go for the implementation

- Status: accepted

## Decision

Implement tenon in Go, preferring the standard library, with `gofmt`-enforced
formatting, `go vet`, and the standard `testing` package as the baseline
toolchain. Pin the toolchain through `go.mod` so local checks and CI build
identically.

This is a fresh decision on the specification's requirements, not an
inheritance: the prototype's Go choice was explicitly a re-decide, and the
prototype's code is never ported.

## Context

The product contract constrains the language more than taste does:

- **Distribution.** [ADR 0005](0005-first-install-release-archive.md) and the
  specification require one executable at a release-archive root, installed
  by extraction to `PATH` with no runtime dependency, and later a staged tree
  carrying `tenon` into a minimal base image
  ([ADR 0012](0012-stage-agent-filesystems-for-downstream-oci-builds.md)).
  That demands a language that cross-compiles to a single static binary.
- **The work is processes, files, and protocols.** Tenon supervises harness
  and tool-host child processes, speaks stdio MCP and JSONL, writes atomic
  owner-only state, hashes and bounds authored bytes, and renders JSON, TOML,
  and Markdown. Go's standard library covers essentially all of this —
  processes, SHA-256, JSON, filesystem discipline, concurrency for the
  dispatcher and clock — which serves tenet 3 (as little machinery as
  necessary) by keeping the trusted dependency graph near zero.
- **Determinism and gates.** Deterministic builds, one canonical format, a
  built-in race detector, and fast compile-test cycles keep the check script
  and CI simple, and the credential-free acceptance style (fake harness
  processes) leans on cheap subprocess tests.
- **The oracle is legible.** The frozen prototype is Go. Porting intent,
  never code, is easiest when a doubted behavior's reference tests read
  without translation. This is corroborating convenience, not the reason.

Alternatives: Rust also produces single static binaries but pays slower
iteration and a dependency ecosystem (async runtime, serde stack) for memory
guarantees this I/O-bound compiler does not need; TypeScript/Deno and Python
require bundling a runtime or compiling to large artifacts, weakening the
release-archive and staged-closure contracts, and their ecosystems pull
toward exactly the dependency growth tenet 3 resists. Note the authored-tool
surface is unaffected: tools are authored in TypeScript, Python, or Go
regardless of tenon's own language.

## North-star reconciliation

The choice serves commitment 2 (tenon owns the crossing: a single additive
binary beside the harness, never a runtime the harness must host) and tenets
1 and 3 (the standard library deletes dependencies a smaller-stdlib language
would need). It tensions nothing ranked higher. It is a founding dependency,
so it is recorded rather than assumed.

## Consequences

- `go.mod` pins the toolchain; `./scripts/check.sh` runs `gofmt`, `go vet`,
  and `go test ./...` and is the merge gate from the first feature commit.
- Dependencies are added one at a time with recorded justification; no
  framework, DI container, or vendor SDK enters core
  ([ADR 0014](0014-use-process-isolated-integration-packages.md)).
- `go install` remains unsupported as an end-user journey per ADR 0005; the
  release archive is the installation contract.
