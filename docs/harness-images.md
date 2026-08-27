# Harness images

Tenon harness images are ordinary OCI build inputs. Each image carries one
native harness (Claude Code or Codex), tenon, and the pinned prepare-time
toolchains [ADR 0021](adr/0021-execute-authored-tools-from-a-self-contained-closure.md)
names — `uv`, the Go toolchain, and (once its TypeScript rendering lands)
Deno's compiler mode — at `/opt/tenon/toolchains`. Build tools live in the
image; the runtimes they produce or fetch (a compiled Go host, a pinned
standalone CPython, a Deno runtime executable) enter the staged closure
`tenon stage` prepares, per ADR 0021's line between runtimes and
toolchains. A harness image contains no agent, no provider credentials, no
login state, and no conversation state.

Definitions for both harnesses live under [`images/`](../images/):
[`images/codex/Dockerfile`](../images/codex/Dockerfile),
[`images/claude/Dockerfile`](../images/claude/Dockerfile), and the shared
pinned-inputs manifest [`images/inputs.json`](../images/inputs.json). They
are buildable and exercisable locally today. **Neither is published.** No
`ghcr.io/alee792/tenon/*` image exists yet for either harness.

## Publication gating

Building and running a harness image locally, including in CI as a
build-and-test step, is not gated. Publishing one is separate, explicitly
authorized work:

- Only an exact `vX.Y.Z` tenon tag would publish an image, reproducing the
  prototype's release discipline: a read-only build-and-check job proves the
  image, a separate, environment-gated release step publishes it, and no
  moving `latest`, major, or minor tag is ever published.
- The Codex image requires a current review of OpenAI's redistribution
  terms and explicit human authorization before its first publication, per
  [ADR 0012](adr/0012-stage-agent-filesystems-for-downstream-oci-builds.md)'s
  "publishing an image requires current permission to redistribute that
  harness." A permissive component license (Codex's own code is
  Apache-2.0) does not by itself clear this gate.
- The Claude image carries the same restriction and, additionally, is not
  publishable at all pending a review of Anthropic's terms and explicit
  authorization; [`images/claude/Dockerfile`](../images/claude/Dockerfile)
  says so at the top of the file and its `inputs.json` entry is marked
  `"publication_gate": "blocked-pending-permission"`.
- Per ADR 0012, tenon must never substitute an unverified runtime download
  to preserve either journey below — a gated or unpinned component blocks
  the image rather than being worked around.

Today every component in [`images/inputs.json`](../images/inputs.json)
other than `go` and `uv`'s version numbers is a placeholder (see
"Pinned inputs" below); the Dockerfiles document the intended build shape,
not a build that currently produces a usable image.

## Two journeys

### Direct: apply and ship

Pin an exact tenon release tag, copy the agent in as the image's non-root
user, and apply it during the build. The result is one larger image that
retains the build toolchains:

```dockerfile
ARG TENON_VERSION
FROM ghcr.io/alee792/tenon/codex:${TENON_VERSION}

COPY --chown=65532:65532 . /agent
RUN tenon apply /agent --workspace /workspace --harness codex

ENTRYPOINT ["/opt/tenon/harness/bin/codex"]
```

This is the shortest journey. Use it when image size, toolchain exposure,
or offline startup do not matter more than build simplicity.

### Two-stage: selective staging

Recommended when image size, build-tool exposure, or offline startup
matters — not required for correctness. `tenon stage` prepares one complete
runnable filesystem tree at canonical paths; a second, disposable build
stage carries only the selected agent's required execution closure onto a
clean compatible base:

```dockerfile
FROM ghcr.io/alee792/tenon/codex:${TENON_VERSION} AS build
COPY --chown=65532:65532 . /agent
RUN tenon stage /agent --harness codex --output /out/agent

FROM docker.io/library/ubuntu:24.04
ENV HOME=/home/tenon \
    PATH=/opt/tenon/bin:/opt/tenon/harness/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN groupadd --gid 65532 tenon \
    && useradd --uid 65532 --gid 65532 --home-dir /home/tenon \
       --shell /bin/sh --no-create-home --no-log-init tenon \
    && mkdir -p /home/tenon /workspace \
    && chown -R 65532:65532 /home/tenon /workspace
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/tenon/ /home/tenon/
USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/opt/tenon/bin/agent-entrypoint"]
```

## Compatible final-base contract

Every published harness image will declare one exact compatible final-base
contract, and `tenon stage` targets the source image's own OS,
architecture, and ABI — cross-compilation is not promised, and a payload is
never copied onto an incompatible base merely because the OS matches.
Concretely, the contract is:

- **Platform.** A pinned Ubuntu LTS platform manifest (`ubuntu:24.04` at the
  time of writing), `linux/amd64`, glibc. Never substitute Alpine or another
  musl base without a separately built musl payload — the staged closure's
  compiled Go host, standalone CPython, and Deno executable are built or
  fetched for glibc and will not run against musl's dynamic linker.
- **Certificate bundle.** `/etc/ssl/certs/ca-certificates.crt` populated
  (Ubuntu's raw layer does not ship one); `SSL_CERT_FILE` pointed at it. Its
  provenance is undecided: the prototype sourced it from staged CPython's
  vendored `certifi` package, recorded as `certificate_source_component`,
  but tenon's composition differs — CPython arrives via `uv` per ADR 0021,
  not as a directly staged component — so the source component must be
  re-decided before the first real build; `images/inputs.json` records this
  as `target.runtime.certificate_source_component:
  "TODO-decide-before-first-build"`.
- **Writable paths.** `/workspace` and `/home/tenon` are writable by the
  runtime identity below; a deployment that mounts an empty volume over
  either path, hiding the staged files it must preserve, is not a supported
  composition (ADR 0012).
- **Runtime identity.** Non-root UID/GID 65532, home `/home/tenon`.
- **Required shared libraries and executables.** Recorded per component in
  [`images/inputs.json`](../images/inputs.json)'s `target.runtime` block —
  the same fields ADR 0021 grows to name a payload's exact libc and ABI, so
  a mismatched base is refused by fact rather than discovered by crash.

## Credential boundary

Image builds and staging are credential-free by construction: neither reads
a credential from its environment or inputs, contacts a model, validates a
live credential, or creates a native trust decision. Authenticate only at
runtime, on a trusted system, after the image is built — never in a
Dockerfile `ARG`, `ENV`, build secret, source tree, or staged filesystem.
The prototype's runtime-authentication guidance (initializing a harness
credential cache through a short-lived container against a private mounted
volume, or mounting an operator-managed credential file through the
deployment platform's own secret mechanism) carries over unchanged; only
the paths move from `/home/hctl` to `/home/tenon`.

## Pinned inputs

[`images/inputs.json`](../images/inputs.json) is the single source of pins
for both harness images: for each component (`claude`, `codex`, `deno`,
`go`, `uv`) it records the version, download URL, sha256 digest, license,
and publication gate; `target.base` records the compatible base image
reference and digest, and `target.runtime` records the identity and
libraries above. An acquisition that cannot be pre-verified against a
recorded digest fails preparation — nothing is fetched at build time or
serve time without a pin (ADR 0012, ADR 0021).

As of this writing, `go` (1.26.5) and `uv` (0.8.17) have real, current
version numbers pinned from the versions available in the reference
environment; their sha256 digests are `TODO-pin-before-first-build`
placeholders pending a verified download of the exact release artifact.
`deno` and both harness components (`claude`, `codex`) are placeholders
across every field — version, URL, and digest — because nothing here was
fetched from the network to invent a plausible-looking value. **No digest
in this repository was fabricated**; every pin is either verified or
explicitly marked TODO, and the Dockerfiles that consume these pins cannot
produce a usable image until each `TODO-pin-before-first-build` is replaced
by a checksum-verified download.
