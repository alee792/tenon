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
#   3. builds a two-stage image: the staged tree copied onto the documented
#      compatible base (ubuntu:24.04, docs/harness-images.md),
#   4. runs the built image with --network none as the staged non-root
#      identity (uid/gid 65532), verifies the staged tree offline via the
#      staged entrypoint's own `tenon stage verify`, and calls the probe
#      tool over MCP through `/opt/tenon/bin/tenon mcp serve`, asserting
#      isError:false and the expected output,
#   5. asserts language-exclusivity in the staged manifest, and
#   6. asserts tree hygiene (zero non-regular files).
#
# It also stages and images a tool-free agent (empty runtime record) and
# proves the TypeScript refusal (stage exits 1 with a named diagnostic and
# publishes no output directory).
#
# Deliberate deviation from images/'s own Dockerfiles: images/inputs.json
# pins every harness component (claude, codex) as
# "TODO-pin-before-first-build" (issue #19), so there is no buildable tenon
# harness image today to use as the `AS build` stage ADR 0012's two-stage
# example shows. This script instead performs that build stage's one
# relevant step — `tenon stage` — directly on the host with a tenon binary
# built from this checkout, then copies the result onto the documented base
# exactly as the two-stage Dockerfile's second stage does. It proves the
# staged tree and the final compatible base, not the harness image
# Dockerfiles themselves (those remain separately gated by issue #19 and
# publication authorization; see docs/harness-images.md).
#
# Usage: ./scripts/check-staged-images.sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

command -v docker >/dev/null 2>&1 || {
  echo "FAIL prerequisites: docker is required for the staged-image acceptance gate" >&2
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
cleanup() {
  rm -rf -- "$work"
}
trap cleanup EXIT HUP INT TERM

base_image="docker.io/library/ubuntu:24.04"

echo "==> building tenon from this checkout"
GOOS=linux GOARCH=amd64 go build -o "$work/tenon" ./cmd/tenon
tenon="$work/tenon"
echo "PASS build: tenon executable built for linux/amd64"

# final.Dockerfile is the two-stage journey's second stage, verbatim from
# docs/harness-images.md, with the build stage's output already materialized
# on disk by `tenon stage` (see the deviation note above): copy the staged
# tree's opt/, workspace/, and home/tenon/ onto the documented compatible
# base and create the non-root runtime identity.
final_dockerfile="$work/Dockerfile.final"
cat >"$final_dockerfile" <<EOF
FROM $base_image
COPY opt/ /opt/
COPY --chown=65532:65532 workspace/ /workspace/
COPY --chown=65532:65532 home/tenon/ /home/tenon/
RUN set -eu; \\
    groupadd --gid 65532 tenon; \\
    useradd --uid 65532 --gid 65532 --home-dir /home/tenon --shell /bin/sh --no-create-home --no-log-init tenon; \\
    mkdir -p /home/tenon /workspace; \\
    chown -R 65532:65532 /home/tenon /workspace
USER 65532:65532
WORKDIR /workspace
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
# none, as the staged non-root identity: proves runtime identity, zero
# non-regular staged entries, and the staged entrypoint's own offline
# verification of the artifact manifest, now inside the container.
check_hygiene_and_container_verify() {
  image=$1
  label=$2
  docker run --rm --network none --entrypoint /bin/sh "$image" -c '
    set -eu
    test "$(id -u):$(id -g)" = "65532:65532"
    test -z "$(find /opt /workspace /home -not -type d -not -type f -print -quit)"
    /opt/tenon/bin/tenon stage verify --artifact /opt/tenon/artifact.json >/dev/null
  ' >"$work/hygiene-$label.out" 2>&1 || {
    echo "FAIL hygiene($label): identity, non-regular-file, or in-container verify check failed" >&2
    cat "$work/hygiene-$label.out" >&2
    exit 1
  }
  echo "PASS hygiene($label): uid/gid 65532, zero non-regular staged entries, artifact verifies in-container"
}

# check_mcp_call runs the staged tenon's `mcp serve` inside the image,
# --network none, feeding a tools/list and one tools/call over stdin (a host
# file, not an in-container shell string — avoiding any escaping of the
# tool's own JSON arguments), and asserts the call succeeds (isError:false)
# with the expected output. `timeout` wraps the docker invocation on the
# host: mcp serve is a long-lived host process by design and only exits when
# stdin closes, which the input file already does.
check_mcp_call() {
  image=$1
  label=$2
  agent_name=$3
  tool_name=$4
  arguments=$5
  expect_grep=$6
  requests="$work/mcp-$label-requests.jsonl"
  cat >"$requests" <<EOF
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"$tool_name","arguments":$arguments}}
EOF
  if ! result=$(timeout 10 docker run --rm -i --network none \
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
      cp "$repo_root/examples/mixed-tools/go.mod" "$agent_dir/go.mod"
      mkdir -p "$agent_dir/tools/reverse"
      cp "$repo_root/examples/mixed-tools/tools/reverse/tool.go" "$agent_dir/tools/reverse/tool.go"
      ;;
    python)
      tool_name=wordcount
      call_arguments='{"text":"one two three"}'
      expect_grep='"words":3'
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
      grep -F '"go"' "$artifact" >/dev/null || {
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
  check_mcp_call "$image" "$lang" "$agent_name" "$tool_name" "$call_arguments" "$expect_grep"
done

printf '%s\n' "PASS check-staged-images: TypeScript refusal, tool-free, Go-only, and Python-only staged images all verified"
