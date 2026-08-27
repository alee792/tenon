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

For each supported authored-tool language (Go and Python today; TypeScript
stays refused pending its own rendering spike, issue #16) the script:

1. writes a minimal probe agent carrying one authored tool in that language,
2. runs `tenon stage` to produce a complete runnable tree,
3. copies that tree onto the documented compatible base (`ubuntu:24.04`,
   [`docs/harness-images.md`](harness-images.md)) — the two-stage journey's
   second stage,
4. runs the built image with `--network none` as the staged non-root
   identity (uid/gid 65532), verifies the staged tree offline through
   `tenon stage verify` — the same call the staged entrypoint makes before
   ever starting a harness — and calls the probe tool over MCP through
   `/opt/tenon/bin/tenon mcp serve`, asserting `isError:false` and the exact
   expected output,
5. asserts staged-manifest language exclusivity (a Go-only image records no
   Python interpreter and stages no `cpython`; a Python-only image records
   the pinned interpreter identity and stages it), and
6. asserts staged-tree hygiene: zero non-regular files anywhere under
   `/opt`, `/workspace`, or `/home`.

It also stages and images a tool-free agent, proving the empty runtime
record (`runtime.bundled: false`, no staged language runtime), and proves
the TypeScript refusal end to end: `tenon stage` exits 1, names the stable
`stage.tools.runtime-unsupported` diagnostic, and publishes no output
directory.

This is a proof of the staged tree and the documented compatible base, not
of a published tenon harness image: `images/inputs.json` pins every harness
component (`claude`, `codex`) as `TODO-pin-before-first-build` (issue #19),
so there is no buildable harness image today to build the two-stage
journey's first stage from. The script performs that first stage's one
relevant step — `tenon stage` — directly on the host instead; the script's
own header comment records this deviation and the reason. It does not
supersede [`docs/harness-images.md`](harness-images.md)'s own publication
gating, which still governs the harness images themselves.

## When to run it

Before every exact `vX.Y.Z` tenon release tag, and any time staging
(`internal/stage`), authored-tool preparation (`internal/toolruntime`), or
the documented compatible base
([`docs/harness-images.md`](harness-images.md)) changes in a way that could
affect what actually runs inside a container. `./scripts/check.sh` proves
tenon's own logic; it cannot prove a real container boots the staged tree
and serves a real tool call over MCP — only this gate does.

## How to run it

On a Linux/amd64 host with Docker, Go, and `uv` installed:

```sh
./scripts/check-staged-images.sh
```

It builds tenon from the current checkout, is self-contained (writes only
under a temporary directory it cleans up), and fails fast on the first
`FAIL` line. A clean run ends with:

```
PASS check-staged-images: TypeScript refusal, tool-free, Go-only, and Python-only staged images all verified
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
