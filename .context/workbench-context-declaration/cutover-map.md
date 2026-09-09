# Cutover boundary inventory

Read-only root inventory at d709734. Production ownership is assigned in the plan
and per-thread grants; this map is a derivation pointer, not a second ledger.

- contextapi/config.go owns authored JSON structs ProjectFile, HomeScopeFile,
  HomeFile and nil-provider/second profileProviders meanings. Replace with
  canonical Pkl declaration values. Keep runtime ProviderConfig, ScopeIdentity,
  EffectiveConfiguration and delivery/RPC invariants as far as semantics permit.
- contextconfig/config.go owns one Load/Resolve and all existing defaults/bounds.
  Replace path discovery + JSON decoder + ancestor refinement. Consumers must
  all call that same replacement; no second decoder or independent profile rule.
- internal/evaluate/Evaluator already designates exact private Pkl path.
  EvaluatePlan is a capability-bound module-reader exemplar, not a bounded hook
  implementation: it explicitly uses context.WithoutCancel at handshake.
- internal/runtime/Toolchain resolves private Pkl and Bun beside installation.
  Pkl-only hooks must not acquire Bun just to evaluate declarations; handle exact
  Pkl capability at composition root. No PATH fallback. Tests need a real isolated
  distribution layout or explicit authorized evaluator dependency.
- Load callers: cmd/workbench/context_cli.go init/status; context_runtime.go hook
  and setup helpers; contextdaemon/runtime.go loadCurrentActivation, traceOptions,
  homeIdleTTL. Load cancellation and dependency injection must reach all of them.
- contexttrace.Open creates files and keeps process-local usage accounting.
  It has one writer contract. Before adding snapshots alongside it, define the
  shared cap and lock boundary; two independent cap values do not enforce total.
  Runtime currently resolves trace options only at startup: a reduced home cap
  must stop further old-budget writes until current usage is admitted/reclaimed.
- Context CLI setup embeds --home-config in hook commands. Default path changes
  to workbench-context.pkl; preserve unrelated settings, reject explicit .json.
  init preserves authored Pkl expressions/imports and reports effective result.
- Capability dispatch: profile-only selections must not receive contribute RPCs.
  Current runtime.collectContributions iterates all providers and Client.Contribute
  lacks the negotiated-capability guard already present on Profile. Filter both
  runtime operations by requested capabilities and enforce negotiated capabilities
  at the client boundary, with a profile-only actual provider witness.
- Named builtin identity: ContributeBuiltin and Revalidate currently hardcode
  ai-context. The configured contributor name must flow into ProviderConfig.ID,
  builtin source/slot/reason identity and freshness checks; do not add an alias
  registry or silently rename project-guidance in explanations.
- Existing acceptance/context_test.go and daemon/provider/engine/config tests
  contain JSON fixtures. Preserve their behavioral coverage through Pkl fixtures.
  RPC payload JSON is unrelated and must stay unchanged.

Highest-risk cross-lane seams: exact import bytes versus later hash reads;
canonical scope provenance versus effective digest; cancellation versus joined
subprocess completion; current home policy versus reduced cache allowance;
no-source activation before private toolchain acquisition. Root will rule these
from bounded executable witnesses, not from interface declarations alone.
