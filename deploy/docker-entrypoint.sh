#!/bin/sh
set -eu

data_dir="${FLYER_DATA_DIR:-/data}"
base_url="${FLYER_BASE_URL:-http://localhost:8787}"
listen="${FLYER_LISTEN:-0.0.0.0:8787}"

mkdir -p "$data_dir"

if [ ! -f "$data_dir/config.toml" ]; then
  echo "flyer: initializing $data_dir" >&2
  flyer init-server --dir "$data_dir" --base-url "$base_url" --listen "$listen"
fi

exec flyer serve --dir "$data_dir"
