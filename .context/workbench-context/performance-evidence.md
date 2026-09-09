# Production hook performance evidence

Observed on Linux 6.8.0-138-generic x86_64, glibc 2.39, 12 logical CPUs,
131,503,192 kB machine memory. Assembled production binary SHA-256:
`b06bff4463b23917e0b64fc95cf98bb2a1246ff920dedb05cf306ae07ca6c061`.
The final verification receipt identifies subsequent source checks; this hash
identifies the measured executable rather than an inferred release revision.

Driver instrument: `/tmp/workbench-context-performance/measure-resident-final.py`.
Exact result: `/tmp/workbench-context-performance/1788913285382/result.json`.

| Workload | Observed | Passing target |
| --- | --- | --- |
| 100 inactive Go hooks, depth 12 | p95 20.46 ms; median 11.93 ms | p95 <=30 ms |
| 8 concurrent cold hooks | max 98.66 ms; all eight received guidance; one daemon | <=2,000 ms; one owner |
| 100 warm builtin hooks, 8 scopes / 32 audiences | p95 34.48 ms; median 19.70 ms; no duplicate guidance | p95 <=100 ms |
| Daemon residency after workload | 24,136 kB RSS, 15 OS threads | Observed workload, not a universal external-provider RSS limit |
| Configured 5,000 ms idle TTL | process absent after 5,021 ms; no residual daemon | Observe actual release within TTL plus bounded cleanup |

The provider was builtin ai-context with an 18-byte message; no external
provider subprocess ran in this measurement. Every warm/inactive hook returned
without stderr diagnostics. Inactive hooks produced empty stdout and created
neither runtime nor cache directories. Initial recruitment exercised 32 audiences.
The cache cap was 1,000,000 bytes; rotation at tiny exact caps is tested separately.

Timing wraps each actual CLI subprocess. RSS comes from the owned daemon's
/proc status; OS thread count is not a goroutine count. Idle success observes
process disappearance. These measurements pass the predeclared acceptance-map
thresholds and describe this workload only.

## Short residency control

The same binary with a 1,000 ms TTL was measured in
`/tmp/workbench-context-performance/1788913236543/result.json`: inactive p95
10.68 ms, warm p95 40.24 ms, cold maximum 42.13 ms, 25,708 kB RSS, final exit
1,221 ms after the last interaction. Three warm hooks contributed again because
individual audiences had already exceeded this deliberately short residency
window. Loss of receipts on idle eviction permits full re-recruitment; this is
not an exactly-once guarantee. The five-second condition keeps all measured
audiences resident and demonstrates suppression without this confound.

## Separate ownership and overload evidence

Provider tests exercise blocked pipes, ignored cancellation, descendant process
termination and joined readers. Runtime tests enforce residual global item,
retained-byte, offer, receipt and provider-process admission. An independent
barrier test reproduced admission of a second process while the first was still
closing with a configured process limit of one. The corrected implementation
retains the closing process's capacity charge until its single close owner joins;
the same race-enabled witness passes. See `runtime-review.md` and the tracked
`TestRuntimeReviewClosingProviderStillConsumesGlobalProcessCapacity`.

These are distinct from a universal operating-system RSS bound for arbitrary
third-party provider executables. Numeric delivery/trace budgets constrain
Workbench-owned retained data; process count and deadlines constrain contributor
lifetime. Arbitrary contributor heap size is not claimed to be OS-enforced.
