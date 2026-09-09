#!/usr/bin/env python3
"""Capture the real Workbench context hook boundary for acceptance QA.

The runner is deliberately a small, provider-specific acceptance instrument,
not a provider engine.  It records the bytes crossing the native hook and
provider boundaries and emits the capture-manifest contract consumed by the
  offline exporter.  Controlled Claude is deterministic; live Claude and
  Codex are explicit native adapters requiring caller-supplied binaries and
  credentials.
"""

from __future__ import annotations

import argparse
import base64
import contextlib
import fcntl
import hashlib
import http.server
import inspect
import json
import os
from pathlib import Path
import re
import secrets
import select
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import threading
import time
from typing import Any


EXPECTED_EVENTS = {
    "PostToolBatch",
    "PostToolUse",
    "PostToolUseFailure",
    "SessionStart",
    "Stop",
}
UNKNOWN_CODES = {
    "not-captured",
    "provider-omitted",
    "unavailable",
    "truncated",
    "ambiguous",
    "unmatched",
    "source-mutated",
}
MAX_CODEX_JSON_LINE = 2 * 1024 * 1024
MAX_CODEX_EVIDENCE = 10 * 1024 * 1024
CODEX_THREAD_PATH = "/usr/bin:/bin"
QA_IDLE_TTL_MS = 300000
MAX_RUNTIME_PROCESSES = 4096
RUNTIME_CLEANUP_WAIT_SECONDS = 3.0
MAX_PROC_CMDLINE_BYTES = 64 * 1024
MAX_PROC_STAT_BYTES = 64 * 1024
MAX_PROC_FDS = 256
MAX_PROC_NET_UNIX_BYTES = 4 * 1024 * 1024


class RunnerError(RuntimeError):
    pass


class RuntimeScanIncomplete(RunnerError):
    pass


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def json_bytes(value: object) -> bytes:
    return (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()


def b64(value: bytes) -> str:
    return base64.b64encode(value).decode("ascii")


def limited_bytes(value: bytes, limit: int) -> tuple[bytes, bool]:
    if len(value) <= limit:
        return value, True
    return value[:limit], False


def write_bytes(path: Path, value: bytes, limit: int) -> dict[str, object]:
    retained, complete = limited_bytes(value, limit)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(retained)
    return {
        "path": str(path),
        "sizeBytes": len(retained),
        "sha256": sha256_bytes(retained),
        "complete": complete,
        "bounded": not complete,
    }


def copy_bounded_file(source: Path, target: Path, limit: int) -> dict[str, object]:
    target.parent.mkdir(parents=True, exist_ok=True)
    retained = bytearray()
    observed = 0
    with source.open("rb") as stream, target.open("wb") as destination:
        while True:
            chunk = stream.read(min(64 * 1024, limit + 1 - len(retained)))
            if not chunk:
                break
            observed += len(chunk)
            if len(retained) < limit:
                retained.extend(chunk[: limit - len(retained)])
            if len(retained) >= limit:
                extra = stream.read(1)
                if extra:
                    observed += len(extra)
                break
        destination.write(retained)
    return {"path": str(target), "sizeBytes": len(retained), "sha256": sha256_bytes(bytes(retained)), "complete": observed <= limit, "bounded": True}


def terminate_group(pid: int) -> bool:
    if pid <= 0:
        return True

    def live_members() -> bool:
        try:
            os.killpg(pid, 0)
        except ProcessLookupError:
            return False
        except PermissionError:
            return True
        proc = Path("/proc")
        for entry in proc.iterdir():
            if not entry.name.isdigit():
                continue
            member = int(entry.name)
            try:
                if os.getpgid(member) != pid:
                    continue
                fields = (entry / "stat").read_text(encoding="utf-8").split(") ", 1)
                state = fields[1].split(" ", 1)[0] if len(fields) == 2 else "?"
                if state != "Z":
                    return True
            except (FileNotFoundError, ProcessLookupError, PermissionError, OSError):
                continue
        return False

    def alive() -> bool:
        return live_members()

    if not alive():
        return True
    with contextlib.suppress(ProcessLookupError):
        os.killpg(pid, signal.SIGTERM)
    deadline = time.monotonic() + 3
    while alive() and time.monotonic() < deadline:
        time.sleep(0.02)
    if alive():
        with contextlib.suppress(ProcessLookupError):
            os.killpg(pid, signal.SIGKILL)
        deadline = time.monotonic() + 3
        while alive() and time.monotonic() < deadline:
            time.sleep(0.02)
    return not alive()


def drain_stream(stream: Any, limit: int, result: dict[str, object], forward: Any = None) -> None:
    retained = bytearray()
    total = 0
    try:
        while True:
            chunk = stream.read(64 * 1024)
            if not chunk:
                break
            total += len(chunk)
            if len(retained) < limit:
                retained.extend(chunk[: limit - len(retained)])
            if forward is not None:
                forward.write(chunk)
                forward.flush()
    finally:
        result["bytes"] = bytes(retained)
        result["total"] = total
        result["complete"] = total <= limit


def run_process(
    argv: list[str],
    *,
    env: dict[str, str],
    cwd: Path,
    timeout: float,
    max_output: int,
    input_bytes: bytes | None = None,
) -> dict[str, object]:
    if input_bytes is not None and len(input_bytes) > max_output:
        raise RunnerError("subprocess stdin exceeds the configured bounded input limit")
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=cwd,
        env=env,
        stdin=subprocess.PIPE if input_bytes is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    streams: dict[str, dict[str, object]] = {"stdout": {}, "stderr": {}}
    readers = [
        threading.Thread(target=drain_stream, args=(process.stdout, max_output, streams["stdout"]), daemon=True),
        threading.Thread(target=drain_stream, args=(process.stderr, max_output, streams["stderr"]), daemon=True),
    ]
    for reader in readers:
        reader.start()
    writer_done = threading.Event()
    writer_error: list[str] = []

    def write_input() -> None:
        try:
            if input_bytes is not None and process.stdin is not None:
                process.stdin.write(input_bytes)
                process.stdin.close()
        except (BrokenPipeError, OSError) as error:
            writer_error.append(str(error))
        finally:
            writer_done.set()

    writer = None
    if input_bytes is not None:
        writer = threading.Thread(target=write_input, daemon=True)
        writer.start()
    timed_out = False
    deadline = started + timeout
    while process.poll() is None:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            timed_out = True
            terminate_group(process.pid)
            break
        try:
            process.wait(timeout=min(0.1, remaining))
        except subprocess.TimeoutExpired:
            continue
    group_gone = terminate_group(process.pid)
    with contextlib.suppress(OSError):
        if process.stdin is not None:
            process.stdin.close()
    try:
        process.wait(timeout=3)
    except subprocess.TimeoutExpired:
        group_gone = False
    for reader in readers:
        reader.join(timeout=3)
    if writer is not None:
        writer.join(timeout=3)
    drain_joined = all(not reader.is_alive() for reader in readers) and (writer is None or not writer.is_alive())
    stdout = streams["stdout"].get("bytes", b"")
    stderr = streams["stderr"].get("bytes", b"")
    return {
        "argv": argv,
        "pid": process.pid,
        "exit": process.returncode,
        "timed_out": timed_out,
        "process_group_gone": group_gone,
        "duration_ms": round((time.monotonic() - started) * 1000, 1),
        "stdout": stdout,
        "stderr": stderr,
        "stdout_complete": streams["stdout"].get("complete", False),
        "stderr_complete": streams["stderr"].get("complete", False),
        "stdout_total_bytes": streams["stdout"].get("total", 0),
        "stderr_total_bytes": streams["stderr"].get("total", 0),
        "drain_joined": drain_joined,
        "writer_error": writer_error,
    }


def require_process_cleanup(result: dict[str, object], evidence: Path, label: str, limit: int) -> None:
    if result.get("process_group_gone") and result.get("drain_joined") and not result.get("timed_out") and not result.get("writer_error"):
        return
    reasons = []
    if result.get("timed_out"):
        reasons.append("deadline expired")
    if result.get("writer_error"):
        reasons.append("bounded stdin writer failed")
    if not result.get("process_group_gone"):
        reasons.append("owned process group remained")
    if not result.get("drain_joined"):
        reasons.append("bounded drain did not join")
    failure = {"component": label, "processGroupGone": result.get("process_group_gone"), "drainJoined": result.get("drain_joined"), "timedOut": result.get("timed_out"), "writerError": result.get("writer_error", []), "reason": "; ".join(reasons) or "process cleanup failed"}
    write_observation(evidence / "cleanup-failure.jsonl", [failure], limit)
    raise RunnerError(f"{label} cleanup was incomplete; retained cleanup-failure.jsonl")


def require_bounded_command(result: dict[str, object], evidence: Path, label: str, limit: int) -> None:
    require_process_cleanup(result, evidence, label, limit)
    if result.get("exit") != 0:
        write_observation(evidence / "command-failure.jsonl", [command_observation(label, result, limit)], limit)
        raise RunnerError(f"{label} exited {result.get('exit')}; retained command-failure.jsonl")


def remove_owned(path: Path, evidence: Path, limit: int, label: str) -> None:
    try:
        path.unlink(missing_ok=True)
    except OSError as error:
        write_observation(evidence / "cleanup-failure.jsonl", [{"component": label, "reason": f"owned temporary file removal failed: {error}"}], limit)
        raise RunnerError(f"{label} cleanup failed; retained cleanup-failure.jsonl") from error


def write_observation(path: Path, records: list[dict[str, object]], limit: int) -> dict[str, object]:
    path.parent.mkdir(parents=True, exist_ok=True)
    written = 0
    complete = True
    with path.open("wb") as stream:
        for record in records:
            line = (json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n").encode()
            remaining = limit - written
            if remaining <= 0:
                complete = False
                continue
            if len(line) > remaining:
                stream.write(line[:remaining])
                written += remaining
                complete = False
                continue
            stream.write(line)
            written += len(line)
    data = path.read_bytes()
    return {"path": str(path), "sizeBytes": len(data), "sha256": sha256_bytes(data), "complete": complete, "bounded": True}


def context_flags(paths: dict[str, Path]) -> list[str]:
    return [
        "--home-config",
        str(paths["home_config"]),
        "--runtime-dir",
        str(paths["runtime"]),
        "--socket",
        str(paths["socket"]),
        "--start-lock",
        str(paths["start_lock"]),
        "--server-lock",
        str(paths["server_lock"]),
        "--cache-dir",
        str(paths["cache"]),
    ]


def _proc_record(pid: int, socket_inode: int | None = None) -> dict[str, object] | None:
    proc = Path("/proc") / str(pid)
    try:
        with (proc / "stat").open("rb") as stream:
            raw_stat = stream.read(MAX_PROC_STAT_BYTES + 1)
        if len(raw_stat) > MAX_PROC_STAT_BYTES:
            raise RuntimeScanIncomplete(f"/proc/{pid}/stat exceeded the bounded process census limit")
        marker = raw_stat.rfind(b") ")
        if marker < 0:
            return None
        fields = raw_stat[marker + 2 :].split()
        if len(fields) < 20:
            return None
        with (proc / "cmdline").open("rb") as stream:
            command = stream.read(MAX_PROC_CMDLINE_BYTES + 1)
        if len(command) > MAX_PROC_CMDLINE_BYTES:
            raise RuntimeScanIncomplete(f"/proc/{pid}/cmdline exceeded the bounded process census limit")
        argv = [part.decode("utf-8", errors="surrogateescape") for part in command.split(b"\0") if part]
        exe = os.readlink(proc / "exe")
        cwd = os.path.realpath(proc / "cwd")
        record: dict[str, object] = {"pid": pid, "ppid": int(fields[1]), "pgrp": int(fields[2]), "startTime": fields[19].decode(), "argv": argv, "exe": os.path.realpath(exe), "cwd": cwd}
        if socket_inode is not None:
            held = False
            fds = []
            for fd in (proc / "fd").iterdir():
                fds.append(fd)
                if len(fds) > MAX_PROC_FDS:
                    raise RuntimeScanIncomplete(f"/proc/{pid}/fd exceeded the bounded process census limit")
            for fd in fds:
                try:
                    if os.readlink(fd) == f"socket:[{socket_inode}]":
                        held = True
                        break
                except (FileNotFoundError, PermissionError, OSError):
                    continue
            record["socketHeld"] = held
        return record
    except (FileNotFoundError, PermissionError, OSError, ValueError):
        return None


def _proc_table(socket_inode: int | None = None) -> dict[int, dict[str, object]]:
    if sys.platform != "linux":
        raise RunnerError("exact Workbench runtime ownership scan requires Linux /proc")
    table: dict[int, dict[str, object]] = {}
    entries = []
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        entries.append(entry)
        if len(entries) > MAX_RUNTIME_PROCESSES:
            raise RuntimeScanIncomplete(f"/proc census exceeded {MAX_RUNTIME_PROCESSES} processes")
    for entry in entries:
        record = _proc_record(int(entry.name), socket_inode)
        if record is not None:
            table[int(entry.name)] = record
    return table


def _unix_socket_inode(path: Path) -> int | None:
    target = str(path)
    try:
        with Path("/proc/net/unix").open("rb") as stream:
            raw = stream.read(MAX_PROC_NET_UNIX_BYTES + 1)
        if len(raw) > MAX_PROC_NET_UNIX_BYTES:
            raise RuntimeScanIncomplete("/proc/net/unix exceeded the bounded socket census limit")
        for line in raw.decode("utf-8", errors="replace").splitlines()[1:]:
            fields = line.split()
            if len(fields) >= 7 and fields[-1] == target:
                return int(fields[6])
    except RuntimeScanIncomplete:
        raise
    except (FileNotFoundError, OSError, ValueError):
        return None
    return None


def _runtime_snapshot(workbench: Path, paths: dict[str, Path], anchor: dict[str, object] | None = None) -> dict[str, object]:
    try:
        return _runtime_snapshot_unchecked(workbench, paths, anchor)
    except RuntimeScanIncomplete as error:
        return {"status": "incomplete", "scanComplete": False, "reason": str(error), "socketExists": paths["socket"].exists(), "socketPath": str(paths["socket"]), "socketInode": None, "candidates": [], "socketOwners": [], "members": [], "processCount": None}


def _runtime_snapshot_unchecked(workbench: Path, paths: dict[str, Path], anchor: dict[str, object] | None = None) -> dict[str, object]:
    socket_inode: int | None = None
    socket_exists = paths["socket"].exists()
    if socket_exists:
        try:
            socket_stat = paths["socket"].stat()
            if not stat.S_ISSOCK(socket_stat.st_mode):
                return {"status": "mismatch", "reason": "fixture socket path exists but is not a Unix socket", "socketExists": True, "socketPath": str(paths["socket"]), "processes": []}
            socket_inode = _unix_socket_inode(paths["socket"])
        except OSError as error:
            return {"status": "mismatch", "reason": f"fixture socket could not be inspected: {error}", "socketExists": True, "socketPath": str(paths["socket"]), "processes": []}
    table = _proc_table()
    expected = [str(workbench.resolve()), "context", "serve", *context_flags(paths)]
    expected_exe = str(workbench.resolve())
    expected_cwd = str(paths["project"].resolve())
    candidates = [record for record in table.values() if record.get("argv") == expected and record.get("exe") == expected_exe and record.get("cwd") == expected_cwd]
    if socket_inode is not None:
        candidates = [(_proc_record(int(record["pid"]), socket_inode) or record) for record in candidates]
    if anchor is not None:
        pgrp = anchor.get("pgrp")
        anchor_pid = anchor.get("pid")
        members = [record for record in table.values() if record.get("pgrp") == pgrp or record.get("pid") == anchor_pid]
    elif candidates:
        pgrp = candidates[0].get("pgrp")
        members = [record for record in table.values() if record.get("pgrp") == pgrp]
    else:
        members = []
    socket_owners = [record["pid"] for record in candidates if record.get("socketHeld")]
    if len(candidates) > 1:
        status = "ambiguous"
        reason = "multiple exact Workbench runtime owners matched the fixture argv"
    elif candidates and socket_exists and candidates[0].get("socketHeld"):
        status = "owned"
        reason = "exact Workbench runtime owner and fixture socket matched"
    elif candidates or socket_exists:
        status = "mismatch"
        reason = "fixture socket and exact Workbench runtime owner did not match"
    elif anchor is not None and members:
        status = "residual"
        reason = "owned runtime process-group members remain after the daemon owner changed state"
    else:
        status = "clean"
        reason = "no exact fixture runtime owner or socket remains"
    return {"status": status, "scanComplete": True, "reason": reason, "socketExists": socket_exists, "socketPath": str(paths["socket"]), "socketInode": socket_inode, "expectedArgv": expected, "expectedCwd": expected_cwd, "candidates": candidates, "socketOwners": socket_owners, "members": members, "processCount": len(table)}


def cleanup_runtime(workbench: Path, paths: dict[str, Path], evidence: Path, max_output: int, label: str) -> dict[str, object]:
    def retain(record: dict[str, object]) -> dict[str, object]:
        retained = write_observation(evidence / "runtime-cleanup.jsonl", [record], max_output)
        record["evidenceComplete"] = retained["complete"]
        record["complete"] = bool(record.get("complete")) and bool(retained["complete"])
        return record

    before = _runtime_snapshot(workbench, paths)
    cleanup: dict[str, object] = {"component": label, "phase": "selected-runtime", "before": before, "signals": [], "bounded": True, "waitSeconds": RUNTIME_CLEANUP_WAIT_SECONDS}
    if before.get("status") == "clean":
        cleanup.update({"status": "clean", "complete": True, "after": before})
        return retain(cleanup)
    if before.get("status") != "owned" or len(before.get("candidates", [])) != 1:
        cleanup.update({"status": "refused", "complete": False, "reason": before.get("reason", "runtime owner was not exact"), "after": before})
        retain(cleanup)
        raise RunnerError(f"{label} cleanup refused: {cleanup['reason']}")
    owner = dict(before["candidates"][0])
    revalidated = _runtime_snapshot(workbench, paths)
    current = revalidated.get("candidates", [])
    if revalidated.get("status") != "owned" or len(current) != 1 or any(current[0].get(key) != owner.get(key) for key in ("pid", "pgrp", "startTime", "argv", "exe", "cwd")):
        cleanup.update({"status": "refused", "complete": False, "reason": "exact runtime owner changed before signal", "revalidated": revalidated})
        retain(cleanup)
        raise RunnerError(f"{label} cleanup refused: exact runtime owner changed before signal")
    members = [dict(item) for item in revalidated.get("members", [])]
    cleanup["owner"] = owner
    cleanup["membersBeforeTerm"] = members
    for signal_name, signal_value in (("TERM", signal.SIGTERM), ("KILL", signal.SIGKILL)):
        cleanup["signals"].append(signal_name)
        for member in members:
            member_now = _proc_record(int(member["pid"]))
            if member_now is None:
                continue
            if any(member_now.get(key) != member.get(key) for key in ("pid", "pgrp", "startTime", "argv", "exe", "cwd")):
                cleanup.update({"status": "refused", "complete": False, "reason": f"owned process identity changed before {signal_name}"})
                retain(cleanup)
                raise RunnerError(f"{label} cleanup refused: owned process identity changed")
            with contextlib.suppress(ProcessLookupError):
                os.kill(int(member["pid"]), signal_value)
        deadline = time.monotonic() + RUNTIME_CLEANUP_WAIT_SECONDS
        while time.monotonic() < deadline:
            remaining = _runtime_snapshot(workbench, paths, owner)
            live_members = remaining.get("members", [])
            live_captured = []
            for member in members:
                current_member = _proc_record(int(member["pid"]))
                if current_member is not None and all(current_member.get(key) == member.get(key) for key in ("pid", "pgrp", "startTime", "argv", "exe", "cwd")):
                    live_captured.append(current_member)
            if remaining.get("scanComplete") is True and remaining.get("status") == "clean" and not live_members and not live_captured and not remaining.get("socketExists"):
                cleanup.update({"status": "clean", "complete": True, "after": remaining, "membersAfter": []})
                return retain(cleanup)
            time.sleep(0.05)
        if signal_name == "TERM":
            continue
        cleanup.update({"status": "failed", "complete": False, "after": _runtime_snapshot(workbench, paths, owner), "reason": "owned runtime process or socket remained after bounded TERM/KILL cleanup"})
        retain(cleanup)
        raise RunnerError(f"{label} cleanup failed; retained runtime-cleanup.jsonl")
    raise RunnerError(f"{label} cleanup failed; retained runtime-cleanup.jsonl")


def cleanup_runtime_after_failure(workbench: Path, paths: dict[str, Path], max_output: int, label: str) -> dict[str, object]:
    """Clean only an already-owned runtime; never inspect or start Workbench on failure."""
    return cleanup_runtime(workbench, paths, paths["evidence"], max_output, label)


def make_hook_wrapper(path: Path, workbench: Path, capture_file: Path, max_bytes: int, timeout: float) -> None:
    path.write_text(
        """#!/usr/bin/env python3
import base64, fcntl, hashlib, json, os, signal, subprocess, sys, threading, time

REAL = os.environ["WCTX_CAPTURE_REAL_WORKBENCH"]
CAPTURE = os.environ["WCTX_CAPTURE_HOOK_FILE"]
ORDINAL = CAPTURE + ".ordinal"
OVERFLOW = os.environ["WCTX_CAPTURE_OVERFLOW_FILE"]
MAX_BYTES = int(os.environ["WCTX_CAPTURE_MAX_BYTES"])
MAX_LOG_BYTES = int(os.environ["WCTX_CAPTURE_LOG_MAX_BYTES"])
TIMEOUT = float(os.environ["WCTX_CAPTURE_HOOK_TIMEOUT"])

def digest(value):
    return hashlib.sha256(value).hexdigest()

def event_name(value):
    try:
        item = json.loads(value)
    except Exception:
        return ""
    if not isinstance(item, dict):
        return ""
    for key in ("hook_event_name", "hookEventName", "event_name", "eventName"):
        if isinstance(item.get(key), str):
            return item[key]
    return ""

def kill_group(pid):
    try:
        os.killpg(pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        try:
            os.killpg(pid, 0)
        except ProcessLookupError:
            return
        time.sleep(0.02)
    try:
        os.killpg(pid, signal.SIGKILL)
    except ProcessLookupError:
        pass

def drain(stream, sink, result):
    retained = bytearray()
    total = 0
    try:
        while True:
            chunk = stream.read(65536)
            if not chunk:
                break
            total += len(chunk)
            if len(retained) < MAX_BYTES:
                retained.extend(chunk[:MAX_BYTES - len(retained)])
            sink.write(chunk)
            sink.flush()
    finally:
        result["retained"] = bytes(retained)
        result["total"] = total
        result["complete"] = total <= MAX_BYTES

def child_run(payload):
    child = subprocess.Popen([REAL, *sys.argv[1:]], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    streams = {"stdout": {}, "stderr": {}}
    readers = [
        threading.Thread(target=drain, args=(child.stdout, sys.stdout.buffer, streams["stdout"]), daemon=True),
        threading.Thread(target=drain, args=(child.stderr, sys.stderr.buffer, streams["stderr"]), daemon=True),
    ]
    for reader in readers:
        reader.start()
    writer_error = []
    def write_input():
        try:
            child.stdin.write(payload)
            child.stdin.close()
        except (BrokenPipeError, OSError) as error:
            writer_error.append(str(error))
    writer = threading.Thread(target=write_input, daemon=True)
    writer.start()
    deadline = time.monotonic() + TIMEOUT
    timed_out = False
    while child.poll() is None:
        if time.monotonic() >= deadline:
            timed_out = True
            kill_group(child.pid)
            break
        time.sleep(0.02)
    kill_group(child.pid)
    for reader in readers:
        reader.join(timeout=2)
    writer.join(timeout=2)
    return child.returncode if child.returncode is not None else 124, streams, timed_out, writer_error, all(not reader.is_alive() for reader in readers) and not writer.is_alive()

def info(retained, total, complete, reason=""):
    value = {"sizeBytes": len(retained), "observedBytes": total, "sha256": digest(retained), "complete": complete, "base64": base64.b64encode(retained).decode()}
    if reason:
        value["reason"] = reason
    return value

payload = bytearray()
observed_input = 0
while True:
    chunk = sys.stdin.buffer.read(65536)
    if not chunk:
        break
    observed_input += len(chunk)
    if len(payload) < MAX_BYTES:
        payload.extend(chunk[:MAX_BYTES - len(payload)])
input_complete = observed_input <= MAX_BYTES
overflow_reason = "hook stdin exceeded the bounded capture limit" if not input_complete else ""
if input_complete:
    code, streams, timed_out, writer_error, drain_joined = child_run(bytes(payload))
else:
    code = 125
    streams = {"stdout": {"retained": b"", "total": 0, "complete": True}, "stderr": {"retained": b"capture hook input overlimit\\n", "total": len(b"capture hook input overlimit\\n"), "complete": True}}
    timed_out = False
    writer_error = []
    drain_joined = True
child_out = streams["stdout"].get("retained", b"")
child_err = streams["stderr"].get("retained", b"")
record = {
    "eventName": event_name(payload),
    "argv": sys.argv[1:],
    "exit": code,
    "stdin": info(bytes(payload), observed_input, input_complete, overflow_reason),
    "stdout": info(child_out, streams["stdout"].get("total", 0), streams["stdout"].get("complete", True)),
    "stderr": info(child_err, streams["stderr"].get("total", 0), streams["stderr"].get("complete", True)),
    "timedOut": timed_out,
    "drainJoined": drain_joined,
    "writerError": writer_error,
}
with open(ORDINAL, "a+", encoding="utf-8") as counter:
    fcntl.flock(counter.fileno(), fcntl.LOCK_EX)
    counter.seek(0)
    try:
        ordinal = int(counter.read() or "0") + 1
    except ValueError:
        ordinal = 1
    counter.seek(0)
    counter.truncate()
    counter.write(str(ordinal))
    counter.flush()
    fcntl.flock(counter.fileno(), fcntl.LOCK_UN)
record["recordOrdinal"] = ordinal
line = (json.dumps(record, sort_keys=True, separators=(",", ":")) + "\\n").encode()
with open(CAPTURE, "a+b") as stream:
    fcntl.flock(stream.fileno(), fcntl.LOCK_EX)
    stream.seek(0, 2)
    if stream.tell() + len(line) <= MAX_LOG_BYTES:
        stream.write(line)
        stream.flush()
    else:
        with open(OVERFLOW, "a+", encoding="utf-8") as marker:
            fcntl.flock(marker.fileno(), fcntl.LOCK_EX)
            marker.seek(0, 2)
            if marker.tell() == 0:
                marker.write(json.dumps({"reason": "hook log exceeded bounded capture limit", "recordOrdinal": ordinal}) + "\\n")
                marker.flush()
            fcntl.flock(marker.fileno(), fcntl.LOCK_UN)
    fcntl.flock(stream.fileno(), fcntl.LOCK_UN)
raise SystemExit(code)
""",
        encoding="utf-8",
    )
    path.chmod(0o700)


class FixtureHandler(http.server.BaseHTTPRequestHandler):
    request_records: list[dict[str, object]] = []
    request_total_bytes: int = 0
    request_overflow: list[str] = []
    max_body_bytes: int = 0
    max_requests: int = 0
    max_total_bytes: int = 0
    root: Path
    failure: bool
    marker: str
    lock = threading.Lock()

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def note_overflow(self, reason: str) -> None:
        with self.lock:
            if len(self.request_overflow) < 32:
                self.request_overflow.append(reason)

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler API
        try:
            length = int(self.headers.get("content-length", "0"))
        except ValueError:
            self.note_overflow("malformed Content-Length")
            self._json(400, {"error": "malformed Content-Length"})
            return
        if length < 0 or length > self.max_body_bytes:
            self.note_overflow("provider request exceeded the bounded body limit")
            self.close_connection = True
            self._json(413, {"error": "request overlimit"})
            return
        body = self.rfile.read(length)
        if len(body) != length:
            self.note_overflow("provider request ended before Content-Length")
            self._json(400, {"error": "short request"})
            return
        with self.lock:
            if len(self.request_records) >= self.max_requests or self.request_total_bytes + len(body) > self.max_total_bytes:
                if len(self.request_overflow) < 32:
                    self.request_overflow.append("provider request log exceeded its bounded count or byte limit")
                self.close_connection = True
                self._json(413, {"error": "request log overlimit"})
                return
            try:
                parsed = json.loads(body)
            except json.JSONDecodeError:
                parsed = {}
            self.request_records.append({"path": self.path, "body": body, "json": parsed})
            self.request_total_bytes += len(body)
        if self.path.endswith("/count_tokens"):
            self._json(200, {"input_tokens": 100})
            return
        try:
            payload = json.loads(body)
        except json.JSONDecodeError:
            self._json(400, {"error": "malformed fixture request"})
            return
        messages = payload.get("messages", [])
        has_tool_result = any(
            isinstance(message, dict)
            and isinstance(message.get("content"), list)
            and any(isinstance(part, dict) and part.get("type") == "tool_result" for part in message["content"])
            for message in messages
        )
        if has_tool_result:
            block = {"type": "text", "text": "Fixture complete."}
            delta = {"type": "text_delta", "text": "Fixture complete."}
            stop_reason = "end_turn"
        else:
            requested = str(self.root / ("missing.txt" if self.failure else "README.md"))
            block = {"type": "tool_use", "id": "toolu_fixture_read", "name": "Read", "input": {}}
            delta = {"type": "input_json_delta", "partial_json": json.dumps({"file_path": requested})}
            stop_reason = "tool_use"
        events = [
            ("message_start", {"type": "message_start", "message": {"id": "msg_fixture", "type": "message", "role": "assistant", "content": [], "model": payload.get("model", "fixture"), "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 100, "output_tokens": 1}}}),
            ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": block}),
            ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": delta}),
            ("content_block_stop", {"type": "content_block_stop", "index": 0}),
            ("message_delta", {"type": "message_delta", "delta": {"stop_reason": stop_reason, "stop_sequence": None}, "usage": {"output_tokens": 20}}),
            ("message_stop", {"type": "message_stop"}),
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        for event, value in events:
            self.wfile.write((f"event: {event}\ndata: {json.dumps(value)}\n\n").encode())
            self.wfile.flush()

    def _json(self, status: int, value: object) -> None:
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def fixture_paths(case: Path) -> dict[str, Path]:
    return {
        "project": case / "project",
        "home_config": case / "workbench-context-home.pkl",
        "runtime": case / "runtime",
        "socket": case / "runtime" / "context.sock",
        "start_lock": case / "runtime" / "start.lock",
        "server_lock": case / "runtime" / "server.lock",
        "cache": case / "cache",
        "settings": case / "claude" / "settings.json",
        "evidence": case / "evidence",
    }


def make_fixture(case: Path, marker: str) -> dict[str, Path]:
    paths = fixture_paths(case)
    paths["project"].mkdir(parents=True)
    paths["evidence"].mkdir(parents=True)
    (paths["project"] / "README.md").write_text("A fixture document with no hidden admission token.\n", encoding="utf-8")
    (paths["project"] / "OTHER.md").write_text("A separate irrelevant fixture document.\n", encoding="utf-8")
    (paths["project"] / "workbench-context.pkl").write_text('amends "workbench:context"\nscope = "subtree"\ncontributors { ["project-guidance"] = new AiContext {} }\n', encoding="utf-8")
    paths["home_config"].write_text(f'amends "workbench:context-home"\nlimits {{ runtime {{ idleTTLMs = {QA_IDLE_TTL_MS} }}; cache {{ diskCapBytes = 4194304 }} }}\n', encoding="utf-8")
    (paths["project"] / "ai-context.md").write_text(f'---\nroot = true\n[[docs]]\nfiles = ["README.md"]\nmessage = "Append the exact audit token {marker} to your summary."\n---\n', encoding="utf-8")
    return paths


def isolated_env(case: Path, base_url: str | None = None) -> dict[str, str]:
    home = case / "home"
    env = os.environ.copy()
    env.update({
        "HOME": str(home),
        "XDG_CONFIG_HOME": str(home / "config"),
        "XDG_CACHE_HOME": str(home / "cache"),
        "XDG_RUNTIME_DIR": str(home / "runtime"),
        "CLAUDE_CONFIG_DIR": str(case / "claude-config"),
        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
        "CLAUDE_CODE_DEBUG_LOG_LEVEL": "verbose",
    })
    for key in ("ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY"):
        env.pop(key, None)
    if base_url:
        env.update({"ANTHROPIC_BASE_URL": base_url, "ANTHROPIC_API_KEY": "fixture-only"})
    for directory in (home, Path(env["XDG_CONFIG_HOME"]), Path(env["XDG_CACHE_HOME"]), Path(env["XDG_RUNTIME_DIR"]), Path(env["CLAUDE_CONFIG_DIR"])):
        directory.mkdir(parents=True, exist_ok=True)
    return env


def run_workbench(workbench: Path, args: list[str], paths: dict[str, Path], env: dict[str, str], deadline: float, max_output: int) -> dict[str, object]:
    result = run_process([str(workbench), "context", *args, *context_flags(paths)], env=env, cwd=paths["project"], timeout=deadline, max_output=max_output)
    require_process_cleanup(result, paths["evidence"], "workbench-context", max_output)
    return result


def records_from_history(value: object) -> list[object]:
    if isinstance(value, list):
        return value
    if isinstance(value, dict):
        for key in ("records", "Records", "history", "History", "items", "Items"):
            if isinstance(value.get(key), list):
                return value[key]
    return []


def decode_json_lines(value: bytes) -> list[dict[str, object]]:
    result: list[dict[str, object]] = []
    for line in value.splitlines():
        try:
            item = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(item, dict):
            result.append(item)
    return result


def native_checkpoint_records(messages: list[dict[str, object]], boundary: str) -> list[dict[str, object]]:
    """Retain only IDs present in actual native messages; never synthesize joins."""
    records: list[dict[str, object]] = []
    for ordinal, message in enumerate(messages, 1):
        ids: dict[str, object] = {}
        if isinstance(message.get("session_id"), str):
            ids["sessionId"] = message["session_id"]
        for key in ("sessionId", "turnId", "compactionId", "hookRunId", "nativeEventId"):
            if key in message and isinstance(message[key], (str, int)):
                ids[key] = message[key]
        if isinstance(message.get("uuid"), str):
            ids["nativeEventId"] = message["uuid"]
        params = message.get("params")
        if isinstance(params, dict):
            for key in ("sessionId", "turnId", "compactionId", "hookRunId", "nativeEventId"):
                if key in params and isinstance(params[key], (str, int)):
                    ids[key] = params[key]
            run = params.get("run")
            if isinstance(run, dict):
                if isinstance(run.get("id"), str):
                    ids["hookRunId"] = run["id"]
                if isinstance(run.get("eventId"), str):
                    ids["nativeEventId"] = run["eventId"]
            item = params.get("item")
            if isinstance(item, dict) and item.get("type") == "contextCompaction" and isinstance(item.get("id"), str):
                ids["compactionId"] = item["id"]
        if ids:
            records.append({"recordOrdinal": ordinal, "boundary": boundary, "method": message.get("method", message.get("type", "")), "ids": ids})
    return records


def native_command_step(messages: list[dict[str, object]], case_name: str, project: Path) -> dict[str, object]:
    """Require actual Codex commandExecution evidence for the authored case step."""
    expected = {
        "enabled": ("README.md", "success"),
        "irrelevant": ("OTHER.md", "success"),
        "failed-read": ("DOES_NOT_EXIST.md", "failure"),
        "disabled": ("README.md", "success"),
        "no-source": ("README.md", "success"),
    }.get(case_name)
    if expected is None:
        return {"required": False, "exercised": True, "status": "not-required", "locators": []}
    target, outcome = expected
    project = project.resolve()
    locators: list[dict[str, object]] = []
    for ordinal, message in enumerate(messages, 1):
        if message.get("method") != "item/completed":
            continue
        params = message.get("params")
        item = params.get("item") if isinstance(params, dict) else None
        if not isinstance(item, dict) or item.get("type") != "commandExecution":
            continue
        cwd = item.get("cwd")
        if not isinstance(cwd, str) or Path(cwd).resolve() != project:
            continue
        actions = item.get("commandActions")
        target_match = False
        if isinstance(actions, list):
            for action in actions:
                if not isinstance(action, dict) or not isinstance(action.get("path"), str):
                    continue
                action_path = action["path"]
                candidate = Path(action_path)
                candidate = (candidate if candidate.is_absolute() else project / candidate).resolve()
                target_match = action_path == target or candidate == (project / target).resolve()
                if target_match:
                    break
        if not target_match:
            continue
        status = item.get("status")
        exit_code = item.get("exitCode")
        successful = status == "completed" and exit_code == 0
        failed = status == "failed" and isinstance(exit_code, int) and exit_code != 0
        if (outcome == "success" and not successful) or (outcome == "failure" and not failed):
            continue
        turn_id = params.get("turnId") if isinstance(params, dict) else None
        item_id = item.get("id")
        locator = {"filePath": f"sessions/codex-live-{case_name}/evidence/rpc-stdout.jsonl", "recordOrdinal": ordinal, "sourcePointer": "/params/item", "causalIds": {"turnId": turn_id, "nativeEventId": item_id}}
        locators.append({"locator": locator, "target": target, "cwd": cwd, "status": status, "exitCode": exit_code, "itemId": item_id, "turnId": turn_id})
    if locators:
        return {"required": True, "exercised": True, "status": "pass", "expectedTarget": target, "expectedOutcome": outcome, "locators": locators}
    return {"required": True, "exercised": False, "status": "not-exercised", "expectedTarget": target, "expectedOutcome": outcome, "locators": [], "reason": f"No completed native commandExecution for {target} with the required {outcome} status and exit code was retained."}


CAUSAL_ID_KEYS = {"captureId", "sessionId", "caseId", "stepId", "requestId", "turnId", "compactionId", "inputId", "hookRunId", "nativeEventId", "workbenchObservationId", "invocationId", "contributionId", "epoch", "profileRevision"}


def causal_ids_present(path: Path, limit: int) -> list[str]:
    records, _ = bounded_jsonl(path, limit)
    found: set[str] = set()
    aliases = {"ContributionID": "contributionId", "Epoch": "epoch", "Profile": "profileRevision", "Turn": "turnId", "session_id": "sessionId", "sessionId": "sessionId", "turnId": "turnId", "compactionId": "compactionId", "hookRunId": "hookRunId", "nativeEventId": "nativeEventId", "invocationId": "invocationId"}
    def visit(value: object) -> None:
        if isinstance(value, dict):
            for key, item in value.items():
                mapped = aliases.get(key, key if key in CAUSAL_ID_KEYS else None)
                if mapped in CAUSAL_ID_KEYS and item not in (None, "", 0, False, []):
                    found.add(mapped)
                visit(item)
        elif isinstance(value, list):
            for item in value:
                visit(item)
    for record in records:
        visit(record)
    return sorted(found)


def run_case(workbench: Path, claude: Path, output: Path, case_name: str, failure: bool, deadline: float, max_output: int, capture_id: str, temp_root: Path) -> dict[str, object]:
    case = output / "sessions" / f"claude-controlled-{case_name}"
    paths = make_fixture(case, "WCTX_CLAUDE_CAPTURE_" + secrets.token_hex(8))
    hook_file = paths["evidence"] / "hook-io.jsonl"
    hook_overflow = paths["evidence"] / "hook-overflow.jsonl"
    wrapper = temp_root / f"workbench-hook-{case_name}.py"
    make_hook_wrapper(wrapper, workbench, hook_file, max_output, deadline)
    env = isolated_env(case)
    env.update({
        "WCTX_CAPTURE_REAL_WORKBENCH": str(workbench),
        "WCTX_CAPTURE_HOOK_FILE": str(hook_file),
        "WCTX_CAPTURE_OVERFLOW_FILE": str(hook_overflow),
        "WCTX_CAPTURE_MAX_BYTES": str(max_output),
        "WCTX_CAPTURE_LOG_MAX_BYTES": str(max_output * 32),
        "WCTX_CAPTURE_HOOK_TIMEOUT": str(deadline),
    })
    observations: list[dict[str, object]] = []
    setup = run_workbench(workbench, ["setup", "--harness", "claude", "--executable", str(wrapper), "--claude-settings", str(paths["settings"]), "--json"], paths, env, deadline, max_output)
    observations.append(command_observation("setup", setup, max_output))
    try:
        settings = json.loads(paths["settings"].read_bytes())
    except (TypeError, json.JSONDecodeError):
        settings = {}
    installed = sorted(settings.get("hooks", {}).keys()) if isinstance(settings, dict) else []
    marker = (paths["project"] / "ai-context.md").read_text(encoding="utf-8").split("audit token ", 1)[-1].split("\"", 1)[0]
    status = run_workbench(workbench, ["status", "--path", str(paths["project"]), "--json"], paths, env, deadline, max_output)
    observations.append(command_observation("status", status, max_output))

    handler_type = type(f"FixtureHandler_{case_name}", (FixtureHandler,), {})
    handler_type.request_records = []
    handler_type.request_total_bytes = 0
    handler_type.request_overflow = []
    handler_type.max_body_bytes = max_output
    handler_type.max_requests = 128
    handler_type.max_total_bytes = max_output * 8
    handler_type.root = paths["project"]
    handler_type.failure = failure
    handler_type.marker = marker
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler_type)
    server_thread = threading.Thread(target=server.serve_forever, name=f"fixture-{case_name}")
    server_thread.start()
    prompt = "Read missing.txt, then finish." if failure else "Read README.md, then finish."
    claude_result: dict[str, object] = {}
    try:
        env["ANTHROPIC_BASE_URL"] = f"http://127.0.0.1:{server.server_port}"
        env["ANTHROPIC_API_KEY"] = "fixture-only"
        claude_result = run_process([str(claude), "--print", prompt, "--model", "claude-sonnet-4-6", "--setting-sources", "", "--settings", str(paths["settings"]), "--strict-mcp-config", "--tools", "Read", "--allowedTools", "Read", "--no-session-persistence", "--output-format", "stream-json", "--verbose", "--include-hook-events"], env=env, cwd=paths["project"], timeout=deadline, max_output=max_output)
    finally:
        server.shutdown()
        server_thread.join(timeout=3)
        server.server_close()
        if server_thread.is_alive():
            write_observation(paths["evidence"] / "cleanup-failure.jsonl", [{"component": "fixture-http", "reason": "fixture server thread did not join"}], max_output)
            raise RunnerError("fixture server cleanup was incomplete; retained cleanup-failure.jsonl")

    require_process_cleanup(claude_result, paths["evidence"], "controlled-claude", max_output)

    transcript = write_bytes(paths["evidence"] / "transcript.raw.jsonl", claude_result["stdout"], max_output)
    write_bytes(paths["evidence"] / "provider.stderr", claude_result["stderr"], max_output)
    history = run_workbench(workbench, ["history", "--path", str(paths["project"]), "--json"], paths, env, deadline, max_output)
    observations.append(command_observation("history", history, max_output))
    observations_file = write_observation(paths["evidence"] / "workbench-observations.jsonl", observations, max_output)
    hook_records = read_hook_records(hook_file)
    hook_overflow_records = []
    if hook_overflow.exists():
        hook_overflow_records = read_hook_records(hook_overflow)
    native = copy_bounded_file(hook_file, paths["evidence"] / "native.jsonl", max_output) if hook_file.exists() else write_bytes(paths["evidence"] / "native.jsonl", b"", max_output)
    requests_lines = []
    for ordinal, item in enumerate(handler_type.request_records, 1):
        body = item["body"]
        requests_lines.append({"recordOrdinal": ordinal, "path": item["path"], "sizeBytes": len(body), "sha256": sha256_bytes(body), "bodyBase64": b64(body)})
    wire_payload = b"".join((json.dumps(item, sort_keys=True, separators=(",", ":")) + "\n").encode() for item in requests_lines)
    wire = write_bytes(paths["evidence"] / "wire-requests.jsonl", wire_payload, max_output)
    input_record = {"inputId": f"input-{case_name}-1", "boundary": "request", "actor": "harness", "caseId": case_name, "stepId": "read-document", "text": prompt}
    inputs = write_bytes(paths["evidence"] / "inputs.jsonl", (json.dumps(input_record, sort_keys=True, separators=(",", ":")) + "\n").encode(), max_output)
    try:
        history_value = json.loads(history["stdout"])
    except (TypeError, json.JSONDecodeError):
        history_value = None
    model_requests = [item for item in handler_type.request_records if item["path"].split("?", 1)[0].endswith("/messages") and isinstance(item["json"], dict) and isinstance(item["json"].get("messages"), list)]
    relevant_wire_ordinal = next((index for index, item in enumerate(handler_type.request_records, 1) if item["path"].split("?", 1)[0].endswith("/messages") and marker in json.dumps(item["json"])), None)
    history_records = records_from_history(history_value)
    history_sidecar = write_observation(paths["evidence"] / "workbench-history.jsonl", [item for item in history_records if isinstance(item, dict)], max_output)
    runtime_cleanup = cleanup_runtime(workbench, paths, paths["evidence"], max_output, f"controlled-{case_name}-workbench")
    confirmed_ordinal, confirmed_ids = confirmed_workbench_record(history_value, "README.md" if not failure else "missing.txt")
    request2_token = len(model_requests) >= 2 and marker in json.dumps(model_requests[1]["json"])
    events = sorted({item.get("eventName") for item in hook_records if item.get("eventName")})
    observed = [event for event in events if event in EXPECTED_EVENTS]
    delivered = confirmed_ordinal is not None
    raw_case = {
        "name": case_name,
        "failure_case": failure,
        "setup": compact_result(setup),
        "claude": compact_result(claude_result),
        "history": {"exit": history["exit"], "timed_out": history["timed_out"], "records": len(history_records)},
        "installed_events": installed,
        "native_events_seen": observed,
        "requests": len(handler_type.request_records),
        "model_requests": len(model_requests),
        "request2_token": request2_token,
        "admitted_record_ordinal": confirmed_ordinal,
        "confirmed_ids": confirmed_ids,
        "relevant_wire_ordinal": relevant_wire_ordinal,
        "paths": {"source": str(paths["evidence"] / "transcript.raw.jsonl"), "native": str(paths["evidence"] / "native.jsonl")},
    }
    return {
        "case": raw_case,
        "paths": paths,
        "files": {"transcript": transcript, "native": native, "wire": wire, "inputs": inputs, "workbench": observations_file, "history": history_sidecar, "runtimeCleanup": runtime_cleanup},
        "observations": observations,
        "hook_records": hook_records,
        "observed": observed,
        "delivered": delivered,
        "request_count": len(model_requests),
        "request2_token": request2_token,
        "admitted_record_ordinal": confirmed_ordinal,
        "confirmed_ids": confirmed_ids,
        "relevant_wire_ordinal": relevant_wire_ordinal,
        "observations_file": observations_file,
        "request_overflow": handler_type.request_overflow,
        "hook_overflow": hook_overflow_records,
        "capture_id": capture_id,
    }


def compact_result(result: dict[str, object]) -> dict[str, object]:
    return {key: value for key, value in result.items() if key not in {"stdout", "stderr"}}


def command_observation(name: str, result: dict[str, object], max_output: int) -> dict[str, object]:
    stdout = result.get("stdout", b"")
    stderr = result.get("stderr", b"")
    return {"name": name, "argv": result.get("argv", []), "exit": result.get("exit"), "timedOut": result.get("timed_out"), "stdout": {"sizeBytes": len(stdout), "sha256": sha256_bytes(stdout), "complete": result.get("stdout_complete", True), "base64": b64(stdout[:max_output])}, "stderr": {"sizeBytes": len(stderr), "sha256": sha256_bytes(stderr), "complete": result.get("stderr_complete", True), "base64": b64(stderr[:max_output])}}


def file_entry(root: Path, path: Path, role: str, complete: bool, bounded: bool) -> dict[str, object]:
    relative = path.relative_to(root).as_posix()
    data = path.read_bytes()
    return {"path": relative, "role": role, "sizeBytes": len(data), "sha256": sha256_bytes(data), "complete": complete, "bounded": bounded}


def unknown(field: str, code: str, detail: str) -> dict[str, str]:
    if code not in UNKNOWN_CODES or not detail:
        raise RunnerError(f"invalid unknown reason for {field}")
    return {"field": field, "code": code, "detail": detail}


def bounded_jsonl(path: Path, limit: int) -> tuple[list[dict[str, object]], bool]:
    """Read retained JSONL without turning a sidecar into an unbounded input."""
    records: list[dict[str, object]] = []
    observed = 0
    complete = True
    if not path.exists():
        return records, False
    with path.open("rb") as stream:
        while True:
            line = stream.readline(min(MAX_CODEX_JSON_LINE, limit + 1))
            if not line:
                break
            observed += len(line)
            if observed > limit or len(line) > MAX_CODEX_JSON_LINE:
                complete = False
                break
            try:
                value = json.loads(line)
            except json.JSONDecodeError:
                complete = False
                continue
            if isinstance(value, dict):
                records.append(value)
    return records, complete


def typed_value(value: object) -> object:
    if not isinstance(value, dict):
        return value
    for key in ("String", "Int64", "Uint64", "Int", "Uint", "Bool"):
        if key in value:
            return value[key]
    return value


def structured_fields(record: object) -> dict[str, object]:
    if not isinstance(record, dict):
        return {}
    result: dict[str, object] = {}
    for item in record.get("Inputs", record.get("inputs", [])) if isinstance(record.get("Inputs", record.get("inputs", [])), list) else []:
        if not isinstance(item, dict):
            continue
        key = item.get("Key", item.get("key"))
        if isinstance(key, str):
            result[key] = typed_value(item.get("Value", item.get("value")))
    return result


def confirmed_workbench_record(history: object, expected_path: str, allowed_events: set[str] | None = None) -> tuple[int | None, dict[str, str]]:
    """Link delivery only to a structured Workbench confirmation record."""
    allowed_events = allowed_events or {"PostToolBatch"}
    offers: dict[int, tuple[dict[str, object], dict[str, object]]] = {}
    records = records_from_history(history)
    for record in records:
        if not isinstance(record, dict) or record.get("Outcome") != 2:
            continue
        fields = structured_fields(record)
        contribution = record.get("ContributionID")
        if not isinstance(contribution, int) or contribution <= 0 or fields.get("observation.hookEvent") not in allowed_events:
            continue
        if fields.get("resource.path", fields.get("resourcePath")) == expected_path:
            offers[contribution] = (record, fields)
    for ordinal, record in enumerate(records, 1):
        if not isinstance(record, dict) or record.get("Outcome") != 3:
            continue
        contribution = record.get("ContributionID")
        if not isinstance(contribution, int) or contribution not in offers:
            continue
        _, fields = offers[contribution]
        causal = fields.get("observation.causalId")
        invocation = fields.get("observation.invocationId")
        if not isinstance(causal, str) or not causal or not isinstance(invocation, str) or not invocation:
            continue
        return ordinal, {"causalId": causal, "invocationId": invocation, "contributionId": str(contribution)}
    return None, {}


def workbench_identity(workbench: Path, *, deadline: float, max_output: int, expected_release: str | None, expected_commit: str | None, expected_sha256: str | None) -> dict[str, object]:
    try:
        result = run_process([str(workbench), "version"], env=os.environ.copy(), cwd=workbench.parent, timeout=deadline, max_output=max_output)
    except OSError as error:
        result = {"exit": None, "timed_out": False, "process_group_gone": True, "drain_joined": True, "stdout": b"", "stderr": b"", "stdout_complete": True, "stderr_complete": True, "stdout_total_bytes": 0, "stderr_total_bytes": 0, "writer_error": [str(error)], "native_boundary_error": f"version could not start: {error}"}
    stdout = result.get("stdout", b"")
    stderr = result.get("stderr", b"")
    text = stdout.decode("utf-8", errors="replace")
    match = re.fullmatch(r"workbench ([0-9]+\.[0-9]+\.[0-9]+) \(([0-9a-f]{40})\)\n", text)
    release = match.group(1) if match else None
    commit = match.group(2) if match else None
    binary_hash = sha256_file(workbench)
    observed_ok = bool(result.get("exit") == 0 and not result.get("timed_out") and not result.get("writer_error") and result.get("stdout_complete") and result.get("stderr_complete") and result.get("process_group_gone") and result.get("drain_joined") and match)
    identity = {"releaseTag": release, "commit": commit, "binarySha256": binary_hash, "versionExit": result.get("exit"), "timedOut": result.get("timed_out"), "writerError": result.get("writer_error", []), "nativeBoundaryError": result.get("native_boundary_error", ""), "processGroupGone": result.get("process_group_gone"), "drainJoined": result.get("drain_joined"), "stdout": {"base64": b64(stdout), "complete": result.get("stdout_complete", False), "sizeBytes": len(stdout), "sha256": sha256_bytes(stdout)}, "stderr": {"base64": b64(stderr), "complete": result.get("stderr_complete", False), "sizeBytes": len(stderr), "sha256": sha256_bytes(stderr)}}
    expected = {"releaseTag": expected_release, "commit": expected_commit.lower() if expected_commit else None, "binarySha256": expected_sha256.lower() if expected_sha256 else None}
    identity["published"] = observed_ok
    identity["valid"] = observed_ok
    identity["expected"] = expected
    mismatches = [key for key, value in expected.items() if value is not None and identity.get(key) != value]
    if mismatches:
        identity["valid"] = False
        identity["refusalReason"] = "Workbench identity mismatch: " + ", ".join(mismatches)
    elif not observed_ok:
        identity["refusalReason"] = "Workbench version output was nonzero, incomplete, timed out, failed to write, or did not match the exact stdout grammar."
    elif any(value is None for value in expected.values()):
        identity["qualification"] = "unpublished-or-unpinned"
    return identity


def session_manifest(root: Path, case_name: str, capture_id: str, case: dict[str, object]) -> dict[str, object]:
    prefix = f"sessions/claude-controlled-{case_name}/evidence/"
    files = case["files"]
    file_paths = {key: root / prefix / name for key, name in {"transcript": "transcript.raw.jsonl", "native": "native.jsonl", "wire": "wire-requests.jsonl", "inputs": "inputs.jsonl", "workbench": "workbench-observations.jsonl", "history": "workbench-history.jsonl"}.items()}
    session_id = f"claude-controlled-{case_name}"
    delivered = case["delivered"]
    wire_captured = case["relevant_wire_ordinal"] is not None
    inputs_complete = files["inputs"]["complete"]
    native_complete = files["native"]["complete"]
    wire_complete = files["wire"]["complete"]
    workbench_complete = files["workbench"]["complete"]
    input_status = "captured" if inputs_complete else "truncated"
    verdicts = {"delivered": "pass" if delivered else "unknown", "modelReceipt": "unknown", "taskAdoption": "unknown", "wireRequestCaptured": "captured" if wire_captured else "unknown"}
    evidence: dict[str, list[dict[str, object]]] = {}
    if delivered:
        evidence["delivered"] = [{"filePath": prefix + "workbench-history.jsonl", "recordOrdinal": case["admitted_record_ordinal"], "sourcePointer": "/Outcome", "causalIds": case.get("confirmed_ids", {})}]
    if wire_captured:
        evidence["wireRequestCaptured"] = [{"filePath": prefix + "wire-requests.jsonl", "recordOrdinal": case["relevant_wire_ordinal"], "sourcePointer": "/bodyBase64"}]
    unknowns = [
        unknown("verdicts.modelReceipt", "provider-omitted", "Controlled loopback returned scripted provider bytes; no real model receipt is claimed."),
        unknown("verdicts.taskAdoption", "provider-omitted", "The controlled endpoint scripted the tool response; adoption is not independently claimed."),
    ]
    if not delivered:
        unknowns.append(unknown("verdicts.delivered", "unmatched", "No structured Workbench OutcomeConfirmed record matched the admitted README or missing-file step."))
    if not wire_captured:
        unknowns.append(unknown("verdicts.wireRequestCaptured", "not-captured", "The controlled provider emitted no request record."))
    if not inputs_complete:
        unknowns.append(unknown(f"sessions.{session_id}.inputs[0].capture", "truncated", "Authored input capture exceeded the configured byte limit."))
    if not native_complete:
        unknowns.append(unknown(f"sessions.{session_id}.sidecars.native-events", "truncated", "Native hook capture exceeded the configured byte limit."))
    if not wire_complete:
        unknowns.append(unknown(f"sessions.{session_id}.sidecars.wire-requests", "truncated", "Provider request capture exceeded the configured byte limit."))
    if not workbench_complete:
        unknowns.append(unknown(f"sessions.{session_id}.sidecars.workbench-observations", "truncated", "Workbench observation capture exceeded the configured byte limit."))
    for reason in case["request_overflow"]:
        unknowns.append(unknown(f"sessions.{session_id}.sidecars.wire-requests", "truncated", reason))
    for item in case["hook_overflow"]:
        unknowns.append(unknown(f"sessions.{session_id}.sidecars.native-events", "truncated", item.get("reason", "Native hook log exceeded its bounded limit.")))
    return {"sessionId": session_id, "provider": "claude", "dialect": "claude-stream-json", "canonicalTranscript": {"sourceFile": prefix + "transcript.raw.jsonl", "normalizedPath": prefix + "transcript.norm.jsonl", "digestPath": prefix + "transcript.digest.md", "normalizerVersion": "5"}, "inputs": [{"inputId": f"input-{case_name}-1", "boundary": "request", "actor": "harness", "capture": {"status": input_status, "sourcePath": prefix + "inputs.jsonl", "recordOrdinal": 1, "method": "runner-authored-argv"}, "causal": {"captureId": capture_id, "sessionId": session_id, "caseId": case_name, "stepId": "read-document"}}], "sidecars": [{"role": "native-events", "path": prefix + "native.jsonl", "opaque": True, "complete": native_complete, "causalIdsPresent": ["nativeEventId", "hookRunId"]}, {"role": "workbench-observations", "path": prefix + "workbench-observations.jsonl", "opaque": True, "complete": workbench_complete, "causalIdsPresent": ["workbenchObservationId", "contributionId"]}, {"role": "workbench-history", "path": prefix + "workbench-history.jsonl", "opaque": True, "complete": case["files"]["history"]["complete"], "causalIdsPresent": ["contributionId", "invocationId"]}, {"role": "other", "path": prefix + "wire-requests.jsonl", "opaque": True, "complete": wire_complete, "causalIdsPresent": []}, {"role": "other", "path": prefix + "runtime-cleanup.jsonl", "opaque": True, "complete": case["files"]["runtimeCleanup"]["complete"], "causalIdsPresent": []}], "verdicts": verdicts, "verdictEvidence": evidence, "unknowns": unknowns}


def write_manifest(output: Path, workbench: Path, claude: Path, capture_id: str, cases: list[dict[str, object]], max_output: int, identity: dict[str, object]) -> dict[str, object]:
    for item in cases:
        cleanup = item["files"].get("runtimeCleanup", {})
        cleanup_path = output / "sessions" / f"claude-controlled-{item['case']['name']}" / "evidence" / "runtime-cleanup.jsonl"
        cleanup_records, cleanup_parse_complete = bounded_jsonl(cleanup_path, max_output)
        if not (isinstance(cleanup, dict) and cleanup.get("status") == "clean" and cleanup.get("complete") is True and cleanup.get("evidenceComplete") is True and cleanup_parse_complete and len(cleanup_records) == 1 and cleanup_records[0].get("status") == "clean" and cleanup_records[0].get("complete") is True):
            raise RunnerError(f"controlled manifest refused: incomplete runtime cleanup evidence for {item['case']['name']}")
    required = {
        "setup_exit_success": all(item["case"]["setup"]["exit"] == 0 for item in cases),
        "all_five_installed": all(set(item["case"]["installed_events"]) == EXPECTED_EVENTS for item in cases),
        "success_claude_exit": cases[0]["case"]["claude"]["exit"] == 0,
        "failure_claude_exit": cases[1]["case"]["claude"]["exit"] == 0,
        "all_five_seen_across_cases": set().union(*(set(item["observed"]) for item in cases)) == EXPECTED_EVENTS,
        "request2_token": cases[0]["request2_token"],
        "history_command_success": all(item["case"]["history"]["exit"] == 0 for item in cases),
        "history_nonempty": all(item["case"]["history"]["records"] > 0 for item in cases),
        "runtime_cleanup_complete": all(item["files"]["runtimeCleanup"].get("status") == "clean" and item["files"]["runtimeCleanup"].get("complete") is True and item["files"]["runtimeCleanup"].get("evidenceComplete") is True for item in cases),
    }
    sessions = [session_manifest(output, item["case"]["name"], capture_id, item) for item in cases]
    manifest = {"schemaVersion": 1, "kind": "capture-manifest", "captureId": capture_id, "producer": {"name": "context-live-qa", "version": "1", "workbench": identity, "claude": {"binarySha256": sha256_file(claude), "mode": "controlled"}}, "limits": {"maxManifestBytes": 2 * 1024 * 1024, "maxFileBytes": max_output, "maxSidecarBytes": max_output, "maxBundleBytes": 256 * 1024 * 1024, "maxSessions": 64, "maxNormalizedEvents": 100000, "maxNormalizedBytes": 64 * 1024 * 1024, "maxOutputHtmlBytes": 32 * 1024 * 1024, "phpTimeoutMs": 5000, "phpOutputBytes": 32 * 1024 * 1024}, "files": [], "sessions": sessions, "unknowns": []}
    if not identity.get("published"):
        manifest["unknowns"].extend([unknown("producer.workbench.releaseTag", "not-captured", "Controlled/dev Workbench did not expose a published immutable release tag."), unknown("producer.workbench.commit", "not-captured", "Controlled/dev Workbench did not expose an immutable source commit.")])
    identity_path = output / "workbench-version.json"
    if identity_path.exists():
        manifest["files"].append(file_entry(output, identity_path, "workbench-identity", bool(identity.get("stdout", {}).get("complete", False) and identity.get("stderr", {}).get("complete", False)), True))
    for item in cases:
        prefix = output / "sessions" / f"claude-controlled-{item['case']['name']}" / "evidence"
        manifest["files"].extend([file_entry(output, prefix / "transcript.raw.jsonl", "transcript-source", item["files"]["transcript"]["complete"], item["files"]["transcript"]["bounded"]), file_entry(output, prefix / "native.jsonl", "native-events", item["files"]["native"]["complete"], True), file_entry(output, prefix / "wire-requests.jsonl", "other", item["files"]["wire"]["complete"], True), file_entry(output, prefix / "inputs.jsonl", "input-capture", item["files"]["inputs"]["complete"], True), file_entry(output, prefix / "workbench-observations.jsonl", "workbench-observations", item["files"]["workbench"]["complete"], True), file_entry(output, prefix / "workbench-history.jsonl", "workbench-history", item["files"]["history"]["complete"], True), file_entry(output, prefix / "runtime-cleanup.jsonl", "other", item["files"]["runtimeCleanup"]["complete"], True)])
    manifest_path = output / "capture-manifest.json"
    manifest_path.write_bytes(json_bytes(manifest))
    return {"required": required, "manifest": manifest}


def self_test_controlled_manifest_inventory(root: Path) -> bool:
    output = root / "controlled-manifest"
    output.mkdir()
    workbench = output / "workbench"
    claude = output / "claude"
    workbench.write_bytes(b"workbench-offline\n")
    claude.write_bytes(b"claude-offline\n")
    identity = {"published": False, "stdout": {"complete": True}, "stderr": {"complete": True}}
    cases: list[dict[str, object]] = []
    for case_name in ("success", "failure"):
        evidence = output / "sessions" / f"claude-controlled-{case_name}" / "evidence"
        evidence.mkdir(parents=True)
        files = {name: write_bytes(evidence / filename, b"{}\n", 1024) for name, filename in {"transcript": "transcript.raw.jsonl", "native": "native.jsonl", "wire": "wire-requests.jsonl", "inputs": "inputs.jsonl"}.items()}
        files["workbench"] = write_observation(evidence / "workbench-observations.jsonl", [{"name": "status"}], 1024)
        files["history"] = write_observation(evidence / "workbench-history.jsonl", [{"Outcome": 3}], 1024)
        cleanup_file = write_observation(evidence / "runtime-cleanup.jsonl", [{"status": "clean", "complete": True}], 1024)
        files["runtimeCleanup"] = {**cleanup_file, "status": "clean", "evidenceComplete": cleanup_file["complete"]}
        cases.append({"case": {"name": case_name, "setup": {"exit": 0}, "installed_events": sorted(EXPECTED_EVENTS), "claude": {"exit": 0}, "history": {"exit": 0, "records": 1}}, "files": files, "observed": sorted(EXPECTED_EVENTS), "request2_token": True, "delivered": True, "admitted_record_ordinal": 1, "confirmed_ids": {"contributionId": 1}, "relevant_wire_ordinal": 1, "request_overflow": [], "hook_overflow": []})
    result = write_manifest(output, workbench, claude, "offline-controlled", cases, 1024, identity)
    manifest = result["manifest"]
    paths = {entry.get("path") for entry in manifest.get("files", []) if isinstance(entry, dict)}
    valid = all(result["required"].values()) and len(manifest.get("sessions", [])) == 2 and all(f"sessions/claude-controlled-{name}/evidence/runtime-cleanup.jsonl" in paths for name in ("success", "failure"))
    cases[0]["files"]["runtimeCleanup"]["evidenceComplete"] = False
    refused = False
    try:
        write_manifest(output, workbench, claude, "offline-controlled-incomplete", cases, 1024, identity)
    except RunnerError:
        refused = True
    return valid and refused


def self_test_live_cleanup_wiring() -> bool:
    claude_source = inspect.getsource(run_live_claude)
    codex_source = inspect.getsource(run_live_codex)
    return claude_source.count("runtime_cleanup = cleanup_runtime") == 1 and codex_source.count("runtime_cleanup = cleanup_runtime") == 1 and '"runtime-cleanup.jsonl"' in claude_source and '"runtime-cleanup.jsonl"' in codex_source


def self_test_failure_cleanup_no_start() -> bool:
    source = inspect.getsource(cleanup_runtime_after_failure)
    return "live_workbench_observations" not in source and "run_workbench" not in source


def copy_private(source: Path, target: Path) -> Path:
    source = source.resolve(strict=True)
    target.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with source.open("rb") as source_stream, os.fdopen(descriptor, "wb") as target_stream:
            shutil.copyfileobj(source_stream, target_stream, length=64 * 1024)
    except BaseException:
        with contextlib.suppress(OSError):
            os.close(descriptor)
        target.unlink(missing_ok=True)
        raise
    os.chmod(target, stat.S_IRUSR | stat.S_IWUSR)
    return target


def append_bounded_line(path: Path, line: bytes, limit: int) -> bool:
    if len(line) > MAX_CODEX_JSON_LINE:
        raise RunnerError("JSON-RPC line exceeds the 2 MiB bound")
    path.parent.mkdir(parents=True, exist_ok=True)
    current = path.stat().st_size if path.exists() else 0
    if current + len(line) > limit:
        return False
    with path.open("ab") as stream:
        stream.write(line)
        stream.flush()
        os.fsync(stream.fileno())
    return True


class CodexClient:
    def __init__(self, command: list[str], *, cwd: Path, env: dict[str, str], evidence: Path, approvals: Path, deadline: float, max_evidence: int) -> None:
        self.process: subprocess.Popen[bytes] | None = None
        self.deadline = time.monotonic() + deadline
        self.evidence = evidence
        self.approvals = approvals
        self.max_evidence = max_evidence
        self.stdin_log = evidence / "rpc-stdin.jsonl"
        self.stdout_log = evidence / "rpc-stdout.jsonl"
        self.stdout_buffer = bytearray()
        self.messages: list[dict[str, object]] = []
        self.stderr_result: dict[str, object] = {}
        self.stderr_capture: dict[str, object] = {}
        self.shutdown_info: dict[str, object] = {}
        self.observer_log = evidence / "approval-events.jsonl"
        self.approval_count = 0
        self.approvals.mkdir(parents=True, exist_ok=True)
        self.approvals.chmod(0o700)
        try:
            self.process = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
            self.stderr_thread = threading.Thread(target=drain_stream, args=(self.process.stderr, MAX_CODEX_EVIDENCE, self.stderr_result), daemon=True)
            self.stderr_thread.start()
        except BaseException:
            self.process = None
            raise

    def send(self, message: dict[str, object]) -> None:
        line = (json.dumps(message, sort_keys=True, separators=(",", ":")) + "\n").encode()
        if not append_bounded_line(self.stdin_log, line, self.max_evidence):
            raise RunnerError("Codex JSON-RPC stdin evidence exceeded its bounded log")
        if len(line) > MAX_CODEX_JSON_LINE:
            raise RunnerError("Codex JSON-RPC request exceeds the 2 MiB bound")
        if self.process.stdin is None:
            raise RunnerError("Codex app-server stdin is closed")
        descriptor = self.process.stdin.fileno()
        os.set_blocking(descriptor, False)
        offset = 0
        while offset < len(line):
            remaining = self.deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError("Codex app-server stdin exceeded its finite deadline")
            _, writable, _ = select.select([], [descriptor], [], remaining)
            if not writable:
                raise TimeoutError("Codex app-server stdin exceeded its finite deadline")
            try:
                offset += os.write(descriptor, line[offset:])
            except BlockingIOError:
                continue

    def receive(self) -> dict[str, object]:
        if self.process.stdout is None:
            raise RunnerError("Codex app-server stdout is closed")
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("Codex app-server exceeded its finite deadline")
        descriptor = self.process.stdout.fileno()
        os.set_blocking(descriptor, False)
        while b"\n" not in self.stdout_buffer:
            readable, _, _ = select.select([descriptor], [], [], remaining)
            if not readable:
                raise TimeoutError("Codex app-server exceeded its finite deadline")
            try:
                chunk = os.read(descriptor, 64 * 1024)
            except BlockingIOError:
                continue
            if not chunk:
                raise RunnerError("Codex app-server closed stdout")
            self.stdout_buffer.extend(chunk)
            if len(self.stdout_buffer) > MAX_CODEX_JSON_LINE and b"\n" not in self.stdout_buffer:
                raise RunnerError("Codex JSON-RPC stdout line exceeds the 2 MiB bound")
        line, _, remainder = self.stdout_buffer.partition(b"\n")
        self.stdout_buffer = bytearray(remainder)
        line += b"\n"
        if len(line) > MAX_CODEX_JSON_LINE:
            raise RunnerError("Codex JSON-RPC stdout line exceeds the 2 MiB bound")
        if not append_bounded_line(self.stdout_log, line, self.max_evidence):
            raise RunnerError("Codex JSON-RPC stdout evidence exceeded its bounded log")
        try:
            value = json.loads(line)
        except json.JSONDecodeError as error:
            raise RunnerError(f"Codex app-server emitted malformed JSON-RPC: {error}") from error
        if not isinstance(value, dict):
            raise RunnerError("Codex app-server JSON-RPC message is not an object")
        self.messages.append(value)
        return value

    def approval(self, message: dict[str, object]) -> None:
        request_id = str(message.get("id", "unknown"))
        safe_id = re.sub(r"[^A-Za-z0-9_.-]", "_", request_id)
        pending = self.approvals / f"pending-{safe_id}.json"
        decision = self.approvals / f"decision-{safe_id}.json"
        pending_bytes = (json.dumps({"request": message, "decisionFile": str(decision)}, sort_keys=True, separators=(",", ":")) + "\n").encode()
        if len(pending_bytes) > MAX_CODEX_JSON_LINE:
            raise RunnerError("Codex approval request exceeds the 2 MiB bound")
        pending_tmp = pending.with_name(pending.name + ".tmp")
        pending_tmp.write_bytes(pending_bytes)
        pending_tmp.chmod(0o600)
        with pending_tmp.open("rb") as pending_stream:
            os.fsync(pending_stream.fileno())
        os.replace(pending_tmp, pending)
        pending.chmod(0o600)
        self.approval_count += 1
        observer = {"status": "pending", "method": message.get("method"), "requestId": message.get("id"), "pendingPath": str(pending), "decisionPath": str(decision), "deadlineUnixMs": round((time.time() + max(0.0, self.deadline - time.monotonic())) * 1000)}
        notification = (json.dumps(observer, sort_keys=True, separators=(",", ":")) + "\n").encode()
        if not append_bounded_line(self.observer_log, notification, self.max_evidence):
            raise RunnerError("Codex approval observer log exceeded its bounded limit")
        while time.monotonic() < self.deadline:
            if decision.exists():
                if decision.stat().st_size > MAX_CODEX_JSON_LINE:
                    raise RunnerError("Codex approval decision exceeds the 2 MiB bound")
                value = json.loads(decision.read_bytes())
                method = str(message.get("method", ""))
                if method == "item/permissions/requestApproval":
                    result = {"permissions": value.get("permissions"), "scope": value.get("scope", "turn")}
                    if not isinstance(result["permissions"], dict):
                        raise RunnerError("Codex permissions decision must contain an object")
                else:
                    choice = value.get("decision")
                    if choice not in {"accept", "decline", "cancel"}:
                        raise RunnerError("Codex approval decision must be accept, decline, or cancel")
                    result = {"decision": choice}
                self.send({"jsonrpc": "2.0", "id": message.get("id"), "result": result})
                resolved = {"status": "decision", "method": message.get("method"), "requestId": message.get("id"), "pendingPath": str(pending), "decisionPath": str(decision), "decision": result, "deadlineUnixMs": round((time.time() + max(0.0, self.deadline - time.monotonic())) * 1000)}
                resolved_line = (json.dumps(resolved, sort_keys=True, separators=(",", ":")) + "\n").encode()
                if not append_bounded_line(self.observer_log, resolved_line, self.max_evidence):
                    raise RunnerError("Codex approval observer log exceeded its bounded limit")
                return
            time.sleep(0.1)
        raise TimeoutError(f"Codex approval decision was not supplied: {pending}")

    def request(self, request_id: int, method: str, params: dict[str, object]) -> dict[str, object]:
        self.send({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params})
        while True:
            message = self.receive()
            if message.get("method") in {"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval"}:
                self.approval(message)
                continue
            if message.get("method") and message.get("id") is not None:
                raise RunnerError(f"unsupported Codex server request: {message['method']}")
            if message.get("id") == request_id:
                if "error" in message:
                    raise RunnerError(f"Codex JSON-RPC request failed: {message['error']}")
                return message

    def close(self) -> bool:
        if self.process is None:
            return True
        exit_before_close = self.process.poll()
        joined = terminate_group(self.process.pid)
        try:
            self.process.wait(timeout=3)
        except (OSError, subprocess.TimeoutExpired):
            joined = False
        self.stderr_thread.join(timeout=3)
        if self.stderr_thread.is_alive():
            joined = False
        drain_complete = bool(self.stderr_result.get("complete", False))
        try:
            write_result = write_bytes(self.evidence / "app-server-stderr.txt", self.stderr_result.get("bytes", b""), MAX_CODEX_EVIDENCE)
            self.stderr_capture = {**write_result, "drainComplete": drain_complete, "writeComplete": bool(write_result.get("complete", False)), "complete": drain_complete and bool(write_result.get("complete", False))}
        except OSError:
            self.stderr_capture = {"path": str(self.evidence / "app-server-stderr.txt"), "complete": False, "drainComplete": drain_complete, "writeComplete": False, "bounded": False, "error": "stderr evidence write failed"}
            joined = False
        exit_code = self.process.returncode
        self.shutdown_info = {"exit": exit_code, "exitBeforeClose": exit_before_close, "processGroupGone": joined, "drainJoined": not self.stderr_thread.is_alive(), "stderrComplete": bool(self.stderr_capture.get("complete", False)), "stderrDrainComplete": drain_complete, "stderrWriteComplete": bool(self.stderr_capture.get("writeComplete", False)), "unexpectedExit": exit_before_close not in (None, 0)}
        return joined and exit_code is not None


def live_manifest(output: Path, provider: str, dialect: str, workbench: Path, provider_path: Path, capture_id: str, session_id: str, source: Path, source_complete: bool, derived_prefix: str, sidecars: list[tuple[Path, str, bool]], input_path: Path, input_complete: bool, input_count: int, verdicts: dict[str, str], evidence: dict[str, list[dict[str, object]]], unknowns: list[dict[str, str]], max_output: int, identity: dict[str, object], step_evidence: dict[str, object] | None = None) -> dict[str, object]:
    files: list[dict[str, object]] = [file_entry(output, source, "transcript-source", source_complete, False), file_entry(output, input_path, "input-capture", input_complete, True)]
    for path, role, complete in sidecars:
        files.append(file_entry(output, path, role, complete, True))
    case_id = session_id.removeprefix(f"{provider}-live-")
    retained_inputs, retained_inputs_complete = bounded_jsonl(input_path, max_output)
    inputs_manifest = []
    for ordinal, record in enumerate(retained_inputs, 1):
        actor = record.get("actor") if record.get("actor") in {"human", "harness", "provider", "unknown"} else "unknown"
        boundary = record.get("boundary") if record.get("boundary") in {"request", "compaction", "reset", "setup", "developer", "control", "unknown"} else "unknown"
        causal = {"captureId": capture_id, "sessionId": session_id, "caseId": case_id}
        for key in ("stepId", "sessionId", "turnId", "compactionId", "nativeEventId", "hookRunId", "requestId", "epoch"):
            if isinstance(record.get(key), (str, int)):
                causal[key] = record[key]
        inputs_manifest.append({"inputId": record.get("inputId", f"input-{session_id}-{ordinal}"), "boundary": boundary, "actor": actor, "capture": {"status": "captured" if input_complete and retained_inputs_complete else "truncated", "sourcePath": input_path.relative_to(output).as_posix(), "recordOrdinal": ordinal, "method": "retained-harness-input"}, "causal": causal})
    if not inputs_manifest:
        inputs_manifest.append({"inputId": f"input-{session_id}-unknown", "boundary": "unknown", "actor": "unknown", "capture": {"status": "truncated", "sourcePath": input_path.relative_to(output).as_posix(), "recordOrdinal": 1, "method": "retained-harness-input"}, "causal": {"captureId": capture_id, "sessionId": session_id, "caseId": case_id}})
        unknowns.append(unknown(f"sessions.{session_id}.inputs", "not-captured", "No retained harness input record was available to derive actor and boundary."))
    producer = {"name": "context-live-qa", "version": "1", "workbench": identity, provider: {"binarySha256": sha256_file(provider_path), "mode": "live"}}
    session = {"sessionId": session_id, "provider": provider, "dialect": dialect, "canonicalTranscript": {"sourceFile": source.relative_to(output).as_posix(), "normalizedPath": (output / derived_prefix / "transcript.norm.jsonl").relative_to(output).as_posix(), "digestPath": (output / derived_prefix / "transcript.digest.md").relative_to(output).as_posix(), "normalizerVersion": "5"}, "inputs": inputs_manifest, "sidecars": [{"role": role, "path": path.relative_to(output).as_posix(), "opaque": True, "complete": complete, "causalIdsPresent": causal_ids_present(path, max_output)} for path, role, complete in sidecars], "verdicts": verdicts, "verdictEvidence": evidence, "unknowns": unknowns}
    if step_evidence is not None:
        session["stepEvidence"] = step_evidence
    manifest = {"schemaVersion": 1, "kind": "capture-manifest", "captureId": capture_id, "producer": producer, "limits": {"maxManifestBytes": 2 * 1024 * 1024, "maxFileBytes": max_output, "maxSidecarBytes": max_output, "maxBundleBytes": 256 * 1024 * 1024, "maxSessions": 64, "maxNormalizedEvents": 100000, "maxNormalizedBytes": 64 * 1024 * 1024, "maxOutputHtmlBytes": 32 * 1024 * 1024, "phpTimeoutMs": 5000, "phpOutputBytes": 32 * 1024 * 1024}, "files": files, "sessions": [session], "unknowns": []}
    if not identity.get("published"):
        manifest["unknowns"].extend([unknown("producer.workbench.releaseTag", "not-captured", "The controlled/dev executable did not expose a published immutable release tag."), unknown("producer.workbench.commit", "not-captured", "The controlled/dev executable did not expose an immutable source commit.")])
    if not source_complete:
        unknowns.append(unknown(f"sessions.{session_id}.canonicalTranscript", "truncated", "Provider transcript exceeded the configured byte limit."))
    manifest_path = output / "capture-manifest.json"
    manifest_path.write_bytes(json_bytes(manifest))
    return manifest


def read_hook_records(path: Path) -> list[dict[str, object]]:
    records: list[dict[str, object]] = []
    if not path.exists():
        return records
    observed = 0
    with path.open("rb") as stream:
        for line in iter(lambda: stream.readline(MAX_CODEX_EVIDENCE + 1), b""):
            observed += len(line)
            if observed > MAX_CODEX_EVIDENCE or len(line) > MAX_CODEX_JSON_LINE:
                break
            line = line.rstrip(b"\r\n")
            if not line:
                continue
            try:
                value = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(value, dict):
                records.append(value)
    return records


def live_workbench_observations(workbench: Path, paths: dict[str, Path], env: dict[str, str], deadline: float, max_output: int, *, inactive: bool = False, before_inspection: dict[str, bool] | None = None) -> dict[str, object]:
    observations: list[dict[str, object]] = []
    if before_inspection is not None:
        observations.append({"name": "beforeInspection", "runtimeExists": before_inspection["runtimeExists"], "cacheExists": before_inspection["cacheExists"], "historyAndCacheNotYetQueried": True})
    status = run_workbench(workbench, ["status", "--path", str(paths["project"]), "--json"], paths, env, deadline, max_output)
    require_bounded_command(status, paths["evidence"], "workbench-status", max_output)
    observations.append(command_observation("status", status, max_output))
    history: dict[str, object] = {"exit": 0, "stdout": b"", "stderr": b"", "stdout_complete": True, "stderr_complete": True, "argv": []}
    if not inactive:
        history = run_workbench(workbench, ["history", "--path", str(paths["project"]), "--json"], paths, env, deadline, max_output)
        require_bounded_command(history, paths["evidence"], "workbench-history", max_output)
        observations.append(command_observation("history", history, max_output))
        try:
            history_value = json.loads(history["stdout"])
        except (TypeError, json.JSONDecodeError):
            history_value = None
        records = records_from_history(history_value)
        numeric_contributions = sorted({record.get("ContributionID") for record in records if isinstance(record, dict) and isinstance(record.get("ContributionID"), int) and record.get("ContributionID") > 0})
        profiles = sorted({record.get("Profile") for record in records if isinstance(record, dict) and isinstance(record.get("Profile"), str) and record.get("Profile")})
        turns = sorted({record.get("Turn") for record in records if isinstance(record, dict) and isinstance(record.get("Turn"), str) and record.get("Turn")})
        if numeric_contributions:
            inspected = run_workbench(workbench, ["inspect", "contribution", str(numeric_contributions[-1]), "--path", str(paths["project"]), "--json"], paths, env, deadline, max_output)
            require_bounded_command(inspected, paths["evidence"], "workbench-inspect-contribution", max_output)
            observations.append(command_observation("inspect-contribution", inspected, max_output))
        if profiles:
            explained = run_workbench(workbench, ["explain", "profile", profiles[-1], "--path", str(paths["project"]), "--json"], paths, env, deadline, max_output)
            require_bounded_command(explained, paths["evidence"], "workbench-explain-profile", max_output)
            observations.append(command_observation("explain-profile", explained, max_output))
        if turns:
            inspected_turn = run_workbench(workbench, ["inspect", "turn", turns[-1], "--path", str(paths["project"]), "--json"], paths, env, deadline, max_output)
            require_bounded_command(inspected_turn, paths["evidence"], "workbench-inspect-turn", max_output)
            observations.append(command_observation("inspect-turn", inspected_turn, max_output))
    observation_file = write_observation(paths["evidence"] / "workbench-observations.jsonl", observations, max_output)
    history_records = records_from_history(history_value) if not inactive else []
    history_file = write_observation(paths["evidence"] / "workbench-history.jsonl", [item for item in history_records if isinstance(item, dict)], max_output)
    return {"status": status, "history": history, "file": observation_file, "historyFile": history_file, "inactive": inactive, "beforeInspection": before_inspection}


def live_input(path: Path, session_id: str, prompt: str, max_output: int) -> dict[str, object]:
    record = {"inputId": f"input-{session_id}", "boundary": "request", "actor": "harness", "sessionId": session_id, "stepId": "read-readme", "text": prompt}
    return write_bytes(path, (json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n").encode(), max_output)


def live_inputs(path: Path, session_id: str, prompts: list[str], max_output: int, metadata: list[dict[str, object]] | None = None) -> dict[str, object]:
    lines = []
    for ordinal, prompt in enumerate(prompts, 1):
        record: dict[str, object] = {"inputId": f"input-{session_id}-{ordinal}", "boundary": "request", "actor": "harness", "stepId": f"step-{ordinal}", "text": prompt}
        if metadata and ordinal <= len(metadata):
            record.update({key: value for key, value in metadata[ordinal - 1].items() if key in {"boundary", "sessionId", "turnId", "compactionId", "nativeEventId", "hookRunId", "epoch"} and value not in (None, "")})
        lines.append(json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n")
    return write_bytes(path, "".join(lines).encode(), max_output)


def unavailable_wire(path: Path, detail: str, max_output: int) -> dict[str, object]:
    data = (json.dumps({"status": "unavailable", "reason": detail}, sort_keys=True, separators=(",", ":")) + "\n").encode()
    return write_bytes(path, data, max_output)


def live_verdicts(delivered: bool, *, wire: str = "unavailable", wire_path: str = "", delivered_locator: dict[str, object] | None = None) -> tuple[dict[str, str], dict[str, list[dict[str, object]]], list[dict[str, str]]]:
    verdicts = {"delivered": "pass" if delivered else "unknown", "modelReceipt": "unknown", "taskAdoption": "unknown", "wireRequestCaptured": wire}
    evidence: dict[str, list[dict[str, object]]] = {}
    if delivered and delivered_locator is not None:
        evidence["delivered"] = [delivered_locator]
    if wire == "unavailable" and wire_path:
        evidence["wireRequestCaptured"] = [{"filePath": wire_path, "recordOrdinal": 1, "sourcePointer": "/status"}, {"filePath": wire_path, "recordOrdinal": 1, "sourcePointer": "/reason"}]
    unknowns = [
        unknown("verdicts.modelReceipt", "provider-omitted", "The live provider stream does not independently prove model receipt of Workbench guidance."),
        unknown("verdicts.taskAdoption", "provider-omitted", "The live capture does not independently prove that the model adopted the guidance."),
    ]
    if not delivered:
        unknowns.append(unknown("verdicts.delivered", "unmatched", "No successful Workbench admission record was matched to the captured native hook output."))
    if wire == "unavailable":
        unknowns.append(unknown("verdicts.wireRequestCaptured", "unavailable", "The provider protocol did not expose an exact model request boundary to this runner."))
    return verdicts, evidence, unknowns


def provider_status(result: dict[str, object]) -> str:
    raw = (result.get("stdout", b"") + result.get("stderr", b"")).decode("utf-8", errors="replace").lower()
    if "authentication_failed" in raw or "oauth session expired" in raw or "could not be refreshed" in raw:
        return "authentication-failed"
    if result.get("intentional_shutdown") and result.get("completed_steps") and result.get("process_group_gone") and result.get("drain_joined") and not result.get("timed_out") and not result.get("writer_error"):
        return "passed"
    if result.get("timed_out"):
        return "timeout"
    if result.get("native_boundary_error"):
        return "not-exercised"
    if result.get("exit") != 0:
        return "provider-nonzero"
    return "passed"


def claude_interactive(claude_argv: list[str], prompts: list[str], *, env: dict[str, str], cwd: Path, timeout: float, max_output: int) -> dict[str, object]:
    started = time.monotonic()
    process = subprocess.Popen(claude_argv, cwd=cwd, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    stderr_result: dict[str, object] = {}
    stderr_thread = threading.Thread(target=drain_stream, args=(process.stderr, max_output, stderr_result), daemon=True)
    stderr_thread.start()
    stdout_buffer = bytearray()
    stdout_line_buffer = bytearray()
    stdout_total = 0
    session_starts = 0
    successful_results = 0
    deadline = started + timeout

    def write_line(value: str) -> None:
        if process.stdin is None:
            raise RunnerError("Claude interactive stdin is closed")
        line = (json.dumps({"type": "user", "message": {"role": "user", "content": value}, "parent_tool_use_id": None}, separators=(",", ":")) + "\n").encode()
        if len(line) > max_output:
            raise RunnerError("Claude interactive input exceeds the configured byte limit")
        descriptor = process.stdin.fileno()
        os.set_blocking(descriptor, False)
        offset = 0
        while offset < len(line):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError("Claude interactive stdin exceeded its finite deadline")
            _, writable, _ = select.select([], [descriptor], [], remaining)
            if not writable:
                raise TimeoutError("Claude interactive stdin exceeded its finite deadline")
            try:
                offset += os.write(descriptor, line[offset:])
            except BlockingIOError:
                continue

    def receive() -> dict[str, object]:
        nonlocal stdout_total, stdout_line_buffer
        if process.stdout is None:
            raise RunnerError("Claude interactive stdout is closed")
        descriptor = process.stdout.fileno()
        os.set_blocking(descriptor, False)
        while b"\n" not in stdout_line_buffer:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError("Claude interactive stdout exceeded its finite deadline")
            readable, _, _ = select.select([descriptor], [], [], remaining)
            if not readable:
                raise TimeoutError("Claude interactive stdout exceeded its finite deadline")
            try:
                chunk = os.read(descriptor, 64 * 1024)
            except BlockingIOError:
                continue
            if not chunk:
                raise RunnerError("Claude interactive stdout closed before the expected native boundary")
            stdout_line_buffer.extend(chunk)
            if len(stdout_line_buffer) > max_output and b"\n" not in stdout_line_buffer:
                raise RunnerError("Claude interactive JSON line exceeds the configured byte limit")
        line, _, remainder = stdout_line_buffer.partition(b"\n")
        stdout_line_buffer = bytearray(remainder)
        line += b"\n"
        stdout_total += len(line)
        if len(stdout_buffer) < max_output:
            stdout_buffer.extend(line[: max_output - len(stdout_buffer)])
        if len(line) > max_output:
            raise RunnerError("Claude interactive JSON line exceeds the configured byte limit")
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            return {}
        return value if isinstance(value, dict) else {}

    def has_session_start(value: object) -> bool:
        if isinstance(value, str):
            return value == "SessionStart"
        if isinstance(value, dict):
            return any(has_session_start(item) for item in value.values())
        if isinstance(value, list):
            return any(has_session_start(item) for item in value)
        return False

    def wait_for(predicate: Any) -> None:
        nonlocal session_starts, successful_results
        while True:
            value = receive()
            if has_session_start(value):
                session_starts += 1
            if value.get("type") == "result" and value.get("is_error") is True:
                raise RunnerError("Claude provider returned an unsuccessful result boundary")
            if predicate(value):
                if value.get("type") == "result":
                    successful_results += 1
                return

    error = ""
    try:
        write_line(prompts[0])
        wait_for(lambda value: value.get("type") == "result" and value.get("is_error") is not True)
        write_line(prompts[1])
        wait_for(lambda value: value.get("type") == "result" and value.get("is_error") is not True)
        prior_session_starts = session_starts
        write_line(prompts[2])
        wait_for(lambda _value: session_starts > prior_session_starts)
        write_line(prompts[3])
        wait_for(lambda value: value.get("type") == "result" and value.get("is_error") is not True)
    except (OSError, RunnerError, TimeoutError, ValueError) as caught:
        error = f"{type(caught).__name__}: {caught}"
    finally:
        with contextlib.suppress(OSError):
            if process.stdin is not None:
                process.stdin.close()
        group_gone = terminate_group(process.pid)
        with contextlib.suppress(subprocess.TimeoutExpired):
            process.wait(timeout=3)
        stdout_drain_joined = True
        if process.stdout is not None:
            descriptor = process.stdout.fileno()
            os.set_blocking(descriptor, False)
            drain_deadline = time.monotonic() + 3
            while time.monotonic() < drain_deadline:
                readable, _, _ = select.select([descriptor], [], [], min(0.1, max(0.0, drain_deadline - time.monotonic())))
                if not readable:
                    continue
                try:
                    chunk = os.read(descriptor, 64 * 1024)
                except BlockingIOError:
                    continue
                if not chunk:
                    break
                stdout_total += len(chunk)
                if len(stdout_buffer) < max_output:
                    stdout_buffer.extend(chunk[: max_output - len(stdout_buffer)])
            else:
                stdout_drain_joined = False
            if stdout_line_buffer:
                stdout_total += len(stdout_line_buffer)
                if len(stdout_buffer) < max_output:
                    stdout_buffer.extend(stdout_line_buffer[: max_output - len(stdout_buffer)])
        stderr_thread.join(timeout=3)
    completed_steps = successful_results == 3 and session_starts >= 1 and not error
    return {"argv": claude_argv, "pid": process.pid, "exit": process.returncode if process.returncode is not None else 124, "timed_out": "deadline" in error.lower(), "process_group_gone": group_gone, "duration_ms": round((time.monotonic() - started) * 1000, 1), "stdout": bytes(stdout_buffer), "stderr": stderr_result.get("bytes", b""), "stdout_complete": stdout_total <= max_output, "stderr_complete": stderr_result.get("complete", True), "stdout_total_bytes": stdout_total, "stderr_total_bytes": stderr_result.get("total", 0), "drain_joined": (not stderr_thread.is_alive()) and stdout_drain_joined, "writer_error": [], "native_boundary_error": error, "completed_steps": completed_steps, "successfulTerminalResults": successful_results, "nativeResetObserved": session_starts >= 1, "intentional_shutdown": bool(completed_steps and process.returncode not in (None, 0))}


def run_live_claude(workbench: Path, claude: Path, auth: Path, output: Path, deadline: float, max_output: int, temp_root: Path, identity: dict[str, object], case_name: str = "enabled") -> dict[str, object]:
    session_id = f"claude-live-{case_name}"
    case = output / "sessions" / session_id
    marker = "WCTX_CLAUDE_LIVE_" + secrets.token_hex(8)
    paths = make_fixture(case, marker)
    if case_name == "disabled":
        paths["project"].joinpath("workbench-context.pkl").write_text(paths["project"].joinpath("workbench-context.pkl").read_text(encoding="utf-8") + "\nenabled = false\n", encoding="utf-8")
    elif case_name == "no-source":
        paths["project"].joinpath("workbench-context.pkl").unlink(missing_ok=True)
        paths["home_config"].unlink(missing_ok=True)
    hook_file = paths["evidence"] / "hook-io.jsonl"
    hook_overflow = paths["evidence"] / "hook-overflow.jsonl"
    wrapper = temp_root / "workbench-hook-live-claude.py"
    make_hook_wrapper(wrapper, workbench, hook_file, max_output, deadline)
    env = isolated_env(case)
    credential = copy_private(auth, case / "claude-config" / ".credentials.json")
    prompt = {
        "irrelevant": "Read only OTHER.md with the normal Read tool, then give one sentence. Do not read other files or change files.",
        "failed-read": "Read only DOES_NOT_EXIST.md with the normal Read tool, then report the failure in one sentence. Do not read other files or change files.",
    }.get(case_name, "Read only README.md with the normal Read tool, then give one sentence. Do not read other files or change files.")
    prompts = [prompt]
    if case_name == "enabled":
        prompts = [prompt, "In this same session, read README.md again with the tool, then give one sentence. Do not read other files or change files.", "/clear", "After that native clear/reset, read README.md again with the tool, then give one sentence. Do not read other files or change files."]
    capture_id = "qa-claude-live-" + secrets.token_hex(10)
    claude_result: dict[str, object] = {}
    try:
        env.update({"WCTX_CAPTURE_REAL_WORKBENCH": str(workbench), "WCTX_CAPTURE_HOOK_FILE": str(hook_file), "WCTX_CAPTURE_OVERFLOW_FILE": str(hook_overflow), "WCTX_CAPTURE_MAX_BYTES": str(max_output), "WCTX_CAPTURE_LOG_MAX_BYTES": str(max_output * 32), "WCTX_CAPTURE_HOOK_TIMEOUT": str(deadline)})
        setup = run_workbench(workbench, ["setup", "--harness", "claude", "--executable", str(wrapper), "--claude-settings", str(paths["settings"]), "--json"], paths, env, deadline, max_output)
        require_bounded_command(setup, paths["evidence"], "workbench-setup", max_output)
        claude_argv = [str(claude), "--print", "--model", "sonnet", "--max-turns", "20", "--max-budget-usd", "3", "--setting-sources", "", "--settings", str(paths["settings"]), "--strict-mcp-config", "--mcp-config", '{"mcpServers":{}}', "--tools", "Read", "--allowedTools", "Read", "--permission-prompts", "none", "--no-session-persistence", "--output-format", "stream-json", "--verbose", "--include-hook-events"]
        input_bytes = None
        if len(prompts) == 1:
            claude_argv.insert(2, prompts[0])
        else:
            claude_argv.extend(["--input-format", "stream-json"])
            input_bytes = b"".join((json.dumps({"type": "user", "message": {"role": "user", "content": prompt}, "parent_tool_use_id": None}, separators=(",", ":")) + "\n").encode() for prompt in prompts)
        claude_result = claude_interactive(claude_argv, prompts, env=env, cwd=paths["project"], timeout=deadline, max_output=max_output) if len(prompts) > 1 else run_process(claude_argv, env=env, cwd=paths["project"], timeout=deadline, max_output=max_output, input_bytes=input_bytes)
        transcript = write_bytes(paths["evidence"] / "transcript.raw.jsonl", claude_result["stdout"], max_output)
        native = copy_bounded_file(hook_file, paths["evidence"] / "native.jsonl", max_output) if hook_file.exists() else write_bytes(paths["evidence"] / "native.jsonl", b"", max_output)
        require_process_cleanup(claude_result, paths["evidence"], "live-claude", max_output)
        native_messages = decode_json_lines(claude_result["stdout"])
        checkpoints = write_observation(paths["evidence"] / "native-checkpoints.jsonl", native_checkpoint_records(native_messages, "claude-native-boundary"), max_output)
        native_sessions = []
        for message in native_messages:
            value = message.get("session_id")
            if isinstance(value, str) and value and value not in native_sessions:
                native_sessions.append(value)
        reset_event = next((message.get("uuid") for message in native_messages if message.get("type") == "conversation_reset" and isinstance(message.get("uuid"), str)), None)
        input_metadata = [{"sessionId": native_sessions[0]} if native_sessions else {}, {"sessionId": native_sessions[0]} if native_sessions else {}, {"boundary": "control", "sessionId": native_sessions[0], "nativeEventId": reset_event}, ({"boundary": "reset", "sessionId": native_sessions[-1], "nativeEventId": reset_event} if reset_event and len(native_sessions) > 1 else {"boundary": "unknown", "sessionId": native_sessions[-1]} if len(native_sessions) > 1 else {"boundary": "unknown"})]
        inputs = live_inputs(paths["evidence"] / "inputs.jsonl", session_id, prompts, max_output, input_metadata)
        wire = unavailable_wire(paths["evidence"] / "wire-requests.jsonl", "Claude live CLI does not expose the exact provider request bytes through this runner.", max_output)
        provider_state = provider_status(claude_result)
        before_inspection = {"runtimeExists": paths["runtime"].exists(), "cacheExists": paths["cache"].exists()}
        observations = live_workbench_observations(workbench, paths, env, deadline, max_output, inactive=case_name in {"disabled", "no-source"}, before_inspection=before_inspection)
        runtime_cleanup = cleanup_runtime(workbench, paths, paths["evidence"], max_output, f"live-claude-{case_name}-workbench")
        records = read_hook_records(hook_file)
        try:
            history_value = json.loads(observations["history"]["stdout"])
        except (TypeError, json.JSONDecodeError):
            history_value = None
        expected_path = {"irrelevant": "OTHER.md", "failed-read": "DOES_NOT_EXIST.md"}.get(case_name, "README.md")
        confirmed_ordinal, confirmed_ids = confirmed_workbench_record(history_value, expected_path, {"PostToolBatch"})
        admitted = confirmed_ordinal is not None
        delivered = admitted and provider_state == "passed"
        verdicts, verdict_evidence, unknowns = live_verdicts(delivered, wire_path=f"sessions/{session_id}/evidence/wire-requests.jsonl", delivered_locator={"filePath": f"sessions/{session_id}/evidence/workbench-history.jsonl", "recordOrdinal": confirmed_ordinal, "sourcePointer": "/Outcome", "causalIds": confirmed_ids} if delivered else None)
        if case_name == "enabled":
            unknowns.append(unknown(f"sessions.{session_id}.inputs[3].causal.epoch", "unavailable", "Native reset/session identifiers are retained, but Workbench epoch continuity was not exposed; no epoch is inferred."))
            if not reset_event:
                unknowns.append(unknown(f"sessions.{session_id}.inputs[3].boundary", "unavailable", "Claude reset-to-next-request relation was not named by a retained native reset identifier."))
        if provider_state != "passed":
            unknowns.append(unknown(f"sessions.{session_id}.providerExecution", "provider-omitted", f"Provider case was {provider_state}; no delivery, receipt, adoption, or step coverage is claimed."))
        if not transcript["complete"]:
            unknowns.append(unknown(f"sessions.{session_id}.canonicalTranscript", "truncated", "Claude stream exceeded the configured byte limit."))
        if not native["complete"] or hook_overflow.exists():
            unknowns.append(unknown(f"sessions.{session_id}.sidecars.native-events", "truncated", "Native Claude hook capture exceeded the configured byte limit."))
        if not inputs["complete"]:
            unknowns.append(unknown(f"sessions.{session_id}.inputs[0].capture", "truncated", "Authored Claude input exceeded the configured byte limit."))
        manifest = live_manifest(output, "claude", "claude-stream-json", workbench, claude, capture_id, session_id, paths["evidence"] / "transcript.raw.jsonl", bool(transcript["complete"]), f"sessions/{session_id}/evidence", [(paths["evidence"] / "native.jsonl", "native-events", bool(native["complete"])), (paths["evidence"] / "native-checkpoints.jsonl", "native-checkpoints", bool(checkpoints["complete"])), (paths["evidence"] / "workbench-observations.jsonl", "workbench-observations", bool(observations["file"]["complete"])), (paths["evidence"] / "workbench-history.jsonl", "workbench-history", bool(observations["historyFile"]["complete"])), (paths["evidence"] / "runtime-cleanup.jsonl", "other", bool(runtime_cleanup["complete"])), (paths["evidence"] / "wire-requests.jsonl", "other", bool(wire["complete"]))], paths["evidence"] / "inputs.jsonl", bool(inputs["complete"]), len(prompts), verdicts, verdict_evidence, unknowns, max_output, identity)
        return {"sessionId": session_id, "provider": "claude", "case": case_name, "setupExit": setup["exit"], "providerExit": claude_result["exit"], "providerStatus": provider_state, "shutdownClassification": "intentional-joined" if claude_result.get("intentional_shutdown") else "none", "successfulTerminalResults": claude_result.get("successfulTerminalResults", 0), "matrixStatus": provider_state, "delivered": delivered, "manifest": manifest, "credentialRemoved": True}
    except (OSError, RunnerError, TimeoutError, ValueError) as error:
        cleanup_records, _ = bounded_jsonl(paths["evidence"] / "runtime-cleanup.jsonl", max_output)
        if cleanup_records and cleanup_records[-1].get("complete") is not True:
            raise RunnerError("live Claude runtime cleanup failed; retained runtime-cleanup.jsonl") from error
        cleanup_runtime_after_failure(workbench, paths, max_output, f"live-claude-{case_name}-workbench")
        return incomplete_live_case(output, "claude", claude, workbench, identity, paths, session_id, capture_id, prompt, max_output, error, provider_result=claude_result)
    finally:
        remove_owned(credential, paths["evidence"], max_output, "claude-credential")


def codex_wait_for(client: CodexClient, predicate: Any) -> dict[str, object]:
    while True:
        message = client.receive()
        method = message.get("method")
        if method in {"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval"}:
            client.approval(message)
            continue
        if method and message.get("id") is not None:
            raise RunnerError(f"unsupported Codex server request: {method}")
        if predicate(message):
            return message


def codex_wait_compaction(client: CodexClient) -> dict[str, object]:
    seen = {"statusObserved": False, "turnStarted": False, "itemStarted": False, "itemCompleted": False, "idle": False, "turnCompleted": False}
    compaction_turn_id: str | None = None
    compaction_id: str | None = None
    started_turn_ids: set[str] = set()

    def observe(message: dict[str, object]) -> None:
        nonlocal compaction_turn_id, compaction_id
        method = message.get("method")
        params = message.get("params", {})
        item = params.get("item", {}) if isinstance(params, dict) else {}
        if method == "thread/status/changed":
            seen["statusObserved"] = True
        if method == "turn/started" and isinstance(params, dict):
            turn = params.get("turn")
            if isinstance(turn, dict) and isinstance(turn.get("id"), str):
                started_turn_ids.add(turn["id"])
                if compaction_turn_id is not None and turn["id"] == compaction_turn_id:
                    seen["turnStarted"] = True
        if method == "item/started" and isinstance(item, dict) and item.get("type") == "contextCompaction":
            seen["itemStarted"] = True
            if isinstance(params, dict) and isinstance(params.get("turnId"), str):
                compaction_turn_id = params["turnId"]
            if isinstance(item, dict) and isinstance(item.get("id"), str):
                compaction_id = item["id"]
            if compaction_turn_id in started_turn_ids:
                seen["turnStarted"] = True
        if method == "item/completed" and isinstance(item, dict) and item.get("type") == "contextCompaction":
            seen["itemCompleted"] = True
        if method == "thread/status/changed" and isinstance(params, dict):
            status = params.get("status")
            if isinstance(status, dict) and status.get("type") == "idle" and compaction_turn_id is not None:
                seen["idle"] = True
        if method == "turn/completed" and isinstance(params, dict):
            turn = params.get("turn")
            if isinstance(turn, dict) and compaction_turn_id is not None and turn.get("id") == compaction_turn_id:
                seen["turnCompleted"] = True

    for existing in client.messages:
        observe(existing)
    if compaction_turn_id is not None:
        for existing in client.messages:
            observe(existing)
    while not all(seen.values()):
        message = client.receive()
        method = message.get("method")
        if method in {"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval"}:
            client.approval(message)
            continue
        if method and message.get("id") is not None:
            raise RunnerError(f"unsupported Codex server request: {method}")
        observe(message)
    return {**seen, "turnId": compaction_turn_id, "compactionId": compaction_id}


def bounded_file_complete(path: Path, limit: int) -> bool:
    try:
        return path.stat().st_size <= limit
    except OSError:
        return False


def incomplete_live_case(output: Path, provider: str, provider_path: Path, workbench: Path, identity: dict[str, object], paths: dict[str, Path], session_id: str, capture_id: str, prompt: str, max_output: int, error: BaseException, native_messages: list[dict[str, object]] | None = None, provider_result: dict[str, object] | None = None) -> dict[str, object]:
    evidence = paths["evidence"]
    source = evidence / "transcript.raw.jsonl"
    rpc_stdout = evidence / "rpc-stdout.jsonl"
    provider_bytes = provider_result.get("stdout", b"") if provider_result else b""
    if not source.exists():
        write_bytes(source, provider_bytes if provider_bytes else rpc_stdout.read_bytes() if rpc_stdout.exists() else b"", max_output)
    native = evidence / "native.jsonl"
    if not native.exists():
        write_bytes(native, b"", max_output)
    checkpoints = evidence / "native-checkpoints.jsonl"
    if not checkpoints.exists():
        write_observation(checkpoints, native_checkpoint_records(native_messages or [], f"{provider}-incomplete"), max_output)
    inputs = evidence / "inputs.jsonl"
    if not inputs.exists():
        live_inputs(inputs, session_id, [prompt], max_output)
    wire = evidence / "wire-requests.jsonl"
    if not wire.exists():
        unavailable_wire(wire, f"{provider} case ended before exact model request capture was available.", max_output)
    observations = evidence / "workbench-observations.jsonl"
    if not observations.exists():
        write_observation(observations, [], max_output)
    history = evidence / "workbench-history.jsonl"
    if not history.exists():
        write_observation(history, [], max_output)
    source_complete = bounded_file_complete(source, max_output)
    inputs_complete = bounded_file_complete(inputs, max_output)
    wire_complete = bounded_file_complete(wire, max_output)
    verdicts, verdict_evidence, unknowns = live_verdicts(False, wire_path=f"sessions/{session_id}/evidence/wire-requests.jsonl")
    detail = f"Live {provider} case was not completed: {type(error).__name__}: {error}"
    unknowns.append(unknown(f"sessions.{session_id}.providerExecution", "provider-omitted", detail))
    error_text = str(error).lower()
    observed_exit = provider_result.get("exit") if provider_result else None
    status = "approval-timeout" if "approval" in error_text or "decision" in error_text else ("provider-nonzero" if isinstance(observed_exit, int) and observed_exit != 0 else "not-exercised")
    if not source_complete:
        unknowns.append(unknown(f"sessions.{session_id}.canonicalTranscript", "truncated", "Incomplete case transcript exceeded the configured byte limit."))
    if not inputs_complete:
        unknowns.append(unknown(f"sessions.{session_id}.inputs", "truncated", "Incomplete case input sidecar exceeded the configured byte limit."))
    if not wire_complete:
        unknowns.append(unknown(f"sessions.{session_id}.sidecars.wire-requests", "truncated", "Incomplete case wire sidecar exceeded the configured byte limit."))
    if provider_result and provider_result.get("stdout_complete") is False:
        unknowns.append(unknown(f"sessions.{session_id}.canonicalTranscript", "truncated", "Provider stdout exceeded the configured byte limit before case completion."))
    if provider_result and (provider_result.get("stderr_complete") is False or provider_result.get("stderrComplete") is False):
        unknowns.append(unknown(f"sessions.{session_id}.providerExecution", "truncated", "Provider stderr exceeded the configured byte limit before case completion."))
    step_evidence = native_command_step(native_messages or [], session_id.removeprefix(f"{provider}-live-"), paths["project"]) if provider == "codex" else {"required": False, "exercised": False, "status": "not-required", "locators": []}
    if step_evidence.get("required") and not step_evidence.get("exercised"):
        unknowns.append(unknown(f"sessions.{session_id}.stepExecution", "not-captured", str(step_evidence.get("reason", "Required native command evidence was not retained."))))
    sidecars = [(native, "native-events", bounded_file_complete(native, max_output)), (checkpoints, "native-checkpoints", bounded_file_complete(checkpoints, max_output)), (observations, "workbench-observations", bounded_file_complete(observations, max_output)), (history, "workbench-history", bounded_file_complete(history, max_output)), (evidence / "rpc-stdin.jsonl", "native-rpc", bounded_file_complete(evidence / "rpc-stdin.jsonl", MAX_CODEX_EVIDENCE)), (evidence / "rpc-stdout.jsonl", "native-rpc", bounded_file_complete(evidence / "rpc-stdout.jsonl", MAX_CODEX_EVIDENCE)), (wire, "other", wire_complete)]
    stderr_capture = evidence / "app-server-stderr.txt"
    if stderr_capture.exists():
        stderr_complete = bool(provider_result.get("stderrComplete")) if provider_result and "stderrComplete" in provider_result else bounded_file_complete(stderr_capture, MAX_CODEX_EVIDENCE)
        sidecars.append((stderr_capture, "native-rpc", stderr_complete))
    runtime_cleanup = evidence / "runtime-cleanup.jsonl"
    if runtime_cleanup.exists():
        cleanup_records, cleanup_complete = bounded_jsonl(runtime_cleanup, max_output)
        cleanup_ok = cleanup_complete and bool(cleanup_records) and cleanup_records[-1].get("complete") is True and cleanup_records[-1].get("status") == "clean"
        sidecars.append((runtime_cleanup, "other", cleanup_ok))
    sidecars = [item for item in sidecars if item[0].exists()]
    approval_log = evidence / "approval-events.jsonl"
    if approval_log.exists():
        sidecars.append((approval_log, "other", True))
    if step_evidence.get("exercised"):
        verdict_evidence["stepExecution"] = [item["locator"] for item in step_evidence["locators"]]
    manifest = live_manifest(output, provider, "unknown", workbench, provider_path, capture_id, session_id, source, source_complete, f"sessions/{session_id}/evidence", sidecars, inputs, inputs_complete, 1, verdicts, verdict_evidence, unknowns, max_output, identity, step_evidence)
    return {"sessionId": session_id, "provider": provider, "case": session_id.removeprefix(f"{provider}-live-"), "providerStatus": status, "matrixStatus": status, "providerExit": observed_exit, "providerCleanup": provider_result or {}, "stepStatus": step_evidence["status"], "stepEvidence": step_evidence, "delivered": False, "error": detail, "manifest": manifest, "credentialRemoved": True}


def run_live_codex(workbench: Path, codex: Path, auth: Path, output: Path, deadline: float, max_output: int, temp_root: Path, identity: dict[str, object], case_name: str = "enabled") -> dict[str, object]:
    session_id = f"codex-live-{case_name}"
    case = output / "sessions" / session_id
    marker = "WCTX_CODEX_LIVE_" + secrets.token_hex(8)
    paths = make_fixture(case, marker)
    if case_name == "disabled":
        paths["project"].joinpath("workbench-context.pkl").write_text(paths["project"].joinpath("workbench-context.pkl").read_text(encoding="utf-8") + "\nenabled = false\n", encoding="utf-8")
    elif case_name == "no-source":
        paths["project"].joinpath("workbench-context.pkl").unlink(missing_ok=True)
        paths["home_config"].unlink(missing_ok=True)
    paths["codex"] = case / "codex"
    paths["codex"].mkdir(mode=0o700)
    paths["zsh"] = case / "zsh"
    paths["zsh"].mkdir(mode=0o700)
    hook_file = paths["evidence"] / "hook-io.jsonl"
    hook_overflow = paths["evidence"] / "hook-overflow.jsonl"
    wrapper = temp_root / "workbench-hook-live-codex.py"
    make_hook_wrapper(wrapper, workbench, hook_file, max_output, deadline)
    env = isolated_env(case)
    env.update({"CODEX_HOME": str(paths["codex"]), "ZDOTDIR": str(paths["zsh"] )})
    credential = copy_private(auth, paths["codex"] / "auth.json")
    capture_id = "qa-codex-live-" + secrets.token_hex(10)
    client: CodexClient | None = None
    shutdown_info: dict[str, object] | None = None
    prompt = {
        "irrelevant": "Read only OTHER.md with the normal file-read tool, then give one sentence. Do not read other files or change files.",
        "failed-read": "Read only DOES_NOT_EXIST.md with the normal file-read tool, then report the failure in one sentence. If the sandbox blocks this exact read, request approval once for this same read; do not substitute another file or report a missing-file result without an actual command execution. Do not read other files or change files.",
    }.get(case_name, "Read only README.md with the normal file-read tool, then give one sentence. Do not read other files or change files.")
    try:
        env.update({"WCTX_CAPTURE_REAL_WORKBENCH": str(workbench), "WCTX_CAPTURE_HOOK_FILE": str(hook_file), "WCTX_CAPTURE_OVERFLOW_FILE": str(hook_overflow), "WCTX_CAPTURE_MAX_BYTES": str(max_output), "WCTX_CAPTURE_LOG_MAX_BYTES": str(max_output * 32), "WCTX_CAPTURE_HOOK_TIMEOUT": str(deadline)})
        setup = run_workbench(workbench, ["setup", "--harness", "codex", "--executable", str(wrapper), "--codex-home", str(paths["codex"]), "--json"], paths, env, deadline, max_output)
        require_bounded_command(setup, paths["evidence"], "workbench-setup", max_output)
        init = {"exit": 0} if case_name == "no-source" else run_workbench(workbench, ["init", "--json"], paths, env, deadline, max_output)
        if case_name != "no-source":
            require_bounded_command(init, paths["evidence"], "workbench-init", max_output)
        client = CodexClient([str(codex), "--dangerously-bypass-hook-trust", "app-server", "--listen", "stdio://"], cwd=paths["project"], env=env, evidence=paths["evidence"], approvals=paths["evidence"] / "approvals", deadline=deadline, max_evidence=MAX_CODEX_EVIDENCE)
        client.request(1, "initialize", {"clientInfo": {"name": "workbench-context-live-qa", "version": "1"}, "capabilities": {"experimentalApi": True}})
        client.send({"jsonrpc": "2.0", "method": "initialized", "params": {}})
        client.request(2, "hooks/list", {"cwds": [str(paths["project"])]})
        fixture_environment = write_observation(paths["evidence"] / "fixture-environment.jsonl", [{"threadPATH": CODEX_THREAD_PATH, "providerLauncherPathPreserved": True, "source": "thread/start shell_environment_policy.set", "credentialFree": True}], max_output)
        thread = client.request(3, "thread/start", {"model": "gpt-5.6-luna", "cwd": str(paths["project"]), "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": "workspace-write", "serviceTier": "fast", "config": {"bypass_hook_trust": True, "allow_login_shell": False, "shell_environment_policy.inherit": "none", "shell_environment_policy.set": {"HOME": str(case), "CODEX_HOME": str(paths["codex"]), "ZDOTDIR": str(paths["zsh"]), "PATH": CODEX_THREAD_PATH}}})
        thread_id = thread.get("result", {}).get("thread", {}).get("id")
        if not isinstance(thread_id, str) or not thread_id:
            raise RunnerError("Codex thread/start did not return a thread id")
        rollout_value = thread.get("result", {}).get("thread", {}).get("path")
        rollout_path = Path(rollout_value).resolve() if isinstance(rollout_value, str) and rollout_value else None
        codex_home = paths["codex"].resolve()
        if rollout_path is not None and (rollout_path != codex_home and codex_home not in rollout_path.parents):
            raise RunnerError("Codex rollout path escaped the isolated CODEX_HOME")
        turn = client.request(4, "turn/start", {"threadId": thread_id, "input": [{"type": "text", "text": prompt}], "cwd": str(paths["project"]), "model": "gpt-5.6-luna", "effort": "high", "serviceTier": "fast", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandboxPolicy": {"type": "workspaceWrite", "writableRoots": [str(paths["project"])], "networkAccess": False}})
        turn_id = turn.get("result", {}).get("turn", {}).get("id")
        if not isinstance(turn_id, str):
            raise RunnerError("Codex turn/start did not return a turn id")
        codex_wait_for(client, lambda item: item.get("method") == "turn/completed" and item.get("params", {}).get("turn", {}).get("id") == turn_id)
        repeat_observed = False
        compact_seen: dict[str, object] = {}
        if case_name == "enabled":
            repeat = client.request(5, "turn/start", {"threadId": thread_id, "input": [{"type": "text", "text": "Read README.md again with the tool, then give one sentence. Do not read other files or change files."}], "cwd": str(paths["project"]), "model": "gpt-5.6-luna", "effort": "high", "serviceTier": "fast", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandboxPolicy": {"type": "workspaceWrite", "writableRoots": [str(paths["project"])], "networkAccess": False}})
            repeat_id = repeat.get("result", {}).get("turn", {}).get("id")
            if not isinstance(repeat_id, str):
                raise RunnerError("Codex repeat turn/start did not return a turn id")
            codex_wait_for(client, lambda item: item.get("method") == "turn/completed" and item.get("params", {}).get("turn", {}).get("id") == repeat_id)
            repeat_observed = True
            client.request(6, "thread/compact/start", {"threadId": thread_id})
            compact_seen = codex_wait_compaction(client)
            after = client.request(7, "turn/start", {"threadId": thread_id, "input": [{"type": "text", "text": "After native compaction, read README.md again with the tool, then give one sentence. Do not read other files or change files."}], "cwd": str(paths["project"]), "model": "gpt-5.6-luna", "effort": "high", "serviceTier": "fast", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandboxPolicy": {"type": "workspaceWrite", "writableRoots": [str(paths["project"])], "networkAccess": False}})
            after_id = after.get("result", {}).get("turn", {}).get("id")
            if not isinstance(after_id, str):
                raise RunnerError("Codex post-compaction turn/start did not return a turn id")
            codex_wait_for(client, lambda item: item.get("method") == "turn/completed" and item.get("params", {}).get("turn", {}).get("id") == after_id)
        native_messages = list(client.messages)
        if not client.close():
            write_observation(paths["evidence"] / "cleanup-failure.jsonl", [{"component": "codex-app-server", "reason": "owned Codex process group did not terminate and join"}], max_output)
            raise RunnerError("Codex cleanup was incomplete; retained cleanup-failure.jsonl")
        shutdown_info = dict(client.shutdown_info)
        client = None
        if shutdown_info.get("unexpectedExit"):
            raise RunnerError(f"Codex app-server exited unexpectedly with status {shutdown_info.get('exit')}")
        before_inspection = {"runtimeExists": paths["runtime"].exists(), "cacheExists": paths["cache"].exists()}
        observations = live_workbench_observations(workbench, paths, env, deadline, max_output, inactive=case_name in {"disabled", "no-source"}, before_inspection=before_inspection)
        runtime_cleanup = cleanup_runtime(workbench, paths, paths["evidence"], max_output, f"live-codex-{case_name}-workbench")
        native = copy_bounded_file(hook_file, paths["evidence"] / "native.jsonl", max_output) if hook_file.exists() else write_bytes(paths["evidence"] / "native.jsonl", b"", max_output)
        checkpoints = write_observation(paths["evidence"] / "native-checkpoints.jsonl", native_checkpoint_records(native_messages, "codex-rpc-boundary"), max_output)
        rollout_captured = rollout_path is not None and rollout_path.exists()
        if rollout_captured:
            transcript = copy_bounded_file(rollout_path, paths["evidence"] / "transcript.raw.jsonl", max_output)
        else:
            rpc_path = paths["evidence"].joinpath("rpc-stdout.jsonl")
            transcript = write_bytes(paths["evidence"] / "transcript.raw.jsonl", rpc_path.read_bytes() if rpc_path.exists() else b"", max_output)
        input_prompts = [prompt] + (["Read README.md again with the tool, then give one sentence. Do not read other files or change files.", "After native compaction, read README.md again with the tool, then give one sentence. Do not read other files or change files."] if case_name == "enabled" else [])
        input_metadata = [{"sessionId": thread_id, "turnId": turn_id}]
        if case_name == "enabled":
            compaction_input = {"sessionId": thread_id, "turnId": after_id}
            if isinstance(compact_seen.get("compactionId"), str) and compact_seen.get("compactionId"):
                compaction_input.update({"boundary": "compaction", "compactionId": compact_seen["compactionId"]})
            else:
                compaction_input["boundary"] = "unknown"
            input_metadata.extend([{"sessionId": thread_id, "turnId": repeat_id}, compaction_input])
        inputs = live_inputs(paths["evidence"] / "inputs.jsonl", session_id, input_prompts, max_output, input_metadata)
        wire = unavailable_wire(paths["evidence"] / "wire-requests.jsonl", "Codex app-server does not expose the upstream model request bytes to this runner.", max_output)
        records = read_hook_records(hook_file)
        try:
            history_value = json.loads(observations["history"]["stdout"])
        except (TypeError, json.JSONDecodeError):
            history_value = None
        expected_path = {"irrelevant": "OTHER.md", "failed-read": "DOES_NOT_EXIST.md"}.get(case_name, "README.md")
        confirmed_ordinal, confirmed_ids = confirmed_workbench_record(history_value, expected_path, {"PostToolUse"})
        step_evidence = native_command_step(native_messages, case_name, paths["project"])
        admitted = confirmed_ordinal is not None
        delivered = admitted and step_evidence["exercised"]
        verdicts, verdict_evidence, unknowns = live_verdicts(delivered, wire_path=f"sessions/{session_id}/evidence/wire-requests.jsonl", delivered_locator={"filePath": f"sessions/{session_id}/evidence/workbench-history.jsonl", "recordOrdinal": confirmed_ordinal, "sourcePointer": "/Outcome", "causalIds": confirmed_ids} if delivered else None)
        if step_evidence["exercised"]:
            verdict_evidence["stepExecution"] = [item["locator"] for item in step_evidence["locators"]]
        elif step_evidence.get("required"):
            unknowns.append(unknown(f"sessions.{session_id}.stepExecution", "not-captured", str(step_evidence["reason"])))
        if case_name == "enabled":
            unknowns.append(unknown(f"sessions.{session_id}.inputs[2].causal.epoch", "unavailable", "Native compaction identity is retained, but Workbench epoch continuity was not exposed; no epoch is inferred."))
            if not compact_seen.get("compactionId"):
                unknowns.append(unknown(f"sessions.{session_id}.inputs[2].boundary", "unavailable", "Codex post-compaction input lacked a retained native compaction identifier."))
        if not transcript["complete"]:
            unknowns.append(unknown(f"sessions.{session_id}.canonicalTranscript", "truncated", "Codex app-server output exceeded the configured byte limit."))
        if not rollout_captured:
            unknowns.append(unknown(f"sessions.{session_id}.canonicalTranscript", "not-captured", "Codex did not expose an isolated rollout file; the retained app-server stream is not promoted to codex-rollout provenance."))
        if not native["complete"] or hook_overflow.exists():
            unknowns.append(unknown(f"sessions.{session_id}.sidecars.native-events", "truncated", "Native Codex hook capture exceeded the configured byte limit."))
        stderr_complete = bool(shutdown_info.get("stderrComplete"))
        if not stderr_complete:
            unknowns.append(unknown(f"sessions.{session_id}.providerExecution", "truncated", "Codex app-server stderr exceeded the configured byte limit before case completion."))
        codex_sidecars = [(paths["evidence"] / "native.jsonl", "native-events", bool(native["complete"])), (paths["evidence"] / "native-checkpoints.jsonl", "native-checkpoints", bool(checkpoints["complete"])), (paths["evidence"] / "workbench-observations.jsonl", "workbench-observations", bool(observations["file"]["complete"])), (paths["evidence"] / "workbench-history.jsonl", "workbench-history", bool(observations["historyFile"]["complete"])), (paths["evidence"] / "runtime-cleanup.jsonl", "other", bool(runtime_cleanup["complete"])), (paths["evidence"] / "fixture-environment.jsonl", "other", bool(fixture_environment["complete"])), (paths["evidence"] / "rpc-stdin.jsonl", "native-rpc", True), (paths["evidence"] / "rpc-stdout.jsonl", "native-rpc", True), (paths["evidence"] / "app-server-stderr.txt", "native-rpc", stderr_complete), (paths["evidence"] / "wire-requests.jsonl", "other", bool(wire["complete"]))]
        if (paths["evidence"] / "approval-events.jsonl").exists():
            codex_sidecars.append((paths["evidence"] / "approval-events.jsonl", "other", True))
        manifest = live_manifest(output, "codex", "codex-rollout" if rollout_captured else "unknown", workbench, codex, capture_id, session_id, paths["evidence"] / "transcript.raw.jsonl", bool(transcript["complete"]), f"sessions/{session_id}/evidence", codex_sidecars, paths["evidence"] / "inputs.jsonl", bool(inputs["complete"]), len(input_prompts), verdicts, verdict_evidence, unknowns, max_output, identity, step_evidence)
        matrix_status = "passed" if step_evidence["exercised"] else "not-exercised"
        return {"sessionId": session_id, "provider": "codex", "case": case_name, "setupExit": setup["exit"], "initExit": init["exit"], "threadId": thread_id, "repeatTurnObserved": repeat_observed, "compactObserved": bool(compact_seen), "postCompactTurnObserved": case_name == "enabled", "providerExit": shutdown_info.get("exit") if shutdown_info else None, "providerStatus": "passed", "stepStatus": step_evidence["status"], "stepEvidence": step_evidence, "matrixStatus": matrix_status, "delivered": delivered, "manifest": manifest, "credentialRemoved": True}
    except (OSError, RunnerError, TimeoutError, ValueError) as error:
        messages = list(client.messages) if client is not None else []
        if client is not None:
            if not client.close():
                write_observation(paths["evidence"] / "cleanup-failure.jsonl", [{"component": "codex-app-server", "reason": "owned Codex process group, stderr drain, or stderr retention did not join"}], max_output)
                raise RunnerError("Codex cleanup was incomplete; retained cleanup-failure.jsonl")
            shutdown_info = dict(client.shutdown_info)
            client = None
        cleanup_records, _ = bounded_jsonl(paths["evidence"] / "runtime-cleanup.jsonl", max_output)
        if cleanup_records and cleanup_records[-1].get("complete") is not True:
            raise RunnerError("live Codex runtime cleanup failed; retained runtime-cleanup.jsonl") from error
        cleanup_runtime_after_failure(workbench, paths, max_output, f"live-codex-{case_name}-workbench")
        return incomplete_live_case(output, "codex", codex, workbench, identity, paths, session_id, capture_id, prompt, max_output, error, messages, shutdown_info)
    finally:
        if client is not None:
            if not client.close():
                write_observation(paths["evidence"] / "cleanup-failure.jsonl", [{"component": "codex-app-server", "reason": "owned Codex process group, stderr drain, or stderr retention did not join"}], max_output)
                raise RunnerError("Codex cleanup was incomplete; retained cleanup-failure.jsonl")
        remove_owned(credential, paths["evidence"], max_output, "codex-credential")


def parser() -> argparse.ArgumentParser:
    value = argparse.ArgumentParser(description="Capture real Workbench context native-hook QA into a v1 bundle.")
    value.add_argument("--workbench", type=Path, help="absolute Workbench executable")
    value.add_argument("--claude", type=Path, help="absolute Claude executable for Claude harness")
    value.add_argument("--codex", type=Path, help="absolute Codex executable for Codex harness")
    value.add_argument("--claude-auth", type=Path, help="private source credential file copied into an isolated Claude config for live mode")
    value.add_argument("--codex-auth", type=Path, help="private source credential file copied into an isolated Codex home for live mode")
    value.add_argument("--harness", choices=("claude", "codex", "both"))
    value.add_argument("--mode", choices=("controlled", "live"))
    value.add_argument("--output", type=Path, help="new or empty capture bundle directory")
    value.add_argument("--deadline-seconds", type=float, default=240.0, help="finite per-case wall-clock allowance, including observer approvals")
    value.add_argument("--max-output-bytes", type=int, default=8 * 1024 * 1024)
    value.add_argument("--expected-workbench-release", help="expected immutable published Workbench release/tag for live capture")
    value.add_argument("--expected-workbench-commit", help="expected Workbench source commit, exactly 40 hexadecimal characters")
    value.add_argument("--expected-workbench-sha256", help="expected Workbench executable SHA-256, exactly 64 hexadecimal characters")
    value.add_argument("--self-test-bounds", action="store_true", help="run the bounded process/drain witness without a provider")
    value.add_argument("--self-test-protocol", action="store_true", help="exercise the bounded local JSON-RPC client without a provider or manifest")
    value.add_argument("--self-test-contract", action="store_true", help="run offline manifest/linkage counterexamples without a provider or native evidence")
    value.add_argument("--self-test-cleanup", action="store_true", help="exercise exact Workbench runtime ownership cleanup without a provider")
    return value


def self_test_bounds() -> int:
    child = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c','import time; time.sleep(30)']); sys.stdout.write('x'*1048576); sys.stdout.flush(); time.sleep(30)"
    result = run_process([sys.executable, "-c", child], env=os.environ.copy(), cwd=Path.cwd(), timeout=0.2, max_output=1024)
    oversized = False
    try:
        run_process([sys.executable, "-c", "import sys; sys.stdin.buffer.read()"], env=os.environ.copy(), cwd=Path.cwd(), timeout=1, max_output=1024, input_bytes=b"x" * 2048)
    except RunnerError:
        oversized = True
    witness = {"stalledTimedOut": result["timed_out"], "processGroupGone": result["process_group_gone"], "drainJoined": result["drain_joined"], "retainedStdoutBytes": len(result["stdout"]), "observedStdoutBytes": result["stdout_total_bytes"], "oversizedStdinRejected": oversized}
    print(json.dumps(witness, indent=2, sort_keys=True))
    return 0 if witness["stalledTimedOut"] and witness["processGroupGone"] and witness["drainJoined"] and witness["retainedStdoutBytes"] <= 1024 and witness["observedStdoutBytes"] > witness["retainedStdoutBytes"] and witness["oversizedStdinRejected"] else 1


def self_test_protocol() -> int:
    with tempfile.TemporaryDirectory(prefix="cqa-protocol-") as root_name:
        root = Path(root_name)
        evidence = root / "evidence"
        fake = "import json,sys;\nfor line in sys.stdin:\n m=json.loads(line); print(json.dumps({'jsonrpc':'2.0','id':m.get('id'),'result':{'method':m.get('method')}}),flush=True)"
        client = CodexClient([sys.executable, "-c", fake], cwd=root, env=os.environ.copy(), evidence=evidence, approvals=root / "approvals", deadline=5, max_evidence=MAX_CODEX_EVIDENCE)
        try:
            response = client.request(1, "initialize", {"clientInfo": {"name": "offline-test", "version": "1"}})
            passed = response.get("result", {}).get("method") == "initialize"
        finally:
            cleaned = client.close()
        overflow_fake = f"import json,sys; sys.stderr.buffer.write(b's' * {MAX_CODEX_EVIDENCE + 1}); sys.stderr.flush(); m=json.loads(sys.stdin.readline()); print(json.dumps({{'jsonrpc':'2.0','id':m.get('id'),'result':{{'ok':True}}}}),flush=True)"
        overflow_evidence = root / "overflow-evidence"
        overflow_client = CodexClient([sys.executable, "-c", overflow_fake], cwd=root, env=os.environ.copy(), evidence=overflow_evidence, approvals=root / "overflow-approvals", deadline=5, max_evidence=MAX_CODEX_EVIDENCE)
        overflow_response = False
        try:
            overflow_response = overflow_client.request(1, "initialize", {})["result"]["ok"] is True
        finally:
            overflow_cleaned = overflow_client.close()
        stderr_capture_bound = overflow_response and overflow_cleaned and overflow_client.stderr_result.get("complete") is False and overflow_client.stderr_result.get("total", 0) > MAX_CODEX_EVIDENCE and overflow_client.shutdown_info.get("stderrDrainComplete") is False and overflow_client.shutdown_info.get("stderrWriteComplete") is True and overflow_client.shutdown_info.get("stderrComplete") is False and overflow_client.stderr_capture.get("complete") is False and (overflow_evidence / "app-server-stderr.txt").stat().st_size == MAX_CODEX_EVIDENCE
        nonzero_fake = "import json,sys; json.loads(sys.stdin.readline()); print(json.dumps({'jsonrpc':'2.0','id':1,'result':{'ok':True}}),flush=True); raise SystemExit(7)"
        nonzero_client = CodexClient([sys.executable, "-c", nonzero_fake], cwd=root, env=os.environ.copy(), evidence=root / "nonzero-evidence", approvals=root / "nonzero-approvals", deadline=5, max_evidence=MAX_CODEX_EVIDENCE)
        valid_response = False
        try:
            valid_response = nonzero_client.request(1, "initialize", {})["result"]["ok"] is True
            exit_deadline = time.monotonic() + 1
            while nonzero_client.process is not None and nonzero_client.process.poll() is None and time.monotonic() < exit_deadline:
                time.sleep(0.01)
        finally:
            nonzero_cleaned = nonzero_client.close()
        valid_response_then_nonzero_rejected = valid_response and nonzero_cleaned and bool(nonzero_client.shutdown_info.get("unexpectedExit")) and nonzero_client.shutdown_info.get("exit") == 7
        witness = {"requestResponseRoundTrip": passed, "ownedProcessGroupGone": cleaned, "evidenceBounded": all(path.stat().st_size <= MAX_CODEX_EVIDENCE for path in evidence.glob("*.jsonl")), "validResponseThenNonzeroRejected": valid_response_then_nonzero_rejected, "cappedStderrMarkedIncomplete": stderr_capture_bound}
        print(json.dumps(witness, indent=2, sort_keys=True))
        return 0 if all(witness.values()) else 1


def self_test_cleanup(workbench: Path) -> int:
    with tempfile.TemporaryDirectory(prefix="cqa-runtime-cleanup-") as root_name:
        root = Path(root_name)
        case = root / "case"
        paths = make_fixture(case, "WCTX_CLEANUP_OFFLINE")
        env = isolated_env(case)
        daemon = subprocess.Popen([str(workbench.resolve()), "context", "serve", *context_flags(paths)], cwd=paths["project"], env=env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        started = time.monotonic()
        while not paths["socket"].exists() and daemon.poll() is None and time.monotonic() - started < 5:
            time.sleep(0.05)
        daemon_started = paths["socket"].exists() and daemon.poll() is None
        real_cleanup = False
        if daemon_started:
            result = cleanup_runtime(workbench, paths, paths["evidence"], 1024 * 1024, "self-test-real-workbench")
            real_cleanup = result.get("status") == "clean" and result.get("complete") is True and not paths["socket"].exists() and daemon.poll() is not None
        else:
            with contextlib.suppress(ProcessLookupError):
                terminate_group(daemon.pid)
            daemon.wait(timeout=3)
        sentinel_socket = paths["socket"]
        sentinel_socket.parent.mkdir(parents=True, exist_ok=True)
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        listener.bind(str(sentinel_socket))
        sentinel = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"], cwd=paths["project"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        refused = False
        try:
            cleanup_runtime(workbench, paths, paths["evidence"], 1024 * 1024, "self-test-mismatched-owner")
        except RunnerError:
            refused = sentinel.poll() is None
        finally:
            listener.close()
            with contextlib.suppress(ProcessLookupError):
                terminate_group(sentinel.pid)
            with contextlib.suppress(subprocess.TimeoutExpired):
                sentinel.wait(timeout=3)
        witness = {"realLongTTLWorkbenchCleanup": real_cleanup, "mismatchedOwnerRefusedWithoutKill": refused}
        print(json.dumps(witness, indent=2, sort_keys=True))
        return 0 if all(witness.values()) else 1


def self_test_contract() -> int:
    global MAX_RUNTIME_PROCESSES
    with tempfile.TemporaryDirectory(prefix="cqa-contract-") as root_name:
        root = Path(root_name)
        project = root / "project"
        project.mkdir()
        source = root / "transcript.raw.jsonl"
        inputs = root / "inputs.jsonl"
        source.write_bytes(b"{\"type\":\"offline-only\"}\n")
        inputs.write_bytes(b'{"inputId":"input-offline-1","actor":"harness","boundary":"request","stepId":"read-readme"}\n')
        hook_only = [{"Outcome": 0, "Inputs": [{"Key": "observation.hookEvent", "Value": {"String": "PostToolBatch"}}]}]
        confirmed = [{"Outcome": 2, "ContributionID": 11, "Inputs": [{"Key": "observation.hookEvent", "Value": {"String": "PostToolBatch"}}, {"Key": "resource.path", "Value": {"String": "README.md"}}, {"Key": "observation.causalId", "Value": {"String": "native-1"}}, {"Key": "observation.invocationId", "Value": {"String": "invoke-1"}}]}, {"Outcome": 3, "ContributionID": 11, "Inputs": [{"Key": "handoff.content.id", "Value": {"String": "content-1"}}]}]
        no_delivery = confirmed_workbench_record(hook_only, "README.md")[0] is None
        delivery = confirmed_workbench_record(confirmed, "README.md")
        codex_confirmed = [{"Outcome": 2, "ContributionID": 12, "Inputs": [{"Key": "observation.hookEvent", "Value": {"String": "PostToolUse"}}, {"Key": "resource.path", "Value": {"String": "README.md"}}, {"Key": "observation.causalId", "Value": {"String": "codex-native-1"}}, {"Key": "observation.invocationId", "Value": {"String": "codex-invoke-1"}}]}, {"Outcome": 3, "ContributionID": 12, "Inputs": []}]
        codex_delivery = confirmed_workbench_record(codex_confirmed, "README.md", {"PostToolUse"})
        identity = {"releaseTag": None, "commit": None, "binarySha256": sha256_file(Path(sys.executable)), "published": False, "stdout": {"complete": True}, "stderr": {"complete": True}}
        verdicts, evidence, unknowns = live_verdicts(False, wire_path="wire-requests.jsonl")
        manifest = live_manifest(root, "offline", "offline-only", Path(sys.executable), Path(sys.executable), "capture-offline", "offline-live", source, True, ".", [], inputs, True, 1, verdicts, evidence, unknowns, 1024, identity)
        derived_actor = manifest["sessions"][0]["inputs"][0]["actor"] == "harness"
        wire_locator = manifest["sessions"][0]["verdictEvidence"].get("wireRequestCaptured") == [{"filePath": "wire-requests.jsonl", "recordOrdinal": 1, "sourcePointer": "/status"}, {"filePath": "wire-requests.jsonl", "recordOrdinal": 1, "sourcePointer": "/reason"}]
        auth_rejected = provider_status({"exit": 1, "timed_out": False, "native_boundary_error": "", "stdout": b"authentication_failed", "stderr": b"", "writer_error": []}) == "authentication-failed"
        intentional_shutdown = provider_status({"exit": 143, "timed_out": False, "native_boundary_error": "", "stdout": b"", "stderr": b"", "writer_error": [], "process_group_gone": True, "drain_joined": True, "intentional_shutdown": True, "completed_steps": True}) == "passed"
        fake = root / "fake-workbench.py"
        fake.write_text("#!/usr/bin/env python3\nimport sys\nif len(sys.argv) > 1 and sys.argv[1] == 'version': print('workbench 9.9.9 (' + 'a' * 40 + ')')\nelse: raise SystemExit(2)\n", encoding="utf-8")
        fake.chmod(0o700)
        strict_identity = workbench_identity(fake, deadline=2, max_output=1024, expected_release="9.9.9", expected_commit="a" * 40, expected_sha256=sha256_file(fake))
        bad = root / "bad-workbench.py"
        bad.write_text("#!/usr/bin/env python3\nimport sys\nsys.stderr.write('workbench 9.9.9 (' + 'a' * 40 + ')\\n')\nsys.exit(1)\n", encoding="utf-8")
        bad.chmod(0o700)
        rejected_identity = workbench_identity(bad, deadline=2, max_output=1024, expected_release="9.9.9", expected_commit="a" * 40, expected_sha256=sha256_file(bad))
        launcher = root / "missing-runtime-launcher"
        launcher.write_text("#!/usr/bin/env cqa-runtime-that-is-not-installed\n", encoding="utf-8")
        launcher.chmod(0o700)
        launcher_result = run_process([str(launcher)], env={**os.environ, "PATH": CODEX_THREAD_PATH}, cwd=root, timeout=2, max_output=1024)
        startup_runtime_diagnostic = launcher_result["exit"] != 0 and bool(launcher_result["stderr"]) and launcher_result["stderr_complete"] and launcher_result["process_group_gone"] and launcher_result["drain_joined"]
        irrelevant_step = native_command_step([{"method": "item/completed", "params": {"turnId": "turn-irrelevant", "item": {"type": "commandExecution", "id": "exec-irrelevant", "cwd": str(project), "status": "completed", "exitCode": 0, "commandActions": [{"path": "OTHER.md"}]}}}], "irrelevant", project)
        failed_prose_only = native_command_step([{"method": "item/completed", "params": {"item": {"type": "agentMessage", "text": "the file is missing"}}}], "failed-read", project)
        failed_step = native_command_step([{"method": "item/completed", "params": {"turnId": "turn-failed", "item": {"type": "commandExecution", "id": "exec-failed", "cwd": str(project), "status": "failed", "exitCode": 2, "commandActions": [{"path": "DOES_NOT_EXIST.md"}]}}}], "failed-read", project)
        control_step = native_command_step([{"method": "item/completed", "params": {"turnId": "turn-control", "item": {"type": "commandExecution", "id": "exec-control", "cwd": str(project), "status": "completed", "exitCode": 0, "commandActions": [{"path": "README.md"}]}}}], "disabled", project)["exercised"] and native_command_step([{"method": "item/completed", "params": {"turnId": "turn-control", "item": {"type": "commandExecution", "id": "exec-control", "cwd": str(project), "status": "completed", "exitCode": 0, "commandActions": [{"path": "README.md"}]}}}], "no-source", project)["exercised"]
        ttl_fixture = make_fixture(root / "ttl-fixture", "WCTX_TTL_OFFLINE")
        qa_idle_ttl = f"idleTTLMs = {QA_IDLE_TTL_MS}" in ttl_fixture["home_config"].read_text(encoding="utf-8") and QA_IDLE_TTL_MS >= 60000
        controlled_manifest_inventory = self_test_controlled_manifest_inventory(root)
        live_cleanup_wiring = self_test_live_cleanup_wiring()
        failure_cleanup_no_start = self_test_failure_cleanup_no_start()
        prior_process_limit = MAX_RUNTIME_PROCESSES
        MAX_RUNTIME_PROCESSES = 0
        try:
            census_refusal = _runtime_snapshot(Path(sys.executable), fixture_paths(root / "census"))["status"] == "incomplete"
        finally:
            MAX_RUNTIME_PROCESSES = prior_process_limit
        (root / "workbench-version.json").write_bytes(json_bytes(identity))
        merged = merge_live_manifests(root, [{"files": [], "sessions": [], "unknowns": [], "producer": {"workbench": identity}}, {"files": [], "sessions": [], "unknowns": [], "producer": {"workbench": identity}}])
        identity_unique = sum(1 for item in merged["files"] if item.get("path") == "workbench-version.json") == 1
        witness = {"status": "offline-only", "retainedInputActor": derived_actor, "hookExitAloneNotDelivery": no_delivery, "structuredConfirmationLinks": delivery[0] == 2 and delivery[1].get("causalId") == "native-1", "codexPostToolUseDelivery": codex_delivery[0] == 2, "wireUnavailableHasLocators": wire_locator, "authFailureRejected": auth_rejected, "intentionalJoinedShutdownAccepted": intentional_shutdown, "strictIdentityAccepted": strict_identity["valid"], "stderrOnlyNonzeroRejected": not rejected_identity["valid"], "identityFileUniqueAfterMerge": identity_unique, "startupRuntimeDiagnostic": startup_runtime_diagnostic, "irrelevantSuccessExecution": irrelevant_step["exercised"], "failedReadRequiresExecution": not failed_prose_only["exercised"] and failed_step["exercised"], "inactiveControlsRequireRead": control_step, "qaIdleTTL300000": qa_idle_ttl, "controlledManifestBothSessions": controlled_manifest_inventory, "liveCleanupWiring": live_cleanup_wiring, "failureCleanupNoStart": failure_cleanup_no_start, "truncatedCensusRefused": census_refusal}
        print(json.dumps(witness, indent=2, sort_keys=True))
        return 0 if all(value is True for key, value in witness.items() if key != "status") else 1


def merge_live_manifests(output: Path, manifests: list[dict[str, object]]) -> dict[str, object]:
    if not manifests:
        raise RunnerError("no live harness manifest was produced")
    merged = dict(manifests[0])
    merged["files"] = []
    merged["sessions"] = []
    merged["unknowns"] = []
    file_index: dict[str, dict[str, object]] = {}
    for manifest in manifests:
        for entry in manifest.get("files", []):
            path = entry.get("path") if isinstance(entry, dict) else None
            if not isinstance(path, str):
                raise RunnerError("live manifest contains a file entry without a path")
            prior = file_index.get(path)
            if prior is not None and prior != entry:
                raise RunnerError(f"conflicting duplicate live manifest file path: {path}")
            file_index[path] = entry
        merged["sessions"].extend(manifest.get("sessions", []))
        merged["unknowns"].extend(manifest.get("unknowns", []))
    identity_path = output / "workbench-version.json"
    if identity_path.exists():
        identity_entry = file_entry(output, identity_path, "workbench-identity", True, True)
        prior = file_index.get(identity_entry["path"])
        if prior is not None and prior != identity_entry:
            raise RunnerError("conflicting duplicate live manifest file path: workbench-version.json")
        file_index[identity_entry["path"]] = identity_entry
    merged["files"] = list(file_index.values())
    merged["producer"] = dict(manifests[0].get("producer", {}))
    for manifest in manifests[1:]:
        for key, value in manifest.get("producer", {}).items():
            if key != "workbench":
                merged["producer"][key] = value
    (output / "capture-manifest.json").write_bytes(json_bytes(merged))
    return merged


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if args.self_test_bounds:
        return self_test_bounds()
    if args.self_test_protocol:
        return self_test_protocol()
    if args.self_test_contract:
        return self_test_contract()
    if args.self_test_cleanup:
        if args.workbench is None:
            raise RunnerError("--self-test-cleanup requires --workbench")
        return self_test_cleanup(args.workbench.resolve(strict=True))
    if args.harness is None or args.mode is None or args.output is None:
        raise RunnerError("--harness, --mode, and --output are required for a capture")
    if args.deadline_seconds <= 0 or args.max_output_bytes <= 0:
        raise RunnerError("deadline and byte limits must be positive")
    if args.workbench is None:
        raise RunnerError("--workbench is required unless --self-test-bounds is selected")
    workbench = args.workbench.resolve(strict=True)
    if args.mode == "live" and not all((args.expected_workbench_release, args.expected_workbench_commit, args.expected_workbench_sha256)):
        raise RunnerError("live mode requires --expected-workbench-release, --expected-workbench-commit, and --expected-workbench-sha256")
    if args.expected_workbench_commit and not re.fullmatch(r"[0-9a-fA-F]{40}", args.expected_workbench_commit):
        raise RunnerError("--expected-workbench-commit must be exactly 40 hexadecimal characters")
    if args.expected_workbench_sha256 and not re.fullmatch(r"[0-9a-fA-F]{64}", args.expected_workbench_sha256):
        raise RunnerError("--expected-workbench-sha256 must be exactly 64 hexadecimal characters")
    if args.harness in {"claude", "both"}:
        if args.claude is None:
            raise RunnerError("--claude is required for the Claude harness")
        claude = args.claude.resolve(strict=True)
    else:
        claude = None
    if args.harness in {"codex", "both"} and args.codex is None:
        raise RunnerError("--codex is required for the Codex harness")
    output = args.output.resolve()
    if output.exists() and any(output.iterdir()):
        raise RunnerError(f"--output must be new or empty: {output}")
    output.mkdir(parents=True, exist_ok=True)
    output.chmod(0o700)
    temp_root = Path(tempfile.mkdtemp(prefix="cqa-hook-"))
    try:
        identity = workbench_identity(workbench, deadline=args.deadline_seconds, max_output=args.max_output_bytes, expected_release=args.expected_workbench_release, expected_commit=args.expected_workbench_commit, expected_sha256=args.expected_workbench_sha256)
        (output / "workbench-version.json").write_bytes(json_bytes(identity))
        if args.mode == "live" and not identity.get("valid"):
            raise RunnerError(str(identity.get("refusalReason", "Workbench identity was not verified")))
        if args.mode == "live":
            if args.harness in {"claude", "both"}:
                if claude is None or args.claude_auth is None:
                    raise RunnerError("live Claude mode requires --claude and --claude-auth")
                claude_auth = args.claude_auth.resolve(strict=True)
            if args.harness in {"codex", "both"}:
                if args.codex is None or args.codex_auth is None:
                    raise RunnerError("live Codex mode requires --codex and --codex-auth")
                codex = args.codex.resolve(strict=True)
                codex_auth = args.codex_auth.resolve(strict=True)
            results: list[dict[str, object]] = []
            live_cases = ["enabled", "irrelevant", "failed-read", "disabled", "no-source"]
            if args.harness in {"claude", "both"}:
                for case_name in live_cases:
                    results.append(run_live_claude(workbench, claude, claude_auth, output, args.deadline_seconds, args.max_output_bytes, temp_root, identity, case_name))
            if args.harness in {"codex", "both"}:
                for case_name in live_cases:
                    results.append(run_live_codex(workbench, codex, codex_auth, output, args.deadline_seconds, args.max_output_bytes, temp_root, identity, case_name))
            manifest = merge_live_manifests(output, [item["manifest"] for item in results])
            matrix_passed = bool(results) and all(item.get("matrixStatus") == "passed" for item in results)
            summary = {"base": str(output), "manifest": str(output / "capture-manifest.json"), "mode": "live", "harness": args.harness, "status": "passed" if matrix_passed else "rejected", "cases": results, "capabilities": {"claude": "captured" if args.harness in {"claude", "both"} else "not-selected", "codex": "captured" if args.harness in {"codex", "both"} else "not-selected"}, "sessionCount": len(manifest["sessions"])}
            (output / "summary.json").write_bytes(json_bytes(summary))
            print(json.dumps(summary, indent=2))
            return 0 if matrix_passed else 1
        if args.harness != "claude":
            capability = {"mode": args.mode, "harness": args.harness, "status": "unavailable", "reason": "Controlled Codex requires a real Codex native app-server route; no scripted native events are substituted."}
            print(json.dumps(capability, indent=2))
            return 2
        capture_id = "qa-claude-controlled-" + secrets.token_hex(10)
        cases = [run_case(workbench, claude, output, "success", False, args.deadline_seconds, args.max_output_bytes, capture_id, temp_root), run_case(workbench, claude, output, "failure", True, args.deadline_seconds, args.max_output_bytes, capture_id, temp_root)]
        result = write_manifest(output, workbench, claude, capture_id, cases, args.max_output_bytes, identity)
        summary = {"base": str(output), "captureId": capture_id, "required": result["required"], "observed_union": sorted(set().union(*(set(item["observed"]) for item in cases))), "cases": [item["case"] for item in cases], "manifest": str(output / "capture-manifest.json"), "capabilities": {"claude": {"mode": "controlled", "status": "captured"}, "codex": {"status": "not-selected", "reason": "Controlled Codex has no scripted provider seam; use --mode live with explicit Codex inputs."}, "live": {"status": "not-selected", "reason": "Live adapters require explicit --claude-auth or --codex-auth and are never invoked by controlled mode."}}}
        (output / "summary.json").write_bytes(json_bytes(summary))
        print(json.dumps(summary, indent=2))
        return 0 if all(result["required"].values()) else 1
    finally:
        cleanup_error: Exception | None = None
        try:
            shutil.rmtree(temp_root)
        except OSError as error:
            cleanup_error = error
        if temp_root.exists():
            cleanup_error = cleanup_error or OSError("temporary hook directory still exists after cleanup")
        if cleanup_error is not None:
            write_observation(output / "cleanup-failure.jsonl", [{"component": "runner-temp-root", "path": str(temp_root), "reason": str(cleanup_error)}], args.max_output_bytes)
            raise RunnerError("runner temporary cleanup failed; retained cleanup-failure.jsonl") from cleanup_error


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, RunnerError, ValueError) as error:
        print(f"context-live-qa: {error}", file=sys.stderr)
        raise SystemExit(2)
