package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	contextLiveQABaselineRunnerEnv = "WCTX_QA_BASELINE_RUNNER"
	contextLiveQAClaudeEnv         = "WCTX_CLAUDE"
	contextLiveQAPklEnv            = "PKL_EXECUTABLE"
	contextLiveQABaselineDeadline  = 2 * time.Minute
	contextLiveQAOutputLimit       = 8 << 20
)

// TestContextLiveQACapture is opt-in because it launches a real native Claude
// process against the controlled loopback endpoint used by the checked-in
// context-live-qa runner. It deliberately does not use a /tmp default: the
// ordinary Go suite must not assume a provider install or private Pkl setup.
func TestContextLiveQACaptureRED(t *testing.T) {
	runnerPath, claudePath, pklPath, selected := contextLiveQAInputs()
	if !selected {
		t.Skip("controlled-native QA fixture not selected; set WCTX_QA_BASELINE_RUNNER, WCTX_CLAUDE, and PKL_EXECUTABLE")
	}
	for name, path := range map[string]string{
		contextLiveQABaselineRunnerEnv: runnerPath,
		contextLiveQAClaudeEnv:         claudePath,
		contextLiveQAPklEnv:            pklPath,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s=%q is unavailable: %v", name, path, err)
		}
		if info.IsDir() {
			t.Fatalf("%s=%q is a directory", name, path)
		}
		if !filepath.IsAbs(path) {
			t.Fatalf("%s=%q must be absolute", name, path)
		}
	}

	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	workbench := buildContextWorkbench(t, moduleRoot)
	ownedTempRoot, err := os.MkdirTemp("/tmp", "cqa-")
	if err != nil {
		t.Fatalf("prepare short isolated TMPDIR: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(ownedTempRoot); err != nil {
			t.Errorf("remove owned controlled QA temp root %q: %v", ownedTempRoot, err)
		}
	})

	command := exec.Command("python3", runnerPath,
		"--workbench", workbench,
		"--claude", claudePath,
		"--harness", "claude",
		"--mode", "controlled",
		"--output", ownedTempRoot,
		"--deadline-seconds", "90",
		"--max-output-bytes", fmt.Sprint(contextLiveQAOutputLimit),
	)
	command.Dir = ownedTempRoot
	command.Env = contextLiveQAEnvironment(workbench, claudePath, ownedTempRoot)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr boundedContextLiveQAOutput
	stdout.limit = contextLiveQAOutputLimit
	stderr.limit = contextLiveQAOutputLimit
	command.Stdout = &stdout
	command.Stderr = &stderr

	result := runContextLiveQAProcess(t, command, contextLiveQABaselineDeadline)
	if result.groupJoined == false {
		t.Fatalf("controlled QA baseline left an owned process group alive: %s", result.describe())
	}
	if result.timedOut {
		t.Fatalf("controlled QA baseline exceeded %s: %s", contextLiveQABaselineDeadline, result.describe())
	}
	if result.err != nil {
		t.Fatalf("context-live-qa runner failed before capture assertion: %s\nstdout=%q\nstderr=%q", result.describe(), stdout.String(), stderr.String())
	}
	if stdout.truncated || stderr.truncated {
		t.Fatalf("controlled QA baseline exceeded bounded output: %s", result.describe())
	}

	var summary struct {
		Base          string          `json:"base"`
		Required      map[string]bool `json:"required"`
		ObservedUnion []string        `json:"observed_union"`
		Cases         []struct {
			Name string `json:"name"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("context-live-qa runner did not emit JSON summary: %v\nstdout=%q", err, stdout.String())
	}
	if summary.Base == "" {
		t.Fatal("context-live-qa runner summary omitted its artifact root")
	}
	expectedRequired := []string{
		"setup_exit_success",
		"all_five_installed",
		"success_claude_exit",
		"failure_claude_exit",
		"all_five_seen_across_cases",
		"request2_token",
		"history_command_success",
		"history_nonempty",
	}
	if len(summary.Required) == 0 {
		t.Fatal("context-live-qa runner summary omitted all named required predicates")
	}
	for _, required := range expectedRequired {
		passed, present := summary.Required[required]
		if !present {
			t.Fatalf("context-live-qa runner summary omitted required predicate %q", required)
		}
		if !passed {
			t.Fatalf("context-live-qa runner did not prove %s", required)
		}
	}
	for _, caseName := range []string{"success", "failure"} {
		if !contextLiveQACaseNamed(summary.Cases, caseName) {
			t.Fatalf("context-live-qa runner summary omitted actual %s case evidence", caseName)
		}
	}
	if !sameStrings(summary.ObservedUnion, []string{"PostToolBatch", "PostToolUse", "PostToolUseFailure", "SessionStart", "Stop"}) {
		t.Fatalf("context-live-qa runner summary native event evidence = %v, want all five generated events", summary.ObservedUnion)
	}

	canonicalTempRoot, err := filepath.EvalSymlinks(ownedTempRoot)
	if err != nil {
		t.Fatalf("canonicalize controlled QA temp root %q: %v", ownedTempRoot, err)
	}
	canonicalBase, err := filepath.EvalSymlinks(summary.Base)
	if err != nil {
		t.Fatalf("canonicalize controlled QA artifact root %q: %v", summary.Base, err)
	}
	if !contextLiveQAWithin(canonicalTempRoot, canonicalBase) {
		t.Fatalf("context-live-qa runner artifact root escaped owned temp root: root=%q artifact=%q", canonicalTempRoot, canonicalBase)
	}
	manifestPath := filepath.Join(canonicalBase, "capture-manifest.json")

	if _, err := os.Stat(manifestPath); err == nil {
		if info, statErr := os.Stat(manifestPath); statErr != nil || info.IsDir() {
			t.Fatalf("selected QA runner reported an invalid capture manifest path %s: %v", manifestPath, statErr)
		}
		manifest, readErr := os.ReadFile(manifestPath)
		if readErr != nil {
			t.Fatalf("read selected QA runner capture manifest %s: %v", manifestPath, readErr)
		}
		validateContextLiveQAManifest(t, manifest)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect expected capture manifest %s: %v", manifestPath, err)
	} else {
		t.Fatalf("context-live-qa runner completed at %s, but the required capture-manifest.json is absent at %s", canonicalBase, manifestPath)
	}
}

func contextLiveQAInputs() (runner, claude, pkl string, selected bool) {
	runner = strings.TrimSpace(os.Getenv(contextLiveQABaselineRunnerEnv))
	claude = strings.TrimSpace(os.Getenv(contextLiveQAClaudeEnv))
	pkl = strings.TrimSpace(os.Getenv(contextLiveQAPklEnv))
	return runner, claude, pkl, runner != ""
}

func contextLiveQAEnvironment(workbench, claude, tempRoot string) []string {
	blocked := map[string]bool{
		"ANTHROPIC_API_KEY":       true,
		"ANTHROPIC_AUTH_TOKEN":    true,
		"CLAUDE_CODE_OAUTH_TOKEN": true,
		"CODEX_API_KEY":           true,
		"OPENAI_API_KEY":          true,
		"ANTHROPIC_BASE_URL":      true,
		"TMPDIR":                  true,
		"TMP":                     true,
		"TEMP":                    true,
	}
	environment := make([]string, 0, len(os.Environ())+5)
	for _, value := range os.Environ() {
		name, _, ok := strings.Cut(value, "=")
		if ok && !blocked[name] {
			environment = append(environment, value)
		}
	}
	return append(environment,
		"WCTX_WORKBENCH="+workbench,
		"WCTX_CLAUDE="+claude,
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
		"TMPDIR="+tempRoot,
	)
}

func TestContextLiveQAInputsRequireDedicatedOptIn(t *testing.T) {
	t.Setenv(contextLiveQABaselineRunnerEnv, "")
	t.Setenv(contextLiveQAClaudeEnv, "/controlled/claude")
	t.Setenv(contextLiveQAPklEnv, "/controlled/pkl")
	if _, _, _, selected := contextLiveQAInputs(); selected {
		t.Fatal("PKL_EXECUTABLE and WCTX_CLAUDE alone selected the native QA fixture")
	}
}

func TestContextLiveQAOfflineCounterexamples(t *testing.T) {
	command := exec.Command("python3", "context_live_qa.py", "--self-test-contract")
	command.Dir = filepath.Join("..", "acceptance")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("offline capture contract witness failed: %v", err)
	}
	var witness map[string]any
	if err := json.Unmarshal(output, &witness); err != nil {
		t.Fatalf("offline capture contract witness was not JSON: %v", err)
	}
	for _, key := range []string{"retainedInputActor", "hookExitAloneNotDelivery", "structuredConfirmationLinks", "codexPostToolUseDelivery", "wireUnavailableHasLocators", "authFailureRejected", "intentionalJoinedShutdownAccepted", "strictIdentityAccepted", "stderrOnlyNonzeroRejected", "identityFileUniqueAfterMerge", "startupRuntimeDiagnostic", "irrelevantSuccessExecution", "failedReadRequiresExecution", "inactiveControlsRequireRead", "qaIdleTTL300000"} {
		if value, ok := witness[key].(bool); !ok || !value {
			t.Fatalf("offline capture contract witness %q = %v, want true", key, witness[key])
		}
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func contextLiveQAWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func contextLiveQACaseNamed(cases []struct {
	Name string `json:"name"`
}, want string) bool {
	for _, item := range cases {
		if item.Name == want {
			return true
		}
	}
	return false
}

func validateContextLiveQAManifest(t *testing.T, manifest []byte) {
	t.Helper()
	var value struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		Files         []struct {
			Path string `json:"path"`
		} `json:"files"`
		Sessions []struct {
			Inputs []struct {
				Actor string `json:"actor"`
			} `json:"inputs"`
			Sidecars []struct {
				Path string `json:"path"`
			} `json:"sidecars"`
		} `json:"sessions"`
	}
	if len(manifest) == 0 {
		t.Fatal("selected QA runner wrote an empty capture manifest")
	}
	if err := json.Unmarshal(manifest, &value); err != nil {
		t.Fatalf("selected QA runner wrote malformed capture manifest: %v", err)
	}
	if value.SchemaVersion != 1 || value.Kind != "capture-manifest" {
		t.Fatalf("selected QA runner capture manifest identity = schemaVersion=%d kind=%q, want v1 capture-manifest", value.SchemaVersion, value.Kind)
	}
	if len(value.Files) == 0 || len(value.Sessions) == 0 {
		t.Fatalf("selected QA runner capture manifest lacks files/sessions: files=%d sessions=%d", len(value.Files), len(value.Sessions))
	}
	paths := make(map[string]bool, len(value.Files))
	for _, file := range value.Files {
		if file.Path == "" || paths[file.Path] {
			t.Fatalf("selected QA runner capture manifest has duplicate or empty file path %q", file.Path)
		}
		paths[file.Path] = true
	}
	for _, session := range value.Sessions {
		for _, input := range session.Inputs {
			if input.Actor == "human" {
				t.Fatalf("runner-authored controlled input was relabeled human")
			}
		}
		sidecars := make(map[string]bool, len(session.Sidecars))
		for _, sidecar := range session.Sidecars {
			if sidecar.Path == "" || sidecars[sidecar.Path] {
				t.Fatalf("selected QA runner session has duplicate or empty sidecar path %q", sidecar.Path)
			}
			sidecars[sidecar.Path] = true
		}
	}
}

type boundedContextLiveQAOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (output *boundedContextLiveQAOutput) Write(value []byte) (int, error) {
	if output.limit <= 0 {
		return len(value), nil
	}
	remaining := output.limit - output.Len()
	if remaining <= 0 {
		output.truncated = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = output.Buffer.Write(value[:remaining])
		output.truncated = true
		return len(value), nil
	}
	_, _ = output.Buffer.Write(value)
	return len(value), nil
}

type contextLiveQAProcessResult struct {
	err         error
	timedOut    bool
	groupJoined bool
	pid         int
	started     time.Time
	finished    time.Time
}

func (result contextLiveQAProcessResult) describe() string {
	return fmt.Sprintf("pid=%d timedOut=%t groupJoined=%t duration=%s err=%v", result.pid, result.timedOut, result.groupJoined, result.finished.Sub(result.started).Round(time.Millisecond), result.err)
}

func runContextLiveQAProcess(t *testing.T, command *exec.Cmd, deadline time.Duration) contextLiveQAProcessResult {
	t.Helper()
	started := time.Now()
	if err := command.Start(); err != nil {
		t.Fatalf("start controlled QA baseline: %v", err)
	}
	result := contextLiveQAProcessResult{pid: command.Process.Pid, started: started}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case result.err = <-wait:
	case <-timer.C:
		result.timedOut = true
		terminateContextLiveQAProcessGroup(result.pid)
		result.err = <-wait
	}
	result.groupJoined = terminateContextLiveQAProcessGroup(result.pid)
	result.finished = time.Now()
	return result
}

func terminateContextLiveQAProcessGroup(pid int) bool {
	if pid <= 0 {
		return true
	}
	alive := func() bool {
		err := syscall.Kill(-pid, 0)
		return err == nil || errors.Is(err, syscall.EPERM)
	}
	if !alive() {
		return true
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(3 * time.Second)
	for alive() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		deadline = time.Now().Add(3 * time.Second)
		for alive() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	return !alive()
}
