package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	contextOfferText       = "Keep the context boundary."
	providerOfferText      = "Provider fixture guidance."
	contextTraceOffered    = 5
	contextTraceConfirmed  = 6
	contextTraceSuppressed = 7
)

type acceptanceContextPaths struct {
	homeConfig string
	runtimeDir string
	socket     string
	startLock  string
	serverLock string
	cacheDir   string
}

type acceptanceTraceResult struct {
	Records []acceptanceTraceRecord `json:"Records"`
}

type acceptanceTraceRecord struct {
	Kind           uint16 `json:"Kind"`
	ContributionID uint64 `json:"ContributionID"`
	Profile        string `json:"Profile"`
	Sample         struct {
		Excerpts []struct {
			Text string `json:"Text"`
		} `json:"Excerpts"`
	} `json:"Sample"`
}

type acceptanceStatus struct {
	Runtime struct {
		State  string `json:"state"`
		Error  string `json:"error"`
		Status *struct {
			ActiveScopes      uint32 `json:"activeScopes"`
			ActiveAudiences   uint32 `json:"activeAudiences"`
			ProviderProcesses uint32 `json:"providerProcesses"`
		} `json:"status"`
	} `json:"runtime"`
}

type acceptanceProfileStatus struct {
	Runtime struct {
		State  string `json:"state"`
		Status *struct {
			Partitions []struct {
				Profile struct {
					Revision string `json:"revision"`
					Facts    []struct {
						Key   string `json:"key"`
						Value struct {
							Kind string `json:"kind"`
							Text string `json:"text"`
						} `json:"value"`
						Validity struct {
							Policy    string    `json:"policy"`
							ExpiresAt time.Time `json:"expiresAt"`
						} `json:"validity"`
						Provenance struct {
							Origin   string `json:"origin"`
							Provider string `json:"provider"`
						} `json:"provenance"`
					} `json:"facts"`
				} `json:"profile"`
			} `json:"partitions"`
		} `json:"status"`
	} `json:"runtime"`
}

func (status acceptanceStatus) runtimeFacts() (activeScopes, activeAudiences, providerProcesses uint32, available bool) {
	if status.Runtime.Status == nil {
		return 0, 0, 0, false
	}
	return status.Runtime.Status.ActiveScopes, status.Runtime.Status.ActiveAudiences, status.Runtime.Status.ProviderProcesses, true
}

func TestContextAcceptance(t *testing.T) {
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := buildContextWorkbench(t, moduleRoot)

	t.Run("inactive is silent and creates no context artifacts", func(t *testing.T) {
		root := t.TempDir()
		paths := newAcceptanceContextPaths(root)
		environment := contextAcceptanceEnvironment(root)
		payload := claudeContextHookPayload(root, "inactive-session", "inactive-turn", "")
		var elapsed []time.Duration
		for index := 0; index < 100; index++ {
			started := time.Now()
			stdout, stderr, runErr := runContextProcess(t, binary, root, environment, payload, contextHookArguments(paths, "claude"))
			elapsed = append(elapsed, time.Since(started))
			if runErr != nil {
				t.Fatalf("inactive hook %d failed: %v\nstderr=%s", index, runErr, stderr)
			}
			if len(stdout) != 0 || len(stderr) != 0 {
				t.Fatalf("inactive hook %d emitted stdout=%q stderr=%q", index, stdout, stderr)
			}
		}
		sort.Slice(elapsed, func(i, j int) bool { return elapsed[i] < elapsed[j] })
		// This is a broad CI smoke bound; the acceptance-map p95 target is
		// 30ms and is measured separately on the documented workload.
		if elapsed[94] > 500*time.Millisecond {
			t.Fatalf("inactive p95 = %s, want bounded fast path", elapsed[94])
		}
		for _, path := range []string{paths.homeConfig, paths.runtimeDir, paths.socket, paths.startLock, paths.serverLock, paths.cacheDir, filepath.Join(root, "workbench-context.pkl")} {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("inactive hook created context artifact %q: %v", path, statErr)
			}
		}
	})

	t.Run("cold evaluator lease wait stays inside the whole-hook deadline", func(t *testing.T) {
		root := t.TempDir()
		paths := newAcceptanceContextPaths(root)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "workbench-context.pkl"), builtinProjectDeclaration(), 0o600)
		leasePath := filepath.Join(root, "home", ".cache", "workbench", "context-evaluator.lock")
		if err := os.MkdirAll(filepath.Dir(leasePath), 0o700); err != nil {
			t.Fatal(err)
		}
		lease, err := os.OpenFile(leasePath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		if err := syscall.Flock(int(lease.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		arguments := append(contextHookArguments(paths, "codex"), "--whole-hook-deadline", "50ms")
		stdout, stderr, runErr := runContextProcess(t, binary, root, environment, codexContextHookPayload(root, "lease-session", "lease-turn", ""), arguments)
		if runErr != nil || len(stdout) != 0 || !strings.Contains(string(stderr), "load activation") {
			t.Fatalf("bounded evaluator wait = err %v stdout %q stderr %q", runErr, stdout, stderr)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("cold evaluator wait exceeded whole-hook bound: %s", elapsed)
		}
	})

	t.Run("setup is isolated idempotent and init opts in", func(t *testing.T) {
		root := t.TempDir()
		paths := newAcceptanceContextPaths(root)
		environment := contextAcceptanceEnvironment(root)
		claudeSettings := filepath.Join(root, "claude", "settings.json")
		codexHome := filepath.Join(root, "codex")
		codexConfig := filepath.Join(root, "codex", "config.toml")
		writeAcceptanceFile(t, claudeSettings, `{"permissions":{"allow":["Read"]},"hooks":{"PostToolBatch":[{"matcher":"unrelated","hooks":[{"type":"command","command":"other-command"}]}]}}`, 0o640)
		writeAcceptanceFile(t, codexConfig, "model = \"fixture\"\n[custom]\nvalue = \"preserve\"\n", 0o640)

		firstStdout, firstStderr, err := runContextProcess(t, binary, root, environment, nil, contextSetupArguments(paths, binary, claudeSettings, codexHome, codexConfig, true))
		if err != nil {
			t.Fatalf("first setup failed: %v\nstderr=%s", err, firstStderr)
		}
		first := decodeSetupReport(t, firstStdout)
		if len(first.Changed) == 0 {
			t.Fatalf("first setup reported no changes: %s", firstStdout)
		}
		claudeAfterFirst := readAcceptanceFile(t, claudeSettings)
		codexHooks := readAcceptanceFile(t, filepath.Join(codexHome, "hooks.json"))
		codexAfterFirst := readAcceptanceFile(t, codexConfig)
		if !strings.Contains(claudeAfterFirst, "other-command") || !strings.Contains(claudeAfterFirst, "context hook --harness claude") {
			t.Fatalf("setup did not preserve Claude hooks and add the owned hook: %s", claudeAfterFirst)
		}
		if !strings.Contains(codexHooks, "additionalContextLimit") || !strings.Contains(codexHooks, "context hook --harness codex") {
			t.Fatalf("setup did not install the Codex hook envelope: %s", codexHooks)
		}
		if !strings.Contains(codexAfterFirst, "model = \"fixture\"") || !strings.Contains(codexAfterFirst, "value = \"preserve\"") || !strings.Contains(codexAfterFirst, "hooks = true") {
			t.Fatalf("setup did not preserve Codex config: %s", codexAfterFirst)
		}

		secondStdout, secondStderr, err := runContextProcess(t, binary, root, environment, nil, contextSetupArguments(paths, binary, claudeSettings, codexHome, codexConfig, true))
		if err != nil {
			t.Fatalf("second setup failed: %v\nstderr=%s", err, secondStderr)
		}
		second := decodeSetupReport(t, secondStdout)
		if len(second.Changed) != 0 || readAcceptanceFile(t, claudeSettings) != claudeAfterFirst || readAcceptanceFile(t, codexConfig) != codexAfterFirst {
			t.Fatalf("setup was not byte-idempotent: changed=%v", second.Changed)
		}
		if len(firstStderr) != 0 || len(secondStderr) != 0 {
			t.Fatalf("setup diagnostics were unexpectedly emitted: first=%q second=%q", firstStderr, secondStderr)
		}

		project := filepath.Join(root, "project")
		if err := os.MkdirAll(project, 0o700); err != nil {
			t.Fatal(err)
		}
		projectConfig := filepath.Join(project, "workbench-context.pkl")
		writeAcceptanceFile(t, filepath.Join(project, ".workbench", "context.json"), `{"schemaVersion":1,"optIn":false,"includeChildren":false,"preserved":{"answer":42}}`, 0o640)
		beforeClaude := readAcceptanceFile(t, claudeSettings)
		beforeCodex := readAcceptanceFile(t, codexConfig)
		initStdout, initStderr, err := runContextProcess(t, binary, root, environment, nil, contextInitArguments(paths, project))
		if err != nil {
			t.Fatalf("project init failed: %v\nstderr=%s", err, initStderr)
		}
		var initialized map[string]any
		decodeAcceptanceJSON(t, initStdout, &initialized)
		if initialized["created"] != true || initialized["activation"].(map[string]any)["state"] != "enabled" {
			t.Fatalf("init report = %s", initStdout)
		}
		config := readAcceptanceFile(t, projectConfig)
		if !strings.Contains(config, `contributors`) || !strings.Contains(config, `project-guidance`) {
			t.Fatalf("init did not create the explicit Pkl declaration: %s", config)
		}
		if legacy := readAcceptanceFile(t, filepath.Join(project, ".workbench", "context.json")); !strings.Contains(legacy, `"preserved":{"answer":42}`) {
			t.Fatalf("init did not preserve inert legacy JSON: %s", legacy)
		}
		if beforeClaude != readAcceptanceFile(t, claudeSettings) || beforeCodex != readAcceptanceFile(t, codexConfig) || len(initStderr) != 0 {
			t.Fatalf("project init rewrote global settings or emitted diagnostics")
		}
	})

	t.Run("CLI exercises executable profile capability", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("profile provider subprocess proof uses Linux cleanup")
		}
		root := t.TempDir()
		paths := newAcceptanceContextPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		profileProvider := filepath.Join(moduleRoot, "examples", "context", "profile-provider.py")
		writeAcceptanceFile(t, filepath.Join(root, "workbench-context.pkl"), profileProjectDeclaration(profileProvider), 0o600)
		writeAcceptanceFile(t, paths.homeConfig, homeRuntimeDeclaration(5000), 0o600)
		stdout, stderr, err := runContextProcess(t, binary, root, environment, codexContextHookPayload(root, "profile-session", "profile-turn", ""), contextHookArguments(paths, "codex"))
		if err != nil || len(stdout) != 0 || len(stderr) != 0 {
			t.Fatalf("profile-only hook failed or delivered unexpected output: err=%v stdout=%q stderr=%q", err, stdout, stderr)
		}
		statusStdout, statusStderr, err := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if err != nil || len(statusStderr) != 0 {
			t.Fatalf("profile status failed: err=%v stderr=%q stdout=%q", err, statusStderr, statusStdout)
		}
		var status acceptanceProfileStatus
		decodeAcceptanceJSON(t, statusStdout, &status)
		if status.Runtime.State != "active" || status.Runtime.Status == nil {
			t.Fatalf("profile runtime was not active: %s", statusStdout)
		}
		var role, preference bool
		var profileRevision string
		for _, partition := range status.Runtime.Status.Partitions {
			if partition.Profile.Revision != "" {
				profileRevision = partition.Profile.Revision
			}
			for _, fact := range partition.Profile.Facts {
				switch fact.Key {
				case "role":
					role = fact.Value.Kind == "text" && fact.Value.Text == "maintainer" && fact.Validity.Policy == "until" && fact.Validity.ExpiresAt.After(time.Now().UTC()) && fact.Provenance.Origin == "provider" && fact.Provenance.Provider == "example-profile"
				case "preference:reviewStyle":
					preference = fact.Value.Kind == "text" && fact.Value.Text == "concise" && fact.Validity.Policy == "until" && fact.Validity.ExpiresAt.After(time.Now().UTC()) && fact.Provenance.Origin == "provider" && fact.Provenance.Provider == "example-profile"
				}
			}
		}
		if !role || !preference || profileRevision == "" {
			t.Fatalf("profile provider facts were not hydrated by the real CLI/runtime: %s", statusStdout)
		}
		explainStdout, explainStderr, err := runContextProcess(t, binary, root, environment, nil, contextExplainProfileArguments(paths, profileRevision))
		if err != nil || len(explainStderr) != 0 {
			t.Fatalf("profile explain failed: err=%v stderr=%q stdout=%q", err, explainStderr, explainStdout)
		}
		var explanation acceptanceTraceResult
		decodeAcceptanceJSON(t, explainStdout, &explanation)
		profileStamped := false
		for _, record := range explanation.Records {
			profileStamped = profileStamped || record.Profile == profileRevision
		}
		if len(explanation.Records) == 0 || !profileStamped {
			t.Fatalf("profile explain did not expose runtime-stamped revision %q: %s", profileRevision, explainStdout)
		}
	})

	t.Run("builtin offers, confirms, separates sessions, and leaves bounded history", func(t *testing.T) {
		root := t.TempDir()
		paths := newAcceptanceContextPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "fixture README\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), "---\nroot: true\ndocs:\n  - files: [\"README.md\"]\n    message: "+contextOfferText+"\n---\nignored markdown body\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "workbench-context.pkl"), builtinProjectDeclaration(), 0o600)
		writeAcceptanceFile(t, paths.homeConfig, homeRuntimeDeclaration(5000), 0o600)

		claude := claudeContextHookPayload(root, "claude-session", "claude-turn", "")
		stdout, stderr, err := runContextProcess(t, binary, root, environment, claude, contextHookArguments(paths, "claude"))
		if err != nil || len(stderr) != 0 {
			t.Fatalf("Claude offer failed: %v stderr=%q", err, stderr)
		}
		assertContextHookOffer(t, stdout, contextOfferText)
		repeated, repeatedStderr, err := runContextProcess(t, binary, root, environment, claude, contextHookArguments(paths, "claude"))
		if err != nil || len(repeated) != 0 || len(repeatedStderr) != 0 {
			t.Fatalf("repeated Claude hook was not suppressed: err=%v stdout=%q stderr=%q", err, repeated, repeatedStderr)
		}

		history := waitForContextHistory(t, binary, root, environment, paths, 2*time.Second)
		var contributionID uint64
		var offered, confirmed, suppressed bool
		for _, record := range history.Records {
			if record.ContributionID != 0 {
				contributionID = record.ContributionID
			}
			offered = offered || record.Kind == contextTraceOffered
			confirmed = confirmed || record.Kind == contextTraceConfirmed
			suppressed = suppressed || record.Kind == contextTraceSuppressed
		}
		if contributionID == 0 || !offered || !confirmed || !suppressed {
			t.Fatalf("history lacks offer/confirmation/suppression evidence: %+v", history.Records)
		}
		cacheStatus, cacheStatusStderr, cacheStatusErr := runContextProcess(t, binary, root, environment, nil, contextCacheStatusArguments(paths))
		if cacheStatusErr != nil || len(cacheStatusStderr) != 0 || len(cacheStatus) == 0 {
			t.Fatalf("explicit cache status failed: err=%v stderr=%q stdout=%q", cacheStatusErr, cacheStatusStderr, cacheStatus)
		}
		inspectStdout, inspectStderr, err := runContextProcess(t, binary, root, environment, nil, contextInspectContributionArguments(paths, contributionID))
		if err != nil || len(inspectStderr) != 0 || !strings.Contains(string(inspectStdout), contextOfferText) {
			t.Fatalf("inspect contribution did not retain bounded guidance: err=%v stderr=%q stdout=%q", err, inspectStderr, inspectStdout)
		}
		clearStdout, clearStderr, err := runContextProcess(t, binary, root, environment, nil, contextCacheClearArguments(paths))
		if err != nil || len(clearStderr) != 0 || !strings.Contains(string(clearStdout), "Delivery state was not cleared") {
			t.Fatalf("cache clear semantics changed: err=%v stderr=%q stdout=%q", err, clearStderr, clearStdout)
		}
		stillSuppressed, _, err := runContextProcess(t, binary, root, environment, claude, contextHookArguments(paths, "claude"))
		if err != nil || len(stillSuppressed) != 0 {
			t.Fatalf("cache clear incorrectly cleared delivery state: err=%v stdout=%q", err, stillSuppressed)
		}

		codex, _, err := runContextProcess(t, binary, root, environment, codexContextHookPayload(root, "codex-session", "codex-turn", ""), contextHookArguments(paths, "codex"))
		if err != nil {
			t.Fatalf("Codex session-separation hook failed: %v", err)
		}
		assertContextHookOffer(t, codex, contextOfferText)
		resetClaude, resetStderr, err := runContextProcess(t, binary, root, environment, claudeContextHookPayload(root, "claude-session", "claude-reset", "clear"), contextHookArguments(paths, "claude"))
		if err != nil {
			t.Fatalf("Claude reset hook failed: %v", err)
		}
		if len(resetClaude) != 0 || len(resetStderr) != 0 {
			t.Fatalf("Claude reset should only revoke the previous epoch: stdout=%q stderr=%q", resetClaude, resetStderr)
		}
		afterReset, afterResetStderr, err := runContextProcess(t, binary, root, environment, claudeContextHookPayload(root, "claude-session", "claude-after-reset", ""), contextHookArguments(paths, "claude"))
		if err != nil || len(afterResetStderr) != 0 {
			t.Fatalf("Claude post-reset hook failed: err=%v stderr=%q", err, afterResetStderr)
		}
		assertContextHookOffer(t, afterReset, contextOfferText)
		freshAfterClear, _, err := runContextProcess(t, binary, root, environment, claudeContextHookPayload(root, "fresh-session", "fresh-turn", ""), contextHookArguments(paths, "claude"))
		if err != nil {
			t.Fatalf("fresh session after cache clear failed: %v", err)
		}
		assertContextHookOffer(t, freshAfterClear, contextOfferText)
		if runtime.GOOS == "linux" {
			fullStdout, fullStderr, err := runContextProcessToFile(t, binary, root, environment, claudeContextHookPayload(root, "stdout-failure-session", "stdout-failure-turn", ""), contextHookArguments(paths, "claude"), "/dev/full")
			if err != nil || !strings.Contains(string(fullStderr), "stdout") || len(fullStdout) != 0 {
				t.Fatalf("stdout failure was not fail-open: err=%v stderr=%q captured=%q", err, fullStderr, fullStdout)
			}
		}
	})

	t.Run("executable provider proves JSON-RPC contribution and cleanup", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("provider process cleanup proof uses Linux process groups")
		}
		root := t.TempDir()
		paths := newAcceptanceContextPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "fixture README\n", 0o600)
		providerLog := filepath.Join(root, "provider.log")
		provider := filepath.Join(root, "provider.sh")
		writeAcceptanceFile(t, provider, providerFixtureScript(), 0o700)
		writeAcceptanceFile(t, filepath.Join(root, "workbench-context.pkl"), executableProjectDeclaration(provider, providerLog, "contribute"), 0o600)
		writeAcceptanceFile(t, paths.homeConfig, homeRuntimeDeclaration(5000), 0o600)
		stdout, stderr, err := runContextProcess(t, binary, root, environment, codexContextHookPayload(root, "provider-session", "provider-turn", ""), contextHookArguments(paths, "codex"))
		if err != nil || len(stderr) != 0 {
			t.Fatalf("executable provider hook failed: %v stderr=%q", err, stderr)
		}
		if len(stdout) == 0 {
			statusStdout, statusStderr, statusErr := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
			log := "<missing>"
			if _, logErr := os.Stat(providerLog); logErr == nil {
				log = readAcceptanceFile(t, providerLog)
			}
			t.Fatalf("executable provider produced no offer; hook stderr=%q; status err=%v stderr=%q json=%q; provider log=%q", stderr, statusErr, statusStderr, statusStdout, log)
		}
		assertContextHookOffer(t, stdout, providerOfferText)
		repeated, _, err := runContextProcess(t, binary, root, environment, codexContextHookPayload(root, "provider-session", "provider-turn", ""), contextHookArguments(paths, "codex"))
		if err != nil || len(repeated) != 0 {
			t.Fatalf("provider contribution was not suppressed on repetition: err=%v stdout=%q", err, repeated)
		}
		waitForContextStatus(t, binary, root, environment, paths, 8*time.Second, func(status acceptanceStatus) bool {
			activeScopes, _, providerProcesses, available := status.runtimeFacts()
			return status.Runtime.State == "not-running" || (available && activeScopes == 0 && providerProcesses == 0)
		})
		log := readAcceptanceFile(t, providerLog)
		for _, method := range []string{"initialize", "context.contribute", "shutdown"} {
			if !strings.Contains(log, `"method":"`+method+`"`) {
				t.Fatalf("provider log lacks %s lifecycle request: %s", method, log)
			}
		}
	})

	t.Run("cold concurrent hooks share one daemon", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("daemon process bound proof uses Linux /proc")
		}
		root := t.TempDir()
		paths := newAcceptanceContextPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "fixture README\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), "---\nroot: true\ndocs:\n  - files: [\"README.md\"]\n    message: "+contextOfferText+"\n---\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "workbench-context.pkl"), builtinProjectDeclaration(), 0o600)
		writeAcceptanceFile(t, paths.homeConfig, homeRuntimeDeclaration(1000), 0o600)

		const callers = 12
		var wait sync.WaitGroup
		wait.Add(callers)
		errorsByCaller := make(chan string, callers)
		for index := 0; index < callers; index++ {
			index := index
			go func() {
				defer wait.Done()
				stdout, stderr, err := runContextProcess(t, binary, root, environment, codexContextHookPayload(root, fmt.Sprintf("cold-session-%d", index), fmt.Sprintf("cold-turn-%d", index), ""), contextHookArguments(paths, "codex"))
				if err != nil || len(stderr) != 0 {
					errorsByCaller <- fmt.Sprintf("caller %d: err=%v stderr=%q", index, err, stderr)
					return
				}
				if !bytes.Contains(stdout, []byte(contextOfferText)) {
					errorsByCaller <- fmt.Sprintf("caller %d: no offer in %q", index, stdout)
				}
			}()
		}
		wait.Wait()
		close(errorsByCaller)
		for failure := range errorsByCaller {
			t.Error(failure)
		}
		if pids := contextDaemonPIDs(paths.socket); len(pids) > 1 {
			t.Fatalf("cold concurrent hooks started %d daemons: %v", len(pids), pids)
		}
	})

	t.Run("short idle TTL withdraws observed state", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("idle eviction observation uses the live Linux daemon")
		}
		root := t.TempDir()
		paths := newAcceptanceContextPaths(root)
		registerContextDaemonCleanup(t, paths)
		environment := contextAcceptanceEnvironment(root)
		writeAcceptanceFile(t, filepath.Join(root, "README.md"), "fixture README\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "ai-context.md"), "---\nroot: true\ndocs:\n  - files: [\"README.md\"]\n    message: "+contextOfferText+"\n---\n", 0o600)
		writeAcceptanceFile(t, filepath.Join(root, "workbench-context.pkl"), builtinProjectDeclaration(), 0o600)
		writeAcceptanceFile(t, paths.homeConfig, homeRuntimeDeclaration(50), 0o600)
		stdout, stderr, err := runContextProcess(t, binary, root, environment, codexContextHookPayload(root, "idle-session", "idle-turn", ""), contextHookArguments(paths, "codex"))
		if err != nil || len(stderr) != 0 {
			t.Fatalf("short-TTL hook failed: %v stderr=%q", err, stderr)
		}
		assertContextHookOffer(t, stdout, contextOfferText)
		waitForContextStatus(t, binary, root, environment, paths, 2*time.Second, func(status acceptanceStatus) bool {
			activeScopes, activeAudiences, providerProcesses, available := status.runtimeFacts()
			return status.Runtime.State == "not-running" || (available && activeScopes == 0 && activeAudiences == 0 && providerProcesses == 0)
		})
	})
}

// buildContextWorkbench builds the CLI and stages the exact private Pkl and
// runtime-lock layout expected by context-only production composition. Tests
// must provide PKL_EXECUTABLE explicitly; no PATH or ambient tool lookup is a
// valid acceptance fixture.
func buildContextWorkbench(t *testing.T, moduleRoot string) string {
	t.Helper()
	installation := t.TempDir()
	binary := filepath.Join(installation, "bin", "workbench")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/workbench")
	command.Dir = moduleRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build context acceptance CLI: %v\n%s", err, output)
	}
	pkl := os.Getenv("PKL_EXECUTABLE")
	if pkl == "" {
		t.Fatal("PKL_EXECUTABLE must designate the pinned Pkl fixture")
	}
	if !filepath.IsAbs(pkl) {
		t.Fatalf("PKL_EXECUTABLE must be absolute: %q", pkl)
	}
	pklBytes, err := os.ReadFile(pkl)
	if err != nil {
		t.Fatalf("read pinned Pkl fixture %q: %v", pkl, err)
	}
	privatePkl := filepath.Join(installation, "libexec", "workbench", "pkl")
	if err := os.MkdirAll(filepath.Dir(privatePkl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privatePkl, pklBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(moduleRoot, "release", "runtime-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	privateLock := filepath.Join(installation, "share", "workbench", "runtime-lock.json")
	if err := os.MkdirAll(filepath.Dir(privateLock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateLock, lock, 0o644); err != nil {
		t.Fatal(err)
	}
	return binary
}

func newAcceptanceContextPaths(root string) acceptanceContextPaths {
	runtimeDir := filepath.Join(root, "runtime")
	return acceptanceContextPaths{
		homeConfig: filepath.Join(root, "home", "workbench-context.pkl"),
		runtimeDir: runtimeDir,
		socket:     filepath.Join(runtimeDir, "context.sock"),
		startLock:  filepath.Join(runtimeDir, "context.start.lock"),
		serverLock: filepath.Join(runtimeDir, "context.server.lock"),
		cacheDir:   filepath.Join(root, "cache"),
	}
}

func builtinProjectDeclaration() string {
	return `amends "workbench:context"

scope = "subtree"

contributors {
  ["project-guidance"] = new AiContext {}
}
`
}

func profileProjectDeclaration(executable string) string {
	return fmt.Sprintf(`amends "workbench:context"

contributors {
  ["example-profile"] = new Executable {
    executable = %q
    capabilities { "profile" }
    limits {
      deadlineMs = 500
      maxResponseBytes = 262144
      maxFacts = 32
      maxContributions = 64
      maxBodyBytes = 262144
    }
  }
}
`, executable)
}

func executableProjectDeclaration(executable, argument, capability string) string {
	return fmt.Sprintf(`amends "workbench:context"

contributors {
  ["fixture-provider"] = new Executable {
    executable = %q
    arguments { %q }
    capabilities { %q }
    limits {
      deadlineMs = 1000
      maxResponseBytes = 65536
      maxContributions = 4
      maxBodyBytes = 4096
    }
  }
}
`, executable, argument, capability)
}

func homeRuntimeDeclaration(idleTTLMs uint64) string {
	return fmt.Sprintf(`amends "workbench:context-home"

limits {
  runtime {
    idleTTLMs = %d
  }
}
`, idleTTLMs)
}

func contextAcceptanceEnvironment(root string) []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "WORKBENCH_CONTEXT_") || strings.HasPrefix(value, "XDG_CONFIG_HOME=") || strings.HasPrefix(value, "XDG_CACHE_HOME=") || strings.HasPrefix(value, "XDG_RUNTIME_DIR=") || strings.HasPrefix(value, "CODEX_HOME=") {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, "HOME="+filepath.Join(root, "home"), "TMPDIR="+filepath.Join(root, "tmp"))
}

func contextHookArguments(paths acceptanceContextPaths, harness string) []string {
	return append([]string{"context", "hook", "--harness", harness}, contextPathArguments(paths)...)
}

func contextSetupArguments(paths acceptanceContextPaths, binary, claudeSettings, codexHome, codexConfig string, jsonOutput bool) []string {
	arguments := []string{"context", "setup", "--harness", "both", "--executable", binary, "--claude-settings", claudeSettings, "--codex-home", codexHome, "--codex-config", codexConfig}
	if jsonOutput {
		arguments = append(arguments, "--json")
	}
	return append(arguments, contextPathArguments(paths)...)
}

func contextInitArguments(paths acceptanceContextPaths, project string) []string {
	return append([]string{"context", "init", "--path", project, "--json"}, contextPathArguments(paths)...)
}

func contextInspectContributionArguments(paths acceptanceContextPaths, contributionID uint64) []string {
	return append([]string{"context", "inspect", "contribution", fmt.Sprint(contributionID)}, contextPathArguments(paths)...)
}

func contextCacheClearArguments(paths acceptanceContextPaths) []string {
	return append([]string{"context", "cache", "clear"}, contextPathArguments(paths)...)
}

func contextCacheStatusArguments(paths acceptanceContextPaths) []string {
	return append([]string{"context", "cache", "status", "--json"}, contextPathArguments(paths)...)
}

func contextHistoryArguments(paths acceptanceContextPaths) []string {
	return append([]string{"context", "history", "--json"}, contextPathArguments(paths)...)
}

func contextStatusArguments(paths acceptanceContextPaths) []string {
	return append([]string{"context", "status", "--path", ".", "--json"}, contextPathArguments(paths)...)
}

func contextExplainProfileArguments(paths acceptanceContextPaths, profile string) []string {
	return append([]string{"context", "explain", "profile", profile, "--path", ".", "--json"}, contextPathArguments(paths)...)
}

func contextPathArguments(paths acceptanceContextPaths) []string {
	return []string{"--home-config", paths.homeConfig, "--runtime-dir", paths.runtimeDir, "--socket", paths.socket, "--start-lock", paths.startLock, "--server-lock", paths.serverLock, "--cache-dir", paths.cacheDir}
}

func runContextProcess(t *testing.T, binary, directory string, environment []string, input []byte, arguments []string) ([]byte, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Dir = directory
	command.Env = environment
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return stdout.Bytes(), stderr.Bytes(), ctx.Err()
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

func runContextProcessToFile(t *testing.T, binary, directory string, environment []string, input []byte, arguments []string, stdoutPath string) ([]byte, []byte, error) {
	t.Helper()
	file, err := os.OpenFile(stdoutPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Dir = directory
	command.Env = environment
	command.Stdin = bytes.NewReader(input)
	command.Stdout = file
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err = command.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return nil, stderr.Bytes(), err
}

func claudeContextHookPayload(root, session, turn, source string) []byte {
	payload := map[string]any{
		"session_id":      session,
		"transcript_path": filepath.Join(root, "does-not-exist-transcript.jsonl"),
		"cwd":             root,
		"prompt_id":       turn,
		"hook_event_name": "PostToolBatch",
		"tool_calls": []any{map[string]any{
			"tool_name":     "Read",
			"tool_use_id":   turn,
			"tool_input":    map[string]string{"file_path": filepath.Join(root, "README.md")},
			"tool_response": "fixture README",
		}},
	}
	if source != "" {
		payload["source"] = source
	}
	return mustMarshalAcceptanceJSON(payload)
}

func codexContextHookPayload(root, session, turn, source string) []byte {
	payload := map[string]any{
		"session_id":      session,
		"turn_id":         turn,
		"transcript_path": nil,
		"cwd":             root,
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": "cat README.md"},
		"tool_response":   map[string]any{"stdout": "fixture README\n", "exit_code": 0},
		"tool_use_id":     turn,
	}
	if source != "" {
		payload["source"] = source
	}
	return mustMarshalAcceptanceJSON(payload)
}

func assertContextHookOffer(t *testing.T, output []byte, want string) {
	t.Helper()
	var envelope struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	decodeAcceptanceJSON(t, output, &envelope)
	if !strings.Contains(envelope.HookSpecificOutput.AdditionalContext, want) {
		t.Fatalf("hook additionalContext = %q, want %q", envelope.HookSpecificOutput.AdditionalContext, want)
	}
}

func waitForContextHistory(t *testing.T, binary, root string, environment []string, paths acceptanceContextPaths, timeout time.Duration) acceptanceTraceResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last acceptanceTraceResult
	for time.Now().Before(deadline) {
		stdout, stderr, err := runContextProcess(t, binary, root, environment, nil, contextHistoryArguments(paths))
		if err == nil && len(stderr) == 0 {
			decodeAcceptanceJSON(t, stdout, &last)
			if len(last.Records) > 0 {
				return last
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("history did not become visible before timeout: %+v", last.Records)
	return last
}

func waitForContextStatus(t *testing.T, binary, root string, environment []string, paths acceptanceContextPaths, timeout time.Duration, ready func(acceptanceStatus) bool) acceptanceStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last acceptanceStatus
	for time.Now().Before(deadline) {
		stdout, stderr, err := runContextProcess(t, binary, root, environment, nil, contextStatusArguments(paths))
		if err == nil && len(stderr) == 0 {
			last = acceptanceStatus{}
			decodeAcceptanceJSON(t, stdout, &last)
			if ready(last) {
				return last
			}
		}
		// Leave a real accept-timeout gap between status probes so the daemon's
		// idle worker can evict state; a tight polling loop would keep active
		// requests continuously in flight and mask cleanup.
		time.Sleep(300 * time.Millisecond)
	}
	activeScopes, activeAudiences, providerProcesses, available := last.runtimeFacts()
	t.Fatalf("context status did not reach expected bounded state: state=%q error=%q available=%t activeScopes=%d activeAudiences=%d providerProcesses=%d", last.Runtime.State, last.Runtime.Error, available, activeScopes, activeAudiences, providerProcesses)
	return last
}

func decodeSetupReport(t *testing.T, output []byte) struct {
	Changed []string `json:"changed"`
} {
	t.Helper()
	var report struct {
		Changed []string `json:"changed"`
	}
	decodeAcceptanceJSON(t, output, &report)
	return report
}

func decodeAcceptanceJSON(t *testing.T, output []byte, destination any) {
	t.Helper()
	if err := json.Unmarshal(output, destination); err != nil {
		t.Fatalf("decode CLI JSON %q: %v", output, err)
	}
}

func mustMarshalAcceptanceJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func writeAcceptanceFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func readAcceptanceFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func providerFixtureScript() string {
	return strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"log=\"$1\"",
		"while IFS= read -r request; do",
		"  printf '%s\\n' \"$request\" >> \"$log\"",
		"  id=$(printf '%s\\n' \"$request\" | sed -n 's#.*\"id\":\\([0-9][0-9]*\\),\"method\".*#\\1#p')",
		"  if printf '%s\\n' \"$request\" | grep -q '\"method\":\"initialize\"'; then",
		"    printf '{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"accepted\":true,\"capabilities\":[\"contribute\"],\"reasons\":[]}}\\n' \"$id\"",
		"  elif printf '%s\\n' \"$request\" | grep -q '\"method\":\"shutdown\"'; then",
		"    printf '{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"requestId\":%s,\"reasons\":[]}}\\n' \"$id\" \"$id\"",
		"    exit 0",
		"  else",
		"    printf '{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"output\":{\"contributions\":[{\"slot\":{\"key\":\"fixture-slot\"},\"source\":{\"identity\":{\"id\":\"fixture-source\"},\"kind\":\"file\",\"path\":\"provider.md\"},\"body\":\"Provider fixture guidance.\",\"reasons\":[]}],\"reasons\":[]}}}\\n' \"$id\"",
		"  fi",
		"done",
	}, "\n") + "\n"
}

func registerContextDaemonCleanup(t *testing.T, paths acceptanceContextPaths) {
	t.Helper()
	t.Cleanup(func() {
		for _, pid := range contextDaemonPIDs(paths.socket) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && len(contextDaemonPIDs(paths.socket)) > 0 {
			time.Sleep(20 * time.Millisecond)
		}
		for _, pid := range contextDaemonPIDs(paths.socket) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}

func contextDaemonPIDs(socket string) []int {
	if runtime.GOOS != "linux" {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		if !entry.Type().IsDir() {
			continue
		}
		var pid int
		if _, err := fmt.Sscan(entry.Name(), &pid); err != nil {
			continue
		}
		commandLine, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if bytes.Contains(commandLine, []byte("context\x00serve")) && bytes.Contains(commandLine, []byte("--socket\x00"+socket)) {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids
}
