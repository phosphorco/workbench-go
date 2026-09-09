# Runtime composition contract

Status: published implementation declaration. This file is owned by the
runtime worker and is the handoff point for the CLI consumer.

The runtime package is the composition root for one Workbench context daemon
per OS user. Its public declarations are in
[`internal/contextdaemon/api.go`](../../internal/contextdaemon/api.go). The
daemon does not know about BB, transcripts, agent queues, or project-local
write operations.

## CLI-facing lifecycle

The hook path has two explicit phases:

```text
bounded contextconfig.Load/Resolve(cwd, explicit home path)
  ├─ inactive/conflict/invalid -> empty successful hook, no Ensure, no trace
  └─ enabled -> contexthook supplies observation/opportunity
                 -> contextdaemon.Ensure (bounded Unix socket startup)
                 -> Client.Observe
                 -> exact Offer.Body handoff to the host
                 -> Client.Confirm only after exact successful stdout handoff
```

`Ensure` is a transport operation, not activation. An inactive caller must
never invoke it. A runtime re-loads the current home/project declarations from
`Paths.HomeConfigPath` for every Observe and Confirm, so a stale hook snapshot
cannot keep withdrawn authority active. The runtime never writes project files.

### Declarations

```go
paths := contextdaemon.Paths{
    HomeConfigPath: homeConfig,
    RuntimeDir:     runtimeDir,
    SocketPath:     runtimeDir + "/context.sock",
    LockPath:       runtimeDir + "/context.lock",
    ServerLockPath: runtimeDir + "/context.server.lock",
    CacheDir:       cacheDir,
}
runtime, err := contextdaemon.NewRuntime(contextdaemon.RuntimeOptions{Paths: paths})
err = runtime.Serve(ctx) // owns socket, workers, provider children, and joins
client, err := contextdaemon.Ensure(ctx, contextdaemon.ClientOptions{
    Paths: paths, Starter: startServeProcess,
})
defer client.Close()
result, err := client.Observe(ctx, contextdaemon.ObserveInput{
    WorkingDirectory: normalized.Host.WorkingDirectory,
    Host: normalized.Host,
    Observation: normalized.Observation,
    Opportunity: normalized.Opportunity,
    Transition: normalized.Transition,
})
// Emit result.Decision.Offer.Body exactly at the adapter boundary first.
confirmed := client.Confirm(ctx, contextdaemon.ConfirmInput{
    WorkingDirectory: normalized.Host.WorkingDirectory,
    Identity: result.Decision.Offer.Identity,
    Handoff: contextapi.HandoffOutcome{
        State: contextapi.HandoffConfirmed,
        EmittedContent: result.Decision.Offer.Identity.Body,
    },
    At: time.Now(),
})
```

For enabled read-only status, `contextdaemon.Connect(ctx, ClientOptions)` only
dials an existing socket. It does not create directories, acquire the startup
lock, start a process, open the cache, or renew residency. The returned
`Client.Status(ctx, StatusRequest)` exposes the bounded current partition
audience/epoch, profile revision and expiry, engine counts, and trace status.
An absent daemon is a dial error and is not converted into an implicit startup.

The in-process `Runtime` methods are the truthful test seam. `ServeListener`
uses a caller-owned listener for transport tests; production `Serve` owns its
private Unix socket. No package-global runtime or cache is used.

`Runtime.Close` is the join point for its listener-independent workers,
admitted requests, trace worker, and provider processes. `Client.Close` closes
only its connection; it never kills or waits for the shared daemon. Context
cancellation requests shutdown but does not establish that listener,
connections, trace workers, or provider processes have exited until the
runtime owner calls `Runtime.Close` or `Serve` has joined its connections.

## Audience epoch and continuity

Adapters provide a harness-namespaced audience/session identity and explicit
transition evidence. They do not mint or prove a runtime epoch. `Epoch == 0`
means “unassigned”; it is not a reset signal.

The runtime keeps a bounded map keyed by the adapter's harness-namespaced
session identity (audience ID), deliberately shared across applicable cwd
scopes so a reset observed in one scope revokes that session's receiving
history in another. This map is created anew for every daemon generation. The
rules are:

| Observation | Runtime action |
| --- | --- |
| Explicit session-start/reset/fork/compact transition | Allocate a fresh monotonic epoch and revoke old pending/offers for that audience. |
| Ordinary hook after a known tracked lifecycle | Reuse the tracked epoch; repeated ordinary hooks may reuse receipts and offers. The runtime exposes the assigned audience with canonical `ContinuityKnown`, regardless of an adapter's per-hook `Unknown` evidence. |
| First-seen session, genuinely unknown continuity, or runtime-lost continuity | Allocate a fresh epoch and record an explainable conservative-fresh reason. Ordinary `ContinuityUnknown`/`Epoch == 0` from an adapter does not reset an already tracked lifecycle by itself. |
| Nonzero adapter epoch disagreeing with tracked runtime state | Do not trust the adapter value; allocate a fresh runtime epoch and explain the mismatch. |

Session ID alone is never proof that model context is continuous. Epoch state
is scoped by harness and session, while partition state remains scoped by
effective cwd/scope, audience, epoch, config, and generation. A missed hook in
an inactive or withdrawn directory does not update the map. New daemon
generations invalidate all previous offers and receipts. A fresh epoch permits
duplicate full delivery because suppressing content without continuity proof
is unsafe.

## Composition and freshness

For enabled requests the runtime recomputes the current activation, obtains
profile facts, and calls `contextengine.Compose` before `Plan`. Provider I/O is
outside runtime/engine locks. The runtime passes only typed observations and
bounded profile inputs to providers. Provider completion order never selects a
profile winner; `Compose` owns precedence.

Before delayed pending delivery or confirmation, runtime revalidation includes:

- current home/project opt-in, exclusion, provider policy, and config digest;
- profile validity and the current composed profile revision;
- provider/source authority and current provider facts;
- builtin source revision, including a deleted `ai-context.md` file.

An incremental hook result is not a full provider inventory. Absence of a
contribution from such a result cannot invalidate an older pending source. If a
provider has no targeted freshness/revalidation operation, the runtime must
surface that gap and defer/withdraw rather than silently deliver stale content.

`Plan` returns an exact `Offer`. The adapter must emit the exact body at its
supported boundary before sending `ConfirmInput`. Only `HandoffConfirmed` with
equal emitted content can create receipts. Failed or uncertain output never
creates suppression. The runtime fills the engine-only
`contextapi.OfferConfirmation` fields from current activation/profile state;
the CLI never invents `ProfileRevision`, `ProfileValidUntil`, scope, or config
values.

The runtime does not retain a second offer-routing index. Confirmation scans
the bounded resident partition set using generation, audience/epoch, current
scope/config, and directory containment. It confirms only a unique candidate;
equal-root candidates (including cross-harness candidates, since the public
identity has no harness field) are rejected without engine mutation. Inactive
confirmation applies exact `Engine.Withdraw` item identities to that unique
candidate and does not revoke an entire partition by numeric OfferID.

## Bounds and ownership

One engine owns mutable pending/live-offer/receipt state per immutable
scope/audience/epoch/config/generation partition. Runtime status uses the
engine's `Stats` as authoritative queue/receipt counts; it does not maintain a
second mutable count. Runtime maps, admitted requests, trace queue items and
bytes, provider processes, active scopes/audiences, offers/receipts, and idle
residency are bounded by resolved home limits. No request creates an
unbounded goroutine or queue.

Before each mutating engine call, the runtime holds its admission fence and
computes `contextapi.GlobalAdmission` as residual maximum resulting totals:
`MaxPendingItems`, `MaxPendingMemoryBytes`, `MaxOutstandingOffers`, and
`MaxReceipts`. Other partitions contribute their authoritative `Engine.Stats`;
the target's retained recruitment evidence and any newly admitted original
recruitment evidence are charged separately. Zero is a real residual grant.
`Engine.Plan(input, admission)` and `Engine.Confirm(input, admission)` then
perform the exact prospective transaction under the engine mutex, including
body/metadata/replay bytes and receipt delta. Evidence refresh follows Plan,
and evidence pruning follows successful Confirm, before the admission fence is
released, so another partition cannot observe a state change without its
runtime-owned metadata charge.

The runtime owns one bounded trace queue and worker. The worker exclusively
owns the synchronous `contexttrace.Store`; hooks enqueue deep-copied records
with the complete bounded contribution body so Store can preserve its
UTF-8-safe head/middle/tail sample and original byte count. Queue drops carry
an explicit evidence-gap counter. Stable numeric ContributionIDs correlate
each coalesced item across observation, contribution, offer, and confirmation;
reason code/origin/summary/time and typed historical inputs remain inspectable.
Query, Inspect, and Clear are sent through the same FIFO worker mailbox, so no
second process-local Store opens the cache directory and accepted records
cannot overtake a later clear.

Idle TTL evicts derived providers and resident partitions. Provider eviction
first performs the provider client's bounded graceful `shutdown` request while
its serialized stream is idle, then joins the process, process group, stderr
reader, and in-flight operations; termination is the cleanup fallback. If a partition still
has pending items or an unconfirmed offer, eviction records an inspectable
`delivery.withdrawn` idle-TTL disposition, drops that partition's pending,
offer, receipt, and recruitment state, and permits later re-recruitment.
Inspection does not renew activity. An idle daemon may exit once its listener,
requests, and owned workers are drained; inactive hooks never start it or keep
it warm.

## Wire protocol

`Client` and `ServeListener` use bounded newline-delimited JSON-RPC 2.0. The
JSON-RPC envelope is `contextapi.JSONRPCRequest/JSONRPCResponse`; every params
and result value is decoded into one of the concrete API types in `api.go`.
The server applies a maximum frame size before decoding and bounds concurrent
admitted requests. A request deadline is propagated to provider calls and
transport writes. A connection owns its request goroutine and is joined by
the server before shutdown.

The transport exposes only these methods:

```text
runtime.observe
runtime.confirm
runtime.status
runtime.query
runtime.inspect
runtime.clear
runtime.ping            (Ensure readiness only)
```

The protocol carries no transcript and no durable delivery journal. Restart
recovery is a declared generation discontinuity; explanation history is
disposable and independent from delivery truth.
