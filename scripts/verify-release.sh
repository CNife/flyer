#!/usr/bin/env bash
# Verify a published GitHub Release: every checksum, then the Linux binary.
# Usage: scripts/verify-release.sh vX.Y.Z
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi

tag="$1"
repo="${GITHUB_REPOSITORY:-CNife/flyer}"
version="${tag#v}"

for tool in gh sha256sum; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "verify release: $tool not found" >&2
    exit 2
  fi
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

downloaded=0
for _ in $(seq 1 10); do
  if gh release download "$tag" --repo "$repo" --dir "$tmp" --clobber >/dev/null 2>&1; then
    downloaded=1
    break
  fi
  sleep 2
done
if [ "$downloaded" -ne 1 ]; then
  echo "verify release: could not download assets for $tag from $repo" >&2
  exit 1
fi

if [ ! -s "$tmp/checksums.txt" ]; then
  echo "verify release: checksums.txt is missing or empty" >&2
  exit 1
fi

(
  cd "$tmp"
  sha256sum -c checksums.txt
)

binary="$tmp/flyer_${version}_linux_amd64"
if [ ! -f "$binary" ]; then
  echo "verify release: $binary is missing" >&2
  exit 1
fi
chmod +x "$binary"
got="$("$binary" version)"
want="flyer v${version}"
if [ "$got" != "$want" ]; then
  echo "verify release: version = '$got', want '$want'" >&2
  exit 1
fi

echo "verify release: ok ($tag, $got)"
