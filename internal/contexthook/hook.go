// Package contexthook decodes the bounded, provider-owned hook boundary.
//
// It performs no activation lookup, provider I/O, filesystem reads, or
// package-global bookkeeping. Route decoding is intentionally separate from
// full normalization so an inactive hook can stop before runtime activation.
package contexthook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"mvdan.cc/sh/v3/syntax"
)

const (
	defaultMaxInputBytes    = int64(1 << 20)
	defaultMaxJSONDepth     = 32
	defaultMaxJSONStrings   = 4096
	defaultMaxStringBytes   = 256 * 1024
	defaultMaxToolCalls     = 128
	defaultMaxToolInput     = 256 * 1024
	defaultMaxShellBytes    = 64 * 1024
	defaultMaxShellWords    = 256
	defaultMaxOutputBytes   = uint64(10_000)
	defaultCodexOutputBytes = uint64(32_000)
	defaultMaxOutputItems   = uint32(128)
	claudeOutputMaxBytes    = uint64(10_000)
	claudeOutputBudgetUnit  = "UTF-8 bytes"
	codexOutputBudgetUnit   = "UTF-8 bytes"
)

var (
	ErrInputLimit        = errors.New("contexthook: input limit exceeded")
	ErrMalformedInput    = errors.New("contexthook: malformed hook input")
	ErrMissingIdentity   = errors.New("contexthook: required host identity is missing")
	ErrInvalidActivation = errors.New("contexthook: activation is not enabled")
	ErrInvalidEnvelope   = errors.New("contexthook: invalid delivery envelope")
)

// InputLimits bounds all work done by route and full hook decoding. Zero
// values use DefaultInputLimits. MaxInputBytes is enforced by ReadHookStdin;
// callers that already own bytes still get the same limit from decoding.
type InputLimits struct {
	MaxInputBytes  int64
	MaxJSONDepth   int
	MaxJSONStrings int
	MaxStringBytes int
	MaxToolCalls   int
	MaxToolInput   int
	MaxShellBytes  int
	MaxShellWords  int
}

func DefaultInputLimits() InputLimits {
	return InputLimits{
		MaxInputBytes:  defaultMaxInputBytes,
		MaxJSONDepth:   defaultMaxJSONDepth,
		MaxJSONStrings: defaultMaxJSONStrings,
		MaxStringBytes: defaultMaxStringBytes,
		MaxToolCalls:   defaultMaxToolCalls,
		MaxToolInput:   defaultMaxToolInput,
		MaxShellBytes:  defaultMaxShellBytes,
		MaxShellWords:  defaultMaxShellWords,
	}
}

func (limits InputLimits) normalized() (InputLimits, error) {
	defaults := DefaultInputLimits()
	if limits.MaxInputBytes == 0 {
		limits.MaxInputBytes = defaults.MaxInputBytes
	}
	if limits.MaxJSONDepth == 0 {
		limits.MaxJSONDepth = defaults.MaxJSONDepth
	}
	if limits.MaxJSONStrings == 0 {
		limits.MaxJSONStrings = defaults.MaxJSONStrings
	}
	if limits.MaxStringBytes == 0 {
		limits.MaxStringBytes = defaults.MaxStringBytes
	}
	if limits.MaxToolCalls == 0 {
		limits.MaxToolCalls = defaults.MaxToolCalls
	}
	if limits.MaxToolInput == 0 {
		limits.MaxToolInput = defaults.MaxToolInput
	}
	if limits.MaxShellBytes == 0 {
		limits.MaxShellBytes = defaults.MaxShellBytes
	}
	if limits.MaxShellWords == 0 {
		limits.MaxShellWords = defaults.MaxShellWords
	}
	if limits.MaxInputBytes < 1 || limits.MaxJSONDepth < 1 || limits.MaxJSONStrings < 1 ||
		limits.MaxStringBytes < 1 || limits.MaxToolCalls < 1 || limits.MaxToolInput < 1 ||
		limits.MaxShellBytes < 1 || limits.MaxShellWords < 1 {
		return InputLimits{}, fmt.Errorf("%w: all input bounds must be positive", ErrInputLimit)
	}
	return limits, nil
}

// SizeError identifies the bounded operation and exact observed size. It is
// returned without truncating caller input.
type SizeError struct {
	Operation string
	Limit     int64
	Observed  int64
	Unit      string
}

func (e *SizeError) Error() string {
	unit := e.Unit
	if unit == "" {
		unit = "byte"
	}
	return fmt.Sprintf("contexthook: %s exceeded %d-%s limit (observed at least %d)", e.Operation, e.Limit, unit, e.Observed)
}

func (e *SizeError) Unwrap() error { return ErrInputLimit }

// ReadHookStdin reads at most maxBytes+1 bytes when that increment is safe. It
// never returns a truncated payload as valid input, and it does not retain the
// reader's buffer. The caller owns any wall-clock deadline for reader I/O.
func ReadHookStdin(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil || maxBytes < 1 {
		return nil, fmt.Errorf("%w: stdin reader and positive limit are required", ErrInputLimit)
	}
	readLimit := maxBytes
	if maxBytes < (1<<63 - 1) {
		readLimit++
	}
	limited := io.LimitReader(reader, readLimit)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read hook stdin: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, &SizeError{Operation: "hook stdin", Limit: maxBytes, Observed: int64(len(data))}
	}
	return data, nil
}

type Event string

type Route struct {
	Harness        contextapi.Harness
	SessionID      string
	CWD            string
	EventName      Event
	PromptID       string
	TurnID         string
	TranscriptPath *string
}

// DecodeRoute performs only bounded, activation-independent routing decode.
// It validates the harness, session, absolute cwd, event, and optional
// transcript path without decoding tool payloads or reading the transcript.
func DecodeRoute(harness contextapi.Harness, data []byte, limits InputLimits) (Route, error) {
	limits, err := limits.normalized()
	if err != nil {
		return Route{}, err
	}
	if int64(len(data)) > limits.MaxInputBytes {
		return Route{}, &SizeError{Operation: "hook input", Limit: limits.MaxInputBytes, Observed: int64(len(data))}
	}
	if harness != contextapi.HarnessClaudeCode && harness != contextapi.HarnessCodex {
		return Route{}, fmt.Errorf("%w: unsupported harness %q", ErrMalformedInput, harness)
	}
	if err := validateJSON(data, limits); err != nil {
		return Route{}, err
	}
	var raw struct {
		SessionID      string          `json:"session_id"`
		CWD            string          `json:"cwd"`
		EventName      string          `json:"hook_event_name"`
		PromptID       string          `json:"prompt_id"`
		TurnID         string          `json:"turn_id"`
		TranscriptPath json.RawMessage `json:"transcript_path"`
	}
	if err := unmarshalObject(data, &raw); err != nil {
		return Route{}, err
	}
	if strings.TrimSpace(raw.SessionID) == "" || raw.CWD == "" || strings.TrimSpace(raw.EventName) == "" {
		return Route{}, fmt.Errorf("%w: session_id, cwd, and hook_event_name are required", ErrMissingIdentity)
	}
	if !filepath.IsAbs(raw.CWD) {
		return Route{}, fmt.Errorf("%w: cwd must be absolute", ErrMalformedInput)
	}
	var transcriptPath *string
	if len(raw.TranscriptPath) > 0 && !bytes.Equal(bytes.TrimSpace(raw.TranscriptPath), []byte("null")) {
		var path string
		if json.Unmarshal(raw.TranscriptPath, &path) != nil || path == "" || !filepath.IsAbs(path) {
			return Route{}, fmt.Errorf("%w: transcript_path must be absolute or null", ErrMalformedInput)
		}
		transcriptPath = &path
	}
	return Route{
		Harness:        harness,
		SessionID:      raw.SessionID,
		CWD:            filepath.Clean(raw.CWD),
		EventName:      Event(raw.EventName),
		PromptID:       raw.PromptID,
		TurnID:         raw.TurnID,
		TranscriptPath: cloneStringPointer(transcriptPath),
	}, nil
}

// HookDTO is the bounded provider-owned DTO. It contains no transcript body;
// transcript_path is retained only as an untrusted provider reference.
type HookDTO struct {
	Harness        contextapi.Harness
	SessionID      string
	PromptID       string
	TurnID         string
	TranscriptPath *string
	CWD            string
	EventName      Event
	ToolName       string
	ToolUseID      string
	ToolInput      json.RawMessage
	ToolResponse   json.RawMessage
	ToolCalls      []ToolCall
	Prompt         string
	SessionSource  string
	limits         InputLimits
}

type ToolCall struct {
	Name     string
	UseID    string
	Input    json.RawMessage
	Response json.RawMessage
}

func DecodeClaudeHook(ctx context.Context, data []byte) (HookDTO, error) {
	return DecodeClaudeHookWithLimits(ctx, data, DefaultInputLimits())
}

func DecodeClaudeHookWithLimits(ctx context.Context, data []byte, limits InputLimits) (HookDTO, error) {
	return decodeHook(ctx, contextapi.HarnessClaudeCode, data, limits)
}

func DecodeCodexHook(ctx context.Context, data []byte) (HookDTO, error) {
	return DecodeCodexHookWithLimits(ctx, data, DefaultInputLimits())
}

func DecodeCodexHookWithLimits(ctx context.Context, data []byte, limits InputLimits) (HookDTO, error) {
	return decodeHook(ctx, contextapi.HarnessCodex, data, limits)
}

func decodeHook(ctx context.Context, harness contextapi.Harness, data []byte, limits InputLimits) (HookDTO, error) {
	if ctx == nil {
		return HookDTO{}, fmt.Errorf("%w: nil context", ErrMalformedInput)
	}
	select {
	case <-ctx.Done():
		return HookDTO{}, ctx.Err()
	default:
	}
	limits, err := limits.normalized()
	if err != nil {
		return HookDTO{}, err
	}
	route, err := DecodeRoute(harness, data, limits)
	if err != nil {
		return HookDTO{}, err
	}
	var raw struct {
		SessionID      string          `json:"session_id"`
		PromptID       string          `json:"prompt_id"`
		TurnID         string          `json:"turn_id"`
		TranscriptPath *string         `json:"transcript_path"`
		CWD            string          `json:"cwd"`
		EventName      string          `json:"hook_event_name"`
		ToolName       string          `json:"tool_name"`
		ToolUseID      string          `json:"tool_use_id"`
		ToolInput      json.RawMessage `json:"tool_input"`
		ToolResponse   json.RawMessage `json:"tool_response"`
		ToolCalls      []struct {
			Name     string          `json:"tool_name"`
			UseID    string          `json:"tool_use_id"`
			Input    json.RawMessage `json:"tool_input"`
			Response json.RawMessage `json:"tool_response"`
		} `json:"tool_calls"`
		Prompt           string `json:"prompt"`
		SessionSource    string `json:"source"`
		SessionSourceAlt string `json:"session_source"`
	}
	if err := unmarshalObject(data, &raw); err != nil {
		return HookDTO{}, err
	}
	if raw.ToolInput != nil && len(raw.ToolInput) > limits.MaxToolInput {
		return HookDTO{}, &SizeError{Operation: "tool_input", Limit: int64(limits.MaxToolInput), Observed: int64(len(raw.ToolInput))}
	}
	if err := validateToolInputLimit(raw.ToolName, raw.ToolInput, limits); err != nil {
		return HookDTO{}, err
	}
	if raw.ToolResponse != nil && len(raw.ToolResponse) > limits.MaxStringBytes+limits.MaxToolInput {
		return HookDTO{}, &SizeError{Operation: "tool_response", Limit: int64(limits.MaxStringBytes + limits.MaxToolInput), Observed: int64(len(raw.ToolResponse))}
	}
	if len(raw.ToolCalls) > limits.MaxToolCalls {
		return HookDTO{}, &SizeError{Operation: "tool_calls", Limit: int64(limits.MaxToolCalls), Observed: int64(len(raw.ToolCalls)), Unit: "item"}
	}
	toolCalls := make([]ToolCall, len(raw.ToolCalls))
	for index, call := range raw.ToolCalls {
		if len(call.Input) > limits.MaxToolInput {
			return HookDTO{}, &SizeError{Operation: "tool_calls.tool_input", Limit: int64(limits.MaxToolInput), Observed: int64(len(call.Input))}
		}
		if err := validateToolInputLimit(call.Name, call.Input, limits); err != nil {
			return HookDTO{}, err
		}
		toolCalls[index] = ToolCall{
			Name:     call.Name,
			UseID:    stringCopy(call.UseID),
			Input:    cloneRaw(call.Input),
			Response: cloneRaw(call.Response),
		}
	}
	return HookDTO{
		Harness:        harness,
		SessionID:      stringCopy(route.SessionID),
		PromptID:       stringCopy(raw.PromptID),
		TurnID:         stringCopy(raw.TurnID),
		TranscriptPath: cloneStringPointer(raw.TranscriptPath),
		CWD:            stringCopy(route.CWD),
		EventName:      Event(raw.EventName),
		ToolName:       stringCopy(raw.ToolName),
		ToolUseID:      stringCopy(raw.ToolUseID),
		ToolInput:      cloneRaw(raw.ToolInput),
		ToolResponse:   cloneRaw(raw.ToolResponse),
		ToolCalls:      toolCalls,
		Prompt:         stringCopy(raw.Prompt),
		SessionSource:  stringCopy(firstNonEmpty(raw.SessionSource, raw.SessionSourceAlt)),
		limits:         limits,
	}, nil
}

func validateToolInputLimit(name string, input json.RawMessage, limits InputLimits) error {
	lowerName := strings.ToLower(name)
	if lowerName != "bash" && lowerName != "exec" && lowerName != "exec_command" && lowerName != "unified_exec" && lowerName != "shell" && lowerName != "apply_patch" && lowerName != "applypatch" {
		return nil
	}
	var object struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
		Patch   string `json:"patch"`
		Diff    string `json:"diff"`
		Input   string `json:"input"`
	}
	if json.Unmarshal(input, &object) != nil {
		return nil
	}
	value := firstNonEmpty(object.Command, object.Cmd)
	operation := "shell command"
	if value == "" && (lowerName == "apply_patch" || lowerName == "applypatch") {
		value = firstNonEmpty(object.Patch, object.Diff, object.Input)
		operation = "apply_patch body"
	}
	if len(value) > limits.MaxShellBytes {
		return &SizeError{Operation: operation, Limit: int64(limits.MaxShellBytes), Observed: int64(len(value))}
	}
	return nil
}

func unmarshalObject(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedInput, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values", ErrMalformedInput)
		}
		return fmt.Errorf("%w: %v", ErrMalformedInput, err)
	}
	return nil
}

func validateJSON(data []byte, limits InputLimits) error {
	if len(data) == 0 || !utf8.Valid(data) {
		return fmt.Errorf("%w: input must be non-empty valid UTF-8 JSON", ErrMalformedInput)
	}
	depth := 0
	stringsSeen := 0
	for index := 0; index < len(data); index++ {
		if data[index] != '"' {
			if data[index] == '{' || data[index] == '[' {
				depth++
				if depth > limits.MaxJSONDepth {
					return &SizeError{Operation: "JSON depth", Limit: int64(limits.MaxJSONDepth), Observed: int64(depth)}
				}
			} else if data[index] == '}' || data[index] == ']' {
				depth--
				if depth < 0 {
					return fmt.Errorf("%w: unbalanced JSON delimiters", ErrMalformedInput)
				}
			}
			continue
		}
		stringsSeen++
		if stringsSeen > limits.MaxJSONStrings {
			return &SizeError{Operation: "JSON strings", Limit: int64(limits.MaxJSONStrings), Observed: int64(stringsSeen)}
		}
		start := index + 1
		closed := false
		for index = start; index < len(data); index++ {
			if data[index] == '\\' {
				index++
				if index >= len(data) {
					return fmt.Errorf("%w: unterminated JSON escape", ErrMalformedInput)
				}
				if data[index] == 'u' {
					if index+4 >= len(data) {
						return fmt.Errorf("%w: short JSON unicode escape", ErrMalformedInput)
					}
					index += 4
				}
				continue
			}
			if data[index] == '"' {
				closed = true
				break
			}
			if data[index] < 0x20 {
				return fmt.Errorf("%w: control byte in JSON string", ErrMalformedInput)
			}
		}
		if !closed {
			return fmt.Errorf("%w: unterminated JSON string", ErrMalformedInput)
		}
		var value string
		if err := json.Unmarshal(data[start-1:index+1], &value); err != nil {
			return fmt.Errorf("%w: invalid JSON string: %v", ErrMalformedInput, err)
		}
		if len(value) > limits.MaxStringBytes {
			return &SizeError{Operation: "JSON string", Limit: int64(limits.MaxStringBytes), Observed: int64(len(value))}
		}
	}
	if depth != 0 || !json.Valid(data) {
		return fmt.Errorf("%w: invalid JSON syntax", ErrMalformedInput)
	}
	return nil
}

type Identity struct {
	Harness       contextapi.Harness
	WorkspaceRoot string
	SessionID     string
	Audience      contextapi.Audience
	EpochKey      string
	TurnID        string
	PromptID      string
}

func NormalizeClaudeIdentity(hook HookDTO, workspaceRoot string) (Identity, error) {
	return normalizeIdentity(hook, contextapi.HarnessClaudeCode, workspaceRoot)
}

func NormalizeCodexIdentity(hook HookDTO, workspaceRoot string) (Identity, error) {
	return normalizeIdentity(hook, contextapi.HarnessCodex, workspaceRoot)
}

func normalizeIdentity(hook HookDTO, harness contextapi.Harness, workspaceRoot string) (Identity, error) {
	if hook.Harness != "" && hook.Harness != harness {
		return Identity{}, fmt.Errorf("%w: DTO harness is %q, want %q", ErrMalformedInput, hook.Harness, harness)
	}
	if strings.TrimSpace(hook.SessionID) == "" || workspaceRoot == "" || !filepath.IsAbs(workspaceRoot) {
		return Identity{}, fmt.Errorf("%w: session and absolute workspace root are required", ErrMissingIdentity)
	}
	return Identity{
		Harness:       harness,
		WorkspaceRoot: filepath.Clean(workspaceRoot),
		SessionID:     stringCopy(hook.SessionID),
		Audience: contextapi.Audience{
			ID:         contextapi.AudienceID(string(harness) + ":" + hook.SessionID),
			Epoch:      0,
			Continuity: identityContinuity(hook),
		},
		EpochKey: stringCopy(epochKey(hook)),
		TurnID:   stringCopy(hook.TurnID),
		PromptID: stringCopy(hook.PromptID),
	}, nil
}

func identityContinuity(hook HookDTO) contextapi.Continuity {
	if transitionKind(hook) != "" {
		return contextapi.ContinuityReset
	}
	return contextapi.ContinuityUnknown
}

func epochKey(hook HookDTO) string {
	kind := transitionKind(hook)
	if kind == "" {
		return ""
	}
	evidence := hook.SessionSource
	if evidence == "" {
		evidence = string(hook.EventName)
	}
	if evidence == "" {
		return ""
	}
	return string(kind) + ":" + evidence
}

type Normalized struct {
	Host        contextapi.HostSnapshot
	Observation contextapi.Observation
	Opportunity contextapi.DeliveryOpportunity
	Transition  contextapi.AudienceTransition
	Identity    Identity
	Reasons     []contextapi.Reason
}

// NormalizeClaudeHook converts an enabled activation and decoded Claude DTO
// into the shared host, observation, opportunity, and transition contracts.
func NormalizeClaudeHook(hook HookDTO, activation contextapi.ActivationResult, now time.Time) (Normalized, error) {
	return normalizeHook(hook, contextapi.HarnessClaudeCode, activation, now)
}

// NormalizeCodexHook converts an enabled activation and decoded Codex DTO
// into the shared host, observation, opportunity, and transition contracts.
func NormalizeCodexHook(hook HookDTO, activation contextapi.ActivationResult, now time.Time) (Normalized, error) {
	return normalizeHook(hook, contextapi.HarnessCodex, activation, now)
}

func normalizeHook(hook HookDTO, harness contextapi.Harness, activation contextapi.ActivationResult, now time.Time) (Normalized, error) {
	if activation.State != contextapi.ActivationEnabled {
		return Normalized{}, fmt.Errorf("%w: state is %q", ErrInvalidActivation, activation.State)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	identity, err := normalizeIdentity(hook, harness, activation.Scope.CanonicalRoot)
	if err != nil {
		return Normalized{}, err
	}
	if hook.CWD == "" || !filepath.IsAbs(hook.CWD) {
		return Normalized{}, fmt.Errorf("%w: hook cwd must be absolute", ErrMalformedInput)
	}
	turn := turnRef(hook)
	capabilities := capabilitiesFor(harness)
	host := contextapi.HostSnapshot{
		Harness:          harness,
		WorkingDirectory: filepath.Clean(hook.CWD),
		RepositoryRoot:   activation.Scope.CanonicalRoot,
		Turn:             turn,
		Capabilities:     capabilities,
	}
	transition := makeTransition(hook, identity.Audience, now)
	observation, reasons, err := makeObservation(hook, activation.Scope, identity.Audience, turn, now)
	if err != nil {
		return Normalized{}, err
	}
	if identity.Audience.Continuity == contextapi.ContinuityUnknown {
		reasons = append(reasons, contextapi.Reason{
			Code: contextapi.ReasonEvidenceUnavailable, Origin: contextapi.ReasonAdapter,
			Summary: "host supplied no continuity or epoch evidence; runtime must conservatively assign a fresh epoch",
			At:      now,
		})
	}
	opportunity := makeOpportunity(hook, harness)
	return Normalized{
		Host:        host,
		Observation: observation,
		Opportunity: opportunity,
		Transition:  transition,
		Identity:    identity,
		Reasons:     reasons,
	}, nil
}

func capabilitiesFor(harness contextapi.Harness) contextapi.AdapterCapabilities {
	if harness == contextapi.HarnessClaudeCode {
		return contextapi.AdapterCapabilities{
			Harness: harness, AudienceIdentity: contextapi.IdentityAvailable,
			EpochIdentity: contextapi.IdentityUnknown, TurnIdentity: contextapi.IdentityUnknown,
			Observations:     []contextapi.ObservationKind{contextapi.ObservationResources, contextapi.ObservationSelectors, contextapi.ObservationRuntime},
			DeliverySurfaces: []contextapi.DeliverySurface{contextapi.DeliveryClaudeContext}, BudgetUnit: claudeOutputBudgetUnit,
		}
	}
	return contextapi.AdapterCapabilities{
		Harness: harness, AudienceIdentity: contextapi.IdentityAvailable,
		EpochIdentity: contextapi.IdentityUnknown, TurnIdentity: contextapi.IdentityAvailable,
		Observations:     []contextapi.ObservationKind{contextapi.ObservationResources, contextapi.ObservationSelectors, contextapi.ObservationRuntime},
		DeliverySurfaces: []contextapi.DeliverySurface{contextapi.DeliveryCodexContext}, BudgetUnit: codexOutputBudgetUnit,
	}
}

func turnRef(hook HookDTO) contextapi.TurnRef {
	if hook.TurnID != "" {
		return contextapi.TurnRef{ID: contextapi.TurnID(hook.TurnID), State: contextapi.TurnKnown}
	}
	return contextapi.TurnRef{State: contextapi.TurnUnknown}
}

func transitionKind(hook HookDTO) contextapi.AudienceTransitionKind {
	source := strings.ToLower(strings.TrimSpace(hook.SessionSource))
	event := strings.ToLower(strings.TrimSpace(string(hook.EventName)))
	switch source {
	case "clear", "reset":
		return contextapi.TransitionReset
	case "fork":
		return contextapi.TransitionFork
	case "compact", "compaction":
		return contextapi.TransitionCompact
	case "startup", "session-start", "start", "resume":
		if event == "sessionstart" || event == "session_start" || event == "session-start" {
			return contextapi.TransitionSessionStart
		}
	}
	if strings.Contains(event, "compact") {
		return contextapi.TransitionCompact
	}
	if event == "sessionstart" || event == "session_start" || event == "session-start" {
		return contextapi.TransitionSessionStart
	}
	return ""
}

func makeTransition(hook HookDTO, audience contextapi.Audience, now time.Time) contextapi.AudienceTransition {
	kind := transitionKind(hook)
	if kind == "" {
		return contextapi.AudienceTransition{}
	}
	causal := hook.PromptID
	if causal == "" {
		causal = hook.TurnID
	}
	if causal == "" {
		causal = hook.SessionSource
	}
	return contextapi.AudienceTransition{
		Kind:     kind,
		Previous: contextapi.Audience{ID: audience.ID, Continuity: contextapi.ContinuityUnknown},
		Current:  contextapi.Audience{ID: audience.ID, Continuity: contextapi.ContinuityReset},
		CausalID: contextapi.CausalID(causal), At: now,
	}
}

func makeOpportunity(hook HookDTO, harness contextapi.Harness) contextapi.DeliveryOpportunity {
	var surface contextapi.DeliverySurface
	available := false
	maxBytes := defaultMaxOutputBytes
	if harness == contextapi.HarnessClaudeCode {
		surface, available = ClaudeDeliverySurface(hook)
		maxBytes = claudeOutputMaxBytes
	} else {
		surface, available = CodexDeliverySurface(hook)
		maxBytes = defaultCodexOutputBytes
	}
	return contextapi.DeliveryOpportunity{
		ID:      string(invocationID(hook)),
		Surface: surface, Available: available, MaxBytes: maxBytes,
		MaxItems:   defaultMaxOutputItems,
		Invocation: contextapi.InvocationRef{ID: invocationID(hook), HookEvent: string(hook.EventName)},
	}
}

// ClaudeDeliverySurface reports whether this callback is the supported
// immediate Claude context boundary. Observation-only lifecycle callbacks
// return false without inventing a delivery opportunity.
func ClaudeDeliverySurface(hook HookDTO) (contextapi.DeliverySurface, bool) {
	if hook.EventName != "PostToolBatch" {
		return "", false
	}
	return contextapi.DeliveryClaudeContext, true
}

// CodexDeliverySurface reports the supported synchronous Codex context
// boundaries. Stop and pre-tool callbacks remain observation-only.
func CodexDeliverySurface(hook HookDTO) (contextapi.DeliverySurface, bool) {
	switch hook.EventName {
	case "PostToolUse", "UserPromptSubmit", "SessionStart":
		return contextapi.DeliveryCodexContext, true
	default:
		return "", false
	}
}

func invocationID(hook HookDTO) contextapi.InvocationID {
	if hook.ToolUseID != "" {
		return contextapi.InvocationID(hook.ToolUseID)
	}
	if len(hook.ToolCalls) == 1 && hook.ToolCalls[0].UseID != "" {
		return contextapi.InvocationID(hook.ToolCalls[0].UseID)
	}
	if len(hook.ToolCalls) > 1 {
		ids := make([]string, len(hook.ToolCalls))
		for index, call := range hook.ToolCalls {
			if call.UseID == "" {
				ids = nil
				break
			}
			ids[index] = call.UseID
		}
		if ids != nil {
			digest := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
			return contextapi.InvocationID("batch:" + hex.EncodeToString(digest[:12]))
		}
	}
	if hook.PromptID != "" {
		return contextapi.InvocationID(hook.PromptID)
	}
	if hook.TurnID != "" {
		return contextapi.InvocationID(hook.TurnID)
	}
	return ""
}

func makeObservation(hook HookDTO, scope contextapi.ScopeIdentity, audience contextapi.Audience, turn contextapi.TurnRef, now time.Time) (contextapi.Observation, []contextapi.Reason, error) {
	observation := contextapi.Observation{
		CausalID: causalID(hook), At: now, Audience: audience, Scope: scope,
		Invocation: contextapi.InvocationRef{ID: invocationID(hook), HookEvent: string(hook.EventName)},
		Turn:       turn, Trigger: observationTrigger(hook),
	}
	reasons := make([]contextapi.Reason, 0)
	var calls []ToolCall
	if len(hook.ToolCalls) > 0 {
		calls = hook.ToolCalls
	} else if hook.ToolName != "" {
		calls = []ToolCall{{Name: hook.ToolName, UseID: hook.ToolUseID, Input: hook.ToolInput, Response: hook.ToolResponse}}
	}
	for _, call := range calls {
		resources, selectors, callReasons, err := normalizeToolCall(hook, call, scope.CanonicalRoot, hook.CWD, now)
		if err != nil {
			return contextapi.Observation{}, nil, err
		}
		observation.Resources = append(observation.Resources, resources...)
		observation.Selectors = append(observation.Selectors, selectors...)
		reasons = append(reasons, callReasons...)
	}
	return observation, reasons, nil
}

func observationTrigger(hook HookDTO) contextapi.ObservationTrigger {
	if hook.ToolName != "" || len(hook.ToolCalls) > 0 {
		return contextapi.TriggerToolResult
	}
	return contextapi.TriggerExplicitGather
}

func causalID(hook HookDTO) contextapi.CausalID {
	if hook.ToolUseID != "" {
		return contextapi.CausalID(hook.ToolUseID)
	}
	if hook.PromptID != "" {
		return contextapi.CausalID(hook.PromptID)
	}
	if hook.TurnID != "" {
		return contextapi.CausalID(hook.TurnID)
	}
	return ""
}

func normalizeToolCall(hook HookDTO, call ToolCall, root, cwd string, now time.Time) ([]contextapi.ObservedResource, []contextapi.ObservedSelector, []contextapi.Reason, error) {
	name := strings.ToLower(call.Name)
	if name == "read" || name == "edit" || name == "write" || name == "multiedit" {
		resources, selectors, reasons := normalizeNativeTool(hook, call, root, cwd, now)
		return resources, selectors, reasons, nil
	}
	if name == "bash" || name == "exec" || name == "exec_command" || name == "unified_exec" || name == "shell" {
		resources, selectors, reasons, err := normalizeShellTool(hook, call, root, cwd, now)
		return resources, selectors, reasons, err
	}
	if name == "apply_patch" || name == "applypatch" {
		resources, selectors, reasons := normalizePatchTool(hook, call, root, cwd, now)
		return resources, selectors, reasons, nil
	}
	return nil, nil, nil, nil
}

func normalizeNativeTool(hook HookDTO, call ToolCall, root, cwd string, now time.Time) ([]contextapi.ObservedResource, []contextapi.ObservedSelector, []contextapi.Reason) {
	var input struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Files    []struct {
			FilePath string `json:"file_path"`
			Path     string `json:"path"`
		} `json:"files"`
	}
	if len(call.Input) == 0 || json.Unmarshal(call.Input, &input) != nil {
		return nil, nil, []contextapi.Reason{invalidToolReason("native tool input is missing or malformed", now)}
	}
	paths := make([]string, 0, 1+len(input.Files))
	if input.FilePath != "" {
		paths = append(paths, input.FilePath)
	} else if input.Path != "" {
		paths = append(paths, input.Path)
	}
	for _, file := range input.Files {
		if file.FilePath != "" {
			paths = append(paths, file.FilePath)
		} else if file.Path != "" {
			paths = append(paths, file.Path)
		}
	}
	outcome := nativeOutcome(hook, call.Response)
	operation := contextapi.ResourceRead
	if strings.EqualFold(call.Name, "edit") || strings.EqualFold(call.Name, "write") || strings.EqualFold(call.Name, "multiedit") {
		operation = contextapi.ResourceWrite
	}
	resources := make([]contextapi.ObservedResource, 0, len(paths))
	reasons := make([]contextapi.Reason, 0)
	for _, path := range paths {
		relative, valid := repositoryPath(path, root, cwd)
		if !valid {
			reasons = append(reasons, invalidToolReason("native tool path escapes the active repository", now))
			continue
		}
		resources = append(resources, contextapi.ObservedResource{
			Path: relative, Kind: contextapi.ResourceFile, Operation: operation,
			Outcome: outcome, Confidence: contextapi.ConfidenceObserved,
		})
	}
	return resources, nil, reasons
}

func normalizePatchTool(hook HookDTO, call ToolCall, root, cwd string, now time.Time) ([]contextapi.ObservedResource, []contextapi.ObservedSelector, []contextapi.Reason) {
	var input struct {
		Patch string `json:"patch"`
		Diff  string `json:"diff"`
		Input string `json:"input"`
	}
	if json.Unmarshal(call.Input, &input) != nil {
		var patchText string
		if json.Unmarshal(call.Input, &patchText) != nil {
			return nil, nil, []contextapi.Reason{invalidToolReason("apply_patch input is malformed", now)}
		}
		input.Patch = patchText
	}
	patch := input.Patch
	if patch == "" {
		patch = input.Diff
	}
	if patch == "" {
		patch = input.Input
	}
	maxPatchBytes := hook.limits.MaxShellBytes
	if maxPatchBytes == 0 {
		maxPatchBytes = defaultMaxShellBytes
	}
	if patch == "" || len(patch) > maxPatchBytes {
		return nil, nil, []contextapi.Reason{invalidToolReason("apply_patch body is absent or oversized", now)}
	}
	paths := patchPaths(patch)
	outcome := nativeOutcome(hook, call.Response)
	resources := make([]contextapi.ObservedResource, 0, len(paths))
	reasons := make([]contextapi.Reason, 0)
	for _, path := range paths {
		relative, valid := repositoryPath(path, root, cwd)
		if !valid {
			reasons = append(reasons, invalidToolReason("apply_patch path escapes the active repository", now))
			continue
		}
		resources = append(resources, contextapi.ObservedResource{
			Path: relative, Kind: contextapi.ResourceFile, Operation: contextapi.ResourceWrite,
			Outcome: outcome, Confidence: contextapi.ConfidenceObserved,
		})
	}
	return resources, nil, reasons
}

func patchPaths(patch string) []string {
	paths := make([]string, 0, 4)
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
			if strings.HasPrefix(line, prefix) {
				path := strings.TrimSpace(strings.TrimPrefix(line, prefix))
				if path != "" {
					paths = append(paths, path)
				}
				break
			}
		}
	}
	return paths
}

func normalizeShellTool(hook HookDTO, call ToolCall, root, cwd string, now time.Time) ([]contextapi.ObservedResource, []contextapi.ObservedSelector, []contextapi.Reason, error) {
	command, ok := commandFromInput(call.Input)
	limits := hook.limits
	if limits.MaxShellBytes == 0 {
		limits = DefaultInputLimits()
	}
	if !ok {
		return nil, nil, []contextapi.Reason{invalidToolReason("shell command is absent or oversized", now)}, nil
	}
	if len(command) > limits.MaxShellBytes {
		return nil, nil, nil, &SizeError{Operation: "shell command", Limit: int64(limits.MaxShellBytes), Observed: int64(len(command))}
	}
	resources, selectors, parsed, overflow := parseStaticShell(command, root, cwd, nativeOutcome(hook, call.Response), limits.MaxShellWords, limits.MaxShellBytes)
	if overflow {
		return nil, nil, nil, &SizeError{Operation: "shell words", Limit: int64(limits.MaxShellWords), Observed: int64(limits.MaxShellWords + 1)}
	}
	if !parsed {
		return nil, nil, []contextapi.Reason{inferredUnknownReason("shell command was not a supported static read/search form", now)}, nil
	}
	return resources, selectors, nil, nil
}

func commandFromInput(input json.RawMessage) (string, bool) {
	var object struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
	}
	if json.Unmarshal(input, &object) != nil {
		return "", false
	}
	if object.Command != "" {
		return object.Command, true
	}
	return object.Cmd, object.Cmd != ""
}

func nativeOutcome(hook HookDTO, response json.RawMessage) contextapi.ResourceOutcome {
	if strings.Contains(strings.ToLower(string(hook.EventName)), "failure") {
		return contextapi.ResourceFailed
	}
	trimmed := bytes.TrimSpace(response)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return contextapi.ResourceOutcomeUnknown
	}
	var structured struct {
		Error    json.RawMessage `json:"error"`
		IsError  *bool           `json:"is_error"`
		Success  *bool           `json:"success"`
		ExitCode *int            `json:"exit_code"`
	}
	if trimmed[0] == '{' {
		if json.Unmarshal(trimmed, &structured) != nil {
			return contextapi.ResourceOutcomeUnknown
		}
		if structured.IsError != nil && *structured.IsError || structured.Success != nil && !*structured.Success || structured.ExitCode != nil && *structured.ExitCode != 0 || len(structured.Error) > 0 && string(structured.Error) != "null" {
			return contextapi.ResourceFailed
		}
		if structured.IsError != nil && !*structured.IsError || structured.Success != nil && *structured.Success || structured.ExitCode != nil && *structured.ExitCode == 0 {
			return contextapi.ResourceResolved
		}
		return contextapi.ResourceOutcomeUnknown
	}
	return contextapi.ResourceOutcomeUnknown
}

func parseStaticShell(command, root, cwd string, outcome contextapi.ResourceOutcome, maxWords, maxBytes int) ([]contextapi.ObservedResource, []contextapi.ObservedSelector, bool, bool) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, nil, false, false
	}
	if ambiguousShell(file) {
		return nil, nil, false, false
	}
	resources := make([]contextapi.ObservedResource, 0)
	selectors := make([]contextapi.ObservedSelector, 0)
	parsed := false
	words := 0
	overflow := false
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		words += len(call.Args)
		if words > maxWords {
			overflow = true
			return false
		}
		arguments := make([]string, len(call.Args))
		for index, word := range call.Args {
			value, static := staticWord(word, maxBytes)
			if !static {
				return true
			}
			arguments[index] = value
		}
		name := strings.ToLower(arguments[0])
		switch name {
		case "cat", "head", "tail", "sed":
			paths := staticReadPaths(name, arguments[1:])
			for _, path := range paths {
				if relative, valid := repositoryPath(path, root, cwd); valid {
					resources = append(resources, contextapi.ObservedResource{Path: relative, Kind: contextapi.ResourceFile, Operation: contextapi.ResourceRead, Outcome: outcome, Confidence: contextapi.ConfidenceInferred})
				}
			}
			if len(paths) > 0 {
				parsed = true
			}
		case "rg", "grep":
			pattern, pathArgs, pathPatterns, found := staticSearchArgs(name, arguments[1:])
			if found {
				interpretation := contextapi.SelectorRegex
				if hasFixedStringFlag(arguments[1:]) {
					interpretation = contextapi.SelectorKeyword
				}
				selectors = append(selectors, contextapi.ObservedSelector{Raw: pattern, ObservedAs: contextapi.SelectorSearchPattern, Interpretations: []contextapi.SelectorInterpretation{interpretation}, Confidence: contextapi.ConfidenceInferred})
				for _, pathPattern := range pathPatterns {
					selectors = append(selectors, contextapi.ObservedSelector{Raw: pathPattern, ObservedAs: contextapi.SelectorPathPattern, Interpretations: []contextapi.SelectorInterpretation{contextapi.SelectorGlob}, Confidence: contextapi.ConfidenceInferred})
				}
				for _, path := range pathArgs {
					if relative, valid := repositoryPath(path, root, cwd); valid {
						resources = append(resources, contextapi.ObservedResource{Path: relative, Kind: contextapi.ResourceDirectory, Operation: contextapi.ResourceRead, Outcome: outcome, Confidence: contextapi.ConfidenceInferred})
					}
				}
				parsed = true
			}
		}
		return true
	})
	if overflow {
		return nil, nil, false, true
	}
	return resources, selectors, parsed, false
}

func ambiguousShell(file *syntax.File) bool {
	if file == nil || len(file.Stmts) != 1 {
		return true
	}
	ambiguous := false
	syntax.Walk(file, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.BinaryCmd, *syntax.Block, *syntax.IfClause, *syntax.ForClause, *syntax.WhileClause, *syntax.CaseClause, *syntax.FuncDecl, *syntax.Subshell, *syntax.CStyleLoop, *syntax.TimeClause, *syntax.CoprocClause, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmCmd:
			ambiguous = true
			return false
		case *syntax.Stmt:
			if node.Negated || node.Background || node.Coprocess || node.Disown || len(node.Redirs) > 0 {
				ambiguous = true
				return false
			}
		case *syntax.CallExpr:
			if len(node.Args) > 0 {
				name, static := staticWord(node.Args[0], defaultMaxShellBytes)
				if static && name == "cd" {
					ambiguous = true
					return false
				}
			}
		}
		return !ambiguous
	})
	return ambiguous
}

func staticWord(word *syntax.Word, maxBytes int) (string, bool) {
	var builder strings.Builder
	var staticPart func(syntax.WordPart) bool
	staticPart = func(part syntax.WordPart) bool {
		switch part := part.(type) {
		case *syntax.Lit:
			builder.WriteString(part.Value)
		case *syntax.SglQuoted:
			builder.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, nested := range part.Parts {
				if !staticPart(nested) {
					return false
				}
			}
		default:
			return false
		}
		return true
	}
	if word == nil {
		return "", false
	}
	for _, part := range word.Parts {
		if !staticPart(part) {
			return "", false
		}
	}
	if builder.Len() > maxBytes {
		return "", false
	}
	return builder.String(), true
}

func hasFixedStringFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-F" || arg == "--fixed-strings" || strings.HasPrefix(arg, "-F") {
			return true
		}
	}
	return false
}

func staticReadPaths(name string, args []string) []string {
	paths := make([]string, 0, len(args))
	endOptions := false
	scriptSeen := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !endOptions && arg == "--" {
			endOptions = true
			continue
		}
		if name == "sed" && !scriptSeen {
			if !endOptions && strings.HasPrefix(arg, "-") {
				continue
			}
			scriptSeen = true
			continue
		}
		if !endOptions && strings.HasPrefix(arg, "-") {
			if name == "head" || name == "tail" {
				if arg == "-n" || arg == "--lines" || arg == "-c" || arg == "--bytes" {
					index++
				}
			}
			continue
		}
		if arg != "-" {
			paths = append(paths, arg)
		}
	}
	return paths
}

func staticSearchArgs(name string, args []string) (string, []string, []string, bool) {
	pattern := ""
	paths := make([]string, 0)
	pathPatterns := make([]string, 0)
	endOptions := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !endOptions && arg == "--" {
			endOptions = true
			continue
		}
		if !endOptions && strings.HasPrefix(arg, "-") {
			if arg == "-g" || arg == "--glob" || arg == "--include" {
				if index+1 < len(args) {
					index++
					pathPatterns = append(pathPatterns, args[index])
				}
			} else if arg == "-e" || arg == "--regexp" || arg == "--regexp-file" {
				if arg == "--regexp-file" {
					return "", nil, nil, false
				}
				if index+1 < len(args) {
					index++
					if pattern == "" {
						pattern = args[index]
					}
				}
			} else if !knownSearchFlag(name, arg) {
				return "", nil, nil, false
			}
			continue
		}
		if pattern == "" {
			pattern = arg
			continue
		}
		if arg != "-" {
			paths = append(paths, arg)
		}
	}
	return pattern, paths, pathPatterns, pattern != ""
}

func knownSearchFlag(name, flag string) bool {
	if name == "rg" {
		switch flag {
		case "-F", "--fixed-strings", "-i", "--ignore-case", "-n", "--line-number", "-w", "--word-regexp", "-x", "--line-regexp", "-v", "--invert-match", "-l", "--files-with-matches", "-L", "--files-without-match", "-q", "--quiet", "-s", "--case-sensitive", "-u", "--no-ignore", "-U", "--multiline":
			return true
		}
	}
	if name == "grep" {
		switch flag {
		case "-F", "--fixed-strings", "-i", "--ignore-case", "-n", "--line-number", "-w", "--word-regexp", "-x", "--line-regexp", "-v", "--invert-match", "-l", "--files-with-matches", "-L", "--files-without-match", "-q", "--quiet", "-R", "-r", "--recursive":
			return true
		}
	}
	return false
}

func repositoryPath(path, root, cwd string) (string, bool) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || root == "" || cwd == "" {
		return "", false
	}
	cleanRoot := filepath.Clean(root)
	base := filepath.Clean(cwd)
	if !filepath.IsAbs(base) {
		base = filepath.Join(cleanRoot, base)
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(base, candidate)
	}
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(cleanRoot, candidate)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func invalidToolReason(summary string, now time.Time) contextapi.Reason {
	return contextapi.Reason{Code: contextapi.ReasonInvalidInput, Origin: contextapi.ReasonAdapter, Summary: summary, At: now}
}

func inferredUnknownReason(summary string, now time.Time) contextapi.Reason {
	return contextapi.Reason{Code: contextapi.ReasonEvidenceUnavailable, Origin: contextapi.ReasonAdapter, Summary: summary, At: now}
}

func cloneRaw(input json.RawMessage) json.RawMessage {
	if input == nil {
		return nil
	}
	return append(json.RawMessage(nil), input...)
}

func cloneStringPointer(input *string) *string {
	if input == nil {
		return nil
	}
	value := stringCopy(*input)
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func stringCopy(input string) string { return strings.Clone(input) }

func EncodeClaudePostToolBatch(additionalContext string) ([]byte, error) {
	return encodeEnvelope("PostToolBatch", additionalContext, claudeOutputMaxBytes)
}

func EncodeCodexPostToolUse(additionalContext string) ([]byte, error) {
	return encodeEnvelope("PostToolUse", additionalContext, defaultCodexOutputBytes)
}

func EncodeCodexUserPromptSubmit(additionalContext string) ([]byte, error) {
	return encodeEnvelope("UserPromptSubmit", additionalContext, defaultCodexOutputBytes)
}

func EncodeCodexSessionStart(additionalContext string) ([]byte, error) {
	return encodeEnvelope("SessionStart", additionalContext, defaultCodexOutputBytes)
}

func EncodeCodexPostToolUseWithLimit(additionalContext string, maxBytes uint64) ([]byte, error) {
	return encodeEnvelope("PostToolUse", additionalContext, maxBytes)
}

func EncodeCodexUserPromptSubmitWithLimit(additionalContext string, maxBytes uint64) ([]byte, error) {
	return encodeEnvelope("UserPromptSubmit", additionalContext, maxBytes)
}

func EncodeCodexSessionStartWithLimit(additionalContext string, maxBytes uint64) ([]byte, error) {
	return encodeEnvelope("SessionStart", additionalContext, maxBytes)
}

func encodeEnvelope(event, additionalContext string, maxBytes uint64) ([]byte, error) {
	if event == "" || additionalContext == "" || !utf8.ValidString(additionalContext) {
		return nil, fmt.Errorf("%w: event and non-empty valid UTF-8 context are required", ErrInvalidEnvelope)
	}
	if maxBytes == 0 || maxBytes > uint64(1<<63-1) {
		return nil, fmt.Errorf("%w: positive finite output limit is required", ErrInvalidEnvelope)
	}
	if uint64(len(additionalContext)) > maxBytes {
		return nil, &SizeError{Operation: "additionalContext", Limit: int64(maxBytes), Observed: int64(len(additionalContext))}
	}
	payload := struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	payload.HookSpecificOutput.HookEventName = event
	payload.HookSpecificOutput.AdditionalContext = additionalContext
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode hook envelope: %w", err)
	}
	return encoded, nil
}
