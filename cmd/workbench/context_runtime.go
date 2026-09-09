package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextconfig"
	"github.com/phosphorco/workbench-go/internal/contextdaemon"
	"github.com/phosphorco/workbench-go/internal/contexthook"
	"github.com/phosphorco/workbench-go/internal/contexttrace"
)

const (
	defaultContextWholeHookDeadline = 2 * time.Second
	defaultContextStartupTimeout    = 2 * time.Second
	defaultContextDialTimeout       = 250 * time.Millisecond
	defaultContextMaxWireBytes      = 4 * 1024 * 1024
)

type hookFailureStage string

// HookStreamError is returned when a hook stream cannot be proven to support
// bounded I/O. Regular files are accepted because the hook writes only an
// already bounded envelope; pipes and sockets must support deadlines.
type HookStreamError struct {
	Stream string
	Reason string
}

func (err *HookStreamError) Error() string {
	if err == nil {
		return "hook stream is unsafe"
	}
	return fmt.Sprintf("hook %s stream is unsafe: %s", err.Stream, err.Reason)
}

const (
	hookStagePaths      hookFailureStage = "paths"
	hookStageRead       hookFailureStage = "read stdin"
	hookStageRoute      hookFailureStage = "decode route"
	hookStageActivation hookFailureStage = "load activation"
	hookStageDecode     hookFailureStage = "decode hook"
	hookStageNormalize  hookFailureStage = "normalize hook"
	hookStageStartup    hookFailureStage = "start runtime"
	hookStageObserve    hookFailureStage = "observe"
	hookStageEncode     hookFailureStage = "encode output"
	hookStageOutput     hookFailureStage = "write stdout"
	hookStageConfirm    hookFailureStage = "confirm handoff"
	hookStageClose      hookFailureStage = "close client"
	hookStageDeadline   hookFailureStage = "whole-hook deadline"
)

// runContextHook is fail-open by design: host hook failures are diagnostics,
// not agent-loop failures. It does not log raw stdin, tool results, or
// transcripts. The caller owns the whole operation deadline.
func runContextHook(parent context.Context, options contextOptions, output, diagnostics io.Writer) error {
	if parent == nil {
		writeHookDiagnostic(diagnostics, hookStageDeadline, errors.New("nil parent context"))
		return nil
	}
	hookStarted := time.Now()
	wholeExplicit := options.wholeDeadline > 0 || os.Getenv("WORKBENCH_CONTEXT_WHOLE_HOOK_DEADLINE") != ""
	wholeDeadline, err := contextDurationOption(options.wholeDeadline, "WORKBENCH_CONTEXT_WHOLE_HOOK_DEADLINE", defaultContextWholeHookDeadline)
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageDeadline, err)
		return nil
	}
	hookContext, cancel := context.WithTimeout(parent, wholeDeadline)
	defer cancel()
	paths, err := contextPaths(options)
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStagePaths, err)
		return nil
	}
	limits := contexthook.DefaultInputLimits()
	if options.stdinLimit > 0 {
		limits.MaxInputBytes = options.stdinLimit
	} else if value := os.Getenv("WORKBENCH_CONTEXT_STDIN_LIMIT"); value != "" {
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || parsed < 1 {
			writeHookDiagnostic(diagnostics, hookStageRead, fmt.Errorf("WORKBENCH_CONTEXT_STDIN_LIMIT must be a positive integer"))
			return nil
		}
		limits.MaxInputBytes = parsed
	}
	if err := hookContext.Err(); err != nil {
		writeHookDiagnostic(diagnostics, hookStageDeadline, err)
		return nil
	}
	data, err := readHookInput(hookContext, os.Stdin, limits.MaxInputBytes)
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageRead, err)
		return nil
	}
	harness, err := contextHarness(options.harness)
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageRoute, err)
		return nil
	}
	route, err := contexthook.DecodeRoute(harness, data, limits)
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageRoute, err)
		return nil
	}
	loaded, err := contextconfig.Load(contextconfig.LoadOptions{
		WorkingDirectory: route.CWD,
		HomeConfigPath:   paths.HomeConfigPath,
		Now:              time.Now().UTC(),
	})
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageActivation, err)
		return nil
	}
	if !wholeExplicit && loaded.Input.Config.Home.Runtime.WholeHookDeadlineMs > 0 {
		homeWhole, durationErr := millisecondsDuration(loaded.Input.Config.Home.Runtime.WholeHookDeadlineMs, "home whole-hook deadline")
		if durationErr != nil {
			writeHookDiagnostic(diagnostics, hookStageDeadline, durationErr)
			return nil
		}
		remaining := homeWhole - time.Since(hookStarted)
		if remaining <= 0 {
			writeHookDiagnostic(diagnostics, hookStageDeadline, context.DeadlineExceeded)
			return nil
		}
		narrowed, narrowCancel := context.WithTimeout(hookContext, remaining)
		defer narrowCancel()
		hookContext = narrowed
	}
	if err := hookContext.Err(); err != nil {
		writeHookDiagnostic(diagnostics, hookStageDeadline, err)
		return nil
	}
	if loaded.Result.State != contextapi.ActivationEnabled {
		// Inactive, conflicting, and invalid activation deliberately produces
		// no stdout and never invokes Ensure, providers, cache, or transcript IO.
		return nil
	}
	var hook contexthook.HookDTO
	switch harness {
	case contextapi.HarnessClaudeCode:
		hook, err = contexthook.DecodeClaudeHookWithLimits(hookContext, data, limits)
	case contextapi.HarnessCodex:
		hook, err = contexthook.DecodeCodexHookWithLimits(hookContext, data, limits)
	default:
		err = fmt.Errorf("unsupported harness %q", harness)
	}
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageDecode, err)
		return nil
	}
	hookDeadline, err := contextDurationOption(options.hookDeadline, "WORKBENCH_CONTEXT_HOOK_DEADLINE", 0)
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageStartup, err)
		return nil
	}
	if options.hookDeadline == 0 && os.Getenv("WORKBENCH_CONTEXT_HOOK_DEADLINE") == "" && loaded.Input.Config.Home.Runtime.HookDeadlineMs > 0 {
		hookDeadline, err = millisecondsDuration(loaded.Input.Config.Home.Runtime.HookDeadlineMs, "home hook deadline")
		if err != nil {
			writeHookDiagnostic(diagnostics, hookStageStartup, err)
			return nil
		}
	}
	now := time.Now().UTC()
	var normalized contexthook.Normalized
	switch harness {
	case contextapi.HarnessClaudeCode:
		normalized, err = contexthook.NormalizeClaudeHook(hook, loaded.Result, now)
	case contextapi.HarnessCodex:
		normalized, err = contexthook.NormalizeCodexHook(hook, loaded.Result, now)
	}
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageNormalize, err)
		return nil
	}
	if err := hookContext.Err(); err != nil {
		writeHookDiagnostic(diagnostics, hookStageDeadline, err)
		return nil
	}
	startupTimeout, err := contextDurationOption(options.startup, "WORKBENCH_CONTEXT_STARTUP_TIMEOUT", defaultContextStartupTimeout)
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageStartup, err)
		return nil
	}
	dialTimeout, err := contextDurationOption(options.dial, "WORKBENCH_CONTEXT_DIAL_TIMEOUT", defaultContextDialTimeout)
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageStartup, err)
		return nil
	}
	maxWireBytes := options.maxWireBytes
	if maxWireBytes == 0 {
		maxWireBytes, err = contextIntOption("WORKBENCH_CONTEXT_MAX_WIRE_BYTES", defaultContextMaxWireBytes)
		if err != nil {
			writeHookDiagnostic(diagnostics, hookStageStartup, err)
			return nil
		}
	}
	stageContext, stageCancel := contextWithHookStageDeadline(hookContext, hookDeadline)
	client, err := contextdaemon.Ensure(stageContext, contextdaemon.ClientOptions{
		Paths:          paths,
		StartupTimeout: startupTimeout,
		DialTimeout:    dialTimeout,
		MaxWireBytes:   maxWireBytes,
		Starter:        startContextDaemon,
	})
	stageCancel()
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageStartup, err)
		return nil
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			writeHookDiagnostic(diagnostics, hookStageClose, closeErr)
		}
	}()
	stageContext, stageCancel = contextWithHookStageDeadline(hookContext, hookDeadline)
	result, err := client.Observe(stageContext, contextdaemon.ObserveInput{
		WorkingDirectory: normalized.Host.WorkingDirectory,
		Host:             normalized.Host,
		Observation:      normalized.Observation,
		Opportunity:      normalized.Opportunity,
		Transition:       normalized.Transition,
	})
	stageCancel()
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageObserve, err)
		return nil
	}
	if result.Decision.Offer.Identity.ID == 0 || result.Decision.Offer.Body == "" || result.Decision.State != contextapi.DeliveryOffered {
		return nil
	}
	envelope, err := encodeHookOffer(harness, normalized.Opportunity.Surface, normalized.Opportunity.Invocation.HookEvent, result.Decision.Offer.Body)
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageEncode, err)
		return nil
	}
	stageContext, stageCancel = contextWithHookStageDeadline(hookContext, hookDeadline)
	err = writeHookOutput(stageContext, output, envelope)
	stageCancel()
	if err != nil {
		writeHookDiagnostic(diagnostics, hookStageOutput, err)
		return nil
	}
	stageContext, stageCancel = contextWithHookStageDeadline(hookContext, hookDeadline)
	confirmed := client.Confirm(stageContext, contextdaemon.ConfirmInput{
		WorkingDirectory: normalized.Host.WorkingDirectory,
		Identity:         result.Decision.Offer.Identity,
		Handoff: contextapi.HandoffOutcome{
			State:          contextapi.HandoffConfirmed,
			EmittedContent: result.Decision.Offer.Identity.Body,
		},
		At: now,
	})
	stageCancel()
	if confirmed.State != contextapi.DeliveryConfirmed {
		writeHookDiagnostic(diagnostics, hookStageConfirm, summarizeConfirmation(confirmed))
	}
	return nil
}

func contextWithHookStageDeadline(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	if duration <= 0 {
		return parent, func() {}
	}
	deadline := time.Now().Add(duration)
	if current, ok := parent.Deadline(); ok && current.Before(deadline) {
		deadline = current
	}
	return context.WithDeadline(parent, deadline)
}

func millisecondsDuration(value uint64, name string) (time.Duration, error) {
	if value > uint64((time.Duration(1<<63-1))/time.Millisecond) {
		return 0, fmt.Errorf("%s is too large", name)
	}
	return time.Duration(value) * time.Millisecond, nil
}

func contextHarness(value string) (contextapi.Harness, error) {
	switch value {
	case "claude":
		return contextapi.HarnessClaudeCode, nil
	case "codex":
		return contextapi.HarnessCodex, nil
	default:
		return "", fmt.Errorf("--harness must be claude or codex, got %q", value)
	}
}

func summarizeConfirmation(result contextapi.ConfirmationResult) error {
	if len(result.Reasons) == 0 {
		return fmt.Errorf("confirmation state %q", result.State)
	}
	return fmt.Errorf("confirmation state %q: %s", result.State, result.Reasons[0].Summary)
}

func writeHookDiagnostic(output io.Writer, stage hookFailureStage, err error) {
	if output == nil || err == nil {
		return
	}
	_, _ = fmt.Fprintf(output, "workbench context hook: %s: %v\n", stage, err)
}

func readHookInput(ctx context.Context, reader *os.File, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, fmt.Errorf("stdin is unavailable")
	}
	if fileNeedsDeadline(reader) {
		return readBoundedFile(ctx, reader, maxBytes)
	}
	return contexthook.ReadHookStdin(reader, maxBytes)
}

func writeHookOutput(ctx context.Context, output io.Writer, data []byte) error {
	if output == nil {
		return errors.New("stdout is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if file, ok := output.(*os.File); ok {
		if fileNeedsDeadline(file) {
			return writeBoundedFile(ctx, file, data)
		}
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written != len(data) {
			return io.ErrShortWrite
		}
		return nil
	}
	if _, known := output.(*bytes.Buffer); !known {
		return &HookStreamError{Stream: "stdout", Reason: "no bounded deadline capability"}
	}
	written, err := output.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	if flusher, ok := output.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return fmt.Errorf("flush stdout: %w", err)
		}
	}
	return nil
}

func readBoundedFile(ctx context.Context, file *os.File, maxBytes int64) ([]byte, error) {
	if maxBytes < 1 {
		return nil, fmt.Errorf("read stdin: positive input limit is required")
	}
	fd := int(file.Fd())
	flags, err := fileStatusFlags(fd)
	if err != nil {
		return nil, &HookStreamError{Stream: "stdin", Reason: fmt.Sprintf("inspect descriptor flags: %v", err)}
	}
	if err := setFileStatusFlags(fd, flags|syscall.O_NONBLOCK); err != nil {
		return nil, &HookStreamError{Stream: "stdin", Reason: fmt.Sprintf("enable bounded polling: %v", err)}
	}
	defer func() { _ = setFileStatusFlags(fd, flags) }()
	data := make([]byte, 0, minInt64(maxBytes, 32*1024))
	chunk := make([]byte, 32*1024)
	for {
		if err := waitForFile(ctx, fd, false); err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		readSize := len(chunk)
		remaining := maxBytes - int64(len(data))
		if remaining < int64(readSize) {
			readSize = int(remaining)
		}
		if readSize < 1 {
			readSize = 1
		}
		count, err := syscall.Read(fd, chunk[:readSize])
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			continue
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		if count == 0 {
			return data, nil
		}
		data = append(data, chunk[:count]...)
		if int64(len(data)) > maxBytes {
			return nil, &contexthook.SizeError{Operation: "hook input", Limit: maxBytes, Observed: int64(len(data))}
		}
	}
}

func writeBoundedFile(ctx context.Context, file *os.File, data []byte) error {
	fd := int(file.Fd())
	flags, err := fileStatusFlags(fd)
	if err != nil {
		return &HookStreamError{Stream: "stdout", Reason: fmt.Sprintf("inspect descriptor flags: %v", err)}
	}
	if err := setFileStatusFlags(fd, flags|syscall.O_NONBLOCK); err != nil {
		return &HookStreamError{Stream: "stdout", Reason: fmt.Sprintf("enable bounded polling: %v", err)}
	}
	defer func() { _ = setFileStatusFlags(fd, flags) }()
	for written := 0; written < len(data); {
		if err := waitForFile(ctx, fd, true); err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
		count, err := syscall.Write(fd, data[written:])
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			continue
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count < 1 {
			return io.ErrShortWrite
		}
		written += count
	}
	return nil
}

func minInt64(value int64, upper int64) int {
	if value < upper {
		return int(value)
	}
	return int(upper)
}

func fileNeedsDeadline(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return true
	}
	return !info.Mode().IsRegular()
}

func encodeHookOffer(harness contextapi.Harness, surface contextapi.DeliverySurface, event, body string) ([]byte, error) {
	switch harness {
	case contextapi.HarnessClaudeCode:
		if surface != contextapi.DeliveryClaudeContext {
			return nil, fmt.Errorf("Claude offer has unsupported delivery surface %q", surface)
		}
		return contexthook.EncodeClaudePostToolBatch(body)
	case contextapi.HarnessCodex:
		switch surface {
		case contextapi.DeliveryCodexContext:
			switch event {
			case "PostToolUse":
				return contexthook.EncodeCodexPostToolUse(body)
			case "UserPromptSubmit":
				return contexthook.EncodeCodexUserPromptSubmit(body)
			case "SessionStart":
				return contexthook.EncodeCodexSessionStart(body)
			default:
				return nil, fmt.Errorf("Codex offer has unsupported hook event %q", event)
			}
		default:
			return nil, fmt.Errorf("Codex offer has unsupported delivery surface %q", surface)
		}
	default:
		return nil, fmt.Errorf("unsupported hook harness %q", harness)
	}
}

func startContextDaemon(ctx context.Context, paths contextdaemon.Paths) (*os.Process, error) {
	if ctx == nil {
		return nil, errors.New("start context daemon: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve Workbench executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve Workbench executable: %w", err)
	}
	arguments := []string{"context", "serve", "--home-config", paths.HomeConfigPath, "--runtime-dir", paths.RuntimeDir, "--socket", paths.SocketPath, "--start-lock", paths.LockPath, "--server-lock", paths.ServerLockPath, "--cache-dir", paths.CacheDir}
	command := exec.Command(executable, arguments...)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open daemon stdio sink: %w", err)
	}
	command.Stdin = devNull
	command.Stdout = devNull
	command.Stderr = devNull
	if err := command.Start(); err != nil {
		_ = devNull.Close()
		return nil, fmt.Errorf("start context daemon: %w", err)
	}
	if err := devNull.Close(); err != nil {
		// The child has its own descriptors; a parent close error should not
		// transfer pipe ownership to the caller.
		return command.Process, nil
	}
	return command.Process, nil
}

func contextRuntimeOptions(paths contextdaemon.Paths, options contextOptions) (contextdaemon.RuntimeOptions, error) {
	hookDeadline, err := contextDurationOption(options.hookDeadline, "WORKBENCH_CONTEXT_HOOK_DEADLINE", 0)
	if err != nil {
		return contextdaemon.RuntimeOptions{}, err
	}
	wholeDeadline, err := contextDurationOption(options.wholeDeadline, "WORKBENCH_CONTEXT_WHOLE_HOOK_DEADLINE", 0)
	if err != nil {
		return contextdaemon.RuntimeOptions{}, err
	}
	return contextdaemon.RuntimeOptions{Paths: paths, LoadLimits: contextconfig.DefaultLoadLimits(), HookDeadline: hookDeadline, WholeHookDeadline: wholeDeadline}, nil
}

func runContextServe(parent context.Context, options contextOptions, output io.Writer) error {
	paths, err := contextPaths(options)
	if err != nil {
		return err
	}
	runtimeOptions, err := contextRuntimeOptions(paths, options)
	if err != nil {
		return err
	}
	runtime, err := contextdaemon.NewRuntime(runtimeOptions)
	if err != nil {
		return fmt.Errorf("construct context runtime: %w", err)
	}
	serveContext, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := runtime.Serve(serveContext)
	closeErr := runtime.Close()
	if serveErr != nil && closeErr != nil {
		return errors.Join(serveErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if serveErr != nil {
		return serveErr
	}
	return writeReport(output, "Workbench context runtime stopped.")
}

func runContextRuntimeCommand(ctx context.Context, invocation contextInvocation, root string, output, diagnostics io.Writer) error {
	if invocation.kind == contextCommandServe {
		return runContextServe(ctx, invocation.options, output)
	}
	paths, err := contextPaths(invocation.options)
	if err != nil {
		return err
	}
	startupTimeout, err := contextDurationOption(invocation.options.startup, "WORKBENCH_CONTEXT_STARTUP_TIMEOUT", defaultContextStartupTimeout)
	if err != nil {
		return err
	}
	dialTimeout, err := contextDurationOption(invocation.options.dial, "WORKBENCH_CONTEXT_DIAL_TIMEOUT", defaultContextDialTimeout)
	if err != nil {
		return err
	}
	maxWireBytes := invocation.options.maxWireBytes
	if maxWireBytes == 0 {
		maxWireBytes, err = contextIntOption("WORKBENCH_CONTEXT_MAX_WIRE_BYTES", defaultContextMaxWireBytes)
		if err != nil {
			return err
		}
	}
	client, err := contextdaemon.Ensure(ctx, contextdaemon.ClientOptions{Paths: paths, StartupTimeout: startupTimeout, DialTimeout: dialTimeout, MaxWireBytes: maxWireBytes, Starter: startContextDaemon})
	if err != nil {
		return fmt.Errorf("start context inspection runtime: %w", err)
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			writeHookDiagnostic(diagnostics, hookStageClose, closeErr)
		}
	}()
	switch invocation.kind {
	case contextCommandHistory:
		query, err := contextTraceQuery(root, paths.HomeConfigPath, invocation.options)
		if err != nil {
			return err
		}
		result, err := client.Query(ctx, query)
		if err != nil {
			return fmt.Errorf("query context history: %w", err)
		}
		return writeTraceResult(output, result, invocation.options, "history")
	case contextCommandInspectContribution:
		query, err := contextTraceQuery(root, paths.HomeConfigPath, invocation.options)
		if err != nil {
			return err
		}
		id, _ := strconv.ParseUint(invocation.value, 10, 64)
		result, err := client.Inspect(ctx, contextdaemon.InspectRequest{Kind: contextdaemon.InspectContribution, ContributionID: id, Query: query})
		if err != nil {
			return fmt.Errorf("inspect contribution %d: %w", id, err)
		}
		return writeTraceResult(output, result, invocation.options, "contribution")
	case contextCommandInspectTurn:
		query, err := contextTraceQuery(root, paths.HomeConfigPath, invocation.options)
		if err != nil {
			return err
		}
		result, err := client.Inspect(ctx, contextdaemon.InspectRequest{Kind: contextdaemon.InspectTurn, Turn: invocation.value, Query: query})
		if err != nil {
			return fmt.Errorf("inspect turn %q: %w", invocation.value, err)
		}
		return writeTraceResult(output, result, invocation.options, "turn")
	case contextCommandExplainProfile:
		query, err := contextTraceQuery(root, paths.HomeConfigPath, invocation.options)
		if err != nil {
			return err
		}
		result, err := client.Inspect(ctx, contextdaemon.InspectRequest{Kind: contextdaemon.InspectProfile, Profile: invocation.value, Query: query})
		if err != nil {
			return fmt.Errorf("explain profile %q: %w", invocation.value, err)
		}
		return writeTraceResult(output, result, invocation.options, "profile")
	case contextCommandCacheStatus:
		status, err := client.Status(ctx, contextdaemon.StatusRequest{})
		if err != nil {
			return fmt.Errorf("read context cache status: %w", err)
		}
		if invocation.options.json {
			return writeJSONReport(output, status.Trace)
		}
		return writeContextCacheStatus(output, status.Trace)
	case contextCommandCacheClear:
		cleared, err := client.Clear(ctx)
		if err != nil {
			return fmt.Errorf("clear context explanation cache: %w", err)
		}
		if invocation.options.json {
			return writeJSONReport(output, cleared)
		}
		return writeReport(output, fmt.Sprintf("Cleared explanation history generation %d; new generation %d. Delivery state was not cleared.", cleared.PreviousGeneration, cleared.Generation))
	default:
		return errors.New(contextHelp)
	}
}

func contextTraceQuery(root, homeConfig string, options contextOptions) (contexttrace.Query, error) {
	query := contexttrace.Query{After: options.after, Limit: options.limit, MaxBytes: options.maxBytes, MaxScanBytes: options.maxScanBytes, Generation: options.generation, Scope: options.scope, Audience: options.audience}
	// Leave all query budgets zero unless the caller explicitly supplies them.
	// The daemon resolves zero against the live store/cache limits; CLI defaults
	// here could exceed a smaller configured cache and turn a bare history
	// command into an avoidable bounds error.
	if options.cursor != "" {
		parts := strings.Split(options.cursor, ":")
		if len(parts) != 2 {
			return contexttrace.Query{}, errors.New("--cursor must be BLOCK_SEQUENCE:RECORD_ID")
		}
		block, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			return contexttrace.Query{}, fmt.Errorf("parse cursor block sequence: %w", err)
		}
		record, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return contexttrace.Query{}, fmt.Errorf("parse cursor record id: %w", err)
		}
		query.Cursor = contexttrace.Cursor{BlockSequence: block, RecordID: record}
	}
	if query.Scope == "" && root != "" {
		loaded, err := contextconfig.Load(contextconfig.LoadOptions{WorkingDirectory: root, HomeConfigPath: homeConfig, Now: time.Now().UTC()})
		if err != nil {
			return contexttrace.Query{}, fmt.Errorf("load inspection scope: %w", err)
		}
		if loaded.Result.Scope.ID != "" {
			query.Scope = string(loaded.Result.Scope.ID)
		}
	}
	return query, nil
}

func writeTraceResult(output io.Writer, result contexttrace.QueryResult, options contextOptions, label string) error {
	if options.json {
		return writeJSONReport(output, result)
	}
	if len(result.Records) == 0 {
		if len(result.Gaps) == 0 {
			return writeReport(output, fmt.Sprintf("%s: no retained records for this scope; absence is not evidence that no activity occurred.", label))
		}
		return writeReport(output, fmt.Sprintf("%s: no retained records; explanation evidence is unavailable in %s.", label, gapSummary(result.Gaps)))
	}
	if err := writeReport(output, fmt.Sprintf("%s: %d record%s; generation %d", label, len(result.Records), plural(len(result.Records), "", "s"), result.Generation)); err != nil {
		return err
	}
	for _, record := range result.Records {
		if err := writeTraceRecord(output, record); err != nil {
			return err
		}
	}
	if result.HasMore {
		if err := writeReport(output, fmt.Sprintf("More records available: --after %d --cursor %d:%d", result.NextAfter, result.NextCursor.BlockSequence, result.NextCursor.RecordID)); err != nil {
			return err
		}
	}
	if len(result.Gaps) > 0 || result.PartialScan || result.PartialPage {
		return writeReport(output, fmt.Sprintf("Evidence incomplete: %s", gapSummary(result.Gaps)))
	}
	return nil
}

func writeTraceRecord(output io.Writer, record contexttrace.Record) error {
	if err := writeReport(output, fmt.Sprintf("Record %d · %s · %s · %s", record.ID, traceKind(record.Kind), record.At.UTC().Format(time.RFC3339Nano), traceOutcome(record.Outcome))); err != nil {
		return err
	}
	if record.Scope != "" || record.Audience != "" || record.Turn != "" || record.Profile != "" {
		if err := writeReport(output, fmt.Sprintf("  Scope: %s  Audience: %s  Epoch: %d  Turn: %s  Profile: %s", record.Scope, record.Audience, record.Epoch, record.Turn, record.Profile)); err != nil {
			return err
		}
	}
	if record.Source != "" || record.Contributor != "" || record.ContributionID != 0 {
		if err := writeReport(output, fmt.Sprintf("  Source: %s  Contributor: %s  Contribution: %d", record.Source, record.Contributor, record.ContributionID)); err != nil {
			return err
		}
	}
	for _, reason := range record.Reasons {
		if err := writeReport(output, fmt.Sprintf("  Why [%d] %s rule=%s provider=%s", reason.Code, reasonSummary(reason), reason.Rule, reason.Provider)); err != nil {
			return err
		}
	}
	if record.Sample.OriginalBytes > 0 {
		if err := writeReport(output, fmt.Sprintf("  Sample: %d/%d bytes retained; %d omitted", record.Sample.OriginalBytes-record.Sample.OmittedBytes, record.Sample.OriginalBytes, record.Sample.OmittedBytes)); err != nil {
			return err
		}
		for _, excerpt := range record.Sample.Excerpts {
			if err := writeReport(output, fmt.Sprintf("    [%d..%d] %s", excerpt.Offset, excerpt.Offset+excerpt.Bytes, excerpt.Text)); err != nil {
				return err
			}
		}
	}
	return nil
}

func reasonSummary(reason contexttrace.Reason) string {
	if len(reason.Params) == 0 {
		return "decision evidence"
	}
	return fmt.Sprintf("decision evidence (%d input%s)", len(reason.Params), plural(len(reason.Params), "", "s"))
}

func gapSummary(gaps []contexttrace.Gap) string {
	if len(gaps) == 0 {
		return "the query was bounded"
	}
	parts := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		parts = append(parts, traceGapKind(gap.Kind))
	}
	return strings.Join(parts, ", ")
}

func traceKind(kind contexttrace.EventKind) string {
	switch kind {
	case contexttrace.KindObservation:
		return "observation"
	case contexttrace.KindProfile:
		return "profile"
	case contexttrace.KindContribution:
		return "contribution"
	case contexttrace.KindQueued:
		return "queued"
	case contexttrace.KindOffered:
		return "offered"
	case contexttrace.KindConfirmed:
		return "confirmed"
	case contexttrace.KindSuppressed:
		return "suppressed"
	case contexttrace.KindDeferred:
		return "deferred"
	case contexttrace.KindRejected:
		return "rejected"
	case contexttrace.KindFailed:
		return "failed"
	case contexttrace.KindWithdrawn:
		return "withdrawn"
	default:
		return fmt.Sprintf("event-%d", kind)
	}
}

func traceOutcome(outcome contexttrace.OutcomeCode) string {
	switch outcome {
	case contexttrace.OutcomeNone:
		return "no outcome"
	case contexttrace.OutcomeQueued:
		return "queued"
	case contexttrace.OutcomeOffered:
		return "offered"
	case contexttrace.OutcomeConfirmed:
		return "confirmed"
	case contexttrace.OutcomeSuppressed:
		return "suppressed"
	case contexttrace.OutcomeDeferred:
		return "deferred"
	case contexttrace.OutcomeRejected:
		return "rejected"
	case contexttrace.OutcomeFailed:
		return "failed"
	case contexttrace.OutcomeWithdrawn:
		return "withdrawn"
	default:
		return fmt.Sprintf("outcome-%d", outcome)
	}
}

func traceGapKind(kind contexttrace.GapKind) string {
	switch kind {
	case contexttrace.GapRotated:
		return "rotated"
	case contexttrace.GapDropped:
		return "dropped"
	case contexttrace.GapCorrupt:
		return "corrupt"
	case contexttrace.GapUnreadable:
		return "unreadable"
	default:
		return fmt.Sprintf("gap-%d", kind)
	}
}

func writeContextCacheStatus(output io.Writer, stats contexttrace.Stats) error {
	if err := writeReport(output, fmt.Sprintf("Explanation cache: generation %d", stats.Generation)); err != nil {
		return err
	}
	if err := writeReport(output, fmt.Sprintf("Disk: %d/%d bytes; blocks: %d", stats.DiskBytes, stats.DiskCap, stats.BlockCount)); err != nil {
		return err
	}
	if stats.PendingRecords > 0 {
		return writeReport(output, fmt.Sprintf("Pending: %d records, %d bytes", stats.PendingRecords, stats.PendingBytes))
	}
	if stats.KnownLoss {
		return writeReport(output, "Evidence gaps are retained; missing history is not equivalent to no activity.")
	}
	return nil
}

func contextDurationOption(option time.Duration, variable string, fallback time.Duration) (time.Duration, error) {
	if option > 0 {
		return option, nil
	}
	if value := os.Getenv(variable); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf("%s must be a positive duration", variable)
		}
		return parsed, nil
	}
	return fallback, nil
}

func contextIntOption(variable string, fallback int) (int, error) {
	if value := os.Getenv(variable); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 || int64(int(parsed)) != parsed {
			return 0, fmt.Errorf("%s must be a positive integer", variable)
		}
		return int(parsed), nil
	}
	return fallback, nil
}
