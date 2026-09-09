#!/usr/bin/env python3
"""Minimal executable profile provider using only the Python stdlib."""

import json
import sys
from datetime import datetime, timedelta, timezone


MAX_REQUEST_BYTES = 1 << 20


def emit(request, result=None, error=None):
    response = {"jsonrpc": "2.0", "id": request.get("id", 0)}
    if error is not None:
        response["error"] = error
    else:
        response["result"] = result
    sys.stdout.write(json.dumps(response, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def parse_time(value):
    if not value:
        return datetime.now(timezone.utc)
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def request_lines():
    while True:
        raw_line = sys.stdin.buffer.readline(MAX_REQUEST_BYTES + 1)
        if not raw_line:
            return
        if len(raw_line) > MAX_REQUEST_BYTES:
            while raw_line and not raw_line.endswith(b"\n"):
                raw_line = sys.stdin.buffer.readline(MAX_REQUEST_BYTES + 1)
            emit({}, error={"code": -32600, "message": "request line exceeds 1048576 bytes"})
            continue
        yield raw_line


for raw_line in request_lines():
    if not raw_line.strip():
        continue
    try:
        request = json.loads(raw_line.decode("utf-8"))
        method = request["method"]
        request_id = request["id"]
    except (KeyError, TypeError, UnicodeDecodeError, ValueError, json.JSONDecodeError):
        emit({}, error={"code": -32700, "message": "invalid JSON-RPC request"})
        continue

    if method == "initialize":
        emit(request, {"accepted": True, "capabilities": ["profile"], "reasons": []})
    elif method == "audience.profile":
        profile_request = request["params"]["input"]
        now = parse_time(profile_request.get("now"))
        expires = (now + timedelta(days=1)).replace(hour=23, minute=59, second=59, microsecond=0)
        audience = profile_request["audience"]
        task = profile_request.get("task", {})
        facts = [
            {
                "key": "role",
                "value": {"kind": "text", "text": "maintainer"},
                "appliesTo": {"audience": audience, "task": task},
                "validity": {"policy": "until", "expiresAt": expires.isoformat().replace("+00:00", "Z"), "rule": "example.profile.daily"},
                "provenance": {"origin": "provider", "rule": "example.profile.role", "sourceRevision": {"source": {"id": "example-profile-role"}, "revision": "v1"}},
            },
            {
                "key": "preference:reviewStyle",
                "value": {"kind": "text", "text": "concise"},
                "appliesTo": {"audience": audience, "task": task},
                "validity": {"policy": "until", "expiresAt": expires.isoformat().replace("+00:00", "Z"), "rule": "example.profile.daily"},
                "provenance": {"origin": "provider", "rule": "example.profile.review-style", "sourceRevision": {"source": {"id": "example-profile-preference"}, "revision": "v1"}},
            },
        ]
        emit(request, {"output": {"facts": facts, "reasons": []}})
    elif method == "shutdown":
        emit(request, {"requestId": request_id, "reasons": []})
        break
    else:
        emit(request, error={"code": -32601, "message": "method not found"})
