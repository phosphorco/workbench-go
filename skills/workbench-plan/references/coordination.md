[for=AGENT]

# Coordinate work

For ready-made definitions, use [actor imports](authoring.md#import-actor-kinds).
Copy only the kinds needed by the harness; configure inference for the current
caller. Actor recognition supplies attribution, not authenticated authority.

## Assign and report

```sh
workbench plan update feature.plan.pkl --node api --assignee @me --status in-progress
workbench plan update feature.plan.pkl --node api --assignee user:alex@example.com --reassign
workbench plan update feature.plan.pkl --node api --status paused --text 'Awaiting acceptance.'
workbench plan update feature.plan.pkl --node api --unassign
workbench plan note feature.plan.pkl --node api --text 'Learned: batch limit is 100.'
```

Assignment and status are independent. Replacing a different assignee requires
`--reassign`; `--unassign` releases responsibility. Set `assignable = false` to
refuse new assignments. `owner` still identifies judgment or decision authority.
For an update based on an earlier read, add `--expect-revision N` from
`work.revision`. It counts `NodeUpdated` events for that node, starting at zero;
a mismatch refuses the whole update. Omit it for an intentional setter against
current state. The local ledger lock protects check-and-append, not worker
liveness; the harness dispatches workers.

Statuses are `unstarted`, `in-progress`, `paused`, and `stalled`. `paused` covers
deliberate stopping, including awaiting acceptance, and requires `--text`.
Completion remains evidence-derived through `verify`, `adjudicate`, or
`rule`; reported status neither satisfies dependencies nor retracts proof.

## Stall and resume

```sh
workbench plan update feature.plan.pkl --node api --status stalled \
  --reason 'Credentials unavailable.' --resume-when 'Sandbox access provisioned.' \
  --waiting-on-actor alex@example.com
workbench plan update feature.plan.pkl --node api --status in-progress
```

Optionally add `--waiting-on-need 'verified(credentials)'`, referencing a real
node. Other forms are `produced(ID)`, `ruled(ID)`, and `granted(ID)`. These annotate
waiting; use `needs` in Pkl for actual graph prerequisites. A satisfied referenced
need prompts explicit resume; it never reports that work has resumed. If a plan
edit breaks the reference, `work.waitingOnNeedError` explains it and satisfaction
is omitted. This warning does not change graph validity or create an edge.

One current stall persists through notes, reassignment, and acceptance completion.
Changing to another work status clears it; history retains the interval.
Re-reporting `stalled` replaces its details, preserving the interval's start.

## Read outputs

- `update` confirms one timestamped ledger event with its actor and changes.
  New `NodeUpdated` timestamps must be valid RFC3339Nano; older tags stay permissive.
- `tick` shows current assignments, work status, and active stall details,
  including work on acceptance-complete nodes.
- `recall` adds completed nodes and chronological events.
- `--format json` exposes the same state for tools; `export` defaults to JSON.

Inspect a node's `work` object or the report's top-level `work` array. Reported
work remains separate from readiness and acceptance counts.

## Override or customize identity

Explicit `--by ACTOR` overrides inference. Elsewhere, `@me` means that resolved
caller. Use `KIND:canonical-id` for a reliable canonical round trip: it bypasses
aliases after validating the ID. Qualified noncanonical aliases still work;
bare-input ambiguity rules are unchanged. Without actor kinds, `update` and `note` require explicit `--by`; legacy identities stay untyped.
Multiple inferred identities or nonempty invalid inference variables are errors.
Historical identities never change when recognition rules change. With actor
policies configured, acceptance fingerprints include the resolved judgment or
Selector owner, so changing an owner's meaning can stale its evidence.
Digest-free historical evidence retains its presence-only behavior.

Amend an imported kind to change its inference variable:

```pkl
["thread"] = (bb.thread) { infer { env = "OUR_THREAD_ID" } }
```

To define a new kind, export a typed value from a local Pkl module:

```pkl
import "workbench:plan" as Plan

thread = new Plan.ActorKind {
  pattern = #"thr_[A-Za-z0-9_]+"#
  infer { env = "BB_THREAD_ID" }
  aliases {
    new {
      pattern = #"@thread:(thr_[A-Za-z0-9_]+)"#
      replacement = "$1"
    }
  }
}
```

Patterns use Go's RE2 syntax and match the entire input. Replacement references
have Unicode-aware name boundaries; unknown captures fail. `${1}é` is capture 1
plus `é`, while `$1é` names an unknown capture. Aliases normalize once;
the resulting ID must match the canonical pattern. Identical typed candidates
deduplicate; conflicting identities fail, including aliases within one kind.
The mapping key names the kind. Imports remain inside the module-root boundary;
the CLI reads configured environment variables after Pkl evaluation.
[/AGENT]
