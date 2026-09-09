# Shared snapshot/trace capacity review

Status: recommend the single-pool design for the next production lane. No
architecture blocker remains in the repaired schema/probe owners; the probe is
not an integrated-resource proof.

## Smallest sound protocol

Use one private OS exclusive lock and one filesystem scan rooted at the cache
pool. A concrete `internal/contextcache` owner is justified by the invariant
that two processes must admit bytes against one total; no public interface or
contexttrace import is needed. Do not add a persistent reservation ledger.

`Begin(ctx, currentPolicy)` acquires the lock with nonblocking flock retries
bounded by `ctx`, validates the exact current home-policy/dependency manifest
under the lock, removes only protocol-owned abandoned temps, and scans bounded
pool namespaces. It then admits the operation only if

`existing bytes + peak temp bytes + bounded metadata <= current cap`.

The mutation keeps the lock through temp creation, bounded write/close (and
fsync if required), same-directory atomic rename, cleanup, and final scan. A
crash therefore leaves either an old final file or a reclaimable protocol temp;
the next lock holder recovers it. There is no crashed reservation to leak.

Replacement charges the old final plus the new temp until rename. The pool
never evicts trace files: contexttrace evicts only its own blocks, and the
snapshot owner evicts only its own entries; otherwise admission fails.

Required lock order is `Store.mu -> pool lock`; no path may take the pool lock
and then wait for Store.mu. Evaluation, provider calls, policy reload, and
snapshot encoding stay outside the lock. A cancelled `Begin` never creates a
temp and returns a bounded lock-wait error.

Suggested concrete consumer seam:

```go
pool, err := contextcache.Open(paths.CacheDir)
mutation, err := pool.Begin(ctx, currentPolicy)
err = mutation.EnsureFits(peakTempBytes, metadataBytes)
// write/rename/delete only while mutation is held
err = mutation.Finish()
```

`contexttrace.Store` must receive this pool; all `Open` cleanup/trim,
`Append`/flush, `Clear`, and `Close` disk mutations use it. Snapshot publication
must accept the caller context and already-bounded encoded bytes, e.g.
`SnapshotPublish(ctx, key, encoded []byte) error`. A nil/uncoordinated pool
fallback would reintroduce the two-writers-full-cap counterexample.

## Runtime obligations and counterexamples

`contexttrace.Store` is process-synchronous but not cross-process safe:
`internal/contexttrace/contexttrace.go:408-411` documents one writer,
`:460-464` says `Open` has no OS lock, and `:1054-1082` cleans temps without
one. Its flush/state paths (`:1225-1391`) currently account only against local
`Store.usage`/`Options.DiskCap`. Those paths must be pool mutations, including
metadata and all snapshot namespaces; `reconcileUsage` (`:1104-1138`) cannot
remain the total authority.

The current daemon opens trace at `internal/contextdaemon/runtime.go:235`
before an authoritative activation/policy is established, while
`traceOptions` (`:1973-2021`) resolves home limits only at startup. This fails
reduced-cap and bootstrap requirements. Establish policy first; no source means
no pool/cache creation or publication. Every later activation must update the
trace worker through its existing ordered `call`/mailbox before new writes
(`trace_runtime.go:110-170`), and pool admission must use that current policy;
an old limit never authorizes growth. Revalidate the policy/manifest after lock
acquisition and reload outside the lock if stale.

The pool must tolerate a tiny cap: reclaim the owner's files if possible, then
reject new growth while leaving activation/evaluation usable. Trace drops are
acceptable and snapshot publication is a miss; neither writer receives the
whole cap independently.

## Owner review

The repaired shared Pkl aliases/settings contract is covered by the schema
owner's `go test ./internal/contextapi -count=1` report. The evaluator probe's
`no_source_probe` (`probe/run.py:130-149`) is a Python fixture, and
`concurrent_cold` (`:178-200`) invokes helpers without a cache path and marks
`cache_integration_exercised=false`; it cannot prove shared trace/snapshot
capacity. Keep the planned real integrated-resource gate. The pkl-go seams also
remain relevant: `pkl/reader.go:122-152` exposes only `Read(url.URL)`, and
`evaluator_manager_exec.go:91-110` performs version discovery without a
context.

Source contract invariants are explicit in `contracts.md:134-155`:
dependency bytes are captured exactly at the read boundary, snapshots are
disposable, and snapshot/temp/metadata/trace bytes share one cap. Preserve
those invariants while implementing the pool.
