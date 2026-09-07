---
name: workbench-plan
description: Author and operate granular, interdependent task graphs with Pkl definitions and evidence ledgers. Use when creating or resuming a .plan.pkl plan, selecting ready work, diagnosing blocked or stale evidence, or recording verification and decisions through workbench plan.
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
3. For new work, define the goal and acceptance evidence before decomposing it.
   Give each node a stable ID, owner, observable outcome, and bounded footprint.
   Split nodes when they have distinct evidence, owners, or independent work;
   avoid both opaque umbrella tasks and a node for every shell command.

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

1. Run `workbench plan check FILE.plan.pkl` after definition edits. Resolve graph
   diagnostics, then `workbench plan tick FILE.plan.pkl` to select ready work.
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
6. Re-tick after evidence, rulings, grants, or definition edits. When delegating
   authorized work, pass the exact plan, ledger, node ID, footprint, and oracle;
   the CLI does not dispatch agents or synchronize across machines.

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

Finish when the authorized outcome has its required evidence. Return the plan
and ledger paths, observed result, and unresolved blockers or decisions. Use
`recall` for a complete handoff. When a needed judgment belongs to someone else,
surface the specific decision while continuing any independent authorized work.
[/AGENT]
