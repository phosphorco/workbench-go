#!/usr/bin/env sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <monorepo-checkout>" >&2
  exit 2
fi

temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
GOCACHE=${GOCACHE:-/tmp/workbench-buildables-gocache} go build -o "$temporary/tsgo-verdict" ./pkl-spike/tsgo-verdict
GOCACHE=${GOCACHE:-/tmp/workbench-buildables-gocache} go build -o "$temporary/latency" ./pkl-spike/latency
"$temporary/latency" --root "$1" --native "$temporary/tsgo-verdict"
