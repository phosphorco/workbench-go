package contexthook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

const claudeFixture = `{"session_id":"64cae29b-d511-4446-92cc-7f4e76e827a2","transcript_path":"/tmp/claude.jsonl","cwd":"/tmp/workbench-context-admission/project","prompt_id":"65bd21a2-cb36-43ce-9240-721013f0aa50","permission_mode":"default","effort":{"level":"high"},"hook_event_name":"PostToolBatch","tool_calls":[{"tool_name":"Read","tool_input":{"file_path":"/tmp/workbench-context-admission/project/README.md"},"tool_use_id":"toolu_fixture_read","tool_response":"1\\tA fixture file.\\n2\\t"}]}`

const codexFixture = `{"session_id":"01a0832c-f072-7b83-891f-5f3d82d4d272","turn_id":"01a0832c-f07c-7020-b2c0-b24cbc3e17be","transcript_path":null,"cwd":"/tmp/workbench-context-admission/codex/project","hook_event_name":"PostToolUse","model":"gpt-5.6","permission_mode":"bypassPermissions","tool_name":"Bash","tool_input":{"command":"cat README.md"},"tool_response":{"stdout":"Codex fixture.\\n","exit_code":0},"tool_use_id":"call_fixture"}`

func TestDecodeCapturedClaudeShapeAndRoute(t *testing.T) {
	hook, err := DecodeClaudeHook(context.Background(), []byte(claudeFixture))
	if err != nil {
		t.Fatal(err)
	}
	if hook.Harness != contextapi.HarnessClaudeCode || hook.EventName != "PostToolBatch" || len(hook.ToolCalls) != 1 {
		t.Fatalf("decoded Claude hook = %#v", hook)
	}
	if hook.ToolCalls[0].Name != "Read" || hook.ToolCalls[0].UseID != "toolu_fixture_read" {
		t.Fatalf("decoded Claude call = %#v", hook.ToolCalls[0])
	}
	route, err := DecodeRoute(contextapi.HarnessClaudeCode, []byte(claudeFixture), DefaultInputLimits())
	if err != nil {
		t.Fatal(err)
	}
	if route.SessionID != hook.SessionID || route.CWD != hook.CWD || route.EventName != hook.EventName {
		t.Fatalf("route = %#v", route)
	}
}

func TestDecodeCapturedCodexShape(t *testing.T) {
	hook, err := DecodeCodexHook(context.Background(), []byte(codexFixture))
	if err != nil {
		t.Fatal(err)
	}
	if hook.Harness != contextapi.HarnessCodex || hook.TurnID == "" || hook.ToolName != "Bash" {
		t.Fatalf("decoded Codex hook = %#v", hook)
	}
	if hook.TranscriptPath != nil {
		t.Fatalf("expected null transcript path, got %q", *hook.TranscriptPath)
	}
}

func TestDecodeCopiesCallerBytesAndAllowsProviderFields(t *testing.T) {
	input := []byte(`{"session_id":"session","cwd":"/repo","hook_event_name":"PostToolUse","vendor":{"new_field":"accepted"},"tool_name":"Read","tool_input":{"file_path":"README.md"}}`)
	hook, err := DecodeCodexHook(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = ' '
	if string(hook.ToolInput) != `{"file_path":"README.md"}` {
		t.Fatalf("decoded data aliases caller: input=%q", hook.ToolInput)
	}
}

func TestReadHookStdinRejectsOversizeWithoutTruncation(t *testing.T) {
	data, err := ReadHookStdin(bytes.NewReader([]byte("123456")), 5)
	if err == nil || !errors.Is(err, ErrInputLimit) {
		t.Fatalf("oversize error = %v", err)
	}
	if data != nil {
		t.Fatalf("oversize returned data %q", data)
	}
	data, err = ReadHookStdin(bytes.NewReader([]byte("12345")), 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("bounded read = %q, %v", data, err)
	}
}

func TestDecodeRejectsMalformedMissingIdentityAndBounds(t *testing.T) {
	cases := []struct {
		name string
		data string
		want error
	}{
		{name: "missing session", data: `{"cwd":"/repo","hook_event_name":"PostToolUse"}`, want: ErrMissingIdentity},
		{name: "relative cwd", data: `{"session_id":"s","cwd":"repo","hook_event_name":"PostToolUse"}`, want: ErrMalformedInput},
		{name: "bad JSON", data: `{"session_id":`, want: ErrMalformedInput},
		{name: "deep JSON", data: `{"session_id":"s","cwd":"/repo","hook_event_name":"x","x":[[[[[1]]]]]} `, want: ErrInputLimit},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultInputLimits()
			if test.name == "deep JSON" {
				limits.MaxJSONDepth = 3
			}
			_, err := DecodeRoute(contextapi.HarnessCodex, []byte(test.data), limits)
			if err == nil || !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}
}

func enabledActivation(root string) contextapi.ActivationResult {
	return contextapi.ActivationResult{
		State: contextapi.ActivationEnabled,
		Scope: contextapi.ScopeIdentity{ID: "scope:test", Authority: contextapi.ScopeAuthorityProject, CanonicalRoot: root, ConfigDigest: "sha256:test"},
	}
}

func TestNormalizeClaudeReadProducesHostObservationOpportunity(t *testing.T) {
	hook, err := DecodeClaudeHook(context.Background(), []byte(claudeFixture))
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeClaudeHook(hook, enabledActivation("/tmp/workbench-context-admission/project"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Identity.Audience.ID != "claude-code:64cae29b-d511-4446-92cc-7f4e76e827a2" || normalized.Identity.Audience.Epoch != 0 || normalized.Identity.Audience.Continuity != contextapi.ContinuityUnknown {
		t.Fatalf("identity = %#v", normalized.Identity)
	}
	if normalized.Host.Turn.State != contextapi.TurnUnknown || normalized.Opportunity.Surface != contextapi.DeliveryClaudeContext || !normalized.Opportunity.Available {
		t.Fatalf("host/opportunity = %#v / %#v", normalized.Host, normalized.Opportunity)
	}
	if normalized.Opportunity.ID != "toolu_fixture_read" || normalized.Opportunity.Invocation.ID != "toolu_fixture_read" {
		t.Fatalf("opportunity identity = %#v", normalized.Opportunity)
	}
	if len(normalized.Observation.Resources) != 1 {
		t.Fatalf("resources = %#v; reasons=%#v", normalized.Observation.Resources, normalized.Reasons)
	}
	resource := normalized.Observation.Resources[0]
	if resource.Path != "README.md" || resource.Confidence != contextapi.ConfidenceObserved || resource.Outcome != contextapi.ResourceOutcomeUnknown || resource.Operation != contextapi.ResourceRead {
		t.Fatalf("resource = %#v", resource)
	}
}

func TestNormalizeCodexShellAndTurn(t *testing.T) {
	hook, err := DecodeCodexHook(context.Background(), []byte(codexFixture))
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeCodexHook(hook, enabledActivation("/tmp/workbench-context-admission/codex/project"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Host.Turn.State != contextapi.TurnKnown || normalized.Host.Turn.ID != contextapi.TurnID(hook.TurnID) {
		t.Fatalf("turn = %#v", normalized.Host.Turn)
	}
	if len(normalized.Observation.Resources) != 1 {
		t.Fatalf("resources = %#v; reasons=%#v", normalized.Observation.Resources, normalized.Reasons)
	}
	resource := normalized.Observation.Resources[0]
	if resource.Path != "README.md" || resource.Confidence != contextapi.ConfidenceInferred || resource.Outcome != contextapi.ResourceResolved {
		t.Fatalf("resource = %#v", resource)
	}
}

func TestNativeFailureAndUnknownOutcomesStayDistinct(t *testing.T) {
	failed := HookDTO{Harness: contextapi.HarnessClaudeCode, SessionID: "s", CWD: "/repo", EventName: "PostToolUseFailure", ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"x.go"}`)}
	normalized, err := NormalizeClaudeHook(failed, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.Observation.Resources[0].Outcome; got != contextapi.ResourceFailed {
		t.Fatalf("failure outcome = %q", got)
	}
	unknown := failed
	unknown.EventName = "PostToolUse"
	unknown.ToolResponse = json.RawMessage(`{}`)
	normalized, err = NormalizeClaudeHook(unknown, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.Observation.Resources[0].Outcome; got != contextapi.ResourceOutcomeUnknown {
		t.Fatalf("unknown outcome = %q", got)
	}
	whitespace := unknown
	whitespace.ToolResponse = json.RawMessage("  {\"exit_code\":1}  ")
	normalized, err = NormalizeClaudeHook(whitespace, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.Observation.Resources[0].Outcome; got != contextapi.ResourceFailed {
		t.Fatalf("whitespace-wrapped failure outcome = %q", got)
	}
	malformed := unknown
	malformed.ToolResponse = json.RawMessage(`{"exit_code":`)
	normalized, err = NormalizeClaudeHook(malformed, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.Observation.Resources[0].Outcome; got != contextapi.ResourceOutcomeUnknown {
		t.Fatalf("malformed response outcome = %q", got)
	}
}

func TestUnsupportedScalarResponsesRemainUnknown(t *testing.T) {
	for _, response := range []string{`"text result"`, `42`, `true`} {
		t.Run(response, func(t *testing.T) {
			hook := HookDTO{
				Harness: contextapi.HarnessCodex, SessionID: "s", CWD: "/repo",
				EventName: "PostToolUse", ToolName: "Read",
				ToolInput:    json.RawMessage(`{"file_path":"README.md"}`),
				ToolResponse: json.RawMessage(response),
			}
			normalized, err := NormalizeCodexHook(hook, enabledActivation("/repo"), time.Unix(10, 0))
			if err != nil {
				t.Fatal(err)
			}
			if got := normalized.Observation.Resources[0].Outcome; got != contextapi.ResourceOutcomeUnknown {
				t.Fatalf("scalar %s outcome = %q, want %q", response, got, contextapi.ResourceOutcomeUnknown)
			}
		})
	}
}

func readHookFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read hook fixture %q: %v", name, err)
	}
	return data
}

func TestNativeOutcomeEnvelopesThroughPublicAdapters(t *testing.T) {
	tests := []struct {
		name    string
		harness contextapi.Harness
		fixture string
		want    contextapi.ResourceOutcome
	}{
		{name: "Codex explicit success", harness: contextapi.HarnessCodex, fixture: "codex-post-tool-use-success.json", want: contextapi.ResourceResolved},
		{name: "Codex explicit failure", harness: contextapi.HarnessCodex, fixture: "codex-post-tool-use-failure.json", want: contextapi.ResourceFailed},
		{name: "Codex captured native success scalar is unknown", harness: contextapi.HarnessCodex, fixture: "codex-live-captured-success.json", want: contextapi.ResourceOutcomeUnknown},
		{name: "Codex captured native failed read is unknown", harness: contextapi.HarnessCodex, fixture: "codex-live-captured-failure.json", want: contextapi.ResourceOutcomeUnknown},
		{name: "Codex unsupported scalar is unknown", harness: contextapi.HarnessCodex, fixture: "codex-post-tool-use-failed-scalar.json", want: contextapi.ResourceOutcomeUnknown},
		{name: "Codex unknown object is unknown", harness: contextapi.HarnessCodex, fixture: "codex-post-tool-use-unknown.json", want: contextapi.ResourceOutcomeUnknown},
		{name: "Codex malformed envelope is unknown", harness: contextapi.HarnessCodex, fixture: "codex-post-tool-use-malformed.json", want: contextapi.ResourceOutcomeUnknown},
		{name: "Codex failure takes precedence over success", harness: contextapi.HarnessCodex, fixture: "codex-post-tool-use-ambiguous.json", want: contextapi.ResourceFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := readHookFixture(t, test.fixture)
			var hook HookDTO
			var err error
			if test.harness == contextapi.HarnessCodex {
				hook, err = DecodeCodexHook(context.Background(), data)
			} else {
				hook, err = DecodeClaudeHook(context.Background(), data)
			}
			if err != nil {
				t.Fatal(err)
			}
			var normalized Normalized
			if test.harness == contextapi.HarnessCodex {
				normalized, err = NormalizeCodexHook(hook, enabledActivation(hook.CWD), time.Unix(10, 0))
			} else {
				normalized, err = NormalizeClaudeHook(hook, enabledActivation(hook.CWD), time.Unix(10, 0))
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(normalized.Observation.Resources) != 1 {
				t.Fatalf("resources = %#v; reasons=%#v", normalized.Observation.Resources, normalized.Reasons)
			}
			if got := normalized.Observation.Resources[0].Outcome; got != test.want {
				t.Fatalf("outcome = %q, want %q; resource=%#v", got, test.want, normalized.Observation.Resources[0])
			}
		})
	}
}

func TestClaudeBatchPreservesPerResourceOutcomes(t *testing.T) {
	data := readHookFixture(t, "claude-post-tool-batch-outcomes.json")
	hook, err := DecodeClaudeHook(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeClaudeHook(hook, enabledActivation("/tmp/workbench-context-admission/project"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Observation.Resources) != 2 {
		t.Fatalf("resources = %#v; reasons=%#v", normalized.Observation.Resources, normalized.Reasons)
	}
	want := []struct {
		path    string
		outcome contextapi.ResourceOutcome
	}{
		{path: "README.md", outcome: contextapi.ResourceResolved},
		{path: "missing-from-live-matrix.txt", outcome: contextapi.ResourceFailed},
	}
	for index, expected := range want {
		resource := normalized.Observation.Resources[index]
		if resource.Path != expected.path || resource.Outcome != expected.outcome {
			t.Fatalf("resource[%d] = %#v, want path=%q outcome=%q", index, resource, expected.path, expected.outcome)
		}
	}
}

func TestCapturedClaudeBatchFailureDoesNotBecomeSuccess(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    contextapi.ResourceOutcome
	}{
		{name: "controlled native success scalar batch is unknown", fixture: "claude-controlled-native-post-tool-batch-success.json", want: contextapi.ResourceOutcomeUnknown},
		{name: "controlled native failed batch", fixture: "claude-controlled-native-post-tool-batch-failure.json", want: contextapi.ResourceOutcomeUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hook, err := DecodeClaudeHook(context.Background(), readHookFixture(t, test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			normalized, err := NormalizeClaudeHook(hook, enabledActivation(hook.CWD), time.Unix(10, 0))
			if err != nil {
				t.Fatal(err)
			}
			if len(normalized.Observation.Resources) != 1 {
				t.Fatalf("resources = %#v; reasons=%#v", normalized.Observation.Resources, normalized.Reasons)
			}
			if got := normalized.Observation.Resources[0].Outcome; got != test.want {
				t.Fatalf("outcome = %q, want %q; resource=%#v", got, test.want, normalized.Observation.Resources[0])
			}
		})
	}
}

func TestLifecycleTransitionsAreExplicitAndOrdinaryHooksUnknown(t *testing.T) {
	cases := []struct {
		source string
		event  Event
		want   contextapi.AudienceTransitionKind
	}{
		{source: "startup", event: "SessionStart", want: contextapi.TransitionSessionStart},
		{source: "clear", event: "SessionStart", want: contextapi.TransitionReset},
		{source: "fork", event: "SessionStart", want: contextapi.TransitionFork},
		{source: "compact", event: "SessionStart", want: contextapi.TransitionCompact},
		{source: "", event: "PostToolUse", want: ""},
	}
	for _, test := range cases {
		hook := HookDTO{Harness: contextapi.HarnessCodex, SessionID: "s", CWD: "/repo", EventName: test.event, SessionSource: test.source, TurnID: "turn"}
		normalized, err := NormalizeCodexHook(hook, enabledActivation("/repo"), time.Unix(10, 0))
		if err != nil {
			t.Fatal(err)
		}
		if normalized.Transition.Kind != test.want {
			t.Fatalf("source %q event %q transition = %#v", test.source, test.event, normalized.Transition)
		}
		if test.want != "" && normalized.Transition.Current.Epoch != 0 {
			t.Fatalf("transition synthesized epoch: %#v", normalized.Transition)
		}
	}
}

func TestStaticShellParsingIsQuotedAndConservative(t *testing.T) {
	hook := HookDTO{Harness: contextapi.HarnessCodex, SessionID: "s", CWD: "/repo", EventName: "PostToolUse", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"cat 'src/file name.go' # rg fake"}`), ToolResponse: json.RawMessage(`{"exit_code":0}`)}
	normalized, err := NormalizeCodexHook(hook, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Observation.Resources) != 1 || normalized.Observation.Resources[0].Path != "src/file name.go" {
		t.Fatalf("quoted command resources = %#v", normalized.Observation.Resources)
	}
	echo := hook
	echo.ToolInput = json.RawMessage(`{"command":"echo \"cat README.md\""}`)
	normalized, err = NormalizeCodexHook(echo, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Observation.Resources) != 0 || len(normalized.Observation.Selectors) != 0 {
		t.Fatalf("echo text was recognized as shell attention: %#v / %#v", normalized.Observation.Resources, normalized.Observation.Selectors)
	}
	search := hook
	search.ToolInput = json.RawMessage(`{"command":"rg -F 'needle here' -g '*.go' src"}`)
	normalized, err = NormalizeCodexHook(search, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Observation.Selectors) != 2 || normalized.Observation.Selectors[0].Interpretations[0] != contextapi.SelectorKeyword || normalized.Observation.Selectors[1].Interpretations[0] != contextapi.SelectorGlob {
		t.Fatalf("search selectors = %#v", normalized.Observation.Selectors)
	}
	custom := hook
	custom.ToolInput = json.RawMessage(`{"command":"/tmp/cat README.md"}`)
	normalized, err = NormalizeCodexHook(custom, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Observation.Resources) != 0 {
		t.Fatalf("arbitrary executable path was treated as native cat: %#v", normalized.Observation.Resources)
	}
	ambiguous := hook
	ambiguous.ToolInput = json.RawMessage(`{"command":"cd sub && cat README.md"}`)
	normalized, err = NormalizeCodexHook(ambiguous, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Observation.Resources) != 0 {
		t.Fatalf("ambiguous cwd command produced a resource: %#v", normalized.Observation.Resources)
	}
}

func TestShellWordLimitReturnsErrorWithoutPartialObservation(t *testing.T) {
	input := []byte(`{"session_id":"s","cwd":"/repo","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"cat one two"},"tool_response":{"exit_code":0}}`)
	limits := DefaultInputLimits()
	limits.MaxShellWords = 2
	hook, err := DecodeCodexHookWithLimits(context.Background(), input, limits)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NormalizeCodexHook(hook, enabledActivation("/repo"), time.Unix(10, 0))
	if err == nil || !errors.Is(err, ErrInputLimit) {
		t.Fatalf("shell word limit error = %v", err)
	}
}

func TestDecodeShellByteLimitFailsBeforeNormalization(t *testing.T) {
	input := []byte(`{"session_id":"s","cwd":"/repo","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"cat README.md"}}`)
	limits := DefaultInputLimits()
	limits.MaxShellBytes = 5
	if _, err := DecodeCodexHookWithLimits(context.Background(), input, limits); err == nil || !errors.Is(err, ErrInputLimit) {
		t.Fatalf("shell byte limit error = %v", err)
	}
}

func TestTraversalIsRejectedAndPatchPathsAreBounded(t *testing.T) {
	hook := HookDTO{Harness: contextapi.HarnessCodex, SessionID: "s", CWD: "/repo", EventName: "PostToolUse", ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"../../secret"}`), ToolResponse: json.RawMessage(`"data"`)}
	normalized, err := NormalizeCodexHook(hook, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Observation.Resources) != 0 || len(normalized.Reasons) == 0 {
		t.Fatalf("traversal result = %#v reasons=%#v", normalized.Observation.Resources, normalized.Reasons)
	}
	patch := hook
	patch.ToolName = "apply_patch"
	patch.ToolInput = json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: dir/file.go\n@@\n-old\n+new\n*** End Patch"}`)
	normalized, err = NormalizeCodexHook(patch, enabledActivation("/repo"), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Observation.Resources) != 1 || normalized.Observation.Resources[0].Operation != contextapi.ResourceWrite {
		t.Fatalf("patch resources = %#v", normalized.Observation.Resources)
	}
}

func TestExactEnvelopesAndUTF8Budgets(t *testing.T) {
	encoded, err := EncodeClaudePostToolBatch("guide")
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"hookSpecificOutput":{"hookEventName":"PostToolBatch","additionalContext":"guide"}}` {
		t.Fatalf("Claude envelope = %s", encoded)
	}
	encoded, err = EncodeCodexPostToolUseWithLimit("guide", 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"guide"}}` {
		t.Fatalf("Codex envelope = %s", encoded)
	}
	if _, err := EncodeClaudePostToolBatch(strings.Repeat("a", 10_000)); err != nil {
		t.Fatalf("10,000-byte Claude body rejected: %v", err)
	}
	if _, err := EncodeClaudePostToolBatch(strings.Repeat("a", 10_001)); err == nil || !errors.Is(err, ErrInputLimit) {
		t.Fatalf("10,001-byte Claude body error = %v", err)
	}
	if _, err := EncodeCodexPostToolUse(strings.Repeat("a", 30_000)); err != nil {
		t.Fatalf("30,000-byte Codex body rejected: %v", err)
	}
	if _, err := EncodeCodexPostToolUse(strings.Repeat("a", 32_001)); err == nil || !errors.Is(err, ErrInputLimit) {
		t.Fatalf("32,001-byte Codex body error = %v", err)
	}
	if _, err := EncodeClaudePostToolBatch(strings.Repeat("é", 5_001)); err == nil || !errors.Is(err, ErrInputLimit) {
		t.Fatalf("UTF-8 Claude budget error = %v", err)
	}
	if _, err := EncodeCodexPostToolUse(""); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("empty Codex envelope error = %v", err)
	}
}
