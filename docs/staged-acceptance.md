# Staged-image acceptance

[`scripts/check-staged-images.sh`](../scripts/check-staged-images.sh) is the
manual container acceptance gate for staging
([ADR 0012](adr/0012-stage-agent-filesystems-for-downstream-oci-builds.md))
and its self-contained runtime closures
([ADR 0021](adr/0021-execute-authored-tools-from-a-self-contained-closure.md)).
It is not run by CI, for the same reason the `//go:build harness`
integration tests against live Claude/Codex binaries are not: it needs
Docker and a real container run, not a fake process, so it is run manually
and its outcome recorded as evidence (see docs/product-spec.md's "Known
limitations", "Real harness drivers").

## What it proves

For each supported authored-tool language (Go, Python, and TypeScript,
ADR 0021, issue #16) the script:

1. writes a minimal probe agent carrying one authored tool in that language,
2. runs `tenon stage` to produce a complete runnable tree,
3. copies that tree onto the documented compatible base (`ubuntu:24.04`,
   [`docs/harness-images.md`](harness-images.md)) — an adaptation of the
   two-stage journey's second stage, not verbatim (see "Deviations" below),
4. runs the built image with `--network none` as the staged non-root
   identity (uid/gid 65532): verifies the staged tree offline directly
   through `tenon stage verify`, separately runs the image through its
   default entrypoint with `--verify-only` to prove the staged
   `agent-entrypoint`'s own fail-closed verification path
   (`internal/stage/entrypoint.go`) — not merely a stand-in for it — and
   calls the probe tool over MCP through `/opt/tenon/bin/tenon mcp serve`,
   asserting `isError:false` and the exact expected output,
5. asserts staged-manifest language exclusivity (a Go-only image records no
   Python interpreter and stages no `cpython`; a Python-only image records
   the pinned interpreter identity and stages it), and
6. asserts staged-tree hygiene: zero non-regular files anywhere under
   `/opt`, `/workspace`, or `/home`, and that `/workspace` and `/home/tenon`
   are actually writable by the runtime identity (the artifact manifest
   never checks this itself).

It also stages and images a tool-free agent, proving the empty runtime
record (`runtime.bundled: false`, no staged language runtime).

This is a proof of the staged tree and the documented compatible base, not
of a published tenon harness image: `images/inputs.json` pins every harness
component (`claude`, `codex`) as `TODO-pin-before-first-build` (issue #19),
so there is no buildable harness image today to build the two-stage
journey's first stage from. The script performs that first stage's one
relevant step — `tenon stage` — directly on the host instead; the script's
own header comment records this deviation and the reason. It does not
supersede [`docs/harness-images.md`](harness-images.md)'s own publication
gating, which still governs the harness images themselves.

### Deviations from the documented two-stage Dockerfile

The gate's own generated final-stage Dockerfile is not verbatim from
[`docs/harness-images.md`](harness-images.md): most notably, it drops the
`COPY --from=build /etc/ssl/certs/ca-certificates.crt ...` line, because
that line copies the CA bundle from a build stage (a harness image `FROM`
ubuntu, carrying it via `apt`) that this gate has no substitute for. This is
genuinely unavoidable host-side, not a shortcut skipped for convenience:
none of this gate's checks make an outbound TLS connection, so the omission
does not weaken anything this run actually proves, but a transcript reader
must know the compatible-base contract's certificate clause
(`docs/harness-images.md`, "Certificate bundle") was **not** exercised by
any run of this gate. The `ENTRYPOINT`, `ENV`, and instruction ordering are
otherwise reproduced so the entrypoint-verification check above is real; the
script's own header comment is the authoritative, complete deviation list.

## When to run it

Before every exact `vX.Y.Z` tenon release tag, and any time staging
(`internal/stage`), authored-tool preparation (`internal/toolruntime`), or
the documented compatible base
([`docs/harness-images.md`](harness-images.md)) changes in a way that could
affect what actually runs inside a container. `./scripts/check.sh` proves
tenon's own logic; it cannot prove a real container boots the staged tree
and serves a real tool call over MCP — only this gate does.

## How to run it

On a Linux/amd64 host with Docker, Go, `uv`, and `deno` installed:

```sh
./scripts/check-staged-images.sh
```

It builds tenon from the current checkout, is self-contained (host-side
writes stay under a temporary directory it removes on exit, and it removes
the Docker images it builds — `tenon-staged-tool-free`, `tenon-staged-go`,
`tenon-staged-python`, tag `acceptance` — the same way), and fails fast on
the first `FAIL` line. A clean run ends with:

```
PASS check-staged-images: tool-free, Go-only, Python-only, and TypeScript-only staged images all verified
```

## Recording acceptance evidence

Before tagging a release, run the gate and record, alongside the release
record (the tag's PR or the release notes):

- the tenon commit hash the gate ran against,
- the host platform (`uname -s`, `uname -m`) and Docker version
  (`docker version --format '{{.Server.Version}}'`),
- the full `PASS`/`FAIL` transcript the script printed.

Do not retain the intermediate Docker build logs or container output beyond
that transcript — the script's own temporary directory is removed on exit,
and no credential, live external effect, or retained artifact is produced by
this gate (staging itself is credential-free by construction, per
ADR 0012).
