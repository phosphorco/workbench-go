package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextconfig"
	"github.com/phosphorco/workbench-go/internal/contextdaemon"
)

const contextHelp = `workbench context <command> [options]

Context enrichment is globally installed but active only for explicitly opted-in projects.

Commands:
  hook --harness claude|codex       Run one bounded host hook (stdin/stdout protocol).
  setup [--harness both|claude|codex]
                                    Reconcile global Claude/Codex hook settings.
  init [--path DIR]                 Create a minimal Pkl project declaration when none applies.
  status [--path DIR]               Explain local activation without starting the runtime.
  history                           Inspect bounded explanation history (JSON or human).
  inspect contribution ID            Inspect one contribution with reasons and outcome.
  inspect turn TURN                 Inspect one host turn and its retained evidence.
  explain profile PROFILE           Inspect one profile transition or snapshot.
  cache status|clear                Inspect or clear explanation history only.
  serve                             Run the shared runtime in the foreground.

Read commands default to the current directory. Use --path, --scope, and --audience
to widen an inspection explicitly. Every read command accepts --json.

Path and bound overrides (flags or environment):
  --home-config PATH       WORKBENCH_CONTEXT_HOME_CONFIG
  --runtime-dir PATH       WORKBENCH_CONTEXT_RUNTIME_DIR
  --socket PATH            WORKBENCH_CONTEXT_SOCKET
  --start-lock PATH        WORKBENCH_CONTEXT_START_LOCK
  --server-lock PATH       WORKBENCH_CONTEXT_SERVER_LOCK
  --cache-dir PATH         WORKBENCH_CONTEXT_CACHE_DIR
  --whole-hook-deadline D  WORKBENCH_CONTEXT_WHOLE_HOOK_DEADLINE
  --hook-deadline D        WORKBENCH_CONTEXT_HOOK_DEADLINE
  --stdin-limit BYTES      WORKBENCH_CONTEXT_STDIN_LIMIT
  --max-wire-bytes BYTES   WORKBENCH_CONTEXT_MAX_WIRE_BYTES
  --startup-timeout D      WORKBENCH_CONTEXT_STARTUP_TIMEOUT
  --dial-timeout D         WORKBENCH_CONTEXT_DIAL_TIMEOUT

Setup overrides:
  --executable PATH        WORKBENCH_CONTEXT_EXECUTABLE
  --claude-settings PATH   WORKBENCH_CONTEXT_CLAUDE_SETTINGS
  --codex-home PATH        WORKBENCH_CONTEXT_CODEX_HOME
  --codex-config PATH      WORKBENCH_CONTEXT_CODEX_CONFIG
  --dry-run                Validate and print changes without writing files.

Defaults use XDG_CONFIG_HOME/workbench/workbench-context.pkl, XDG_RUNTIME_DIR/workbench,
and XDG_CACHE_HOME/workbench/context (with HOME fallbacks). Hook output is empty
on inactive, conflicting, invalid, or failed input; hook failures are fail-open.
For deadlines, flag > environment > enabled home Runtime limits > ordinary default;
the home whole-hook limit is applied to elapsed time and never widens a bound.
`

const (
	contextCommandHook contextCommandKind = iota + 1
	contextCommandSetup
	contextCommandInit
	contextCommandStatus
	contextCommandHistory
	contextCommandInspectContribution
	contextCommandInspectTurn
	contextCommandExplainProfile
	contextCommandCacheStatus
	contextCommandCacheClear
	contextCommandServe
)

type contextCommandKind uint8

type contextInvocation struct {
	kind    contextCommandKind
	value   string
	options contextOptions
}

// contextOptions is deliberately a concrete value. It is parsed before any
// cwd, configuration, runtime, or filesystem capability is acquired.
type contextOptions struct {
	harness       string
	path          string
	scope         string
	audience      string
	generation    uint64
	after         uint64
	limit         int
	maxBytes      int
	maxScanBytes  int
	cursor        string
	json          bool
	dryRun        bool
	homeConfig    string
	runtimeDir    string
	socket        string
	startLock     string
	serverLock    string
	cacheDir      string
	stdinLimit    int64
	maxWireBytes  int
	hookDeadline  time.Duration
	wholeDeadline time.Duration
	startup       time.Duration
	dial          time.Duration
	executable    string
	claudeSetting string
	codexHome     string
	codexConfig   string
}

type contextOptionKind uint8

const (
	contextStringKind contextOptionKind = iota + 1
	contextBoolKind
	contextDurationKind
	contextUintKind
	contextIntKind
)

var contextOptionKinds = map[string]contextOptionKind{
	"harness": contextStringKind, "path": contextStringKind,
	"scope": contextStringKind, "audience": contextStringKind,
	"cursor": contextStringKind, "home-config": contextStringKind,
	"runtime-dir": contextStringKind, "socket": contextStringKind,
	"start-lock": contextStringKind, "server-lock": contextStringKind,
	"cache-dir": contextStringKind, "executable": contextStringKind,
	"claude-settings": contextStringKind, "codex-home": contextStringKind,
	"codex-config": contextStringKind,
	"generation":   contextUintKind, "after": contextUintKind,
	"limit": contextIntKind, "max-bytes": contextIntKind,
	"max-scan-bytes": contextIntKind, "stdin-limit": contextIntKind,
	"max-wire-bytes":      contextIntKind,
	"hook-deadline":       contextDurationKind,
	"whole-hook-deadline": contextDurationKind,
	"startup-timeout":     contextDurationKind, "dial-timeout": contextDurationKind,
	"json": contextBoolKind, "dry-run": contextBoolKind,
}

func parseContextInvocation(arguments []string) (contextInvocation, error) {
	if len(arguments) == 0 || arguments[0] == "--help" || arguments[0] == "-h" || arguments[0] == "help" {
		return contextInvocation{}, nil
	}
	options, positionals, err := parseContextOptions(arguments[1:])
	if err != nil {
		return contextInvocation{}, contextUsageError(err.Error())
	}
	if options.help {
		if len(positionals) != 0 {
			return contextInvocation{}, contextUsageError("--help cannot be combined with positional arguments")
		}
		return contextInvocation{}, nil
	}
	parsed, err := contextOptionsValue(options.values, options.switches)
	if err != nil {
		return contextInvocation{}, contextUsageError(err.Error())
	}
	switch arguments[0] {
	case "hook":
		if len(positionals) != 0 || (parsed.harness != "claude" && parsed.harness != "codex") {
			return contextInvocation{}, contextUsageError("hook requires --harness claude or --harness codex")
		}
		return contextInvocation{kind: contextCommandHook, options: parsed}, nil
	case "setup":
		if len(positionals) != 0 {
			return contextInvocation{}, contextUsageError("setup does not accept positional arguments")
		}
		if parsed.harness == "" {
			parsed.harness = "both"
		}
		if parsed.harness != "both" && parsed.harness != "claude" && parsed.harness != "codex" {
			return contextInvocation{}, contextUsageError("setup --harness must be both, claude, or codex")
		}
		return contextInvocation{kind: contextCommandSetup, options: parsed}, nil
	case "init":
		if len(positionals) != 0 {
			return contextInvocation{}, contextUsageError("init does not accept positional arguments")
		}
		return contextInvocation{kind: contextCommandInit, options: parsed}, nil
	case "status":
		if len(positionals) == 0 {
			return contextInvocation{kind: contextCommandStatus, options: parsed}, nil
		}
	case "history":
		if len(positionals) == 0 {
			return contextInvocation{kind: contextCommandHistory, options: parsed}, nil
		}
	case "inspect":
		if len(positionals) == 2 && positionals[0] == "contribution" {
			id, parseErr := strconv.ParseUint(positionals[1], 10, 64)
			if parseErr == nil && id != 0 {
				return contextInvocation{kind: contextCommandInspectContribution, value: positionals[1], options: parsed}, nil
			}
		}
		if len(positionals) == 2 && positionals[0] == "turn" {
			return contextInvocation{kind: contextCommandInspectTurn, value: positionals[1], options: parsed}, nil
		}
	case "explain":
		if len(positionals) == 2 && positionals[0] == "profile" && positionals[1] != "" {
			return contextInvocation{kind: contextCommandExplainProfile, value: positionals[1], options: parsed}, nil
		}
	case "cache":
		if len(positionals) == 1 && positionals[0] == "status" {
			return contextInvocation{kind: contextCommandCacheStatus, options: parsed}, nil
		}
		if len(positionals) == 1 && positionals[0] == "clear" {
			return contextInvocation{kind: contextCommandCacheClear, options: parsed}, nil
		}
	case "serve":
		if len(positionals) == 0 {
			return contextInvocation{kind: contextCommandServe, options: parsed}, nil
		}
	}
	return contextInvocation{}, contextUsageError(fmt.Sprintf("unknown or malformed context command %q", arguments[0]))
}

func contextUsageError(reason string) error {
	return fmt.Errorf("context: %s; run 'workbench context --help'", reason)
}

type parsedContextOptions struct {
	values   map[string]string
	switches map[string]bool
	help     bool
}

func parseContextOptions(arguments []string) (parsedContextOptions, []string, error) {
	result := parsedContextOptions{values: make(map[string]string), switches: make(map[string]bool)}
	positionals := make([]string, 0, len(arguments))
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			positionals = append(positionals, arguments[index+1:]...)
			break
		}
		if argument == "--help" || argument == "-h" {
			result.help = true
			continue
		}
		if !strings.HasPrefix(argument, "--") || argument == "--" {
			positionals = append(positionals, argument)
			continue
		}
		nameValue := strings.SplitN(strings.TrimPrefix(argument, "--"), "=", 2)
		name := nameValue[0]
		kind, known := contextOptionKinds[name]
		if !known || name == "" {
			return parsedContextOptions{}, nil, fmt.Errorf("unknown option --%s", name)
		}
		if kind == contextBoolKind {
			if _, duplicate := result.switches[name]; duplicate {
				return parsedContextOptions{}, nil, fmt.Errorf("option --%s specified more than once", name)
			}
			if len(nameValue) == 2 {
				value, parseErr := strconv.ParseBool(nameValue[1])
				if parseErr != nil {
					return parsedContextOptions{}, nil, fmt.Errorf("option --%s value %q must be true or false", name, nameValue[1])
				}
				result.switches[name] = value
			} else {
				result.switches[name] = true
			}
			continue
		}
		var value string
		if len(nameValue) == 2 {
			value = nameValue[1]
		} else if index+1 < len(arguments) && arguments[index+1] != "--" && !strings.HasPrefix(arguments[index+1], "--") {
			index++
			value = arguments[index]
		} else {
			return parsedContextOptions{}, nil, fmt.Errorf("option --%s requires a value", name)
		}
		if _, duplicate := result.values[name]; duplicate {
			return parsedContextOptions{}, nil, fmt.Errorf("option --%s specified more than once", name)
		}
		if value == "" {
			return parsedContextOptions{}, nil, fmt.Errorf("option --%s value must not be empty", name)
		}
		result.values[name] = value
	}
	return result, positionals, nil
}

func contextOptionsValue(values map[string]string, switches map[string]bool) (contextOptions, error) {
	options := contextOptions{harness: values["harness"], path: values["path"], scope: values["scope"], audience: values["audience"], cursor: values["cursor"], homeConfig: values["home-config"], runtimeDir: values["runtime-dir"], socket: values["socket"], startLock: values["start-lock"], serverLock: values["server-lock"], cacheDir: values["cache-dir"], executable: values["executable"], claudeSetting: values["claude-settings"], codexHome: values["codex-home"], codexConfig: values["codex-config"], json: switches["json"], dryRun: switches["dry-run"]}
	if err := parseContextUint(values, "generation", &options.generation); err != nil {
		return contextOptions{}, err
	}
	if err := parseContextUint(values, "after", &options.after); err != nil {
		return contextOptions{}, err
	}
	if err := parseContextInt(values, "limit", &options.limit); err != nil {
		return contextOptions{}, err
	}
	if err := parseContextInt(values, "max-bytes", &options.maxBytes); err != nil {
		return contextOptions{}, err
	}
	if err := parseContextInt(values, "max-scan-bytes", &options.maxScanBytes); err != nil {
		return contextOptions{}, err
	}
	if err := parseContextInt64(values, "stdin-limit", &options.stdinLimit); err != nil {
		return contextOptions{}, err
	}
	if err := parseContextInt(values, "max-wire-bytes", &options.maxWireBytes); err != nil {
		return contextOptions{}, err
	}
	durations := []struct {
		name        string
		destination *time.Duration
	}{
		{name: "hook-deadline", destination: &options.hookDeadline},
		{name: "whole-hook-deadline", destination: &options.wholeDeadline},
		{name: "startup-timeout", destination: &options.startup},
		{name: "dial-timeout", destination: &options.dial},
	}
	for _, duration := range durations {
		if value := values[duration.name]; value != "" {
			parsed, err := time.ParseDuration(value)
			if err != nil || parsed <= 0 {
				return contextOptions{}, fmt.Errorf("%s must be a positive duration", duration.name)
			}
			*duration.destination = parsed
		}
	}
	return options, nil
}

func parseContextUint(values map[string]string, name string, destination *uint64) error {
	if value := values[name]; value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return fmt.Errorf("%s must be a non-negative integer", name)
		}
		*destination = parsed
	}
	return nil
}

func parseContextInt(values map[string]string, name string, destination *int) error {
	if value := values[name]; value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 || int64(int(parsed)) != parsed {
			return fmt.Errorf("%s must be a positive integer", name)
		}
		*destination = int(parsed)
	}
	return nil
}

func parseContextInt64(values map[string]string, name string, destination *int64) error {
	if value := values[name]; value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("%s must be a positive integer", name)
		}
		*destination = parsed
	}
	return nil
}

func runContextCommand(ctx context.Context, arguments []string, workingDirectory func() (string, error), output, diagnostics io.Writer) error {
	if len(arguments) > 0 && arguments[0] == "worker" {
		return runContextWorkerCommand(arguments[1:])
	}
	invocation, err := parseContextInvocation(arguments)
	if err != nil {
		return err
	}
	if invocation.kind == 0 {
		return writeReport(output, contextHelp)
	}
	if invocation.kind == contextCommandHook {
		return runContextHook(ctx, invocation.options, output, diagnostics)
	}
	if invocation.kind == contextCommandSetup {
		return runContextSetup(ctx, invocation.options, output, diagnostics)
	}
	if invocation.kind == contextCommandServe {
		return runContextRuntimeCommand(ctx, invocation, "", output, diagnostics)
	}
	root, err := workingDirectory()
	if err != nil {
		return fmt.Errorf("resolve context working directory: %w", err)
	}
	if invocation.options.path != "" {
		root = invocation.options.path
	}
	root, err = absoluteDirectory(root)
	if err != nil {
		return err
	}
	switch invocation.kind {
	case contextCommandInit:
		return runContextInitContext(ctx, root, invocation.options, output)
	case contextCommandStatus:
		return runContextStatus(ctx, root, invocation.options, output)
	case contextCommandCacheStatus, contextCommandCacheClear:
		return runContextRuntimeCommand(ctx, invocation, root, output, diagnostics)
	case contextCommandHistory, contextCommandInspectContribution, contextCommandInspectTurn,
		contextCommandExplainProfile:
		return runContextRuntimeCommand(ctx, invocation, root, output, diagnostics)
	default:
		return errors.New(contextHelp)
	}
}

func absoluteDirectory(path string) (string, error) {
	if path == "" {
		return "", errors.New("context path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve context path %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("context path %q is unavailable: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("context path %q is not a directory", abs)
	}
	return filepath.Clean(abs), nil
}

type contextInitReport struct {
	Path       string                      `json:"path"`
	Created    bool                        `json:"created"`
	Activation contextapi.ActivationResult `json:"activation"`
}

const minimalProjectDeclaration = `amends "workbench:context"

scope = "subtree"

contributors {
  ["project-guidance"] = new AiContext {}
}
`

func runContextInit(root string, options contextOptions, output io.Writer) error {
	return runContextInitContext(context.Background(), root, options, output)
}

func runContextInitContext(ctx context.Context, root string, options contextOptions, output io.Writer) error {
	path := filepath.Join(root, "workbench-context.pkl")
	paths, err := contextPaths(options)
	if err != nil {
		return err
	}
	deps, err := contextLoadDependencies(paths)
	if err != nil {
		return err
	}
	_, statErr := os.Lstat(path)
	exists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect project context declaration %q: %w", path, statErr)
	}
	loaded, err := contextconfig.Load(ctx, contextconfig.LoadOptions{WorkingDirectory: root, HomeConfigPath: paths.HomeConfigPath, Now: time.Now().UTC()}, deps)
	if err != nil {
		return fmt.Errorf("load context activation: %w", err)
	}
	created := false
	if !exists && !initMustPreserve(loaded) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create project context directory: %w", err)
		}
		published, err := writeAtomicIfAbsent(path, []byte(minimalProjectDeclaration), 0o644)
		if err != nil {
			return fmt.Errorf("write project context declaration %q: %w", path, err)
		}
		created = published
		loaded, err = contextconfig.Load(ctx, contextconfig.LoadOptions{WorkingDirectory: root, HomeConfigPath: paths.HomeConfigPath, Now: time.Now().UTC()}, deps)
		if err != nil {
			return fmt.Errorf("resolve initialized context activation: %w", err)
		}
	}
	report := contextInitReport{Path: path, Created: created, Activation: loaded.Result}
	if options.json {
		return writeJSONReport(output, report)
	}
	if created {
		if err := writeReport(output, fmt.Sprintf("Created project declaration: %s", path)); err != nil {
			return err
		}
	} else if exists {
		if err := writeReport(output, fmt.Sprintf("Preserved project declaration: %s", path)); err != nil {
			return err
		}
	} else {
		if err := writeReport(output, "Preserved the applicable ancestor/home declaration; no local project declaration was created."); err != nil {
			return err
		}
	}
	return writeReport(output, fmt.Sprintf("Activation: %s", report.Activation.State))
}

// writeAtomicIfAbsent publishes a new declaration without replacing a file
// that appeared after discovery. The final hard-link is the no-clobber point;
// a racing actor therefore keeps its authored bytes and init simply reloads.
func writeAtomicIfAbsent(path string, data []byte, mode os.FileMode) (bool, error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".workbench-context-init-")
	if err != nil {
		return false, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Link(temporaryName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func initMustPreserve(loaded contextconfig.LoadResult) bool {
	if len(loaded.Input.Config.Projects) > 0 || loaded.Result.Scope.CanonicalRoot != "" {
		return true
	}
	if loaded.Result.State == contextapi.ActivationInvalid {
		return true
	}
	for _, reason := range loaded.Result.Reasons {
		if reason.Code == contextapi.ReasonExcluded {
			return true
		}
	}
	return false
}

type contextStatusReport struct {
	WorkingDirectory string                      `json:"workingDirectory"`
	Paths            contextPathReport           `json:"paths"`
	Activation       contextapi.ActivationResult `json:"activation"`
	Runtime          contextRuntimeStatusReport  `json:"runtime"`
}

type contextRuntimeStatusReport struct {
	State  string                `json:"state"`
	Error  string                `json:"error,omitempty"`
	Status *contextdaemon.Status `json:"status,omitempty"`
}

type contextPathReport struct {
	HomeConfigPath string `json:"homeConfigPath,omitempty"`
	RuntimeDir     string `json:"runtimeDir,omitempty"`
	SocketPath     string `json:"socketPath,omitempty"`
	StartLockPath  string `json:"startLockPath,omitempty"`
	ServerLockPath string `json:"serverLockPath,omitempty"`
	CacheDir       string `json:"cacheDir,omitempty"`
}

func runContextStatus(ctx context.Context, root string, options contextOptions, output io.Writer) error {
	paths, err := contextPaths(options)
	if err != nil {
		return err
	}
	deps, err := contextLoadDependencies(paths)
	if err != nil {
		return err
	}
	loaded, err := contextconfig.Load(ctx, contextconfig.LoadOptions{WorkingDirectory: root, HomeConfigPath: paths.HomeConfigPath, Now: time.Now().UTC()}, deps)
	if err != nil {
		return fmt.Errorf("load context activation: %w", err)
	}
	report := contextStatusReport{WorkingDirectory: loaded.Input.WorkingDirectory, Paths: contextPathReport{HomeConfigPath: paths.HomeConfigPath, RuntimeDir: paths.RuntimeDir, SocketPath: paths.SocketPath, StartLockPath: paths.LockPath, ServerLockPath: paths.ServerLockPath, CacheDir: paths.CacheDir}, Activation: contextStatusActivation(loaded.Result), Runtime: contextRuntimeStatusReport{State: "not-checked"}}
	if loaded.Result.State == contextapi.ActivationEnabled {
		report.Runtime = readExistingRuntimeStatus(ctx, paths, options, loaded.Input.WorkingDirectory)
	}
	if options.json {
		return writeJSONReport(output, report)
	}
	return writeContextStatus(output, report)
}

func contextStatusActivation(result contextapi.ActivationResult) contextapi.ActivationResult {
	result.Reasons = append([]contextapi.Reason(nil), result.Reasons...)
	for index := range result.Reasons {
		if result.Reasons[index].Code == contextapi.ReasonProviderNotSelected && strings.Contains(result.Reasons[index].Summary, "no enabled contributors") {
			result.Reasons[index].Summary = "empty selection: " + result.Reasons[index].Summary
		}
	}
	return result
}

func writeContextStatus(output io.Writer, report contextStatusReport) error {
	if err := writeReport(output, "Working directory: "+report.WorkingDirectory); err != nil {
		return err
	}
	if err := writeReport(output, "Activation: "+string(report.Activation.State)); err != nil {
		return err
	}
	if report.Activation.Scope.CanonicalRoot != "" {
		if err := writeReport(output, fmt.Sprintf("Scope: %s (%s)", report.Activation.Scope.CanonicalRoot, report.Activation.Scope.Authority)); err != nil {
			return err
		}
	}
	if report.Activation.State == contextapi.ActivationEnabled {
		profileProviders := 0
		for _, provider := range report.Activation.Effective.Providers {
			for _, capability := range provider.Capabilities {
				if capability == contextapi.ProviderCapabilityProfile {
					profileProviders++
					break
				}
			}
		}
		if err := writeReport(output, fmt.Sprintf("Providers: %d; profile providers: %d", len(report.Activation.Effective.Providers), profileProviders)); err != nil {
			return err
		}
	}
	if err := writeContextRuntimeStatus(output, report); err != nil {
		return err
	}
	for _, reason := range report.Activation.Reasons {
		if err := writeReport(output, fmt.Sprintf("Reason [%s]: %s", reason.Code, reason.Summary)); err != nil {
			return err
		}
	}
	if report.Activation.State != contextapi.ActivationEnabled && len(report.Activation.Reasons) == 0 {
		return writeReport(output, "Reason: no applicable opt-in declaration")
	}
	return nil
}

func readExistingRuntimeStatus(ctx context.Context, paths contextdaemon.Paths, options contextOptions, workingDirectory string) contextRuntimeStatusReport {
	report := contextRuntimeStatusReport{State: "not-running"}
	if ctx == nil {
		ctx = context.Background()
	}
	dialTimeout, err := contextDurationOption(options.dial, "WORKBENCH_CONTEXT_DIAL_TIMEOUT", defaultContextDialTimeout)
	if err != nil {
		report.State = "unavailable"
		report.Error = err.Error()
		return report
	}
	maxWireBytes := options.maxWireBytes
	if maxWireBytes == 0 {
		maxWireBytes, err = contextIntOption("WORKBENCH_CONTEXT_MAX_WIRE_BYTES", defaultContextMaxWireBytes)
		if err != nil {
			report.State = "unavailable"
			report.Error = err.Error()
			return report
		}
	}
	statusContext, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	client, err := contextdaemon.Connect(statusContext, contextdaemon.ClientOptions{Paths: paths, DialTimeout: dialTimeout, MaxWireBytes: maxWireBytes})
	if err != nil {
		report.State = contextDaemonErrorState(err)
		report.Error = err.Error()
		return report
	}
	defer client.Close()
	request := contextdaemon.StatusRequest{WorkingDirectory: workingDirectory}
	if options.scope != "" {
		request.Scope = contextapi.ScopeIdentity{ID: contextapi.ScopeID(options.scope)}
	}
	if options.audience != "" {
		request.Audience = contextapi.Audience{ID: contextapi.AudienceID(options.audience)}
	}
	status, err := client.Status(statusContext, request)
	if err != nil {
		report.State = contextDaemonErrorState(err)
		report.Error = err.Error()
		return report
	}
	report.State = "active"
	report.Status = &status
	return report
}

func contextDaemonErrorState(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "unavailable"
	}
	return "not-running"
}

func writeContextRuntimeStatus(output io.Writer, report contextStatusReport) error {
	runtime := report.Runtime
	switch runtime.State {
	case "not-checked":
		return writeReport(output, fmt.Sprintf("Runtime: not checked (%s scope; no daemon startup or dial)", report.Activation.State))
	case "active":
		if runtime.Status == nil {
			return writeReport(output, "Runtime: active (status unavailable)")
		}
		status := runtime.Status
		if err := writeReport(output, fmt.Sprintf("Runtime: active; generation %d; socket %s", status.Generation, status.SocketPath)); err != nil {
			return err
		}
		if err := writeReport(output, fmt.Sprintf("Queue: %d items/%d bytes; providers: %d; trace dropped: %d", status.TraceQueueItems, status.TraceQueueBytes, status.ProviderProcesses, status.TraceDropped)); err != nil {
			return err
		}
		if len(status.Partitions) == 0 {
			return writeReport(output, "Partitions: none resident for this scope/audience")
		}
		for _, partition := range status.Partitions {
			validUntil := "not set"
			if !partition.Profile.ValidUntil.IsZero() {
				validUntil = partition.Profile.ValidUntil.UTC().Format(time.RFC3339Nano)
			}
			if err := writeReport(output, fmt.Sprintf("Partition: audience %s epoch %d; profile %s (%d facts); valid until %s; pending %d; live %d; receipts %d", partition.Audience.ID, partition.Audience.Epoch, partition.Profile.Revision, len(partition.Profile.Facts), validUntil, partition.Engine.PendingItems, partition.Engine.LiveOffers, partition.Engine.Receipts)); err != nil {
				return err
			}
		}
		return nil
	default:
		if runtime.Error == "" {
			return writeReport(output, "Runtime: "+runtime.State+" (socket not available)")
		}
		return writeReport(output, fmt.Sprintf("Runtime: %s (%s)", runtime.State, runtime.Error))
	}
}

func decodeJSONObject(raw []byte, destination *map[string]json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	if *destination == nil {
		return errors.New("JSON value is not an object")
	}
	return nil
}

func contextPaths(options contextOptions) (contextdaemon.Paths, error) {
	homeConfig := optionOrEnv(options.homeConfig, "WORKBENCH_CONTEXT_HOME_CONFIG")
	if homeConfig != "" && filepath.Ext(homeConfig) == ".json" {
		return contextdaemon.Paths{}, fmt.Errorf("explicit home path %q is obsolete JSON; use workbench-context.pkl", homeConfig)
	}
	runtimeDir := optionOrEnv(options.runtimeDir, "WORKBENCH_CONTEXT_RUNTIME_DIR")
	socket := optionOrEnv(options.socket, "WORKBENCH_CONTEXT_SOCKET")
	startLock := optionOrEnv(options.startLock, "WORKBENCH_CONTEXT_START_LOCK")
	serverLock := optionOrEnv(options.serverLock, "WORKBENCH_CONTEXT_SERVER_LOCK")
	cacheDir := optionOrEnv(options.cacheDir, "WORKBENCH_CONTEXT_CACHE_DIR")
	if homeConfig == "" {
		configHome, err := xdgDirectory("XDG_CONFIG_HOME", ".config")
		if err != nil {
			return contextdaemon.Paths{}, err
		}
		homeConfig = filepath.Join(configHome, "workbench", "workbench-context.pkl")
	}
	if runtimeDir == "" {
		if value := os.Getenv("XDG_RUNTIME_DIR"); value != "" {
			runtimeDir = filepath.Join(value, "workbench")
		} else {
			cacheHome, err := xdgDirectory("XDG_CACHE_HOME", ".cache")
			if err != nil {
				return contextdaemon.Paths{}, err
			}
			runtimeDir = filepath.Join(cacheHome, "workbench", "runtime")
		}
	}
	if cacheDir == "" {
		cacheHome, err := xdgDirectory("XDG_CACHE_HOME", ".cache")
		if err != nil {
			return contextdaemon.Paths{}, err
		}
		cacheDir = filepath.Join(cacheHome, "workbench", "context")
	}
	paths := contextdaemon.DefaultPaths(runtimeDir)
	paths.HomeConfigPath = homeConfig
	paths.CacheDir = cacheDir
	if socket != "" {
		paths.SocketPath = socket
	}
	if startLock != "" {
		paths.LockPath = startLock
	}
	if serverLock != "" {
		paths.ServerLockPath = serverLock
	}
	pathValues := []struct {
		name  string
		value string
		set   func(string)
	}{
		{name: "home config", value: paths.HomeConfigPath, set: func(value string) { paths.HomeConfigPath = value }},
		{name: "runtime directory", value: paths.RuntimeDir, set: func(value string) { paths.RuntimeDir = value }},
		{name: "socket", value: paths.SocketPath, set: func(value string) { paths.SocketPath = value }},
		{name: "start lock", value: paths.LockPath, set: func(value string) { paths.LockPath = value }},
		{name: "server lock", value: paths.ServerLockPath, set: func(value string) { paths.ServerLockPath = value }},
		{name: "cache directory", value: paths.CacheDir, set: func(value string) { paths.CacheDir = value }},
	}
	for _, pathValue := range pathValues {
		name, path := pathValue.name, pathValue.value
		if path == "" {
			return contextdaemon.Paths{}, fmt.Errorf("%s path is empty", name)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return contextdaemon.Paths{}, fmt.Errorf("resolve %s path %q: %w", name, path, err)
		}
		pathValue.set(absolute)
	}
	return paths, nil
}

func xdgDirectory(variable, fallback string) (string, error) {
	if value := os.Getenv(variable); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home for %s: %w", variable, err)
	}
	return filepath.Abs(filepath.Join(home, fallback))
}

func optionOrEnv(option, variable string) string {
	if option != "" {
		return option
	}
	return os.Getenv(variable)
}

func writeAtomicPreserving(path string, data []byte, defaultMode os.FileMode) error {
	mode := defaultMode
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".workbench-context-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	remove = false
	return nil
}

type contextSetupReport struct {
	Executable string                 `json:"executable"`
	Paths      contextPathReport      `json:"paths"`
	Harnesses  []string               `json:"harnesses"`
	Commands   map[string]string      `json:"commands"`
	Changed    []string               `json:"changed"`
	NextSteps  []contextSetupNextStep `json:"nextSteps,omitempty"`
	DryRun     bool                   `json:"dryRun"`
}

type contextSetupNextStep struct {
	Harness       string `json:"harness"`
	Action        string `json:"action"`
	Documentation string `json:"documentation"`
}

const codexHooksDocumentation = "https://learn.chatgpt.com/docs/hooks"

func contextSetupNextSteps(harnesses []string) []contextSetupNextStep {
	for _, harness := range harnesses {
		if harness == "codex" {
			return []contextSetupNextStep{{
				Harness:       "codex",
				Action:        "Review and trust the exact installed hooks via native /hooks; untrusted hooks remain skipped.",
				Documentation: codexHooksDocumentation,
			}}
		}
	}
	return nil
}

func runContextSetup(_ context.Context, options contextOptions, output, _ io.Writer) error {
	paths, err := contextPaths(options)
	if err != nil {
		return err
	}
	executable := optionOrEnv(options.executable, "WORKBENCH_CONTEXT_EXECUTABLE")
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return fmt.Errorf("resolve workbench executable: %w", err)
		}
	}
	executable, err = stableExecutablePath(executable)
	if err != nil {
		return err
	}
	harness := options.harness
	if harness == "" {
		harness = "both"
	}
	report := contextSetupReport{Executable: executable, Paths: contextPathReport{HomeConfigPath: paths.HomeConfigPath, RuntimeDir: paths.RuntimeDir, SocketPath: paths.SocketPath, StartLockPath: paths.LockPath, ServerLockPath: paths.ServerLockPath, CacheDir: paths.CacheDir}, Harnesses: setupHarnesses(harness), Commands: make(map[string]string), DryRun: options.dryRun}
	report.NextSteps = contextSetupNextSteps(report.Harnesses)
	for _, selected := range report.Harnesses {
		command := contextHookCommand(executable, selected, paths)
		report.Commands[selected] = command
		switch selected {
		case "claude":
			settings := optionOrEnv(options.claudeSetting, "WORKBENCH_CONTEXT_CLAUDE_SETTINGS")
			if settings == "" {
				home, homeErr := os.UserHomeDir()
				if homeErr != nil {
					return fmt.Errorf("resolve Claude settings home: %w", homeErr)
				}
				settings = filepath.Join(home, ".claude", "settings.json")
			}
			settings, err = absolutePath(settings)
			if err != nil {
				return fmt.Errorf("Claude settings: %w", err)
			}
			changed, err := reconcileJSONHooks(settings, claudeHookEvents, command, false, options.dryRun)
			if err != nil {
				return err
			}
			if changed {
				report.Changed = append(report.Changed, settings)
			}
		case "codex":
			codexHome := optionOrEnv(options.codexHome, "WORKBENCH_CONTEXT_CODEX_HOME")
			if codexHome == "" {
				if value := os.Getenv("CODEX_HOME"); value != "" {
					codexHome = value
				} else {
					home, homeErr := os.UserHomeDir()
					if homeErr != nil {
						return fmt.Errorf("resolve Codex home: %w", homeErr)
					}
					codexHome = filepath.Join(home, ".codex")
				}
			}
			codexHome, err = absolutePath(codexHome)
			if err != nil {
				return fmt.Errorf("Codex home: %w", err)
			}
			if !options.dryRun {
				if err := os.MkdirAll(codexHome, 0o700); err != nil {
					return fmt.Errorf("create Codex home %q: %w", codexHome, err)
				}
			}
			hooksPath := filepath.Join(codexHome, "hooks.json")
			changed, err := reconcileJSONHooks(hooksPath, codexHookEvents, command, true, options.dryRun)
			if err != nil {
				return err
			}
			if changed {
				report.Changed = append(report.Changed, hooksPath)
			}
			configPath := optionOrEnv(options.codexConfig, "WORKBENCH_CONTEXT_CODEX_CONFIG")
			if configPath == "" {
				configPath = filepath.Join(codexHome, "config.toml")
			} else {
				configPath, err = absolutePath(configPath)
				if err != nil {
					return fmt.Errorf("Codex config: %w", err)
				}
			}
			changed, err = reconcileCodexConfig(configPath, options.dryRun)
			if err != nil {
				return err
			}
			if changed {
				report.Changed = append(report.Changed, configPath)
			}
		}
	}
	if options.json {
		return writeJSONReport(output, report)
	}
	if options.dryRun {
		if err := writeReport(output, "Workbench context setup validated (dry run; no files written)."); err != nil {
			return err
		}
	} else if err := writeReport(output, "Workbench context setup reconciled."); err != nil {
		return err
	}
	if err := writeReport(output, "Executable: "+report.Executable); err != nil {
		return err
	}
	if err := writeReport(output, "Home config: "+report.Paths.HomeConfigPath); err != nil {
		return err
	}
	if err := writeReport(output, "Runtime: "+report.Paths.RuntimeDir); err != nil {
		return err
	}
	if err := writeReport(output, "Socket: "+report.Paths.SocketPath); err != nil {
		return err
	}
	if err := writeReport(output, "Start lock: "+report.Paths.StartLockPath); err != nil {
		return err
	}
	if err := writeReport(output, "Server lock: "+report.Paths.ServerLockPath); err != nil {
		return err
	}
	if err := writeReport(output, "Cache: "+report.Paths.CacheDir); err != nil {
		return err
	}
	for _, harness := range report.Harnesses {
		if err := writeReport(output, harness+" hook: "+report.Commands[harness]); err != nil {
			return err
		}
	}
	for _, nextStep := range report.NextSteps {
		if err := writeReport(output, fmt.Sprintf("Next step (%s): %s", nextStep.Harness, nextStep.Action)); err != nil {
			return err
		}
		if err := writeReport(output, "Documentation: "+nextStep.Documentation); err != nil {
			return err
		}
	}
	if len(report.Changed) == 0 {
		return writeReport(output, "No owned settings changed.")
	}
	return writeReport(output, fmt.Sprintf("Updated %d settings path%s.", len(report.Changed), plural(len(report.Changed), "", "s")))
}

var claudeHookEvents = []string{"PostToolBatch", "PostToolUse", "PostToolUseFailure", "SessionStart", "Stop"}
var codexHookEvents = []string{"PostToolUse", "UserPromptSubmit", "SessionStart"}

func setupHarnesses(value string) []string {
	if value == "claude" {
		return []string{"claude"}
	}
	if value == "codex" {
		return []string{"codex"}
	}
	return []string{"claude", "codex"}
}

func stableExecutablePath(path string) (string, error) {
	abs, err := absolutePath(path)
	if err != nil {
		return "", fmt.Errorf("resolve Workbench executable %q: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve Workbench executable %q: %w", abs, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect Workbench executable %q: %w", resolved, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("Workbench executable %q is not executable", resolved)
	}
	return resolved, nil
}

func absolutePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func contextHookCommand(executable, harness string, paths contextdaemon.Paths) string {
	parts := []string{shellQuote(executable), "context", "hook", "--harness", harness, "--home-config", shellQuote(paths.HomeConfigPath), "--runtime-dir", shellQuote(paths.RuntimeDir), "--socket", shellQuote(paths.SocketPath), "--start-lock", shellQuote(paths.LockPath), "--server-lock", shellQuote(paths.ServerLockPath), "--cache-dir", shellQuote(paths.CacheDir)}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func reconcileJSONHooks(path string, events []string, command string, codex, dryRun bool) (bool, error) {
	raw, exists, mode, err := readOptionalSettings(path)
	if err != nil {
		return false, fmt.Errorf("read hook settings %q: %w", path, err)
	}
	root := make(map[string]json.RawMessage)
	if exists {
		if err := decodeJSONObject(raw, &root); err != nil {
			return false, fmt.Errorf("validate hook settings %q: %w", path, err)
		}
	}
	hooks := make(map[string]json.RawMessage)
	if encoded, present := root["hooks"]; present {
		if err := decodeJSONObject(encoded, &hooks); err != nil {
			return false, fmt.Errorf("validate hooks object in %q: %w", path, err)
		}
	}
	changed := false
	for _, event := range events {
		updated, eventChanged, err := reconcileHookEvent(hooks[event], event, command, codex)
		if err != nil {
			return false, fmt.Errorf("reconcile %s in %q: %w", event, path, err)
		}
		if eventChanged {
			changed = true
		}
		hooks[event] = updated
	}
	encodedHooks, err := json.Marshal(hooks)
	if err != nil {
		return false, err
	}
	if !jsonRawEqual(root["hooks"], encodedHooks) {
		root["hooks"] = encodedHooks
		changed = true
	}
	if !changed {
		return false, nil
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode hook settings %q: %w", path, err)
	}
	encoded = append(encoded, '\n')
	if !exists {
		mode = 0o600
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create settings directory: %w", err)
	}
	if err := writeAtomicPreserving(path, encoded, mode); err != nil {
		return false, fmt.Errorf("write hook settings %q: %w", path, err)
	}
	return true, nil
}

func readOptionalSettings(path string) ([]byte, bool, os.FileMode, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, 0, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, 0, err
	}
	return contents, true, info.Mode().Perm(), nil
}

func reconcileHookEvent(raw json.RawMessage, event, command string, codex bool) ([]byte, bool, error) {
	groups := make([]json.RawMessage, 0)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &groups); err != nil {
			return nil, false, err
		}
	}
	changed := false
	found := false
	harness := eventHarness(command, event)
	newCommand, err := encodeHookCommand(command, codex)
	if err != nil {
		return nil, false, err
	}
	for index, encodedGroup := range groups {
		var group map[string]json.RawMessage
		if err := decodeJSONObject(encodedGroup, &group); err != nil {
			return nil, false, err
		}
		var commands []json.RawMessage
		if encoded, present := group["hooks"]; present {
			if err := json.Unmarshal(encoded, &commands); err != nil {
				return nil, false, err
			}
		}
		filtered := make([]json.RawMessage, 0, len(commands))
		groupChanged := false
		for _, encodedCommand := range commands {
			var hook map[string]json.RawMessage
			if err := decodeJSONObject(encodedCommand, &hook); err != nil {
				return nil, false, err
			}
			var hookCommand string
			_ = json.Unmarshal(hook["command"], &hookCommand)
			if isOwnedContextCommand(hookCommand, harness) {
				if !found {
					filtered = append(filtered, newCommand)
					if !jsonRawEqual(encodedCommand, newCommand) {
						groupChanged = true
					}
					found = true
				} else {
					groupChanged = true
				}
				continue
			}
			filtered = append(filtered, encodedCommand)
		}
		if groupChanged {
			encoded, _ := json.Marshal(filtered)
			group["hooks"] = encoded
			updated, _ := json.Marshal(group)
			groups[index] = updated
			changed = true
		}
	}
	if !found {
		group := map[string]json.RawMessage{}
		group["hooks"], _ = json.Marshal([]json.RawMessage{newCommand})
		encodedGroup, _ := json.Marshal(group)
		groups = append(groups, encodedGroup)
		changed = true
	}
	encoded, err := json.Marshal(groups)
	return encoded, changed || !jsonRawEqual(encoded, raw), err
}

func jsonRawEqual(left, right []byte) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return bytes.Equal(left, right)
	}
	return bytes.Equal(mustMarshalJSON(leftValue), mustMarshalJSON(rightValue))
}

func mustMarshalJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func eventHarness(command, _ string) string {
	if strings.Contains(command, "--harness codex") {
		return "codex"
	}
	return "claude"
}

func isOwnedContextCommand(command, harness string) bool {
	return strings.Contains(command, " context hook --harness "+harness)
}

func encodeHookCommand(command string, codex bool) ([]byte, error) {
	hook := map[string]json.RawMessage{}
	hook["type"], _ = json.Marshal("command")
	hook["command"], _ = json.Marshal(command)
	if codex {
		hook["additionalContextLimit"], _ = json.Marshal(0)
	}
	return json.Marshal(hook)
}

func reconcileCodexConfig(path string, dryRun bool) (bool, error) {
	raw, exists, mode, err := readOptionalSettings(path)
	if err != nil {
		return false, fmt.Errorf("read Codex config %q: %w", path, err)
	}
	if exists {
		var decoded map[string]any
		if err := toml.Unmarshal(raw, &decoded); err != nil {
			return false, fmt.Errorf("validate Codex config %q: %w", path, err)
		}
	}
	updated, changed, err := setTOMLFeatureHooks(raw)
	if err != nil {
		return false, fmt.Errorf("reconcile features.hooks in %q: %w", path, err)
	}
	if !changed {
		return false, nil
	}
	if !exists {
		mode = 0o600
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create Codex config directory: %w", err)
	}
	if err := writeAtomicPreserving(path, updated, mode); err != nil {
		return false, fmt.Errorf("write Codex config %q: %w", path, err)
	}
	return true, nil
}

func setTOMLFeatureHooks(raw []byte) ([]byte, bool, error) {
	if len(raw) == 0 {
		return []byte("[features]\nhooks = true\n"), true, nil
	}
	lines := strings.SplitAfter(string(raw), "\n")
	section := ""
	featuresFound := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			if section == "features" {
				featuresFound = true
			}
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if (section == "" && key == "features.hooks") || (section == "features" && key == "hooks") {
			newline := "\n"
			if !strings.HasSuffix(line, "\n") {
				newline = ""
			}
			prefix := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[index] = prefix + key + " = true" + newline
			if strings.TrimSpace(strings.SplitN(trimmed, "=", 2)[1]) == "true" {
				return raw, false, nil
			}
			return []byte(strings.Join(lines, "")), true, nil
		}
	}
	if featuresFound {
		for index, line := range lines {
			trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
			if trimmed == "[features]" {
				insert := index + 1
				for insert < len(lines) {
					next := strings.TrimSpace(strings.TrimSuffix(lines[insert], "\n"))
					if strings.HasPrefix(next, "[") && strings.HasSuffix(next, "]") {
						break
					}
					insert++
				}
				lines = append(lines, "")
				copy(lines[insert+1:], lines[insert:])
				lines[insert] = "hooks = true\n"
				return []byte(strings.Join(lines, "")), true, nil
			}
		}
	}
	if strings.Contains(string(raw), "features = {") {
		return nil, false, errors.New("inline features table cannot be safely reconciled; use [features] hooks = true")
	}
	separator := ""
	if !bytes.HasSuffix(raw, []byte("\n")) {
		separator = "\n"
	}
	return append(append(append([]byte(nil), raw...), separator...), []byte("[features]\nhooks = true\n")...), true, nil
}
