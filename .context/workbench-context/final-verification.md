# Existing context implementation final verification

Date: 2026-09-08
Scope: existing JSON-backed context implementation only. No declaration files,
declaration plan, ADR, commit, push, or optional trace/nit changes were made.

## Final source hashes

SHA-256, measured from the final worktree:

- internal/contextengine/engine.go: 5cb776d262a138d476112eb650a12b12696edb357b2e8ffd87e2c29cd93a1c64
- internal/contextengine/engine_test.go: b99ffdf2689497c540e2c7bc7711b56e921dc04826d8854151eccedf88b0212b
- internal/contextdaemon/runtime.go: c603c7043f5e19a58289a2c1eff534cc0e7d3d4fd4b941876d5b66f2c7aff8dd
- internal/contextdaemon/runtime_review_test.go: 42a4d41f08748ea9cb8357ce6ad0f8d7bfc4ffb8a8ffda9ff3b49b4bd3e277ac
- docs/context.md: 355eb37770e377b2766f0612236ea94f58bef31b4ca80ec627bd0944df9161b2

## Checks and artifacts

- Full suite: `timeout 10m go test ./...`; PASS in 42s; job `job-mttfngmj-938082c3`.
- Vet: `timeout 10m go vet ./...`; PASS in 2s; job `job-mttfni2l-1559525a`.
- Final engine race: `timeout 5m go test -race -count=1 ./internal/contextengine`; PASS; job `job-mttfiu5j-4d05a438`.
- Final daemon race: `timeout 5m go test -race -count=1 ./internal/contextdaemon`; PASS; job `job-mttfiu7n-0b0f681e`.
- Earlier core race witness: `timeout 5m go test -race -count=1 ./internal/contextengine ./internal/contextdaemon ./internal/contexttrace`; PASS; job `job-mtte2rig-d9d71e7a`.
- Provider race witness: `timeout 5m go test -race -count=1 ./internal/contextprovider`; PASS; job `job-mtte3o9a-c63c7181`.
- Final cross-builds: `timeout 10m env GOOS=linux GOARCH=amd64 go build -o /tmp/workbench-context-linux-amd64 ./cmd/workbench && timeout 10m env GOOS=darwin GOARCH=arm64 go build -o /tmp/workbench-context-darwin-arm64 ./cmd/workbench`; PASS; job `job-mttfj037-96911960`.
- Focused normal packages after R1: `go test ./internal/contextengine ./internal/contextdaemon`; PASS.

The final changed-code review independently re-hashed and confirmed these exact
four implementation/test files. The reviewed R1 counterexample was old pending
A=20 bytes plus new B=20 and healthy C=5 under MaxPendingBytes=40. The byte
trim now sheds only newly accepted items, attributes the drop, preserves A,
and commits atomically. Ordering is oversized-render isolation, item-count
trim, then pending-byte trim. F4 trace deduplication and F5 small nits remain
optional and were not changed.

## Assembled CLI external-provider latency witness

Measurement command:

    go build -trimpath -o /tmp/workbench-context-assembled-r1 ./cmd/workbench

Binary: `/tmp/workbench-context-assembled-r1`
Binary SHA-256: `ca263935face08694f94549e17b66d7794748126cb04373d7bec9e2fc23e4bb0`

Provider fixture: `/tmp/workbench-context-cli-latency.ZY7eek/provider.sh`
Provider SHA-256: `940f3c51303b64752cffeed63706197d1975f9a6db92e0df96cd3b54b7a1fadb`
The fixture is the existing provider fixture with a bounded 50ms sleep per
JSON-RPC request and request logging added in the isolated temporary copy.

Each hook was invoked as follows, with the Codex PostToolUse JSON on stdin:

    printf '<Codex PostToolUse JSON>' | env -i HOME=<isolated>/home TMPDIR=<isolated>/tmp PATH=/usr/bin:/bin LC_ALL=C PROVIDER_LATENCY_LOG=<isolated>/provider.log /tmp/workbench-context-assembled-r1 context hook --harness codex --home-config <isolated>/home/context.json --runtime-dir <isolated>/runtime --socket <isolated>/runtime/context.sock --start-lock <isolated>/runtime/context.start.lock --server-lock <isolated>/runtime/context.server.lock --cache-dir <isolated>/cache

Workload and limits: one cold hook followed by two repeated warm hooks; one
Linux x86_64 binary; one executable provider; no home declaration; explicit
isolated runtime, cache, socket, and lock paths; default hook and whole-hook
deadlines; provider deadline 500ms; each hook used a distinct Codex session and
turn while reading README.md. This is a bounded workload measurement, not a
universal latency guarantee.

Results:

- cold: 182.004ms, status 0, stdout 124 bytes, stderr 0 bytes
- warm 1: 117.321ms, status 0, stdout 124 bytes, stderr 0 bytes
- warm 2: 116.960ms, status 0, stdout 124 bytes, stderr 0 bytes
- provider log counts: initialize 1, context.contribute 6, shutdown 1
- The six contribute calls are two per assembled hook: recruitment plus the
  correctness-preserving Confirm freshness revalidation.
- Daemon PID 2998339, PPID 1478, and provider child PID 2998348 were sent
  SIGTERM. Exact post-cleanup process checks found neither process. The log
  contains one shutdown request; process disappearance confirms cleanup in this fixture.
  The race-tested lifecycle witnesses separately establish join ownership.

Raw results are retained at:

    /tmp/workbench-context-cli-latency.ZY7eek/cold.stdout
    /tmp/workbench-context-cli-latency.ZY7eek/cold.stderr
    /tmp/workbench-context-cli-latency.ZY7eek/warm1.stdout
    /tmp/workbench-context-cli-latency.ZY7eek/warm1.stderr
    /tmp/workbench-context-cli-latency.ZY7eek/warm2.stdout
    /tmp/workbench-context-cli-latency.ZY7eek/warm2.stderr
    /tmp/workbench-context-cli-latency.ZY7eek/provider.log

Known optional findings and limits: F4 trace sample deduplication and F5
hasFixedStringFlag/TOML-comment nits remain unchanged; assembled latency is
only one cold plus two warm samples with one known 50ms provider fixture. The
measurement closes the prior Runtime.Observe-only evidence gap for this
assembled path but does not claim a broad external-provider performance bound.


## Root final native admission refresh

Root checked the final source and binary hashes against this handoff and Fable's
independently reviewed files. Native Claude 2.1.260 and Codex 0.153.4 were rerun
against the exact assembled binary above, using localhost model fixtures and
isolated hook/configuration paths. This proves native next-request admission;
the separate real-model usability trials are recorded in usability-evidence.md.

All six cases passed (job job-mttfxueo-59dc3aca): each harness admitted the marker
for enabled applicable guidance, omitted it for irrelevant guidance, and omitted
it without creating runtime/cache paths for inactive directories. Each case
captured two actual native requests. Raw result:
/tmp/wctx-native.sd4z1sqc/result.json; per-case artifacts are in its sibling
claude-* and codex-* directories. Binary SHA-256:
ca263935face08694f94549e17b66d7794748126cb04373d7bec9e2fc23e4bb0.

The preceding attempt at /tmp/workbench-context-final-native.ngkj9wit failed
Claude enabled admission because the fixture's generated Unix socket pathname
was too long. Shortening the temporary fixture prefix resolved it; no production
source changed. That attempt is retained as an instrument failure, not passing
evidence. No global harness configuration or credentials were changed.
