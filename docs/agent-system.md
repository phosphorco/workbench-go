# Agent-native Workbench system design

Status: proposed target architecture. This document is not a description of
commands already available in Workbench `0.6.1`. The released surface is listed
in the repository [README](../README.md); the staged implementation plan is in
[ROADMAP.md](../ROADMAP.md).

## Design center

Workbench should feel to an agent like one coherent, inspectable machine rather
than a directory containing several repositories and tools.

At every point, an agent should be able to answer five questions cheaply and
exactly:

1. **What is true?** Which facts are authoritative, which were observed, how
   fresh are they, and what evidence supports them?
2. **What is intended?** What Subject, repository declarations, ownership rules,
   and invariants define the desired environment?
3. **What can I do?** Which bounded operations are available, what authority do
   they require, what will they affect, and approximately what will they cost?
4. **What happened?** Which actions completed, which did not, and what exact
   state is recoverable now?
5. **What should I inspect next?** What is the smallest additional observation
   that will reduce uncertainty enough to choose a safe action?

The central agent loop is therefore:

```text
                         immutable contracts
                                 │
                                 ▼
authoritative intent ──► observe ──► derive ──► plan ──► apply ──► verify
        ▲                    │          │          │         │         │
        │                    └──────────┴──────────┴─────────┴─────────┘
        │                                      evidence
        │                                         │
        └──── promote durable knowledge ◄── receipts and diagnostics
```

`setup` remains the one-command composition of this loop. The target control
plane also exposes the read-only stages so an agent can understand and budget an
operation before granting it mutation authority.

## The abstraction tower

Every layer has one job, consumes only the layer below it, and produces a typed
artifact for the layer above it.

| Layer | Abstraction | Owns | Must not own |
| --- | --- | --- | --- |
| 0 | Contracts | Versioned schemas, identities, validation laws, protocol versions | Ambient machine state |
| 1 | Authority | Subject, resource declarations, Git-owned source and skills, context prose | Observations or derived files |
| 2 | Observation | Local Git/filesystem facts, optional remote facts, evidence and freshness | Desired-state interpretation |
| 3 | Environment index | Normalized resource, package, skill, buildable, ownership, dependency, and health graph | Mutation authority |
| 4 | Intent | Desired closure, placement, work line, projections, and invariant set | A second source of truth |
| 5 | Plan | Ordered action graph, preconditions, effects, capabilities, costs, and recovery boundaries | Permission to ignore stale preconditions |
| 6 | Application | Capability-bounded execution and atomic publication where possible | Inference or policy invention |
| 7 | Verification | Re-observation, convergence proof, scoped code-health evidence | Silent repair |
| 8 | Orientation and delivery | Minimal context views, change composition, snapshots, handoff, and durable receipts | Hidden mutable memory |

The layers divide into three planes:

- The **authority plane** is authored. It contains the Subject, repository
  declarations, source, skills, and context prose.
- The **knowledge plane** is derived and read-only to consumers. It contains
  observations, the environment index, graph slices, plans, diagnostics, and
  impact analysis.
- The **execution plane** changes state only through explicit capabilities. It
  contains setup application, checks, buildable lifecycles, commit sagas,
  snapshot reproduction, and guarded prune.

An execution result feeds evidence back into the knowledge plane. It never
silently rewrites the authority plane.

## One fact, one owner, many projections

The existing ownership model remains the foundation:

- `workbench-subject.pkl` owns the current repository entrypoints and work line.
- Each resource's `workbench.pkl` owns repository composition and irreducible
  resource semantics.
- Git-owned source owns facts that can be derived from source.
- Git-owned `skills/` trees own reusable agent guidance.
- Tracked `AGENTS.pkl` owns context prose.
- Workbench owns complete generated projections.

The agent-native system adds new *derived* artifacts, not new authorities:

| Artifact | Lifetime | Authority | Purpose |
| --- | --- | --- | --- |
| Environment index | Replaceable cache | None | Canonical normalized read model |
| Reconciliation plan | Expiring, content-addressed | Exact capability grant only after revalidation | Preview one possible transition |
| Receipt | Append-only local evidence | None over desired state | Prove what was attempted and completed |
| Diagnostic | Bound to an observation digest | None | Explain a conflict and enumerate lawful remedies |
| `AGENTS.md` | Replaceable projection | None | Small bootstrap for agents |
| Snapshot | Explicit immutable input when reproducing | Exact revisions only | Reconstruct repository commits without changing Subject branch policy |

A plan is never desired state. A receipt is never desired state. A cached
observation is never current merely because it exists.

## The environment index is the agent's map

Today, an agent must combine `AGENTS.md`, console output, generated files, Git
commands, and repository traversal to reconstruct the environment. The target
system projects one versioned Environment Index from the same normalized facts
used by planning and setup.

The index should be atomically published beneath a versioned directory:

```text
.workbench/index/v1/
├── manifest.json
├── resources/<resource-key>.json
├── packages/<package-key>.json
├── skills/<skill-key>.json
├── buildables/<buildable-key>.json
├── ownership.json
├── graph.json
└── health.json
```

The manifest contains the protocol version, Workbench binary identity, contract
version, Subject digest, observation digest, desired-state digest, shard
digests, publication time, and freshness summary. Shards avoid forcing an agent
to load the complete topology to answer a narrow question. Keys are encoded
stable identities, not lossy filenames.

Every material fact carries or references:

- its semantic owner;
- its evidence source;
- the observation mode used to obtain it;
- the time or Git revision at which it was observed;
- a content digest where exact bytes matter;
- whether it is `current`, `stale`, or `unknown` for the requested operation.

The index is private, ignored, reproducible, and contains no credentials,
environment-variable values, or arbitrary command output. Consumers may read it
directly, but the CLI is the compatibility boundary and can return bounded
slices without exposing storage layout.

### First-class graph edges

The index reifies relationships that are currently distributed across several
projections:

```text
Subject ─entrypoint─► Resource ─includes─► Resource
Resource ─contains─► Package ─imports─► Package
Resource ─exports─► Skill ─composes─► Skill
Resource ─declares─► Buildable ─produces─► Output
Path ─owned-by─► Resource/Package/Workbench
Change ─touches─► Path ─impacts─► Package/Check
```

Every edge has provenance. An import edge, for example, identifies the source
file and line; an include edge identifies the declaring resource and Pkl path.
This makes `why is this here?`, `what owns this path?`, and `what could this
change affect?` ordinary graph queries rather than fresh investigations.

## Observation has explicit cost and freshness

Observation is not one undifferentiated operation. Every read command accepts a
refresh policy with three semantic levels:

| Refresh | May do | Will not do | Typical use |
| --- | --- | --- | --- |
| `none` | Read a previously published index | Touch Git, filesystem metadata outside the index, run Pkl/Bun, or use network | Instant orientation and repeated queries |
| `local` | Re-read local declarations, Git state, source fingerprints, and owned outputs | Fetch or contact remotes | Normal edit loop |
| `remote` | Perform the complete remote-aware preflight needed for an apply-ready plan | Mutate canonical checkouts or owned projections | Before setup, snapshot reproduction, push, or prune |

`unknown` is a valid result. A local-only observation must not infer remote
health. A cached fact must not be reported as current unless its evidence still
matches. Commands report both the requested and achieved freshness.

Expensive observations are content-addressed. Unchanged declaration and source
digests reuse parsed Pkl, import, skill-catalog, and graph facts. Remote ref
observations have explicit expiry rather than an implicit promise of freshness.
Concurrent readers may share immutable cache entries; canonical publication
still uses one atomic manifest swap.

Correctness always outranks cache reuse. A mutating operation revalidates every
relevant precondition against live state.

## Plans are typed action graphs

A reconciliation plan is a deterministic, machine-readable graph of actions.
Each action has:

- a stable action ID derived from its meaning;
- the invariant it advances;
- exact targets and expected before-state;
- declared effects;
- prerequisite action IDs;
- required capabilities;
- estimated network, process, byte, and filesystem cost;
- its atomicity boundary;
- its revalidation rule;
- its recovery or resume behavior;
- diagnostic IDs for every refusal.

Representative capability classes are:

```text
observe.local
observe.remote
git.clone
git.fetch
git.switch
projection.writeOwned
dependency.reconcile
process.verify
buildable.execute
git.commit
git.push
checkout.delete
```

Capabilities are semantic, not raw syscalls. `projection.writeOwned`, for
example, grants writes only to the exact Workbench-owned outputs in the plan.
It does not grant general filesystem write access.

`workbench setup` grants the established non-destructive setup capability set.
An explicit `workbench apply <plan>` grants only the capabilities recorded in
that exact plan. Delete, commit, push, and repository-owned executable actions
remain separate commands and are never smuggled into setup.

Before the first canonical mutation, apply verifies:

1. plan and protocol compatibility;
2. Subject and desired-state digests;
3. every batch-wide precondition that can be checked in advance;
4. exact target identity and path containment;
5. that the caller-granted capability set covers the actions;
6. that no action exceeds an explicit caller budget.

An unexpected change produces a stale-plan result and a minimal observation
needed to replan. It never triggers speculative repair.

## Receipts make partial state legible

Every mutating command emits and durably records one receipt before returning.
The receipt contains:

- command and protocol identity;
- Subject, observation, desired-state, and plan digests;
- requested and actually exercised capabilities;
- action outcomes and exact changed paths or refs;
- before and after evidence;
- verification results;
- warnings and unresolved diagnostics;
- continuation information for recoverable partial work.

Receipts use one common envelope even when an operation has a specialized
journal, such as a cross-repository commit saga. Specialized journals remain
the recovery authority; the receipt links to them by digest and summarizes
their state.

Receipts are append-only evidence, not a mutable database. A compact latest
index points to them. Retention can remove old, unreferenced receipts without
affecting source, desired state, or the ability to observe the environment
again.

## Diagnostics are data, not prose fragments

Human messages remain concise projections of a structured diagnostic. The
machine form contains:

- stable `code` and occurrence `id`;
- severity and outcome class;
- affected resources, paths, refs, and graph nodes;
- evidence with expected and observed values;
- the invariant that failed;
- blocked action IDs;
- zero or more lawful remedies;
- whether each remedy is automatic, manual, destructive, or external;
- the capability and estimated cost of an automatic remedy;
- the smallest refresh that could change the conclusion.

A remedy is a typed operation plus parameters, not a shell command assembled
from untrusted text. Some failures intentionally have no automatic remedy.

All commands use the same versioned result envelope and produce a useful JSON
result for success, refusal, unhealthy state, stale plans, and recoverable
partial completion. Exit codes remain a transport convenience; the envelope's
outcome is canonical.

## Progressive disclosure keeps context small

The default generated `AGENTS.md` should be a compact bootstrap, because many
agents load it automatically. It should state:

- the governing work line and current index digest;
- the non-negotiable ownership and preservation rules;
- the current overall health/freshness in one line;
- the first commands for status, path ownership, scoped context, planning,
  verification, and delivery.

It should not inline a large private topology, every skill, or a full command
manual. Those are queryable projections.

The target read surface supports progressively larger views:

```text
status summary
  └── resource/package/path description
       └── relevant dependency and skill closure
            └── complete environment graph or evidence
```

Path-oriented queries are especially important. Given one path, an agent should
receive its repository, package, ownership class, generated/hand-owned status,
applicable skills, relevant checks, and nearby graph edges in one bounded
response.

## The driver's workflow

An agent using Workbench should follow one uniform lifecycle.

### 1. Orient

Read the compact root instructions and request local status. This establishes
the Subject digest, work line, participating resources, current freshness, dirty
repositories, active partial operations, and blocking diagnostics.

### 2. Scope

Describe the paths or packages relevant to the task. Workbench returns ownership,
instructions, skills, dependencies, and the existing impact boundary. The agent
loads only the context needed for that scope.

### 3. Plan the environment transition

If the Workbench is not converged, request a plan with the least refresh level
that can settle the decision. Inspect capabilities, effects, cost, and
diagnostics. Use `setup` directly when the standard safe grant is obviously
appropriate.

### 4. Edit source

Edit Git-owned source and typed declarations. Never edit generated projections.
Path ownership queries settle ambiguity before the edit.

### 5. Verify proportionally

Request impact analysis from the current change set. Run sound targeted checks
while iterating, then the full required verification before delivery. A targeted
pass is recorded as scoped evidence and never mislabeled as whole-Workbench
health.

### 6. Deliver exactly

Inspect the normalized cross-repository diff. Generate or author an exact
commit-plan candidate, review its file and hunk selection, then invoke the
recoverable commit saga. Push progress and partial failure are returned through
the same receipt protocol.

### 7. Hand off

Share the Subject tuple for a moving collaboration line, a Snapshot for exact
revisions, and receipt/change identifiers for completed or partial operations.
Another agent can reconstruct both intended state and execution evidence
without inheriting an opaque conversation.

## Accretion without hidden memory

An agent-accretive system gets cheaper and more accurate as work is performed,
but it must not turn incidental history into secret authority.

Workbench uses three knowledge horizons:

1. **Ephemeral reasoning** lives only in the current agent interaction.
2. **Local evidence** lives in content-addressed observations, indexes, plans,
   receipts, and journals. It accelerates and explains work but is disposable.
3. **Durable knowledge** is deliberately promoted into Git-owned source,
   declarations, tests, skills, or context prose and reviewed like code.

Promotion is explicit. Repeated diagnostics can suggest that a declaration,
skill, test, or context instruction should be improved, but Workbench does not
make that edit automatically. The receipt provides the evidence needed to make
the durable change accurately.

Skill accretion remains compositional. The index records skill descriptions,
domains, owners, selection roots, composition edges, and byte cost. Scoped
context queries return the least valid skill closure for a resource or path.
The Workbench root still exposes the complete assembled catalog, but agents need
not load it wholesale.

## Verification and impact

The same environment graph used for setup should drive verification selection.
Given a set of changed paths, Workbench derives:

- directly changed packages and resources;
- reverse package-import closure;
- affected generated projections;
- relevant skill or declaration validation;
- buildables whose declared producer inputs changed;
- the smallest known check set and whether it is complete.

Verification reports its proof scope explicitly:

```text
path < package < resource < impacted closure < complete Workbench
```

No narrower proof may be summarized as a broader one. Unknown dynamic behavior
widens the recommended scope rather than being ignored.

## Compatibility boundaries

Workbench has three independently versioned contracts:

1. **Resource contracts** define authorable Pkl semantics and preserve historical
   meanings such as placement rules.
2. **Control protocol** defines command envelopes, diagnostic codes, index
   records, plans, and receipts.
3. **Storage schemas** define private cache and journal layouts.

The CLI is the boundary for storage compatibility. Agents should not depend on a
private path unless the control protocol explicitly designates it as readable.
A new control-protocol version does not require resource authors to amend a new
Pkl contract. A new resource semantic does.

Existing `setup`, `check`, `commit`, `snapshot`, `prune`, `run`, `buildable`, and
`skills check` behavior remains available. New read surfaces compose the same
internal normalized facts; they do not create parallel interpretations.

## Agent-interface laws

The ten existing Workbench domain laws continue to govern repositories,
branches, generated outputs, and delivery. The agent control plane adds these
laws:

1. **Every material fact carries provenance and freshness.** Unknown and stale
   are explicit states.
2. **One normalized model serves setup, status, planning, orientation, impact,
   and diagnostics.** No command gets a weaker private interpretation.
3. **Read authority is separable from mutation authority.** An agent may inspect
   a complete plan without granting its effects.
4. **Every mutation is capability-bounded and preconditioned.** Raw ambient
   authority is never the public abstraction.
5. **Every operation returns a versioned structured outcome.** Human prose is a
   projection, not the only record.
6. **Context is progressively disclosed.** Default orientation is small; detail
   is exact and queryable.
7. **Partial work is durably legible and resumable.** Failure never erases the
   last proven state.
8. **Derived knowledge is disposable; durable knowledge is reviewed.** Hidden
   local memory never becomes policy.
9. **Resource cost is visible and constrainable.** Correctness outranks a budget,
   so an insufficient budget refuses rather than weakens proof.
10. **Every successful mutating operation closes the loop with re-observation.**
    Intended effects are not reported as completed effects.

## Deliberate boundaries

The agent-native design does not make Workbench:

- an autonomous project manager that invents goals;
- an unbounded shell or arbitrary-hook framework;
- a credential store or secret-bearing event log;
- a source of implicit permission to commit, push, delete, publish, or contact
  people;
- a daemon required for correctness;
- a substitute for Git history or reviewed documentation;
- a package-version solver or multi-revision environment manager;
- an oracle that reports remote or dynamic facts it did not observe.

The goal is not maximum automation. The goal is maximum *legibility and
control*: the smallest accurate observation, the narrowest lawful capability,
and the strongest recoverable evidence for every transition.
