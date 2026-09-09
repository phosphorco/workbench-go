#!/bin/sh
set -eu

pkl=''
max_data=''
record=''
write_stderr=''
stderr_bytes=''
fail_after=0
ignore_close=0
block=0

while [ "$#" -gt 0 ]; do
  case "$1" in
  --record)
    record=$2
    shift 2
    ;;
  --write-stderr)
    write_stderr=$2
    shift 2
    ;;
  --stderr-bytes)
    stderr_bytes=$2
    shift 2
    ;;
  --fail-after)
    fail_after=1
    shift
    ;;
  --ignore-close)
    ignore_close=1
    shift
    ;;
  --block)
    block=1
    shift
    ;;
  --pkl)
    pkl=$2
    shift 2
    ;;
  --max-data-bytes)
    max_data=$2
    shift 2
    ;;
  server)
    shift
    break
    ;;
  *)
    echo "unexpected worker argument: $1" >&2
    exit 64
    ;;
  esac
done

if [ -n "$record" ]; then
  printf 'pkl=%s max-data-bytes=%s stderr-bytes=%s\n' "$pkl" "$max_data" "$stderr_bytes" >"$record"
fi
if [ "$block" -eq 1 ]; then
  sleep 30
  exit 0
fi
if [ -n "$write_stderr" ]; then
  printf '%s\n' "$write_stderr" >&2
fi
if [ -n "$stderr_bytes" ]; then
  dd if=/dev/zero bs=1 count="$stderr_bytes" 2>/dev/null | tr '\000' x >&2
fi

if [ "$fail_after" -eq 1 ]; then
  "$pkl" server
  status=$?
  if [ "$ignore_close" -eq 1 ]; then
    sleep 30
  fi
  exit 99
fi

"$pkl" server
status=$?
if [ "$ignore_close" -eq 1 ]; then
  sleep 30
fi
exit "$status"
