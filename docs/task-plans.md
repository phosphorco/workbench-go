# Task plans

`workbench plan` operates a local task dependency graph. A Pkl definition owns
the intended work; its sibling JSONL ledger owns recorded evidence, decisions,
grant changes, and reported work. Readiness is derived from those two inputs.
The command records assignments but does not dispatch agents.

This task graph is distinct from Workbench's proposed **reconciliation plan**,
which describes environment changes derived from a Subject and observations.
Task plans do not run setup or grant repository mutation authority.

For agent guidance, `workbench self skills export workbench-plan` exports the independent
planning skill folder. See [installation](skills.md); the environment skill is optional.

For a large or changing effort, follow the skill's
[campaign walkthrough](../skills/workbench-plan/references/planning-tactics.md).
It shows how a compatibility investigation becomes a decision, how an agreed
contract enables parallel consumers, and how a new requirement revises the
affected acceptance evidence while preserving the ledger.

## Author and inspect

Use the [example definition](../examples/planning/feature.plan.pkl) as a starting
point. `workbench plan schema` prints the bundled schema for inspection. Amend
`workbench:plan` to use the schema embedded in the running binary, or save the
schema as a local `.pkl` module for editor tooling and amend that file. No package
download or project discovery occurs during plan evaluation.

```sh
workbench plan tick feature.plan.pkl
workbench plan tick feature.plan.pkl --format json | jq '.ready[].id'
workbench plan check feature.plan.pkl
workbench plan recall feature.plan.pkl
workbench plan export feature.plan.pkl > snapshot.json
```

`workbench plan --help` owns the complete command and flag reference. A positional
file and `--plan FILE` are equivalent. The sibling ledger is derived by replacing
`.plan.pkl` with `.ledger.jsonl`; `--ledger` selects another location. A missing
ledger means no recorded events. An unreadable or malformed ledger is an error.

Ordinary read and write results default to `--format agent`: XML-shaped context
boundaries with compact Markdown leaves and literal commands, paths, and text.
These boundaries are for readers; they do not promise parser-valid XML. Use
`--format json` for scripts. `export` defaults to JSON for machine interchange;
`export --format agent` selects the reader projection. `schema` prints Pkl.
Both result formats derive from the same graph and ledger fold.

The JSON projection preserves authored IDs, owner, outcome, grant, oracle fields,
and declaration order within each readiness group. Each node is one compact
line; summary fields and groups have their own lines. `needs`, `missing`, and
`stale` use canonical expressions such as `verified(api)`. These reference the
producer's **id**, which may differ from a local Pkl variable name. `probe`
contributes one derived evidence dependency. Oracle objects remain structured
for queries such as `.ready[].oracle.run`.

`valid` distinguishes a lawful graph from one with diagnostics. Empty `ready`
does not itself mean failure: counts distinguish complete work from blocked work.
`criticalPath` is the longest dependency chain by node count, including completed
nodes; it is not a duration estimate. `recall` and `export` add completed nodes
and the chronological event history. Oracle process output goes to stderr so
stdout remains the selected result projection during verification.

## Command reference

All definition commands take `FILE.plan.pkl` or `--plan FILE`, plus optional
`--ledger FILE`, `--module-root DIR`, `--timeout DURATION`, and `--format agent|json`.
The default timeout is `30s`. Consult `workbench plan --help` on the installed
release for invocation diagnostics.
Cancellation interrupts evaluation and oracle execution; the trusted Pkl process
startup handshake finishes before cancellation is observed.

| Verb | Additional flags | Result or effect |
| --- | --- | --- |
| `schema` | None | Print the embedded Pkl contract without loading a definition. |
| `check` | None | Check graph laws; diagnostics exit nonzero. |
| `tick` | None | Derive readiness, blockers, stale needs, counts, and critical path. |
| `recall` | None | Include completed nodes and all chronological ledger events. |
| `export` | None | Write the complete snapshot to stdout, JSON by default. |
| `verify` | `--node ID`, `--by AUTHOR`, `--cwd DIR` | Execute a Mechanical oracle; without a node, run initially ready Mechanical Guards. |
| `adjudicate` | `--node ID --result RESULT --reason TEXT`, optional `--by OWNER` | Record the named oracle owner's judgment (`pass` or `fail`). |
| `rule` | `--node ID --choice OPTION`, optional `--by OWNER` | Record the Selector owner's choice. |
| `land` | `--node ID` | Record artifact availability separately from verification. |
| `grant` | `--node ID --paths PATH,PATH --reason TEXT` | Add authority paths to an Action after collision checks. |
| `revoke` | `--node ID --paths PATH,PATH --reason TEXT` | Withdraw paths introduced by ledger grants. |
| `note` | `--text TEXT`, optional `--by ACTOR --node ID` | Record context that discharges no dependency. |
| `update` | `--node ID`, assignment/status flags below, optional `--by ACTOR` | Atomically record assignment and reported work, independently of acceptance. |

Read verbs never execute oracles or write the ledger. Exit zero means the
command succeeded, including when no work is ready; nonzero can mean invalid
invocation, graph diagnostics, a missing runtime, a refused write, or failed
verification. Read the structured result and stderr to distinguish the cause.

## Compose typed definitions

- **Action** declares `grant` and an `oracle`.
- **Guard** declares `observes` and an `oracle`.
- **Selector** declares distinct `options`, an `owner`, an optional `question`,
  and an optional Guard `probe`.

Use `module.produced(action)`, `module.verified(actionOrGuard)`,
`module.ruled(selector)`, and `module.granted(action)` inside amended definitions.
The helpers take node values, preventing wrong producer kinds in ordinary Pkl
authorship. Workbench also checks membership, duplicate IDs, cycles, disconnected
components, selector options, and overlapping grants on unordered Actions.
Grant overlap retains the original planner's directory-prefix semantics;
arbitrary glob-language intersection is not inferred.

Pkl supports local variables, methods, amendments, generators and imported
fragments. For a fragment, import `workbench:plan` as `Plan` and return typed
`Plan.Action`, `Plan.Guard`, or `Plan.Selector` values from methods. The fragment
test in `cmd/workbench/plan_test.go` is an executable example.

Local module imports are confined to the definition's directory by default.
`--module-root DIR` explicitly expands that boundary, including for definitions
under a shared fragment tree. Files must have `.pkl` names; symlinks cannot escape
the root. Environment, resource, network and external-reader access are denied.
Dynamic repository discovery must happen outside evaluation and supply an
explicit local Pkl inventory. Loading never executes authored TypeScript.

## Record evidence and decisions

[for=AGENT]
Use `tick` to inspect readiness. Read the selected node's outcome, grant and
oracle before doing work. Record artifact availability with `land` and verify
the acceptance instrument with `verify`; these discharge different needs.
Re-tick after recording evidence or a decision. Use `recall` after losing context.
[/AGENT]

```sh
workbench plan land feature.plan.pkl --node api
workbench plan verify feature.plan.pkl --node api --timeout 2m
workbench plan adjudicate feature.plan.pkl --node review \
  --result pass --by reviewer --reason 'Inspected the artifact against the brief.'
```

Mechanical verification runs `bash -c` in the caller's working directory, or
`--cwd DIR`. It records the actual exit result; a failed oracle exits nonzero.
An interrupted or unstartable command does not mint evidence. Named verification
may refresh a completed or self-stale node but cannot bypass its dependencies.
Without `--node`, verification runs the initially ready Mechanical Guards.

An Adjudicated oracle requires its named owner's recorded judgment. A Selector
requires its named owner's recorded choice after probe/dependency evidence
exists. `--by` (or inferred actor identity) supplies attribution, not authenticated
identity.
`ruled(selector)` waits for a current ruling; it does not automatically select
different subgraphs by choice. Express that subsequent plan revision explicitly.

A historical Mechanical oracle is authored as:

```pkl
oracle = (module.mechanical("test -f failing-witness.txt")) { once = true }
```

After a passing observation, `once` evidence remains historical and verification
does not rerun it. Ordinary later failures retract earlier passes. Changes to
commands, adjudication criteria/owner, options, and artifact footprints stale
the corresponding evidence just as in the TypeScript planner. Reordering an
option or footprint set is not a semantic change.

Grant events add paths to an Action's initial grant. Revocation withdraws paths
introduced by grant events; change the definition to alter its initial grant.
New grants are checked before append. Revocation, notes, and work updates remain
available when a definition edit has created a graph violation, allowing recovery.
These ownership declarations do not sandbox verification processes.

## Actors, assignment, and reported work

Assignment and reported activity are separate from acceptance. They never
satisfy dependencies, retract proof, change `owner`, or dispatch workers.
A node can be acceptance-complete and still report a stall. `tick`, `recall`,
and `export` expose each node's `work`; the top-level `work` array also keeps
reported work on completed nodes visible in compact reads.

### Import actor kinds

The exported planning skill supplies reusable [BB threads](../skills/workbench-plan/assets/actors/bb.pkl),
[Codex-style threads](../skills/workbench-plan/assets/actors/codex.pkl),
[email users](../skills/workbench-plan/assets/actors/people.pkl), and
[services](../skills/workbench-plan/assets/actors/services.pkl) under `assets/actors/`.
Copy the kinds you need beside your plan into `actors/`, then combine them:

```pkl
amends "workbench:plan"
import "actors/bb.pkl" as bb
import "actors/people.pkl" as people

actors {
  kinds {
    ["thread"] = bb.thread
    ["user"] = people.email
  }
}
// Supply meta and nodes as usual.
```

Amend imported values to customize them, for example:

```pkl
["thread"] = (bb.thread) { infer { env = "OUR_THREAD_ID" } }
```

The [repository example](../examples/planning/coordination.plan.pkl) imports the
same authoritative assets across the tree. From the repository root, run:

```sh
workbench plan check examples/planning/coordination.plan.pkl --module-root .
```

Keep `--module-root .` on subsequent commands for that example. Ordinary plans
with copied local actor files need no wider import boundary. These convenience
definitions are examples; adapt their patterns and variables to the harness.
For custom kinds, see the skill's [coordination reference](../skills/workbench-plan/references/coordination.md).

### Recognize and infer identity

Kind names match `[A-Za-z][A-Za-z0-9_-]*`. Each kind declares a canonical
`pattern`, optional `infer.env`, and optional `aliases` with `pattern` and
`replacement`. Patterns use Go's RE2 syntax and match the entire input.
Recognition validates shape, not existence, authentication, or authorization.

Unqualified input contributes canonical and alias matches from every kind.
Aliases normalize once into IDs that must match the canonical pattern. Equal
`(kind, id)` candidates deduplicate; conflicting identities are ambiguous, even
within one kind. No declaration order wins. A known `KIND:` prefix selects one
kind: a canonical ID then bypasses aliases for reliable round trips; a
noncanonical spelling still uses that kind's aliases. Thus `thread:thr_api`
always identifies the canonical `thr_api`, while `thread:@thread:thr_api` can
use the BB alias. Bare-input ambiguity rules remain unchanged.

Replacement references follow Go's Unicode-aware name boundaries. Unknown
captures are definition errors. Use braces to separate a capture from adjacent
name characters: `${1}é` means capture 1 followed by `é`; `$1é` names an unknown
capture. `$1`, `${name}`, and `$$` are also supported.

All writes accept `--by`. Explicit `--by ACTOR` wins over inference, even when
ambient variables are invalid. Otherwise the CLI recognizes configured nonempty
environment values within their kinds. Unset or empty variables contribute
nothing; nonempty invalid values and multiple inferred identities are errors.
An empty `infer.env` disables inference. Pkl never reads these variables, and
read commands do not need identity.

`--by @me` requests inference. Elsewhere, `@me` means the resolved command actor,
including an explicit `--by` override. Events store the resolved `actor` pair and
its qualified `by` string. Historical identities never change with policy edits.
With actor policies configured, judgments and Selector rulings fingerprint the
resolved owner; changing what an owner alias means can stale acceptance evidence
without rewriting history. Assignees never acquire an owner's authority.
Digest-free historical evidence retains its presence-only behavior.

Without actor kinds, explicit identities remain untyped (`kind: ""`); `note`,
`rule`, `adjudicate`, and `update` require `--by`. Other commands retain legacy
optional attribution, with `verify` defaulting to `workbench`.

### Assign and report activity

```sh
workbench plan update feature.plan.pkl --node api --assignee @me --status in-progress
workbench plan update feature.plan.pkl --node api --assignee user:alex@example.com --reassign
workbench plan update feature.plan.pkl --node api --status paused --text 'Awaiting acceptance.'
workbench plan update feature.plan.pkl --node api --unassign --expect-revision 3
```

Nodes are assignable by default; `assignable = false` refuses new assignments.
Unassignment remains possible after that policy changes. There is one assignee;
replacing a different one requires `--reassign`. Assignment, unassignment, and
status are independent; omitted assignment/status fields remain unchanged.
`--reassign` requires `--assignee` and cannot combine with `--unassign`.

For updates based on a prior read, supply `--expect-revision N` from
`work.revision`. It counts recorded `NodeUpdated` events for that node, starting
at zero. The CLI checks the revision and appends under the same ledger lock;
a mismatch refuses the entire update. Omitting the flag means an intentional
setter against current state. The lock serializes writes; it is not a worker
lease or cross-machine synchronization.

Statuses are `unstarted` (default), `in-progress`, `paused`, and `stalled`.
`paused` covers deliberate stopping, including awaiting acceptance, and requires
`--text`. `stalled` requires the structured fields below. There is no writable
`completed` status: use `verify`, `adjudicate`, or `rule` for acceptance.
Work updates remain available despite graph blockers or diagnostics.

Each update records one timestamped `NodeUpdated` event. New events require a
valid RFC3339Nano timestamp; the CLI supplies UTC. `work.statusSince` changes on
actual status transitions, while `updatedAt` and `updatedBy` track every work
update. These are reported intervals, not measured execution time or liveness.
`--text` belongs to a status update; use `note` for other context. Verification
does not implicitly start, stop, or resume work.

### Record an actionable stall

```sh
workbench plan update feature.plan.pkl --node api --status stalled \
  --reason 'Integration credentials are unavailable.' \
  --resume-when 'Sandbox credentials have been provisioned.' \
  --waiting-on-actor alex@example.com \
  --waiting-on-need 'verified(credentials)'
```

One current stall contains a required reason and release condition, plus an
optional actor and/or plan need. `--waiting-on-need` accepts `produced(ID)`,
`verified(ID)`, `ruled(ID)`, or `granted(ID)` with an existing, compatible
producer. This annotates waiting; use `needs` for actual graph prerequisites.
Waiting on an actor neither assigns them work nor creates a graph edge.

Notes, reassignment, and verification preserve the stall. Re-reporting `stalled`
replaces its details while preserving `stallRaisedAt` and `stallRaisedBy` for
the interval. Changing to another work status clears it and retains history.
For a valid referenced need, `work.waitingOnNeedSatisfied` reflects current
proof; satisfaction prompts explicit resume. If a plan edit removes or changes
the producer incompatibly, `work.waitingOnNeedError` explains the broken
reference and satisfaction is omitted. This warning does not invalidate the
dependency graph or add an edge. Free-text conditions and actor availability
are not evaluated automatically.

## Compatibility and local ownership

New `NodeUpdated` events require a reader with coordination support. Older Go
or TypeScript readers may reject the new event tag; preserve an original or
copied ledger when comparing versions. Historical untyped events need no rewrite;
their tags retain permissive timestamp handling.

Equivalent definitions can read existing TypeScript JSONL ledgers without
rewriting them. New evidence uses tagged SHA-256 fingerprints. Historical
`Bun.hash` fingerprints use a fixed compatibility expression over inert semantic
inputs in the bundled Bun runtime; this never imports the original TypeScript
module. Development builds need Bun on PATH only for that compatibility path.
The `legacyDigests` output identifies affected nodes. Digest-free historical
events retain their original presence-only behavior.

Writing new SHA-256 evidence is a runtime cutover: the old TypeScript reader
does not recognize those fingerprints. Use a copied ledger when comparing
the two runtimes side by side. Preserve authored IDs, node order, footprints,
commands, criteria, and options while manually expressing the equivalent Pkl
nodes; the CLI does not convert source. For example:

```sh
cp legacy.ledger.jsonl portable.ledger.jsonl
workbench plan check portable.plan.pkl --ledger portable.ledger.jsonl
workbench plan recall portable.plan.pkl --ledger portable.ledger.jsonl --format json
```

Compare readiness, stale dependencies, completed nodes, and historical events
before making the copied ledger the new write target. Keep the original for the
TypeScript reader and its legacy review page. Never point that page at a ledger
after Go has appended SHA-256 evidence.

Writes use a nonblocking process lock beside the ledger, then reread its events
before appending. Competing writers receive a retryable refusal. The lock file
can remain after exit; the OS lock lifetime belongs to the process. Read commands
do not create a ledger or lock file. A reader that encounters an incomplete
external append reports a malformed ledger rather than inventing empty state.

Plans and ledgers are local files. This interface supplies no cross-machine
synchronization, authenticated owner service, TypeScript source converter, or
web review server. The complete JSON export is the consumer boundary for a renderer; it is not
the legacy TypeScript `plan.json`/`index.json`/`diagram.mmd` web bundle. Review
`recall` or a saved export directly, or use the existing TypeScript page with its
original TypeScript definition and unmixed ledger. No `workbench plan serve`,
`watch`, `open-questions`, or automatic web adapter is implied.

The `0.11.0` executable uses the `0.11.0` Pkl package
`package://github.com/phosphorco/workbench-go/releases/download/0.11.0/workbench@0.11.0#/Plan.pkl`.
Use the release URI to inspect the contract outside this evaluator. Runnable
plans amend `workbench:plan`; release URLs do not enable network imports.

## Verify the contract

```sh
go test ./internal/plan ./internal/evaluate ./cmd/workbench
go test ./...
go vet ./...
```

The coordination end-to-end test builds the executable and runs independent CLI
processes through import composition, inferred attribution, assignment, structured
stalls, proof completion, and explicit resume. Generated CLI sequences check
acceptance independence; Go fuzz properties check JSONL replay, interval tracking,
alias normalization, and ambiguity. Run them with:

```sh
go test ./cmd/workbench -run 'TestPlan' -count=1
go test ./internal/plan -run '^$' -fuzz FuzzCoordinationLedgerProjection -fuzztime 10s
go test ./internal/plan -run '^$' -fuzz FuzzActorRecognition -fuzztime 10s
```

The plan tests compare frontier categories and dependency expressions against
captured TypeScript results. CLI tests exercise Pkl composition, real process
results, ownership checks, legacy evidence, confinement, cancellation and both result
projections through the command entrypoint.
