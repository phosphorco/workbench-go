package contextconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
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

func loadTest(t *testing.T, directory, home string) LoadResult {
	t.Helper()
	loaded, err := Load(LoadOptions{
		WorkingDirectory: directory,
		HomeConfigPath:   home,
		Now:              testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func projectConfig(root string) string {
	return filepath.Join(root, projectConfigRelativePath)
}

func reasonSummaryContains(result contextapi.ActivationResult, needle string) bool {
	for _, reason := range result.Reasons {
		if strings.Contains(reason.Summary, needle) {
			return true
		}
	}
	return false
}

func TestLoadMinimalOptInDefaultsAndProviderLimits(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "repo", "child")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projectConfig(filepath.Join(root, "repo")), `{"schemaVersion":1,"optIn":true,"includeChildren":true}`)

	loaded := loadTest(t, work, "")
	if loaded.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("state = %q, reasons = %#v", loaded.Result.State, loaded.Result.Reasons)
	}
	if !strings.HasPrefix(string(loaded.Input.Config.ConfigDigest), sha256Prefix) {
		t.Fatalf("activation digest is not canonical: %q", loaded.Input.Config.ConfigDigest)
	}
	if loaded.Input.WorkingDirectory != work {
		t.Fatalf("working directory = %q, want %q", loaded.Input.WorkingDirectory, work)
	}
	providers := loaded.Result.Effective.Providers
	if len(providers) != 1 || providers[0].ID != "ai-context" {
		t.Fatalf("providers = %#v", providers)
	}
	if providers[0].Limits != defaultProviderLimits() {
		t.Fatalf("provider limits = %#v, want defaults %#v", providers[0].Limits, defaultProviderLimits())
	}
	if loaded.Result.Effective.Runtime.MaxProviderProcesses != 4 {
		t.Fatalf("MaxProviderProcesses = %d, want 4", loaded.Result.Effective.Runtime.MaxProviderProcesses)
	}
	if loaded.Result.Effective.Cache.DiskCapBytes != 5_000_000_000 {
		t.Fatalf("DiskCapBytes = %d", loaded.Result.Effective.Cache.DiskCapBytes)
	}
	if loaded.Result.Effective.Delivery.MaxRetainedBytes != defaultMaxRetainedBytes {
		t.Fatalf("MaxRetainedBytes = %d, want %d", loaded.Result.Effective.Delivery.MaxRetainedBytes, defaultMaxRetainedBytes)
	}
}

func TestProviderListAbsenceAndExplicitEmptyAreDistinct(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectConfig(root), `{"schemaVersion":1,"optIn":true,"includeChildren":true}`)
	first := loadTest(t, root, "")
	if first.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("absent providers state = %q", first.Result.State)
	}

	writeTestFile(t, projectConfig(root), `{"schemaVersion":1,"optIn":true,"includeChildren":true,"providers":[]}`)
	second := loadTest(t, root, "")
	if second.Result.State != contextapi.ActivationInactive {
		t.Fatalf("empty providers state = %q, reasons = %#v", second.Result.State, second.Result.Reasons)
	}
	if !reasonSummaryContains(second.Result, "explicit empty provider list") {
		t.Fatalf("empty-provider explanation missing: %#v", second.Result.Reasons)
	}
}

func TestNearestDeclarationWithdrawalAndSymlinkCanonicalization(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	first := loadTest(t, child, "")
	if first.Result.State != contextapi.ActivationInactive {
		t.Fatalf("without declaration state = %q", first.Result.State)
	}

	writeTestFile(t, projectConfig(parent), `{"schemaVersion":1,"optIn":true,"includeChildren":true}`)
	second := loadTest(t, child, "")
	if second.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("parent declaration state = %q, reasons = %#v", second.Result.State, second.Result.Reasons)
	}
	originalScopeID := second.Result.Scope.ID
	originalDigest := second.Result.Scope.ConfigDigest
	writeTestFile(t, projectConfig(parent), `{"schemaVersion":1,"optIn":true,"includeChildren":true,"profile":{"role":"edited"}}`)
	edited := loadTest(t, child, "")
	if edited.Result.Scope.ID != originalScopeID || edited.Result.Scope.ConfigDigest == originalDigest {
		t.Fatalf("edited declaration identity = (%q, %q), original = (%q, %q)", edited.Result.Scope.ID, edited.Result.Scope.ConfigDigest, originalScopeID, originalDigest)
	}

	writeTestFile(t, projectConfig(child), `{"schemaVersion":1,"optIn":false,"includeChildren":true}`)
	third := loadTest(t, child, "")
	if third.Result.State != contextapi.ActivationInactive || !reasonSummaryContains(third.Result, "opts out") {
		t.Fatalf("nearer opt-out result = %#v", third.Result)
	}
	if err := os.Remove(projectConfig(child)); err != nil {
		t.Fatal(err)
	}
	fourth := loadTest(t, child, "")
	if fourth.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("withdrawn nearer declaration state = %q", fourth.Result.State)
	}

	alias := filepath.Join(root, "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	viaAlias := loadTest(t, filepath.Join(alias, "child"), "")
	if viaAlias.Input.WorkingDirectory != child {
		t.Fatalf("symlink working directory = %q, want %q", viaAlias.Input.WorkingDirectory, child)
	}
	if viaAlias.Result.Scope.ID != fourth.Result.Scope.ID || viaAlias.Result.State != fourth.Result.State {
		t.Fatalf("symlink result = %#v, canonical result = %#v", viaAlias.Result, fourth.Result)
	}

	otherParent := filepath.Join(root, "other")
	otherChild := filepath.Join(otherParent, "child")
	if err := os.MkdirAll(otherChild, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projectConfig(otherParent), `{"schemaVersion":1,"optIn":true,"includeChildren":true}`)
	other := loadTest(t, otherChild, "")
	if other.Result.Scope.ID == fourth.Result.Scope.ID {
		t.Fatalf("distinct worktrees unexpectedly share scope ID %q", other.Result.Scope.ID)
	}
}

func TestHomeExclusionPolicyAndNearestOptOut(t *testing.T) {
	root := t.TempDir()
	excluded := filepath.Join(root, "excluded")
	if err := os.MkdirAll(excluded, 0o755); err != nil {
		t.Fatal(err)
	}
	project := `{"schemaVersion":1,"optIn":true,"includeChildren":true}`
	writeTestFile(t, projectConfig(root), project)
	home := filepath.Join(t.TempDir(), "context.json")
	writeTestFile(t, home, fmt.Sprintf(`{"schemaVersion":1,"scopes":[{"root":%q,"optIn":true,"includeChildren":true}],"exclusions":[{"root":%q,"includeChildren":true}]}`, root, excluded))

	excludedResult := loadTest(t, excluded, home)
	if excludedResult.Result.State != contextapi.ActivationInactive || !reasonSummaryContains(excludedResult.Result, "exclusion") {
		t.Fatalf("excluded result = %#v", excludedResult.Result)
	}

	allowed := loadTest(t, root, home)
	if allowed.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("allowed result = %#v", allowed.Result)
	}

	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projectConfig(nested), `{"schemaVersion":1,"optIn":false,"includeChildren":true}`)
	nestedHome := filepath.Join(t.TempDir(), "context.json")
	writeTestFile(t, nestedHome, fmt.Sprintf(`{"schemaVersion":1,"scopes":[{"root":%q,"optIn":true,"includeChildren":true},{"root":%q,"optIn":false,"includeChildren":true}]}`, root, nested))
	nestedResult := loadTest(t, nested, nestedHome)
	if nestedResult.Result.State != contextapi.ActivationInactive || !reasonSummaryContains(nestedResult.Result, "nearest home scope") {
		t.Fatalf("nested home opt-out result = %#v", nestedResult.Result)
	}

	duplicateHome := filepath.Join(t.TempDir(), "context.json")
	writeTestFile(t, duplicateHome, fmt.Sprintf(`{"schemaVersion":1,"scopes":[{"root":%q,"optIn":true,"includeChildren":true},{"root":%q,"optIn":false,"includeChildren":true}]}`, root, root))
	conflict := loadTest(t, root, duplicateHome)
	if conflict.Result.State != contextapi.ActivationConflict || !reasonSummaryContains(conflict.Result, "home declarations conflict") {
		t.Fatalf("same-root home conflict = %#v", conflict.Result)
	}
}

func TestHomeProviderPolicyAndRuntimeCacheDefaults(t *testing.T) {
	root := t.TempDir()
	provider := `{"id":"external","kind":"executable","executable":"/definitely-not-started","capabilities":["contribute"]}`
	writeTestFile(t, projectConfig(root), fmt.Sprintf(`{"schemaVersion":1,"optIn":true,"includeChildren":true,"providers":[%s]}`, provider))
	home := filepath.Join(t.TempDir(), "context.json")
	writeTestFile(t, home, fmt.Sprintf(`{"schemaVersion":1,"scopes":[{"root":%q,"optIn":true,"includeChildren":true}],"providerPolicy":{"mode":"allowlist","allowed":["ai-context"]},"runtime":{"maxOutstandingOffers":2,"maxReceipts":3,"idleTTLMs":3600000,"providerDefaults":{"deadlineMs":77}},"delivery":{"queue":{"maxItemBytes":128},"maxLiveOffers":1,"maxReceipts":1,"maxOfferAgeMs":1000},"cache":{"diskCapBytes":123456}}`, root))

	blocked := loadTest(t, root, home)
	if blocked.Result.State != contextapi.ActivationConflict || !reasonSummaryContains(blocked.Result, "excludes") {
		t.Fatalf("blocked result = %#v", blocked.Result)
	}

	writeTestFile(t, projectConfig(root), `{"schemaVersion":1,"optIn":true,"includeChildren":true}`)
	enabled := loadTest(t, root, home)
	if enabled.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("default provider result = %#v", enabled.Result)
	}
	if enabled.Result.Effective.Runtime.MaxOutstandingOffers != 2 || enabled.Result.Effective.Runtime.MaxReceipts != 3 || enabled.Result.Effective.Runtime.IdleTTLMs != 3600000 {
		t.Fatalf("runtime = %#v", enabled.Result.Effective.Runtime)
	}
	if enabled.Result.Effective.Cache.DiskCapBytes != 123456 {
		t.Fatalf("cache = %#v", enabled.Result.Effective.Cache)
	}
	if enabled.Result.Effective.Providers[0].Limits.DeadlineMs != 77 {
		t.Fatalf("home provider default = %#v", enabled.Result.Effective.Providers[0].Limits)
	}
	if enabled.Result.Effective.Delivery.MaxLiveOffers != 1 || enabled.Result.Effective.Delivery.MaxReceipts != 1 || enabled.Result.Effective.Delivery.Queue.MaxItemBytes != 128 {
		t.Fatalf("delivery caps = %#v", enabled.Result.Effective.Delivery)
	}
}

func TestDeliveryRetainedBytesDefaultAndGlobalMemoryClamp(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectConfig(root), `{"schemaVersion":1,"optIn":true,"includeChildren":true}`)
	home := filepath.Join(t.TempDir(), "context.json")
	writeTestFile(t, home, fmt.Sprintf(`{"schemaVersion":1,"scopes":[{"root":%q,"optIn":true,"includeChildren":true}],"runtime":{"maxPendingMemoryBytes":1000000},"delivery":{"maxRetainedBytes":99000000}}`, root))

	loaded := loadTest(t, root, home)
	if loaded.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("state = %q, reasons = %#v", loaded.Result.State, loaded.Result.Reasons)
	}
	if loaded.Input.Config.Home.Delivery.MaxRetainedBytes != 1_000_000 {
		t.Fatalf("snapshot MaxRetainedBytes = %d, want 1000000", loaded.Input.Config.Home.Delivery.MaxRetainedBytes)
	}
	if loaded.Result.Effective.Delivery.MaxRetainedBytes != 1_000_000 {
		t.Fatalf("effective MaxRetainedBytes = %d, want 1000000", loaded.Result.Effective.Delivery.MaxRetainedBytes)
	}
}

func TestExecutableWithoutTuningNumbersAndInactiveDoesNotExecute(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectConfig(root), `{"schemaVersion":1,"optIn":true,"includeChildren":true,"providers":[{"id":"external","kind":"executable","executable":"/path/that/is/not/executed","capabilities":["contribute"]}]}`)
	loaded := loadTest(t, root, "")
	if loaded.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("executable without limits state = %q, reasons = %#v", loaded.Result.State, loaded.Result.Reasons)
	}
	if loaded.Result.Effective.Providers[0].Limits != defaultProviderLimits() {
		t.Fatalf("resolved executable limits = %#v", loaded.Result.Effective.Providers[0].Limits)
	}

	writeTestFile(t, projectConfig(root), `{"schemaVersion":1,"optIn":false,"includeChildren":true,"providers":[{"id":"external","kind":"executable","executable":"/path/that/is/not/executed"}]}`)
	inactive := loadTest(t, root, "")
	if inactive.Result.State != contextapi.ActivationInactive {
		t.Fatalf("inactive executable state = %q", inactive.Result.State)
	}
}

func TestInvalidAndBoundedConfigurationFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, projectConfig(root), `{"schemaVersion":1,"optIn":true,"includeChildren":true`)
	invalid := loadTest(t, root, "")
	if invalid.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(invalid.Result, "invalid") {
		t.Fatalf("malformed result = %#v", invalid.Result)
	}

	writeTestFile(t, projectConfig(root), `{"schemaVersion":1,"optIn":true,"includeChildren":true,"profile":{"role":"bounded"}}`)
	bounded, err := Load(LoadOptions{
		WorkingDirectory: root,
		Now:              testNow,
		Limits:           LoadLimits{MaxConfigBytes: 16},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bounded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(bounded.Result, "exceeds") {
		t.Fatalf("oversized result = %#v", bounded.Result)
	}

	home := filepath.Join(t.TempDir(), "context.json")
	writeTestFile(t, home, fmt.Sprintf(`{"schemaVersion":1,"scopes":[{"root":%q,"optIn":true,"includeChildren":true}],"runtime":{"hookDeadlineMs":2001,"wholeHookDeadlineMs":1000}}`, root))
	homeInvalid := loadTest(t, root, home)
	if homeInvalid.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(homeInvalid.Result, "cannot exceed") {
		t.Fatalf("invalid runtime result = %#v", homeInvalid.Result)
	}
}

func TestHomeRuntimeBoundsConstrainProjectLookup(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	project := `{"schemaVersion":1,"optIn":true,"includeChildren":true}`
	writeTestFile(t, projectConfig(root), project)
	writeTestFile(t, projectConfig(child), project)
	home := filepath.Join(t.TempDir(), "context.json")
	writeTestFile(t, home, fmt.Sprintf(`{"schemaVersion":1,"scopes":[{"root":%q,"optIn":true,"includeChildren":true}],"runtime":{"maxActivationConfigFiles":1}}`, root))

	loaded := loadTest(t, child, home)
	if loaded.Result.State != contextapi.ActivationInvalid || !reasonSummaryContains(loaded.Result, "count exceeded") {
		t.Fatalf("runtime project-file bound result = %#v", loaded.Result)
	}
}

func TestResolveDoesNotAliasInputMutableProviderFields(t *testing.T) {
	root := t.TempDir()
	provider := contextapi.ProviderConfig{
		ID:           "external",
		Kind:         contextapi.ProviderKindExecutable,
		Executable:   "/not-started",
		Arguments:    []string{"--safe"},
		Settings:     []byte(`{"mode":"safe"}`),
		Capabilities: []contextapi.ProviderCapability{contextapi.ProviderCapabilityContribute},
	}
	input := contextapi.ActivationInput{
		WorkingDirectory: root,
		Now:              testNow,
		Config: contextapi.ActivationSnapshot{
			SchemaVersion: 1,
			ConfigDigest:  "sha256:input",
			Projects: []contextapi.ProjectSnapshot{{
				SourcePath: projectConfig(root),
				Scope: contextapi.ScopeIdentity{
					ID:            "scope:input",
					Authority:     contextapi.ScopeAuthorityProject,
					CanonicalRoot: root,
					ConfigDigest:  "sha256:project",
				},
				Config: contextapi.ProjectFile{SchemaVersion: 1, OptIn: true, IncludeChildren: true, Providers: []contextapi.ProviderConfig{provider}},
			}},
		},
	}
	result := Resolve(input)
	if result.State != contextapi.ActivationEnabled {
		t.Fatalf("state = %q, reasons = %#v", result.State, result.Reasons)
	}
	result.Effective.Providers[0].Arguments[0] = "--changed"
	result.Effective.Providers[0].Settings[0] = 'X'
	result.Effective.Providers[0].Capabilities[0] = contextapi.ProviderCapabilityProfile
	if provider.Arguments[0] != "--safe" || provider.Settings[0] != '{' || provider.Capabilities[0] != contextapi.ProviderCapabilityContribute {
		t.Fatal("result mutation changed local provider input")
	}
	input.Config.Projects[0].Config.Providers[0].Arguments[0] = "--input-changed"
	if result.Effective.Providers[0].Arguments[0] != "--changed" {
		t.Fatal("input mutation changed already-resolved result")
	}
}

func BenchmarkResolveInactive(b *testing.B) {
	input := contextapi.ActivationInput{
		WorkingDirectory: "/tmp/workbench-context-benchmark",
		Now:              testNow,
		Config:           contextapi.ActivationSnapshot{SchemaVersion: 1, ConfigDigest: "sha256:empty"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Resolve(input)
	}
}

func BenchmarkLoadInactive(b *testing.B) {
	root := b.TempDir()
	options := LoadOptions{WorkingDirectory: root, Now: testNow}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Load(options); err != nil {
			b.Fatal(err)
		}
	}
}
