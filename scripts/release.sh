#!/bin/sh
# Build the release archives and checksum manifest for an exact vX.Y.Z tag.
#
# The artifact names are contract, not convenience: ADR 0005, as widened by
# ADR 0022, binds tenon_X.Y.Z_<os>_<arch>.tar.gz, each carrying exactly one
# `tenon` executable at the archive root, plus one tenon_X.Y.Z_SHA256SUMS
# covering every archive. A user verifies that manifest before trusting the
# binary, so this script writes those names literally rather than deriving them
# from a tool's defaults.
#
# Output is byte-reproducible for a given tag and Go toolchain: -trimpath drops
# build paths, every timestamp comes from the tag's commit rather than the
# clock, tar records a fixed order and uid/gid, and gzip -n omits its own name
# and mtime header.
#
# Requires GNU tar (--sort, --mtime) and GNU touch. Both are present on the
# ubuntu runner this is meant for; a macOS laptop has bsdtar and will fail the
# check below rather than emit a subtly different archive.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

if ! tar --version 2>/dev/null | head -n 1 | grep -q "GNU tar"; then
  echo "release: GNU tar is required (--sort/--mtime); run this in CI" >&2
  exit 1
fi

tag=${1:-$(git describe --tags --exact-match)}
case "$tag" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *)
    echo "release: need an exact vX.Y.Z tag, got '$tag'" >&2
    exit 1
    ;;
esac
version=${tag#v}

# Every timestamp in the output derives from the tagged commit, never from the
# clock, so rebuilding the same tag later produces the same bytes.
source_date=$(git log -1 --format=%ct "$tag")

out=$repo_root/dist
rm -rf "$out"
mkdir -p "$out"

for target in darwin/arm64 linux/amd64 linux/arm64; do
  goos=${target%/*}
  goarch=${target#*/}
  stage=$(mktemp -d)

  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath \
      -ldflags "-s -w -X github.com/alee792/tenon/internal/version.Version=$version" \
      -o "$stage/tenon" ./cmd/tenon

  touch -d "@$source_date" "$stage/tenon"
  tar --sort=name --owner=0 --group=0 --numeric-owner \
      --mtime="@$source_date" --format=gnu \
      -C "$stage" -cf - tenon |
    gzip -9n >"$out/tenon_${version}_${goos}_${goarch}.tar.gz"

  rm -rf "$stage"
done

(cd "$out" && sha256sum tenon_"$version"_*.tar.gz >"tenon_${version}_SHA256SUMS")

echo "release: wrote $out/tenon_${version}_SHA256SUMS"
