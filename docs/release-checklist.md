# v0.1.0 release checklist

The ordered pre-tag checklist for the first release, distilled from
[issue #22](https://github.com/alee792/tenon/issues/22). Version policy is
already decided: ship `v0.1.0` clean — 0.x *is* the instability signal in
semver — and use GitHub's pre-release checkbox (a `-suffixed` tag such as
`v0.1.0-rc.1`) for audience signalling instead of an `-alpha` suffix; the
release workflow marks a suffixed tag as a GitHub pre-release
automatically.

Each step below names what runs it and what it requires. "Automated"
means CI or the release workflow proves it with no human action beyond
triggering it; "manual" means a person must run and record it themselves.

## 1. Full suite green

```sh
./scripts/check.sh
```

Automated: CI runs `./scripts/check.sh` on every push to `main` and every
pull request. Formats (`gofmt`), vets, and runs the full test suite with
`-race`; every test is credential-free per `AGENTS.md`, so no credentials
are required, and Docker is never required. Network *is* required when
`uv` is present on the machine: the Python-tool tests exercise real
Python-closure preparation, which fetches the pinned standalone CPython
interpreter (see the Python entry in `CHANGELOG.md`'s Known limitations —
this is not cached, and happens on every run). CI installs Deno and pins
`uv` to `0.8.17`, and sets `TENON_REQUIRE_TOOLCHAINS=1` so a missing
toolchain fails the run instead of silently skipping it; a local run on a
Go-only machine will silently skip the Python/TypeScript-dependent tests
rather than fail, so a faithful local reproduction of the CI gate needs
both toolchains installed and `TENON_REQUIRE_TOOLCHAINS=1` set, not just
`go` on `PATH`.

## 2. Reproducibility job green

The "Release build is reproducible" job in `.github/workflows/ci.yml`
builds the real release path twice against a throwaway tag
(`v0.0.0-reproducibility-check`) via `scripts/release.sh` and diffs the two
`SHA256SUMS` manifests. Automated; requires GNU tar (the runner is
`ubuntu-24.04`, which has it) and no credentials. Confirm it is green on
the commit that will be tagged, not merely on `main` at some earlier point.

## 3. Container acceptance gate

**Manual, and conditional — out of scope for v0.1.0 as a hard gate.**
Publishing either harness image is separate, explicitly authorized work
(issue #19), not required to cut v0.1.0, and currently unsatisfiable as a
gate: as of this writing, `images/inputs.json` carries thirteen unresolved
`TODO-pin-before-first-build`/`TODO-decide-before-first-build` values —
`target.base.digest`; `target.runtime.certificate_source_component`; and,
per component, `go`'s and `uv`'s `sha256`, plus `deno`'s, `claude`'s, and
`codex`'s `version`, `url`, and `sha256` in full — so
`images/codex/Dockerfile` and `images/claude/Dockerfile` "cannot produce a
usable image" (`docs/harness-images.md`'s own words) until every one of
those is resolved to a real, checksum-verified value — building them
today is a known-broken build, not a check. The Claude image additionally
awaits an Anthropic terms review before it may be published at all,
independent of pin state.

The actual container acceptance evidence for v0.1.0 is the per-language
probe gate issue #17 defines: `scripts/check-staged-images.sh`
(documented in `docs/staged-acceptance.md`), which stages each supported
language's tool agent onto the documented compatible base (`docs/harness-images.md`'s
pinned Ubuntu LTS/glibc/uid 65532 contract), runs it with `--network
none`, and proves a real tool call round-trips — proof that a staged
tree actually serves, independent of either harness image ever being
published. Run it on a Docker-capable machine and record the transcript
per `docs/staged-acceptance.md`. If no Docker-capable machine is
available before the tag, this step is satisfied by recording that state
explicitly in the release notes rather than by a green check, and does
not block step 8.

## 4. Codex driver live validation

Either of two outcomes satisfies this step:

- **Live validation.** Manually run the Codex driver's successful-turn
  path against a real, authenticated `codex` binary (the
  `//go:build harness` integration tests exercise this; CI does not run
  them). Requires local Codex credentials.
- **Known-limitation line confirmed present.** If live auth is not fixed
  before cut, confirm the release notes and `CHANGELOG.md`'s
  Known limitations state the gap explicitly: only the Codex driver's
  credential-safe 401 classification is live-validated, not its
  successful-turn path. This is already recorded in
  [`CHANGELOG.md`](../CHANGELOG.md) and
  [the specification's known limitations](product-spec.md#known-limitations)
  as of this checklist's writing.

## 5. Release candidate tag rehearsal

Tag an `-rc` suffix to exercise the full tag → build → publish pipeline
end to end before the real tag:

```sh
git tag v0.1.0-rc.1
git push origin v0.1.0-rc.1
```

**Requires a human with push and release-creation credentials** — this
checklist does not authorize running it. The `Release` GitHub Actions
workflow (`.github/workflows/release.yml`) is automated from the tag push
onward: it builds all three platform archives with `scripts/release.sh`,
verifies the checksum manifest, and publishes a GitHub release marked
pre-release (the workflow's `case "$TAG" in *-*)` branch). Confirm the
release page carries three `tenon_0.1.0-rc.1_<os>_<arch>.tar.gz` archives
and one `tenon_0.1.0-rc.1_SHA256SUMS`, and that it is flagged pre-release.

## 6. Clean-machine journey per platform

**Manual, per platform** (`darwin-arm64`, `linux-amd64`, `linux-arm64`),
using only the rc's published artifacts and the README — not this
checkout:

```sh
curl -LO https://github.com/alee792/tenon/releases/download/v0.1.0-rc.1/tenon_0.1.0-rc.1_<os>_<arch>.tar.gz
curl -LO https://github.com/alee792/tenon/releases/download/v0.1.0-rc.1/tenon_0.1.0-rc.1_SHA256SUMS
sha256sum -c tenon_0.1.0-rc.1_SHA256SUMS --ignore-missing        # linux
shasum -a 256 -c --ignore-missing tenon_0.1.0-rc.1_SHA256SUMS    # darwin
tar -xzf tenon_0.1.0-rc.1_<os>_<arch>.tar.gz
./tenon version                # reports 0.1.0-rc.1 — no leading "v";
                                # scripts/release.sh strips it from the tag
                                # before stamping the binary
```

Then, per issue #22:

- Run [the five-minute journey](../README.md#the-first-five-minutes) verbatim.
- Apply the same source to a second, unrelated workspace and confirm both
  apply cleanly.
- `tenon schedule trigger` against a schedule file, confirming it dispatches.
- `tenon stage` an agent, per staging's ADR-0021-truthful state at cut
  time (Go, Python, and TypeScript tool agents all stage and serve).

Record the transcript of each platform run as release acceptance evidence.
Requires one real machine per platform (or an equivalent clean VM/container)
and no credentials beyond what the journey itself needs (none, for `apply`
and `stage`).

## 7. README and release-notes accuracy pass

**Manual**, done once per cut, not automated:

- The README's first-five-minutes journey matches the rc rehearsal's
  actual output byte for byte.
- The README's Status section and this repository's `CHANGELOG.md` both
  name exactly which languages stage end to end at cut time (per
  issues #14–#17's state).
- `CHANGELOG.md`'s `## [0.1.0]` heading's `UNRELEASED` marker is replaced
  with the tag date, and its content is swept against `git log` one more
  time for anything landed since the rc rehearsal.
- `go test ./internal/docscheck/` passes, confirming every relative
  Markdown link across `README.md`, `AGENTS.md`, `CHANGELOG.md`,
  `CONTRIBUTING.md`, `SECURITY.md`, `examples/README.md`, and `docs/`
  still resolves.

## 8. Tag v0.1.0

Only after every step above is green and recorded, except step 3, which is
conditional per its own entry and does not block this tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

**Requires a human with push and release-creation credentials.** The
`Release` workflow then runs automatically, exactly as it did for the rc,
publishing the three platform archives and the checksum manifest as a
non-pre-release GitHub release (no `-suffix` on `v0.1.0`, so the workflow's
prerelease case does not match).
