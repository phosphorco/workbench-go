# Workbench context shared contracts

Status: proposed implementation contract for the accepted ADR. The Go value
types live in [`internal/contextapi`](../../internal/contextapi). This package
contains data only: no filesystem, process management, locks, clocks, queue
mutation, persistence, or catch-all runtime service interface.

## Package boundaries and consumer signatures

The package owners consume and return these concrete values. The signatures are
the agreement; each owner supplies its own state and tests.

```go
// internal/contextconfig
func Resolve(input contextapi.ActivationInput) contextapi.ActivationResult

// internal/contextprovider
func Profile(input contextapi.ProfileRequest) contextapi.ProfileResponse
func Contribute(input contextapi.ContributionRequest) contextapi.ContributionResponse

// internal/contextengine
func NewEngine(partition contextapi.DeliveryPartition, limits contextapi.EngineLimits) (*Engine, error)
func (e *Engine) Plan(input contextapi.DeliveryPlanInput, admission contextapi.GlobalAdmission) contextapi.DeliveryDecision
func (e *Engine) Confirm(input contextapi.OfferConfirmation, admission contextapi.GlobalAdmission) contextapi.ConfirmationResult
func (e *Engine) Withdraw(input contextapi.DeliveryWithdrawal) contextapi.WithdrawalResult
func (e *Engine) PendingSources() []contextapi.PendingSource

// internal/contextdaemon
func HandleHook(input contextapi.ActivationInput, observation contextapi.Observation, opportunity contextapi.DeliveryOpportunity) (contextapi.DeliveryDecision, error)
func Confirm(input contextapi.OfferConfirmation) contextapi.ConfirmationResult
```

There are no shared capability interfaces. If a package needs an interface for
testing or composition, it owns a narrow local one around the concrete
function it calls. There is no `RuntimeService`, `ContextService`, or combined
provider interface.

`contexttrace` is independent and owns its own `Record`, `Query`, numeric disk
format, and limits. Its synchronous caller-facing API is:

```go
func Open(dir string, options Options) (*Store, error)
func (s *Store) Append(record Record) (AppendResult, error)
func (s *Store) Flush() error
func (s *Store) Query(query Query) (QueryResult, error)
func (s *Store) InspectContribution(query Query) (QueryResult, error)
func (s *Store) InspectTurn(query Query) (QueryResult, error)
func (s *Store) InspectProfile(query Query) (QueryResult, error)
func (s *Store) Clear() (ClearResult, error)
func (s *Store) Close() error
```

The runtime maps contextapi decisions into those trace records. No trace
storage type is imported by `contextapi`. Trace queries carry exact scope,
`MaxScanBytes`, and a `{BlockSequence, RecordID}` cursor; results report a next
cursor, partial scans, retained ranges, and explicit gaps. One writer process
owns a cache directory; the store's mutex is process-local.

## Configuration and activation

`ProjectFile` is the authored `.workbench/context.json` schema. `HomeFile` is
the separately authored XDG/user-home Workbench context schema. A loader reads
those files and produces `ActivationSnapshot`; the snapshot is the resolver's
complete input to `contextconfig.Resolve`.

The project file contains only `optIn`, `includeChildren`, `providers`,
`profileProviders`, and `profile`. It cannot author a root, home constraints,
scope ID, or revision. Its containing directory is the scope root. Home config
contains explicit `scopes` (which may name their roots), hard `exclusions`, the
provider policy, and profile defaults. A declaration participates only when
`optIn` is true and its canonical root contains the requested directory;
`includeChildren` controls subtree coverage. Home exclusions dominate project
declarations. The nearest applicable project refines defaults but cannot widen
the home provider policy.

Project `providers` are configured executables or named builtins explicitly
delegated to Workbench. `profileProviders` selects profile-capable providers.
The loader derives each `ScopeIdentity.ID`, `CanonicalRoot`, and declaration
`ConfigDigest` from the actual file origin and bytes. The snapshot's
`ConfigDigest` and `EffectiveConfiguration.ConfigDigest` are the combined
effective activation digest. They are intentionally distinct: consumers pass
both unchanged and compare each only with its matching field. No user-maintained
revision string is required to notice an edit. Empty project profile fields
inherit; explicit profile values win over inferred provider defaults.

For `ProjectFile.Providers`, JSON field absence decodes as `nil` and means the
minimal opted-in project gets the builtin `ai-context` contributor. An explicit
`"providers": []` is a non-nil empty slice and disables provider selection for
that project. The loader preserves this distinction from the original bytes;
it must not normalize both forms before resolving. A non-empty list is explicit
selection.

`ProviderPolicy.Mode` is `unrestricted`, `allowlist`, or `deny-all`; this makes
an explicit empty allowlist distinguishable from an absent policy.

Machine-wide runtime bounds are authored only in `HomeFile.Runtime`:
`hookDeadlineMs` bounds one hook-stage operation, while
`wholeHookDeadlineMs` bounds the end-to-end hook; activation ancestor/config
byte/file counts, provider/profile/contribution counts and body bytes, active
scope and audience counts, provider-process/outstanding-offer/receipt limits,
pending memory/items, `providerDefaults`, and `idleTTLMs` for runtime/provider
residency. Per-scope delivery defaults are authored in `HomeFile.Delivery.Queue`
(`maxItemBytes`, `maxPendingBytes`, `maxPendingItems`, `maxAgeMs`). Trace cache
bounds are authored separately in `HomeFile.Cache`: `diskCapBytes`
(5,000,000,000 by default), record/input/string/reason/query/scan byte or
count bounds, and block/retention bounds. Config loading owns validation and
defaulting; contextapi and contextengine do not duplicate those defaults.
Projects cannot author delivery, runtime, or cache limits. The resolver uses
the home delivery defaults and applies any narrower internal policy without
letting a project widen home/global limits.
`HomeSnapshot.Delivery` carries those home delivery defaults through loading;
`EffectiveConfiguration.Delivery` is the resolved per-scope value, so the
resolver does not recreate a second queue-default type.
`Delivery.MaxReceipts` bounds one engine partition (one audience/epoch), while
`Runtime.MaxReceipts` bounds receipt state across runtime partitions; the
runtime uses the engine `Stats` result rather than maintaining a second count.

Example `.workbench/context.json` (durations use explicitly named `*Ms` fields
and byte budgets use `*Bytes` fields):

```json
{
  "schemaVersion": 1,
  "optIn": true,
  "includeChildren": true,
  "profile": {
    "role": "reviewer",
    "preferences": [
      {"name": "detail", "value": {"kind": "number", "number": 2}}
    ]
  },
  "providers": [{
    "id": "ai-context",
    "kind": "builtin",
    "capabilities": ["contribute"],
    "limits": {
      "deadlineMs": 500,
      "maxResponseBytes": 262144,
      "maxFacts": 32,
      "maxContributions": 64,
      "maxBodyBytes": 262144
    }
  }]
}
```

Separate home/XDG configuration example:

```json
{
  "schemaVersion": 1,
  "scopes": [{
    "root": "/home/alice/work",
    "optIn": true,
    "includeChildren": true,
    "profile": {"role": "developer"}
  }],
  "exclusions": [{
    "root": "/home/alice/work/vendor",
    "includeChildren": true
  }],
  "providerPolicy": {
    "mode": "allowlist",
    "allowed": ["ai-context", "team-profile"]
  },
  "defaults": {
    "selection": {"role": "developer", "guidanceSet": "default"},
    "providers": ["team-profile"]
  },
  "runtime": {
    "hookDeadlineMs": 100,
    "wholeHookDeadlineMs": 500,
    "maxActivationAncestors": 32,
    "maxActivationConfigBytes": 1048576,
    "maxActivationConfigFiles": 64,
    "providerDefaults": {
      "deadlineMs": 500,
      "maxResponseBytes": 262144,
      "maxFacts": 32,
      "maxContributions": 64,
      "maxBodyBytes": 262144
    },
    "idleTTLMs": 3600000
  },
  "delivery": {
    "queue": {
      "maxItemBytes": 262144,
      "maxPendingBytes": 1048576,
      "maxPendingItems": 64,
      "maxAgeMs": 300000
    },
    "maxLiveOffers": 32,
    "maxReceipts": 1000,
    "maxOfferAgeMs": 300000,
    "maxRetainedBytes": 16777216
  },
  "cache": {
    "diskCapBytes": 5000000000,
    "maxRecordBytes": 65536,
    "maxInputBytes": 67108864,
    "maxStringBytes": 4096,
    "maxReasons": 64,
    "maxReasonParameters": 256,
    "maxQueryRecords": 100,
    "maxQueryBytes": 1048576,
    "maxScanBytes": 4194304,
    "maxBlockBytes": 262144,
    "maxBlockRecords": 64,
    "maxBlocks": 32768
  }
}
```

`Resolve` returns `ActivationInactive`, `ActivationConflict`, or
`ActivationInvalid` with structured reasons. Those states have no effective
providers. The inactive hook fast path is bounded lookup followed by empty
output and success: no runtime/daemon, provider, profile lookup, history,
project write, or trace append. A newly added nearer declaration must
invalidate a negative lookup through the relevant digest.

## Ownership and copying

These are Go value contracts with Rust-like ownership discipline:

- Strings, numeric IDs, `time.Time`, and the small structs containing them are
  copied values. A copied `OfferIdentity` remains an exact identity, not a live
  handle.
- Slices are borrowed for the duration of a call. A callee that queues, caches,
  starts a goroutine with, or returns them after the call must copy the slice
  and every nested slice. A producer owns returned slices; consumers treat them
  as read-only and clone before mutation.
- `json.RawMessage` is the one opaque JSON boundary. A receiver copies its
  bytes before retaining or handing them to another owner; no package parses
  another package's settings.
- Bodies are `string`, not subslices of a large byte buffer. `Body` is complete
  delivery content. A trace sample is a separate bounded copy and can never be
  used as delivery content.
- Implementations sort/canonicalize owned copies before comparing or retaining
  slices. No contract uses `map[string]any`; repeated data is typed or a
  bounded list.
- One owner mutates each queue, receipt set, provider process state, and cache.
  Provider I/O stays outside engine locks. A runtime restart creates a new
  `RuntimeGeneration`; it does not manufacture receipts for old offers.

## Observation and profile snapshots

`Observation` is an attenuated, normalized hook snapshot. It carries a
runtime-local numeric `ID`, optional host-stable `CausalID`, timestamp,
audience/epoch, scope, invocation, turn, trigger, resource paths, selectors,
outcomes, and bounded facts. Resource paths are canonical repository-relative
paths where available. Selectors retain their raw bounded pattern plus whether
it was a search pattern, path pattern, or tool argument. This preserves both
resource attention and selector-only attention without retaining a transcript.

`AdapterCapabilities` records what the harness actually supplies: audience,
epoch, and turn identity availability; observation kinds; delivery surfaces;
and budget units. `ContinuityUnknown` means suppression uses a fresh epoch. A
reset changes the epoch; it is not profile expiry. `TurnRef.State` is the one
known/unknown discriminator; an unknown turn has no invented turn ID.
Adapters carry host session identity and reset/unknown evidence; the shared
runtime owns the monotonic epoch map. A zero adapter epoch is normalized before
engine construction. A new daemon generation or untracked session starts fresh
history. `session-start`, `reset`, `fork`, and `compact` transitions revoke old
offers explicitly; stateless hooks do not hash cwd or profile to invent an
epoch.

`ProfileRequest` supplies the host snapshot, explicit profile selection, task,
scope, audience token, current config digest, current time, and bounded limits.
A profile provider returns `ProfileFact` values with applicability, validity,
provenance, and structured reasons. The audience value in a request is a
runtime-provided scope token: a provider cannot invent identity, confirm
delivery, or merge histories. A returned audience scope must be empty or echo
the request's exact audience and epoch.

`FactValidity` makes profile lifetime explicit. `until` uses `ExpiresAt`,
`activity-ttl` records the activity-derived rule and inputs, and
`refresh-after` produces a `profile.refresh-required` reason at its boundary;
an actual `ExpiresAt` still produces `profile.expired`. Validity inputs are
copied into the structured reason. Expiration or decay recomputes dependent
contributions; it does not create a receipt or clear prior delivery state.
`ProfileSnapshot.ValidUntil` is a derived upper bound for its effective facts.

The pure profile composition signature is:

```go
func Compose(input contextapi.ProfileCompositionInput) contextapi.ProfileCompositionResult
```

For each applicable key, precedence is deterministic and explicit: configured
selection, then host fact, then provider fact. Applicability must match
the actual working directory beneath the fact's directory and must match task;
it must match the exact audience/epoch when the fact is audience-scoped. Equal values coalesce. Different values at the same
precedence are resolved by stable provider/source order and emit a
`profile.conflict` reason containing both bounded values; provider completion
order is never the rule. `ProfileSnapshot.Revision` is a digest of the
canonical config digest, explicit selection, applicable host/provider facts,
scope, audience/epoch, task, and validity state. `Now` can remove an expired
fact and therefore change the revision, but does not perturb the revision while
the applicable facts remain unchanged.

## Contributions and builtin `ai-context`

`Contribution` is a complete source block. Its stable slot (`Provider` + `Key`)
is separate from `SourceRevision` and `ContentIdentity`:

```text
slot identity       = contributor + stable slot key
source identity     = provider + stable source ID
source revision     = source identity + revision ID
content identity    = content ID/digest + UTF-8 byte length
delivery identity   = audience + epoch + offer generation/ID + exact item/body
```

A source revision may change while its slot remains stable. A content digest
must change when the full body changes. Equal text from another source is not
the same source receipt. `Recruitment` explains whether a source was recruited
by an observed file or selectors; it is not folded into semantic source
identity. Contributor reasons explain matching rules and profile/config inputs;
the runtime remains the authority for admission and delivery.

The builtin provider keeps the existing `ai-context.md` frontmatter contract;
the body is not injected as a whole document. It parses TOML or YAML between
the opening and closing `---` lines:

```yaml
root: true
docs:
  - files: ["src/**/*.ts"]
    mentions: ["contextapi", "selector"]
    include: ["guidance/architecture.md"]
    message: "Keep the boundary narrow."
commands:
  - files: ["src/**/*.ts"]
    mentions: ["migration"]
    command: "go test ./internal/contextapi"
    label: "verify contracts"
    cwd: "."
```

For each observed resource, the provider walks context directories toward the
repository root and stops after a parsed `root: true`. A docs section matches
when it has no selectors, or its file glob matches the relative observed path,
or a mention occurs in bounded observed text/selectors. `include` files are
read as bounded lines and diagnostics become scoped contributions; they are not
opaque full-document injection. Selector-only observations use the repository
root context. Command file variables are expanded only for resource attention
using the existing bounded set: `file`, `fileRelative`, `fileRelativeToRoot`,
`fileDir`, `fileDirRelative`, `dirname`, `fileBaseName`, `fileName`, and
`fileExt`.

## Provider request and response schemas

The JSON-RPC envelope is deliberately small. `params` and `result` are decoded
into the typed method values below; `json.RawMessage` is not a provider data
model.

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "context.contribute",
  "params": {
      "resource": {
        "provider": "ai-context",
        "scope": {
          "id": "scope:control",
          "authority": "project",
          "canonicalRoot": "/home/alice/work/control",
          "configDigest": "sha256:project-bytes"
        },
        "configDigest": "sha256:project-bytes"
      },
      "input": {
      "requestId": 7,
      "scope": {
        "id": "scope:control",
        "authority": "project",
        "canonicalRoot": "/home/alice/work/control",
        "configDigest": "sha256:project-bytes"
      },
      "audience": {"id": "claude:session-1", "epoch": 3, "continuity": "known"},
      "observation": {
        "id": 18,
        "causalId": "hook:abc",
        "trigger": "tool-result",
        "resources": [{
          "path": "src/contextapi/config.go",
          "kind": "file",
          "operation": "read",
          "outcome": "resolved",
          "confidence": "observed"
        }],
        "selectors": [{
          "raw": "contextapi",
          "observedAs": "tool-argument",
          "interpretations": ["keyword"],
          "confidence": "observed"
        }]
      },
      "profile": {
        "revision": "profile:12",
        "audience": {"id": "claude:session-1", "epoch": 3, "continuity": "known"},
        "facts": []
      },
      "configDigest": "sha256:project-bytes",
      "limits": {"maxContributions": 64, "maxBodyBytes": 262144, "maxReasonBytes": 8192}
    }
  }
}
```

The typed initialize call is `ProviderInitializeRequest` and returns
`ProviderInitializeResponse`; optional `audience.profile` uses
`ProviderProfileRequest`/`ProviderProfileResponse`; `context.contribute` uses
`ProviderContributeRequest`/`ProviderContributeResponse`; and `shutdown` uses
`ProviderShutdownRequest`/`ProviderShutdownResponse`. A response is bounded by
the configured deadline and byte/count limits. A failed or malformed provider
is scoped to that provider, while healthy peers continue.

## Exact offer, handoff, and receipts

`NewEngine` fixes one scope/audience/epoch/runtime-generation partition and its
queue, offer, receipt, offer-age, and retained-memory bounds. The
`MaxRetainedBytes` bound covers pending metadata/bodies, live offer
metadata/bodies, receipts, and bounded replay identities. The engine owns
pending items, live offers, and receipts; callers do not pass prior receipts
or mutable queue limits. `Plan` receives the current active scope, config digest, audience,
profile snapshot, complete contributions, and one delivery opportunity. It
revalidates those inputs before admission and returns a complete `Offer` or an
explicit queued/deferred/rejected/suppressed/withdrawn outcome with reasons.

An offer is not a receipt. `Offer.Body` and every `OfferItem` body are the exact
strings passed to the adapter. The adapter emits that exact body at the
opportunity, computes the emitted content identity, and then sends
`OfferConfirmation` containing the copied `OfferIdentity` and
`HandoffOutcome`, plus current `Active`, `Scope`, `Audience`, `ConfigDigest`,
`ProfileRevision`, and `ProfileValidUntil` values. Those current values let `Confirm` revalidate
withdrawal and freshness without a package-global clock or state handle.
Profile-validity timestamps are compared by instant (`time.Time.Equal`) across
JSON-RPC, not by Go structural equality:

1. `confirmed` records local exact handoff only when emitted content matches
   the offered body;
2. `failed` and `unknown` never create suppression receipts;
3. delayed confirmation is accepted only if generation, offer ID, opportunity,
   audience ID/epoch, surface, item source revisions/content identities, and
   body identity all match the live offer;
4. only a confirmed `full-body` item can create a `Receipt`; an
   `elided-reminder` requires that same source revision/content and audience
   epoch to already have a full-body receipt;
5. withdrawal, source/config/profile changes, profile expiry, queue expiry,
   and epoch changes invalidate pending selections before admission.

`RuntimeGeneration` prevents an offer ID reused after restart from matching an
old confirmation. Unknown continuity is assigned a fresh epoch, so duplication
is preferable to false suppression. A receipt records handoff evidence only;
it does not claim that a model understood the guidance or that a host included
it in a later request.

`Plan` and `Confirm` also require a `GlobalAdmission` residual grant. Its
`MaxPendingItems`, `MaxPendingMemoryBytes`, `MaxOutstandingOffers`, and
`MaxReceipts` fields bound the target engine's resulting totals after other
partitions and runtime-owned metadata/reservations are charged. Zero is an
explicit no-capacity grant, not an unlimited value. The engine computes exact
prospective retained bytes from its owned pending/live/replay/receipt state and
the exact confirmation receipt delta while holding its mutex; a rejected call
does not commit state. Runtime serializes these short mutations with its
admission lock and uses `Stats` as the only cross-partition state source.

`Withdraw` is the provider-revalidation seam. It accepts one exact
`OfferItemIdentity` and removes only that pending item and live offers that
contain that exact identity. A stale validation for an older revision cannot
remove a newer replacement; unrelated pending items and receipts remain. Its
result reports removed-item and revoked-offer counts plus structured reasons.
`PendingSources` returns one bounded, copied `PendingSource` per pending item:
the exact identity, `SourceRef`, and `SourceRecruitment`, without bodies.

## Reasons and trace mapping

`Reason` distinguishes observed, configured, inferred, provider, adapter, and
runtime origin. `Code`, `Rule`, bounded typed params, evidence IDs, provider,
summary, and timestamp preserve actual decision inputs. The codes cover
activation exclusion/conflict/withdrawal, no match, provider failures, profile
or source changes/expiry, queue bounds, unavailable delivery, exact offer
mismatch, audience/epoch mismatch, failed/unknown output, restart, and
unavailable evidence. Reasons explain decisions; they are not suppression truth.

The runtime maps complete contribution metadata and these reasons to
`contexttrace.Record`. Trace sampling is independent: max 10,000 UTF-8 bytes
per contribution sample with original length, offsets, omissions, and
head/distributed-middle/tail excerpts; trace disk defaults to 5,000,000,000
bytes including auxiliary files. A sample is never provider guidance and never
confirms an offer. Trace gaps explicitly mean rotated, dropped, corrupt, or
unreadable evidence.

## Smallest counterexample

The easiest bug is to key suppression by `(SourceID, ContentID)` alone. Two
sessions in one directory then share a receipt, or a delayed old confirmation
clears a replacement slot. Tests must mutate audience, epoch, source revision,
content, offer generation, and exact body independently and prove that only the
fully matching confirmed offer creates a receipt.
