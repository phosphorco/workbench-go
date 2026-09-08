[for=AGENT]

# Plan a campaign that can change

Use this walkthrough when the effort exceeds one session, has unresolved choices,
or changes while work is underway. The example is a bulk-export feature: users
need downloadable records that an existing reader can consume. Adapt the
outcomes, owners, file sets, and acceptance commands to the real repository.

The [example definitions](../examples/bulk-export/discovery.plan.pkl) are valid
Pkl, not an implemented export application. Their `python3 -m unittest` commands
name acceptance instruments the example team must build in its repository.
`check` and `tick` work immediately; passing verification requires the real
artifacts, tests, and judgments. Never replace missing instruments with `true`.

## Start with the destination and the unknowns

Suppose the request is: deliver downloadable exports without breaking the
existing reader. A first investigation establishes this picture:

| Observation | Representation | Why |
| --- | --- | --- |
| CSV and JSONL might both work; compatibility is unmeasured. | Compatibility probe → format Selector. | The question is precise even though its answer is unknown. |
| The export size distribution is unknown, so the rollout shape is unclear. | A ledger note identifying when to revisit rollout. | Detailed rollout tasks would encode guesses. |
| A dashboard redesign is excluded from this delivery. | A scope note with the owner's decision. | Excluded work must not return as newly discovered work. |
| The reader consumes the record schema, not the writer's source code. | A separate contract Action feeding both implementations. | It exposes work that can overlap. |

Record unresolved context through the real write surface, for example:

```sh
workbench plan note export.plan.pkl --by planner --text 'Unspecified: rollout shape depends on measured export sizes. Revisit after the compatibility investigation and before release review. Dashboard redesign is outside the agreed destination.'
```

The note records context; it blocks nothing. Keep unresolved in-scope items
visible in the final acceptance rubric until they become concrete work, are
resolved with evidence, or the actual owner changes scope. When graduating one
into nodes, append a note pointing to those IDs; do not keep two active lists.

Use a probe when more evidence can decide an implementation approach. Use a
named owner's decision when the question changes what the user will receive.
A rough prototype can make a preference concrete, but cannot impersonate the
person who must judge it.

## Discover before committing the dependent work

[discovery.plan.pkl](../examples/bulk-export/discovery.plan.pkl) contains:

```text
Build compatibility probe → Observe compatibility evidence → Choose format
```

The probe builder owns its test and fixture files. The observation Guard runs
that instrument; the format Selector's `probe` structurally requires its pass.
The instrument must establish measured compatibility, including rejected cases,
not merely exit successfully after printing a report.

```sh
workbench plan check export.plan.pkl
# After the compatibility instrument actually exists:
workbench plan land export.plan.pkl --node probe-build
workbench plan verify export.plan.pkl --node probe-build --cwd REPO
workbench plan verify export.plan.pkl --node compatibility --cwd REPO
# Only after the named owner has actually selected CSV:
workbench plan rule export.plan.pkl --node format --choice csv --by product-owner
```

If the request was only to settle the route, stop with the supported decision
and remaining limitations. If delivery is authorized, expand the same graph
and retain its ledger. [delivery.plan.pkl](../examples/bulk-export/delivery.plan.pkl)
illustrates the CSV choice. A ruling satisfies `ruled(format)` regardless of
which option won: the CLI does not choose this file or generate a branch.
For JSONL, author the corresponding contract and instruments explicitly.

## Extract the contract and the evidence

The delivery graph adds the following structure to the discovery graph:

```text
Choose format → CSV contract ─┬→ Reader ────────────────────────┐
                             └→ Failing writer witness → Writer ─┴→ Round-trip proof
                                                                        │
                                                             Release review
```

Read the full node definitions in [campaign.pkl](../examples/bulk-export/campaign.pkl).
The decisive details are the edges:

- Reader and writer both consume the produced, verified contract. The reader
  does not depend on writer completion; tests against the contract can proceed.
- The writer requires a historical failing witness. The witness's command must
  recognize the intended assertion failure and reject import errors, crashes,
  and unrelated failures. A plain negated test command cannot distinguish these.
- The round-trip Guard needs both artifacts and their verification. It proves
  their composition, something two independent green suites cannot establish.
- The release review needs the round-trip proof and asks whether the entire
  destination is covered, including rollout. Its pass records a real review;
  it is not itself a deployment or authorization to publish.

Contract extraction is useful only when consumers can build against the
agreement. Keep the producer edge for tasks that require actual runtime
behavior, migrated data, or measured evidence.

Write a small acceptance map before dispatch. It is a review projection, not
another progress database:

| Acceptance condition | Instrument |
| --- | --- |
| Existing readers accept the format. | Compatibility probe and reader contract suite. |
| Writer quotes delimiters, newlines, and Unicode correctly. | Specific failing witness followed by the writer suite. |
| A download can actually be read back. | Round-trip integration suite over both implementations. |
| Release covers the promised scope and a viable rollout. | Named review against measured constraints and remaining uncertainty. |

For an uncovered condition, add the missing instrument before treating the
graph as a complete delivery plan. For an uncertain condition, expose the
question and its owner instead of inventing an acceptance threshold.

## Choose useful work from the frontier

Suppose the contract is verified and the failing witness is recorded. Writer
and reader are both ready with disjoint footprints. Start both if the harness
and authorization permit; verify each as it returns. Round-trip work waits
because it consumes both. Do not create a separate wait-for-all barrier before
starting another consumer that needs only the contract.

Give priority to a short investigation that can invalidate several lanes over
a cheap isolated task that merely produces visible progress. The CLI's
`criticalPath` counts nodes; it does not estimate task durations or rank risk.
Use actual cost and uncertainty estimates when choosing among ready nodes.

For a hundred equivalent caller edits, a single transformation plus inspection
may replace a hundred Actions. Keep separate nodes when evidence, ownership,
or independently reviewable delivery changes. Split large plans into local Pkl
fragments by coherent obligations, retaining one root graph for cross-fragment
checks; fragments do not create private ledgers or independent state.

## Delegate a bounded node

An example handoff after the graph enables the writer:

<assignment>
Implement CSV export quoting against `contracts/export.schema.json`.
Plan: `plans/export.plan.pkl`; ledger: `plans/export.ledger.jsonl`; node: `writer`.
Write footprint: `src/export/**`. Acceptance: `python3 -m unittest tests.test_writer`.
Read the contract, the recorded CSV decision, and the observed failing witness.
Return changed artifact paths, verification output, and any blocked assumption.
If the contract or tests need revision, surface the specific need before writing
outside this footprint.
</assignment>

The host's assignment mechanism records who is working. A `grant` is a write
footprint declaration, not a claim or a shell sandbox. Confirm actual activity;
do not report a queued brief as work underway. When the worker returns, inspect
the diff and independently run the acceptance command against the returned
revision. Passes from an earlier revision do not prove the current artifact.

If a worker hits a contradiction, repair the contract or briefing from the
evidence and redistribute it to affected consumers. Repeating the same brief
does not resolve the contradiction. Continue work whose inputs remain valid.

## Revise after a discovery

During implementation, measured exports exceed available memory. The owner
confirms that streaming within a 32 MiB working-memory budget is required.
This is an example decision, not a default budget for other projects.

| Before | After | Work that remains usable |
| --- | --- | --- |
| Writer proves CSV correctness. | Same `writer` ID; oracle also proves bounded working memory. | Agreed CSV schema. |
| Round-trip proof uses small input. | Same `roundtrip` ID; oracle also checks a large streamed export. | Existing reader tests and supported data types. |
| Release review lacks a measured size bound. | Same `release-review` ID; rubric requires streaming evidence and rollout fit. | Prior observations remain in the ledger. |

[revised.plan.pkl](../examples/bulk-export/revised.plan.pkl) expresses those
changes by amending the affected nodes from the shared fragment. It is a
replacement definition snapshot; in real work update `export.plan.pkl` in place
and retain `export.ledger.jsonl`. When comparing snapshots, pass the same copied
ledger explicitly with `--ledger`; their default sibling filenames differ.

```sh
workbench plan note export.plan.pkl --by planner --text 'Measured export sizes exceed the buffered writer capacity. Owner confirmed the 32 MiB streaming budget; see evidence/export-size.md. Revise writer, roundtrip, and release-review acceptance; retain the CSV contract.'
# Edit the affected definition and build the new acceptance instruments.
workbench plan check export.plan.pkl
# After implementing the correction, run the revised instruments:
workbench plan verify export.plan.pkl --node writer --cwd REPO
workbench plan verify export.plan.pkl --node roundtrip --cwd REPO
# The release reviewer inspects the actual proof before adjudicating.
```

The changed oracle commands stale their earlier evidence. Changing only
`outcome`, a source file, a Selector's question, or a dependency is not a
reliable invalidation mechanism. Completed consumers may stay complete despite
changed upstream evidence. Inspect the affected subtree, rerun its checks, and
revise the final review rubric when acceptance changes. For newly discovered
obligations, add explicit nodes and evidence edges; preserve old history.

An unchanged reader need not be rewritten just because the writer changed.
Its composition with the new writer must still be tested. If the CSV agreement
itself changes, both implementations and their acceptance instruments are now
affected: revise that larger subtree rather than applying the smaller example.

## Resume and make review easy

Keep the definition, its local imports, ledger, and referenced evidence together
in the project's chosen durable workspace. Follow the project's Git policy;
ignored files require separate preservation. A shared checkout needs one
coherent writer/assignment arrangement; copying a ledger is not synchronization.

Recover with `recall`; use `tick` for subsequent frontier checks. After definition
edits, `check` returns the frontier too. For a large graph, keep the complete
result as a disposable snapshot and inspect a small projection:

```sh
workbench plan check export.plan.pkl --format json > plan-check.json &&
  jq '{valid, counts, diagnostics, ready: [.ready[] | {id, outcome}]}' plan-check.json
```

This example uses `jq`; any JSON reader can inspect the same saved result. A
nonzero `check` stops the success projection: inspect stderr and the saved
diagnostics before selecting work. Read blocked-node details only for the
dependency or decision being considered. The snapshot is not another ledger.

Present a short view whose claims point to the artifacts and ledger:

<review>
Destination: compatible downloadable exports.
Established: CSV contract and reader verification.
Changed: measured sizes require streaming; writer and integration proof need refresh.
Available: bounded-memory writer correction.
Decision needed: none; the owner confirmed the memory budget.
Remaining: streaming integration evidence and release review, including rollout.
</review>

This is an illustrative view, not saved execution state. Regenerate it as the
definition and ledger change. A newcomer should be able to see what changed,
what can proceed, and what evidence will finish the work without reading every
historical event. An all-green graph with unresolved in-scope obligations is a
partial plan, not a completed destination.

## Further reading

Matt Pocock's [Wayfinder](https://github.com/mattpocock/skills/blob/main/skills/engineering/wayfinder/SKILL.md)
offers a useful discovery pattern: keep the overview small, distinguish precise
questions from still-unspecified work, and expand the map as decisions make
more work concrete. Here those ideas use local Pkl intent and ledger events.
Execution continues when authorized; no issue tracker or additional skill is
required. Human judgments remain human judgments regardless of storage format.
[/AGENT]
