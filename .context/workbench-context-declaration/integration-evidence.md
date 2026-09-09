# Pkl consumer integration evidence

This records measured observations, not additional activation rules. The public
contract is [context declarations](../../../docs/context-declarations.md).

The installed Linux x86_64 binary used below has SHA-256
`a4200299ca9bfe76477ec071e9d7df42c3ba01c9b6451925940cbdc7d1e1905c`.
It was built with Go 1.26.6 using `go build -trimpath -buildvcs=false` and staged
at `/tmp/wctx-install.6uobrla_/bin/workbench` with the exact private Pkl 0.32.1
artifact and repository runtime lock. No ambient Pkl supplies the capability.
Final delivery checks must be refreshed if the relevant source changes.
Independent consumer review found lazy-open cancellation, ordinary-status
policy freshness, and warm no-source trace initialization gaps. Those repairs
are now covered by passing red-to-green tests and an independent re-review;
the measurements below were refreshed on the repaired binary.

## Independent package evidence

Root ran the CLI, runtime, daemon, provider and trace packages together under
`go test -race -count=1`: all passed. Log:
`/tmp/workbench-declaration-consumers-root.log`.
The final rooted-read consistency repair also passed the evaluator race oracle:
`/tmp/workbench-declaration-evaluator-read-consistency.log`.
The final full repository `go test ./... -count=1 -timeout 5m` passed in 49 s;
`go vet ./...` passed in 5 s. Logs are
`/tmp/workbench-declaration-full-test-final.log` and
`/tmp/workbench-declaration-full-vet-final.log`. Linux arm64 and Darwin amd64/arm64
whole-CLI builds also passed. Darwin compilation is not runtime memory enforcement
or a macOS activation-support claim.

## Actual native request admission

`python3 /tmp/workbench-context-pkl-native.py <installed-binary>` passed all six
cases. Real Claude and Codex harnesses ran installed Go hooks using Pkl fixtures
against a controlled model endpoint. Enabled guidance appeared in request two;
inactive and irrelevant guidance did not. Inactive controls created neither
runtime nor cache. This proves native next-request admission, not a real-model
usability session or automatic native trust approval.

Raw evidence: `/tmp/wctx-pkl.7dliez_x/result.json` and each case's captured
requests/stdout/stderr. The native Codex fixture's explicit test trust bypass is
instrumentation only; product setup still asks the user to review native hooks.

## Complete hook latency

`python3 /tmp/workbench-context-pkl-measure.py <installed-binary>` passed its
assertions. Linux 6.8 x86_64, 12 CPUs, depth 12, eight scopes, 32 audiences, one
builtin contributor, 1-second idle TTL:

| Case | Samples | p95 / elapsed |
| --- | ---: | ---: |
| No source, private Pkl and lock absent | 100 | 3.539 ms p95 |
| Home-only inactive, cold | 1 | 20.447 ms |
| Home-only inactive, warm | 100 | 4.269 ms p95 |
| Eight concurrent cold hooks | 8 | 144.816 ms total |
| Warm active hooks | 100 | 7.006 ms p95 |

All hooks exited successfully without diagnostics. No-source controls created
no runtime/cache/evaluator artifacts. Home-only inactivity retained a 2,371-byte
disposable snapshot without starting a daemon. Eight cold hooks shared one
daemon; observed resident RSS was 20,568 KiB with 16 threads. It exited after
1,036 ms of idle time with no remaining fixture process.

Raw samples, exact cleanup and report: `/tmp/wctx-perf.y9cgywce/`.
These are workload measurements, not universal latency guarantees.

## Shared capacity and process ownership

`python3 /tmp/workbench-context-pkl-resources.py <installed-binary>` exercised
64 fresh sessions in batches of eight concurrent hooks across eight scopes.
Project snapshots and actual daemon trace blocks shared one pool. The home cap
began at 262,144 bytes and was then reduced to 65,536 bytes without restarting
the daemon. All 64 hooks delivered guidance without diagnostics.

Across 268 requested-2-ms observations, initial regular-file usage peaked at
196,178 bytes with at most 14 entries. An explicit `cache status` call established
the new 65,536-byte policy before post-policy assertions. Shared-flock filesystem
scans then observed at most 64,575 bytes, settling at 59,141 bytes. At most one
evaluator and one daemon were observed simultaneously; aggregate hook/evaluator/
daemon RSS peaked at 194,184 KiB. All observed fixture processes exited after idle.

Raw observations, policy-barrier output and hook outcomes:
`/tmp/wctx-cap.uoacvak7/`. Sampling is not an exhaustive proof of temporary peaks.
The pool and trace mutation tests independently cover serialized peak temp/state/
block admission, root-confined deletion, cancellation, reduced policy, and
owner-specific reclamation. Their combined oracle rejected trace growth beyond
a shared 20,000-byte allowance while retaining 12,048 actual bytes; a root-retarget
counterexample wrote zero files outside the captured root.

The earlier unlocked observer at `/tmp/wctx-cap.jnxjft4s/` failed an overstrong
assertion: it set its reduced phase before changing the home file and counted
already-admitted old-policy work during the transition (218,635 bytes, below the
old 262,144-byte cap). A mutation's policy is validated at `Pool.Begin` under the
shared lock; an external home-file write does not retroactively cancel that lease.
The corrected observer uses shared-lock scans and the explicit new-policy barrier.
One intermediate run also had an instrument-only `diskCap`/`DiskCap` JSON-key error.
Neither failed instrument was treated as passing product evidence or used to
relax admission. The original script is retained as
`/tmp/workbench-context-pkl-resources-v1.py`.

The process measurement is builtin-only. Linux child `RLIMIT_DATA` behavior was
separately checked with the real worker: a 200 MB internal allocation failed at
128 MiB and succeeded under a 512 MiB control. This is not a portable RSS or
macOS heap-containment claim. Cole subsequently accepted availability without a
hard memory ceiling, prioritizing warm-hook caching and latency. The limitation
is retained as engineering evidence rather than a routine user warning.

## Memory-risk follow-up

At Cole's request, a supplemental Linux measurement used `/usr/bin/time` to
record process high-water RSS for the same pinned Pkl 0.32.1 executable, running
direct `pkl eval` under child `ulimit -d`, with a 3-second external kill/join
budget. This is a direct evaluator measurement, separate from the production
worker protocol witness above. `/tmp/workbench-pkl-memory-followup/result.json`
records exact results.

- A trivial expression under 128 MiB DATA succeeded at 33,280 KiB peak RSS.
- A 200,000,000-character string under 512 MiB DATA succeeded at 232,960 KiB
  peak RSS in 142 ms wall time.
- The same expression under 128 MiB DATA printed its result, then exited 99
  with OutOfMemoryError; peak RSS was already 238,208 KiB in 175 ms wall time.

The failure proves rejection, not a 128 MiB resident-memory ceiling or rejection
before allocation. The restriction permitted more than 200 MiB resident usage
in this witness. A short deadline and bounded serialized output do not prevent
large internal allocations. Do not describe the Linux DATA setting as complete
physical-memory containment. This qualified the resource-policy decision
beyond the absence of macOS evidence; Cole accepted the residual risk.

Re-analysis of the earlier actual-hook samples found Pkl alone at a sampled
maximum of 45,928 KiB (46 evaluator observations across 46 processes). The
194,184 KiB aggregate peak included Pkl 45,928 KiB, hooks 125,772 KiB, and daemon
22,484 KiB. These sparse samples are not evaluator high-water measurements.

## Platform availability ruling

Cole accepted the residual evaluator memory risk and prioritized cached warm-hook
latency. The Darwin worker now attempts the native DATA resource limit instead
of unconditionally rejecting the platform; finite protocol/deadline bounds and
joined cleanup remain. Other unsupported Unix platforms still reject.
The Darwin valid-limit test runs in a bounded isolated child, never changing
the test runner's limits. Darwin amd64/arm64 test binaries and whole-CLI builds
compile; native Darwin execution was not performed on this Linux host.
There is no new runtime warning or hard RSS/native-heap guarantee.

## Exact warm-cache process counts

The independent warm-cache instrument ran the unchanged final installed binary
with home and project Pkl plus a local project import. `strace` recorded actual
Pkl `execve` calls: two on cold load (home and project), zero on three unchanged
hooks after daemon idle/restart, one after the import changed, zero on its next
unchanged hook, one after restoration, and zero on the next unchanged hook.
Root independently counted the raw traces in
`/tmp/workbench-context-warm-traces.mftZaw/`. The configured idle TTL was 200 ms
so tracing the detached daemon ended within the bounded instrument lifetime.

Twenty separate untraced complete-hook samples measured 13 ms p95 (9–23 ms
range) for this Linux/Claude fixture; this is separate from the trace durations.
The source/entry/import freshness checks remain active on cache hits; avoiding
Pkl startup does not mean accepting stale declarations. The reproducible probe
is [warm-cache instrument](acceptance/warm-cache/warm_cache_probe.sh).

The final reproducibility pass used the same binary and explicit Claude
PostToolBatch/Read payload. It reproduced every evaluation count and measured
11 ms p95 over 20 untraced hooks (9–24 ms); root verified all 20 delivered
guidance without diagnostics. See [final warm-cache evidence](acceptance/warm-cache/evidence.md)
and `/tmp/workbench-context-warm-traces.0e6lXY/`. The earlier 13 ms run is retained
as a separate observation rather than replaced.
