# Declaration-contract publication

This lane publishes the source-value contract only. It does not implement the
Pkl evaluator, activation loader, snapshot cache, trace writer, or conversion
from the removed JSON activation format.

## Public Pkl modules

The bundled schema reader publishes these exact module URIs:

| Source | URI | Embedded value |
| --- | --- | --- |
| `pkl/WorkbenchContext.pkl` | `workbench:context` | `pkl.Context` |
| `pkl/WorkbenchContextTypes.pkl` | `workbench:context-types` | `pkl.ContextTypes` |
| `pkl/WorkbenchContextHome.pkl` | `workbench:context-home` | `pkl.ContextHome` |

The project and home modules import the shared types module. They expose
`AiContext` and `Executable` aliases from that module so the authored spelling
remains `new AiContext {}` / `new Executable {}` without duplicating class
definitions.

Both schemas evaluate defaults as values. An absent authored property is not a
second semantic state: project `enabled` defaults to `true`, `scope` to
`subtree`, `contributors` to an empty mapping, and `profile` to an empty
profile. Home policy, exclusions, and directory selections also have explicit
empty values. The project and home examples are evaluated through these exact
bundled schema strings in `declaration_schema_test.go`; they are not hand-built
JSON fixtures.

Project declarations contain:

```text
enabled: Boolean = true
scope: "directory" | "subtree" = "subtree"
contributors: Mapping<ContributorName, AiContext | Executable> = {}
profile: Profile = {}
```

`AiContext` is the builtin contributor and has the fixed `contribute`
capability. `Executable` contains an executable path, direct arguments,
requested `profile`/`contribute` capabilities, provider-owned JSON-shaped
settings, and optional narrowing contributor limits. Settings are a bounded
recursive JSON value at the decoder boundary: objects preserve arbitrary
string keys, arrays preserve order, and strings, numbers, booleans, and null
are retained without a string-only restriction. Paths are resolved
relative to the consuming declaration root or accepted as absolute; ambient
PATH lookup is not part of this contract. A disabled contributor is never
started.

The home declaration contains typed `limits`, `exclusions`, and explicit
`directories`. Each directory selection has an absolute `root`, `enabled`,
`scope`, `contributors`, and `profile`. Home location does not imply coverage.
Home policy applies to the resolved contributor descriptor, including future
executable changes according to the policy ruled by the activation owner.

There is no authored `profileProviders` list. Profile selection is derived
from the enabled effective contributor values whose capabilities contain
`profile`; the builtin contributes only. Capabilities do not establish
audience identity or epochs. Provider RPC payloads, delivery identities, and
receipt values remain unchanged.

## Canonical Go values

`internal/contextapi/config.go` replaces the authored JSON values without
aliases:

| Removed value | Canonical value | Fields |
| --- | --- | --- |
| `ProjectFile` | `ProjectDeclaration` | `Enabled`, `Scope`, `Contributors`, `Profile` |
| `HomeScopeFile` | `HomeDirectorySelection` | `Root`, `Enabled`, `Scope`, `Contributors`, `Profile` |
| `HomeFile` | `HomeDeclaration` | `Limits`, `Exclusions`, `Directories` |

`Contributor` is a concrete closed value with `Kind`, `Enabled`, and typed
`AiContext`/`Executable` values. Pkl emits a flat contributor object, so
The internal evaluated-contributor projection is the one explicit decode
boundary and places Pkl's scalar executable path into the typed Go `Executable`
value while cloning arguments/capabilities and retaining the settings object
byte-for-byte. `CapabilitiesForContributor` is the pure value
helper that makes profile derivation explicit. Empty maps, slices, profiles,
and settings are values; no JSON `null` versus omitted property is a domain
signal. Owned strings and slices must be cloned when retained by a loader,
resolver, provider resource, or snapshot.

The contributor map key is the effective `ProviderConfig.ID`. A named builtin
such as `project-guidance` therefore remains `project-guidance` in status,
history, and source attribution; it is not silently renamed to `ai-context`.
Multiple named `AiContext` entries are valid source-isolated contributors, and
their IDs remain distinct through runtime revalidation. There is no alias or
trust registry. Home `limits.providerPolicy.allowed` entries match these
resolved descriptor IDs after the contributor's declaration origin, scope, and
executable descriptor have been resolved. An allowlist entry delegates that
named descriptor's future executable/settings/capability changes within the
same authorized declaration root and scope, subject to current home bounds; a
renamed contributor or changed authority/root requires a new explicit match.

`HomeLimits` contains the typed provider policy, profile defaults, delivery,
runtime, and disposable-cache bounds. `HomeSnapshot` keeps its existing
derived runtime/cache/delivery fields and scope-oriented JSON shape where that
avoids gratuitous wire changes. `EffectiveConfiguration` no longer contains
`ProfileProviders`; runtime providers already carry their capabilities, so a
profile-capable provider is selected by filtering those unchanged
`ProviderConfig.Capabilities` values.

`ScopeIdentity` remains loader-owned. Its stable scope identity is distinct
from declaration/dependency revision and from the combined effective digest.
No authored schema version, compatibility reader, converter, registry, or JSON
activation fallback is retained.

## Evaluator input and exact dependency capture

The evaluator lane should consume a value contract equivalent to:

```text
EvaluationInput {
  origin: absolute declaration path
  authority: project | home
  sourceBytes: exact bytes read for the entry module
  schemaURI: workbench:context | workbench:context-home
  bounds: finite required evaluator input/output/diagnostic/import/deadline limits
}

DependencyCapture {
  designation: resolved module URI requested from the reader
  importingOrigin: unknown with the current pkl-go ModuleReader API; do not
                   fabricate it with a source parser
  resolvedCanonicalTarget: canonical target selected for this read
  bytes: exact bytes supplied to the evaluator
  digest: digest(bytes)
}

DependencyManifest {
  captures: deterministic DependencyCapture list
  diagnostics: bounded evaluation diagnostics
}

EvaluatedDeclaration {
  kind: project | home
  project: ProjectDeclaration value
  home: HomeDeclaration value
  dependencies: DependencyManifest
  revision: this declaration's source/import closure plus schema/evaluator identity
  evaluatorIdentity: implementation-derived evaluator identity
}
```

Process-memory control is intentionally absent from this source contract until
the evaluator/probe lane proves a concrete enforcement mechanism. Joined child
cleanup is a return-time invariant, not serialized activation evidence; resource
measurements belong to probe evidence.

The module reader captures bytes at the evaluator read boundary. It resolves
and captures each designation once per evaluation, serves repeated reads from
that captured value, and never hashes a later second read. The current reader
does not expose the importing origin; the contract records that fact as
unknown rather than inventing import edges. Freshness re-resolves every
captured designation under the declaration's authority root and compares the
resolved canonical target and exact bytes, so symlink retargeting and ABA
edits invalidate reuse without a Pkl source parser. The cache key must include
declaration origin, authority, schema/evaluator identity, and the captured
dependency manifest. Missing imports and reads outside the declaration root
are errors. No environment, clock, network, glob, unrestricted resource
reader, or cross-root import is available.

Snapshot publication may use a bounded disposable derivative of these values,
never a second activation authority. A corrupt, missing, or incompatible
snapshot is a cache miss. Snapshot bytes, temporary publication bytes, and
metadata share capacity with trace bytes under one protocol. The reservation,
temporary-publication, atomic-replacement, eviction, crash-recovery, and
cross-process owner-lock details remain pending the probe/root allocation
ruling. In particular, trace and snapshot writers cannot each spend the full
cap, and a reduced current home cap must close writes or reclaim/admit before
new reservation. This document does not freeze a cache interface.

## Loader composition seam proposed to root

To avoid an import cycle, `contextconfig` should receive function values from
the composition root rather than import evaluator implementation types:

```text
Load(ctx context.Context, options LoadOptions, deps LoadDependencies) (LoadResult, error)

LoadDependencies {
  Evaluate(ctx, EvaluationInput) (EvaluatedDeclaration, error)
  SnapshotRead(ctx, SnapshotKey) (encoded []byte, found bool, err error)
  SnapshotPublish(ctx, SnapshotKey, encoded []byte) error
}
```

Snapshot bytes are opaque to `contextcache`; evaluation and encoding happen
outside the pool lock. `SnapshotPublish` receives the same hook context: acquiring the shared
cross-process owner lock, reserving capacity, writing temporary bytes, and
publishing must all stop at the caller's deadline. A lock wait cannot be an
unbounded side effect hidden behind an otherwise context-aware `Load`.

The composition root supplies one exported evaluator freshness function over
the captured entry source/import closure. Cold result admission, snapshot reuse
and `Policy.Validate` call that same function; publication invokes the policy
validator under the shared lock. `contextcache` does not carry a duplicate
manifest validator. A known stale capture cannot become an enabled result just
because optional snapshot publication failed. Cancellation propagates to the
caller rather than becoming an authored-configuration diagnosis.
A present home declaration is evaluated before
the first cache publication, while an absent home declaration remains a real
no-source lookup so a later home file is observed. No-source lookup never
invokes evaluator or snapshot dependencies. The provider-policy allowlist is
explicit delegation to the actual contributor ID after declaration-root
resolution, not executable pinning; a later descriptor change remains subject
to current bounds and authority.

The exact snapshot methods remain provisional until the shared capacity
protocol is ruled. `Evaluate` and snapshot dependencies are not called when no
project or applicable home source is discovered; that path returns inactive
without acquiring a Pkl process, Bun, evaluator, cache, or daemon. A present
home source may require bounded cold evaluation to establish inactivity, but
must not start contributors or write history. Before any reservation, the
successfully evaluated home policy supplies the current allowance. A zero
dependency bundle is therefore valid for no-source lookup, while a source
lookup must fail closed if the required evaluator/capture dependency is absent.

## Red witnesses recorded before implementation

Before these contracts existed, the real baseline was run from
`/home/ubuntu/phosphor/workbench-go` with:

```text
go test ./internal/contextapi -run 'Test(PklDeclarationIsTheActivationAuthority|LegacyJSONDeclarationIsInert)$' -count=1
```

The temporary witness at
`internal/contextapi/declaration_behavior_test.go` showed two behavioral
failures: a valid `workbench-context.pkl` returned inactive with
`activation.inactive` / `no applicable project or home opt-in declaration`,
and a real `.workbench/context.json` containing
`{"schemaVersion":1,"optIn":true,"includeChildren":true}` returned enabled
instead of inert. The temporary loader-only witness is removed after recording;
the permanent real-binary acceptance owner retains the cutover behavior test.
