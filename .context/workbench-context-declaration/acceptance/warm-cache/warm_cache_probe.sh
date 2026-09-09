#!/usr/bin/env bash

set -euo pipefail

binary=${WORKBENCH_WARM_BINARY:-/tmp/wctx-install.6uobrla_/bin/workbench}
installation=$(cd "$(dirname "$binary")/.." && pwd)
private_pkl="$installation/libexec/workbench/pkl"
runtime_lock="$installation/share/workbench/runtime-lock.json"

for path in "$binary" "$private_pkl" "$runtime_lock"; do
	[[ -f "$path" ]] || { echo "missing installed fixture: $path" >&2; exit 1; }
done
[[ "$(sha256sum "$binary" | awk '{print $1}')" == a4200299ca9bfe76477ec071e9d7df42c3ba01c9b6451925940cbdc7d1e1905c ]] || { echo "unexpected Workbench binary" >&2; exit 1; }
[[ "$(sha256sum "$private_pkl" | awk '{print $1}')" == 3180b62da95c0cad1d904e9bb6c5f4a8f9032413c21e53194bb91ff1ee5f3211 ]] || { echo "unexpected private Pkl" >&2; exit 1; }
[[ "$(sha256sum "$runtime_lock" | awk '{print $1}')" == 6f2a3df238ba7bc7e2210e7a26eb1ccac7556dbd9310b73f9159b95be59eb82c ]] || { echo "unexpected runtime lock" >&2; exit 1; }
command -v strace >/dev/null || { echo "strace is required" >&2; exit 1; }
command -v timeout >/dev/null || { echo "timeout is required" >&2; exit 1; }

root=$(mktemp -d /tmp/workbench-context-warm.XXXXXX)
trace_dir=$(mktemp -d /tmp/workbench-context-warm-traces.XXXXXX)
cleanup() {
	for proc in /proc/[0-9]*; do
		[[ -r "$proc/cmdline" ]] || continue
		cmd=$(tr '\0' ' ' <"$proc/cmdline" 2>/dev/null || true)
		case "$cmd" in *"context serve"*"--socket $root/runtime/context.sock"*) kill -TERM "${proc##*/}" 2>/dev/null || true ;; esac
	done
	deadline=$((SECONDS + 2))
	while ((SECONDS < deadline)); do
		found=0
		for proc in /proc/[0-9]*; do
			[[ -r "$proc/cmdline" ]] || continue
			cmd=$(tr '\0' ' ' <"$proc/cmdline" 2>/dev/null || true)
			case "$cmd" in *"context serve"*"--socket $root/runtime/context.sock"*) found=1 ;; esac
		done
		if ((found == 0)); then break; fi
		sleep 0.02
	done
	for proc in /proc/[0-9]*; do
		[[ -r "$proc/cmdline" ]] || continue
		cmd=$(tr '\0' ' ' <"$proc/cmdline" 2>/dev/null || true)
		case "$cmd" in *"context serve"*"--socket $root/runtime/context.sock"*) kill -KILL "${proc##*/}" 2>/dev/null || true ;; esac
	done
	rm -rf "$root"
}
trap cleanup EXIT

mkdir -p "$root/home" "$root/runtime" "$root/tmp"
printf '%s\n' 'warm cache probe' >"$root/README.md"
printf '%s\n' '---' 'root: true' 'docs:' '  - files: ["README.md"]' '    message: Warm cache guidance.' '---' >"$root/ai-context.md"
printf '%s\n' 'module context.rules' '' 'enabled: Boolean = true' >"$root/rules.pkl"
printf '%s\n' 'amends "workbench:context"' 'import "context-local:/rules.pkl" as rules' '' 'enabled = rules.enabled' 'scope = "subtree"' 'contributors {' '  ["project-guidance"] = new AiContext {}' '}' >"$root/workbench-context.pkl"
printf '%s\n' 'amends "workbench:context-home"' '' 'limits {' '  cache {' '    diskCapBytes = 262144' '  }' '  runtime {' '    idleTTLMs = 200' '  }' '}' >"$root/home/workbench-context.pkl"

export HOME="$root/home" TMPDIR="$root/tmp" PKL_EXECUTABLE="$private_pkl"
unset XDG_CONFIG_HOME XDG_CACHE_HOME XDG_RUNTIME_DIR CODEX_HOME || true
unset WORKBENCH_CONTEXT_HOME_CONFIG WORKBENCH_CONTEXT_RUNTIME_DIR WORKBENCH_CONTEXT_SOCKET WORKBENCH_CONTEXT_START_LOCK WORKBENCH_CONTEXT_SERVER_LOCK WORKBENCH_CONTEXT_CACHE_DIR || true

args=(context hook --harness claude
	--home-config "$root/home/workbench-context.pkl"
	--runtime-dir "$root/runtime"
	--socket "$root/runtime/context.sock"
	--start-lock "$root/runtime/context.start.lock"
	--server-lock "$root/runtime/context.server.lock"
	--cache-dir "$root/cache")

payload() {
	local session=$1
	printf '{"session_id":"%s","transcript_path":"%s","cwd":"%s","prompt_id":"%s","hook_event_name":"PostToolBatch","tool_calls":[{"tool_name":"Read","tool_use_id":"%s","tool_input":{"file_path":"%s"},"tool_response":"fixture README"}]}\n' "$session" "$root/does-not-exist-transcript.jsonl" "$root" "$session" "$session" "$root/README.md"
}

count_workers() {
	local trace=$1
	awk -v p="$private_pkl" 'index($0, "execve(\"" p "\",") { count++ } END { print count + 0 }' "$trace"
}

run_counted() {
	local label=$1 trace="$trace_dir/$1.strace"
	timeout --kill-after=2s 10s strace -f -qq -e trace=execve -o "$trace" "$binary" "${args[@]}" < <(payload "$label") >"$trace_dir/$label.stdout" 2>"$trace_dir/$label.stderr"
	[[ ! -s "$trace_dir/$label.stderr" ]] || { echo "$label stderr: $(cat "$trace_dir/$label.stderr")" >&2; exit 1; }
	count_workers "$trace"
}

expect_offer() {
	local label=$1
	grep -Fq 'Warm cache guidance.' "$trace_dir/$label.stdout" || { echo "$label did not offer guidance" >&2; exit 1; }
}

expect_silent() {
	local label=$1
	[[ ! -s "$trace_dir/$label.stdout" ]] || { echo "$label unexpectedly offered guidance" >&2; exit 1; }
}

cold=$(run_counted cold)
[[ "$cold" == 2 ]] || { echo "cold worker count=$cold, want 2 (home + project)" >&2; exit 1; }
expect_offer cold
for index in 1 2 3; do
	label="warm-$index"
	workers=$(run_counted "$label")
	[[ "$workers" == 0 ]] || { echo "$label worker count=$workers, want 0" >&2; exit 1; }
	expect_offer "$label"
done

sed 's/enabled: Boolean = true/enabled: Boolean = false/' "$root/rules.pkl" >"$root/rules.next"
mv "$root/rules.next" "$root/rules.pkl"
edited=$(run_counted edited)
[[ "$edited" == 1 ]] || { echo "edited worker count=$edited, want 1 (project import)" >&2; exit 1; }
expect_silent edited
edited_warm=$(run_counted edited-warm)
[[ "$edited_warm" == 0 ]] || { echo "edited warm worker count=$edited_warm, want 0" >&2; exit 1; }
expect_silent edited-warm

sed 's/enabled: Boolean = false/enabled: Boolean = true/' "$root/rules.pkl" >"$root/rules.next"
mv "$root/rules.next" "$root/rules.pkl"
restored=$(run_counted restored)
[[ "$restored" == 1 ]] || { echo "restored worker count=$restored, want 1 (project import)" >&2; exit 1; }
expect_offer restored
restored_warm=$(run_counted restored-warm)
[[ "$restored_warm" == 0 ]] || { echo "restored warm worker count=$restored_warm, want 0" >&2; exit 1; }
expect_offer restored-warm

durations=()
for index in $(seq 1 20); do
	started=$(date +%s%N)
	timeout --kill-after=2s 5s "$binary" "${args[@]}" < <(payload "timed-$index") >"$trace_dir/timed-$index.stdout" 2>"$trace_dir/timed-$index.stderr"
	finished=$(date +%s%N)
	[[ ! -s "$trace_dir/timed-$index.stderr" ]] || { echo "timed-$index emitted stderr" >&2; exit 1; }
	durations+=("$(( (finished - started) / 1000000 ))")
done
printf '%s\n' "${durations[@]}" | sort -n >"$trace_dir/durations.ms"
p95_line=$(( (95 * ${#durations[@]} + 99) / 100 ))
echo "fixture.binary=$binary"
echo "fixture.private_pkl=$private_pkl"
echo "fixture.runtime_lock=$runtime_lock"
echo "daemon_idle_ttl_ms=200 counted_phases_restart_after_idle=true timing_phase_target=warm_daemon"
echo "cold_workers=$cold warm_workers=0,0,0 edited_workers=$edited edited_warm_workers=$edited_warm restored_workers=$restored restored_warm_workers=$restored_warm"
echo "warm_samples=${#durations[@]} warm_durations_ms=$(paste -sd, "$trace_dir/durations.ms") warm_p95_ms=$(sed -n "${p95_line}p" "$trace_dir/durations.ms")"
echo "trace_dir=$trace_dir"
