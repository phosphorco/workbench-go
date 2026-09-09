# Pkl consumer composition

This is the shared boundary for the two consumer grants in declaration.plan.pkl.
The source remains home/project Pkl and its exact local imports. Both consumer
lanes call `contextconfig.Load(ctx, options, dependencies)`; neither introduces
another activation, profile-selection, snapshot, or freshness implementation.

## CLI grant

Own `cmd/workbench/context*.go`, `internal/runtime/context*.go`, and the existing
`acceptance/context_test.go`. The independent declaration acceptance instrument
has a separate owner. Preserve the existing hook I/O, confirmation, setup,
status, provider, idle, and concurrency behavior when porting fixtures.

Derive exact private Pkl and runtime-lock paths beside the Workbench executable
through a context-only path capability. It must not acquire Bun or probe private
Pkl before declaration discovery. No PATH fallback. Tests that evaluate Pkl
construct a real isolated installation layout with the pinned Pkl and lock.

At the composition boundary, construct one immutable `evaluate.ContextEvaluator`
and one `contextcache.Pool` without filesystem writes. Bind the evaluator's
Identity/Evaluate/Freshness methods into `contextconfig.LoadDependencies` with
that pool. Hook/status/init and daemon construction receive these same concrete
dependencies. Acquiring the evaluator lease or opening snapshot files happens
only when the loader demands it after finding a source.

Derive the evaluator resource lease from the default private user runtime
location, independently of declaration roots and per-command cache/runtime path
overrides. Do not add a public lease-path flag that lets each project acquire
its own process allowance. Isolated tests may supply explicit dependencies or
an isolated user environment. This empty lock is resource coordination, never
an activation record or declaration cache.

The evaluator owns the exact worker arguments for its configured Pkl path and
finite child limit. The CLI supplies the fixed `context worker` prefix and
implements that narrow hidden dispatch using `evaluate.RunContextWorker`.
Do not independently repeat the path/limit authority in two argument lists.
The worker is not another declaration reader or activation route.

Setup writes the Pkl home designation and reconciles only owned generated hook
entries. Preserve unrelated/custom settings and native Codex review/trust.
Explicit obsolete JSON home paths get an actionable refusal. JSON-only project
files remain inert and untouched. Init follows the state table in
docs/context-declarations.md; report file creation separately from effective
activation and preserve existing expressions, disabled/empty declarations,
inherited contributor selections, and home exclusions.

## Daemon/provider grant

Own `internal/contextdaemon/**`, `internal/contextprovider/**`, the engine test
files and `internal/contextapi/contracts_test.go`. Preserve their original
behavioral coverage while porting the authored configuration fixtures.

Publish `RuntimeOptions.LoadDependencies contextconfig.LoadDependencies` before
CLI adaptation. No daemon-local toolchain discovery, fallback resolver, or
freshness callback. The injected Pool is the exact pool passed to trace Open.

LoadResult.CachePolicy is the loader-owned current home cap and freshness
validator. Establish it before trace publication. Refresh it through the owned
trace mailbox when requests reload authority. A stale old policy stops writes;
evaluation/reload occurs outside Store and pool locks. A lower validated cap
applies even if existing cache usage must first be reduced. The trace Store
derives its directory from the pool and rotates only its own blocks.

Explicit historical access after a daemon restart carries the caller's cwd:
`QueryRequest{WorkingDirectory, Query}`, `InspectRequest.WorkingDirectory`, and
`Clear(ctx, workingDirectory)`, mirrored by the client/transport. These calls
load current policy through the same loader before opening/updating the Store.
They can inspect past history with a valid home policy even when project
activation is inactive or invalid. Invalid home policy cannot authorize use of
an old cap. Explicit cache commands may initialize this path; ordinary status
must retain its no-start/no-residency-renewal behavior. An unopened lazy Store
is not evidence that the on-disk cache is empty.

`CacheStatus(ctx, workingDirectory)` returns `contexttrace.Stats` through that
same explicit trace lease. The CLI cache-status command uses this method;
ordinary `Status`, including scoped status, does not initialize history storage.

Contributor map names are identities, including named builtin AiContext entries.
Pass the configured name through builtin source, slot, reason and revalidation
metadata. Two differently named builtin contributors remain distinct. Derive
profile and contribution dispatch from declared capabilities and enforce the
negotiated capability at the executable client boundary. A profile-only
executable must never receive context.contribute.

## Integration proof

Send signature changes and blockers directly to the peer and root before
adapting consumers. Never add aliases or production stubs for compilation.
Each lane runs its focused tests when real peer code is available; root owns
combined tests, expanded public witnesses, shared-capacity proof, native model
admission, outcome-only usability, and final delivery. Current JSON-era evidence
does not prove the Pkl hard cut.
