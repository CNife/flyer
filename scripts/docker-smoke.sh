#!/usr/bin/env bash
# Start a flyer image, publish through its HTTP API, and fetch the page.
# Usage: scripts/docker-smoke.sh <image>
# Set CONTAINER_ENGINE=podman to use Podman locally; CI uses Docker.
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <image>" >&2
  exit 2
fi

engine="${CONTAINER_ENGINE:-docker}"
image="$1"
name="flyer-smoke-$$-${RANDOM}"

if ! command -v "$engine" >/dev/null 2>&1; then
  echo "docker smoke: $engine not found" >&2
  exit 2
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "docker smoke: curl not found" >&2
  exit 2
fi

cleanup() {
  "$engine" rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
  echo "docker smoke: $*" >&2
  "$engine" logs "$name" >&2 || true
  exit 1
}

"$engine" run -d --name "$name" -p 127.0.0.1::8787 "$image" >/dev/null \
  || fail "could not start $image"

port_line="$("$engine" port "$name" 8787/tcp 2>/dev/null | head -n1 || true)"
port="${port_line##*:}"
port="${port%$'\r'}"
if [ -z "$port" ] || [ "$port" = "$port_line" ]; then
  fail "could not determine published port"
fi

base="http://127.0.0.1:$port"
ready=0
for _ in $(seq 1 60); do
  if curl -fsS "$base/health" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! "$engine" inspect -f '{{.State.Running}}' "$name" 2>/dev/null | grep -q true; then
    fail "container exited before becoming healthy"
  fi
  sleep 0.5
done
[ "$ready" -eq 1 ] || fail "timed out waiting for /health"

admin_key="$("$engine" logs "$name" 2>&1 | grep -oE 'fly_[A-Za-z0-9_-]+' | head -n1 || true)"
[ -n "$admin_key" ] || fail "could not read the admin key from container logs"

response="$(curl -fsS -X POST "$base/api/pages" \
  -H "Authorization: Bearer $admin_key" \
  -H "Content-Type: application/json" \
  -d '{"html":"<h1>container smoke</h1>","title":"smoke"}')"
slug="$(printf '%s' "$response" | sed -n 's/.*"slug":"\([^"]*\)".*/\1/p')"
[ -n "$slug" ] || fail "publish response has no slug: $response"

page="$(curl -fsS "$base/$slug")"
case "$page" in
  *"container smoke"*) ;;
  *) fail "published page did not contain the smoke marker" ;;
esac

echo "docker smoke: ok ($image, $base/$slug)"
