# Pkl context hard cut — shared execution context

Cole authorized greenfield implementation on 2026-09-09: hard cut, TDD, BB child
threads, all Codex Luna/xhigh/fast. Message results/blockers directly to root
thr_kma65k6vp8 with bb thread tell --mode auto; never bb wait. No child commits,
pushes, subthreads, global hooks/auth edits, or ungranted source changes.
Peer feedback uses --mode steer for an active peer. Do not auto-reopen idle
peers merely to relay a checkpoint: send it to root for the next grant instead.
Root owns the three-child concurrency budget and reopens idle implementation or
review turns explicitly. Message passing must not create ungranted relay turns.

Read docs/context-declarations.md, docs/adr/2026-09-wokbench-context.md and the
executable declaration.plan.pkl plus sibling ledger. The prior conversion design is removed: no converter, compatibility reader, registry or JSON fallback.
Existing .workbench/context.json and old home JSON are inert. JSON-RPC and CLI
JSON output remain unchanged. Existing JSON tests must be ported, not discarded.

Canonical graph: cwd + home/project Pkl + exact local import bytes -> evaluated
selection -> one pure activation resolver -> existing runtime. Snapshot is a
bounded disposable derivative, never independent authority. Nearest complete
project boundary, home exclusions dominate, explicit contributors (no implicit
builtin), capabilities derive profile selection (never audience identity).
No source => bounded Go lookup only, no evaluator/daemon/cache. Present source
cold evaluation may create private bounded snapshots even when inactive. Cold
startup/evaluation/cancellation/cleanup must fit the whole-hook budget. No stale
fallback, unjoined process, unrestricted imports/readers, permanent evaluator.
Single disk cap spans trace/snapshot/temp metadata; contract may partition cap
but cannot independently give every writer the full allowance.

TDD: first record an executable behavioral failing witness and exact failure
against current real code/binary, then implement and run the same witness green.
Compile errors/missing files are not behavioral red. Add precise ownership,
resource, freshness and public activation examples, not implementation mirrors.
No production stubs to enable green tests. Contract changes are messaged before
consumers adapt. Do not touch another lane to silence compile errors.

Go: values for facts, pointers for owned identities, concrete functions before
interfaces, clone retained strings/slices at ownership boundaries, one authority
per invariant, cancellation plus joined cleanup, no second mutable queue/cache
truth. Apply writing-go, writing-pkl, writing-repository-code, relevant testing
skills from /home/ubuntu/.bb/worktrees/env_r5k32s44gx/control/.agents/skills.
Root owns design/plan/ledger, review rulings, final verification and source delivery.
Preserve three pre-existing .orig/.rej files, lock files and recovery stash.

Current source contract is root-verified: workbench:context, workbench:context-home,
workbench:context-types; contextapi EvaluationInput/EvaluatedDeclaration and the
single DecodeProjectDeclaration/DecodeHomeDeclaration projection. The config
owner is replacing Load/Resolve. The shared-context-capacity node owns opaque
snapshot bytes and one concrete Pool; see cache-contract.md. Capacity Policy
uses the loader/evaluator-owned bounded freshness callback, not another manifest
model. All current worker/reviewer follow-ups remain Luna/xhigh/fast.
