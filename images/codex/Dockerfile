# Tenon harness image: Codex.
#
# An ordinary OCI build input: one native harness (Codex), tenon, and the
# pinned prepare-time toolchains ADR 0021 names (uv, Go; Deno once its
# TypeScript rendering lands) at /opt/tenon/toolchains. It carries no agent,
# no provider credentials, no login state, and no conversation state. See
# ../../docs/harness-images.md for the full contract, the two supported
# journeys, and why this image is not published today.
#
# Build (locally; this is not a published image):
#   docker build -f images/codex/Dockerfile \
#     --build-arg TENON_VERSION=0.1.0-dev \
#     -t tenon/codex:local .
#
# Every ARG below must resolve to an entry in images/inputs.json before a
# real build; entries still marked "TODO-pin-before-first-build" there
# block this Dockerfile from producing a usable image until pinned.

ARG BASE_IMAGE=docker.io/library/ubuntu:24.04
FROM ${BASE_IMAGE}

ARG TENON_VERSION
ARG SOURCE_REVISION
ARG BASE_REFERENCE
ARG BASE_DIGEST
ARG INPUTS_SHA256
ARG CODEX_VERSION
ARG CODEX_SHA256
ARG GO_VERSION
ARG GO_SHA256
ARG UV_VERSION
ARG UV_SHA256

LABEL org.opencontainers.image.title="tenon Codex harness" \
      org.opencontainers.image.description="Pinned Codex harness and tenon authored-tool preparation inputs" \
      org.opencontainers.image.source="https://github.com/alee792/tenon" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      org.opencontainers.image.version="${TENON_VERSION}" \
      org.opencontainers.image.base.name="${BASE_REFERENCE}" \
      org.opencontainers.image.base.digest="${BASE_DIGEST}" \
      io.tenon.inputs.sha256="${INPUTS_SHA256}" \
      io.tenon.harness.name="codex" \
      io.tenon.harness.version="${CODEX_VERSION}" \
      io.tenon.harness.sha256="${CODEX_SHA256}" \
      io.tenon.toolchain.go.version="${GO_VERSION}" \
      io.tenon.toolchain.go.sha256="${GO_SHA256}" \
      io.tenon.toolchain.uv.version="${UV_VERSION}" \
      io.tenon.toolchain.uv.sha256="${UV_SHA256}" \
      io.tenon.compatible-base="linux-amd64-glibc" \
      io.tenon.publication-gate="blocked-pending-permission"

ENV HOME=/home/tenon \
    PATH=/opt/tenon/bin:/opt/tenon/harness/bin:/opt/tenon/toolchains/go/bin:/opt/tenon/toolchains/uv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
    GOTOOLCHAIN=local
# UV_PYTHON_DOWNLOADS=never is forward-looking: it takes effect once ADR
# 0021's standalone-CPython staging lands and uv is pointed at the pinned
# interpreter it installs into the closure. Until then uv has no pinned
# interpreter to prefer and this variable is not yet set.

# rootfs/ is materialized by scripts/prepare-codex-image-context.sh (not yet
# implemented in tenon) from the pins in images/inputs.json: the tenon
# binary at /opt/tenon/bin/tenon, the Codex harness at
# /opt/tenon/harness/bin/codex, and Go/uv at /opt/tenon/toolchains/<name>.
# It intentionally does not exist yet — no component in inputs.json is
# pinned by a verified digest, so there is nothing to materialize
# honestly. This COPY documents the shape of the build, not a runnable
# step today.
COPY --chown=0:0 rootfs/ /

RUN set -eu; \
    groupadd --gid 65532 tenon; \
    useradd --uid 65532 --gid 65532 --home-dir /home/tenon --shell /bin/sh --no-create-home --no-log-init tenon; \
    mkdir -p /home/tenon/.codex /workspace /out; \
    chown -R 65532:65532 /home/tenon /workspace /out; \
    test -x /opt/tenon/bin/tenon; \
    test -x /opt/tenon/harness/bin/codex; \
    test -s /etc/ssl/certs/ca-certificates.crt

USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/opt/tenon/bin/tenon"]
CMD ["--help"]
