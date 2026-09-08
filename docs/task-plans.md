# Task plans

`workbench plan` operates a local task dependency graph. A Pkl definition owns
the intended work; its sibling JSONL ledger owns recorded evidence, decisions,
and grant changes. Readiness is derived from those two inputs. The command
does not assign or dispatch agents.

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
| `adjudicate` | `--node ID --result RESULT --by OWNER --reason TEXT` | Record the named oracle owner's judgment (`pass` or `fail`). |
| `rule` | `--node ID --choice OPTION --by OWNER` | Record the Selector owner's choice. |
| `land` | `--node ID` | Record artifact availability separately from verification. |
| `grant` | `--node ID --paths PATH,PATH --reason TEXT` | Add authority paths to an Action after collision checks. |
| `revoke` | `--node ID --paths PATH,PATH --reason TEXT` | Withdraw paths introduced by ledger grants. |
| `note` | `--text TEXT --by AUTHOR`, optional `--node ID` | Record context that discharges no dependency. |

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
exists. `--by` is attribution supplied by the caller, not authenticated identity.
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
New grants are checked before append. Revocation and notes remain available
when a definition edit has created a graph violation, allowing recovery.
These ownership declarations do not sandbox verification processes.

## Compatibility and local ownership

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

The `0.7.1` executable uses the existing Pkl package
`package://github.com/phosphorco/workbench-go/releases/download/0.7.0/workbench@0.7.0#/Plan.pkl`.
Use the release URI to inspect the contract outside this evaluator. Runnable
plans amend `workbench:plan`; release URLs do not enable network imports.

## Verify the contract

```sh
go test ./internal/plan ./internal/evaluate ./cmd/workbench
go test ./...
go vet ./...
```

The plan tests compare frontier categories and dependency expressions against
captured TypeScript results. CLI tests exercise Pkl composition, real process
results, ownership checks, legacy evidence, confinement, cancellation and both result
projections through the command entrypoint.
