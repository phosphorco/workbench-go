# Independent review: Workbench context implementation vs accepted ADR

Reviewer: Claude Fable (thread thr_hkdffgbvv6), 2026-09-08. Read-only; advisory, not acceptance authority.
Contract: docs/adr/2026-09-wokbench-context.md (Accepted). Sources read: full ADR, docs/context.md,
.context/workbench-context/{CONTEXT,acceptance-map,runtime-review,progress,harness-evidence,harness-baselines,performance-evidence,usability-evidence}.md,
internal/contextapi (delivery.go), internal/contextengine/engine.go, internal/contexthook/hook.go,
internal/contextconfig/config.go (through Resolve/limits), internal/contextdaemon/runtime.go (complete),
cmd/workbench/context_cli.go, cmd/workbench/context_runtime.go, acceptance/context_test.go,
plus targeted greps of contexttrace.go, transport.go, contextprovider/client.go. `go vet` over all
context packages: clean.

## Verdict

The implementation substantially delivers the ADR's judged outcomes, and the evidence base is unusually
honest (fixture vs production Go admission separated; instrument limitations preserved; performance evidence
scoped to its workload). I found no new correctness defect that creates false suppression, silent truncation,
opt-in bypass, or unbounded state. Remaining risks are efficiency/latency with external providers, small
doc/behavior mismatches, and the already-tracked in-flight corrections in progress.md.

## Goal coverage (with evidence)

1. Automatic relevant next-action guidance — PROVEN. Production Go binary admitted project ai-context
   guidance into real Claude 2.1.260 and Codex 0.153.4 model request #2, with irrelevant and inactive
   controls negative (harness-baselines.md, six-case repetition on binary b06bff44…). Unprompted agent use:
   usability-evidence.md repair trial (marker used without reading the manifest; one recorded containment
   intervention).
2. Explicit opt-in / inactive silence — PROVEN in code and test. cmd/workbench/context_runtime.go:145-149
   returns before hook decode, Ensure, providers, or cache when activation != enabled; acceptance
   context_test.go:106-134 asserts 100 inactive hooks produce no stdout/stderr and no artifacts
   (home config, runtime dir, socket, locks, cache, project file). Measured inactive p95 20.46 ms.
3. One global installation — PROVEN. Reconcile-based setup (context_cli.go:1030-1169) preserves unrelated
   hooks/settings, replaces only owned `context hook --harness <h>` commands, byte-idempotent on second run
   (context_test.go:136-206), sets Codex `additionalContextLimit: 0` and `features.hooks = true` without
   clobbering authored TOML. Setup surfaces the Codex native /hooks trust step instead of granting trust.
4. BB independence — HOLDS. No BB/thread/project model in internal/context*; audience identity comes only
   from harness adapter evidence.
5. Audience/epoch isolation and exact delivery confirmation — HOLDS. Engine Confirm requires generation,
   audience+epoch, exact offer identity, profile revision, profileValidUntil instant, and exact emitted
   content identity (engine.go:365-480); failed/unknown handoff deletes the live offer and never mints a
   receipt (engine.go:425-432); replayed confirmations answer from a completed-offer record; delayed stale
   confirmations cannot clear a replacement slot (completed/live separation + sameOfferIdentity).
   Reset in one directory revokes the session's partitions across all scopes (runtime.go:1278-1282).
6. Contributor/profile vs receiving audience — HOLDS. Profile providers return facts only; audience/epoch
   are runtime-owned (assignAudience, runtime.go:675-721); profile revision changes withdraw pending rather
   than confirming stale content (engine.go:413-419).
7. Bounded numeric explanations — HOLDS. Numeric-first records with decision-time reason parameters,
   10,000-byte samples with UTF-8-boundary excerpts and authoritative omission counts
   (contexttrace.go:2029-2100), 5,000,000,000-byte default disk cap (contexttrace.go:29), rotation gaps
   reported as rotated/dropped/corrupt/unreadable, cache clear starts a new generation and provably does
   not clear delivery state (context_test.go:305-312).
8. Ownership/cleanup — HOLDS. Deep clones at every boundary; single close owners with joins
   (Runtime.Close, closeProviderState, client.terminate SIGKILLs the provider's process group and joins
   readers); engine is the single authority for pending/offers/receipts with partitionState.sourceEvidence
   explicitly non-authoritative metadata (runtime.go:132-135); provider capacity charged until the close
   owner finishes (closeProviderState comment + performance-evidence barrier test).
9. Discoverability — GOOD. status/history/inspect/explain/cache commands default to cwd, disclose resolved
   scope, and the missing-guidance diagnosis trial succeeded via ordinary commands (usability-evidence.md).

## Findings (prioritized)

F1 (medium, efficiency/latency — external providers). validateOfferItems re-invokes
context.contribute for every selected external-provider item to prove freshness
(runtime.go:987-1034), including items that the same Observe call just collected
(collectContributions → planPartition in the same request). Every delivered external offer costs at least
two provider round-trips inside the ≤2 s hook budget; a provider near its 500 ms deadline can flip fresh
content to deferred. Counterexample: provider with deadlineMs=900; collect ≈0.9 s + validate ≈0.9 s +
startup leaves no room; offer degrades to deferral although nothing changed. Not an ADR violation (bounded
and explained) but a real acceptance risk: performance-evidence.md measured builtin-only. Smallest fix:
skip revalidation for items whose identity was produced by this very request's contribution pass (evidence
`lastSeen == now`), keeping the second call only for genuinely delayed pending items.

F2 (low-medium, batch scoping vs ADR fault isolation). Engine.Plan rejects the entire contribution batch
when any single contribution fails normalizeContribution (engine.go:251-254), and the runtime plans all
providers' contributions in one batch (runtime.go:1082-1094). The provider client hydrates identities, so
today a malformed item should be caught per-provider; but any future hydration gap (e.g. provider-authored
content ID mismatch surviving the client) would let one bad contributor suppress healthy contributors'
delivery in that hook — contrary to ADR "malformed optional contributions … remain scoped to their
contribution". Smallest fix: drop the offending contribution with its reason and continue the batch.

F3 (low, doc/behavior mismatch on resume). transitionKind maps SessionStart source "resume" to a
session-start transition (hook.go:619-622), which clears receipts and pending (engine.go:217-220) and
allocates a fresh epoch — so every resumed Claude/Codex session re-delivers previously confirmed guidance.
Safe direction per ADR (duplicate preferred to false suppression), but docs/context.md:83-85 says only
"reset, fork, or compaction … starts a new epoch"; resume behavior should be documented as a deliberate
conservative choice, or resume continuity established from stronger evidence later.

F4 (low, trace duplication). Offered/confirmed decisions write one record per selected item, each carrying
a sample of the full offer body (runtime.go:2090-2099); an offer coalescing N sources stores N near-identical
≤10 KB samples per event, on top of per-contribution samples. Bounded and interpretable, but it spends the
rotating cap ~N× faster than needed. Optional: sample the body once on a parent record; keep per-item
records numeric.

F5 (nit). boundedTraceContent (runtime.go:2119-2121) is an identity function; the bound lives downstream
in contexttrace.prepareRecord. Rename or bound in place so the name doesn't overclaim.
hasFixedStringFlag (hook.go:1094-1101) treats any arg with prefix "-F" (e.g. "-Foo") as fixed-strings;
affects only selector interpretation confidence. setTOMLFeatureHooks (context_cli.go:1263-1273) drops a
trailing comment on an existing `hooks = false` line when rewriting it.

## Deliberate limits (correctly scoped, should stay documented)

- DeliveryElidedReminder is declared (contextapi/delivery.go:145) but never produced: repetition yields
  suppression silence, never a compact reminder. ADR only constrains reminders if they exist; docs/context.md
  does not promise them. Fine for v1.
- Pending state is not durable across daemon restart; disclosed via generation and ADR-sanctioned.
- Pkl project declaration (`workbench-context.pkl`) is future scope owned separately; the shipped JSON
  `.workbench/context.json` is the single activation source and docs/context.md documents it as such.
  No duplicate activation path exists in code. The ADR's Pkl paragraph is intent, not a claim the JSON
  implementation supports Pkl — implementation should not be failed for its absence.
- Codex has no generic Read hook; unknown/ambiguous shell stays unknown (no fabricated evidence), matching
  docs/context.md:272-274.

## Missing evidence / unknowns

- External-provider hook latency and overload are exercised in unit/ownership tests but absent from the
  measured performance workload (performance-evidence.md says so). F1 makes this worth one measured run.
- progress.md lists in-flight final corrections owned by four threads; several appear already merged in the
  source I read (ambiguous-confirmation rejection runtime.go:418-424, numeric idle-eviction reasons
  runtime.go:1460-1465, per-scope/audience capacity runtime.go:788-805, inspection lease runtime.go:586-601,
  closing-provider capacity charge). The three driver witnesses in
  /tmp/workbench-context-runtime-review/engine_authority_test.go were red at progress-writing time; I did not
  run them (read-only, driver-owned) — their current color is unknown to me.
- Implementation remains uncommitted; review is of working-tree state at branch cole/2026-08-agent-native-workbench (8480694 base).

---

# Pass 2 (2026-09-08 ~21:20 UTC) — new changes since pass 1

## Coverage ledger (sha256 first 16 hex at review time)
cc4ca6f7c8a69c96 internal/contextdaemon/runtime.go (re-reviewed: Confirm preflight, validateOfferItems skip, revokePartitions snapshot)
1fb0028296fe483d internal/contextdaemon/runtime_review_test.go (new cold-inactive test reviewed)
c2aeb973e45b14f4 transport.go / 8310f9b28dcf0c3d trace_runtime.go (scanned: joins + deep-clone/drop markers landed)
b8bf9037d4fc3e4e api.go · 6ed83c3b3c05682b engine.go (unchanged semantics; method offsets identical to pass 1)
3b39ad06aa611a93 hook.go · 49c1e02bcd93a3e7 config.go (tail 1199-1360 newly read: resolveEffectiveDelivery)
da4e6bd05608c13f normalize.go (newly read: per-item hydration) · afe5a6c08023fdbf client.go · ae1a4711e67d4e7e builtin.go
6028f7d1320bc854 contexttrace.go · 9d58cdc3719d8a06 context_runtime.go · 229be28d4aa57642 context_cli.go
dc872fe6e8013f2b acceptance/context_test.go · 355eb37770e377b2 docs/context.md (resume note landed at :86-88)

## Dispositions of pass-1 findings
- F1 (redundant same-request revalidation): FIXED and verified sound. currentEvidence is populated only from
  this Observe's provider responses (runtime.go:1101-1110); skip at validateOfferItems (runtime.go:1011-1013);
  Confirm passes nil (runtime.go:442) so confirmation-time and delayed pending items still revalidate. Later/
  pending freshness checks preserved. Nit: explanatory comment sits mid-request-hydration (runtime.go:1016-1021).
- F3 (resume epoch doc): FIXED (docs/context.md:86-88). Stray .rej/.orig files remain in tree.
- Cold inactive Confirm starts runtime: FIXED. Preflight before beginRequest (runtime.go:405-413) uses the same
  loadCurrentActivation; enabled path reloads after admission (runtime.go:418-428) so withdrawal during startup
  is handled without touching old-scope offers. Regression test is a truthful public-seam witness (withdrawn
  result + no runtime/cache dirs). Cross-partition safety holds: findInactiveOfferPartition filters
  audience+epoch+pathWithin, ambiguity touches nothing, and engine.Withdraw fences exact partition identity.
  Residual invariant note: the preflight withdrawal path holds no request lease, so it may run concurrently
  with/after Close; benign today (pure memory, no trace/provider use) but any future trace call there would
  race trace shutdown — worth a guarding comment or closing-check.
- revokePartitions profile read race: FIXED (profile cloned under state.mu before traceDecision; remaining
  unlocked state.scope/state.audience reads are construction-immutable).
- Transport ServeListener joins + trace deep queue ownership/drop markers: landed (transport.go:347-415;
  trace_runtime.go cloneTraceRecord/noteDropLocked/writeDropMarker).

## New findings (pass 2)
- F2 UPGRADED (medium, reachable defect — F2a): whole-batch engine rejection is reachable through documented
  home configuration, not just a hypothetical hydration gap. resolveEffectiveDelivery (config.go:1258-1290)
  never couples delivery Queue.MaxItemBytes with runtime.MaxContributionBodyBytes/provider MaxBodyBytes;
  hydration bounds bodies only by the body limits (normalize.go:111-125). Counterexample: home
  {"delivery":{"queue":{"maxItemBytes":4096}}}, default body limits; one builtin ai-context rule with a 10 KB
  message passes hydration, then Engine.Plan normalizeContribution body>maxBytes (engine.go:667-669) returns
  DeliveryRejected for the ENTIRE batch (engine.go:251-254) — every hook, deterministically suppressing all
  healthy contributors in the scope. Same family: batch-count precheck (engine.go:238) and coalesced render
  where "Sources: <label>\n"+body exceeds MaxItemBytes though the raw body passed (engine.go:289-292;
  needs an external provider that sets Source.Label — builtin sets none, builtin.go:627). Invariant (ADR):
  malformed/oversized contributions stay scoped to their contribution. Smallest fix: in Engine.Plan, drop the
  offending contribution with its reason and continue the batch, reserving whole-batch rejection for
  request-identity failures (generation/scope/audience/digest); optionally also clamp effective
  MaxContributionBodyBytes <= Queue.MaxItemBytes at resolveEffectiveDelivery minus a render margin.
- F4 TRIAGED: optional optimization, not a defect. Every Observe returning an offer writes one record per item,
  each with a <=10 KB sample of the same offer body (runtime.go:2090-2099), and a still-live re-offered offer
  re-traces on every hook: an unconfirmed N-item offer over H hooks writes N*H duplicate samples. Bounded,
  interpretable, cap enforced — correctness unaffected. If taken: sample the body once on an offer-level
  record; per-item records stay numeric (item.content.id already links them).
- Housekeeping (low): docs/context.md.orig/.rej, runtime.go.orig, runtime_review_test.go.orig are patch
  leftovers that must not be committed; the .rej hunk's content already landed.

Open finding IDs after pass 2: F2 (upgraded, owner implementer), F4 (optional, disposition = implementer's
choice), F5 nits (comment placement, hasFixedStringFlag prefix, TOML comment drop), housekeeping .orig/.rej.
External-provider latency measurement still absent from performance evidence (F1 fix reduces its urgency).

---

# Pass 3 (2026-09-08 ~21:5x UTC) — F2 fix review

## Coverage ledger updates (sha256 first 16 hex)
c4734a7f63dd80a6 internal/contextengine/engine.go (re-reviewed: Plan per-item isolation, trimNewItems, isolateOversizedItems)
bd7c83098149d86d internal/contextengine/engine_test.go (two new isolation witnesses reviewed)
42a4d41f08748ea9 internal/contextdaemon/runtime_review_test.go (daemon oversized-builtin witness reviewed)
a59d31f86cfc08a7 internal/contextdaemon/runtime.go (only change vs pass 2: F1 skip comment relocated to the skip — nit fixed)
config.go, normalize.go, docs/context.md unchanged. .orig/.rej leftovers removed.

## Dispositions
- F2: FIXED and verified, including by independently running the three focused tests (all pass, -count=1).
  Plan drops malformed/oversized items per item with contributor attribution (engine.go Plan admission loop);
  trimNewItems structurally removes ONLY identities accepted in this call, so old pending can never be evicted
  by a new flood; isolateOversizedItems pops from the group tail, which preferentially sheds new items — old-only
  groups cannot become oversize between calls because removeSource/expiry only shrink renders and limits are
  fixed per engine, so the old-item fallback is unreachable in practice and safe (drop+reason) if ever reached.
  Both helpers run on the cloned working set before the transactional cap checks and the atomic commit;
  receipts/live offers are untouched except deliberate changed-source revocation. The daemon witness reproduces
  the exact pass-2 counterexample (home queue.maxItemBytes=128, oversized builtin, healthy executable sibling)
  end-to-end with attribution asserted.
- F5 comment-placement nit: FIXED (runtime.go:1011-1013).
- Housekeeping (.orig/.rej): FIXED.

## Residual findings (pass 3)
- R1 (low-medium, same family as F2, follow-up): the Queue.MaxPendingBytes bound still rejects the WHOLE batch
  (engine.go, "pending queue bound would be exceeded" path) with no per-item trimming, unlike the item-count and
  item-size bounds. Counterexample: home queue {maxItemBytes:4096, maxPendingBytes:8192}; old deferred pending
  item A=4KB; new batch B=4KB + healthy C=1KB → working 9KB > 8192 → batch rejected, C suppressed, recurring
  while A stays pending. Note the asymmetry: global-admission byte failures DEFER (state preserved) while this
  path REJECTS. Smallest fix: extend trimNewItems (or a byte-variant) to shed new items until the byte bound
  fits, keeping batch rejection only for invariant-violation backstops.
- R2 (nit, fairness): trimNewItems (count) runs before isolateOversizedItems, so a valid new item can be
  trimmed to satisfy the count bound while an oversized item that isolation is about to drop still occupies a
  slot. Swapping the order removes the unfairness.
- Backstop note: the post-isolation "coalesced complete body exceeds item bound" reject is now unreachable
  (isolation enforces the same renderGroup metric); fine as defense, worth a comment saying it is a backstop.

Open after pass 3: R1 (implementer), R2 nit, F4 optional (untouched, implementer's choice), remaining F5 nits
(hasFixedStringFlag prefix, TOML comment drop), external-provider latency measurement still unmeasured.

## Pass 3 final-state addendum
Implementer finalized during the pass; re-verified hashes at close:
c4734a7f63dd80a6 engine.go (unchanged from mid-pass review — Plan analysis stands)
df88f3799eecf2ac engine_test.go (content-identity test updated to Idle+malformed-reason; I ran it under -race: pass)
c603c7043f5e19a5 runtime.go (added lease-invariant comment at :1375 — closes pass-2 residual note)
42a4d41f08748ea9 runtime_review_test.go (unchanged from mid-pass review)
Independent witness runs: 3 focused F2 tests -count=1 pass; TestContentIdentityMustMatchChangedBody -race pass.
F2 disposition: CONFIRMED FIXED (final state). Open: R1 (MaxPendingBytes batch reject), R2 ordering nit,
F4 optional, F5 nits, external-provider latency measurement.

---

# Pass 4 (final R1/R2 confirmation)

Ledger: 5cb776d262a138d4 engine.go, b99ffdf2689497c5 engine_test.go (only files changed since pass-3
close; runtime.go c603c704 and runtime_review_test.go 42a4d41f unchanged, as implementer stated).

R1 CONFIRMED FIXED: trimNewItemsByBytes (engine.go:921-954) sheds only current-call accepted identities
(same newItems gate as the count trim), recomputes pendingBytes/renderGroup each iteration so coalesced
label shrinkage is accounted, terminates (found=false breaks to the backstop reject for legacy state),
attributes each drop to the contributor, and runs on the cloned working set before the backstop rejects,
retained-bytes, global admission, and the atomic commit. Old pending is structurally preserved.
R2 CONFIRMED FIXED: order is now isolateOversizedItems -> count trim -> byte trim (engine.go:291-296);
the coalesced-size rejection carries the requested backstop comment (engine.go:298-299).
Witness TestPlanIsolatesNewItemsFromPendingByteOverflow is faithful (old 20B pending preserved, new item
shed tail-first with attribution, no whole-batch rejection). Tail-first shedding is positional rather than
size-optimal (it dropped the small healthy sibling rather than the larger new item) — deterministic,
bounded, re-recruitable; acceptable, noted, not a finding.

Independently ran under -race, -count=1, all PASS: TestPlanIsolatesNewItemsFromPendingByteOverflow,
TestPlanIsolatesMalformedContributionFromHealthyBatch, TestPlanIsolatesOversizedRenderedItemFromHealthyBatch,
TestContentIdentityMustMatchChangedBody, TestPlanUsesExactGlobalTotalsAndRejectsAtomically,
TestQueueExpiryAndExactBudget.

Remaining open (unchanged): F4 optional trace-sample dedup, F5 nits (hasFixedStringFlag prefix, TOML comment
drop), and the disclosed proof boundary that external-provider latency was measured at Runtime.Observe
(50ms provider, one call, 61.19ms) rather than through the assembled CLI hook — honest disclosure; assembled
CLI timing is being closed by the implementer.

## Closure note
Implementer declared final: engine.go 5cb776d2, engine_test.go b99ffdf2, runtime.go c603c704,
runtime_review_test.go 42a4d41f — re-hashed after the declaration and byte-identical to the pass-4
verified state, so the confirmation applies to the exact final code. Implementer reports full
go test ./... , go vet ./... , and engine/daemon race jobs green (their runs, not re-executed here —
my independent evidence remains the six focused -race witnesses on these hashes). F1/F2/F3/R1/R2 and
all invariant notes closed. Open, minor, tracked: F4 optional trace dedup, F5 nits, assembled-CLI
external-latency timing (implementer closing).
