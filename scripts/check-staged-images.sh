#!/bin/sh
# check-staged-images.sh: the manual container acceptance gate for staged
# agent images (ADR 0012, ADR 0021). It is NOT run by CI, exactly like the
# `//go:build harness` integration tests against live harness binaries
# (docs/product-spec.md's "Known limitations"): it requires Docker and a
# Linux/amd64 host, and must be run by a human before an exact `vX.Y.Z`
# release tag, per docs/staged-acceptance.md.
#
# For each supported authored-tool language (go, python today; typescript
# stays refused pending its own rendering spike, issue #16 — see the
# refusal check below, and add a case here once it stages) this:
#
#   1. writes a minimal probe agent carrying one authored tool in that
#      language,
#   2. runs `tenon stage` to produce a complete runnable tree,
#   3. builds a final image: the staged tree copied onto the documented
#      compatible base (ubuntu:24.04, docs/harness-images.md) — see the
#      Dockerfile deviations noted below,
#   4. runs the built image with --network none as the staged non-root
#      identity (uid/gid 65532): verifies the staged tree offline via
#      `tenon stage verify` run directly, separately proves the staged
#      entrypoint's own fail-closed `--verify-only` path
#      (internal/stage/entrypoint.go) through the image's default
#      ENTRYPOINT, and calls the probe tool over MCP through
#      `/opt/tenon/bin/tenon mcp serve`, asserting isError:false and the
#      expected output,
#   5. asserts language-exclusivity in the staged manifest, and
#   6. asserts tree hygiene (zero non-regular files, and that /workspace and
#      /home/tenon are writable by the runtime identity).
#
# It also stages and images a tool-free agent (empty runtime record) and
# proves the TypeScript refusal (stage exits 1 with a named diagnostic and
# publishes no output directory).
#
# Deliberate deviations from ADR 0012's two-stage example and images/'s own
# Dockerfiles, both rooted in the same cause: images/inputs.json pins every
# harness component (claude, codex) as "TODO-pin-before-first-build" (issue
# #19), so there is no buildable tenon harness image today to use as the
# `AS build` stage. This script instead performs that build stage's one
# relevant step — `tenon stage` — directly on the host with a tenon binary
# built from this checkout, and its own final.Dockerfile (built below) is
# NOT verbatim from docs/harness-images.md's two-stage example:
#
#   - it drops the `COPY --from=build /etc/ssl/certs/ca-certificates.crt
#     ...` line: that line copies the CA bundle from the build stage (a
#     harness image FROM ubuntu, which has it via apt), and this gate has
#     no such build stage. This is genuinely unavoidable host-side, not a
#     shortcut: none of this gate's checks make an outbound TLS connection,
#     so its absence does not weaken what is proven, but a transcript
#     reader must know the compatible-base contract's certificate clause
#     (docs/harness-images.md, "Certificate bundle") was NOT exercised by
#     this run;
#   - it therefore also carries `SSL_CERT_FILE` pointed at a bundle that
#     does not exist in the built image (documented in the Dockerfile
#     itself, below);
#   - `RUN` (create the non-root identity) runs before the `COPY` of the
#     staged tree, matching the documented ordering, not after it;
#   - `ENTRYPOINT ["/opt/tenon/bin/agent-entrypoint"]` is present exactly
#     as documented, so the entrypoint's own verification path is real and
#     checked below — every other check in this script overrides it with
#     `--entrypoint` for its own direct command;
#   - the RUN step also removes a stray default "ubuntu" account (and its
#     /home/ubuntu) if the pulled base image carries one: recent
#     docker.io/library/ubuntu:24.04 builds ship a non-root uid-1000
#     "ubuntu" user with a mode-700 home directory, which the staged
#     identity (uid 65532) cannot traverse, tripping the hygiene walk
#     below on a directory that has nothing to do with the staged tree.
#     This is base-image drift the pinned digest (once `target.base.digest`
#     is resolved, issue #19) would freeze against; until then this keeps
#     the gate honest about what it actually staged instead of failing on
#     an unrelated stock account.
#
# This proves the staged tree and the documented compatible base (short of
# the certificate clause above), not the harness image Dockerfiles
# themselves (those remain separately gated by issue #19 and publication
# authorization; see docs/harness-images.md).
#
# Usage: ./scripts/check-staged-images.sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

command -v docker >/dev/null 2>&1 || {
  echo "FAIL prerequisites: docker is required for the staged-image acceptance gate" >&2
  exit 1
}
docker info >/dev/null 2>&1 || {
  echo "FAIL prerequisites: the docker daemon is not reachable (docker info failed)" >&2
  exit 1
}
command -v go >/dev/null 2>&1 || {
  echo "FAIL prerequisites: go is required to build tenon and to stage a Go-tool agent" >&2
  exit 1
}
command -v uv >/dev/null 2>&1 || {
  echo "FAIL prerequisites: uv is required to stage a Python-tool agent (ADR 0021)" >&2
  exit 1
}
command -v timeout >/dev/null 2>&1 || {
  echo "FAIL prerequisites: timeout (GNU coreutils) is required to bound each container MCP call" >&2
  exit 1
}
[ "$(uname -s)-$(uname -m)" = "Linux-x86_64" ] || {
  echo "FAIL prerequisites: this gate requires linux/amd64 (staging is platform-honest, ADR 0012)" >&2
  exit 1
}

work=$(mktemp -d "${TMPDIR:-/tmp}/tenon-check-staged-images.XXXXXX")
# The three image tags this gate ever builds, fixed ahead of time so cleanup
# can remove them unconditionally (a missing tag is a silent no-op) without
# tracking what actually got built on a failed run.
built_images="tenon-staged-tool-free:acceptance tenon-staged-go:acceptance tenon-staged-python:acceptance"
cleanup() {
  rm -rf -- "$work"
  # shellcheck disable=SC2086
  docker image rm -f $built_images >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

base_image="docker.io/library/ubuntu:24.04"

echo "==> building tenon from this checkout"
GOOS=linux GOARCH=amd64 go build -o "$work/tenon" ./cmd/tenon
tenon="$work/tenon"
echo "PASS build: tenon executable built for linux/amd64"

# final.Dockerfile is the two-stage journey's second stage, adapted from
# docs/harness-images.md — NOT verbatim; see the header comment above for
# every deviation and why. The build stage's output is already materialized
# on disk by `tenon stage` (host-side, per the deviation note): copy the
# staged tree's opt/, workspace/, and home/tenon/ onto the documented
# compatible base and create the non-root runtime identity.
final_dockerfile="$work/Dockerfile.final"
cat >"$final_dockerfile" <<EOF
FROM $base_image
ENV HOME=/home/tenon \\
    PATH=/opt/tenon/bin:/opt/tenon/harness/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \\
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
# NOT copied here: /etc/ssl/certs/ca-certificates.crt. The documented
# two-stage Dockerfile copies it --from=build, a build stage this gate does
# not have (see the header comment). SSL_CERT_FILE above therefore points
# at a bundle this image does not carry; nothing this gate checks makes an
# outbound TLS connection, so this does not weaken the proof, but the
# certificate clause of the compatible-base contract is NOT exercised here.
RUN set -eu; \\
    if id ubuntu >/dev/null 2>&1; then userdel --remove ubuntu 2>/dev/null || rm -rf /home/ubuntu; fi; \\
    groupadd --gid 65532 tenon; \\
    useradd --uid 65532 --gid 65532 --home-dir /home/tenon --shell /bin/sh --no-create-home --no-log-init tenon; \\
    mkdir -p /home/tenon /workspace; \\
    chown -R 65532:65532 /home/tenon /workspace
COPY opt/ /opt/
COPY --chown=65532:65532 workspace/ /workspace/
COPY --chown=65532:65532 home/tenon/ /home/tenon/
USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/opt/tenon/bin/agent-entrypoint"]
EOF

# write_instructions gives a probe agent the minimal valid instructions.md
# tenon requires: frontmatter with a description, plus a non-empty body.
write_instructions() {
  dir=$1
  desc=$2
  mkdir -p "$dir"
  cat >"$dir/instructions.md" <<EOF
---
description: $desc
---

Acceptance probe agent for scripts/check-staged-images.sh (ADR 0012, ADR
0021). Not an authored agent for real use.
EOF
}

# stage_agent runs `tenon stage` for one probe agent and returns (via the
# global staged_dir) the published tree, fatal on failure.
stage_agent() {
  agent_dir=$1
  label=$2
  staged_dir="$work/staged-$label"
  if ! "$tenon" stage "$agent_dir" --harness claude --output "$staged_dir" >"$work/stage-$label.out" 2>&1; then
    echo "FAIL stage($label): tenon stage exited nonzero" >&2
    cat "$work/stage-$label.out" >&2
    exit 1
  fi
  echo "PASS stage($label): tenon stage produced a published tree"
}

# verify_staged_tree runs the same offline verification the staged
# entrypoint invokes, against the tree still at its host-side --prefix.
verify_staged_tree() {
  label=$1
  artifact="$staged_dir/opt/tenon/artifact.json"
  if ! "$tenon" stage verify --artifact "$artifact" --prefix "$staged_dir" >"$work/verify-$label.out" 2>&1; then
    echo "FAIL stage-verify($label): the freshly staged tree does not verify" >&2
    cat "$work/verify-$label.out" >&2
    exit 1
  fi
  echo "PASS stage-verify($label): the staged tree verifies clean on the host"
}

# check_hygiene_and_container_verify runs inside the built image, --network
# none, as the staged non-root identity, entrypoint overridden to a plain
# shell: proves runtime identity, zero non-regular staged entries, that
# /workspace and /home/tenon are actually writable by that identity (stage.
# Verify never checks Dirs, so this is the only proof of that), and a
# direct in-container `tenon stage verify` of the artifact manifest. The
# staged entrypoint's OWN verification path is a separate, later check
# (check_entrypoint_verify) — this one bypasses the entrypoint entirely.
check_hygiene_and_container_verify() {
  image=$1
  label=$2
  docker run --rm --network none --entrypoint /bin/sh "$image" -c '
    set -eu
    test "$(id -u):$(id -g)" = "65532:65532"
    # A failing find (not merely a nonempty result) must abort loudly rather
    # than have its exit status swallowed by wrapping it in test -z "$(...)":
    # this plain assignment aborts under set -e if find itself errors.
    non_regular=$(find /opt /workspace /home -not -type d -not -type f -print -quit)
    [ -z "$non_regular" ]
    touch /workspace/.check-staged-images-probe
    touch /home/tenon/.check-staged-images-probe
    /opt/tenon/bin/tenon stage verify --artifact /opt/tenon/artifact.json >/dev/null
  ' >"$work/hygiene-$label.out" 2>&1 || {
    echo "FAIL hygiene($label): identity, non-regular-file, writability, or in-container verify check failed" >&2
    cat "$work/hygiene-$label.out" >&2
    exit 1
  }
  echo "PASS hygiene($label): uid/gid 65532, zero non-regular staged entries, /workspace and /home/tenon writable, artifact verifies in-container"
}

# check_entrypoint_verify proves the staged entrypoint's OWN fail-closed
# verification path (internal/stage/entrypoint.go), not a stand-in for it:
# it runs the image through its default ENTRYPOINT
# (/opt/tenon/bin/agent-entrypoint) with --verify-only, the entrypoint's
# documented verify-and-exit mode, and asserts a clean exit 0 with no
# harness handoff attempted.
check_entrypoint_verify() {
  image=$1
  label=$2
  docker run --rm --network none "$image" --verify-only >"$work/entrypoint-verify-$label.out" 2>&1 || {
    echo "FAIL entrypoint-verify($label): the staged entrypoint's --verify-only path failed" >&2
    cat "$work/entrypoint-verify-$label.out" >&2
    exit 1
  }
  echo "PASS entrypoint-verify($label): the staged agent-entrypoint verified the tree and exited 0 via --verify-only"
}

# check_mcp_call runs the staged tenon's `mcp serve` inside the image,
# --network none, feeding a tools/list and one tools/call over stdin (a host
# file, not an in-container shell string — avoiding any escaping of the
# tool's own JSON arguments), and asserts the call succeeds (isError:false)
# with the expected output. `timeout` wraps the docker invocation on the
# host: mcp serve is a long-lived host process by design and only exits when
# stdin closes, which the input file already does. The caller supplies the
# timeout in seconds; Python's leg needs longer than Go's for a cold read of
# the staged ~82MB standalone CPython closure.
check_mcp_call() {
  image=$1
  label=$2
  agent_name=$3
  tool_name=$4
  arguments=$5
  expect_grep=$6
  timeout_seconds=$7
  requests="$work/mcp-$label-requests.jsonl"
  cat >"$requests" <<EOF
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"$tool_name","arguments":$arguments}}
EOF
  if ! result=$(timeout "$timeout_seconds" docker run --rm -i --network none \
      --entrypoint /opt/tenon/bin/tenon "$image" \
      mcp serve "/opt/tenon/agents/$agent_name" --workspace /workspace --harness claude \
      <"$requests" 2>"$work/mcp-$label.err"); then
    echo "FAIL mcp-call($label): tenon mcp serve exited nonzero" >&2
    cat "$work/mcp-$label.err" >&2
    exit 1
  fi
  printf '%s\n' "$result" | grep -F "\"name\":\"$tool_name\"" >/dev/null || {
    echo "FAIL mcp-call($label): tools/list did not carry $tool_name: $result" >&2
    exit 1
  }
  printf '%s\n' "$result" | grep -F '"isError":false' >/dev/null || {
    echo "FAIL mcp-call($label): tools/call did not report isError:false: $result" >&2
    exit 1
  }
  printf '%s\n' "$result" | grep -F "$expect_grep" >/dev/null || {
    echo "FAIL mcp-call($label): tools/call output did not carry $expect_grep: $result" >&2
    exit 1
  }
  echo "PASS mcp-call($label): $tool_name listed and called over MCP, isError:false, output matched"
}

# ---------------------------------------------------------------------------
# TypeScript refusal (ADR 0021): staging refuses a TypeScript-bearing agent
# with a named diagnostic and publishes no output directory. No Docker
# needed for this leg — the refusal happens before any image is built.
# ---------------------------------------------------------------------------
echo "==> typescript refusal"
ts_agent="$work/agents/typescript-probe"
write_instructions "$ts_agent" "TypeScript-tool refusal probe."
mkdir -p "$ts_agent/tools"
cp "$repo_root/examples/mixed-tools/deno.json" "$ts_agent/deno.json"
cp "$repo_root/examples/mixed-tools/deno.lock" "$ts_agent/deno.lock"
cp "$repo_root/examples/mixed-tools/tools/shout.ts" "$ts_agent/tools/shout.ts"
ts_out="$work/staged-typescript"
set +e
"$tenon" stage "$ts_agent" --harness claude --output "$ts_out" --diagnostics jsonl >"$work/stage-typescript.out" 2>&1
ts_code=$?
set -e
if [ "$ts_code" -eq 0 ]; then
  echo "FAIL typescript-refusal: tenon stage unexpectedly succeeded for a TypeScript-bearing agent" >&2
  exit 1
fi
grep -F '"id":"stage.tools.runtime-unsupported"' "$work/stage-typescript.out" >/dev/null || {
  echo "FAIL typescript-refusal: the stable stage.tools.runtime-unsupported diagnostic was not reported" >&2
  cat "$work/stage-typescript.out" >&2
  exit 1
}
if [ -e "$ts_out" ]; then
  echo "FAIL typescript-refusal: a refused stage must publish no output directory" >&2
  exit 1
fi
echo "PASS typescript-refusal: stage exits 1, names stage.tools.runtime-unsupported, publishes nothing"

# ---------------------------------------------------------------------------
# Tool-free agent: proves the empty runtime record and otherwise runs the
# same hygiene/verify proof as a language leg.
# ---------------------------------------------------------------------------
echo "==> tool-free probe"
free_agent="$work/agents/tool-free-probe"
write_instructions "$free_agent" "Tool-free acceptance probe."
stage_agent "$free_agent" "tool-free"
verify_staged_tree "tool-free"

artifact="$staged_dir/opt/tenon/artifact.json"
grep -F '"bundled": false' "$artifact" >/dev/null || {
  echo "FAIL exclusivity(tool-free): runtime.bundled must be false" >&2
  exit 1
}
if grep -F '"languages"' "$artifact" >/dev/null; then
  echo "FAIL exclusivity(tool-free): runtime.languages must be absent for a tool-free agent" >&2
  exit 1
fi
if [ -n "$(find "$staged_dir/opt/tenon/runtimes" -mindepth 1 -print -quit)" ]; then
  echo "FAIL exclusivity(tool-free): /opt/tenon/runtimes must be empty for a tool-free agent" >&2
  exit 1
fi
echo "PASS exclusivity(tool-free): the manifest carries an empty runtime record, and /opt/tenon/runtimes is empty"

free_image="tenon-staged-tool-free:acceptance"
docker build --platform linux/amd64 --tag "$free_image" --file "$final_dockerfile" "$staged_dir" >"$work/build-tool-free.out" 2>&1 || {
  echo "FAIL build(tool-free): building the final image failed" >&2
  cat "$work/build-tool-free.out" >&2
  exit 1
}
echo "PASS build(tool-free): the final image built onto $base_image"
check_hygiene_and_container_verify "$free_image" "tool-free"
check_entrypoint_verify "$free_image" "tool-free"

# ---------------------------------------------------------------------------
# Language legs: go, python today. Add typescript here once its rendering
# lands (ADR 0021, issue #16) — everything below is already keyed by
# language name so a new case is the only change needed.
# ---------------------------------------------------------------------------
for lang in go python; do
  echo "==> $lang probe"
  agent_name="$lang-probe"
  agent_dir="$work/agents/$agent_name"
  write_instructions "$agent_dir" "$lang-tool acceptance probe."

  case "$lang" in
    go)
      tool_name=reverse
      call_arguments='{"text":"tenon"}'
      expect_grep='"reversed":"nonet"'
      mcp_timeout=10
      cp "$repo_root/examples/mixed-tools/go.mod" "$agent_dir/go.mod"
      mkdir -p "$agent_dir/tools/reverse"
      cp "$repo_root/examples/mixed-tools/tools/reverse/tool.go" "$agent_dir/tools/reverse/tool.go"
      ;;
    python)
      tool_name=wordcount
      call_arguments='{"text":"one two three"}'
      expect_grep='"words":3'
      # A cold container reads the staged ~82MB standalone CPython closure
      # off disk for the first time; Go's compiled host binary is much
      # smaller, so only Python needs the longer bound.
      mcp_timeout=30
      mkdir -p "$agent_dir/tools"
      cp "$repo_root/examples/mixed-tools/pyproject.toml" "$agent_dir/pyproject.toml"
      cp "$repo_root/examples/mixed-tools/uv.lock" "$agent_dir/uv.lock"
      cp "$repo_root/examples/mixed-tools/tools/wordcount.py" "$agent_dir/tools/wordcount.py"
      ;;
  esac

  stage_agent "$agent_dir" "$lang"
  verify_staged_tree "$lang"

  artifact="$staged_dir/opt/tenon/artifact.json"
  case "$lang" in
    go)
      # Scoped to the runtime.languages array (the "languages" key line plus
      # its following entries), not a whole-file substring match: a
      # whole-file '"go"' grep would also pass against a mixed manifest that
      # happened to carry python, so it would not actually prove go-only.
      grep -A3 '"languages"' "$artifact" | grep -F '"go"' >/dev/null || {
        echo "FAIL exclusivity(go): runtime.languages must record go" >&2
        exit 1
      }
      if grep -F '"interpreters"' "$artifact" >/dev/null; then
        echo "FAIL exclusivity(go): a Go-only manifest must carry no interpreters record" >&2
        exit 1
      fi
      if [ -n "$(find "$staged_dir/opt/tenon/runtimes" -iname cpython -print -quit)" ]; then
        echo "FAIL exclusivity(go): a Go-only closure must carry no cpython" >&2
        exit 1
      fi
      echo "PASS exclusivity(go): the manifest records go with no interpreters entry, and no cpython is staged"
      ;;
    python)
      grep -F '"python": "cpython-' "$artifact" >/dev/null || {
        echo "FAIL exclusivity(python): runtime.interpreters must record the pinned cpython identity" >&2
        exit 1
      }
      if [ ! -d "$staged_dir/opt/tenon/runtimes/tools" ] || [ -z "$(find "$staged_dir/opt/tenon/runtimes" -iname cpython -print -quit)" ]; then
        echo "FAIL exclusivity(python): a Python closure must carry a staged cpython interpreter" >&2
        exit 1
      fi
      echo "PASS exclusivity(python): the manifest records the pinned cpython identity, and cpython is staged"
      ;;
  esac

  image="tenon-staged-$lang:acceptance"
  docker build --platform linux/amd64 --tag "$image" --file "$final_dockerfile" "$staged_dir" >"$work/build-$lang.out" 2>&1 || {
    echo "FAIL build($lang): building the final image failed" >&2
    cat "$work/build-$lang.out" >&2
    exit 1
  }
  echo "PASS build($lang): the final image built onto $base_image"

  check_hygiene_and_container_verify "$image" "$lang"
  check_entrypoint_verify "$image" "$lang"
  check_mcp_call "$image" "$lang" "$agent_name" "$tool_name" "$call_arguments" "$expect_grep" "$mcp_timeout"
done

printf '%s\n' "PASS check-staged-images: TypeScript refusal, tool-free, Go-only, and Python-only staged images all verified"
