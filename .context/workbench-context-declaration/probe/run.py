#!/usr/bin/env python3
"""Bounded feasibility probes for the Pkl declaration evaluator route.

This intentionally does not implement a production evaluator or cache. It runs
the installed, hash-checked Pkl binary through a probe-only pkl-go helper and
prints JSON evidence. Expected red witnesses are reported as data, not failures
of this harness.
"""

from __future__ import annotations

import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import resource
import selectors
import signal
import shutil
import statistics
import subprocess
import tempfile
import time


PROBE = Path(__file__).resolve().parent
ROOT = PROBE.parents[2]
LOCK = ROOT / "release" / "runtime-lock.json"
HELPER_SOURCE = PROBE / "reader_probe.go"
NO_SOURCE_DEPTH = 12
NO_SOURCE_SAMPLES = 200
HOME_COLD_SAMPLES = 3
SHARED_CAP_BYTES = 1024 * 1024
OUTPUT_BUDGET_BYTES = 1024 * 1024
DIAGNOSTIC_BUDGET_BYTES = 64 * 1024
INPUT_BUDGET_BYTES = 4 * 1024 * 1024
DEADLINE_MS = 3000
DATA_LIMITS_MIB = (128, 256, 512, 1024)
MEMORY_DIAGNOSTIC_CAPTURE_BYTES = 64 * 1024


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def command_output(command: list[str], *, timeout: float = 10.0) -> str:
    completed = subprocess.run(command, cwd=ROOT, text=True, capture_output=True, timeout=timeout)
    if completed.returncode != 0:
        raise RuntimeError(f"{command!r} failed ({completed.returncode}): {completed.stderr.strip()}")
    return completed.stdout.strip()


def resolve_pkl() -> tuple[Path, str]:
    shim = shutil.which("pkl")
    if not shim:
        raise RuntimeError("pkl is not installed on PATH")
    version = command_output([shim, "--version"])
    mise = shutil.which("mise")
    if mise:
        installed = command_output([mise, "which", "pkl"])
        path = Path(installed).resolve()
    else:
        path = Path(shim).resolve()
    return path, version


def build_helper(build_root: Path) -> Path:
    binary = build_root / "reader-probe"
    env = os.environ.copy()
    env["GOCACHE"] = str(build_root / "go-cache")
    completed = subprocess.run(
        ["go", "build", "-trimpath", "-o", str(binary), str(HELPER_SOURCE)],
        cwd=ROOT,
        env=env,
        text=True,
        capture_output=True,
        timeout=60,
    )
    if completed.returncode != 0:
        raise RuntimeError(f"go build failed: {completed.stderr.strip()}")
    return binary


def parse_helper(binary: Path, pkl: Path, mode: str, *, timeout: float = 12.0, extra: list[str] | None = None, allow_failure: bool = False) -> dict:
    command = [str(binary), mode, str(pkl)] + (extra or [])
    started = time.perf_counter()
    completed = subprocess.run(command, cwd=ROOT, text=True, capture_output=True, timeout=timeout)
    wall_ms = (time.perf_counter() - started) * 1000
    if completed.returncode != 0 and allow_failure:
        return {
            "mode": mode,
            "ok": False,
            "returncode": completed.returncode,
            "stderr": completed.stderr[-4000:],
            "wall_ms": wall_ms,
            "process_red_witness": True,
        }
    if completed.returncode != 0:
        raise RuntimeError(f"{command!r} failed ({completed.returncode}): {completed.stderr.strip()} {completed.stdout.strip()}")
    lines = [line for line in completed.stdout.splitlines() if line.strip()]
    if not lines:
        raise RuntimeError(f"{command!r} returned no JSON")
    result = json.loads(lines[-1])
    result["wall_ms"] = wall_ms
    return result


def bounded_process(command: list[str], *, data_limit_mib: int, timeout: float = 8.0, stdout_limit: int = OUTPUT_BUDGET_BYTES, stderr_limit: int = MEMORY_DIAGNOSTIC_CAPTURE_BYTES) -> dict:
    """Run one child with a child-only Linux DATA limit and bounded captures."""
    limit = data_limit_mib * 1024 * 1024

    def set_data_limit() -> None:
        resource.setrlimit(resource.RLIMIT_DATA, (limit, limit))

    started = time.perf_counter()
    process = subprocess.Popen(
        command,
        cwd=ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
        preexec_fn=set_data_limit,
    )
    assert process.stdout is not None and process.stderr is not None
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ, "stdout")
    selector.register(process.stderr, selectors.EVENT_READ, "stderr")
    counts = {"stdout": 0, "stderr": 0}
    previews = {"stdout": bytearray(), "stderr": bytearray()}
    capped = {"stdout": False, "stderr": False}
    killed = False

    def kill_group() -> None:
        nonlocal killed
        if killed:
            return
        killed = True
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    try:
        while selector.get_map():
            if time.perf_counter() - started > timeout:
                kill_group()
            for key, _ in selector.select(timeout=0.05):
                stream = key.fileobj
                label = key.data
                chunk = os.read(stream.fileno(), 8192)
                if not chunk:
                    selector.unregister(stream)
                    stream.close()
                    continue
                counts[label] += len(chunk)
                limit_for_stream = stdout_limit if label == "stdout" else stderr_limit
                if len(previews[label]) < 1024:
                    previews[label].extend(chunk[: 1024 - len(previews[label])])
                if counts[label] > limit_for_stream:
                    capped[label] = True
                    kill_group()
    finally:
        selector.close()
    try:
        returncode = process.wait(timeout=2.0)
    except subprocess.TimeoutExpired:
        kill_group()
        returncode = process.wait(timeout=2.0)
    return {
        "returncode": returncode,
        "wall_ms": (time.perf_counter() - started) * 1000,
        "stdout_bytes": counts["stdout"],
        "stderr_bytes": counts["stderr"],
        "stdout_preview": bytes(previews["stdout"]).decode("utf-8", "replace"),
        "stderr_preview": bytes(previews["stderr"]).decode("utf-8", "replace"),
        "stdout_capped": capped["stdout"],
        "stderr_capped": capped["stderr"],
        "child_killed_by_probe": killed,
        "data_limit_mib": data_limit_mib,
    }


def memory_limit_probe(pkl: Path) -> dict:
    controls = {}
    for limit in DATA_LIMITS_MIB:
        controls[str(limit)] = bounded_process(
            [str(pkl), "eval", "-x", "1 + 1", "pkl:base"],
            data_limit_mib=limit,
            timeout=8.0,
        )
    stress = bounded_process(
        [str(pkl), "eval", "-x", '("x".repeat(200000000)).length', "pkl:base"],
        data_limit_mib=DATA_LIMITS_MIB[0],
        timeout=8.0,
    )
    return {
        "platform": "Linux child-only RLIMIT_DATA; not a Pkl -Xmx heap flag",
        "controls": controls,
        "allocation_failure": stress,
        "allocation_failure_witness": stress["returncode"] != 0 and stress["data_limit_mib"] == 128,
        "disclosure": "RLIMIT_DATA bounds this process resource on this Linux host; it does not establish a portable heap or total-RSS bound, and stdout may be emitted before the evaluator exits nonzero.",
    }


def percentile(values: list[float], p: float) -> float:
    ordered = sorted(values)
    if not ordered:
        return 0.0
    index = min(len(ordered) - 1, max(0, int(round((p / 100) * (len(ordered) - 1)))))
    return ordered[index]


def discover_project_sources(cwd: Path) -> list[Path]:
    """Bounded no-source route: discovery only, no toolchain/cache acquisition."""
    found: list[Path] = []
    current = cwd
    for _ in range(NO_SOURCE_DEPTH + 1):
        candidate = current / "workbench-context.pkl"
        if candidate.exists():
            found.append(candidate)
        if current.parent == current:
            break
        current = current.parent
    return found


def no_source_probe(root: Path) -> dict:
    cwd = root
    before = sorted(str(path.relative_to(root)) for path in root.rglob("*"))
    samples: list[float] = []
    for _ in range(NO_SOURCE_SAMPLES):
        started = time.perf_counter()
        found = discover_project_sources(cwd)
        samples.append((time.perf_counter() - started) * 1000)
        if found:
            raise RuntimeError("no-source fixture discovered an unexpected declaration")
    after = sorted(str(path.relative_to(root)) for path in root.rglob("*"))
    return {
        "samples": len(samples),
        "depth": NO_SOURCE_DEPTH,
        "p95_ms": percentile(samples, 95),
        "max_ms": max(samples),
        "process_started": False,
        "cache_write_bytes": 0,
        "writes": sorted(set(after) - set(before)),
    }


def wrapper_script(directory: Path, pkl: Path, mode: str) -> Path:
    script = directory / f"blocked-{mode}.sh"
    real = str(pkl)
    if mode == "version":
        body = f"#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then sleep 2; exec {sh_quote(real)} --version; fi\nexec {sh_quote(real)} \"$@\"\n"
    elif mode == "handshake":
        body = f"#!/bin/sh\nif [ \"$1\" = \"server\" ]; then sleep 2; fi\nexec {sh_quote(real)} \"$@\"\n"
    elif mode == "owned":
        body = f"#!/bin/sh\nif [ \"$1\" = \"eval\" ]; then sleep 2; fi\nexec {sh_quote(real)} \"$@\"\n"
    elif mode == "server":
        body = f"#!/bin/sh\nif [ \"$1\" = \"server\" ]; then sleep 2; fi\nexec {sh_quote(real)} \"$@\"\n"
    elif mode == "blocked-stdin":
        body = f"#!/bin/sh\nif [ \"$1\" = \"server\" ]; then trap '' HUP INT TERM; sleep 10; fi\nexec {sh_quote(real)} \"$@\"\n"
    else:
        raise ValueError(mode)
    script.write_text(body)
    script.chmod(0o755)
    return script


def sh_quote(value: str) -> str:
    return "'" + value.replace("'", "'\\''") + "'"


def run_cold_samples(binary: Path, pkl: Path, samples: int) -> list[dict]:
    return [parse_helper(binary, pkl, "cold") for _ in range(samples)]


def concurrent_cold(binary: Path, pkl: Path, cap_root: Path) -> dict:
    cap_root.mkdir(parents=True, exist_ok=True)
    filler = cap_root / "trace-filler.bin"
    filler.write_bytes(b"t" * (SHARED_CAP_BYTES - 4096))
    before = sum(path.stat().st_size for path in cap_root.iterdir())
    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
        futures = [executor.submit(parse_helper, binary, pkl, "cold") for _ in range(8)]
        results = [future.result() for future in futures]
    wall_ms = (time.perf_counter() - started) * 1000
    after = sum(path.stat().st_size for path in cap_root.iterdir())
    return {
        "workers": 8,
        "nearly_full_cap_bytes": SHARED_CAP_BYTES,
        "bytes_before": before,
        "bytes_after": after,
        "wall_ms": wall_ms,
        "cold_wall_p95_ms": percentile([item["wall_ms"] for item in results], 95),
        "child_peak_tree_rss_kb_max": max(item.get("child_peak_tree_rss_kb", 0) for item in results),
        "results_ok": all(item.get("ok", False) for item in results),
        "cache_integration_exercised": False,
        "limit": "This only proves eight cold processes can run beside a nearly-full fixture; it does not prove shared trace/snapshot accounting.",
    }


def concurrent_owned(binary: Path, pkl: Path) -> dict:
    """Eight fresh owned-server calls; no cache or shared-capacity path is used."""
    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
        futures = [
            executor.submit(parse_helper, binary, pkl, "owned-server", extra=["normal", "3000"], timeout=8.0)
            for _ in range(8)
        ]
        results = [future.result() for future in futures]
    wall_ms = (time.perf_counter() - started) * 1000
    joined = all(item.get("ok", False) and item.get("joined", False) and not item.get("child_pids_after_close") for item in results)
    return {
        "workers": 8,
        "wall_ms": wall_ms,
        "total_target_ms": 2000,
        "total_target_met": wall_ms <= 2000,
        "results_ok": all(item.get("ok", False) for item in results),
        "joined_cleanup": joined,
        "results": results,
        "cache_integration_exercised": False,
        "limit": "Eight fresh owned server processes only; this proves evaluator lifecycle throughput and joined cleanup, not shared trace/snapshot accounting.",
    }


def main() -> int:
    pkl, pkl_version = resolve_pkl()
    lock = json.loads(LOCK.read_text())
    expected = lock["runtimes"]["pkl"]["artifacts"]["linux-x64"]["sha256"]
    actual_hash = sha256(pkl)
    if pkl_version != f"Pkl {lock['runtimes']['pkl']['version']} (Linux 6.17.0-1020-azure, native)" and not pkl_version.startswith(f"Pkl {lock['runtimes']['pkl']['version']} "):
        raise RuntimeError(f"installed Pkl version {pkl_version!r} does not match runtime lock")

    with tempfile.TemporaryDirectory(prefix="workbench-eval-probe-") as temporary:
        runtime = Path(temporary)
        binary = build_helper(runtime)
        fixture_root = runtime / "fixture"
        cwd = fixture_root
        for index in range(NO_SOURCE_DEPTH):
            cwd = cwd / f"d{index:02d}"
        cwd.mkdir(parents=True)
        no_source = no_source_probe(fixture_root)
        cold = run_cold_samples(binary, pkl, HOME_COLD_SAMPLES)
        warm = parse_helper(binary, pkl, "warm")
        concurrent = concurrent_cold(binary, pkl, runtime / "shared-cap")
        owned_concurrent = concurrent_owned(binary, pkl)
        freshness = parse_helper(binary, pkl, "file-freshness", timeout=3.0)
        memory = memory_limit_probe(pkl)

        version_wrapper = wrapper_script(runtime, pkl, "version")
        handshake_wrapper = wrapper_script(runtime, pkl, "handshake")
        owned_wrapper = wrapper_script(runtime, pkl, "owned")
        server_wrapper = wrapper_script(runtime, pkl, "server")
        blocked_stdin_wrapper = wrapper_script(runtime, pkl, "blocked-stdin")
        blocked_version = parse_helper(binary, version_wrapper, "lifecycle", timeout=8.0, allow_failure=True)
        blocked_handshake = parse_helper(binary, handshake_wrapper, "lifecycle", timeout=8.0, allow_failure=True)
        owned_cancellation = parse_helper(binary, owned_wrapper, "owned-cancel", extra=["100"], timeout=3.0)
        owned_server = parse_helper(binary, pkl, "owned-server", extra=["normal", "3000"], timeout=8.0)
        owned_server_handshake = parse_helper(binary, server_wrapper, "owned-server", extra=["handshake", "100"], timeout=3.0)
        owned_server_output = parse_helper(binary, pkl, "owned-server", extra=["output", "5000"], timeout=12.0)
        owned_server_diagnostic = parse_helper(binary, pkl, "owned-server", extra=["diagnostic", "5000"], timeout=12.0)
        owned_server_blocked_stdin = parse_helper(binary, blocked_stdin_wrapper, "owned-server", extra=["output", "100"], timeout=3.0)
        owned_server_blocked_write = parse_helper(binary, blocked_stdin_wrapper, "owned-write-cancel", extra=["100"], timeout=3.0)
        oversized_output = parse_helper(binary, pkl, "oversized-output", timeout=12.0)
        oversized_diagnostic = parse_helper(binary, pkl, "oversized-diagnostic", timeout=12.0)

    report = {
        "toolchain": {
            "go": command_output(["go", "version"]),
            "pkl_go": command_output(["go", "list", "-m", "-f", "{{.Path}} {{.Version}}", "github.com/apple/pkl-go"]),
            "pkl_path": str(pkl),
            "pkl_version": pkl_version,
            "pkl_sha256": actual_hash,
            "runtime_lock_pkl_version": lock["runtimes"]["pkl"]["version"],
            "runtime_lock_linux_x64_sha256": expected,
            "pkl_hash_matches_runtime_lock": actual_hash == expected,
            "runtime_lock_sha256": sha256(LOCK),
            "reader_probe_source_sha256": sha256(HELPER_SOURCE),
            "distribution_exemplar": {
                "source": "internal/distribution/distribution.go",
                "layout": "libexec/workbench/pkl",
                "lock": str(LOCK.relative_to(ROOT)),
            },
        },
        "bounds": {
            "deadline_ms": DEADLINE_MS,
            "input_bytes": INPUT_BUDGET_BYTES,
            "output_bytes": OUTPUT_BUDGET_BYTES,
            "diagnostic_bytes": DIAGNOSTIC_BUDGET_BYTES,
            "memory_data_limits_mib": list(DATA_LIMITS_MIB),
            "memory_diagnostic_capture_bytes": MEMORY_DIAGNOSTIC_CAPTURE_BYTES,
            "no_source_p95_target_ms": 30,
            "warm_p95_target_ms": 100,
            "eight_cold_total_target_ms": 2000,
        },
        "workload": {
            "no_source": no_source,
            "home_only_cold": {
                "samples": len(cold),
                "wall_p95_ms": percentile([item["wall_ms"] for item in cold], 95),
                "evaluator_p95_ms": percentile([item["elapsed_ms"] for item in cold], 95),
                "reader_calls": [item.get("reader_calls", 0) for item in cold],
                "child_peak_tree_rss_kb_max": max(item.get("child_peak_tree_rss_kb", 0) for item in cold),
                "results_ok": all(item.get("ok", False) for item in cold),
            },
            "warm": warm,
            "eight_concurrent_cold_misses": concurrent,
            "eight_concurrent_owned_misses": owned_concurrent,
            "file_freshness": freshness,
            "memory_limit": memory,
        },
        "adversarial": {
            "blocked_version_discovery": blocked_version,
            "blocked_handshake": blocked_handshake,
            "owned_process_cancellation": owned_cancellation,
            "owned_server": owned_server,
            "owned_server_blocked_handshake": owned_server_handshake,
            "owned_server_oversized_output": owned_server_output,
            "owned_server_oversized_diagnostic": owned_server_diagnostic,
            "owned_server_blocked_stdin": owned_server_blocked_stdin,
            "owned_server_blocked_stdin_write": owned_server_blocked_write,
            "oversized_output": {
                **oversized_output,
                "budget_exceeded": oversized_output.get("output_bytes", 0) > OUTPUT_BUDGET_BYTES,
            },
            "oversized_diagnostic": {
                **oversized_diagnostic,
                "budget_exceeded": oversized_diagnostic.get("diagnostic_bytes", 0) > DIAGNOSTIC_BUDGET_BYTES,
            },
        },
        "conclusions": {
            "no_source_is_process_and_cache_free_in_probe": no_source["process_started"] is False and no_source["cache_write_bytes"] == 0,
            "reader_exact_bytes_captured_at_read": warm.get("reader_calls") == 1 and bool(warm.get("captures")),
            "reader_importing_origin_proven": False,
            "dummy_cache_proves_trace_integration": False,
            "current_pkl_go_version_path_bounded": not blocked_version.get("process_red_witness", False) and blocked_version.get("new_evaluator_elapsed_ms", 0) <= blocked_version.get("context_timeout_ms", 100) * 2,
            "current_pkl_go_handshake_joined": not blocked_handshake.get("process_red_witness", False) and blocked_handshake.get("manager_close_elapsed_ms", 0) <= 1000 and not blocked_handshake.get("child_pids_after_close"),
            "owned_process_cancellation_joined": owned_cancellation.get("joined", False) and not owned_cancellation.get("child_pids_after_close"),
            "owned_server_normal_protocol_green": owned_server.get("ok", False) and owned_server.get("joined", False) and not owned_server.get("child_pids_after_close"),
            "owned_server_handshake_joined": owned_server_handshake.get("ok", False) and owned_server_handshake.get("joined", False),
            "owned_server_output_rejected_before_allocation": owned_server_output.get("ok", False) and owned_server_output.get("protocol_rejected", False),
            "owned_server_diagnostic_rejected_before_allocation": owned_server_diagnostic.get("ok", False) and owned_server_diagnostic.get("protocol_rejected", False),
            "owned_server_blocked_stdin_joined": owned_server_blocked_stdin.get("ok", False) and owned_server_blocked_stdin.get("joined", False),
            "owned_server_blocked_stdin_write_joined": owned_server_blocked_write.get("ok", False) and owned_server_blocked_write.get("joined", False),
            "owned_eight_total_under_two_seconds": owned_concurrent.get("total_target_met", False) and owned_concurrent.get("results_ok", False) and owned_concurrent.get("joined_cleanup", False),
            "file_freshness_witness_green": freshness.get("ok", False),
            "memory_data_limit_witness_green": memory.get("allocation_failure_witness", False),
            "current_output_limit_enforced": oversized_output.get("output_bytes", 0) <= OUTPUT_BUDGET_BYTES,
            "current_diagnostic_limit_enforced": oversized_diagnostic.get("diagnostic_bytes", 0) <= DIAGNOSTIC_BUDGET_BYTES,
        },
        "seams": {
            "reader": "Resolve each designation once under the authority root, reject non-regular or over-bound files, read exact bytes once, clone bytes at ownership boundary, hash those bytes, and retain designation + canonical target in the dependency manifest. The installed pkl-go ModuleReader callback exposes designation only; accepted closure is ImportingOrigin.Known=false, never a fabricated parser edge. Freshness must re-resolve under authority and use an owned rooted-open/revalidation seam later.",
            "cache": "Snapshot key includes authority/origin/schema/evaluator identity and exact dependency manifest. Validate exact captured bytes before reuse; publish through one shared reservation covering temp bytes and metadata. This probe models no cache writer and cannot prove integration with contexttrace.",
            "lifecycle": "Composition owns an evaluator process handle and deadline; version discovery, server handshake, output/diagnostic decode, cancellation, and joined process-group cleanup must each be bounded. pkl-go v0.14.0 alone does not satisfy that hook contract. A hidden worker-exec may apply child-only RLIMIT_DATA before syscall.Exec of the pinned server; this probe does not implement that production entry.",
        },
    }
    required = {
        "runtime_hash_matches_lock": report["toolchain"]["pkl_hash_matches_runtime_lock"],
        "owned_normal_protocol": report["conclusions"]["owned_server_normal_protocol_green"],
        "owned_handshake_joined": report["conclusions"]["owned_server_handshake_joined"],
        "owned_output_rejected": report["conclusions"]["owned_server_output_rejected_before_allocation"],
        "owned_diagnostic_rejected": report["conclusions"]["owned_server_diagnostic_rejected_before_allocation"],
        "owned_process_cancellation_joined": report["conclusions"]["owned_process_cancellation_joined"],
        "owned_blocked_stdin_write_joined": report["conclusions"]["owned_server_blocked_stdin_write_joined"],
        "owned_eight_total_under_two_seconds": report["conclusions"]["owned_eight_total_under_two_seconds"],
        "file_freshness_witness": report["conclusions"]["file_freshness_witness_green"],
        "memory_data_limit_witness": report["conclusions"]["memory_data_limit_witness_green"],
    }
    report["verification"] = {
        "required_probe_cases": required,
        "passed": all(required.values()),
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if report["verification"]["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
