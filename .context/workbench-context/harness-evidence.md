# Harness probe evidence

Status: bounded protocol investigation, 2026-09-08.  This is evidence for
plan node `harness-probe`; it is not a claim that the Control `agent-cli`
implementation is Workbench behavior.

## Result and capability gaps

The provider contract is sufficient to design Go adapters:

| Harness | Immediate model-context boundary | Delayed boundary | Stable identities observed | Provider output limit |
|---|---|---|---|---|
| Claude Code | `PostToolBatch` | `SessionStart`/`UserPromptSubmit` are available, but are not needed for the first batch-delivery slice | `session_id`; current Claude also supplies `prompt_id` (2.1.196+) | `additionalContext` is capped at 10,000 characters; over-limit content is replaced by a preview/path |
| Codex | `PostToolUse` | `UserPromptSubmit`, `SessionStart` | `session_id`, turn-scoped `turn_id` | default roughly 2,500 tokens per model-visible hook-output message; over-limit content spills to a file/preview unless the handler changes `additionalContextLimit` |

Capability gaps that must remain visible:

1. The exact Claude Code 2.1.260 executable now launches at
   `/tmp/workbench-context-claude.8DYqHI/node_modules/.bin/claude`, and its help
   exposes the temporary settings flags needed below. A driver-run localhost
   baseline has now proven real host admission with a fixture hook; the
   Workbench Go hook replacement and its admission remain unproven.
2. The installed Codex is `codex-cli 0.153.4`, but `codex plugin list --json`
   does not report the Phosphor plugin even though a dedicated marketplace and
   cached payload exist. No global registration or reinstall was attempted.
3. Neither vendor documentation found here specifies a maximum hook **stdin**
   size. The Control binding calls `readFileSync(0)` with no application limit,
   but that is not evidence of an OS/provider limit. Workbench must configure a
   fail-closed byte limit and report `stdin_limit_exceeded`; it must not claim a
   vendor limit until measured with the isolated real harnesses.
4. For the Workbench Go hook, hook stdout JSON alone proves only local handoff.
   The driver’s fixture-hook baseline proves that a real Claude request can
   carry the marker, but exact Workbench bytes still require a version-pinned
   request, proxy, or diagnostic artifact. A model answer is corroborating
   evidence, not the delivery oracle.
5. The current Codex CLI has no `codex hooks` subcommand (`codex hooks --help`
   falls back to root help). Current official guidance points to `/hooks` and
   the hook/config files for review.
6. No localhost Anthropic/Responses fixture server exists in this checkout yet;
   the driver’s external Claude baseline is recorded below, while the route’s
   Workbench Go hook replacement and Codex leg remain pending.
7. Neither hook DTO provides a universal monotonic context-epoch field.
   `session_id` alone does not establish context continuity. The adapter/runtime
   must derive a fresh epoch from supported reset/fork/compact evidence; when
   continuity is unknown, prior receipts must not suppress guidance in the new
   epoch and the source must be safely re-recruited.

No durable message, global configuration edit, or commit was performed by this
evidence-writing probe. The driver’s separate fixture-hook baseline is recorded
below; it is not Workbench Go acceptance.

## Evidence classification

| Classification | Evidence used | Interpretation |
|---|---|---|
| Sourced protocol | [Claude hooks](https://code.claude.com/docs/en/hooks), [Claude plugins](https://code.claude.com/docs/en/plugins-reference), [Codex hooks](https://developers.openai.com/codex/hooks) (currently redirects to the official Learn page), [Codex plugin build](https://developers.openai.com/codex/plugins/build) | Vendor-supported DTOs, lifecycle, output envelopes, limits, trust/config locations. |
| Accepted product protocol | [accepted Workbench context ADR](/home/ubuntu/phosphor/workbench-go/docs/adr/2026-09-wokbench-context.md) | Audience/epoch isolation, complete source blocks, bounded queue, receipt-after-output, fail-open provider loop. It does not prove commands are shipped. |
| Control source learning | [Control `docs/CLAUDE.md`](/home/ubuntu/phosphor/control/docs/CLAUDE.md:1), [Control `docs/CODEX.md`](/home/ubuntu/phosphor/control/docs/CODEX.md:1), [Control harness ADR](/home/ubuntu/phosphor/control/docs/adrs/harness/2026-07-agent-hook-context-magnet-delivery.md:1), [Control architecture](/home/ubuntu/phosphor/control/pkg/@agent-plugins/agent-cli/ARCHITECTURE.md:1) | Useful adapter and failure-surface precedent, explicitly legacy/Control PoC. The `agentd` inbox and durable messaging portions are outside this Workbench request. |
| Current installed observation | `codex-cli 0.153.4`; Claude Code `2.1.260` executable launches at `/tmp/workbench-context-claude.8DYqHI/node_modules/.bin/claude`; Bun `1.3.14`; Node `v22.21.1` | Machine facts as observed on 2026-09-08, not stable product guarantees. |
| Assumption/open proof | Workbench Go APIs below; isolated smoke route below | Design proposals or test instructions, not observed behavior. |

## Measured isolated Claude baseline (fixture hook only)

The driver ran `/tmp/workbench-context-admission/probe.py` with the exact Claude
2.1.260 executable, isolated `--settings`, and `--setting-sources empty`. The
loopback Anthropic server returned one deterministic `Read` tool call over SSE;
Claude exited `0`, made two actual upstream requests, and ran the
`PostToolBatch` hook. A randomized guidance marker emitted by the fixture hook
was present in the second actual request. This proves Claude host admission and
the system-reminder/request plumbing, not Workbench implementation.

Artifacts are available at:

- [baseline runner](/tmp/workbench-context-admission/probe.py)
- [captured requests](/tmp/workbench-context-admission/requests.json)
- [captured PostToolBatch input](/tmp/workbench-context-admission/hook-input.json)
- [captured Claude stdout](/tmp/workbench-context-admission/stdout.jsonl)
- [captured Claude stderr](/tmp/workbench-context-admission/stderr.txt)

The final acceptance must replace the fixture hook with the Workbench Go hook,
retain the same isolated settings and loopback SSE server, and re-prove the
marker in request two. No claim is made here about Go CLI behavior until that
replacement run succeeds.

## Official Claude protocol

The official hook locations are `~/.claude/settings.json`, project
`.claude/settings.json` and `.claude/settings.local.json`, managed policy, and
plugin `hooks/hooks.json`. A plugin may provide hooks in `hooks/hooks.json` or
inline in its manifest. Claude runs `SessionStart`/`SessionEnd` at session
boundaries, prompt/stop events at turn boundaries, and tool events for tool
calls. `PostToolBatch` runs after a complete parallel tool batch and before the
next model call.

Common input is one JSON object on stdin. Relevant current fields are:

```json
{
  "session_id": "ses_...",
  "prompt_id": "uuid-or-null-in-older-versions",
  "transcript_path": "/absolute/path/or-null",
  "cwd": "/absolute/workspace",
  "permission_mode": "...",
  "hook_event_name": "PostToolBatch",
  "effort": "..."
}
```

`prompt_id` is documented for current Claude versions (2.1.196+), and is a
prompt-boundary correlation key, not a context epoch. The current Claude
`SessionStart` source distinguishes startup/resume/clear/compact/fork; `fork`
is distinct from resume in current releases. `session_id` is only the provider
session component and does not establish context continuity. No universal
monotonic epoch identifier was found. The adapter/runtime must derive a fresh
epoch from supported reset/fork/compact evidence; if the marker or continuity
proof is absent, safely start a new epoch and treat earlier receipts as
inapplicable so guidance can be re-recruited.

The event-specific tool DTOs are:

```json
{
  "session_id": "ses_...",
  "transcript_path": "/tmp/claude.jsonl",
  "cwd": "/work/tree",
  "permission_mode": "default",
  "hook_event_name": "PostToolUse",
  "tool_name": "Read",
  "tool_input": {"file_path":"README.md"},
  "tool_response": "provider-defined structured result",
  "tool_use_id": "toolu_...",
  "duration_ms": 12
}
```

`PostToolBatch` instead carries `tool_calls`, each with
`tool_name`, `tool_input`, `tool_use_id`, and `tool_response`. Its
`tool_response` is the serialized result content Claude sees; it is not the
same object as the structured `PostToolUse` result. A representative fixture
for an actual batch is:

```json
{
  "session_id":"ses_probe",
  "prompt_id":"prompt_probe",
  "transcript_path":"/tmp/claude-probe.jsonl",
  "cwd":"/tmp/workbench-fixture",
  "permission_mode":"default",
  "hook_event_name":"PostToolBatch",
  "tool_calls":[
    {"tool_name":"Read","tool_input":{"file_path":"README.md"},"tool_use_id":"tool_read_1","tool_response":"# fixture"},
    {"tool_name":"Bash","tool_input":{"command":"printf probe"},"tool_use_id":"tool_bash_1","tool_response":"probe"}
  ]
}
```

The model-visible output envelope is exactly one JSON object whose event name
matches the callback:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PostToolBatch",
    "additionalContext": "<complete source blocks>"
  }
}
```

Claude wraps `additionalContext` in a provider-owned system reminder. The
10,000-character ceiling applies to the injected value; above it Claude gives
the model a preview and session-file path. Workbench must admit only complete
source blocks within its configured byte/character budget and receipt only
after stdout succeeds.

For Workbench v1 the necessary Claude delivery event is `PostToolBatch`.
`PostToolUse` and `PostToolUseFailure` may collect observations/diagnostics;
`SessionStart` and `Stop` are lifecycle opportunities. They should not be
treated as additional batch boundaries. The current vendor surface has more
events (including compact/session-end variants); adding them requires a
separate adapter decision and evidence.

## Official Codex protocol

Current official Codex hook/config locations are `~/.codex/hooks.json`,
`~/.codex/config.toml`, `<repo>/.codex/hooks.json`, and
`<repo>/.codex/config.toml`; matching definitions merge. Project-local hooks
require a trusted project. Plugin-bundled hooks run alongside these layers and
are discovered from `hooks/hooks.json` unless the plugin manifest overrides the
path. The feature gate is `features.hooks = true`.

Common input is one JSON object on stdin. Relevant fields are:

```json
{
  "session_id":"sess_...",
  "turn_id":"turn_...",
  "transcript_path":"/absolute/path/or-null",
  "cwd":"/work/tree",
  "hook_event_name":"PostToolUse",
  "model":"...",
  "permission_mode":"..."
}
```

`turn_id` is present for turn-scoped callbacks. `transcript_path` is a
provider-owned convenience path, not a stable transcript API. Codex currently
exposes `SessionStart` sources such as startup/resume/clear/compact and
Pre/PostCompact callbacks, but no universal monotonic epoch field. `session_id`
is only the provider-session component. Derive a fresh epoch from supported
reset/compact evidence and continuity checks; if those are missing or
ambiguous, treat the audience as a new epoch, do not apply old receipts, and
allow safe re-recruitment. Preserve `turn_id` and all reset markers as evidence.

The immediate tool fixture is:

```json
{
  "session_id":"sess_probe",
  "turn_id":"turn_probe",
  "transcript_path":"/tmp/codex-probe.jsonl",
  "cwd":"/tmp/workbench-fixture",
  "hook_event_name":"PostToolUse",
  "model":"fixture",
  "tool_name":"Bash",
  "tool_use_id":"call_bash_1",
  "tool_input":{"command":"printf probe"},
  "tool_response":{"stdout":"probe","exit_code":0}
}
```

Current official coverage includes `PreToolUse`/`PostToolUse` for Bash,
unified exec, `apply_patch` (with Edit/Write aliases), MCP, and other local
function tools; hosted WebSearch is excluded. `write_stdin` does not rerun
PreToolUse. This is broader than the Control matcher and must be tested against
the exact installed release.

The model-visible envelope is:

```json
{
  "hookSpecificOutput": {
    "hookEventName":"PostToolUse",
    "additionalContext":"<complete source blocks>"
  }
}
```

Plain stdout is ignored for PostToolUse. `additionalContext` is added as extra
developer context. The same shape applies to `UserPromptSubmit` and
`SessionStart`, with the matching event name. `Stop` is a continuation/control
surface (`stop_hook_active`, `last_assistant_message` and continuation fields),
not a context-only delivery event.

Current official Codex docs support synchronous and `async: true` command
hooks. Async output is delivered at a later safe point, so it is unsuitable for
the first exact receipt boundary unless Workbench explicitly models that delay.
The current documented default is about 2,500 tokens per model-visible
hook-output message. A larger value spills to
`<temp_dir>/hook_outputs/<session_id>/<uuid>.txt` with a head/tail preview and
path; a positive `additionalContextLimit` can change that behavior and `0`
requests direct full context. This supersedes the legacy Control statement that
Codex had no provider-specific ceiling. Workbench should keep a smaller shared
budget and record whether the provider accepted direct bytes or spilled.

For Workbench v1 the necessary Codex events are `PostToolUse` for immediate
delivery and `UserPromptSubmit`/`SessionStart` for queued delivery. `Stop` can
scan/queue evidence but should not emit context. `PreToolUse` is available for
preflight decisions, not normal post-read delivery. Codex has no generic
provider-native Read callback; a transcript adapter is optional and must remain
strictly bounded, path-confined, and separately classified as inferred
evidence.

## Control adapter facts (source learning, not Workbench behavior)

The Control plugin manifests register:

- Claude `PreToolUse`, `PostToolUse`, and `PostToolUseFailure` matching
  `Bash|Read|Edit|MultiEdit|Write`; unfiltered `PostToolBatch`, `SessionStart`,
  and `Stop`; command timeout 30 seconds. See [Claude hook manifest](/home/ubuntu/phosphor/control/pkg/@agent-plugins/claude-support/hooks/hooks.json:1).
- Codex `PreToolUse` and `PostToolUse` matching `Bash|apply_patch`; unfiltered
  `UserPromptSubmit`, `SessionStart`, and `Stop`; command timeout 30 seconds.
  See [Codex hook manifest](/home/ubuntu/phosphor/control/pkg/@agent-plugins/codex-support/hooks/hooks.json:1).

The shared copied binding reads all stdin bytes with `readFileSync(0)`, decodes
fatal UTF-8, parses JSON, requires absolute `cwd`, and defaults absent
`session_id` to `unknown-session` at its locator boundary. It walks to a
sentinel checkout and requires an exact trusted root/device/inode grant; an
untrusted cwd exits silently without invoking the repo-local observer. See
[Claude binding](/home/ubuntu/phosphor/control/pkg/@agent-plugins/claude-support/src/internal/HookBinding.ts:44)
and [Codex binding](/home/ubuntu/phosphor/control/pkg/@agent-plugins/codex-support/src/internal/HookBinding.ts:44).

The Control decoder requires `session_id`, absolute `cwd`, non-empty
`hook_event_name`, optional `turn_id`, absolute-or-null `transcript_path`, and
optional `tool_use_id`; single-tool events add `tool_name` and object
`tool_input`; Claude batch adds `tool_calls` with the four fields above. Its
event/tool allowlists and inferred-vs-observed confidence are explicit in
[AgentPluginHookDecoding](/home/ubuntu/phosphor/control/pkg/@agent-plugins/agent-cli/src/internal/AgentPluginHookDecoding.ts:71).

Control emits only these delivery surfaces: Claude `PostToolBatch`; Codex
`PostToolUse`, `UserPromptSubmit`, and `SessionStart`, using the exact envelope
shape above. See [ProviderDelivery](/home/ubuntu/phosphor/control/pkg/@agent-plugins/agent-cli/src/internal/ProviderDelivery.ts:16).
Its current shared planning limit is 10,000 UTF-16 code units for Claude and
32,000 for Codex ([AgentPlugin](/home/ubuntu/phosphor/control/pkg/@agent-plugins/agent-cli/src/internal/AgentPlugin.ts:1371)).
The 32,000 value is Control policy, not an official Codex limit.

Control’s installer historically staged a self-contained plugin, wrote a
plugin-local Bun launcher, recorded canonical checkout root/device/inode grants,
and atomically swapped the payload. See [installer grant and atomic
staging](/home/ubuntu/phosphor/control/scripts/agent-plugin-support.mts:377).
Workbench setup is expected to install direct global Go hooks; no marketplace
registration belongs in the Workbench route unless a real vendor test proves it
is required.

## Retained and obsolete source guidance

Retain for the Workbench rewrite:

- One dependency-free global locator plus explicit checkout activation and an
  exact trusted workspace boundary ([Claude docs](/home/ubuntu/phosphor/control/docs/CLAUDE.md:12), [Codex docs](/home/ubuntu/phosphor/control/docs/CODEX.md:12)).
- Provider-owned DTO decoding, absolute cwd/session validation, fail-open
  trusted-hook failures, and no model-visible diagnostics containing raw
  transcript bodies.
- Claude batch coalescing and complete `additionalContext` source blocks;
  Codex per-tool serialization because it has no PostToolBatch.
- Delivery receipts only after successful exact envelope stdout, with duplicate
  safety and at-least-once behavior where the provider supplies no stronger
  identity ([harness ADR](/home/ubuntu/phosphor/control/docs/adrs/harness/2026-07-agent-hook-context-magnet-delivery.md:50)).
- No filesystem-MCP workaround for Codex’s missing generic Read hook; a bounded
  transcript evidence adapter remains optional.
- The legacy smoke-test oracle: retain provider/version provenance and a
  request/proxy/diagnostic artifact proving exact model-context bytes. See
  [Claude smoke requirements](/home/ubuntu/phosphor/control/docs/CLAUDE.md:188)
  and [Codex smoke requirements](/home/ubuntu/phosphor/control/docs/CODEX.md:259).

Obsolete or unsafe to copy as current Workbench facts:

- The `Checked: 2026-07-09` headers and any “currently” wording in the two
  Control docs are historical snapshots; current vendor docs have expanded
  events and changed limits.
- Control `docs/CODEX.md:89` says async hooks are unsupported. Current official
  Codex docs support async command hooks; Workbench must choose whether to defer
  them, not reject them as a vendor impossibility.
- Control `docs/CODEX.md:233` says Codex has no provider-specific context cap.
  Current docs document the roughly 2,500-token default and spill behavior.
- Control’s narrow Codex `Bash|apply_patch` matcher is an installed PoC choice,
  not the current official tool coverage. It cannot be used to claim generic
  current Codex coverage.
- The Control architecture’s `agentd` inbox, directed continuation, and durable
  messaging paths are explicitly outside this Workbench context integration.
  The accepted Workbench ADR also says the current PoC checkpoint is not yet
  the accepted event-fold/WAL target ([ADR](/home/ubuntu/phosphor/control/docs/adrs/harness/2026-07-agent-hook-context-magnet-delivery.md:168)).
- A local stdout receipt is not proof of provider admission; do not promote the
  Control architecture’s “successfully writes envelope” boundary into a real
  harness claim without the provider artifact.

## Proposed Go adapter boundary

These are proposed signatures only; no Go implementation was written in this
probe. Keep the raw DTO extensible because vendor fields evolve.

```go
type Harness string // "claude" | "codex"
type Event string
type Surface string

type Identity struct {
	Harness       Harness
	WorkspaceRoot string // validated before admission
	SessionID     string
	EpochKey      string // derived from reset/fork/compact evidence; never session ID alone
	TurnID        string // Codex; optional elsewhere
	PromptID      string // Claude current; optional
}

type ToolCall struct {
	Name       string
	UseID      string
	Input      json.RawMessage
	Response   json.RawMessage // preserve provider shape; Claude batch may be serialized content
}

type HookDTO struct {
	SessionID      string          `json:"session_id"`
	PromptID       string          `json:"prompt_id,omitempty"`
	TurnID         string          `json:"turn_id,omitempty"`
	TranscriptPath *string         `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	EventName      Event           `json:"hook_event_name"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse   json.RawMessage `json:"tool_response,omitempty"`
	ToolCalls      []ToolCall      `json:"tool_calls,omitempty"`
	Prompt         string          `json:"prompt,omitempty"`
	SessionSource  string          `json:"source,omitempty"`
	Raw            json.RawMessage `json:"-"`
}

func ReadHookStdin(r io.Reader, maxBytes int64) ([]byte, error)
func DecodeClaudeHook(context.Context, []byte) (HookDTO, error)
func DecodeCodexHook(context.Context, []byte) (HookDTO, error)
func NormalizeClaudeIdentity(HookDTO, string /* validated workspace */) (Identity, error)
func NormalizeCodexIdentity(HookDTO, string /* validated workspace */) (Identity, error)
func ClaudeDeliverySurface(HookDTO) (Surface, bool) // false means observe/queue only
func CodexDeliverySurface(HookDTO) (Surface, bool)  // false means observe/queue only
func EncodeClaudePostToolBatch(additionalContext string) ([]byte, error)
func EncodeCodexPostToolUse(additionalContext string) ([]byte, error)
func EncodeCodexUserPromptSubmit(additionalContext string) ([]byte, error)
func EncodeCodexSessionStart(additionalContext string) ([]byte, error)
```

`ReadHookStdin` must return a typed size error rather than truncate. The limit
is Workbench policy because no vendor stdin maximum was found. `Encode` must
validate that the envelope event matches the callback and must not emit plain
text. The runtime, not either adapter, owns audience locking, queue/WAL,
complete-block budgeting, receipt-after-write, and failure reporting.

## Safe isolated localhost admission route

This is the viable next route now that the exact Claude executable is available.
It was not run in this probe. It changes only throwaway directories, binds the
fixture server to loopback, uses a fake local API key, and uses no durable
messaging. The route requires a small test-only `harness-fixture-server` helper
to be added later; that helper is not present today.

The server contract is deliberately deterministic:

- bind only `127.0.0.1`, accept Anthropic Messages requests at `/v1/messages`
  and Responses requests at `/v1/responses`, and write each request body to a
  temp capture directory with auth headers redacted;
- on the first Claude request, return one assistant `tool_use` for the built-in
  `Read` tool targeting `README.md`; on the next request, return final text;
- on the first Codex request, return one Responses tool/function call for the
  executable tool actually offered by Codex (normally a shell/unified-exec call
  whose command is `cat README.md`); on the next request, return final text;
- never call the network or require a real credential. The server must keep the
  exact request JSON and a request ordinal so the second request is the
  admission oracle.

Generate a randomized marker and seed it into an isolated Workbench source
block/pending contribution. The assertion is that the captured second Claude
request contains the marker in the provider `system` content, or the captured
second Codex request contains it in the provider developer/instructions/input
content. The hook stdout envelope and a final model answer are supplementary
assertions only.

1. Create a disposable workspace fixture containing the Workbench sentinels,
   one small `README.md`, and a command that prints a unique marker. Record
   root/device/inode and provider versions. Do not grant or install it in the
   user’s real plugin state.
2. Use the driver’s exact Claude 2.1.260 binary under
   `/tmp/workbench-context-claude.8DYqHI`. Point the harness at a disposable
   `CLAUDE_CONFIG_DIR` and pass direct Workbench Go hook settings through
   `--settings`; no marketplace registration or legacy Control installer is
   part of this route:

   ```sh
   fixture_config=$(mktemp -d /tmp/workbench-claude-config.XXXXXX)
   fixture_root=/path/to/prepared/workbench-fixture
   capture_dir=$(mktemp -d /tmp/workbench-harness-capture.XXXXXX)
   guidance_marker="WB_ADMISSION_$(openssl rand -hex 16)"
   harness-fixture-server --listen 127.0.0.1:8787 \
     --capture "$capture_dir" --marker "$guidance_marker" \
     --protocols anthropic-messages,responses &
   server_pid=$!
   trap 'kill "$server_pid" 2>/dev/null || true' EXIT
   ```

   Write the direct hook entries to `fixture_settings` (the Workbench Go hook
   executable is the command for `PostToolBatch`, `SessionStart`, and `Stop`):

   ```sh
   fixture_settings="$fixture_config/settings.json"
   fixture_hook=/path/to/workbench-hook
   # Write fixture_settings with direct command-hook entries using fixture_hook.
   ```

   The fixture must already contain the Workbench sentinels and test files;
   `mktemp` is used only for disposable state. The exact Claude 2.1.260
   executable and its session-local flags are now known from its help output:

   ```sh
   PATH="/tmp/workbench-context-claude.8DYqHI/node_modules/.bin:$PATH" \
   CLAUDE_CONFIG_DIR="$fixture_config" \
   ANTHROPIC_API_KEY=fixture \
   ANTHROPIC_BASE_URL=http://127.0.0.1:8787 \
   /tmp/workbench-context-claude.8DYqHI/node_modules/.bin/claude \
     --settings "$fixture_settings" \
   --setting-sources empty \
   --permission-mode bypassPermissions \
   --allowedTools Read \
   --print --output-format stream-json \
   "Read README.md"
   ```

   `--settings`, `--setting-sources`, `--print`,
   `--output-format`, `--permission-mode`, and `--allowedTools` are present in
   the exact binary’s help. `ANTHROPIC_BASE_URL` is the standard endpoint
   override assumed by this fixture route; verify the server path once when
   the run starts. The driver baseline used `--setting-sources empty`, the
   exact executable, and `Read` only; retain that minimal route for admission
   proof. Do not use the global Claude shim. Capture the real PostToolBatch
   stdin and assert the `Read` call is present.
3. For Codex, use an isolated `CODEX_HOME` and a disposable fixture. Configure
   direct user hooks in its `hooks.json` and enable `features.hooks`; do not
   register a marketplace or plugin. Run the documented `codex exec` route with
   the fixture as cwd and read-only/no-approval settings. The 0.153.4 help confirms
   `CODEX_HOME` profile/config layering, `--config`, `--local-provider`,
   `--sandbox read-only`, `--ask-for-approval`, `--cd`, `--json`, and
   `--dangerously-bypass-hook-trust`. A temporary Responses provider config has
   this shape:

   ```sh
   codex_home=$(mktemp -d /tmp/workbench-codex-home.XXXXXX)
   mkdir -p "$codex_home"
   cat > "$codex_home/config.toml" <<'EOF'
   features.hooks = true
   model = "fixture"
   model_provider = "fixture"
   [model_providers.fixture]
   name = "loopback fixture"
   base_url = "http://127.0.0.1:8787/v1"
   wire_api = "responses"
   env_key = "OPENAI_API_KEY"
   EOF
   fixture_hook=/path/to/workbench-hook
   # Write "$codex_home/hooks.json" with direct PostToolUse,
   # UserPromptSubmit, SessionStart, and Stop command entries for fixture_hook.
   CODEX_HOME="$codex_home" OPENAI_API_KEY=fixture codex exec \
     --cd "$fixture_root" --sandbox read-only --ask-for-approval never \
     --dangerously-bypass-hook-trust --json \
     "Read README.md and run the fixture marker command"
   ```

   The config keys `features.hooks`, `model_provider`,
   `[model_providers.<id>]`, `base_url`, `wire_api = "responses"`, and `env_key`
   are the provider-config shape to confirm against the exact Codex release
   before executing. `--local-provider`
   is only for Ollama/LM Studio and is not the HTTP Responses fixture path.
   Do not use `--dangerously-bypass-hook-trust` outside this disposable run;
   prefer persisted trust in the temporary `CODEX_HOME` when available.
4. In each run capture: provider version, hook JSON stdin bytes (redacted to
   fixture data), exit code, stdout bytes, stderr, hook event/identity, and the
   exact provider request/proxy/diagnostic artifact containing the unique source
   block. Run under/at/over the Claude 10,000-character limit and under/over the
   Codex documented output threshold; verify over-limit behavior is recorded as
   spill, not falsely receipted as direct complete context. Repeat a hook with
   the same invocation identity to test duplicate suppression where the provider
   supplies an identity. Do **not** restart and claim pending durability: the
   accepted ADR does not promise restart persistence for an interrupted
   in-process handoff. If interrupted before provider output/receipt, disclose
   any lost pending state, allow later hooks to recruit/re-admit it, and assert
   no receipt was created for guidance that was not emitted (no false
   suppression).

The direct hook fixture is useful before a model run, but is not the real-harness
oracle: the final acceptance assertion is the provider-owned system reminder
(Claude) or developer context (Codex), captured with version-pinned evidence.

## Exact commands and observed outputs

Commands run from the relevant repository roots:

```sh
bb status
git status --short --branch
bun ./tools/docs-search/run.mts query --help
bun ./tools/docs-search/run.mts query 'What are the actual Claude and Codex hook payloads and additionalContext delivery boundaries?'
claude --version 2>&1
claude --help 2>&1
codex --version 2>&1
codex hooks --help 2>&1
codex plugin list --json 2>&1
codex plugin marketplace list 2>&1
bun --version 2>&1
node --version 2>&1
mise --version 2>&1
/tmp/workbench-context-claude.8DYqHI/node_modules/.bin/claude --help 2>&1
codex exec --help 2>&1
# Driver-run, not rerun by this probe:
python /tmp/workbench-context-admission/probe.py
```

Observed version/results:

```text
global claude shim: Error: claude native binary not installed ...
isolated Claude 2.1.260 executable: launches; --help shows --settings and --setting-sources
codex-cli 0.153.4
codex hooks: unrecognized command; root help printed
codex exec --help: shows CODEX_HOME config/profile layering, --cd, --sandbox,
  --ask-for-approval, --dangerously-bypass-hook-trust, and --json
bun 1.3.14
node v22.21.1
mise 2026.9.1 linux-x64
driver Claude baseline: exit 0; 2 actual localhost Anthropic SSE requests;
  deterministic Read only; PostToolBatch hook ran; randomized marker present
  in request 2; artifacts under /tmp/workbench-context-admission/
```

The relevant source/evidence paths are:

- `/home/ubuntu/phosphor/control/pkg/@agent-plugins/claude-support/hooks/hooks.json`
- `/home/ubuntu/phosphor/control/pkg/@agent-plugins/codex-support/hooks/hooks.json`
- `/home/ubuntu/phosphor/control/pkg/@agent-plugins/agent-cli/src/internal/AgentPluginHookDecoding.ts`
- `/home/ubuntu/phosphor/control/pkg/@agent-plugins/agent-cli/src/internal/ProviderDelivery.ts`
- `/home/ubuntu/phosphor/control/pkg/@agent-plugins/claude-support/src/internal/HookBinding.ts`
- `/home/ubuntu/phosphor/control/pkg/@agent-plugins/codex-support/src/internal/HookBinding.ts`
- `/home/ubuntu/phosphor/control/scripts/agent-plugin-support.mts`
- `/home/ubuntu/phosphor/control/docs/CLAUDE.md`
- `/home/ubuntu/phosphor/control/docs/CODEX.md`
- `/home/ubuntu/phosphor/control/docs/adrs/harness/2026-07-agent-hook-context-magnet-delivery.md`
- `/home/ubuntu/phosphor/control/pkg/@agent-plugins/agent-cli/ARCHITECTURE.md`

Only this evidence file was written in Workbench. The legacy Control docs and
implementation remain untouched.
