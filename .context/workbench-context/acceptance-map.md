# Acceptance instruments and counterexamples

This is the driver's review rubric, not a second progress database. Completion is derived from the Workbench plan ledger.

| Promise | Required witness | Counterexample to interrogate |
| --- | --- | --- |
| Inactive work is invisible | Execute installed-style Go hooks in a fresh non-opted directory; empty stdout, success, no daemon/provider, no project/cache writes. Benchmark many invocations. | A lookup loads home config through Pkl or starts a daemon before deciding inactive. |
| One global setup | Reconcile isolated Claude/Codex home configurations twice; preserve unrelated hooks/settings; add an opted-in project without editing either global file. | Setup duplicates hooks or overrides another installed plugin. |
| Scope and withdrawal | Positive and negative ancestor resolution, home exclusion, nested opt-in, symlink alias, distinct worktree, invalid declaration, newly added nearer declaration. Withdraw while an offer exists. | Cached negative lasts forever; a pending old-config offer still injects after authority is withdrawn. |
| Useful guidance | Real agent reads a target file, receives nearby applicable instruction in the model request before its next action; unrelated reads produce nothing. | AdditionalContext appears in a mocked stdout fixture but the real host drops it. |
| Audience isolation | Two sessions in one directory, same source/content, independent full receipts; reset one context; exercise delayed confirmations. | Confirm old offer after slot replacement clears new guidance; equal text from another source receives a reminder without its full receipt. |
| Valid relevance | Explicit profile beats inferred defaults, scoped facts cannot leak, source/config/profile revision changes invalidate dependent pending selection; expiry explains actual rule inputs. | A provider process reused across projects retains the previous user's profile in a mutable map. |
| Contributor extension | Real subprocess JSON-RPC initialize/profile/contribute/shutdown with a project-configured executable; unrelated capable peer remains healthy. | Infinite stdout, ignored cancellation, blocked stdin, stderr secret dump, child process surviving timeout. |
| Bounded runtime | Concurrent cold starts yield one owner, hot request isolation, finite outstanding work, idle expiry and demand rebuild. | Goroutine per waiting request without admission bound; long provider call holds global engine lock. |
| Why and when | Inspect actual contributed/queued/offered/confirmed/deferred/rejected/withdrawn/error facts, causal observation and turn boundaries, old profile parameters after config edit. | Hash-only evidence points to today's file and invents an old decision. |
| Sampled gist | Bodies shorter/longer than 10,000 bytes, UTF-8 characters crossing every split, overlapping snippets, important omitted qualification. | Five independent 10 KB excerpts or sample text reused as real delivery text. |
| Rotating disposable cache | Tiny cap forces rotation, including indexes/dictionaries; retained record interpretation, bounded page query, clear and reopen, partial/corrupt final block. | Index memory grows forever; clearing cache resets delivery receipts; missing record is shown as no contribution. |
| Failure and recovery | Queue capacity and expiry, runtime restart, stdout failure, successful stdout then failed confirmation, provider rejection. | Restart claims delivery without receipts; overflow silently advances the provider evidence cursor. |
| Ownership | Mutate request/result backing storage after calls; race tests for concurrent offers, close and append/query. | Shallow-copy struct retains aliased map; tiny sample retains multi-megabyte source backing storage. |

## Performance acceptance

Record CPU/OS/RAM, binary revision, harness versions, directory depth, contributor count, payload size, and active audiences. Initial engineering targets on the available Linux machine: inactive hook p95 <= 30 ms, warm builtin hook p95 <= 100 ms, cold builtin startup <= 2 s; hard whole-hook deadline <= 2 s for ordinary requests. Use a representative depth-12 tree, eight active project scopes, 32 audiences, 100 invocations per warm/inactive measurement, and concurrent cold starts. A documented scope-specific provider deadline must fit the hook budget. These targets are hypotheses to test; a miss requires investigation and an explicit recorded rationale before changing the acceptance instrument.

Measure resident memory across the runtime and its owned provider process trees, plus goroutine/process counts before/after bounded idle release. No timing-only sleep assertion may substitute for observing actual cleanup. Record concrete process/RAM bounds implemented, test overload at those bounds, and separate Go heap budgeting from externally executed contributor memory.

Use tiny configured cache limits to prove hard rotation rather than filling 5 GB during routine tests. Exercise paginated query cost after many records and confirm bounded return bytes and memory. The production default remains 5,000,000,000 bytes.

## Honest evidence

Subprocess hooks prove decoding, emission and local confirmation. Only actual host/model request evidence establishes admission. A task-completion response alone may be confounded by prior instructions; use a fresh isolated session and randomized instruction/canary specific to the contributed source. Do not retain a whole transcript as product history. Test-only evidence may record bounded admission facts and redacted canary results in campaign artifacts.
