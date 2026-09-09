// Package contextconfig loads and resolves Workbench context activation.
//
// Load is the bounded filesystem boundary. Resolve is pure and consumes only
// the value snapshot produced by Load; neither function starts providers,
// writes files, or talks to the runtime.
package contextconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

const (
	projectConfigRelativePath = ".workbench/context.json"
	sha256Prefix              = "sha256:"
	invalidDigestPrefix       = "invalid:"
	maxHookDeadlineMs         = uint64(60 * 1000)
	maxWholeHookDeadlineMs    = uint64(5 * 60 * 1000)
	maxActivationAncestors    = uint32(4096)
	maxActivationConfigBytes  = uint64(64 * 1024 * 1024)
	maxActivationConfigFiles  = uint32(4096)
	maxConfiguredProviders    = uint32(4096)
	maxConfiguredCount        = uint32(1_000_000)
	maxContributionBodyBytes  = uint64(64 * 1024 * 1024)
	maxPendingMemoryBytes     = uint64(1024 * 1024 * 1024)
	maxQueueItemBytes         = uint64(64 * 1024 * 1024)
	maxQueuePendingBytes      = uint64(1024 * 1024 * 1024)
	maxQueueAgeMs             = uint64(30 * 24 * 60 * 60 * 1000)
	maxProviderDeadlineMs     = uint64(60 * 1000)
	maxProviderBytes          = uint64(64 * 1024 * 1024)
	maxCacheDiskCapBytes      = uint64(64 * 1024 * 1024 * 1024)
	maxCacheIdleTTLMs         = uint64(365 * 24 * 60 * 60 * 1000)
	maxCacheBytes             = uint64(256 * 1024 * 1024)
	maxCacheStringBytes       = uint64(1024 * 1024)
	defaultMaxRetainedBytes   = uint64(4 * 1024 * 1024)
)

// LoadLimits bounds work performed while resolving one hook invocation. The
// zero value is filled from DefaultLoadLimits, allowing callers to override a
// single bound without copying the complete default set.
type LoadLimits struct {
	MaxAncestorDepth    int
	MaxProjectConfigs   int
	MaxConfigBytes      int64
	MaxHomeScopes       int
	MaxProviders        int
	MaxProfileProviders int
}

// DefaultLoadLimits are intentionally small enough for a hook fast path. The
// loader performs one direct candidate-file lookup per ancestor; it never
// recursively scans a directory.
func DefaultLoadLimits() LoadLimits {
	return LoadLimits{
		MaxAncestorDepth:    64,
		MaxProjectConfigs:   32,
		MaxConfigBytes:      256 * 1024,
		MaxHomeScopes:       64,
		MaxProviders:        64,
		MaxProfileProviders: 64,
	}
}

// LoadOptions makes all filesystem inputs explicit. An empty HomeConfigPath
// means that the optional XDG/home declaration is absent; the loader does not
// guess a home path or consult environment variables.
type LoadOptions struct {
	WorkingDirectory string
	HomeConfigPath   string
	Now              time.Time
	Limits           LoadLimits
}

// LoadResult keeps the exact value input alongside the interpreted result so
// hooks and status inspection can use the same Resolve interpretation. Expected
// malformed/unreadable configuration is represented as ActivationInvalid with
// structured reasons rather than returned as an untyped error.
type LoadResult struct {
	Input  contextapi.ActivationInput
	Result contextapi.ActivationResult
}

var errInvalidLoadOptions = errors.New("invalid context configuration load options")

type loadIssue struct {
	reason contextapi.Reason
}

type projectCandidate struct {
	snapshot contextapi.ProjectSnapshot
	depth    int
}

type homeCandidate struct {
	snapshot contextapi.HomeScopeSnapshot
	depth    int
}

// Load reads the explicit home file and bounded .workbench/context.json
// candidates from the canonical working directory to its ancestors. Missing
// optional files are normal; malformed, oversized, unreadable, or ambiguous
// declarations fail closed with ActivationInvalid reasons.
func Load(options LoadOptions) (LoadResult, error) {
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return LoadResult{}, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	input := contextapi.ActivationInput{
		WorkingDirectory: options.WorkingDirectory,
		Now:              now,
		Config: contextapi.ActivationSnapshot{
			SchemaVersion: 1,
		},
	}
	issues := make([]loadIssue, 0, 2)

	workingDirectory, issue := canonicalDirectory(options.WorkingDirectory, now)
	if issue != nil {
		issues = append(issues, loadIssue{reason: *issue})
		input.Config.ConfigDigest = invalidDigestPrefix + "working-directory"
		result := invalidResult(input, reasonsFromIssues(issues))
		return LoadResult{Input: input, Result: result}, nil
	}
	input.WorkingDirectory = workingDirectory

	home, homeIssues := loadHome(options.HomeConfigPath, limits, now)
	input.Config.Home = home
	issues = append(issues, homeIssues...)
	if len(homeIssues) == 0 {
		limits = applyRuntimeLoadBounds(limits, home.Runtime)
	}

	providerDefaults := defaultProviderLimits()
	if len(homeIssues) == 0 && home.ConfigDigest != "" {
		providerDefaults = home.Runtime.ProviderDefaults
	}
	projects, projectIssues := loadProjects(workingDirectory, limits, providerDefaults, now)
	input.Config.Projects = projects
	issues = append(issues, projectIssues...)
	input.Config.ConfigDigest = activationDigest(home, projects, issues)

	result := Resolve(input)
	if len(issues) > 0 {
		result = invalidResult(input, reasonsFromIssues(issues))
	}
	return LoadResult{Input: input, Result: result}, nil
}

func normalizeLimits(input LoadLimits) (LoadLimits, error) {
	defaults := DefaultLoadLimits()
	if input.MaxAncestorDepth == 0 {
		input.MaxAncestorDepth = defaults.MaxAncestorDepth
	}
	if input.MaxProjectConfigs == 0 {
		input.MaxProjectConfigs = defaults.MaxProjectConfigs
	}
	if input.MaxConfigBytes == 0 {
		input.MaxConfigBytes = defaults.MaxConfigBytes
	}
	if input.MaxHomeScopes == 0 {
		input.MaxHomeScopes = defaults.MaxHomeScopes
	}
	if input.MaxProviders == 0 {
		input.MaxProviders = defaults.MaxProviders
	}
	if input.MaxProfileProviders == 0 {
		input.MaxProfileProviders = defaults.MaxProfileProviders
	}
	if input.MaxAncestorDepth < 1 || input.MaxProjectConfigs < 1 || input.MaxConfigBytes < 1 ||
		input.MaxHomeScopes < 1 || input.MaxProviders < 1 || input.MaxProfileProviders < 1 {
		return LoadLimits{}, errInvalidLoadOptions
	}
	return input, nil
}

func applyRuntimeLoadBounds(limits LoadLimits, runtime contextapi.RuntimeLimits) LoadLimits {
	if runtime.MaxActivationAncestors > 0 && uint64(limits.MaxAncestorDepth) > uint64(runtime.MaxActivationAncestors) {
		limits.MaxAncestorDepth = int(runtime.MaxActivationAncestors)
	}
	if runtime.MaxActivationConfigFiles > 0 && uint64(limits.MaxProjectConfigs) > uint64(runtime.MaxActivationConfigFiles) {
		limits.MaxProjectConfigs = int(runtime.MaxActivationConfigFiles)
	}
	if runtime.MaxActivationConfigBytes > 0 && limits.MaxConfigBytes > int64(runtime.MaxActivationConfigBytes) {
		limits.MaxConfigBytes = int64(runtime.MaxActivationConfigBytes)
	}
	if runtime.MaxProviders > 0 && uint64(limits.MaxProviders) > uint64(runtime.MaxProviders) {
		limits.MaxProviders = int(runtime.MaxProviders)
	}
	return limits
}

func canonicalDirectory(input string, now time.Time) (string, *contextapi.Reason) {
	if input == "" || !filepath.IsAbs(input) {
		reason := makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter,
			"working directory must be an absolute path", now)
		return "", &reason
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(input))
	if err != nil {
		reason := makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter,
			fmt.Sprintf("working directory cannot be canonicalized: %v", err), now)
		return "", &reason
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		message := "working directory is not a directory"
		if err != nil {
			message = fmt.Sprintf("working directory cannot be read: %v", err)
		}
		reason := makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter, message, now)
		return "", &reason
	}
	return filepath.Clean(canonical), nil
}

func loadHome(path string, limits LoadLimits, now time.Time) (contextapi.HomeSnapshot, []loadIssue) {
	if path == "" {
		return contextapi.HomeSnapshot{}, nil
	}
	raw, canonicalPath, exists, err := readOptionalConfig(path, limits.MaxConfigBytes)
	if !exists && err == nil {
		return contextapi.HomeSnapshot{}, nil
	}
	if err != nil {
		return contextapi.HomeSnapshot{}, []loadIssue{{reason: makeReason(
			contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
			fmt.Sprintf("home configuration %q cannot be loaded: %v", path, err), now)}}
	}
	var authored contextapi.HomeFile
	if err := decodeConfig(raw, &authored); err != nil {
		return contextapi.HomeSnapshot{}, []loadIssue{{reason: makeReason(
			contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
			fmt.Sprintf("home configuration %q is invalid: %v", canonicalPath, err), now)}}
	}
	authored.Runtime = resolveRuntimeLimits(authored.Runtime)
	authored.Cache = resolveCacheLimits(authored.Cache)
	normalizeHomeProviderLimits(&authored)
	if err := validateHome(authored, limits); err != nil {
		return contextapi.HomeSnapshot{}, []loadIssue{{reason: makeReason(
			contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
			fmt.Sprintf("home configuration %q is invalid: %v", canonicalPath, err), now)}}
	}

	home := contextapi.HomeSnapshot{
		ConfigDigest:   digestBytes(raw),
		ProviderPolicy: cloneProviderPolicy(authored.ProviderPolicy),
		Defaults:       cloneProfileDefaults(authored.Defaults),
		Delivery:       cloneEngineLimits(resolveDeliveryLimits(authored.Delivery, authored.Runtime)),
		Runtime:        resolveRuntimeLimits(authored.Runtime),
		Cache:          resolveCacheLimits(authored.Cache),
	}
	issues := make([]loadIssue, 0)
	for index, scope := range authored.Scopes {
		root, err := canonicalConfigRoot(scope.Root, filepath.Dir(canonicalPath))
		if err != nil {
			issues = append(issues, loadIssue{reason: makeReason(
				contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
				fmt.Sprintf("home scope %d root %q is invalid: %v", index, scope.Root, err), now)})
			continue
		}
		identity := makeScopeIdentity(contextapi.ScopeAuthorityHome, root, raw)
		home.Scopes = append(home.Scopes, contextapi.HomeScopeSnapshot{
			Scope: identity,
			Config: contextapi.HomeScopeFile{
				Root:             root,
				OptIn:            scope.OptIn,
				IncludeChildren:  scope.IncludeChildren,
				Providers:        cloneProviderConfigs(scope.Providers),
				ProfileProviders: cloneProviderIDs(scope.ProfileProviders),
				Profile:          cloneProfileSelection(scope.Profile),
			},
		})
	}
	for index, exclusion := range authored.Exclusions {
		root, err := canonicalConfigRoot(exclusion.Root, filepath.Dir(canonicalPath))
		if err != nil {
			issues = append(issues, loadIssue{reason: makeReason(
				contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
				fmt.Sprintf("home exclusion %d root %q is invalid: %v", index, exclusion.Root, err), now)})
			continue
		}
		home.Exclusions = append(home.Exclusions, contextapi.PathRule{Root: root, IncludeChildren: exclusion.IncludeChildren})
	}
	return home, issues
}

func loadProjects(workingDirectory string, limits LoadLimits, providerDefaults contextapi.ProviderLimits, now time.Time) ([]contextapi.ProjectSnapshot, []loadIssue) {
	projects := make([]contextapi.ProjectSnapshot, 0)
	issues := make([]loadIssue, 0)
	current := workingDirectory
	for depth := 0; ; depth++ {
		if depth >= limits.MaxAncestorDepth {
			issues = append(issues, loadIssue{reason: makeReason(
				contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter,
				fmt.Sprintf("ancestor lookup exceeded bound %d", limits.MaxAncestorDepth), now)})
			break
		}
		candidatePath := filepath.Join(current, projectConfigRelativePath)
		raw, canonicalPath, exists, err := readOptionalConfig(candidatePath, limits.MaxConfigBytes)
		if err != nil {
			if !os.IsNotExist(err) {
				issues = append(issues, loadIssue{reason: makeReason(
					contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
					fmt.Sprintf("project configuration %q cannot be loaded: %v", candidatePath, err), now)})
				break
			}
		} else if exists {
			if len(projects) >= limits.MaxProjectConfigs {
				issues = append(issues, loadIssue{reason: makeReason(
					contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter,
					fmt.Sprintf("project configuration count exceeded bound %d", limits.MaxProjectConfigs), now)})
				break
			}
			var authored contextapi.ProjectFile
			decodeErr := decodeConfig(raw, &authored)
			if decodeErr == nil {
				normalizeProjectProviderLimits(&authored, providerDefaults)
				decodeErr = validateProject(authored, raw, limits)
			}
			identity := makeScopeIdentity(contextapi.ScopeAuthorityProject, current, raw)
			if decodeErr != nil {
				// Keep an invalid marker in the pure snapshot as well as the
				// detailed LoadResult reason. Resolve therefore cannot accidentally
				// activate a manually reused invalid snapshot.
				identity.ConfigDigest = invalidDigestPrefix + digestBytes(raw)
				projects = append(projects, contextapi.ProjectSnapshot{
					SourcePath: canonicalPath,
					Scope:      identity,
					Config:     authored,
				})
				issues = append(issues, loadIssue{reason: makeReason(
					contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
					fmt.Sprintf("project configuration %q is invalid: %v", canonicalPath, decodeErr), now)})
				break
			}
			projects = append(projects, contextapi.ProjectSnapshot{
				SourcePath: canonicalPath,
				Scope:      identity,
				Config: contextapi.ProjectFile{
					SchemaVersion:    authored.SchemaVersion,
					OptIn:            authored.OptIn,
					IncludeChildren:  authored.IncludeChildren,
					Providers:        cloneProviderConfigs(authored.Providers),
					ProfileProviders: cloneProviderIDs(authored.ProfileProviders),
					Profile:          cloneProfileSelection(authored.Profile),
				},
			})
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return projects, issues
}

func readOptionalConfig(path string, maxBytes int64) ([]byte, string, bool, error) {
	canonicalPath, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return nil, "", false, err
	}
	if !info.Mode().IsRegular() {
		return nil, canonicalPath, true, fmt.Errorf("not a regular file")
	}
	file, err := os.Open(canonicalPath)
	if err != nil {
		return nil, canonicalPath, true, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, canonicalPath, true, err
	}
	if int64(len(contents)) > maxBytes {
		return nil, canonicalPath, true, fmt.Errorf("file exceeds %d-byte bound", maxBytes)
	}
	return contents, canonicalPath, true, nil
}

func decodeConfig(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func defaultProviderLimits() contextapi.ProviderLimits {
	return contextapi.ProviderLimits{
		DeadlineMs:       500,
		MaxResponseBytes: 256 * 1024,
		MaxFacts:         32,
		MaxContributions: 64,
		MaxBodyBytes:     256 * 1024,
	}
}

func providerLimitsWithDefaults(input contextapi.ProviderLimits) contextapi.ProviderLimits {
	return providerLimitsWithDefaultsFrom(input, defaultProviderLimits())
}

func providerLimitsWithDefaultsFrom(input, defaults contextapi.ProviderLimits) contextapi.ProviderLimits {
	if input.DeadlineMs == 0 {
		input.DeadlineMs = defaults.DeadlineMs
	}
	if input.MaxResponseBytes == 0 {
		input.MaxResponseBytes = defaults.MaxResponseBytes
	}
	if input.MaxFacts == 0 {
		input.MaxFacts = defaults.MaxFacts
	}
	if input.MaxContributions == 0 {
		input.MaxContributions = defaults.MaxContributions
	}
	if input.MaxBodyBytes == 0 {
		input.MaxBodyBytes = defaults.MaxBodyBytes
	}
	return input
}

func normalizeProviderLimits(providers []contextapi.ProviderConfig) {
	normalizeProviderLimitsWith(providers, defaultProviderLimits())
}

func normalizeProviderLimitsWith(providers []contextapi.ProviderConfig, defaults contextapi.ProviderLimits) {
	for index := range providers {
		providers[index].Limits = providerLimitsWithDefaultsFrom(providers[index].Limits, defaults)
	}
}

func normalizeProjectProviderLimits(project *contextapi.ProjectFile, defaults contextapi.ProviderLimits) {
	normalizeProviderLimitsWith(project.Providers, defaults)
}

func normalizeHomeProviderLimits(home *contextapi.HomeFile) {
	home.Runtime.ProviderDefaults = providerLimitsWithDefaults(home.Runtime.ProviderDefaults)
	for index := range home.Scopes {
		normalizeProviderLimitsWith(home.Scopes[index].Providers, home.Runtime.ProviderDefaults)
	}
}

func validateProject(project contextapi.ProjectFile, raw []byte, limits LoadLimits) error {
	if project.SchemaVersion != 1 {
		return fmt.Errorf("schemaVersion must be 1")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if _, ok := fields["optIn"]; !ok {
		return errors.New("optIn must be explicit")
	}
	return validateProviderLists(project.Providers, project.ProfileProviders, limits)
}

func validateHome(home contextapi.HomeFile, limits LoadLimits) error {
	if home.SchemaVersion != 1 {
		return fmt.Errorf("schemaVersion must be 1")
	}
	if len(home.Scopes) > limits.MaxHomeScopes {
		return fmt.Errorf("scope count exceeds %d", limits.MaxHomeScopes)
	}
	if err := validateProviderPolicy(home.ProviderPolicy); err != nil {
		return err
	}
	if err := validateRuntimeLimits(home.Runtime); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if err := validateCacheLimits(home.Cache); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if err := validateEngineLimits(home.Delivery); err != nil {
		return fmt.Errorf("delivery: %w", err)
	}
	if len(home.Defaults.Providers) > limits.MaxProfileProviders {
		return fmt.Errorf("default profile provider count exceeds %d", limits.MaxProfileProviders)
	}
	if err := validateProviderIDs(home.Defaults.Providers); err != nil {
		return err
	}
	for index, scope := range home.Scopes {
		if strings.TrimSpace(scope.Root) == "" {
			return fmt.Errorf("scope %d root is empty", index)
		}
		if len(scope.Providers) > limits.MaxProviders || len(scope.ProfileProviders) > limits.MaxProfileProviders {
			return fmt.Errorf("scope %d provider count exceeds configured bound", index)
		}
		if err := validateProviderLists(scope.Providers, scope.ProfileProviders, limits); err != nil {
			return fmt.Errorf("scope %d: %w", index, err)
		}
	}
	for index, exclusion := range home.Exclusions {
		if strings.TrimSpace(exclusion.Root) == "" {
			return fmt.Errorf("exclusion %d root is empty", index)
		}
	}
	return nil
}

func validateProviderLists(providers []contextapi.ProviderConfig, profileProviders []contextapi.ProviderID, limits LoadLimits) error {
	if len(providers) > limits.MaxProviders || len(profileProviders) > limits.MaxProfileProviders {
		return errors.New("provider count exceeds configured bound")
	}
	seen := make(map[contextapi.ProviderID]struct{}, len(providers))
	for _, provider := range providers {
		if err := validateProvider(provider); err != nil {
			return err
		}
		if _, exists := seen[provider.ID]; exists {
			return fmt.Errorf("duplicate provider %q", provider.ID)
		}
		seen[provider.ID] = struct{}{}
	}
	return validateProviderIDs(profileProviders)
}

func validateProvider(provider contextapi.ProviderConfig) error {
	provider.Limits = providerLimitsWithDefaults(provider.Limits)
	if strings.TrimSpace(string(provider.ID)) == "" {
		return errors.New("provider id is empty")
	}
	switch provider.Kind {
	case contextapi.ProviderKindBuiltin:
		if provider.Executable != "" {
			return fmt.Errorf("builtin provider %q cannot set executable", provider.ID)
		}
	case contextapi.ProviderKindExecutable:
		if strings.TrimSpace(provider.Executable) == "" {
			return fmt.Errorf("executable provider %q has no executable", provider.ID)
		}
	default:
		return fmt.Errorf("provider %q has unknown kind %q", provider.ID, provider.Kind)
	}
	capabilities := make(map[contextapi.ProviderCapability]struct{}, len(provider.Capabilities))
	for _, capability := range provider.Capabilities {
		if capability != contextapi.ProviderCapabilityProfile && capability != contextapi.ProviderCapabilityContribute {
			return fmt.Errorf("provider %q has unknown capability %q", provider.ID, capability)
		}
		if _, exists := capabilities[capability]; exists {
			return fmt.Errorf("provider %q repeats capability %q", provider.ID, capability)
		}
		capabilities[capability] = struct{}{}
	}
	if err := validateProviderLimits(provider.Limits); err != nil {
		return fmt.Errorf("provider %q: %w", provider.ID, err)
	}
	return nil
}

func validateProviderLimits(limits contextapi.ProviderLimits) error {
	if limits.DeadlineMs > maxProviderDeadlineMs {
		return fmt.Errorf("deadlineMs exceeds %d", maxProviderDeadlineMs)
	}
	if limits.MaxResponseBytes > maxProviderBytes || limits.MaxBodyBytes > maxProviderBytes {
		return fmt.Errorf("response/body bytes exceed %d", maxProviderBytes)
	}
	if limits.MaxFacts > maxConfiguredCount || limits.MaxContributions > maxConfiguredCount {
		return fmt.Errorf("fact/contribution count exceeds %d", maxConfiguredCount)
	}
	return nil
}

func validateRuntimeLimits(runtime contextapi.RuntimeLimits) error {
	if runtime.HookDeadlineMs > 0 && runtime.WholeHookDeadlineMs > 0 && runtime.HookDeadlineMs > runtime.WholeHookDeadlineMs {
		return errors.New("hookDeadlineMs cannot exceed wholeHookDeadlineMs")
	}
	if runtime.HookDeadlineMs > maxHookDeadlineMs {
		return fmt.Errorf("hookDeadlineMs exceeds %d", maxHookDeadlineMs)
	}
	if runtime.WholeHookDeadlineMs > maxWholeHookDeadlineMs {
		return fmt.Errorf("wholeHookDeadlineMs exceeds %d", maxWholeHookDeadlineMs)
	}
	if runtime.MaxActivationAncestors > maxActivationAncestors {
		return fmt.Errorf("maxActivationAncestors exceeds %d", maxActivationAncestors)
	}
	if runtime.MaxActivationConfigBytes > maxActivationConfigBytes {
		return fmt.Errorf("maxActivationConfigBytes exceeds %d", maxActivationConfigBytes)
	}
	if runtime.MaxActivationConfigFiles > maxActivationConfigFiles {
		return fmt.Errorf("maxActivationConfigFiles exceeds %d", maxActivationConfigFiles)
	}
	if runtime.MaxProviders > maxConfiguredProviders {
		return fmt.Errorf("maxProviders exceeds %d", maxConfiguredProviders)
	}
	if runtime.MaxProfileFacts > maxConfiguredCount || runtime.MaxContributions > maxConfiguredCount {
		return fmt.Errorf("profile/contribution count exceeds %d", maxConfiguredCount)
	}
	if runtime.MaxContributionBodyBytes > maxContributionBodyBytes {
		return fmt.Errorf("maxContributionBodyBytes exceeds %d", maxContributionBodyBytes)
	}
	if runtime.MaxActiveScopes > maxConfiguredCount || runtime.MaxActiveAudiences > maxConfiguredCount ||
		runtime.MaxProviderProcesses > maxConfiguredCount || runtime.MaxPendingItems > maxConfiguredCount ||
		runtime.MaxOutstandingOffers > maxConfiguredCount || runtime.MaxReceipts > maxConfiguredCount {
		return fmt.Errorf("runtime count exceeds %d", maxConfiguredCount)
	}
	if runtime.MaxPendingMemoryBytes > maxPendingMemoryBytes {
		return fmt.Errorf("maxPendingMemoryBytes exceeds %d", maxPendingMemoryBytes)
	}
	if runtime.IdleTTLMs > maxCacheIdleTTLMs {
		return fmt.Errorf("idleTTLMs exceeds %d", maxCacheIdleTTLMs)
	}
	return validateProviderLimits(runtime.ProviderDefaults)
}

func validateQueueLimits(queue contextapi.QueueLimits) error {
	if queue.MaxItemBytes > maxQueueItemBytes || queue.MaxPendingBytes > maxQueuePendingBytes {
		return fmt.Errorf("queue bytes exceed configured maximum")
	}
	if queue.MaxPendingItems > maxConfiguredCount {
		return fmt.Errorf("maxPendingItems exceeds %d", maxConfiguredCount)
	}
	if queue.MaxAgeMs > maxQueueAgeMs {
		return fmt.Errorf("maxAgeMs exceeds %d", maxQueueAgeMs)
	}
	return nil
}

func validateCacheLimits(cache contextapi.CacheLimits) error {
	if cache.DiskCapBytes > maxCacheDiskCapBytes {
		return fmt.Errorf("diskCapBytes exceeds %d", maxCacheDiskCapBytes)
	}
	if cache.MaxRecordBytes > maxCacheBytes || cache.MaxInputBytes > maxCacheBytes ||
		cache.MaxQueryBytes > maxCacheBytes || cache.MaxScanBytes > maxCacheBytes || cache.MaxBlockBytes > maxCacheBytes {
		return fmt.Errorf("cache byte limit exceeds %d", maxCacheBytes)
	}
	if cache.MaxStringBytes > maxCacheStringBytes {
		return fmt.Errorf("maxStringBytes exceeds %d", maxCacheStringBytes)
	}
	if cache.MaxReasons > maxConfiguredCount || cache.MaxReasonParameters > maxConfiguredCount ||
		cache.MaxQueryRecords > maxConfiguredCount || cache.MaxBlockRecords > maxConfiguredCount ||
		cache.MaxBlocks > maxConfiguredCount {
		return fmt.Errorf("cache count exceeds %d", maxConfiguredCount)
	}
	return nil
}

func validateEngineLimits(engine contextapi.EngineLimits) error {
	if err := validateQueueLimits(engine.Queue); err != nil {
		return err
	}
	if engine.MaxLiveOffers > maxConfiguredCount || engine.MaxReceipts > maxConfiguredCount {
		return fmt.Errorf("offer/receipt count exceeds %d", maxConfiguredCount)
	}
	if engine.MaxRetainedBytes > maxPendingMemoryBytes {
		return fmt.Errorf("maxRetainedBytes exceeds %d", maxPendingMemoryBytes)
	}
	if engine.MaxOfferAgeMs > maxQueueAgeMs {
		return fmt.Errorf("maxOfferAgeMs exceeds %d", maxQueueAgeMs)
	}
	return nil
}

func validateProviderIDs(ids []contextapi.ProviderID) error {
	seen := make(map[contextapi.ProviderID]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(string(id)) == "" {
			return errors.New("profile provider id is empty")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate profile provider %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateProviderPolicy(policy contextapi.ProviderPolicy) error {
	switch policy.Mode {
	case "", contextapi.ProviderPolicyUnrestricted, contextapi.ProviderPolicyAllowlist, contextapi.ProviderPolicyDenyAll:
	default:
		return fmt.Errorf("unknown provider policy mode %q", policy.Mode)
	}
	return validateProviderIDs(policy.Allowed)
}

func canonicalConfigRoot(root, base string) (string, error) {
	if root == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(base, root)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return filepath.Clean(canonical), nil
}

// Resolve applies activation policy without touching the filesystem, clock,
// providers, or runtime. The input is treated as borrowed; every returned
// mutable slice is independently allocated.
func Resolve(input contextapi.ActivationInput) contextapi.ActivationResult {
	result := contextapi.ActivationResult{}
	if input.WorkingDirectory == "" || !filepath.IsAbs(input.WorkingDirectory) {
		return invalidResult(input, []contextapi.Reason{makeReason(
			contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter,
			"working directory must be an absolute canonical path", input.Now)})
	}
	if input.Config.ConfigDigest != "" && strings.HasPrefix(string(input.Config.ConfigDigest), invalidDigestPrefix) {
		return invalidResult(input, []contextapi.Reason{makeReason(
			contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
			"configuration snapshot is marked invalid", input.Now)})
	}
	if issues := validateSnapshot(input.Config); len(issues) > 0 {
		return invalidResult(input, issues)
	}
	runtime := resolveRuntimeLimits(input.Config.Home.Runtime)

	workingDirectory := filepath.Clean(input.WorkingDirectory)
	homeScopes := applicableHomeScopes(input.Config.Home.Scopes, workingDirectory)
	if homeConflict := duplicateHomeConflict(homeScopes); homeConflict != nil {
		return conflictResult(*homeConflict)
	}
	var homeScope *contextapi.HomeScopeSnapshot
	if len(homeScopes) > 0 {
		homeScope = &homeScopes[0].snapshot
	}

	for _, exclusion := range input.Config.Home.Exclusions {
		if pathMatches(exclusion.Root, exclusion.IncludeChildren, workingDirectory) {
			reason := makeReason(contextapi.ReasonExcluded, contextapi.ReasonConfigured,
				fmt.Sprintf("home exclusion %q dominates project declarations", exclusion.Root), input.Now)
			return inactiveResult(homeScopeIdentity(homeScope), []contextapi.Reason{reason})
		}
	}
	if homeScope != nil && !homeScope.Config.OptIn {
		reason := makeReason(contextapi.ReasonInactive, contextapi.ReasonConfigured,
			fmt.Sprintf("nearest home scope %q opts out", homeScope.Scope.CanonicalRoot), input.Now)
		return inactiveResult(homeScope.Scope, []contextapi.Reason{reason})
	}

	projects := applicableProjects(input.Config.Projects, workingDirectory)
	if projectConflict := duplicateProjectConflict(projects); projectConflict != nil {
		return conflictResult(*projectConflict)
	}
	var winningProject *projectCandidate
	if len(projects) > 0 {
		winningProject = &projects[0]
		if !winningProject.snapshot.Config.OptIn {
			reason := makeReason(contextapi.ReasonInactive, contextapi.ReasonConfigured,
				fmt.Sprintf("nearest project declaration %q opts out", winningProject.snapshot.Scope.CanonicalRoot), input.Now)
			return inactiveResult(winningProject.snapshot.Scope, []contextapi.Reason{reason})
		}
	}
	if winningProject == nil && homeScope == nil {
		reason := makeReason(contextapi.ReasonInactive, contextapi.ReasonConfigured,
			"no applicable project or home opt-in declaration", input.Now)
		return inactiveResult(contextapi.ScopeIdentity{}, []contextapi.Reason{reason})
	}

	providers, providersExplicit := inheritedProviders(projects, homeScope)
	profileProviders := inheritedProfileProviders(projects, homeScope, input.Config.Home.Defaults.Providers)
	profile := inheritedProfile(projects, homeScope, input.Config.Home.Defaults.Selection)
	if !providersExplicit {
		builtin := defaultBuiltinProvider()
		builtin.Limits = runtime.ProviderDefaults
		providers = []contextapi.ProviderConfig{builtin}
	} else {
		normalizeProviderLimitsWith(providers, runtime.ProviderDefaults)
	}
	if providersExplicit && len(providers) == 0 {
		reason := makeReason(contextapi.ReasonProviderNotSelected, contextapi.ReasonConfigured,
			"an explicit empty provider list disables the builtin ai-context default", input.Now)
		return inactiveResult(winningScope(winningProject, homeScope), []contextapi.Reason{reason})
	}

	filtered, blocked := applyProviderPolicy(providers, input.Config.Home.ProviderPolicy)
	reasons := make([]contextapi.Reason, 0, 2)
	if blocked {
		reasons = append(reasons, makeReason(contextapi.ReasonConflict, contextapi.ReasonConfigured,
			"home provider policy excludes one or more selected providers", input.Now))
	}
	if len(filtered) == 0 {
		if input.Config.Home.ProviderPolicy.Mode == contextapi.ProviderPolicyDenyAll {
			reasons = append(reasons, makeReason(contextapi.ReasonConflict, contextapi.ReasonConfigured,
				"home provider policy is explicit deny-all", input.Now))
		} else {
			reasons = append(reasons, makeReason(contextapi.ReasonProviderNotSelected, contextapi.ReasonConfigured,
				"no selected provider is permitted by the home policy", input.Now))
		}
		return conflictResultWithScope(winningScope(winningProject, homeScope), reasons)
	}
	profileProviders, profileBlocked := applyProviderIDPolicy(profileProviders, input.Config.Home.ProviderPolicy)
	if profileBlocked {
		reasons = append(reasons, makeReason(contextapi.ReasonConflict, contextapi.ReasonConfigured,
			"home provider policy excludes one or more profile providers", input.Now))
	}

	scope := winningScope(winningProject, homeScope)
	result.State = contextapi.ActivationEnabled
	result.Scope = scope
	result.Effective = contextapi.EffectiveConfiguration{
		Scope:            scope,
		ConfigDigest:     input.Config.ConfigDigest,
		Providers:        cloneProviderConfigs(filtered),
		ProfileProviders: cloneProviderIDs(profileProviders),
		Profile:          cloneProfileSelection(profile),
		Delivery:         cloneEngineLimits(resolveEffectiveDelivery(input.Config.Home.Delivery, runtime)),
		Runtime:          runtime,
		Cache:            resolveCacheLimits(input.Config.Home.Cache),
	}
	result.Reasons = reasons
	return result
}

func validateSnapshot(snapshot contextapi.ActivationSnapshot) []contextapi.Reason {
	reasons := make([]contextapi.Reason, 0)
	if snapshot.Home.ConfigDigest != "" && !strings.HasPrefix(string(snapshot.Home.ConfigDigest), sha256Prefix) {
		reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
			"home configuration digest is not canonical", time.Time{}))
	}
	for _, project := range snapshot.Projects {
		if project.Config.SchemaVersion != 1 {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
				fmt.Sprintf("project declaration %q has invalid schema", project.SourcePath), time.Time{}))
			break
		}
		if project.Scope.CanonicalRoot == "" || !filepath.IsAbs(project.Scope.CanonicalRoot) {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
				fmt.Sprintf("project declaration %q has no canonical root", project.SourcePath), time.Time{}))
			break
		}
		if err := validateProviderLists(project.Config.Providers, project.Config.ProfileProviders, DefaultLoadLimits()); err != nil {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
				fmt.Sprintf("project declaration %q has invalid providers: %v", project.SourcePath, err), time.Time{}))
			break
		}
	}
	if err := validateProviderPolicy(snapshot.Home.ProviderPolicy); err != nil {
		reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
			fmt.Sprintf("home provider policy is invalid: %v", err), time.Time{}))
	}
	if err := validateRuntimeLimits(resolveRuntimeLimits(snapshot.Home.Runtime)); err != nil {
		reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
			fmt.Sprintf("home runtime is invalid: %v", err), time.Time{}))
	}
	if err := validateCacheLimits(resolveCacheLimits(snapshot.Home.Cache)); err != nil {
		reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
			fmt.Sprintf("home cache is invalid: %v", err), time.Time{}))
	}
	for _, scope := range snapshot.Home.Scopes {
		if scope.Scope.CanonicalRoot == "" || !filepath.IsAbs(scope.Scope.CanonicalRoot) {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
				"home scope has no canonical root", time.Time{}))
			break
		}
		if err := validateProviderLists(scope.Config.Providers, scope.Config.ProfileProviders, DefaultLoadLimits()); err != nil {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured,
				fmt.Sprintf("home scope %q has invalid providers: %v", scope.Scope.CanonicalRoot, err), time.Time{}))
			break
		}
	}
	return reasons
}

func applicableHomeScopes(scopes []contextapi.HomeScopeSnapshot, directory string) []homeCandidate {
	applicable := make([]homeCandidate, 0)
	for _, scope := range scopes {
		if pathMatches(scope.Scope.CanonicalRoot, scope.Config.IncludeChildren, directory) {
			applicable = append(applicable, homeCandidate{snapshot: cloneHomeScopeSnapshot(scope), depth: pathDepth(scope.Scope.CanonicalRoot)})
		}
	}
	sort.SliceStable(applicable, func(left, right int) bool {
		if applicable[left].depth != applicable[right].depth {
			return applicable[left].depth > applicable[right].depth
		}
		return applicable[left].snapshot.Scope.ID < applicable[right].snapshot.Scope.ID
	})
	return applicable
}

func applicableProjects(projects []contextapi.ProjectSnapshot, directory string) []projectCandidate {
	applicable := make([]projectCandidate, 0)
	for _, project := range projects {
		if pathMatches(project.Scope.CanonicalRoot, project.Config.IncludeChildren, directory) {
			applicable = append(applicable, projectCandidate{snapshot: cloneProjectSnapshot(project), depth: pathDepth(project.Scope.CanonicalRoot)})
		}
	}
	sort.SliceStable(applicable, func(left, right int) bool {
		if applicable[left].depth != applicable[right].depth {
			return applicable[left].depth > applicable[right].depth
		}
		return applicable[left].snapshot.SourcePath < applicable[right].snapshot.SourcePath
	})
	return applicable
}

func duplicateHomeConflict(scopes []homeCandidate) *contextapi.Reason {
	if len(scopes) < 2 || scopes[0].depth != scopes[1].depth || scopes[0].snapshot.Scope.CanonicalRoot != scopes[1].snapshot.Scope.CanonicalRoot {
		return nil
	}
	reason := makeReason(contextapi.ReasonConflict, contextapi.ReasonConfigured,
		fmt.Sprintf("home declarations conflict at %q", scopes[0].snapshot.Scope.CanonicalRoot), time.Time{})
	return &reason
}

func duplicateProjectConflict(projects []projectCandidate) *contextapi.Reason {
	if len(projects) < 2 || projects[0].depth != projects[1].depth || projects[0].snapshot.Scope.CanonicalRoot != projects[1].snapshot.Scope.CanonicalRoot {
		return nil
	}
	reason := makeReason(contextapi.ReasonConflict, contextapi.ReasonConfigured,
		fmt.Sprintf("project declarations conflict at %q", projects[0].snapshot.Scope.CanonicalRoot), time.Time{})
	return &reason
}

func inheritedProviders(projects []projectCandidate, home *contextapi.HomeScopeSnapshot) ([]contextapi.ProviderConfig, bool) {
	var providers []contextapi.ProviderConfig
	set := false
	if home != nil && home.Config.Providers != nil {
		providers = cloneProviderConfigs(home.Config.Providers)
		set = true
	}
	for index := len(projects) - 1; index >= 0; index-- {
		if projects[index].snapshot.Config.Providers != nil {
			providers = cloneProviderConfigs(projects[index].snapshot.Config.Providers)
			set = true
		}
	}
	return providers, set
}

func inheritedProfileProviders(projects []projectCandidate, home *contextapi.HomeScopeSnapshot, defaults []contextapi.ProviderID) []contextapi.ProviderID {
	providers := cloneProviderIDs(defaults)
	if home != nil && home.Config.ProfileProviders != nil {
		providers = cloneProviderIDs(home.Config.ProfileProviders)
	}
	for index := len(projects) - 1; index >= 0; index-- {
		if projects[index].snapshot.Config.ProfileProviders != nil {
			providers = cloneProviderIDs(projects[index].snapshot.Config.ProfileProviders)
		}
	}
	return uniqueProviderIDs(providers)
}

func inheritedProfile(projects []projectCandidate, home *contextapi.HomeScopeSnapshot, defaults contextapi.ProfileSelection) contextapi.ProfileSelection {
	profile := cloneProfileSelection(defaults)
	if home != nil {
		profile = mergeProfile(profile, home.Config.Profile)
	}
	for index := len(projects) - 1; index >= 0; index-- {
		profile = mergeProfile(profile, projects[index].snapshot.Config.Profile)
	}
	return profile
}

func mergeProfile(base, overlay contextapi.ProfileSelection) contextapi.ProfileSelection {
	if overlay.Role != "" {
		base.Role = overlay.Role
	}
	if overlay.GuidanceSet != "" {
		base.GuidanceSet = overlay.GuidanceSet
	}
	if overlay.Preferences != nil {
		merged := make([]contextapi.NamedValue, 0, len(base.Preferences)+len(overlay.Preferences))
		seen := make(map[string]struct{}, len(base.Preferences)+len(overlay.Preferences))
		for _, value := range overlay.Preferences {
			if _, exists := seen[value.Name]; !exists {
				merged = append(merged, cloneNamedValue(value))
				seen[value.Name] = struct{}{}
			}
		}
		for _, value := range base.Preferences {
			if _, exists := seen[value.Name]; !exists {
				merged = append(merged, cloneNamedValue(value))
				seen[value.Name] = struct{}{}
			}
		}
		base.Preferences = merged
	}
	return base
}

func applyProviderPolicy(providers []contextapi.ProviderConfig, policy contextapi.ProviderPolicy) ([]contextapi.ProviderConfig, bool) {
	if policy.Mode == "" || policy.Mode == contextapi.ProviderPolicyUnrestricted {
		return uniqueProviderConfigs(providers), false
	}
	if policy.Mode == contextapi.ProviderPolicyDenyAll {
		return nil, len(providers) > 0
	}
	allowed := make(map[contextapi.ProviderID]struct{}, len(policy.Allowed))
	for _, id := range policy.Allowed {
		allowed[id] = struct{}{}
	}
	filtered := make([]contextapi.ProviderConfig, 0, len(providers))
	blocked := false
	for _, provider := range providers {
		if _, ok := allowed[provider.ID]; !ok {
			blocked = true
			continue
		}
		filtered = append(filtered, cloneProviderConfig(provider))
	}
	return uniqueProviderConfigs(filtered), blocked
}

func applyProviderIDPolicy(ids []contextapi.ProviderID, policy contextapi.ProviderPolicy) ([]contextapi.ProviderID, bool) {
	if policy.Mode == "" || policy.Mode == contextapi.ProviderPolicyUnrestricted {
		return uniqueProviderIDs(ids), false
	}
	if policy.Mode == contextapi.ProviderPolicyDenyAll {
		return nil, len(ids) > 0
	}
	allowed := make(map[contextapi.ProviderID]struct{}, len(policy.Allowed))
	for _, id := range policy.Allowed {
		allowed[id] = struct{}{}
	}
	filtered := make([]contextapi.ProviderID, 0, len(ids))
	blocked := false
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			blocked = true
			continue
		}
		filtered = append(filtered, id)
	}
	return uniqueProviderIDs(filtered), blocked
}

func pathMatches(root string, includeChildren bool, directory string) bool {
	root = filepath.Clean(root)
	directory = filepath.Clean(directory)
	if root == directory {
		return true
	}
	if !includeChildren || root == "." {
		return false
	}
	relative, err := filepath.Rel(root, directory)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepathSeparator)) && !filepath.IsAbs(relative)
}

// filepath separator is expressed through a helper so the path boundary is
// explicit without relying on string-prefix matches.
const filepathSeparator = filepath.Separator

func pathDepth(path string) int {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	depth := 0
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part != "" && part != "." {
			depth++
		}
	}
	return depth
}

func winningScope(project *projectCandidate, home *contextapi.HomeScopeSnapshot) contextapi.ScopeIdentity {
	if project != nil {
		return cloneScopeIdentity(project.snapshot.Scope)
	}
	if home != nil {
		return cloneScopeIdentity(home.Scope)
	}
	return contextapi.ScopeIdentity{}
}

func homeScopeIdentity(home *contextapi.HomeScopeSnapshot) contextapi.ScopeIdentity {
	if home == nil {
		return contextapi.ScopeIdentity{}
	}
	return cloneScopeIdentity(home.Scope)
}

func defaultBuiltinProvider() contextapi.ProviderConfig {
	return contextapi.ProviderConfig{
		ID:           contextapi.ProviderID("ai-context"),
		Kind:         contextapi.ProviderKindBuiltin,
		Capabilities: []contextapi.ProviderCapability{contextapi.ProviderCapabilityContribute},
		Limits:       defaultProviderLimits(),
	}
}

func defaultQueueLimits() contextapi.QueueLimits {
	return contextapi.QueueLimits{
		MaxItemBytes:    256 * 1024,
		MaxPendingBytes: 1024 * 1024,
		MaxPendingItems: 64,
		MaxAgeMs:        24 * 60 * 60 * 1000,
	}
}

func defaultRuntimeLimits() contextapi.RuntimeLimits {
	return contextapi.RuntimeLimits{
		HookDeadlineMs:           2 * 1000,
		WholeHookDeadlineMs:      2 * 1000,
		MaxActivationAncestors:   64,
		MaxActivationConfigBytes: 256 * 1024,
		MaxActivationConfigFiles: 32,
		MaxProviders:             64,
		MaxProfileFacts:          32,
		MaxContributions:         64,
		MaxContributionBodyBytes: 256 * 1024,
		MaxActiveScopes:          64,
		MaxActiveAudiences:       256,
		MaxProviderProcesses:     4,
		MaxPendingMemoryBytes:    4 * 1024 * 1024,
		MaxPendingItems:          256,
		MaxOutstandingOffers:     128,
		MaxReceipts:              1024,
		ProviderDefaults:         defaultProviderLimits(),
		IdleTTLMs:                5 * 60 * 1000,
	}
}

func resolveRuntimeLimits(input contextapi.RuntimeLimits) contextapi.RuntimeLimits {
	defaults := defaultRuntimeLimits()
	if input.WholeHookDeadlineMs == 0 {
		input.WholeHookDeadlineMs = defaults.WholeHookDeadlineMs
	}
	if input.HookDeadlineMs == 0 {
		input.HookDeadlineMs = defaults.HookDeadlineMs
		if input.HookDeadlineMs > input.WholeHookDeadlineMs {
			input.HookDeadlineMs = input.WholeHookDeadlineMs
		}
	}
	if input.MaxActivationAncestors == 0 {
		input.MaxActivationAncestors = defaults.MaxActivationAncestors
	}
	if input.MaxActivationConfigBytes == 0 {
		input.MaxActivationConfigBytes = defaults.MaxActivationConfigBytes
	}
	if input.MaxActivationConfigFiles == 0 {
		input.MaxActivationConfigFiles = defaults.MaxActivationConfigFiles
	}
	if input.MaxProviders == 0 {
		input.MaxProviders = defaults.MaxProviders
	}
	if input.MaxProfileFacts == 0 {
		input.MaxProfileFacts = defaults.MaxProfileFacts
	}
	if input.MaxContributions == 0 {
		input.MaxContributions = defaults.MaxContributions
	}
	if input.MaxContributionBodyBytes == 0 {
		input.MaxContributionBodyBytes = defaults.MaxContributionBodyBytes
	}
	if input.MaxActiveScopes == 0 {
		input.MaxActiveScopes = defaults.MaxActiveScopes
	}
	if input.MaxActiveAudiences == 0 {
		input.MaxActiveAudiences = defaults.MaxActiveAudiences
	}
	if input.MaxProviderProcesses == 0 {
		input.MaxProviderProcesses = defaults.MaxProviderProcesses
	}
	if input.MaxPendingMemoryBytes == 0 {
		input.MaxPendingMemoryBytes = defaults.MaxPendingMemoryBytes
	}
	if input.MaxPendingItems == 0 {
		input.MaxPendingItems = defaults.MaxPendingItems
	}
	if input.MaxOutstandingOffers == 0 {
		input.MaxOutstandingOffers = defaults.MaxOutstandingOffers
	}
	if input.MaxReceipts == 0 {
		input.MaxReceipts = defaults.MaxReceipts
	}
	if input.IdleTTLMs == 0 {
		input.IdleTTLMs = defaults.IdleTTLMs
	}
	input.ProviderDefaults = providerLimitsWithDefaults(input.ProviderDefaults)
	return input
}

func resolveQueueLimits(input, defaults contextapi.QueueLimits) contextapi.QueueLimits {
	if input.MaxItemBytes == 0 {
		input.MaxItemBytes = defaults.MaxItemBytes
	}
	if input.MaxPendingBytes == 0 {
		input.MaxPendingBytes = defaults.MaxPendingBytes
	}
	if input.MaxPendingItems == 0 {
		input.MaxPendingItems = defaults.MaxPendingItems
	}
	if input.MaxAgeMs == 0 {
		input.MaxAgeMs = defaults.MaxAgeMs
	}
	return input
}

func defaultDeliveryLimits() contextapi.EngineLimits {
	return contextapi.EngineLimits{
		Queue:            defaultQueueLimits(),
		MaxLiveOffers:    128,
		MaxReceipts:      1024,
		MaxOfferAgeMs:    24 * 60 * 60 * 1000,
		MaxRetainedBytes: defaultMaxRetainedBytes,
	}
}

func resolveDeliveryLimits(input contextapi.EngineLimits, runtime contextapi.RuntimeLimits) contextapi.EngineLimits {
	defaults := defaultDeliveryLimits()
	resolvedRuntime := resolveRuntimeLimits(runtime)
	input.Queue = resolveQueueLimits(input.Queue, defaults.Queue)
	if input.MaxLiveOffers == 0 {
		input.MaxLiveOffers = resolvedRuntime.MaxOutstandingOffers
	}
	if input.MaxReceipts == 0 {
		input.MaxReceipts = resolvedRuntime.MaxReceipts
	}
	if input.MaxOfferAgeMs == 0 {
		input.MaxOfferAgeMs = defaults.MaxOfferAgeMs
	}
	if input.MaxRetainedBytes == 0 {
		input.MaxRetainedBytes = defaults.MaxRetainedBytes
	}
	if input.MaxRetainedBytes > resolvedRuntime.MaxPendingMemoryBytes {
		input.MaxRetainedBytes = resolvedRuntime.MaxPendingMemoryBytes
	}
	return input
}

func resolveEffectiveDelivery(homeDelivery contextapi.EngineLimits, runtime contextapi.RuntimeLimits) contextapi.EngineLimits {
	delivery := resolveDeliveryLimits(homeDelivery, runtime)
	homeDefaults := delivery
	resolvedRuntime := resolveRuntimeLimits(runtime)
	if delivery.Queue.MaxItemBytes > homeDefaults.Queue.MaxItemBytes {
		delivery.Queue.MaxItemBytes = homeDefaults.Queue.MaxItemBytes
	}
	if delivery.Queue.MaxPendingBytes > homeDefaults.Queue.MaxPendingBytes {
		delivery.Queue.MaxPendingBytes = homeDefaults.Queue.MaxPendingBytes
	}
	if delivery.Queue.MaxPendingItems > homeDefaults.Queue.MaxPendingItems {
		delivery.Queue.MaxPendingItems = homeDefaults.Queue.MaxPendingItems
	}
	if delivery.Queue.MaxAgeMs > homeDefaults.Queue.MaxAgeMs {
		delivery.Queue.MaxAgeMs = homeDefaults.Queue.MaxAgeMs
	}
	if delivery.MaxLiveOffers > resolvedRuntime.MaxOutstandingOffers {
		delivery.MaxLiveOffers = resolvedRuntime.MaxOutstandingOffers
	}
	if delivery.MaxReceipts > resolvedRuntime.MaxReceipts {
		delivery.MaxReceipts = resolvedRuntime.MaxReceipts
	}
	if delivery.MaxLiveOffers > homeDefaults.MaxLiveOffers {
		delivery.MaxLiveOffers = homeDefaults.MaxLiveOffers
	}
	if delivery.MaxReceipts > homeDefaults.MaxReceipts {
		delivery.MaxReceipts = homeDefaults.MaxReceipts
	}
	if delivery.MaxOfferAgeMs > homeDefaults.MaxOfferAgeMs {
		delivery.MaxOfferAgeMs = homeDefaults.MaxOfferAgeMs
	}
	return delivery
}

func defaultCacheLimits() contextapi.CacheLimits {
	return contextapi.CacheLimits{
		DiskCapBytes:        5_000_000_000,
		MaxRecordBytes:      256 * 1024,
		MaxInputBytes:       256 * 1024,
		MaxStringBytes:      64 * 1024,
		MaxReasons:          64,
		MaxReasonParameters: 32,
		MaxQueryRecords:     256,
		MaxQueryBytes:       1024 * 1024,
		MaxScanBytes:        1024 * 1024,
		MaxBlockBytes:       1024 * 1024,
		MaxBlockRecords:     1024,
		MaxBlocks:           1024,
	}
}

func resolveCacheLimits(input contextapi.CacheLimits) contextapi.CacheLimits {
	defaults := defaultCacheLimits()
	if input.DiskCapBytes == 0 {
		input.DiskCapBytes = defaults.DiskCapBytes
	}
	if input.MaxRecordBytes == 0 {
		input.MaxRecordBytes = defaults.MaxRecordBytes
	}
	if input.MaxInputBytes == 0 {
		input.MaxInputBytes = defaults.MaxInputBytes
	}
	if input.MaxStringBytes == 0 {
		input.MaxStringBytes = defaults.MaxStringBytes
	}
	if input.MaxReasons == 0 {
		input.MaxReasons = defaults.MaxReasons
	}
	if input.MaxReasonParameters == 0 {
		input.MaxReasonParameters = defaults.MaxReasonParameters
	}
	if input.MaxQueryRecords == 0 {
		input.MaxQueryRecords = defaults.MaxQueryRecords
	}
	if input.MaxQueryBytes == 0 {
		input.MaxQueryBytes = defaults.MaxQueryBytes
	}
	if input.MaxScanBytes == 0 {
		input.MaxScanBytes = defaults.MaxScanBytes
	}
	if input.MaxBlockBytes == 0 {
		input.MaxBlockBytes = defaults.MaxBlockBytes
	}
	if input.MaxBlockRecords == 0 {
		input.MaxBlockRecords = defaults.MaxBlockRecords
	}
	if input.MaxBlocks == 0 {
		input.MaxBlocks = defaults.MaxBlocks
	}
	return input
}

func cloneEngineLimits(input contextapi.EngineLimits) contextapi.EngineLimits {
	return input
}

func makeScopeIdentity(authority contextapi.ScopeAuthority, root string, raw []byte) contextapi.ScopeIdentity {
	return contextapi.ScopeIdentity{
		ID:            contextapi.ScopeID("scope:" + digestString(string(authority)+"\x00"+filepath.Clean(root))),
		Authority:     authority,
		CanonicalRoot: filepath.Clean(root),
		ConfigDigest:  digestBytes(raw),
	}
}

func activationDigest(home contextapi.HomeSnapshot, projects []contextapi.ProjectSnapshot, issues []loadIssue) contextapi.ConfigDigest {
	if len(issues) > 0 {
		return contextapi.ConfigDigest(invalidDigestPrefix + "load")
	}
	parts := []string{"home:" + string(home.ConfigDigest)}
	for _, project := range projects {
		parts = append(parts, "project:"+project.SourcePath+":"+string(project.Scope.ConfigDigest))
	}
	sort.Strings(parts)
	return contextapi.ConfigDigest(sha256Prefix + digestString(strings.Join(parts, "\x00")))
}

func digestBytes(raw []byte) contextapi.ConfigDigest {
	hash := sha256.Sum256(raw)
	return contextapi.ConfigDigest(sha256Prefix + hex.EncodeToString(hash[:]))
}

func digestString(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func makeReason(code contextapi.ReasonCode, origin contextapi.ReasonOrigin, summary string, at time.Time) contextapi.Reason {
	return contextapi.Reason{Code: code, Origin: origin, Summary: summary, At: at}
}

func reasonsFromIssues(issues []loadIssue) []contextapi.Reason {
	reasons := make([]contextapi.Reason, 0, len(issues))
	for _, issue := range issues {
		reasons = append(reasons, issue.reason)
	}
	return reasons
}

func invalidResult(input contextapi.ActivationInput, reasons []contextapi.Reason) contextapi.ActivationResult {
	return contextapi.ActivationResult{State: contextapi.ActivationInvalid, Reasons: cloneReasons(reasons)}
}

func inactiveResult(scope contextapi.ScopeIdentity, reasons []contextapi.Reason) contextapi.ActivationResult {
	return contextapi.ActivationResult{State: contextapi.ActivationInactive, Scope: cloneScopeIdentity(scope), Reasons: cloneReasons(reasons)}
}

func conflictResult(reason contextapi.Reason) contextapi.ActivationResult {
	return conflictResultWithScope(contextapi.ScopeIdentity{}, []contextapi.Reason{reason})
}

func conflictResultWithScope(scope contextapi.ScopeIdentity, reasons []contextapi.Reason) contextapi.ActivationResult {
	return contextapi.ActivationResult{State: contextapi.ActivationConflict, Scope: cloneScopeIdentity(scope), Reasons: cloneReasons(reasons)}
}

func cloneProviderConfig(input contextapi.ProviderConfig) contextapi.ProviderConfig {
	output := input
	output.Arguments = append([]string(nil), input.Arguments...)
	output.Capabilities = append([]contextapi.ProviderCapability(nil), input.Capabilities...)
	output.Settings = append([]byte(nil), input.Settings...)
	return output
}

func cloneProviderConfigs(input []contextapi.ProviderConfig) []contextapi.ProviderConfig {
	if input == nil {
		return nil
	}
	output := make([]contextapi.ProviderConfig, len(input))
	for index, provider := range input {
		output[index] = cloneProviderConfig(provider)
	}
	return output
}

func uniqueProviderConfigs(input []contextapi.ProviderConfig) []contextapi.ProviderConfig {
	output := make([]contextapi.ProviderConfig, 0, len(input))
	seen := make(map[contextapi.ProviderID]struct{}, len(input))
	for _, provider := range input {
		if _, exists := seen[provider.ID]; exists {
			continue
		}
		seen[provider.ID] = struct{}{}
		output = append(output, cloneProviderConfig(provider))
	}
	return output
}

func cloneProviderIDs(input []contextapi.ProviderID) []contextapi.ProviderID {
	if input == nil {
		return nil
	}
	return append([]contextapi.ProviderID(nil), input...)
}

func uniqueProviderIDs(input []contextapi.ProviderID) []contextapi.ProviderID {
	output := make([]contextapi.ProviderID, 0, len(input))
	seen := make(map[contextapi.ProviderID]struct{}, len(input))
	for _, id := range input {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		output = append(output, id)
	}
	return output
}

func cloneProviderPolicy(input contextapi.ProviderPolicy) contextapi.ProviderPolicy {
	input.Allowed = cloneProviderIDs(input.Allowed)
	return input
}

func cloneProfileDefaults(input contextapi.ProfileDefaults) contextapi.ProfileDefaults {
	return contextapi.ProfileDefaults{Selection: cloneProfileSelection(input.Selection), Providers: cloneProviderIDs(input.Providers)}
}

func cloneProfileSelection(input contextapi.ProfileSelection) contextapi.ProfileSelection {
	output := input
	if input.Preferences != nil {
		output.Preferences = make([]contextapi.NamedValue, len(input.Preferences))
		for index, value := range input.Preferences {
			output.Preferences[index] = cloneNamedValue(value)
		}
	}
	return output
}

func cloneNamedValue(input contextapi.NamedValue) contextapi.NamedValue {
	return input
}

func cloneScopeIdentity(input contextapi.ScopeIdentity) contextapi.ScopeIdentity {
	return input
}

func cloneProviderSnapshotScope(input contextapi.ScopeIdentity) contextapi.ScopeIdentity {
	return cloneScopeIdentity(input)
}

func cloneProjectSnapshot(input contextapi.ProjectSnapshot) contextapi.ProjectSnapshot {
	return contextapi.ProjectSnapshot{
		SourcePath: input.SourcePath,
		Scope:      cloneProviderSnapshotScope(input.Scope),
		Config: contextapi.ProjectFile{
			SchemaVersion:    input.Config.SchemaVersion,
			OptIn:            input.Config.OptIn,
			IncludeChildren:  input.Config.IncludeChildren,
			Providers:        cloneProviderConfigs(input.Config.Providers),
			ProfileProviders: cloneProviderIDs(input.Config.ProfileProviders),
			Profile:          cloneProfileSelection(input.Config.Profile),
		},
	}
}

func cloneHomeScopeSnapshot(input contextapi.HomeScopeSnapshot) contextapi.HomeScopeSnapshot {
	return contextapi.HomeScopeSnapshot{
		Scope: cloneScopeIdentity(input.Scope),
		Config: contextapi.HomeScopeFile{
			Root:             input.Config.Root,
			OptIn:            input.Config.OptIn,
			IncludeChildren:  input.Config.IncludeChildren,
			Providers:        cloneProviderConfigs(input.Config.Providers),
			ProfileProviders: cloneProviderIDs(input.Config.ProfileProviders),
			Profile:          cloneProfileSelection(input.Config.Profile),
		},
	}
}

func cloneReasons(input []contextapi.Reason) []contextapi.Reason {
	if input == nil {
		return nil
	}
	output := make([]contextapi.Reason, len(input))
	for index, reason := range input {
		output[index] = reason
		output[index].Params = append([]contextapi.ReasonParameter(nil), reason.Params...)
		output[index].Evidence = append([]contextapi.EvidenceID(nil), reason.Evidence...)
	}
	return output
}
