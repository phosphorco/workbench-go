# Workbench context implementation campaign

Owner: Cole. Driver: BB thread `thr_kma65k6vp8`.
Repository: `/home/ubuntu/phosphor/workbench-go`; shared checkout, disjoint write grants.
Contract: `docs/adr/2026-09-wokbench-context.md` (Accepted).
Plan: `.context/workbench-context/implementation.plan.pkl`; sibling ledger is operated only by the driver using `/tmp/workbench-context-plan plan`.

Implement the entire accepted feature, not a mock RPC demo. User requires BB child workers using Codex `gpt-5.6-luna`, xhigh reasoning, fast tier. Workers do not spawn other workers, commit, push, change global harness configuration, or edit outside their grants. Report results and blockers to the driver with `bb thread tell thr_kma65k6vp8 '...'`; do not wait on the driver silently.

## Meaning and authority

Project intent + actual agent observations determine relevant guidance. Host evidence determines audience/context continuity and delivery opportunities. The runtime alone owns contribution admission, queue and receipt decisions. Contributors declare content and reasons. Profile providers declare scoped relevance facts, not receiving-context identity. Explanations record decisions and never become suppression truth.

- No durable messaging, inboxes, agent scheduling, BB-specific domain model, or transcript archive.
- One per-OS-user Go runtime; Go hooks do bounded activation before contacting it. Inactive directories: silent successful no-op, no daemon/providers/history/project writes.
- Explicit project or home opt-in; home exclusions/constraints dominate; nearest project refines defaults. Project opts into its configured executables. Revalidate withdrawal and pending selection before admission.
- Each audience and epoch isolates queue/receipts. Full source+content must have confirmed exact handoff before reminder. Confirmation cannot acknowledge replacement slots or another epoch. Offer then stdout then confirmation. Unknown continuity permits duplication.
- Pending context crosses hook processes, not necessarily daemon restarts. Profile decay, reset, eviction, and cache rotation have different meanings. Bounded queue age/count/bytes; no silent normal-operation loss.
- JSON-RPC contributor capabilities: initialize, optional audience.profile, context.contribute, shutdown. Language neutral; subprocess is a transport, not a sandbox. Healthy providers progress despite failed peers.
- Explanation cache: numeric-first events, max 10,000 UTF-8 bytes total per contribution sample (head/distributed middles/tail, offsets/omissions), configurable 5,000,000,000-byte rotating disk cap including auxiliaries. No transcript, bounded queries/memory, disposable generation, historical decision inputs, honest gaps.
- CLI offers bounded human/JSON status/history/inspect/explain/cache operations; one global setup for Claude and Codex. Do not make the user edit global hooks for each project.

## Composition and Go review rubric

Read the actual repo and existing tests before work. Applicable source skills live at `/home/ubuntu/.bb/worktrees/env_r5k32s44gx/control/.agents/skills`: writing-go (and its ownership/aliasing, concurrency/lifetimes, interfaces, errors, filesystem and testing references), writing-composable-code, writing-repository-code, verifying-repository-changes. Apply their semantics, not repository-specific TypeScript commands to Go.

Treat Go ownership with Rust-like explicitness without emulating Rust syntax: distinguish copied values from mutable identities; no escaping borrowed slices/maps without clone or documented transfer; no retained large buffers through tiny subslices; immutable snapshots across concurrency; concrete data/functions before interfaces; enums plus payloads where states differ; avoid map[string]any except opaque JSON boundaries. Dense numeric trace data; avoid pointer-rich per-event graphs. Every goroutine/process has one owner, finite cancellation, join, error destination; cancellation alone is not cleanup. Keep provider I/O out of shared locks. Do not launch per-working-directory permanent processes. Decisions must be deterministic from explicit inputs and time facts.

Return: changed paths, exact commands/results, known gaps, ownership/lifetime decisions, and the smallest counterexample likely to break your design. Your report is a claim; the driver independently inspects and tests it. Never weaken tests or claim mocks establish real model admission.

## Candidate package boundaries

`internal/contextapi`: small shared data contracts, no I/O, no catch-all service interfaces.
`internal/contextconfig`: pure/FS activation and scoped config resolution.
`internal/contextengine`: queue/receipt/profile decision state.
`internal/contexttrace`: disposable sampled rotating explanation storage and bounded inspection.
`internal/contextprovider`: contributor subprocess RPC and builtin file guidance.
`internal/contexthook`: trusted Claude/Codex normalization and envelope projection.
`internal/contextdaemon`: per-user runtime/client transport/lifecycle and composition.
`cmd/workbench/context*.go`: command/setup/inspection integration.

These are provisional; contract worker should simplify boundaries if actual consumers justify it, and tell the driver before consumers fan out. No duplicate hidden config or delivery interpretation in CLI/adapters.

## Evidence floor

Focus tests on input/output guarantees; include replay/races, changed source/profile, expired/withdrawn items, cache rotation and multibyte sampling, provider timeouts/process cleanup, negative activation freshness, and real CLI subprocesses. Final driver runs full Go tests/vet/race and representative end-to-end tests, benchmarks inactive/cold/warm/concurrent hooks, and exercises real Claude/Codex admission where supported. Unavailable host facts are explicit limitations to resolve, not a passing stub.
