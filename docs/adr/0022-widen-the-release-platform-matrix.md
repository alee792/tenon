# ADR 0022: Widen the release platform matrix

- Status: accepted
- Supersedes in part: [ADR 0005](0005-first-install-release-archive.md)'s
  darwin-arm64-only platform contract
- Answers: prototype ADR 0007 (alee792/hctl)'s deferral of "a future
  relocatable package or platform matrix" to "a separate, evidence-backed
  decision"

## Decision

The supported release platforms are `darwin-arm64`, `linux-amd64`, and
`linux-arm64`. The exact `vX.Y.Z` Git tag remains the authoritative release
version. For each platform the release produces
`tenon_X.Y.Z_<os>_<arch>.tar.gz`, containing exactly one `tenon` executable
at the archive root, and exactly one `tenon_X.Y.Z_SHA256SUMS` covering every
archive of that release in two-space `sha256sum -c` format. A user verifies
the exact release, extracts the executable to a stable location on `PATH`,
and applies portable agent source to a workspace — the same journey ADR 0005
established, now proven on three platforms instead of one.

Everything ADR 0005 bound beyond the platform name still binds: there is no
`go install` journey, no `tenon package` command, and generated MCP
configuration still records the resolved absolute path to the installed
executable, so moving the binary still requires `tenon apply` again.

Release archives are byte-identical for a given tag and Go toolchain, built
on the GNU-tar host `scripts/release.sh` requires (it hard-fails on bsdtar):
build timestamps derive from the tagged commit rather than the clock, the
archive records a fixed member order and uid/gid, and the gzip wrapper omits
its own name and mtime. This is not a documentation claim; it is a CI job —
"Release build is reproducible" in `.github/workflows/ci.yml` — that builds
the real release path twice against a throwaway tag and fails if the two
checksum manifests differ.

## Context

ADR 0005 chose `darwin-arm64` alone because it was the one platform the
prototype's clean-install proof exercised, and it deliberately deferred a
wider platform matrix pending separate, evidence-backed justification.
That evidence now exists on two fronts.

First, the shape of tenon's own deployment target is Linux. ADR 0012 stages
agent filesystems onto "a documented compatible base image" targeting "the
source image's own OS, architecture, and ABI" without naming an OS, but
ADR 0021 makes the Linux commitment explicit: it defines the self-contained
runtime closure those images serve from in terms of a Linux base and a
`cpython-*-linux-*` interpreter ABI. CI itself runs on `ubuntu-24.04`. A
release that never builds or exercises a `linux-amd64` (the common OCI base
and CI runner architecture) or `linux-arm64` (the common managed-runtime
target) binary ships an artifact its own build system never proves works,
while claiming Linux is where staged images run.

Second, `scripts/release.sh` and the CI reproducibility job already build
and verify all three platforms as a proven, checked-in implementation: the
script cross-compiles each `GOOS/GOARCH` pair with `CGO_ENABLED=0`, and the
`Release build is reproducible` CI job builds the full three-platform release
twice and diffs the checksum manifests. This ADR records that implementation
as the decision, rather than leaving a wider matrix as an unrecorded fact
about the release tooling.

`darwin-arm64` is kept, not dropped: it remains the proven local
development journey — the platform most tenon contributors build and run on
day to day — and dropping it would regress the very proof ADR 0005 recorded.
Widening to Linux does not change the local installation contract ADR 0005
set: no `go install`, no relocatable package, no `tenon package` command.
ADR 0012 already noted its staged-filesystem contract is a distinct,
bounded supersession of ADR 0005's packaging deferral, not a platform
matrix; this decision is the platform-matrix half of that same original
deferral, decided separately as ADR 0005 anticipated.

## Consequence

Release tooling must build and publish `darwin-arm64`, `linux-amd64`, and
`linux-arm64` archives from one exact `vX.Y.Z` tag, plus one
`tenon_X.Y.Z_SHA256SUMS` covering all three, and must keep the reproducible
build proof in CI so "byte-identical for a given tag and toolchain" stays a
tested property rather than a claim. It must not introduce an agent-image or
deployment system, copy workspace caches between machines, add a `tenon
package` command, or claim a platform beyond this three-entry matrix without
its own evidence-backed decision. Anything that widens the matrix further —
Windows, an additional Linux architecture, a libc variant — needs the same
kind of proof this decision required: a build that CI actually exercises,
not an assertion.

Adding a platform grows the release job's build matrix and the checksum
manifest's line count but changes nothing about the verify-extract-run
journey, the archive naming contract, or the absolute-path consequence for
generated MCP configuration; a script or document that assumed
`darwin-arm64` was the only entry must instead iterate the full matrix.
