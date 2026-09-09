# Runtime integration review

Driver findings against the first implementation draft. These are unresolved
counterexamples, not claims that the feature has passed acceptance. The runtime
worker owns fixes; the driver verifies their observable consequences.

## Delivery and freshness

- A no-contribution hook with a delivery opportunity must deliver still-valid
  pending guidance. Refusing every such request defeats the queue contract.
- Current incremental results are not a complete source inventory. Rebuilding
  the engine on unrelated results loses pending work and suppression history.
- Use the builtin provider's actual `Revalidate` algorithm, including manifest
  and included content. Reading a file and calculating a hash without comparing
  its expected revision is not validation. All reads remain bounded.
- External delayed guidance needs re-evaluation against its original bounded
  recruitment facts and the current profile, or an explicit provider freshness
  contract. Do not silently turn delayed delivery into permanent deferral.
- Remove placeholder helpers such as `inputObservationResources` returning nil.
  Remove hard-coded replacement-engine defaults; home limits remain authoritative.
- Runtime wall time governs expiry. Caller observation timestamps describe
  historical evidence and cannot move the admission clock backward.

## Lifetime and bounds

- `partitionCapacityLocked` currently allows admission whenever any partition
  exists. Test exact scope/audience caps, global retained bytes, and all count
  bounds under simultaneous requests.
- Session metadata is allocated before partition admission. It needs its own
  bound and eviction, including rejected requests. Offer freshness metadata must
  also disappear when offers expire or are revoked.
- Concurrent `Close` calls cannot both close the stop channel. One close owner
  requests cancellation, joins admitted work, then joins workers/providers;
  later callers wait for that same completion.
- `beginRequest` must recheck close state after startup. WaitGroup admission and
  shutdown must have one invariant; no Add may race a completed shutdown Wait.
- `ensureForInspection` currently releases request ownership before the query.
  Each query/inspection/clear must retain its request lease through completion.
- Cache open failure must produce unavailable explanation evidence while context
  operation remains usable. Do not make successful trace startup a prerequisite
  for hook guidance, or open/scan the cache under the shared runtime mutex.
- Authored home cache/runtime limits must govern startup and inspection, before
  any project hook. Explicitly test a tiny configured disk cap.

## Audience and explanation fidelity

- A reset observed in directory B must revoke old receiving-context history in
  directory A for the same harness session. Scope partitioning must not prevent
  a host-wide audience reset from propagating to every affected partition.
- Inactive confirmation must target its actual scope/audience; an empty scope
  must never accidentally withdraw unrelated partitions.
- Status must honor current-directory, explicit-scope, and audience filters;
  attaching activation reasons alone does not scope the returned partitions.
- Current profile revision/expiry and stable contribution IDs must be discoverable.
  Confirmation records must join the same contribution history as its sample,
  selection reasons, and offer outcome.

These obligations should be proven through the concrete runtime/client seam,
including concurrent shutdown, source edits/deletion, delayed delivery, custom
limits, failed cache startup, and scope-filtered inspection.
