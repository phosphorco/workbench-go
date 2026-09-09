#!/usr/bin/env bash
set -euo pipefail

lock_file="release/runtime-lock.json"
platform=""
output=""
env_file="${GITHUB_ENV:-}"

usage() {
  printf 'usage: %s --platform PLATFORM --output PATH [--lock PATH] [--env-file PATH]\n' "$0" >&2
}

while (($#)); do
  case "$1" in
    --lock)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      lock_file="$2"
      shift 2
      ;;
    --platform)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      platform="$2"
      shift 2
      ;;
    --output)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      output="$2"
      shift 2
      ;;
    --env-file)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      env_file="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

if [[ -z "$platform" || -z "$output" ]]; then
  usage
  exit 2
fi

url=$(jq -er --arg platform "$platform" '.runtimes.pkl.artifacts[$platform].url' "$lock_file")
expected_sha=$(jq -er --arg platform "$platform" '.runtimes.pkl.artifacts[$platform].sha256' "$lock_file")
mkdir -p "$(dirname "$output")"
curl --fail --location --silent --show-error "$url" --output "$output"
printf '%s  %s\n' "$expected_sha" "$output" | shasum -a 256 --check
chmod 0755 "$output"

output_directory=$(cd "$(dirname "$output")" && pwd)
absolute_output="$output_directory/$(basename "$output")"
if [[ -n "$env_file" ]]; then
  printf 'PKL_EXECUTABLE=%s\n' "$absolute_output" >> "$env_file"
fi
printf 'PKL_EXECUTABLE=%s\n' "$absolute_output"
