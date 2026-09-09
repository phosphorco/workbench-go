# Live harness QA for Workbench context

Use this procedure to demonstrate the complete installed hook path with a real
Claude Code or Codex session. A pass requires native hook execution, matching
Workbench explanations, and evidence that the model used guidance available
only through the hook. A successful `workbench context hook` invocation alone
does not pass this test.

Run each harness separately in a fresh fixture. Keep the provider's real model
endpoint and normal authentication. Record the model and harness version, exact
Workbench binary hash, setup output, native events, and Workbench history.
The [integration evidence](../.context/workbench-context-declaration/integration-evidence.md)
describes a different test: real harnesses against a controlled model endpoint,
which can inspect the exact next request deterministically.

## Prepare an isolated fixture

Use Bash for these snippets. Set `WB`, `CLAUDE`, and `CODEX` to absolute installed
executables. Workbench must have its matching private Pkl and runtime lock in
the installed layout; copying only the Go binary is insufficient. Keep the
fixture path short for Unix socket limits. Do not run inside an existing
project or reuse its harness configuration.

```bash
WB=/absolute/install/bin/workbench
CLAUDE=/absolute/path/to/claude
CODEX=/absolute/path/to/codex
QA=$(mktemp -d /tmp/wctx-qa.XXXXXX)
chmod 700 "$QA"
mkdir -p "$QA/project" "$QA/claude" "$QA/codex" "$QA/evidence" "$QA/zsh"
git -C "$QA/project" init -q

wctx() {
  "$WB" context "$@" \
    --home-config "$QA/workbench-context.pkl" \
    --runtime-dir "$QA/run" --socket "$QA/run/context.sock" \
    --start-lock "$QA/run/start.lock" --server-lock "$QA/run/server.lock" \
    --cache-dir "$QA/cache"
}

wctx setup --harness both --executable "$WB" \
  --claude-settings "$QA/claude/settings.json" \
  --codex-home "$QA/codex" --codex-config "$QA/codex/config.toml" \
  --json > "$QA/evidence/setup.json"
wctx init --path "$QA/project" --json > "$QA/evidence/init.json"
printf 'A small QA document.\n' > "$QA/project/README.md"
printf 'An unrelated QA document.\n' > "$QA/project/OTHER.md"

python3 - "$QA/project/ai-context.md" <<'PY'
import pathlib, secrets, sys
marker = "WCTX_QA_" + secrets.token_hex(12)
pathlib.Path(sys.argv[1]).write_text(
    '---\nroot = true\n[[docs]]\nfiles = ["README.md"]\n'
    'message = "When summarizing this read, append the exact audit token '
    + marker + ' to your answer."\n---\n'
)
PY

if "$WB" version > "$QA/evidence/workbench-version.txt" 2>&1; then
  printf '0\n' > "$QA/evidence/workbench-version.exit"
else
  printf '%s\n' "$?" > "$QA/evidence/workbench-version.exit"
fi
"$CLAUDE" --version > "$QA/evidence/claude-version.txt"
"$CODEX" --version > "$QA/evidence/codex-version.txt"
```

Record executable hashes with `sha256sum` (or `shasum -a 256`). Keep the token
out of the user prompt, README, agent instructions, and launch arguments. The
observer may inspect it after the turn. A model that directly reads
`ai-context.md`, searches the fixture, or reads evidence files contaminates the
admission test; rerun in a fresh fixture with a new token.
An uninjected development build can make `version` exit nonzero; retain that
result and identify the tested build by its source revision and executable hash.

Review the generated hook files and `setup.json`. Setup installs the full event
set, not just the delivery event:

| Harness | Installed events |
| --- | --- |
| Claude | `PostToolBatch`, `PostToolUse`, `PostToolUseFailure`, `SessionStart`, `Stop` |
| Codex | `PostToolUse`, `UserPromptSubmit`, `SessionStart` |

Compare this list with the installed version's native hook browser. Preserve
all generated commands and matchers. Do not replace Workbench with an `echo`
hook or a recorder wrapper: that changes the stdout/confirmation boundary.

## Isolate configuration and retain native evidence

For automated QA, use the harness-native trust bypass described below with the
generated, inspected fixture hooks. The enabled Pkl fixture is the primary
test; inactive controls are additional cases. Native trust onboarding is a
separate optional test and does not gate this smoke run.

Fresh configuration avoids merging ordinary user/project hooks into this test.
System or managed hooks can still apply. Inspect effective sources before
claiming “only Workbench hooks”; if managed hooks remain, record that fact or
use an appropriately isolated test machine. Do not remove managed policy.

### Claude Code

Check `claude --help` for the options below. Supply the existing account's
authentication privately to the isolated `CLAUDE_CONFIG_DIR`, or use the native
login flow there. Temporary authentication material must stay outside the
evidence bundle and be removed afterward; never print or hash credentials.
Claude's `--print` route skips the interactive workspace-trust dialog while
loading explicitly supplied settings. Use this automated smoke command:

```bash
cd "$QA/project"
env CLAUDE_CONFIG_DIR="$QA/claude" CLAUDE_CODE_DEBUG_LOG_LEVEL=verbose "$CLAUDE" \
  --print 'Read only README.md using Read, then give a one-sentence summary. Do not read other files or change files.' \
  --setting-sources '' --settings "$QA/claude/settings.json" \
  --strict-mcp-config --mcp-config '{"mcpServers":{}}' \
  --tools Read --allowedTools Read \
  --output-format stream-json --verbose --include-hook-events \
  --debug-file "$QA/evidence/claude-debug.log" \
  > "$QA/evidence/claude-events.jsonl" 2> "$QA/evidence/claude-stderr.txt"
```

Inspect the generated settings before launch and the effective source/matcher
list in the native debug log. An empty `--setting-sources` excludes ordinary settings sources;
`--settings` supplies the fixture explicitly. Do not set `disableAllHooks`: that
would disable the hooks under test too. The native debug log supplies hook
matching/execution and output diagnostics; retain the session/tool transcript
alongside it. See [Claude CLI options](https://code.claude.com/docs/en/cli-usage)
and [debugging configuration](https://code.claude.com/docs/en/debug-your-config).

This command starts a new session; it does not test same-session suppression.
For that case, omit `--print` and the stream-output flags, use the same isolated
settings interactively, inspect `/hooks`, and submit successive prompts in one
session. Use an external timeout for automation. The narrow `Read` allow-rule
does not disable the sandbox or grant arbitrary tool access.
When supported by the installed release, `--init-only` is a model-free startup
check: it can demonstrate the generated `SessionStart` command and its Workbench
observation. It does not exercise a read, failure, stop, or model admission.
Avoid `--bare` and `--safe-mode`, which skip the hooks being tested.

### Codex

Supply existing account authentication privately to the isolated `CODEX_HOME`,
or use `env CODEX_HOME="$QA/codex" "$CODEX" login`. After inspecting the generated
hook commands, launch with the invocation-scoped hook-trust bypass:

```bash
cd "$QA/project"
env CODEX_HOME="$QA/codex" ZDOTDIR="$QA/zsh" "$CODEX" \
  --dangerously-bypass-hook-trust \
  -C "$QA/project" --sandbox workspace-write --ask-for-approval on-request \
  -c "log_dir=\"$QA/evidence/codex-log\"" \
  -c allow_login_shell=false
```

Use `/hooks` to inspect effective sources; no interactive hook approval is
needed for this bypassed invocation. Record **trust bypassed for isolated QA**.
For the separate onboarding test, omit the flag, inspect the untrusted/skipped
state, review/trust through `/hooks`, and start a fresh session. Project hooks, inline
config, plugins, and managed sources can compose; a fresh `CODEX_HOME` alone
is not proof that all other sources are absent. See
[Codex hook discovery and trust](https://learn.chatgpt.com/docs/hooks).

This bypass exists for automation that has already vetted the exact hook
sources; it does not prove the native trust workflow. It is separate from sandbox/approval bypasses,
which this procedure does not need. `--ignore-user-config` is not a hook-isolation
switch. Explicit [`log_dir`](https://learn.chatgpt.com/docs/config-file/config-reference)
enables a plaintext TUI log; do not assume `codex exec --json` exposes every hook
notification or the complete model request.

For structured invocation evidence, use the same native Codex **app-server**
with the same isolated configuration and invocation-scoped trust bypass. It executes the real
harness; it is not a Workbench hook emulator. Generate its exact release schema:

```bash
"$CODEX" app-server generate-json-schema --out "$QA/evidence/codex-schema"
env CODEX_HOME="$QA/codex" ZDOTDIR="$QA/zsh" "$CODEX" \
  --dangerously-bypass-hook-trust \
  -c allow_login_shell=false app-server --listen stdio://
```

An agent operating this stdio process must keep reading notifications while
handling requests. Follow the [native protocol](https://learn.chatgpt.com/docs/app-server):

1. Send `initialize` with client information; after its response send `initialized`.
2. Call `hooks/list` with `cwds: [<absolute fixture project>]`; retain the effective
   sources, commands, enabled states, and trust decisions.
3. Call `thread/start` with the fixture `cwd`, chosen live model,
   `approvalPolicy: "on-request"`, and `sandbox: "workspace-write"`. Include
   `config: {"bypass_hook_trust": true}` in this isolated thread's configuration;
   verify it against the generated schema/release. Do not persist the bypass in
   ordinary user configuration or assume the process flag survives every
   client-supplied thread configuration.
4. Call `turn/start` with the returned thread ID and the text prompt below.
   Forward native approval requests to the observer; never auto-approve them.
5. Retain `hook/started`, `hook/completed`, tool item events, warnings, and
   `turn/completed`. Repeat `turn/start` on the same thread for the repeat case.
6. Stop/join the owned process after the final turn and artifact capture.

Synchronous hook notifications contain `threadId`, optional `turnId`, and `run`:
`id`, `eventName`, `sourcePath`, `startedAt`, `completedAt`, `durationMs`, `status`,
and output `entries`. Save the generated schema with the results; these native
API fields are versioned independently of Workbench. Do not substitute a
different model endpoint for this live-model pass. `codex debug prompt-input`
renders initial prompt input; it is not proof of a later tool-triggered injection.

For a JSONL capture containing the native notifications directly, extract:

```bash
jq 'select(.method == "hook/started" or .method == "hook/completed") |
  {event: .method, thread: .params.threadId, turn: .params.turnId,
   run: .params.run}' "$QA/evidence/codex-events.jsonl"
```

The capture must include session-start notifications before the first turn,
not just messages received after `turn/start` returns.

## Exercise behavior, not just installation

First prompt, identical in both harnesses:

> Read only README.md using the normal file-read tool (or `cat README.md`), then
> give a one-sentence summary. Do not read any other files or change files.

The token must appear in the answer, with native tool evidence showing no direct
read of its source. Distinguish **receipt** from **use**: a model quoting the
token only to reject the hook's instruction proves receipt, but does not pass
the guidance-use check. Record both verdicts. A factual variant supplies a
project name only in the hook and asks for the tool's name and purpose; the
same distinction applies if the model rejects that fact. Where a native view
exposes the inserted context, retain that too. Exact wire-level admission is a
separate claim requiring a captured model request; a final answer is not such a
capture.

After each action, collect the evidence in the next section. Keep repeat actions
within the configured idle TTL and record daemon generation: a daemon restart
legitimately loses in-memory delivery receipts.

| Action | Native evidence | Workbench expectation |
| --- | --- | --- |
| Fresh trusted session | `SessionStart` executed from generated settings | Enabled observation with matching hook event, resulting epoch and reason. The exact session source/transition kind comes from native evidence. No relevant read yet. |
| Submit prompt in Codex | `UserPromptSubmit` executed | Matching normalized observation; no invented file read. |
| Read README | Successful Read or simple `cat`; installed post-tool event(s) execute | README resource with successful outcome and named contributor. Claude delivers at `PostToolBatch`; its per-tool callback can defer pending guidance. Codex delivers at its available post-tool opportunity. Require offered and confirmed guidance at the delivery boundary. |
| Read README again in the same session | A second actual tool call and hooks, not an answer from memory | New observation; already-delivered guidance is suppressed with its reason, no new confirmed delivery. The model still knowing the token is expected. |
| Read OTHER in a fresh session | Successful read and post-tool hooks | OTHER observation; no matching guidance offered/confirmed. No token in the answer. |
| Ask Claude to Read a nonexistent file | Native failure and `PostToolUseFailure` | Failed resource outcome; never invent a successful read or confirm matching guidance. |
| Finish a Claude turn | Native `Stop` hook | Matching enabled observation with no fabricated file operation. |
| Clear/reset through native session UI, then read README | Supported native reset/start event and a real read | New audience epoch or session identity; guidance can be delivered again. Record the actual source value in native evidence: Workbench history does not retain the exact transition kind/source. Do not fake the hook input. |
| Disable the project declaration, then use a fresh session | Hooks still execute; their output is empty | `status` reports disabled activation. No new delivery/history from inactive hooks. |
| Fresh fixture with no project **or home** declaration | Hooks execute silently | No evaluator/runtime/cache artifacts before inspection. `status` explains no applicable declaration. |

For negative cases, use a fresh session and token so previous context cannot
produce a false positive. To disable the declaration, add `enabled = false` to
the fixture Pkl; retain the exact before/after source. For no-source, use a new
fixture, omit `init` and both declarations, and capture filesystem state before
running history/cache commands: those explicit commands can create an inspection
runtime. Claude versions may emit both per-tool and batch events; record the
actual sequence, without expecting one record per human action. In particular,
`PostToolUse(Read)` and the later `PostToolBatch` are distinct native invocations;
retain and explain both. `Stop` occurs after each assistant response, not only
at task completion, and is not a substitute for an interrupt event.
Claude's `PostToolUse`, `PostToolUseFailure`, `SessionStart`, and `Stop` are
observation-only surfaces in Workbench; only `PostToolBatch` can deliver. Codex's
three installed events are available delivery surfaces, subject to relevance
and pending state. Installed does not mean “must emit guidance.”

## Require the observable evidence trail

Use the same `wctx` function and fixture paths as setup. Capture both readable
output and JSON; the JSON retains the detailed why-inputs.

```bash
wctx status --path "$QA/project" --json > "$QA/evidence/status.json"
wctx history --path "$QA/project" > "$QA/evidence/history.txt"
wctx history --path "$QA/project" --json > "$QA/evidence/history.json"
wctx cache status --path "$QA/project" --json > "$QA/evidence/cache.json"
```

Give files a case/step suffix instead of overwriting earlier captures. Follow
`HasMore` with the returned `NextAfter`/`NextCursor` (`--after` and `--cursor`)
until the relevant interval is covered. `Gaps`, `PartialScan`, `PartialPage`, or
cache `KnownLoss` prevent a claim of complete observation coverage.
Also capture `.runtime.status.generation` and `.runtime.status.traceDropped`
from CLI status while the daemon is running. The drop counter is process-local;
zero after restart is not a historical guarantee. Before expecting repeat
suppression, establish a confirmed receipt and verify the same daemon generation
and resident partition before/after the repeat. Restart or idle eviction makes
that particular suppression test unexercised.

For each relevant native invocation, identify the corresponding record group:

- **What and when:** `At`, `Inputs` keys `observation.hookEvent`,
  `observation.invocationId`, `observation.causalId`, `observation.id`, and
  `resource.path`, `resource.operation`, `resource.outcome`.
- **Whose context:** `Scope`, `Audience`, `Epoch`, optional `Turn`, and `Profile`.
  A native hook-run ID is not necessarily Workbench's invocation ID. Match
  exposed IDs where available, then session/turn, event, resource and time;
  disclose ambiguous correlation instead of inventing an exact join.
- **Why:** contributor/source, `ContributionID`, `Reasons[].Provider`, `Rule`,
  and typed `Params` including `_reason.code`, `_reason.summary`, and
  `_reason.at.unixNano`. Human history currently summarizes some parameter sets;
  the JSON is required to audit their values.
- **Result:** offered/confirmed/suppressed/rejected/withdrawn outcome and bounded
  content sample. Observation time is not native hook start/end timing; use the
  native log or `run` timestamps to calculate hook duration.

Inspect the actual identifiers returned by history, not guessed filenames:

```bash
wctx inspect contribution "$CONTRIBUTION_ID" --path "$QA/project" --json
wctx inspect turn "$TURN_ID" --path "$QA/project" --json
wctx explain profile "$PROFILE_REVISION" --path "$QA/project" --json
```

`CONTRIBUTION_ID` is Workbench's numeric `ContributionID`, derived from the item
identity. It is not the native hook-run ID or `tool_use_id`.

Skip the turn query explicitly when the native payload supplied no turn ID.
Confirmation records can lack `Turn`; correlate them through contribution,
scope/audience/epoch and offer/content identity. A confirmed record means
Workbench completed its local output handoff, not that the remote model used it.

This projection makes normalized observation fields visible while retaining the
full history response separately. History JSON is not the original hook payload;
retain native logs and any native payload capture separately:

```bash
jq '.Records[] | {ID, At, Kind, Outcome, Scope, Audience, Epoch, Turn,
  ContributionID, Profile,
  observations: [.Inputs[]? | select(.Key | startswith("observation."))],
  resources: [.Inputs[]? | select(.Key | startswith("resource."))],
  Reasons}' "$QA/evidence/history.json"
```

Workbench history is a bounded decision trace, not a universal hook process
audit. Inactive/no-source invocations deliberately produce no history; failures
before runtime observation may appear only in native hook stderr. Missing
history therefore cannot prove “hook did not run.” For these cases retain native
execution evidence, contemporaneous `status`, exact declarations, and filesystem
observations. If an enabled invocation reaches the runtime but lacks its expected
record and no declared loss explains it, report an observability failure even
when the model answer looks correct.
Check queue-drop aggregates, the current `traceDropped` counter, and cache-policy
failure reasons as well as persisted query gaps. If those make the interval
incomplete, report that limit instead of certifying complete coverage.
`traceDropped` is not a one-to-one count of missing history records, and not
every increment has a corresponding aggregate marker.

## Report and clean up

Report each matrix row as pass, fail, or not exercised, linking the native event,
Workbench record IDs/reasons, and model/tool transcript. Include effective hook
sources and trust mode, versions/hashes, model identity, exact config, timestamps,
latency, evidence gaps, and any direct-read contamination. Do not call the whole
hook set covered if only a successful read was exercised.
For the enabled case, report hook execution, model receipt, and guidance use
separately; native trust bypass establishes execution eligibility, not how a
model will judge the supplied content.

Keep raw logs private: they may contain prompts and credentials. Share a redacted
projection of the fixture evidence, never authentication files. Exit the native
sessions, allow the fixture daemon to idle out or stop only its verified exact
PID/socket-owned process, and check that no fixture process remains. Retain the
evidence first, then remove only the recorded fixture directory. Do not use a
global `pkill`, alter normal hook configuration, or clear unrelated caches.

The implementation reference points are the [setup event lists](../cmd/workbench/context_cli.go),
[native adapter](../internal/contexthook/hook.go),
[trace inputs and confirmation records](../internal/contextdaemon/runtime.go),
and [bounded query contract](../internal/contexttrace/contexttrace.go).

## Checked-in capture runner

The checked-in acceptance runner is acceptance/context_live_qa.py. It requires
explicit --workbench, --output, --harness, and --mode arguments and has no
ad-hoc /tmp runner default. The controlled Claude command is:

    python3 acceptance/context_live_qa.py --workbench "$WB" --claude "$CLAUDE" --harness claude --mode controlled --output "$QA/evidence/capture"

It retains the raw Claude stream, bounded exact hook stdin/stdout pass-through
records, actual native hook events, loopback request bytes, authored inputs,
and Workbench observations. The source manifest names normalizer v5 derived
paths; export-transcripts creates those derivations. Use --self-test-bounds for
the no-provider stalled-child, process-group cleanup, output-cap, and
oversized-stdin witness. Live Claude and Codex modes are explicit native
adapters: they require caller-supplied binaries and private credential paths,
copy credentials into isolated mode-0600 homes, and remove them in finally.
Codex uses the real app-server JSON-RPC initialize/hooks-list/thread/turn
route, forwards approval requests to private decision files, and records
repeat and compaction notifications without synthesizing native events. Neither
live mode is invoked by the controlled Go acceptance test.
The provider-free `--self-test-protocol` option exercises the bounded local
JSON-RPC client only; it writes no capture manifest and does not promote its
scripted response to native evidence. An explicit live invocation runs the
enabled, irrelevant, failed-read, disabled, and no-source cases for the chosen
harness; Claude uses a native `/clear` input and Codex uses native compaction
and a post-compaction turn. The runner does not treat the compact RPC response
as completion: it waits for native `contextCompaction` started/completed
records, the matching `turn/started`, observed status, idle, and terminal turn
boundary. Claude waits for
the next native `SessionStart` boundary before its post-reset read.

Live release identity is pinned by all three flags:

    --expected-workbench-release 0.8.0 \
    --expected-workbench-commit 567493e4b0de2dd4e837cb7ff821fe35f91f028a \
    --expected-workbench-sha256 "$WB_SHA256"

The runner executes the bounded `workbench version` subprocess, hashes the exact
executable, retains `workbench-version.json`, and refuses a live capture when a
value is absent or differs. Controlled/dev captures retain the observed output
but qualify it as unpublished/unknown rather than inventing a release tag.
Delivered is pass only when a structured Workbench `OutcomeConfirmed` history
record matches the case resource and carries its actual causal/invocation and
contribution identifiers; a hook exit code alone is not delivery evidence.
The retained input sidecar is the source for input actor and boundary fields.
Native/RPC streams and boundary checkpoint sidecars retain IDs actually present
in provider messages. An unavailable wire request has locators to both the
bounded wire sidecar status and reason records.

`--self-test-contract` is an offline-only counterexample witness. It proves that
hook success alone does not satisfy delivery, that input actor derives from the
retained harness input, that structured confirmation links are required, and
that unavailable wire capture has explicit evidence locators. It creates no
provider process or native-event provenance.

Codex live cases have a finite 240-second default per-case allowance, including
approval waits; the byte and process bounds remain independent hard limits. The
native thread receives an isolated `PATH=/usr/bin:/bin`, which is retained in
the credential-free `fixture-environment.jsonl` sidecar so external fixture
commands such as `sed` and `cat` are reproducible without ambient credentials.

When Codex requests an approval, the runner atomically creates a mode-0600
`pending-<request-id>.json` and appends/fsyncs a credential-free record to
`approval-events.jsonl` containing the method, request ID, pending path,
decision path, and deadline. An observer watches that file, reviews the private
pending request, and writes `{"decision":"accept"}`, `{"decision":"decline"}`,
or the method-specific bounded permissions object to the named decision path.
The runner never auto-approves. A missing decision is retained as an explicit
approval-timeout/incomplete case and rejects the live matrix; a clean process
shutdown does not convert it into delivery or model-adoption evidence.

The live summary is rejected and exits nonzero if any matrix row is
authentication-failed, provider-nonzero, timed out, approval-timeout, or
otherwise not exercised. Provider exit 0 is necessary but not sufficient for a
case pass; delivery still requires the structured Workbench offer/confirmation
link, and the audit-token fixture does not claim task adoption. Claude delivery
matches `PostToolBatch`; Codex delivery matches `PostToolUse`. The merged
manifest contains one root `workbench-version.json` entry, and all file paths
must be unique. A persistent Claude stream that exits 143 after every expected
successful result and reset boundary is classified as `intentional-joined`,
not as provider failure; exit 143 without those successful boundaries remains
provider-nonzero/not-exercised and cannot support delivery.
