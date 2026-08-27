# Tenon harness image: Claude Code.
#
# UNPUBLISHABLE PENDING PERMISSION. This definition may be built and
# exercised locally; it must never be published to a registry. Claude Code
# is distributed under Anthropic's commercial terms, and redistributing it
# inside an image requires a current review of those terms and explicit
# human authorization tenon does not have today (ADR 0012). The same rule
# would apply to a Codex image were its own vendor-terms review to change;
# see ../codex/Dockerfile and ../../docs/harness-images.md.
#
# Otherwise this image is an ordinary OCI build input, structurally
# identical to the Codex image: one native harness (Claude Code), tenon,
# and the pinned prepare-time toolchains ADR 0021 names (uv, Go; Deno once
# its TypeScript rendering lands) at /opt/tenon/toolchains. It carries no
# agent, no provider credentials, no login state, and no conversation
# state.
#
# Build (locally only; never push, tag for a registry, or publish this):
#   docker build -f images/claude/Dockerfile \
#     --build-arg TENON_VERSION=0.1.0-dev \
#     -t tenon/claude:local .
#
# Every ARG below must resolve to an entry in images/inputs.json before a
# real build; the "claude" component there is a placeholder pending both a
# pinned, checksum-verified download and the redistribution review above.

ARG BASE_IMAGE=docker.io/library/ubuntu:24.04
FROM ${BASE_IMAGE}

ARG TENON_VERSION
ARG SOURCE_REVISION
ARG BASE_REFERENCE
ARG BASE_DIGEST
ARG INPUTS_SHA256
ARG CLAUDE_VERSION
ARG CLAUDE_SHA256
ARG GO_VERSION
ARG GO_SHA256
ARG UV_VERSION
ARG UV_SHA256

LABEL org.opencontainers.image.title="tenon Claude Code harness" \
      org.opencontainers.image.description="Pinned Claude Code harness and tenon authored-tool preparation inputs; not for publication" \
      org.opencontainers.image.source="https://github.com/alee792/tenon" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      org.opencontainers.image.version="${TENON_VERSION}" \
      org.opencontainers.image.base.name="${BASE_REFERENCE}" \
      org.opencontainers.image.base.digest="${BASE_DIGEST}" \
      io.tenon.inputs.sha256="${INPUTS_SHA256}" \
      io.tenon.harness.name="claude" \
      io.tenon.harness.version="${CLAUDE_VERSION}" \
      io.tenon.harness.sha256="${CLAUDE_SHA256}" \
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
# interpreter it installs into the closure.

# rootfs/ is materialized by a prepare-context script (not yet implemented
# in tenon, mirroring scripts/prepare-codex-image-context.sh) from the pins
# in images/inputs.json: the tenon binary at /opt/tenon/bin/tenon, Claude
# Code at /opt/tenon/harness/bin/claude, and Go/uv at
# /opt/tenon/toolchains/<name>. It does not exist yet: the "claude"
# component in inputs.json has no verified digest and no redistribution
# clearance, so there is nothing to materialize honestly. This COPY
# documents the shape of the build, not a runnable step today.
COPY --chown=0:0 rootfs/ /

RUN set -eu; \
    groupadd --gid 65532 tenon; \
    useradd --uid 65532 --gid 65532 --home-dir /home/tenon --shell /bin/sh --no-create-home --no-log-init tenon; \
    mkdir -p /home/tenon/.claude /workspace /out; \
    chown -R 65532:65532 /home/tenon /workspace /out; \
    test -x /opt/tenon/bin/tenon; \
    test -x /opt/tenon/harness/bin/claude; \
    test -s /etc/ssl/certs/ca-certificates.crt

USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/opt/tenon/bin/tenon"]
CMD ["--help"]
