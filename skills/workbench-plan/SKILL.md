---
name: workbench-plan
description: Plan and execute large, uncertain, interdependent work across sessions with Pkl task graphs and evidence ledgers. Use when decomposing a complex effort, resolving design forks, revising a plan after discoveries or changed requirements, or creating, resuming, and verifying a .plan.pkl plan through workbench plan.
metadata:
  domain: orchestration
---

# Plan and execute interdependent work

[for=AGENT]
The Pkl definition owns intended work; the JSONL ledger owns observations and
decisions. Derive readiness with `workbench plan tick`. Keep those two inputs
authoritative instead of maintaining a parallel checkbox or status model.

## Enter or resume

1. Read the user's outcome, relevant repository instructions, and existing plan.
   Run `workbench plan --help` for the installed command contract.
2. For existing work, run `workbench plan recall FILE.plan.pkl`. Read the current
   definition alongside completed nodes, evidence, rulings, and notes.
3. Establish the destination, acceptance evidence, scope, and decision owners.
   Distinguish a request to discover a route from authorization to deliver it.
   Resolve implementation choices within the existing grant; ask the owner
   about choices that change the promised outcome. Continue independent work.

For large, uncertain, or changing work, read [planning tactics](references/planning-tactics.md)
before authoring or revising the graph. It walks one effort from discovery to
parallel delivery to a changed requirement, with complete Pkl definitions.

## Find a better graph

Work backward from the observable outcome: what consumes what? Inspect the
actual contracts, callers, tests, and write footprints before drawing edges.
Separate decisions from implementation and evidence from artifact availability.

```text
Unexamined plan: implement exporter → implement reader → test everything

Recovered inputs:
consumer probe → format decision → contract ─┬→ exporter ─┐
                                           └→ reader ───┴→ round-trip proof
```

The reader can start from the agreed contract when it needs only the shape.
The round-trip proof still needs both real implementations. Extracting the
contract removes a wait without removing a dependency.

Before taking the next frontier, make this short pass:

- **What could invalidate the most work?** Resolve the uncertain fork with the
  largest downstream impact; buy a small probe or prototype before its subtree.
- **What is sharp enough to specify?** Make a precise unanswered question a
  Selector, even when blocked. Keep still-vague in-scope work in a ledger note
  with a trigger for revisiting it. Do not invent downstream tasks to fill it in.
  Keep excluded work separate, with the owner's scope decision.
- **What can overlap?** Split contracts from implementations, narrow colliding
  footprints, and remove waits without real consumers. A useful work unit has
  its own outcome, grant, and proof; neither an entire department nor each command.
- **What can disappear?** Use a mechanical transform plus inspection for
  repetitive edits instead of assigning one worker per file. Choose increments
  that can be reviewed and landed independently; put irreversible effects
  behind evidence and the user's actual authorization.
- **What proves the destination?** Map every acceptance condition to an oracle.
  Where appropriate, gate a fix on an observed failing witness, then verify the
  same behavior passes. A final outcome review must also account for unresolved
  in-scope work that has not yet become nodes.

## Shape the graph

| Node | Use for | Required meaning |
| --- | --- | --- |
| `Action` | Producing or changing an artifact | `grant` identifies its write footprint; `oracle` judges success. |
| `Guard` | Observing a fact or checking acceptance | `observes` identifies the inspected footprint; `oracle` supplies evidence. |
| `Selector` | A choice requiring an owner's ruling | Distinct `options`, an optional `question`, and an optional Guard `probe`. |

Use typed references in `needs`:

- `module.produced(action)`: the consumer needs the artifact to exist.
- `module.verified(actionOrGuard)`: the consumer needs passing evidence.
- `module.ruled(selector)`: the consumer needs a current owner's choice.
- `module.granted(action)`: the consumer needs a recorded grant for that Action.

Add edges for actual input requirements. Independent tasks should remain
independent. Actions that write overlapping footprints need a justified order
or narrower grants. Artifact availability and verification are separate facts;
require both when the consumer needs both. A Selector's probe adds its own
verification dependency. A ruling does not automatically activate different
branches: revise the downstream definition explicitly after the choice.

Choose Mechanical oracles whose exit status proves the outcome, including a
known failure case. Use Adjudicated oracles when a named owner must judge a
concrete rubric. Do not turn an unresolved judgment into an always-passing test.

## Author a definition

When creating or composing a plan, read [Pkl authoring](references/authoring.md)
for the executable example, import boundary, helper syntax, and release
contract reference. Keep the main skill focused on operating the graph.

## Work from the frontier

1. Run `workbench plan check FILE.plan.pkl` after definition edits and resolve
   diagnostics. Its result already includes the frontier; use `tick` after
   subsequent observations rather than printing the unchanged graph twice.
2. Match the ready node ID (`ready[].id` in JSON) to the authored ID, which may
   differ from its Pkl variable name. Read its outcome, oracle, dependencies, and effective grant before work.
   Grant declarations describe ownership; they do not sandbox shell commands or
   confer authority beyond the user's request.
3. Produce the artifact, then record availability with
   `workbench plan land FILE.plan.pkl --node ID` when that fact is true.
4. Run a Mechanical oracle with
   `workbench plan verify FILE.plan.pkl --node ID --cwd REPO_ROOT`.
   Set `--timeout 2m` or another appropriate budget when needed. Without `--cwd`,
   oracles run in the caller's directory. Omitting `--node` verifies only the
   initially ready Mechanical Guards.
5. Record a real owner's judgment with `workbench plan adjudicate FILE.plan.pkl`:
   supply `--node ID`, `--result pass` or `--result fail`, `--by OWNER`, and
   `--reason TEXT`. For a choice, use `workbench plan rule FILE.plan.pkl` with
   `--node ID`, `--choice OPTION`, and `--by OWNER`.
   `--by` records attribution; it does not authenticate or obtain that judgment.
6. Re-tick after evidence, rulings, grants, or definition edits. Select work by
   dependencies, uncertainty, and available capacity. A blocked decision holds
   its dependent subtree; keep independent authorized work moving.

When delegating, read the [delegation example](references/planning-tactics.md#delegate-a-bounded-node).
Pass the exact plan, ledger, node ID, footprint, accepted contract, and oracle.
Confirm that the recipient started; an assignment is not active work. Inspect
the returned artifact and independently run its acceptance checks before
accepting it. Use the harness's real assignment mechanism to avoid duplicate
work: `tick` reports readiness, not worker claims, and a ledger write lock does
not reserve a node. The CLI does not dispatch agents or synchronize machines.

## Revise when reality changes

Read the [worked revision](references/planning-tactics.md#revise-after-a-discovery)
when a probe fails, a premise changes, or acceptance expands. Record the finding
and its evidence; identify affected consumers; revise their nodes and oracles;
then check and re-tick. Keep stable IDs for the same obligation and preserve the
ledger. Add new nodes only as new obligations become concrete.

Changing prose or source files alone does not reliably retract a green status.
Rerun affected checks, and change the acceptance instrument when its meaning
changes. Explicitly gate final acceptance on the revised evidence. Never erase
failed observations or weaken proof to make the frontier advance.

## Interpret and recover

- Default output uses XML-shaped boundaries and compact Markdown with literal
  text preserved; it is reader context, not parser-valid XML. For scripts use
  `workbench plan tick FILE.plan.pkl --format json | jq '.ready[].id'`.
  `export` defaults to JSON; `export --format agent` selects reader context.
  Both formats use the same fold. In JSON, `needs`, `missing`, and `stale` retain
  expressions such as `verified(api)`. Inspect `valid`, diagnostics, and counts before treating an
  empty frontier as completion. `criticalPath` counts nodes, not elapsed time.
- Failures and blocked work are information. Fix the cause or obtain the needed
  ruling; never delete dependencies, weaken an oracle, or invent evidence just
  to advance the graph. Oracle output is on stderr; recorded results use the
  selected format.
- After implementation changes, rerun the relevant acceptance checks. Definition
  fingerprints do not hash source-file contents, and completed consumers do not
  automatically reopen when upstream evidence changes. Inspect that impact.
- A stale node can be refreshed with named verification once its dependencies
  are satisfied. Reserve `(module.mechanical("COMMAND")) { once = true }` for
  deliberately historical observations that should never be rerun.
- Keep the sibling `.ledger.jsonl`, or pass the same explicit `--ledger FILE`
  to every command. Use CLI writes; a competing writer's lock refusal means
  reread and retry after it finishes. Do not replace a malformed ledger with
  empty state. Record useful handoff context with `note --text TEXT --by AUTHOR`.
- Existing TypeScript ledger fingerprints can be read through Bun compatibility.
  New Go evidence uses SHA-256 that the old reader does not recognize; copy the
  ledger when comparing runtimes. Review a Go plan with `recall` or the JSON from
  `export`; the legacy TypeScript page needs its original definition and ledger.
  There is no portable web server, watcher, or automatic source converter.

Keep human review compact: destination, established evidence, named decisions,
next available work, and blockers with their release condition. Link details
instead of pasting the whole ledger each turn. Use meaningful outcome names in
prose and stable IDs in commands. For large graphs, use `tick` for routine work
and `recall` for recovery; read relevant artifacts in detail on demand.

Finish when the authorized outcome has its required evidence, including any
in-scope work previously too vague to specify. An empty frontier or an entirely
green partial graph is insufficient. Return the plan and ledger paths, observed
result, and unresolved blockers or decisions. For discovery-only work, hand off
the resolved route and its remaining limits; do not silently begin delivery.
[/AGENT]
