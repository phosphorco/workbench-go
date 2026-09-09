package contextconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextcache"
)

var testNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func projectPath(root string) string { return filepath.Join(root, projectDeclarationRelativePath) }

func testIdentity() contextapi.EvaluatorIdentity {
	return contextapi.EvaluatorIdentity{Name: "test-evaluator", Version: "1", Digest: "sha256:test-evaluator"}
}

func testDependencies(t *testing.T, calls *int) LoadDependencies {
	t.Helper()
	return LoadDependencies{
		Identity: func(context.Context) (contextapi.EvaluatorIdentity, error) { return testIdentity(), nil },
		Freshness: func(_ context.Context, input contextapi.EvaluationInput, value contextapi.EvaluatedDeclaration) error {
			if value.Origin != input.Origin {
				return errors.New("origin changed")
			}
			if value.Evaluator != testIdentity() {
				return errors.New("identity changed")
			}
			return nil
		},
		Evaluate: func(_ context.Context, input contextapi.EvaluationInput) (contextapi.EvaluatedDeclaration, error) {
			if calls != nil {
				*calls++
			}
			if strings.Contains(string(input.SourceBytes), "malformed") {
				return contextapi.EvaluatedDeclaration{}, errors.New("malformed evaluator fixture")
			}
			value := contextapi.EvaluatedDeclaration{Origin: input.Origin, Evaluator: testIdentity(), Revision: contextapi.ConfigDigest("sha256:" + digestString(string(input.SourceBytes)))}
			if input.Origin.Authority == contextapi.ScopeAuthorityHome {
				value.Kind = contextapi.DeclarationKindHome
				value.Home = homeForSource(string(input.SourceBytes), input.Origin.Root)
			} else {
				value.Kind = contextapi.DeclarationKindProject
				value.Project = projectForSource(string(input.SourceBytes))
			}
			return value, nil
		},
	}
}

func projectForSource(source string) contextapi.ProjectDeclaration {
	declaration := contextapi.ProjectDeclaration{Enabled: true, Scope: contextapi.DeclarationScopeSubtree, Contributors: map[contextapi.ContributorName]contextapi.Contributor{
		"project-guidance": {Enabled: true, Kind: contextapi.ContributorKindAiContext},
	}}
	switch {
	case strings.Contains(source, "disabled"):
		declaration.Enabled = false
	case strings.Contains(source, "empty"):
		declaration.Contributors = map[contextapi.ContributorName]contextapi.Contributor{}
	case strings.Contains(source, "directory"):
		declaration.Scope = contextapi.DeclarationScopeDirectory
	case strings.Contains(source, "external"):
		declaration.Contributors = map[contextapi.ContributorName]contextapi.Contributor{
			"external": {Enabled: true, Kind: contextapi.ContributorKindExecutable, Executable: contextapi.Executable{Executable: "bin/provider", Arguments: []string{"--safe"}, Capabilities: []contextapi.ProviderCapability{contextapi.ProviderCapabilityProfile, contextapi.ProviderCapabilityContribute}, Settings: []byte(`{"mode":"safe"}`), Limits: contextapi.ProviderLimits{DeadlineMs: 77}}},
		}
		declaration.Profile = contextapi.ProfileSelection{Role: "project-role"}
	}
	return declaration
}

func homeForSource(source, root string) contextapi.HomeDeclaration {
	selectionRoot := root
	declaration := contextapi.HomeDeclaration{Limits: contextapi.HomeLimits{Defaults: contextapi.ProfileDefaults{Selection: contextapi.ProfileSelection{Role: "home-role"}}}, Directories: []contextapi.HomeDirectorySelection{{Root: selectionRoot, Enabled: true, Scope: contextapi.DeclarationScopeSubtree, Contributors: map[contextapi.ContributorName]contextapi.Contributor{"home-guidance": {Enabled: true, Kind: contextapi.ContributorKindAiContext}}}}}
	if strings.Contains(source, "exclude") {
		declaration.Exclusions = []contextapi.PathRule{{Root: filepath.Join(root, "excluded"), IncludeChildren: true}}
	}
	if strings.Contains(source, "disabled") {
		declaration.Directories[0].Enabled = false
	}
	if strings.Contains(source, "allow-external") {
		declaration.Limits.ProviderPolicy = contextapi.ProviderPolicy{Mode: contextapi.ProviderPolicyAllowlist, Allowed: []contextapi.ProviderID{"external"}}
	}
	if strings.Contains(source, "deny-external") {
		declaration.Limits.ProviderPolicy = contextapi.ProviderPolicy{Mode: contextapi.ProviderPolicyAllowlist, Allowed: []contextapi.ProviderID{"home-guidance"}}
	}
	if strings.Contains(source, "retained") {
		declaration.Limits.Runtime.MaxPendingMemoryBytes = 1_000_000
		declaration.Limits.Delivery.MaxRetainedBytes = 99_000_000
	}
	if strings.Contains(source, "runtime-count-overflow") {
		declaration.Limits.Runtime.MaxProfileFacts = maxConfiguredCount + 1
	}
	if strings.Contains(source, "provider-overflow") {
		declaration.Limits.Runtime.ProviderDefaults.MaxFacts = maxConfiguredCount + 1
	}
	if strings.Contains(source, "cache-overflow") {
		declaration.Limits.Cache.MaxReasons = maxConfiguredCount + 1
	}
	if strings.Contains(source, "delivery-overflow") {
		declaration.Limits.Delivery.Queue.MaxPendingItems = maxConfiguredCount + 1
	}
	if strings.TrimSpace(source) == "overflow" {
		declaration.Limits.Runtime.HookDeadlineMs = ^uint64(0)
		declaration.Limits.Runtime.WholeHookDeadlineMs = ^uint64(0)
	}
	if strings.Contains(source, "many-home") {
		declaration.Directories = append(declaration.Directories, contextapi.HomeDirectorySelection{Root: filepath.Join(root, "child"), Enabled: true, Scope: contextapi.DeclarationScopeSubtree, Contributors: map[contextapi.ContributorName]contextapi.Contributor{"home-guidance": {Enabled: true, Kind: contextapi.ContributorKindAiContext}}})
	}
	return declaration
}

func loadTest(t *testing.T, directory, home string, deps LoadDependencies) LoadResult {
	t.Helper()
	loaded, err := Load(context.Background(), LoadOptions{WorkingDirectory: directory, HomeConfigPath: home, Now: testNow}, deps)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func reasonSummaryContains(result contextapi.ActivationResult, needle string) bool {
	for _, reason := range result.Reasons {
		if strings.Contains(reason.Summary, needle) {
			return true
		}
	}
	return false
}

func TestLoadNoSourceIsSilentAndDoesNotUseDependencies(t *testing.T) {
	root := t.TempDir()
	evaluateCalls, freshnessCalls, identityCalls := 0, 0, 0
	loaded := loadTest(t, root, "", LoadDependencies{
		Identity: func(context.Context) (contextapi.EvaluatorIdentity, error) {
			identityCalls++
			return testIdentity(), nil
		},
		Freshness: func(context.Context, contextapi.EvaluationInput, contextapi.EvaluatedDeclaration) error {
			freshnessCalls++
			return nil
		},
		Evaluate: func(context.Context, contextapi.EvaluationInput) (contextapi.EvaluatedDeclaration, error) {
			evaluateCalls++
			return contextapi.EvaluatedDeclaration{}, errors.New("must not evaluate")
		},
	})
	if loaded.Result.State != contextapi.ActivationInactive || len(loaded.Captures) != 0 || evaluateCalls != 0 || freshnessCalls != 0 || identityCalls != 0 {
		t.Fatalf("no-source result = %#v, evaluate=%d freshness=%d identity=%d captures=%d", loaded.Result, evaluateCalls, freshnessCalls, identityCalls, len(loaded.Captures))
	}
	if !reasonSummaryContains(loaded.Result, "no applicable") {
		t.Fatalf("no-source reason = %#v", loaded.Result.Reasons)
	}
}

func TestLegacyJSONIsInertAndPklSourceActivates(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".workbench", "context.json"), `{"schemaVersion":1,"optIn":true,"includeChildren":true}`)
	calls := 0
	legacy := loadTest(t, root, "", testDependencies(t, &calls))
	if legacy.Result.State != contextapi.ActivationInactive || calls != 0 {
		t.Fatalf("legacy JSON was not inert: result=%#v calls=%d", legacy.Result, calls)
	}
	if legacy.CachePolicy == nil || legacy.CachePolicy.Validate(context.Background()) != nil {
		t.Fatal("absent-home cache policy was not returned")
	}
	writeTestFile(t, projectPath(root), "enabled")
	loaded := loadTest(t, root, "", testDependencies(t, &calls))
	if loaded.Result.State != contextapi.ActivationEnabled || len(loaded.Result.Effective.Providers) != 1 || loaded.Result.Effective.Providers[0].ID != "project-guidance" {
		t.Fatalf("Pkl source result = %#v", loaded.Result)
	}
	if loaded.Result.Effective.Runtime.MaxProviderProcesses != 4 || loaded.Result.Effective.Cache.DiskCapBytes != 5_000_000_000 {
		t.Fatalf("defaults were not retained: %#v", loaded.Result.Effective)
	}
	if loaded.Result.Effective.Delivery.MaxRetainedBytes != defaultMaxRetainedBytes {
		t.Fatalf("retained-byte default = %d, want %d", loaded.Result.Effective.Delivery.MaxRetainedBytes, defaultMaxRetainedBytes)
	}
}

func TestNearestCompleteProjectStopsAncestorEvaluation(t *testing.T) {
	root := t.TempDir()
	ancestor := filepath.Join(root, "ancestor")
	child := filepath.Join(ancestor, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projectPath(ancestor), "malformed")
	writeTestFile(t, projectPath(child), "enabled")
	calls := 0
	loaded := loadTest(t, child, "", testDependencies(t, &calls))
	if loaded.Result.State != contextapi.ActivationEnabled || calls != 1 {
		t.Fatalf("nearest project did not own resolution: state=%q calls=%d result=%#v", loaded.Result.State, calls, loaded.Result)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(ancestor, alias); err != nil {
		t.Fatal(err)
	}
	viaAlias := loadTest(t, filepath.Join(alias, "child"), "", testDependencies(t, nil))
	if viaAlias.Input.WorkingDirectory != child || viaAlias.Result.State != contextapi.ActivationEnabled || viaAlias.Result.Scope.CanonicalRoot != child {
		t.Fatalf("canonical Pkl alias result = %#v, input=%#v", viaAlias.Result, viaAlias.Input)
	}

	writeTestFile(t, projectPath(child), "disabled")
	loaded = loadTest(t, child, "", testDependencies(t, nil))
	if loaded.Result.State != contextapi.ActivationInactive || !reasonSummaryContains(loaded.Result, "disabled") || loaded.Result.Scope.CanonicalRoot != child {
		t.Fatalf("disabled nearest boundary = %#v", loaded.Result)
	}

	nested := filepath.Join(child, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projectPath(child), "directory")
	loaded = loadTest(t, nested, "", testDependencies(t, nil))
	if loaded.Result.State != contextapi.ActivationInactive || !reasonSummaryContains(loaded.Result, "does not cover") {
		t.Fatalf("directory boundary fell through: %#v", loaded.Result)
	}
}

func TestDanglingNearestDeclarationBlocksAncestorFallback(t *testing.T) {
	root := t.TempDir()
	ancestor := filepath.Join(root, "ancestor")
	child := filepath.Join(ancestor, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projectPath(ancestor), "enabled")
	if err := os.Symlink(filepath.Join(child, "missing.pkl"), projectPath(child)); err != nil {
		t.Fatal(err)
	}
	loaded := loadTest(t, child, "", testDependencies(t, nil))
	if loaded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(loaded.Result, "cannot be loaded") {
		t.Fatalf("dangling nearest declaration fell through: %#v", loaded.Result)
	}

	outside := filepath.Join(t.TempDir(), "outside.pkl")
	writeTestFile(t, outside, "enabled")
	if err := os.Remove(projectPath(child)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, projectPath(child)); err != nil {
		t.Fatal(err)
	}
	loaded = loadTest(t, child, "", testDependencies(t, nil))
	if loaded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(loaded.Result, "cannot be loaded") {
		t.Fatalf("escaping nearest declaration was read: %#v", loaded.Result)
	}
}

func TestInvalidHomeStopsProjectEvaluation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectPath(root), "enabled")
	home := filepath.Join(root, "home-workbench-context.pkl")
	writeTestFile(t, home, "malformed")
	calls := 0
	loaded := loadTest(t, root, home, testDependencies(t, &calls))
	if loaded.Result.State != contextapi.ActivationInvalid || calls != 1 || len(loaded.Captures) != 0 {
		t.Fatalf("invalid home did not stop project work: result=%#v calls=%d captures=%d", loaded.Result, calls, len(loaded.Captures))
	}
	if loaded.CachePolicy != nil {
		t.Fatal("invalid home returned a usable cache policy")
	}
}

func TestInvalidBoundedAndOverflowDeclarationsFailClosed(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home-workbench-context.pkl")
	writeTestFile(t, home, "overflow")
	if loaded := loadTest(t, root, home, testDependencies(t, nil)); loaded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(loaded.Result, "hook deadline") {
		t.Fatalf("overflow home was accepted: %#v", loaded.Result)
	}
	writeTestFile(t, home, "many-home")
	if err := os.MkdirAll(filepath.Join(root, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(context.Background(), LoadOptions{WorkingDirectory: root, HomeConfigPath: home, Now: testNow, Limits: LoadLimits{MaxHomeScopes: 1}}, testDependencies(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(loaded.Result, "selection count") {
		t.Fatalf("home lookup cap was not enforced: %#v", loaded.Result)
	}
	for _, testCase := range []struct {
		marker string
		reason string
	}{
		{marker: "runtime-count-overflow", reason: "runtime bound"},
		{marker: "provider-overflow", reason: "provider count"},
		{marker: "cache-overflow", reason: "cache count"},
		{marker: "delivery-overflow", reason: "queue item count"},
	} {
		writeTestFile(t, home, testCase.marker)
		loaded = loadTest(t, root, home, testDependencies(t, nil))
		if loaded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(loaded.Result, testCase.reason) {
			t.Fatalf("%s was accepted: %#v", testCase.marker, loaded.Result)
		}
	}

	writeTestFile(t, projectPath(root), "enabled")
	loaded, err = Load(context.Background(), LoadOptions{WorkingDirectory: root, Now: testNow, Limits: LoadLimits{MaxConfigBytes: 1}}, testDependencies(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(loaded.Result, "exceeds") {
		t.Fatalf("bounded source was not rejected: %#v", loaded.Result)
	}
}

func TestRetainedBytesClampAndStableScopeIdentity(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectPath(root), "enabled")
	home := filepath.Join(root, "home-workbench-context.pkl")
	writeTestFile(t, home, "retained")
	first := loadTest(t, root, home, testDependencies(t, nil))
	if first.Result.State != contextapi.ActivationEnabled || first.Input.Config.Home.Delivery.MaxRetainedBytes != 1_000_000 || first.Result.Effective.Delivery.MaxRetainedBytes != 1_000_000 {
		t.Fatalf("retained-byte clamp = input=%d effective=%d result=%#v", first.Input.Config.Home.Delivery.MaxRetainedBytes, first.Result.Effective.Delivery.MaxRetainedBytes, first.Result)
	}
	firstID := first.Result.Scope.ID
	writeTestFile(t, projectPath(root), "enabled-edited")
	second := loadTest(t, root, home, testDependencies(t, nil))
	if second.Result.Scope.ID != firstID || second.Result.Scope.ConfigDigest == first.Result.Scope.ConfigDigest {
		t.Fatalf("scope identity/revision = first(%q,%q) second(%q,%q)", firstID, first.Result.Scope.ConfigDigest, second.Result.Scope.ID, second.Result.Scope.ConfigDigest)
	}
}

func TestEntrySymlinkKeepsDeclaredOriginPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "declared-source.pkl")
	writeTestFile(t, target, "enabled")
	if err := os.Symlink(filepath.Base(target), projectPath(root)); err != nil {
		t.Fatal(err)
	}
	var seen string
	deps := testDependencies(t, nil)
	baseEvaluate := deps.Evaluate
	deps.Evaluate = func(ctx context.Context, input contextapi.EvaluationInput) (contextapi.EvaluatedDeclaration, error) {
		seen = input.Origin.Path
		return baseEvaluate(ctx, input)
	}
	loaded := loadTest(t, root, "", deps)
	if loaded.Result.State != contextapi.ActivationEnabled || seen != projectPath(root) {
		t.Fatalf("entry origin path = %q, want %q; result=%#v", seen, projectPath(root), loaded.Result)
	}
}

func TestAbsentHomePolicyDetectsLaterAppearance(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home-workbench-context.pkl")
	writeTestFile(t, projectPath(root), "enabled")
	loaded := loadTest(t, root, home, testDependencies(t, nil))
	if loaded.CachePolicy == nil {
		t.Fatal("absent home did not return cache policy")
	}
	writeTestFile(t, home, "enabled")
	if err := loaded.CachePolicy.Validate(context.Background()); err == nil {
		t.Fatal("absent-home policy accepted a later home declaration")
	}
}

func TestColdProjectValidatesAbsentHomePolicyWithoutCache(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home-workbench-context.pkl")
	writeTestFile(t, projectPath(root), "enabled")
	deps := testDependencies(t, nil)
	baseEvaluate := deps.Evaluate
	deps.Evaluate = func(ctx context.Context, input contextapi.EvaluationInput) (contextapi.EvaluatedDeclaration, error) {
		if input.Origin.Authority == contextapi.ScopeAuthorityProject {
			writeTestFile(t, home, "enabled")
		}
		return baseEvaluate(ctx, input)
	}
	loaded := loadTest(t, root, home, deps)
	if loaded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(loaded.Result, "appeared after absence") {
		t.Fatalf("cold project bypassed absent-home policy: %#v", loaded.Result)
	}
}

func TestHomeExclusionDisabledScopeAndProfileDefaults(t *testing.T) {
	root := t.TempDir()
	excluded := filepath.Join(root, "excluded")
	if err := os.MkdirAll(excluded, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projectPath(root), "enabled")
	home := filepath.Join(root, "home-workbench-context.pkl")
	writeTestFile(t, home, "exclude")
	excludedResult := loadTest(t, excluded, home, testDependencies(t, nil))
	if excludedResult.Result.State != contextapi.ActivationInactive || !reasonSummaryContains(excludedResult.Result, "exclusion") {
		t.Fatalf("excluded result = %#v", excludedResult.Result)
	}
	allowedResult := loadTest(t, root, home, testDependencies(t, nil))
	if allowedResult.Result.State != contextapi.ActivationEnabled || allowedResult.Result.Effective.Profile.Role != "home-role" {
		t.Fatalf("home defaults/result = %#v", allowedResult.Result)
	}

	writeTestFile(t, home, "disabled")
	disabled := loadTest(t, root, home, testDependencies(t, nil))
	if disabled.Result.State != contextapi.ActivationInactive || !reasonSummaryContains(disabled.Result, "home scope") {
		t.Fatalf("disabled home scope did not dominate: %#v", disabled.Result)
	}
}

func TestNamedContributorsCapabilitiesPolicyAndRelativeExecutable(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectPath(root), "external")
	home := filepath.Join(root, "home-workbench-context.pkl")
	writeTestFile(t, home, "allow-external")
	loaded := loadTest(t, root, home, testDependencies(t, nil))
	if loaded.Result.State != contextapi.ActivationEnabled || len(loaded.Result.Effective.Providers) != 1 {
		t.Fatalf("named contributor result = %#v", loaded.Result)
	}
	provider := loaded.Result.Effective.Providers[0]
	if provider.ID != "external" || provider.Executable != filepath.Join(root, "bin/provider") || len(provider.Capabilities) != 2 || provider.Limits.DeadlineMs != 77 {
		t.Fatalf("provider projection = %#v", provider)
	}
	if loaded.Result.Effective.Profile.Role != "project-role" {
		t.Fatalf("project profile did not override home default: %#v", loaded.Result.Effective.Profile)
	}

	writeTestFile(t, home, "deny-external")
	blocked := loadTest(t, root, home, testDependencies(t, nil))
	if blocked.Result.State != contextapi.ActivationConflict || !reasonSummaryContains(blocked.Result, "excludes") {
		t.Fatalf("policy result = %#v", blocked.Result)
	}
}

func TestLoadRejectsMissingEvaluatorForPresentSource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectPath(root), "enabled")
	loaded := loadTest(t, root, "", LoadDependencies{})
	if loaded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(loaded.Result, "evaluator dependency") {
		t.Fatalf("missing evaluator result = %#v", loaded.Result)
	}
}

func TestColdEvaluationRequiresFreshnessBeforeActivation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectPath(root), "enabled")
	freshnessCalls := 0
	deps := testDependencies(t, nil)
	deps.Freshness = func(context.Context, contextapi.EvaluationInput, contextapi.EvaluatedDeclaration) error {
		freshnessCalls++
		return errors.New("captured source changed during evaluation")
	}
	loaded := loadTest(t, root, "", deps)
	if loaded.Result.State != contextapi.ActivationInvalid || freshnessCalls != 1 || len(loaded.Captures) != 0 || !reasonSummaryContains(loaded.Result, "captured source changed") {
		t.Fatalf("stale cold result was activated: calls=%d captures=%d result=%#v", freshnessCalls, len(loaded.Captures), loaded.Result)
	}
}

func TestCancellationPropagatesFromEvaluation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectPath(root), "enabled")
	deps := testDependencies(t, nil)
	deps.Evaluate = func(context.Context, contextapi.EvaluationInput) (contextapi.EvaluatedDeclaration, error) {
		return contextapi.EvaluatedDeclaration{}, context.Canceled
	}
	_, err := Load(context.Background(), LoadOptions{WorkingDirectory: root, Now: testNow}, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("evaluation cancellation = %v, want context.Canceled", err)
	}
}

func TestSnapshotRetainsEntryBytesAndUsesFreshness(t *testing.T) {
	root := t.TempDir()
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	pool, err := contextcache.Open(cacheRoot, contextcache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projectPath(root), "enabled")
	calls := 0
	deps := testDependencies(t, &calls)
	deps.Cache = pool
	first := loadTest(t, root, "", deps)
	if first.Result.State != contextapi.ActivationEnabled || calls != 1 || len(first.Captures) != 1 || string(first.Captures[0].Input.SourceBytes) != "enabled" {
		t.Fatalf("first cached load = result %#v calls=%d captures=%#v", first.Result, calls, first.Captures)
	}
	second := loadTest(t, root, "", deps)
	if second.Result.State != contextapi.ActivationEnabled || calls != 1 {
		t.Fatalf("snapshot was not reused: result=%#v calls=%d", second.Result, calls)
	}
	writeTestFile(t, projectPath(root), "enabled-edited")
	third := loadTest(t, root, "", deps)
	if third.Result.State != contextapi.ActivationEnabled || calls != 2 {
		t.Fatalf("entry edit reused stale snapshot: result=%#v calls=%d", third.Result, calls)
	}
}

func TestCachePolicyOwnsHomeCaptureSeparatelyFromReturnedCaptures(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectPath(root), "enabled")
	home := filepath.Join(root, "home-workbench-context.pkl")
	writeTestFile(t, home, "enabled")
	deps := testDependencies(t, nil)
	deps.Freshness = func(_ context.Context, _ contextapi.EvaluationInput, value contextapi.EvaluatedDeclaration) error {
		if value.Kind == contextapi.DeclarationKindHome && value.Home.Limits.Defaults.Selection.Role != "home-role" {
			return errors.New("home capture was mutated")
		}
		return nil
	}
	loaded := loadTest(t, root, home, deps)
	if loaded.CachePolicy == nil || len(loaded.Captures) < 2 {
		t.Fatalf("present-home policy/captures = policy=%#v captures=%d", loaded.CachePolicy, len(loaded.Captures))
	}
	loaded.Captures[0].Declaration.Home.Limits.Defaults.Selection.Role = "caller-mutated"
	if err := loaded.CachePolicy.Validate(context.Background()); err != nil {
		t.Fatalf("cache policy retained caller-owned capture: %v", err)
	}
}

func TestResolveClonesCanonicalValuesAndBlocksAncestorFallback(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	working := filepath.Join(child, "nested")
	project := contextapi.ProjectSnapshot{SourcePath: projectPath(child), Scope: makeScopeIdentity(contextapi.ScopeAuthorityProject, child, "sha256:child"), Config: contextapi.ProjectDeclaration{Enabled: true, Scope: contextapi.DeclarationScopeDirectory, Contributors: map[contextapi.ContributorName]contextapi.Contributor{"named": {Enabled: true, Kind: contextapi.ContributorKindAiContext}}}}
	input := contextapi.ActivationInput{WorkingDirectory: working, Now: testNow, Config: contextapi.ActivationSnapshot{ConfigDigest: "sha256:effective", Projects: []contextapi.ProjectSnapshot{project}}}
	result := Resolve(input)
	if result.State != contextapi.ActivationInactive || !reasonSummaryContains(result, "does not cover") {
		t.Fatalf("directory boundary result = %#v", result)
	}
	project.Config.Scope = contextapi.DeclarationScopeSubtree
	input.Config.Projects[0] = project
	result = Resolve(input)
	if result.State != contextapi.ActivationEnabled || result.Effective.Providers[0].ID != "named" {
		t.Fatalf("canonical resolve result = %#v", result)
	}
	result.Effective.Providers[0].Capabilities[0] = contextapi.ProviderCapabilityProfile
	if project.Config.Contributors["named"].Kind != contextapi.ContributorKindAiContext {
		t.Fatal("resolved provider mutation changed input")
	}
}

func TestResolveClampsDeliveryToRuntimeCeilings(t *testing.T) {
	root := t.TempDir()
	project := contextapi.ProjectSnapshot{SourcePath: projectPath(root), Scope: makeScopeIdentity(contextapi.ScopeAuthorityProject, root, "sha256:project"), Config: contextapi.ProjectDeclaration{Enabled: true, Scope: contextapi.DeclarationScopeSubtree, Contributors: map[contextapi.ContributorName]contextapi.Contributor{"named": {Enabled: true, Kind: contextapi.ContributorKindAiContext}}}}
	input := contextapi.ActivationInput{WorkingDirectory: root, Now: testNow, Config: contextapi.ActivationSnapshot{ConfigDigest: "sha256:effective", Projects: []contextapi.ProjectSnapshot{project}, Home: contextapi.HomeSnapshot{Runtime: contextapi.RuntimeLimits{MaxOutstandingOffers: 2, MaxReceipts: 3}, Delivery: contextapi.EngineLimits{MaxLiveOffers: 10, MaxReceipts: 20}}}}
	result := Resolve(input)
	if result.State != contextapi.ActivationEnabled || result.Effective.Delivery.MaxLiveOffers != 2 || result.Effective.Delivery.MaxReceipts != 3 {
		t.Fatalf("delivery did not honor runtime ceilings: %#v", result)
	}
}

func TestResolveConflictingHomeRootsPrecedeDisabledState(t *testing.T) {
	root := t.TempDir()
	selection := func(enabled bool) contextapi.HomeDirectorySnapshot {
		return contextapi.HomeDirectorySnapshot{
			Scope: makeScopeIdentity(contextapi.ScopeAuthorityHome, root, "sha256:home"),
			Config: contextapi.HomeDirectorySelection{
				Root: root, Enabled: enabled, Scope: contextapi.DeclarationScopeSubtree,
				Contributors: map[contextapi.ContributorName]contextapi.Contributor{"home": {Enabled: true, Kind: contextapi.ContributorKindAiContext}},
			},
		}
	}
	input := contextapi.ActivationInput{WorkingDirectory: root, Now: testNow, Config: contextapi.ActivationSnapshot{Home: contextapi.HomeSnapshot{Scopes: []contextapi.HomeDirectorySnapshot{selection(false), selection(true)}}}}
	result := Resolve(input)
	if result.State != contextapi.ActivationConflict || !reasonSummaryContains(result, "home declarations conflict") {
		t.Fatalf("duplicate home roots were shadowed by disabled state: %#v", result)
	}
}
