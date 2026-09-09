# Shared disposable cache contract

Root engineering ruling following the bounded capacity review. This implements
the declaration proposal's single total cap; it does not add activation state.

`internal/contextcache` owns a concrete filesystem pool and opaque snapshot
storage. Snapshot codecs and dependency validation belong to the declaration
loader/evaluator. Trace records, indexes and trace eviction remain owned by
`contexttrace`. The pool must never delete a live trace owner's blocks.

## Authority and lifetime

All mutations use one private cross-process lock. Actual regular file lengths
under bounded, known pool subdirectories determine usage, including temporary,
metadata and abandoned files. There is no persistent reservation ledger. Holding
the lock from admission through publication/error cleanup is the reservation;
the kernel releases it on process exit. Abandoned files remain charged until an
authorized owner reclaims them under that lock. Never unlink the lock file to
unlock it or create a second lock inode while peers may hold the first.

The concrete pool capability names an absolute private root and finite scan,
byte and lock-wait bounds. Construction performs no filesystem writes. Reads
of an absent cache return a miss without creating paths. Snapshot names are
bounded opaque digests; they cannot designate arbitrary paths or symlinks.

Evaluator concurrency has a separate resource lifetime from disk mutation.
A fixed per-user evaluator lease may bound simultaneous temporary evaluators;
it carries no declaration, selection or cache truth. Acquire it only after
discovering a source, release it after the child and its I/O have joined, and
never hold the disk-mutation lock while acquiring it or evaluating Pkl. Warm
snapshot validation needs neither evaluator startup nor this lease. Empty
fixed lock metadata is bounded bootstrap overhead and remains included in pool
entry accounting; it does not authorize a snapshot under an unknown home cap.

A mutation receives the current home-policy allowance and an exact bounded
freshness check for the captured home source/import closure. The check runs
after acquiring the lock. It may read bounded files and re-resolve designations;
it must never evaluate Pkl, call providers, recursively enter the pool or start
workers. If the policy changed, release the lock and reload outside it. An
absent home source also has an absence check: a newly created home file cannot
be hidden by an old default allowance. Bootstrap must evaluate an existing home
source before any publication; no guessed cap authorizes a write.

Lock acquisition is cancellable and also has a finite default wait for callers
whose APIs have no context. Lock order is trace Store mutex, then pool lock;
there is no reverse path. Disk operations and scan counts remain bounded. Every
exit releases its lock; cleanup errors are reported and remaining files charged.

## Admission and publication

Encode bounded snapshots and trace blocks before acquiring the pool lock where
possible. Under the lock, validate policy and scan current owned usage. New
writes must fit the total peak, including the old destination, new temporary
bytes and metadata until same-directory atomic replacement removes the old
destination. Re-scan after cleanup/publication before releasing ownership.

Each writer may evict only its own cache entries. A snapshot writer can remove
old snapshots, but must return a bounded capacity miss when trace usage leaves
insufficient room. A trace writer receives residual space after snapshot and
other owned usage, then evicts its own blocks using its existing index. Neither
writer independently receives the entire configured cap.

A reduced cap closes new admissions until owned usage has been reclaimed enough
to fit. Existing bytes can temporarily exceed a newly lowered cap; that cannot
authorize further growth or be reported as successful compliance. No writer may
continue using a startup-time allowance after current policy changes. This check
also covers trace Open cleanup/reconciliation, Flush, Clear, and Close writes.

History clear removes trace history only. Snapshot eviction/replacement changes
no selection or delivery state. Missing or corrupt snapshot bytes are cache
misses; evaluated inputs remain authoritative.

## Required witnesses

Before implementation, demonstrate the two-independent-writers over-allocation
counterexample or a truthful missing shared-admission behavior through a compiled
public seam. Then prove concurrent processes, cancellation while waiting, stale
policy rejection, reduced cap, exact replacement peak, abandoned temporary
cleanup, bounded scans and symlink rejection. Package tests may use opaque trace
fixtures to test the pool algebra, but final acceptance must run concurrent
snapshot publication with the actual trace writer and exercise every mutation
entry point. A synthetic filler file is not that integration proof.

Publish concrete Go method signatures to loader/evaluator and integration owners
before they adapt consumers. Prefer a short bounded mutation closure or explicit
owned lease with one close operation; do not introduce a generalized transaction
framework or an interface hierarchy.
