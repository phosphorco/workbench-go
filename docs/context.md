# Workbench context

Workbench context is bounded, opt-in enrichment for Claude Code and Codex
hooks. It supplies a small, attributable piece of project guidance to the host
when observed work makes that guidance relevant. The host remains responsible
for the agent, the model request, and whether injected context is admitted.

The accepted design is recorded in the [context ADR](adr/2026-09-wokbench-context.md)
and its operational boundaries are in the [runtime contract](../.context/workbench-context/runtime-contract.md).
This page describes the user-facing product; the [CLI implementation](../cmd/workbench/context_cli.go),
[hook adapter](../cmd/workbench/context_runtime.go), and
[activation loader](../internal/contextconfig/config.go) are the source of
truth for details that must evolve with the implementation.

## Install once, activate deliberately

Install the Workbench executable once, then reconcile the host hook settings:

```sh
workbench context setup --harness both
```

Setup writes the Claude settings and Codex configuration selected by their
normal locations (or by explicit flags). It installs a quoted absolute
`workbench context hook` command for the supported events and preserves
unrelated authored settings. It does not opt a project in and does not write a
project file. Use `--dry-run` to inspect the planned changes, or provide
`--executable`, `--claude-settings`, `--codex-home`, and `--codex-config` for an
isolated installation. Running setup again is intended to be idempotent.

Activation is a separate, explicit choice in either a directory or the user
home configuration. The shortest project activation is:

```sh
workbench context init --path "$PWD"
```

This creates or updates `$PWD/.workbench/context.json`, setting `optIn` to
`true`, adding `includeChildren: true` when absent, and adding
`schemaVersion: 1` when absent. Existing JSON fields are retained; formatting
may be normalized. To opt in only one directory, set `includeChildren` to
`false` after initialization. A nearer `.workbench/context.json` wins while
walking toward the filesystem root, so a nearer `optIn: false` can withdraw an
ancestor declaration.

The project file has this deliberately small authored shape:

```json
{
  "schemaVersion": 1,
  "optIn": true,
  "includeChildren": true
}
```

The home file defaults to `XDG_CONFIG_HOME/workbench/context.json` (falling
back to `$HOME/.config/workbench/context.json`). It can name explicit
directory scopes without rewriting project files:

```json
{
  "schemaVersion": 1,
  "scopes": [
    {"root": "/work/repo", "optIn": true, "includeChildren": true}
  ],
  "exclusions": [
    {"root": "/work/repo/vendor", "includeChildren": true}
  ]
}
```

An enabled project with no `providers` field uses the builtin `ai-context`
provider. An explicit `"providers": []` disables providers for that scope.
Home settings are machine-wide limits and optional directory declarations;
they are not a replacement for deliberate project activation.

## Audience and profile are different

An audience is the receiving context identity supplied by the host adapter. It
often corresponds to a Claude or Codex session, but a session identifier alone
is not enough: the runtime also needs epoch evidence to distinguish a reset,
fork, compaction, or unknown continuity. Its identity and epoch determine
whether a particular offer is new, already handed off, or belongs to an older
context. A reset, fork, or compaction supplied by the host starts a new epoch;
unknown continuity is handled conservatively.
A `SessionStart` reported as `resume` is treated as a fresh epoch as well. This
deliberate conservatism clears prior receipts so resumed sessions can redeliver
guidance when runtime continuity cannot be proved.

A profile is the bounded, time-sensitive selection context used to choose
facts and guidance: role, guidance set, preferences, and provider facts. A
profile provider reports profile facts and relevance; it does not mint or
reinterpret audience identity. A profile revision can change while the
audience remains the same. Delivery is bound to both identities, so a profile
change withdraws stale pending content rather than confirming content that no
longer matches its selection.

The adapter emits an offer first and confirms it only after the exact stdout
write succeeds. A repeated observation with the same source, content, audience,
and epoch is suppressed. A process crash or unknown continuity can therefore
cause a later duplicate; the system does not claim exactly-once delivery across
runtime loss.

## Builtin `ai-context` guidance

The builtin contributor reads bounded frontmatter from `ai-context.md` files.
The markdown body outside the frontmatter delimiters is ignored. YAML and TOML
frontmatter are supported. A minimal YAML file is:

```yaml
---
root: true
docs:
  - files: ["README.md"]
    message: "Keep the package boundary visible."
commands:
  - files: ["cmd/**/*.go"]
    command: "go test ./cmd/..."
    label: "verify"
---
```

`docs` rules may use `files`, `mentions`, `include`, and `message`.
`commands` rules may use `files`, `mentions`, `command`, `label`, and `cwd`.
Files and mentions recruit a rule from observed resources or selectors.
Includes are bounded file material; command rules are rendered as guidance and
are never executed by Workbench. `root: true` stops the ancestor manifest
walk. A rule with no selector is eligible for the applicable manifest.

The implementation bounds manifest, include, mention, selector, body, reason,
resource-path, and total output bytes. The exact complete contribution can be
larger than the explanation sample, but no explanation sample exceeds 10,000
UTF-8 bytes. Large or invalid inputs are skipped or explained as bounded
failures; they are not silently presented as complete guidance. See the
[builtin provider](../internal/contextprovider/builtin.go) for the enforced
limits.

## Executable providers

An enabled scope may add an executable provider. Providers run as same-user
child processes in the scope's canonical root. The provider owns its opaque
`settings` value and its facts or contribution bodies; Workbench owns scope,
audience, configuration, profile, content, and source revisions.

This is the exact configuration shape; use an absolute executable path:

```json
{
  "id": "my-guidance",
  "kind": "executable",
  "executable": "/absolute/path/to/provider",
  "arguments": [],
  "settings": {"mode": "review"},
  "capabilities": ["profile", "contribute"],
  "limits": {
    "deadlineMs": 500,
    "maxResponseBytes": 262144,
    "maxFacts": 32,
    "maxContributions": 64,
    "maxBodyBytes": 262144
  }
}
```

The client sends one newline-delimited JSON-RPC 2.0 request and expects one
newline-delimited response. Initialization is:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"resource":{"provider":"my-guidance","scope":{"id":"scope:...","authority":"project","canonicalRoot":"/repo","configDigest":"sha256:..."},"configDigest":"sha256:..."},"settings":{"mode":"review"},"supportedCapabilities":["contribute"],"limits":{"deadlineMs":500,"maxResponseBytes":262144,"maxFacts":32,"maxContributions":64,"maxBodyBytes":262144}}}
```

The response is a JSON-RPC result containing `accepted`, the advertised
capabilities, and provider `reasons`. `audience.profile` is available only
when `profile` was advertised and receives a typed `ProfileRequest`; it returns
typed `ProfileResponse` facts and reasons. `context.contribute` is available
only when `contribute` was advertised and receives a typed
`ContributionRequest`; it returns typed contributions and reasons. A
contribution may author its stable slot, source, body, and reasons. Workbench
hydrates the provider, scope, profile, content, config, and source-revision
identity and independently checks semantic counts, bytes, UTF-8, scope, and
digest bounds. The builtin provider is separate and has no builtin profile.

On shutdown the client sends the typed `shutdown` request and then closes the
bounded process transport. A malformed response, mismatched ID, provider
error, timeout, or limit violation affects that provider contribution and is
reported as bounded evidence; it does not block the host hook or healthy
contributors.

## What, when, and why commands

All read commands default to the current directory. Add `--path DIR` to inspect
another directory, and add `--json` for structured output. The grammar is
intentional; there are no project-init or inspection aliases.

```sh
workbench context status --path "$PWD"
workbench context history --path "$PWD" --json
workbench context inspect contribution 123 --path "$PWD"
workbench context inspect turn turn-123 --path "$PWD"
workbench context explain profile sha256:... --path "$PWD"
workbench context cache status
workbench context cache clear
```

`status` reports resolved activation and, when enabled, checks an existing
runtime without starting one. `history` answers what happened in a bounded
page. `inspect contribution` combines source, content sample, reasons, and
delivery outcomes. `inspect turn` groups retained observations and outcomes.
`explain profile` reports profile evidence and transitions. `cache status`
reports the explanation generation, disk usage, blocks, and known gaps.
`cache clear` starts a new explanation-history generation and does not clear
delivery suppression, pending offers, or the host conversation.

The raw hook command is normally installed by `setup`, but it can be exercised
directly with the host's JSON payload:

```sh
printf '%s\n' "$CLAUDE_HOOK_JSON" |
  workbench context hook --harness claude
printf '%s\n' "$CODEX_HOOK_JSON" |
  workbench context hook --harness codex
```

The daemon can also be run explicitly for diagnostics:

```sh
workbench context serve
```

Setup is configuration, not host trust. In Codex app-server inspection, a
reconciled hook can be reported as `enabled: true` while its `trustStatus` is
`untrusted`; the host will not automatically deliver the hook until the user
reviews and trusts the installed command through the native `/hooks` flow. The
Workbench feature remains host-agnostic and does not invent a trust command.
Claude and Codex native host/model admission are therefore separate proofs from
the Go hook's local stdout behavior.

Path, socket, lock, cache, deadline, input, and wire-bound overrides are
available as flags and as the `WORKBENCH_CONTEXT_*` variables shown by
`workbench context --help`. An explicit bound can only narrow the configured
policy; oversized requests are rejected.

## Cache and privacy boundaries

The explanation cache is disposable evidence, not authoritative delivery state.
It stores decision records, typed reasons, identities, outcomes, offsets, and a
bounded sample. It never stores prompts, assistant messages, complete tool
results, or a transcript. The sample limit is 10,000 UTF-8 bytes per
contribution; small bodies may fit in full, while larger bodies retain bounded
head, middle, and tail excerpts with original size and omitted-byte counts.

The default cache cap is 5,000,000,000 decimal bytes. It includes segment and
index overhead; oldest blocks rotate out and queries disclose rotated, dropped,
corrupt, or unreadable gaps. History queries have bounded record, page-byte,
and scan-byte budgets. A bare `history --json` uses the live store policy for
its byte budgets, so a smaller configured cache remains usable; pass explicit
`--limit`, `--max-bytes`, or `--max-scan-bytes` only when a smaller bounded page
is desired.

Pending offers, receipts, and suppression are runtime delivery state. They are
not made durable by the explanation cache and may be lost on runtime restart;
inspection discloses the resulting generation or evidence gap. Cache failure
is diagnostic and does not by itself prevent an enabled hook from delivering
context.

## Harness coverage and bounded hooks

Claude setup covers `PostToolBatch`, `PostToolUse`, `PostToolUseFailure`,
`SessionStart`, and `Stop`; Claude delivery uses the command hook's
`hookSpecificOutput` envelope with `hookEventName: "PostToolBatch"` and an
`additionalContext` string capped at 10,000 UTF-8 bytes. Codex setup covers
`PostToolUse`, `UserPromptSubmit`, and `SessionStart`; delivery uses the same
envelope with the corresponding event name and a 32,000-byte adapter limit.

The Go adapter recognizes bounded evidence such as a Bash `cat README.md` and
its structured response. Unknown or ambiguous shell commands remain unknown:
Workbench does not claim a file was read or fabricate evidence. Hook stdout
proves only the local handoff to the host adapter. It does not prove that the
native host accepted the envelope or that a model request contained the
context; those are separate host/model tests.

The inactive path is intentionally silent: it does not start the runtime,
providers, or create project, socket, lock, or cache artifacts. Enabled hooks
have a default two-second whole-hook bound, bounded stdin and JSON decoding,
bounded provider and wire I/O, and bounded output writes. Inactive, conflicting,
invalid, and failed input is fail-open with empty stdout; diagnostics, when
available, go to stderr. A failed stdout write cannot create a confirmed
receipt.

These bounds are product behavior, not performance promises for every machine.
The acceptance lane measures representative cold concurrency and cleanup and
keeps inactive smoke timing separate from the proposed p95 target. The native
Claude/Codex model-admission proof and independent runtime lifecycle proof are
separate acceptance responsibilities.

For exact schemas and rationale, use the [accepted ADR](adr/2026-09-wokbench-context.md),
[runtime contract](../.context/workbench-context/runtime-contract.md),
[provider API](../internal/contextapi/contribution.go), and
[trace implementation](../internal/contexttrace/contexttrace.go).
