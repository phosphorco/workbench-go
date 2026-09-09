# Workbench context declarations

Design proposal for the declaration mechanism described in the
[context ADR](adr/2026-09-wokbench-context.md). This document specifies intended
behavior for a separate implementation effort; it does not describe a shipped
Pkl loader. The ADR remains the authority for the wider context product.

## One project source and one activation decision

`workbench-context.pkl` makes a directory's context behavior discoverable in
the project itself. It declares which contributors are selected, their settings,
and the directory coverage. Changing that source changes context behavior across
the globally configured Claude and Codex integrations.

Pkl replaces authored JSON activation. It is not a wrapper that writes an active
`.workbench/context.json`, an additional override, or a second resolver. Hooks,
status, and daemon admission use the same canonical declaration values and the
same pure activation function. The daemon revalidates current authority before
admission; a hook's earlier result is not an authority token.

There are two authority levels, not two activation mechanisms:

| Source | Authority |
| --- | --- |
| `<directory>/workbench-context.pkl` | Select contributors, settings and configured profile for its own directory coverage. |
| `$XDG_CONFIG_HOME/workbench/workbench-context.pkl` (fallback `$HOME/.config/workbench/workbench-context.pkl`) | Set user limits and exclusions, constrain contributors, and explicitly name directory declarations for projects the user cannot edit. |

Home directory declarations carry the same selection value as a project
declaration, with an explicit absolute root. Home policy alone does not activate
directories. The home file's location does not give it implicit subtree coverage.
Project declarations cannot author runtime, delivery-cache, or user-policy limits.

## Declaration shape

The proposed project contract is `WorkbenchContext.pkl`; the home contract is
`WorkbenchContextHome.pkl`. Both reuse the same contributor and selection types.
Workbench supplies these contracts with its existing bundled Pkl distribution;
normal hook execution never downloads a schema or resolves an ambient Pkl binary.
Contract URI/version spellings are finalized with the executable schema.

| Project field | Meaning and default |
| --- | --- |
| `enabled` | Explicit suspension switch; default `true`. |
| `scope` | `directory` or `subtree`; default `subtree`, printed explicitly by `init`. |
| `contributors` | Mapping from stable local names to typed contributor declarations; default empty. There is no implicit builtin. |
| `profile` | Optional configured role, guidance set and preferences; default empty. |

The smallest useful declaration explicitly selects the builtin. This is a
schema sketch; the implementation must supply and validate its amended contract:

```pkl
scope = "subtree"

contributors {
  ["project-guidance"] = new AiContext {}
}
```

An executable contributor additionally names an executable, an argument list,
requested capabilities, provider-owned JSON-shaped settings, and optional limits
that can only narrow the effective home bounds. Executable paths are absolute or
relative to the consuming declaration root, never ambient PATH lookup. Arguments
are passed directly, not interpreted as a shell command. Imported templates do
not change that path origin.

An executable's requested capabilities are `profile`, `contribute`, or both.
The provider still negotiates supported capabilities at initialization. Derive
the profile-provider selection from these declarations; do not retain a second
authored list of provider IDs that can disagree with them. The builtin supports
contribution only. Disabled contributors have no running process. Deterministic
name ordering makes effective configuration and explanations stable.

Contributor names identify configuration entries, not executable authority by
themselves. Home policy must be applied to the resolved contributor descriptor;
the policy contract must state whether it permits future executable changes.
Project opt-in retains the ADR's explicit delegation for project-configured
executables within applicable home constraints. No additional per-project
harness registration is introduced.

Profile facts determine relevance. They do not create audience identity or
epochs; those remain host/runtime responsibilities. No change to contributor
RPC framing, delivery identity, or receipt semantics is part of this effort.

## Directory selection and explicit composition

Resolution uses nearest-declaration ownership. The nearest project
file establishes the boundary and supplies the complete selection. Its presence
never silently merges contributors or configured profile fields from ancestors.
Reuse is explicit Pkl composition of local templates. The ADR records this
choice; the plan's `scope-semantics` ruling records its adoption. There is no
ancestor-refinement fallback in the target design.

| Situation | Result |
| --- | --- |
| No project file and no matching explicit home declaration | Inactive. |
| Valid nearest project, enabled, with permitted contributors and covering cwd | Enabled with exactly that selection after home constraints. |
| Nearest project has `enabled = false` | Inactive; do not fall back to an ancestor or home selection. |
| Nearest project selects no enabled contributors | Inactive with an explicit empty-selection reason. |
| Nearest project has `scope = "directory"`, and cwd is a descendant | Inactive outside its coverage; the boundary still prevents ancestor/home fallback. A nearer descendant file can opt in. |
| Nearest project cannot be read or evaluated | No admission; report invalid or unavailable, not an inferred absence or ancestor fallback. |
| No project boundary, but a matching home declaration exists | Most-specific home declaration supplies the complete selection. |
| Applicable home exclusion or disabled home scope | Inactive regardless of project selection. |
| Conflicting home declarations for the same canonical root | No admission; explain the conflict. |

Home exclusions and limits constrain every project, including nearer files.
Home policy may remove disallowed contributors; status names each removal. An
empty result cannot start the daemon or providers. Optional user profile defaults
may fill unconfigured fields through the same resolver, but must not select
additional contributors. Scope IDs remain derived from authority plus canonical
root; a configuration edit changes revision, not scope identity. Symlink aliases
share canonical roots; separate worktrees remain separate scopes.

## Evaluation and freshness without a second source of truth

The dependency shape is:

```text
cwd + bounded directory discovery + home source
  -> selected Pkl modules and explicit local imports
  -> evaluated, validated declaration values
  -> one canonical activation resolver + home constraints
  -> effective contributor/profile selection
  -> existing runtime admission and delivery
```

`internal/contextconfig` remains the owner of activation meaning. Evolving its
load seam to accept cancellation and explicit evaluator/cache dependencies must
replace its existing implementation, not leave `LoadJSON` and `LoadPkl` runtime
branches. `internal/evaluate` supplies constrained evaluation, using the existing
bundled-toolchain boundary. Shared data contracts must cease exposing JSON file
presence/nil semantics as domain values.

The first version permits the bundled schema, required Pkl standard modules,
and explicitly named local `.pkl` imports confined to the declaration root.
For the home declaration, the boundary is its configuration directory. No
environment, clock, network, resource reads, globs, or external readers are
available. A missing import is an evaluation error. This deliberately makes the
complete dependency set finite and observable. Cross-root template distribution
is a separate capability; it cannot appear through an unrestricted import escape.

An evaluated snapshot owns its canonical values and the dependency manifest:
each import's designation and importing origin, resolved canonical target,
digest of the exact bytes supplied to the evaluator, schema/evaluator identity
and bounded evaluation diagnostics. Capture these at the module reader boundary;
do not evaluate one read and hash a later read. Resolve each dependency once
within an evaluation and serve repeated reads from that captured value. Cache
keys also include declaration origin and authority. Declaration provenance
includes its import closure; effective revision additionally includes applicable
home policy. Do not confuse either revision with stable scope identity or a
builtin contribution's source revision.

Warm hooks do bounded discovery and validate exact dependency bytes before using
a snapshot. Metadata alone must not permit same-length/timestamp-preserving
edits to retain stale authority. Newly created nearer declarations must be found;
an old negative result cannot hide them. Evaluation publication requires a
consistent captured dependency set; if dependencies change during evaluation,
retry within the same deadline or return unavailable. No stale-while-revalidate
is permitted for activation or withdrawal. Publication and reuse must also
re-resolve import designations: changing a symlink target must invalidate the
snapshot even if the old target still exists unchanged. Include an ABA witness
where bytes supplied to evaluation differ from bytes in a later hashing pass;
validation must compare against the actual evaluated inputs.

Snapshots may be held in bounded memory and in a private disposable disk cache
so separate Go hook processes and an idle-restarted daemon can reuse evaluation.
They are not authored or project-local files. Missing, corrupt or incompatible
snapshots are cache misses, and deleting them changes neither selection nor
authority. Their disk bytes, temporary writes and metadata count against the
configured total context-cache cap; they cannot create a second unbounded cache.
Clearing the explanation history still changes no delivery truth; a snapshot
eviction only requires evaluation again.

All writers must participate in one shared capacity-accounting protocol before
snapshot storage is implemented. Define reservation, temporary publication,
atomic replacement, eviction and crash recovery across hooks and the daemon's
trace writer. Two concurrent evaluations cannot independently spend the same
free bytes, and capacity remains charged until owned files and temporary writes
are removed. Test simultaneous snapshot publication and trace rotation, tiny
or reduced caps, cancelled writers and abandoned reservations. A process-local
trace mutex alone cannot establish this guarantee. The contract may partition
or reserve the budget, but cannot give each cache the full cap independently.

Evaluation bootstraps under finite compiled limits for evaluator processes,
memory, input/output, imports and diagnostics. Before the home policy has been
successfully evaluated, no snapshot is published using an assumed user disk
allowance. Changed home limits must be validated before new reservations;
reduced limits prevent new admission until owned usage satisfies the new cap.

On a cache miss, the proposed route is bounded, automatically owned evaluation
before starting context contributors. No permanent evaluator per directory and
no required manual compile step. No discovered project or home source means no Pkl process,
daemon, provider, or cache creation. A present home Pkl source is a special cold
case: evaluating its policy may be necessary to establish that cwd is inactive.
That temporary evaluation must not start the context daemon or create context
history. The feasibility probe must make its latency, memory and cache-write
behavior explicit; it may not quietly weaken the ADR's inactive contract.

The adopted cold policy permits a bounded temporary evaluator and private derived
snapshot writes when a declaration exists but its snapshot is missing or invalid,
even if evaluation returns inactive. It permits no contributor or context-daemon
startup, project writes or explanation history for inactive work. No-source
inactivity remains silent and creates no process or cache. This settles permitted
behavior; it does not establish that the latency and resource targets are feasible.
The `cold-inactive-policy` ruling records the choice separately from probe evidence.

The probe must separately measure no-source inactivity and uncached home-only
inactivity, including eight simultaneous misses with a nearly full shared cache.
Cover executable/version discovery, startup/handshake, evaluation, cancellation,
output decoding and joined cleanup, not just Pkl evaluation time. Use blocked
version/handshake and oversized output/diagnostic fixtures with explicit process,
memory, input and output budgets. The existing evaluator's uncancelled handshake
and pkl-go version discovery are not ready-made bounded hook implementations.
A failed probe blocks
the affected implementation: do not add background watchers, stale fallback,
permanent evaluators or an extra activation registry as a local workaround.

## Migration removes the old activation path

The target release reads only Pkl for authored project and home activation.
`.workbench/context.json` and the old home `context.json` never act as runtime
fallbacks or overrides, even when both formats exist. JSON remains appropriate
for CLI output, contributor RPC and internal serialization; those are not
activation declarations.

Provide an explicit conversion operation, with preview and no overwrite of an
existing Pkl target. It reads old JSON only as conversion input and emits one
Pkl source; normal hooks never invoke the converter. Resolve the effective legacy
selection before converting: an omitted provider field may inherit an executable
from an ancestor and is not itself a request for the builtin. Emit an explicit
builtin entry only when legacy resolution selects it; explicit empty selections
stay empty. Materialize inherited selection or refuse with an explanation of
the unresolved dependency. Preserve executable origins when relocating relative
references. Compare coverage for affected descendants as well as the root,
including nearer directory-only, disabled and empty declarations. If the chosen
target semantics cannot preserve a region's behavior, refuse conversion of that
region and show the difference; do not silently drop guidance. Bound the scope
analysis, and treat incomplete enumeration as unproven preservation. Provider
paths, capabilities, exclusions and home bounds must not silently change. Old
files remain user data; removal is explicit, and their presence after conversion
is inert.

Status can identify a legacy file as migration information without using its
contents to activate anything. If both formats are present, Pkl is the sole
authority and status says the legacy file is ignored. Installed hooks may contain
an old explicit `--home-config` path: reconciliation must replace known generated
defaults while preserving unrelated harness settings; a custom old JSON path
must produce actionable migration guidance, never silently select another home.
This one-time reconciliation is a format upgrade, not per-project registration.

## User-facing behavior and acceptance

`context init` reports file creation separately from effective activation. It
must not claim "enabled" because it wrote a file or silently replace inherited
contributors with the builtin. Its behavior is:

| Initial state | Initialization outcome |
| --- | --- |
| No local file, no applicable inherited/home selection, no legacy file | Create the minimal explicit builtin declaration, then report the actual resolved result. |
| Effective selection comes only from an ancestor or home declaration | Report its source and preserve it; creating a replacement local selection requires an explicit scope/selection change with the displaced contributors shown. |
| Only a legacy JSON file exists | Preserve it and direct the user to explicit conversion; do not generate a builtin declaration over its intended behavior. |
| Existing Pkl is enabled, disabled, empty or invalid | Preserve the authored module and explain its resolved state; never flatten imports, rewrite expressions or implicitly enable it. |
| Home policy excludes the directory | Report the exclusion and do not create a misleading activation file. |

`status` uses the same loader as hooks and reports winning or blocking source,
coverage, contributors/capabilities, blocked entries, policy origin and evaluation
diagnostics without starting contributors. Under nearest ownership, a descendant
blocked by a directory-only declaration must see that declaration and the reason
ancestor fallback was not used.

History must preserve the declaration/dependency revision and contributor name
used for a historical decision. It must not reinterpret history using today's
Pkl source. A provider with profile capability remains identifiable in status
and profile explanations. Inspection can explain invalid declarations even
though ordinary inactive hooks create no history.

Implementation acceptance must demonstrate:

- One authoritative format and resolver, including simultaneous old/new files,
  explicit home overrides and every public activation consumer.
- Nearest boundaries, disabled/empty selections, excluded directories, exact
  directory coverage, aliases, worktrees, and absent home configuration.
- Imported-template edits/deletion, same-length edits, new nearer declarations,
  symlink retargeting, ABA reads, corrupt/removed snapshots, races during evaluation
  and withdrawal of queued guidance before further admission.
- Real CLI init/status/conversion preservation, actionable error output, and
  contributor/profile delivery through the assembled Pkl path.
- Measured no-source inactive, configured-home inactive, cold, warm and concurrent
  hook budgets; total evaluator/daemon process-tree resources and joined cleanup.
  Concurrent cache misses and trace publication must respect one shared cap,
  including reservations, crash recovery, temporary bytes and reduced limits.
  Preserve the established depth-12 baseline targets of inactive p95 <=30 ms,
  warm p95 <=100 ms, and eight concurrent cold hooks <=2 s on the stated test
  machine/workload, or explicitly resolve a measured conflict before adoption.
- Native Claude/Codex admission and an outcome-only discovery/diagnosis session
  using Pkl declarations; old JSON-only proof does not prove the replacement.

The separate executable plan is
[declaration.plan.pkl](../.context/workbench-context-declaration/declaration.plan.pkl).
Implementation begins only after its feasibility and contract gates are met;
this proposal itself adds no activation behavior.
