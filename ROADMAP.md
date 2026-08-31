# Agent-native Workbench roadmap

This roadmap implements the target described by
[docs/agent-system.md](docs/agent-system.md) and
[docs/agent-protocol.md](docs/agent-protocol.md).

Status markers:

- **Released**: available in the latest released binary.
- **Next**: the next coherent implementation slice.
- **Planned**: designed but dependent on earlier slices.
- **Exploratory**: useful direction whose exact contract is not yet fixed.

The roadmap is dependency-ordered. A later slice must consume the normalized
facts and protocol established earlier; it must not create a parallel model.

## Baseline — reconciliation spine

Status: **Released** through binary `0.6.1` and resource contracts `0.6.0`.

The baseline already provides the safety-critical substrate:

- typed, capability-constrained Subject and resource evaluation;
- recursive repository closure and closed identity/placement shapes;
- preflighted non-destructive Git reconciliation;
- complete owned workspace and skill projections with convergence checks;
- source-derived TypeScript adjacency and exact external dependency authority;
- generated `AGENTS.md` orientation;
- exact cross-repository commit plans and recoverable push saga;
- exact revision snapshots and guarded orphan prune;
- skill catalog validation and composition;
- declared multi-output buildable lifecycle and materialization;
- self-contained, pinned released runtimes.

The new work exposes and composes facts that largely exist internally. It should
refactor before it duplicates.

## Slice 1 — one structured outcome protocol

Status: **Next**.

Goal: make every existing command unambiguous and cheaply consumable by an agent
without changing its domain behavior.

Deliverables:

1. Introduce `workbench.control/v1` result, diagnostic, environment identity,
   and receipt Go types.
2. Add `--format human|json` to every non-streaming existing command.
3. Add `workbench capabilities --format json` for machine-readable binary,
   protocol, schema, command, and capability discovery.
4. Convert existing setup, check, skills, buildable, commit, snapshot, and prune
   results and refusals to stable outcome classes and diagnostic codes.
5. Preserve concise human output as a projection of the same result.
6. Write a final receipt for every mutating command and link existing change
   journals rather than replacing them.
7. Report binary `0.6.1`-style release identity separately from resource
   contract `0.6.0` identity.

Acceptance gates:

- Success, invalid input, safety refusal, code-health failure, stale state, and
  partial push each produce schema-valid JSON.
- JSON mode emits no extra prose on stdout.
- Human and JSON modes exercise identical application capabilities and effects.
- An agent can discover every released command and schema without parsing human
  help or provoking an invalid invocation.
- Diagnostics never contain credential-bearing Git URLs or environment values.
- A failure after an external effect is recoverable from journal plus receipt.
- Existing narrative, compatibility, release, and convergence tests remain
  green.

## Slice 2 — normalized observation and Environment Index

Status: **Planned**.

Goal: give all commands one read model and make repeated orientation local and
cheap.

Deliverables:

1. Extract setup's evaluation, discovery, source observation, catalog, workspace,
   buildable, ownership, and health facts into immutable normalized model types.
2. Publish `.workbench/index/v1` atomically after successful local observation or
   setup.
3. Add provenance, content digests, observation mode, and explicit
   current/stale/unknown state.
4. Add content-addressed reuse for unchanged Pkl, TypeScript-import, and skill
   catalog inputs.
5. Add `workbench status --refresh none|local|remote --format json`.
6. Keep private index storage behind the CLI compatibility boundary.

Acceptance gates:

- Setup and status consume the same normalized model; differential tests prove
  they cannot disagree about identity, placement, graph, ownership, or health.
- `refresh=none` invokes no Git, Pkl, Bun, filesystem traversal, or network work
  outside the published index.
- `refresh=local` makes no network request.
- Remote facts are `unknown` or explicitly stale when not refreshed.
- Interrupted publication leaves either the old complete index or the new
  complete index, never a mixed generation.
- Unchanged inputs avoid repeated parsers while changed bytes invalidate exactly
  the dependent facts.

## Slice 3 — describe, ownership, graph, and scoped context

Status: **Planned**.

Goal: let an agent answer a narrow question without loading the environment.

Deliverables:

1. Add `describe resource`, `describe package`, and `describe path`.
2. Add bounded forward/reverse graph queries with edge provenance.
3. Define the closed path-ownership classification.
4. Index skill metadata, selection, composition, owner, and projected byte cost.
5. Add `context --path --intent` returning the least relevant resource facts and
   valid skill closure.
6. Regenerate a compact root `AGENTS.md` that points to these queries and states
   only the non-negotiable rules and current digest.

Acceptance gates:

- Every path inside a participating checkout is classified or explicitly
  unknown; generated paths are never labeled Git-owned.
- Symlink, containment, case-normalization, and nested-checkout ambiguity refuse
  ownership-dependent answers.
- Every graph edge identifies its authority or observation evidence.
- Scoped context is deterministic and composition-complete.
- Removing a resource removes all of its current index and orientation facts
  while preserving orphan evidence.
- Default `AGENTS.md` size is bounded independently of total repository count;
  full topology remains queryable.

## Slice 4 — pure reconciliation plan and exact apply

Status: **Planned**.

Goal: expose setup's existing internal preflight/apply seam as an inspectable,
budgetable control surface.

Deliverables:

1. Refactor setup into explicit Observe, Derive, Plan, Apply, and Verify packages
   over the normalized model.
2. Add strict `workbench.plan/v1` with action IDs, DAG edges, preconditions,
   effects, capabilities, costs, atomicity, and recovery.
3. Add `workbench plan --refresh local|remote` with no canonical mutation.
4. Add `workbench apply <plan>` with complete precondition and capability
   revalidation.
5. Reimplement `workbench setup` as remote-aware plan + standard safe grant +
   apply + convergence verification.
6. Add optional strict budgets; insufficient budgets refuse without weakening
   proof.

Acceptance gates:

- Identical normalized inputs produce byte-identical plan artifacts.
- Planning performs no canonical checkout, generated-path, dependency, commit,
  push, or delete mutation.
- Every setup mutation corresponds to one planned action and exercised
  capability in the receipt.
- A changed Subject, ref, path identity, owned-file digest, or relevant Git state
  makes apply stale before the unsafe effect.
- Batch-wide preconditions are checked before the first mutation wherever the
  operation's external boundaries permit.
- `setup` remains byte-convergent and behavior-compatible with the released
  safety laws.

## Slice 5 — delta status and proportional verification

Status: **Planned**.

Goal: minimize repeated work during the edit loop without overstating evidence.

Deliverables:

1. Add normalized cross-repository dirty-state and changed-path observation.
2. Derive direct and reverse impact from repository, package, import, skill,
   buildable-input, and projection edges.
3. Add `change status` and delta queries since an observation or receipt digest.
4. Add `verify --scope changed|resource|package|workbench`.
5. Record proof scope and unknown dynamic edges in verification results.
6. Preserve `check` as setup plus complete generated typecheck and test.

Acceptance gates:

- Targeted verification includes every statically known reverse dependent.
- Unknown dynamic behavior widens or qualifies scope; it is never silently
  omitted.
- A scoped pass cannot produce whole-Workbench healthy status.
- Unchanged resources do not rerun resource-local checks without a declared
  reason.
- Full verification remains equivalent to the existing accepted `check` path.

## Slice 6 — agent-native delivery

Status: **Planned**.

Goal: connect environment understanding to exact multi-repository delivery with
less manual transcription.

Deliverables:

1. Add `change draft` to render a deterministic, reviewable
   `WorkbenchCommitPlan.pkl` skeleton from normalized dirty state.
2. Annotate every selected path/hunk with ownership and impact facts.
3. Link commit saga progress, remote evidence, and shared change ID through the
   common receipt.
4. Expose exact continuation actions for partial push and lost acknowledgement.
5. Link handoff summaries to Subject, Snapshot, plan, receipt, and commit IDs.

Acceptance gates:

- Drafting never stages, commits, or pushes.
- Generated and nonparticipating paths are excluded with diagnostics, not
  silently selected.
- The authored/evaluated commit plan remains the only commit and push grant.
- Retry cannot rebuild, rewrite, widen, or duplicate an accepted local commit.
- A second agent can determine exact local and remote completion from durable
  artifacts without conversation history.

## Slice 7 — bounded knowledge accretion and retention

Status: **Exploratory**.

Goal: make repeated work cheaper and recurring lessons promotable without
creating hidden policy.

Candidate deliverables:

1. Content-addressed observation and receipt retention with reachability-based
   garbage collection.
2. Recurrence summaries for diagnostic codes, affected graph nodes, and remedies.
3. Explicit suggestions to promote recurring knowledge into declarations,
   tests, skills, or `AGENTS.pkl` prose.
4. Tooling that shows the evidence behind a suggestion and drafts, but never
   silently applies, a Git-owned change.
5. Measured context and process-cost telemetry containing no source, secrets, or
   arbitrary command output.

Before promotion from exploratory status, this slice needs a threat model for
privacy, poisoning, stale advice, and accidental policy creation. Deleting the
entire local knowledge cache must never change desired state or correctness.

## Cross-cutting implementation rules

Every slice must obey these constraints:

- Refactor one normalized truth; do not add a second parser or graph.
- Keep resource-contract evolution independent from control-protocol evolution.
- Preserve exact historical contract behavior and public compatibility fixtures.
- Make all new artifacts strict, deterministic, versioned, and secret-free.
- Re-observe at mutation boundaries and after effects.
- Test refusal paths for zero unintended mutation.
- Treat partial external effects as normal recoverable states.
- Prefer bounded local queries and immutable cache reuse, but never label stale
  evidence current.
- Keep commit, push, prune, build execution, and publication outside setup's
  authority.
- Update the README's released/target status table when a slice ships.

## Explicitly deferred

These are not hidden roadmap items:

- a daemon required for correctness;
- an arbitrary repository task or hook framework;
- automatic editing of durable agent memory;
- automatic commit, push, prune, release, or human communication;
- package-version solving or multiple revisions of one identity;
- repository-history migration;
- BasinDB or `phosphorco/community-packages` adoption.

They require separate designs and authority models if they are ever pursued.
