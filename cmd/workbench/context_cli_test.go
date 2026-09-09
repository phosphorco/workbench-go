package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextdaemon"
	"github.com/phosphorco/workbench-go/internal/contextengine"
)

func TestContextHelpDoesNotAcquireWorkingDirectory(t *testing.T) {
	var output bytes.Buffer
	err := runContextCommand(context.Background(), []string{"--help"}, func() (string, error) {
		t.Fatal("context help observed cwd")
		return "", nil
	}, &output, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "hook --harness claude|codex") {
		t.Fatalf("help = %q", output.String())
	}
}

func TestContextGrammarCoversInspectionCursor(t *testing.T) {
	cases := []struct {
		args  []string
		kind  contextCommandKind
		value string
	}{
		{[]string{"hook", "--harness", "claude"}, contextCommandHook, ""},
		{[]string{"setup", "--harness", "both", "--json"}, contextCommandSetup, ""},
		{[]string{"init", "--path", "/tmp"}, contextCommandInit, ""},
		{[]string{"status", "--path", "/tmp"}, contextCommandStatus, ""},
		{[]string{"history", "--generation", "7", "--after", "9", "--cursor", "3:8"}, contextCommandHistory, ""},
		{[]string{"inspect", "contribution", "42"}, contextCommandInspectContribution, "42"},
		{[]string{"inspect", "turn", "turn-7"}, contextCommandInspectTurn, "turn-7"},
		{[]string{"explain", "profile", "profile:7"}, contextCommandExplainProfile, "profile:7"},
		{[]string{"cache", "status"}, contextCommandCacheStatus, ""},
		{[]string{"cache", "clear"}, contextCommandCacheClear, ""},
		{[]string{"serve"}, contextCommandServe, ""},
	}
	for _, test := range cases {
		invocation, err := parseContextInvocation(test.args)
		if err != nil {
			t.Fatalf("parse %v: %v", test.args, err)
		}
		if invocation.kind != test.kind || invocation.value != test.value {
			t.Fatalf("parse %v = %#v", test.args, invocation)
		}
	}
	for _, args := range [][]string{{"hook"}, {"hook", "--harness", "both"}, {"inspect", "contribution", "0"}, {"history", "--limit", "0"}} {
		if _, err := parseContextInvocation(args); err == nil {
			t.Fatalf("parse %v unexpectedly succeeded", args)
		}
	}
}

func TestContextSetupIsIdempotentAndPreservesUnrelatedSettings(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "bin", "workbench with space")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeSettings := filepath.Join(directory, "claude", "settings.json")
	initialClaude := []byte(`{"permissions":{"allow":["Read"]},"hooks":{"PostToolBatch":[{"matcher":"unrelated","hooks":[{"type":"command","command":"other"}]}]}}`)
	if err := os.MkdirAll(filepath.Dir(claudeSettings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeSettings, initialClaude, 0o640); err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(directory, "codex")
	codexConfig := filepath.Join(codexHome, "config.toml")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexConfig, []byte("model = \"fixture\"\n[features]\n\tfoo = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := contextOptions{
		harness:       "both",
		executable:    executable,
		claudeSetting: claudeSettings,
		codexHome:     codexHome,
		codexConfig:   codexConfig,
		homeConfig:    filepath.Join(directory, "workbench", "context.json"),
		runtimeDir:    filepath.Join(directory, "runtime"),
		cacheDir:      filepath.Join(directory, "cache"),
	}
	var first bytes.Buffer
	if err := runContextSetup(context.Background(), options, &first, io.Discard); err != nil {
		t.Fatal(err)
	}
	claudeAfterFirst, err := os.ReadFile(claudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	codexAfterFirst, err := os.ReadFile(filepath.Join(codexHome, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	mode, err := os.Stat(claudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0o640 {
		t.Fatalf("Claude settings mode = %o", mode.Mode().Perm())
	}
	var second bytes.Buffer
	if err := runContextSetup(context.Background(), options, &second, io.Discard); err != nil {
		t.Fatal(err)
	}
	claudeAfterSecond, _ := os.ReadFile(claudeSettings)
	codexAfterSecond, _ := os.ReadFile(filepath.Join(codexHome, "hooks.json"))
	if !bytes.Equal(claudeAfterFirst, claudeAfterSecond) || !bytes.Equal(codexAfterFirst, codexAfterSecond) {
		t.Fatalf("setup was not idempotent\nClaude first=%s\nClaude second=%s\nCodex first=%s\nCodex second=%s", claudeAfterFirst, claudeAfterSecond, codexAfterFirst, codexAfterSecond)
	}
	var claude map[string]json.RawMessage
	if err := json.Unmarshal(claudeAfterSecond, &claude); err != nil {
		t.Fatal(err)
	}
	if _, ok := claude["permissions"]; !ok || !strings.Contains(string(claude["permissions"]), "Read") {
		t.Fatalf("unrelated Claude setting lost: %s", claudeAfterSecond)
	}
	var codex map[string]json.RawMessage
	if err := json.Unmarshal(codexAfterSecond, &codex); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codex["hooks"]), `"additionalContextLimit": 0`) {
		t.Fatalf("Codex hook limit missing: %s", codexAfterSecond)
	}
	config, _ := os.ReadFile(codexConfig)
	if !strings.Contains(string(config), "model = \"fixture\"") || !strings.Contains(string(config), "hooks = true") {
		t.Fatalf("Codex config preservation = %s", config)
	}
	if !strings.Contains(first.String(), "claude hook:") || !strings.Contains(first.String(), "codex hook:") {
		t.Fatalf("setup report omitted commands: %q", first.String())
	}
	if !strings.Contains(first.String(), "Next step (codex): Review and trust the exact installed hooks via native /hooks") || !strings.Contains(first.String(), "https://learn.chatgpt.com/docs/hooks") {
		t.Fatalf("setup report omitted Codex trust guidance: %q", first.String())
	}
}

func TestContextSetupJSONIncludesCodexTrustGuidance(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "workbench")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	options := contextOptions{
		harness:    "codex",
		executable: executable,
		codexHome:  filepath.Join(directory, "codex"),
		homeConfig: filepath.Join(directory, "home.json"),
		runtimeDir: filepath.Join(directory, "runtime"),
		cacheDir:   filepath.Join(directory, "cache"),
		json:       true,
	}
	if err := runContextSetup(context.Background(), options, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var report contextSetupReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("setup JSON = %q: %v", output.String(), err)
	}
	if len(report.NextSteps) != 1 || report.NextSteps[0].Harness != "codex" || !strings.Contains(report.NextSteps[0].Action, "native /hooks") || report.NextSteps[0].Documentation != codexHooksDocumentation {
		t.Fatalf("Codex trust guidance = %#v", report.NextSteps)
	}
}

func TestContextSetupRejectsMalformedSettingsBeforeMutation(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "workbench")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"hooks":"not-an-object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runContextSetup(context.Background(), contextOptions{harness: "claude", executable: executable, claudeSetting: settings, homeConfig: filepath.Join(directory, "home.json"), runtimeDir: filepath.Join(directory, "run"), cacheDir: filepath.Join(directory, "cache")}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "hooks object") {
		t.Fatalf("malformed settings error = %v", err)
	}
	contents, _ := os.ReadFile(settings)
	if string(contents) != `{"hooks":"not-an-object"}` {
		t.Fatalf("malformed settings changed: %q", contents)
	}
}

func TestContextInitPreservesProjectFieldsAndOptIns(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, ".workbench", "context.json")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"schemaVersion":1,"optIn":false,"includeChildren":false,"providers":[],"unknown":"keep"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runContextInit(root, contextOptions{}, &output); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(project)
	if !strings.Contains(string(contents), `"optIn": true`) || !strings.Contains(string(contents), `"providers": []`) || !strings.Contains(string(contents), `"unknown": "keep"`) {
		t.Fatalf("init lost fields: %s", contents)
	}
	mode, _ := os.Stat(project)
	if mode.Mode().Perm() != 0o640 {
		t.Fatalf("project mode = %o", mode.Mode().Perm())
	}
	if !strings.Contains(output.String(), "Opted in project scope") {
		t.Fatalf("init report = %q", output.String())
	}
}

func TestContextStatusInactiveIsLocalAndExplainable(t *testing.T) {
	directory := t.TempDir()
	options := contextOptions{homeConfig: filepath.Join(directory, "missing-home.json"), runtimeDir: filepath.Join(directory, "runtime"), cacheDir: filepath.Join(directory, "cache")}
	var output bytes.Buffer
	if err := runContextStatus(context.Background(), directory, options, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Activation: inactive") || !strings.Contains(output.String(), "no applicable project or home opt-in") {
		t.Fatalf("inactive status = %q", output.String())
	}
	if _, err := os.Stat(options.runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created runtime directory: %v", err)
	}
}

func TestContextStatusShowsWinningOptInScopeLocally(t *testing.T) {
	directory := t.TempDir()
	if err := runContextInit(directory, contextOptions{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runContextStatus(context.Background(), directory, contextOptions{homeConfig: filepath.Join(directory, "missing-home.json"), runtimeDir: filepath.Join(directory, "runtime"), cacheDir: filepath.Join(directory, "cache")}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Activation: enabled") || !strings.Contains(output.String(), "Scope: "+directory) {
		t.Fatalf("enabled status = %q", output.String())
	}
	if _, err := os.Stat(filepath.Join(directory, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created runtime directory: %v", err)
	}
}

func TestContextStatusEnabledReportsExistingRuntimeFactsWithoutStarting(t *testing.T) {
	directory := t.TempDir()
	if err := runContextInit(directory, contextOptions{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	paths := contextdaemon.DefaultPaths(filepath.Join(directory, "runtime"))
	report := contextStatusReport{
		WorkingDirectory: directory,
		Activation:       contextapi.ActivationResult{State: contextapi.ActivationEnabled},
		Runtime: contextRuntimeStatusReport{State: "active", Status: &contextdaemon.Status{
			Generation:        7,
			SocketPath:        paths.SocketPath,
			ProviderProcesses: 1,
			TraceQueueItems:   2,
			TraceQueueBytes:   128,
			TraceDropped:      3,
			Partitions: []contextdaemon.PartitionStatus{{
				Audience: contextapi.Audience{ID: "session-1", Epoch: 4},
				Profile:  contextapi.ProfileSnapshot{Revision: "profile-7", ValidUntil: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)},
				Engine:   contextengine.Stats{PendingItems: 5, LiveOffers: 1, Receipts: 2},
			}},
		}},
	}
	var output bytes.Buffer
	if err := writeContextStatus(&output, report); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Runtime: active; generation 7", "Queue: 2 items/128 bytes", "audience session-1 epoch 4", "profile-7", "pending 5; live 1; receipts 2"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("runtime status missing %q: %s", expected, output.String())
		}
	}
}

func TestContextStatusEnabledMissingRuntimeDoesNotCreateRuntimePaths(t *testing.T) {
	directory := t.TempDir()
	if err := runContextInit(directory, contextOptions{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(directory, "runtime")
	report := readExistingRuntimeStatus(context.Background(), contextdaemon.DefaultPaths(runtimeDir), contextOptions{}, directory)
	if report.State != "not-running" || report.Status != nil {
		t.Fatalf("missing runtime report = %#v", report)
	}
	if _, err := os.Stat(runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("existing-daemon status created runtime directory: %v", err)
	}
}

func TestContextHookCommandQuotesAllExplicitPaths(t *testing.T) {
	paths := contextdaemon.Paths{HomeConfigPath: "/tmp/home with space/context.json", RuntimeDir: "/tmp/run with space", SocketPath: "/tmp/run with space/context.sock", LockPath: "/tmp/run with space/start.lock", ServerLockPath: "/tmp/run with space/server.lock", CacheDir: "/tmp/cache with space"}
	command := contextHookCommand("/tmp/bin/workbench with space", "codex", paths)
	for _, value := range []string{"/tmp/bin/workbench with space", paths.HomeConfigPath, paths.RuntimeDir, paths.SocketPath, paths.LockPath, paths.ServerLockPath, paths.CacheDir} {
		if !strings.Contains(command, shellQuote(value)) {
			t.Fatalf("command %q does not quote %q", command, value)
		}
	}
}

func TestContextOptionsAcceptPositiveBoundsAndRejectZero(t *testing.T) {
	invocation, err := parseContextInvocation([]string{"history", "--limit", "7", "--max-bytes", "100", "--max-scan-bytes", "200", "--generation", "2", "--after", "3"})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.options.limit != 7 || invocation.options.maxBytes != 100 || invocation.options.generation != 2 || invocation.options.after != 3 {
		t.Fatalf("options = %#v", invocation.options)
	}
	if _, err := parseContextInvocation([]string{"history", "--limit", "0"}); err == nil {
		t.Fatal("zero limit unexpectedly accepted")
	}
	firstPage, err := parseContextInvocation([]string{"history", "--generation", "0", "--after", "0", "--json=false"})
	if err != nil || firstPage.options.generation != 0 || firstPage.options.after != 0 || firstPage.options.json {
		t.Fatalf("zero cursors = %#v, err=%v", firstPage, err)
	}
	if _, err := parseContextInvocation([]string{"history", "--json=maybe"}); err == nil || !strings.Contains(err.Error(), "--json") {
		t.Fatalf("invalid bool error = %v", err)
	}
	if _, err := parseContextInvocation([]string{"history", "--json", "--json"}); err == nil || !strings.Contains(err.Error(), "specified more than once") {
		t.Fatalf("duplicate bool error = %v", err)
	}
}

func TestContextTraceQueryDefersByteBoundsToRuntime(t *testing.T) {
	query, err := contextTraceQuery("", "", contextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if query.Limit != 0 || query.MaxBytes != 0 || query.MaxScanBytes != 0 {
		t.Fatalf("default trace query = %#v; query bounds must be runtime-owned", query)
	}
}

func TestContextSetupDryRunDoesNotCreateFiles(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "workbench")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(directory, "claude", "settings.json")
	codex := filepath.Join(directory, "codex")
	if err := runContextSetup(context.Background(), contextOptions{harness: "both", executable: executable, claudeSetting: claude, codexHome: codex, homeConfig: filepath.Join(directory, "home.json"), runtimeDir: filepath.Join(directory, "run"), cacheDir: filepath.Join(directory, "cache"), dryRun: true}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{claude, codex, filepath.Join(directory, "run"), filepath.Join(directory, "cache")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dry-run created %q: %v", path, err)
		}
	}
}

func TestContextJSONStatusIsMachineReadable(t *testing.T) {
	directory := t.TempDir()
	var output bytes.Buffer
	if err := runContextStatus(context.Background(), directory, contextOptions{json: true, homeConfig: filepath.Join(directory, "home.json"), runtimeDir: filepath.Join(directory, "run"), cacheDir: filepath.Join(directory, "cache")}, &output); err != nil {
		t.Fatal(err)
	}
	var report contextStatusReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("status JSON = %q: %v", output.String(), err)
	}
	if report.Activation.State != contextapi.ActivationInactive || report.WorkingDirectory != directory {
		t.Fatalf("status report = %#v", report)
	}
}
