# Agent control protocol

Status: proposed protocol for the target architecture in
[agent-system.md](agent-system.md). It is deliberately concrete enough to drive
implementation and acceptance tests, but none of the commands in this document
should be treated as released until the roadmap marks its slice complete.

## Public command families

The target CLI has four conceptual families. Existing commands retain their
names and human output while gaining the common structured envelope.

### Observe and explain

```text
workbench status [--refresh none|local|remote]
workbench capabilities
workbench describe resource <identity>
workbench describe package <name>
workbench describe path <path>
workbench graph [--from <node>] [--direction forward|reverse|both]
workbench context --path <path> [--intent inspect|edit|verify|deliver]
workbench explain <diagnostic-or-action-id>
```

These commands are read-only with respect to canonical checkouts and generated
projections. Their envelopes state whether they used local process, scratch, or
network authority.

`capabilities` is the self-description surface. It reports the installed binary
and protocol versions, supported command and artifact schemas, operation names,
possible capability classes, and—when an Environment Index is available—the
declared buildables relevant to this Workbench. An agent never needs to scrape a
usage error to discover the control surface.

### Plan and reconcile

```text
workbench plan [--refresh local|remote] [--output <file>]
workbench apply <plan-file>
workbench setup
workbench verify [--scope workbench|changed|resource:<id>|package:<name>]
workbench check
```

`plan` derives an expiring plan. `apply` revalidates and applies exactly that
plan. `setup` performs remote-aware plan, standard safe application, and
convergence verification in one invocation. `check` retains its present meaning:
setup followed by full generated typecheck and test scripts.

### Inspect and deliver changes

```text
workbench change status
workbench change draft [--output <file>]
workbench commit [plan]
workbench snapshot record [output]
workbench snapshot reproduce <file>
workbench prune <identity>...
```

`change status` normalizes dirty state across participating repositories and
reports ownership and impact. `change draft` proposes a commit-plan skeleton but
does not authorize commit or push. Existing exact-plan and saga guarantees remain
unchanged.

### Use declared capabilities

```text
workbench skills check
workbench run <buildable> -- <arguments...>
workbench buildable check|build|seal|verify|check-fresh|promote|materialize ...
```

These existing operations become discoverable through status, resource, and
context responses. Repository-owned executable behavior remains confined to the
closed buildable lifecycle; the control protocol does not add an arbitrary task
runner.

## Common options

Every non-streaming command supports:

```text
--format human|json
--output <path>             # only where an artifact is meaningful
--budget <budget-file>      # only where work may be expensive
```

Human is the interactive default. Agents should request JSON. A command that
executes a child process may additionally support an event stream in a later
protocol version; it must still end with one canonical result envelope and
receipt.

No command changes semantic behavior merely because JSON was requested.

## Result envelope

All JSON results use one top-level shape:

```json
{
  "protocol": "workbench.control/v1",
  "command": "status",
  "outcome": "ok",
  "operationId": "op_...",
  "environment": {
    "root": ".",
    "subjectDigest": "sha256:...",
    "observationDigest": "sha256:...",
    "desiredDigest": "sha256:...",
    "freshness": {
      "requested": "local",
      "achieved": "local",
      "remote": "unknown"
    }
  },
  "summary": {},
  "diagnostics": [],
  "links": [],
  "receipt": null
}
```

Allowed `outcome` classes in v1 are:

| Outcome | Meaning |
| --- | --- |
| `ok` | The requested proof or transition completed |
| `drift` | Observation succeeded and desired state differs |
| `unhealthy` | State is converged enough to inspect, but a requested health proof failed |
| `refused` | A law or required authority prevented the operation before its unsafe effect |
| `stale` | A supplied plan or cached precondition no longer matches live state |
| `partial` | A non-atomic operation completed some durable effects and is recoverable |
| `invalid` | User-authored input or invocation is invalid |
| `error` | Workbench could not complete the observation needed to classify the domain result |

An envelope is emitted whenever stdout remains available, including nonzero
domain outcomes. Secret-bearing values and unrestricted child-process output are
never copied into it.

### Stable identifiers

- `operationId` identifies this invocation and receipt.
- `diagnostic.id` identifies one occurrence under one observation digest.
- `diagnostic.code` is stable across occurrences.
- `action.id` is derived from normalized action meaning inside one plan.
- `plan.digest` binds every precondition and action.
- Graph nodes have typed keys such as `resource:<identity>` and
  `package:<name>`.

Identifiers are opaque to callers unless a format is explicitly documented.

## Status response

The compact status summary contains:

- binary, control-protocol, and resource-contract versions;
- work line and Subject digest;
- repository, package, skill, and buildable counts;
- overall convergence and freshness;
- dirty, wrong-branch, missing, orphaned, and unknown counts;
- active partial operation and stale-plan counts;
- highest-severity diagnostics;
- the cheapest useful next queries or actions.

The JSON response may link to index shards rather than inline every resource.
`describe` returns one bounded entity view with its immediate and requested graph
closure.

## Path description

`describe path` first normalizes the path beneath the Workbench root and refuses
symlink or containment ambiguity. Its result includes:

```json
{
  "path": "pkg/@example/app/src/index.ts",
  "classification": "gitOwnedSource",
  "resource": "@example",
  "package": "@example/app",
  "repositoryRoot": "pkg/@example",
  "generated": false,
  "applicableSkills": ["engineering-basics"],
  "checks": ["typecheck", "test"],
  "impactRoots": ["package:@example/app"]
}
```

Classification is a closed enum such as `gitOwnedSource`, `gitOwnedPolicy`,
`workbenchOwnedProjection`, `contextOwned`, `ignoredExternalState`, `orphan`, or
`unknown`. Unknown refuses an ownership-dependent mutation.

## Plan artifact

A plan is strict JSON with an immutable header and ordered action DAG:

```json
{
  "schema": "workbench.plan/v1",
  "digest": "sha256:...",
  "createdBy": { "release": "...", "revision": "..." },
  "environment": {
    "subjectDigest": "sha256:...",
    "observationDigest": "sha256:...",
    "desiredDigest": "sha256:..."
  },
  "refresh": "remote",
  "expires": { "remoteEvidenceAt": "..." },
  "totals": {
    "actions": 3,
    "networkRequests": 1,
    "processes": 2,
    "writeBytesUpperBound": 4096
  },
  "actions": [],
  "diagnostics": []
}
```

Each action includes:

```json
{
  "id": "action:...",
  "kind": "projection.writeOwned",
  "targets": ["package.json"],
  "requires": [],
  "capabilities": ["projection.writeOwned"],
  "preconditions": [],
  "effects": [],
  "atomicity": "singleFileReplace",
  "recovery": "replan",
  "cost": {}
}
```

Plans use repository-relative and Workbench-relative paths. They never embed a
credential, bearer URL, arbitrary environment variable, or executable body.
A local-only plan may contain unknown remote preconditions and therefore be
useful for cost and drift inspection without being apply-ready. Apply accepts
only a complete plan whose required evidence freshness is still satisfied.

## Budget artifact

A caller can constrain expensive work without changing correctness rules:

```json
{
  "schema": "workbench.budget/v1",
  "network": "deny",
  "maxProcesses": 8,
  "maxWriteBytes": 1048576,
  "allowCapabilities": [
    "observe.local",
    "projection.writeOwned"
  ]
}
```

If the minimum correct operation exceeds the budget, Workbench returns
`refused` with code `budget.insufficient` and the smallest sufficient budget. It
does not omit checks or silently downgrade freshness.

## Diagnostic object

```json
{
  "id": "diag:...",
  "code": "git.branch.dirtyMismatch",
  "severity": "error",
  "invariant": "subjectBranchCoherent",
  "scope": ["resource:@example"],
  "message": "The dirty checkout is not on the Subject branch.",
  "evidence": [
    { "field": "expectedBranch", "value": "cole/task" },
    { "field": "observedBranch", "value": "main" }
  ],
  "blockedActions": ["action:..."],
  "remedies": [
    {
      "kind": "manual",
      "operation": "preserveOrMoveChanges",
      "destructive": false
    }
  ],
  "refreshCouldChange": "local"
}
```

Messages may improve without a protocol version. Codes, field meanings, and
closed enums require compatibility discipline. Evidence paths and values are
encoded as data and never interpolated into suggested shell commands.

## Receipt artifact

```json
{
  "schema": "workbench.receipt/v1",
  "operationId": "op_...",
  "command": "setup",
  "startedAt": "...",
  "finishedAt": "...",
  "outcome": "ok",
  "planDigest": "sha256:...",
  "before": { "observationDigest": "sha256:..." },
  "after": { "observationDigest": "sha256:..." },
  "capabilities": {
    "granted": [],
    "exercised": []
  },
  "actions": [],
  "changed": [],
  "verification": [],
  "diagnostics": [],
  "continuation": null
}
```

The receipt is written through a temporary file and atomic rename. For a
multi-step operation, an append-only journal is updated before irreversible
effects are acknowledged; the final receipt summarizes it. A process failure
between effect and acknowledgement is recovered by re-observation, never by
blind repetition.

## Human projection

Human output should lead with the outcome and one next action. Detail is grouped
by resource or action only when useful. For example:

```text
Workbench is locally converged at subject sha256:…; remote state was not checked.
2 repositories · 3 packages · 1 dirty repository · 0 blocking diagnostics
Next: workbench describe resource @example --format json
```

Refusals name the invariant, evidence, and safest next step. They do not dump the
entire envelope or require parsing a Go error chain.

## Security and privacy

- Indexes, plans, and receipts are private ignored state by default.
- Paths are Workbench-relative unless an absolute path is essential and
  explicitly marked local-only.
- Remote URLs are normalized public identities, not credential-bearing Git
  transport strings.
- Environment values, tokens, command-line secrets, and unrestricted subprocess
  output are excluded.
- Pkl remains capability-constrained.
- Every path used for mutation is rechecked for containment, symlinks, identity,
  and ownership at the point of use.
- JSON fields originating in repositories are never treated as executable text.

## Compatibility and deprecation

Control protocol v1 is additive within its version. Removing a field, changing
an enum's meaning, or weakening a guarantee requires a new major protocol
version. Readers ignore unknown object fields but refuse unknown schema majors.

The binary reports all supported control and resource-contract versions in
status. Human output is not a parsing interface. Storage layout may change as
long as CLI results and explicitly public artifact schemas remain compatible.

Experimental fields live beneath an `experimental` object and cannot be
required for correctness. A roadmap item is not a protocol capability until a
released binary reports it.
