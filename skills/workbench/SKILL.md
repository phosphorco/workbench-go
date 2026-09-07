---
name: workbench
description: Prepare, verify, and maintain multi-repository Workbench environments from their Pkl Subject and resource declarations. Use for checkout setup, generated workspace drift, skill projection, buildable tooling, snapshots, or exact source delivery involving workbench-subject.pkl and workbench.pkl.
metadata:
  domain: general
---

# Operate a Workbench environment

[for=AGENT]
Treat the Subject and resource declarations as authoritative inputs. Generated
workspace files are disposable projections; repair their inputs and regenerate.

## Establish the context

1. Run `workbench version` and `workbench --help` to identify the installed tool.
2. Work from the context directory containing `workbench-subject.pkl`. Read it,
   the local agent instructions, and the affected resources' `workbench.pkl`.
3. Recover the selected entrypoints, intended work line, canonical checkout
   locations, and source/generated ownership before changing anything.
4. Use the operation matching the user's authorized outcome. Environment
   preparation can clone, fetch, change branches, generate files, and install
   dependencies. `check` includes that preparation.

## Choose the operation

| Outcome | Command | Effect |
| --- | --- | --- |
| Assemble or refresh the environment | `workbench setup` | Reconcile the Subject's repository closure and generated outputs. |
| Prepare and verify code health | `workbench check` | Run setup, then the generated typecheck and test scripts. |
| Validate the local skill catalog | `workbench skills check` | Inspect skill contracts without running setup. |
| Invoke a declared tool | `workbench run NAME -- ARGUMENTS` | Resolve the buildable and execute it with the explicit arguments. |
| Inspect a declared buildable | `workbench buildable check --name NAME` | Report candidate validity. |
| Capture exact resource revisions | `workbench snapshot record SNAPSHOT.pkl` | Write a snapshot; choose its destination explicitly. |
| Reconstruct an exact revision set | `workbench snapshot reproduce SNAPSHOT.pkl` | Acquire the snapshot in a fresh context; conflicts refuse. |
| Deliver an exact source change | `workbench commit commit-plan.pkl` | Run the commit/push workflow for the explicitly designated change. |
| Remove named managed orphans | `workbench prune IDENTITY` | Perform guarded removal; use only for an authorized cleanup. |

## Repair through the source

- Change `workbench-subject.pkl` to select entrypoints and work-line policy.
  Change a resource's `workbench.pkl` to declare its includes, exceptional
  package policy, skill selection, or buildables.
- Read the local declaration's amended Pkl contract before introducing fields.
  Use [contract references](references/contracts.md) when locating that schema.
  Keep its contract version explicit; do not guess fields from another version.
- Follow the command's concrete diagnostic. Preserve dirty checkouts, foreign
  files, and receipts when a command refuses. Resolve the reported ownership or
  branch conflict before retrying; do not force-reset or delete evidence to
  make a refusal disappear.
- Edit source skill catalogs, then let setup project selected skills. Avoid
  hand-editing Workbench-owned skill projections, package manifests, tsconfigs,
  workspace links, and generated agent instructions.
- Keep source changes scoped to their owning repositories. A successful check
  does not authorize a commit, push, publication, or prune. When delivery is
  already authorized, inspect the exact commit plan and proceed within it.

## Finish or hand off

Run the relevant operation after changing its authoritative inputs. Report the
actual result, affected repositories, and remaining diagnostics. Distinguish a
successful setup from passing code health. On a refusal, name the unresolved
input or ownership conflict and the next safe action. Preserve enough paths and
command evidence for another agent to resume without rediscovering the context.
[/AGENT]
