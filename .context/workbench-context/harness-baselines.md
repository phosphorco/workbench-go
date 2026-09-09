# Driver-run harness admission baselines

Observed 2026-09-08. These tests establish an available real-harness request
admission route. They use a fixture hook, not the Workbench implementation;
completion must repeat the route with the production Go CLI.

Both tests launch the actual harness against an HTTP server on loopback with a
dummy credential, a fixed tool call, and isolated configuration. No external
model is contacted. The local server captures actual outbound requests. Test
artifacts contain fixture data only; they are not product explanation history.

| Harness | Result | Actual receiving location |
| --- | --- | --- |
| Claude Code 2.1.260 | Exit 0; successful Read; PostToolBatch fired; marker found in request 2. | User-role tool_result content, inside system-reminder with PostToolBatch attribution. |
| Codex 0.153.4 | Exit 0; successful exec_command of cat README.md; PostToolUse normalized tool to Bash; marker found in request 2. | Separate developer-role message containing the marker. |

Claude script: `/tmp/workbench-context-admission/probe.py`. Artifacts:
`requests.json`, `hook-input.json`, `stdout.jsonl`, `stderr.txt` in that directory.
Isolated native binary:
`/tmp/workbench-context-claude.8DYqHI/node_modules/.bin/claude`.
It uses explicit settings, empty setting-sources, Read-only tool exposure,
isolated CLAUDE_CONFIG_DIR, and no session persistence.

Codex script: `/tmp/workbench-context-admission/codex-probe.py`. Artifacts are
under the sibling `codex/` directory. It runs the installed native binary
directly, bypassing the subscription router, with isolated CODEX_HOME, direct
user hooks.json, features.hooks=true, and additionalContextLimit=0. The exact
native executable path is in the script.

The first Codex attempt used read-only sandboxing, but this host could not set
up bwrap loopback networking; the tool failed before reading. Hook admission
still occurred. The successful read run uses danger-full-access and vetted hook
trust bypass in the disposable fixture: the deterministic local server supplies
only the fixed read command. This is a transport test, not an approval-gated
usability session. Its warnings about temporary helper aliases and fallback
model metadata are retained in stderr/stdout artifacts.

The fixture hooks consume their actual JSON stdin and return one
hookSpecificOutput.additionalContext marker. The successful claims require the
marker in the following captured request, not merely hook stdout or the final
assistant answer. Final acceptance will use a fresh canary sourced through
Workbench, test absence for irrelevant/inactive scopes, and preserve the
separate requirement for an unprompted real-agent usability trial.

## Exact output ceilings

Driver script `/tmp/workbench-context-admission/boundaries.py` repeated actual
request capture with deterministic ASCII source bodies on those same versions:

| Case | Complete body in next request |
| --- | --- |
| Claude, 10,000 characters | Yes |
| Claude, 10,001 characters | No; host substituted its oversize representation |
| Codex, 30,000 characters, additionalContextLimit=0 | Yes |
| Codex, 30,000 characters, default limit | No; host substituted its oversize representation |

Each run exited successfully, executed its read, and invoked the hook. Isolated
artifacts are under `/tmp/workbench-context-boundaries/<harness>-limit-<size>-<limit>/`.
The adapter must budget complete output including attribution before emission.
A 10,000 UTF-8-byte ceiling is conservative for Claude's character ceiling.
Codex global setup can use additionalContextLimit=0 so Workbench owns the
bounded complete-body decision without a hidden second truncation policy.
This is not permission for unbounded Workbench output.

## Production hook runners prepared

The independent driver runners in
`/tmp/workbench-context-production-admission/{probe.py,codex-probe.py}` accept
`WORKBENCH_PROBE_HOOK_COMMAND` instead of generating synthetic context. Each
creates an explicitly opted-in project and an `ai-context.md` rule whose
message contains a fresh production canary, absent from the requested read
file and model prompt. They retain the real harness's next outbound requests.
They are prepared, not yet passing production evidence: the integrated Go CLI
must build and supply its isolated runtime/home/cache path arguments first.

## Production inactive hooks: first assembled binary

Both native harnesses passed the inactive case using the actual Go CLI on
2026-09-08 at 23:36 UTC. Binary SHA-256:
`90a7920cdac84b4ecab72c190a3494d4620706d4398e2fd920a6484260da4563`.
The build includes uncommitted integration work; this is bounded evidence for
that executable, not a final revision acceptance claim.

Runner: `/tmp/workbench-context-production-admission/run.py`, scenarios
`claude inactive` and `codex inactive`. Each native process exited 0, performed
the README read, made two model requests, and admitted no canary. Neither
created its fresh Workbench runtime or cache directory. Isolated results and
captured requests are under that runner's directory:

- `claude-inactive-1788910570481-result.json`
- `codex-inactive-1788910571576-result.json`
- `evidence/<matching-run-name>/`

Enabled delivery, irrelevant opted-in rules, measured hook latency, and actual
model usability remain separate acceptance obligations.

## Production enabled admission

The rebuilt Go binary with SHA-256
`cfbf5ceddf625fa78594431b8fa9ee7fea3d3fcc68e53e831ae1b1d44b5dcbb1`
passed the positive native tests at 23:47 UTC on 2026-09-08. Both actual hosts
read their fixture README, exited 0, and sent the canary sourced from the
project's ai-context rule in model request 2. The canary was absent from the
prompt and read file. Claude placed it in a user-role tool result; Codex placed
it in a developer-role message. This is production Go admission through the
real hosts; the model server remains a deterministic loopback fixture.

Artifacts under `/tmp/workbench-context-production-admission`:

- `claude-enabled-1788911236784-result.json`
- `codex-enabled-1788911237433-result.json`
- `evidence/<matching-run-name>/requests.json`

The same production binary is used for the matching negative controls and
resource measurement. Actual-model usability remains a separate gate.

Matching controls on the same rebuilt binary also passed:

| Harness | Irrelevant opted-in rule | Inactive directory |
| --- | --- | --- |
| Claude | `claude-irrelevant-1788911257937` | `claude-inactive-1788911295968` |
| Codex | `codex-irrelevant-1788911262421` | `codex-inactive-1788911296516` |

All exited 0 after two actual model requests without the canary. Inactive cases
created no Workbench runtime/cache directories. Irrelevant opted-in cases used
the runtime and cache, as expected for observable no-match decisions. Result
JSON and per-run evidence use the same directory scheme as the positive runs.

## Final assembled admission repetition

Binary `b06bff4463b23917e0b64fc95cf98bb2a1246ff920dedb05cf306ae07ca6c061`
passed all six production Go cases again. Summary:
`/tmp/workbench-context-final-native/results.json`.

| Harness | Enabled | Irrelevant | Inactive |
| --- | --- | --- | --- |
| Claude 2.1.260 | `claude-enabled-1788913210398` | `claude-irrelevant-1788913213004` | `claude-inactive-1788913216252` |
| Codex 0.153.4 | `codex-enabled-1788913210402` | `codex-irrelevant-1788913212215` | `codex-inactive-1788913214493` |

Each used the actual native host, actual Go command, and deterministic localhost
model fixture. Only enabled applicable guidance appeared in the next outbound
request. Inactive cases created neither runtime nor cache. Per-run artifacts
remain under `/tmp/workbench-context-production-admission/evidence/`.
Actual-model unprompted use is recorded separately in `usability-evidence.md`.
