// Package contextapi contains the value contracts shared by Workbench context
// packages. It has no filesystem, process, clock, queue, or persistence
// behavior.
package contextapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type (
	ConfigDigest      string
	ScopeID           string
	ProviderID        string
	SourceID          string
	SourceRevisionID  string
	ContentID         string
	ProfileRevision   string
	SlotKey           string
	AudienceID        string
	TaskID            string
	TurnID            string
	InvocationID      string
	CausalID          string
	EpochID           uint64
	RuntimeGeneration uint64
	OfferID           uint64
	RequestID         uint64
	EvidenceID        uint64
)

type ScopeAuthority string

const (
	ScopeAuthorityHome    ScopeAuthority = "home"
	ScopeAuthorityProject ScopeAuthority = "project"
)

type DeclarationScope string

const (
	DeclarationScopeDirectory DeclarationScope = "directory"
	DeclarationScopeSubtree   DeclarationScope = "subtree"
)

// PathRule is an explicit directory exclusion. Root is canonicalized by the
// activation owner before matching; IncludeChildren controls subtree coverage.
type PathRule struct {
	Root            string `json:"root"`
	IncludeChildren bool   `json:"includeChildren"`
}

type ContributorName string

type ContributorKind string

const (
	ContributorKindAiContext  ContributorKind = "aiContext"
	ContributorKindExecutable ContributorKind = "executable"
)

// AiContext is the typed builtin contributor value. Its capability is fixed:
// the builtin contributes guidance and cannot provide profile facts.
type AiContext struct{}

// Executable is an authored executable contributor. Executable paths are
// resolved by the declaration owner relative to the consuming declaration
// root; Arguments are passed directly and Settings remain provider-owned JSON.
type Executable struct {
	Executable   string               `json:"executable"`
	Arguments    []string             `json:"arguments"`
	Capabilities []ProviderCapability `json:"capabilities"`
	Settings     json.RawMessage      `json:"settings"`
	Limits       ProviderLimits       `json:"limits"`
}

// Contributor is a closed value contract for one named declaration entry.
// Kind selects exactly one of AiContext or Executable; the inactive value is
// ignored and carries no presence semantics. The evaluator lane validates this
// invariant while decoding its typed Pkl result.
type Contributor struct {
	Enabled    bool            `json:"enabled"`
	Kind       ContributorKind `json:"kind"`
	AiContext  AiContext       `json:"aiContext"`
	Executable Executable      `json:"executable"`
}

// evaluatedContributor is the flat JSON projection emitted by the Pkl
// contributor classes. It is deliberately separate from Contributor because
// Pkl's executable path is a scalar while the canonical Go value owns a typed
// Executable sub-value. Settings remain one provider-owned JSON object.
type evaluatedContributor struct {
	Enabled      bool                 `json:"enabled"`
	Kind         ContributorKind      `json:"kind"`
	Executable   string               `json:"executable"`
	Arguments    []string             `json:"arguments"`
	Capabilities []ProviderCapability `json:"capabilities"`
	Settings     json.RawMessage      `json:"settings"`
	Limits       ProviderLimits       `json:"limits"`
}

// evaluatedProfile is the flat Pkl projection. Pkl profile preference values
// are scalar strings; the canonical runtime profile records them as text
// FactValues without silently dropping the tag.
type evaluatedProfile struct {
	Role        string                       `json:"role"`
	GuidanceSet string                       `json:"guidanceSet"`
	Preferences []evaluatedProfilePreference `json:"preferences"`
}

type evaluatedProfilePreference struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type evaluatedProjectDeclaration struct {
	Enabled      bool                                     `json:"enabled"`
	Scope        DeclarationScope                         `json:"scope"`
	Contributors map[ContributorName]evaluatedContributor `json:"contributors"`
	Profile      evaluatedProfile                         `json:"profile"`
}

type evaluatedProfileDefaults struct {
	Selection evaluatedProfile `json:"selection"`
}

type evaluatedHomeLimits struct {
	ProviderPolicy ProviderPolicy           `json:"providerPolicy"`
	Defaults       evaluatedProfileDefaults `json:"defaults"`
	Delivery       EngineLimits             `json:"delivery"`
	Runtime        RuntimeLimits            `json:"runtime"`
	Cache          CacheLimits              `json:"cache"`
}

type evaluatedExclusion struct {
	Root  string           `json:"root"`
	Scope DeclarationScope `json:"scope"`
}

type evaluatedDirectorySelection struct {
	Root         string                                   `json:"root"`
	Enabled      bool                                     `json:"enabled"`
	Scope        DeclarationScope                         `json:"scope"`
	Contributors map[ContributorName]evaluatedContributor `json:"contributors"`
	Profile      evaluatedProfile                         `json:"profile"`
}

type evaluatedHomeDeclaration struct {
	Limits      evaluatedHomeLimits           `json:"limits"`
	Exclusions  []evaluatedExclusion          `json:"exclusions"`
	Directories []evaluatedDirectorySelection `json:"directories"`
}

// DecodeProjectDeclaration decodes one evaluated Pkl JSON result and projects
// every field into the canonical value contract. It is not a legacy JSON
// activation reader.
func DecodeProjectDeclaration(encoded []byte) (ProjectDeclaration, error) {
	var input evaluatedProjectDeclaration
	if err := json.Unmarshal(encoded, &input); err != nil {
		return ProjectDeclaration{}, fmt.Errorf("decode evaluated project declaration: %w", err)
	}
	contributors, err := projectContributors(input.Contributors)
	if err != nil {
		return ProjectDeclaration{}, err
	}
	profile, err := projectProfile(input.Profile)
	if err != nil {
		return ProjectDeclaration{}, err
	}
	if err := validateDeclarationScope(input.Scope); err != nil {
		return ProjectDeclaration{}, err
	}
	return ProjectDeclaration{
		Enabled:      input.Enabled,
		Scope:        DeclarationScope(strings.Clone(string(input.Scope))),
		Contributors: contributors,
		Profile:      profile,
	}, nil
}

// DecodeHomeDeclaration decodes one evaluated Pkl JSON result and projects
// home policy, selections, exclusions, and all bounded runtime/cache/delivery
// values without dropping fields.
func DecodeHomeDeclaration(encoded []byte) (HomeDeclaration, error) {
	var input evaluatedHomeDeclaration
	if err := json.Unmarshal(encoded, &input); err != nil {
		return HomeDeclaration{}, fmt.Errorf("decode evaluated home declaration: %w", err)
	}
	directories := make([]HomeDirectorySelection, len(input.Directories))
	for index, selection := range input.Directories {
		if !filepath.IsAbs(selection.Root) {
			return HomeDeclaration{}, fmt.Errorf("home directory %d root %q is not absolute", index, selection.Root)
		}
		if err := validateDeclarationScope(selection.Scope); err != nil {
			return HomeDeclaration{}, fmt.Errorf("home directory %d: %w", index, err)
		}
		contributors, err := projectContributors(selection.Contributors)
		if err != nil {
			return HomeDeclaration{}, fmt.Errorf("home directory %d: %w", index, err)
		}
		profile, err := projectProfile(selection.Profile)
		if err != nil {
			return HomeDeclaration{}, fmt.Errorf("home directory %d: %w", index, err)
		}
		directories[index] = HomeDirectorySelection{
			Root:         strings.Clone(selection.Root),
			Enabled:      selection.Enabled,
			Scope:        DeclarationScope(strings.Clone(string(selection.Scope))),
			Contributors: contributors,
			Profile:      profile,
		}
	}
	exclusions := make([]PathRule, len(input.Exclusions))
	for index, exclusion := range input.Exclusions {
		if !filepath.IsAbs(exclusion.Root) {
			return HomeDeclaration{}, fmt.Errorf("home exclusion %d root %q is not absolute", index, exclusion.Root)
		}
		if err := validateDeclarationScope(exclusion.Scope); err != nil {
			return HomeDeclaration{}, fmt.Errorf("home exclusion %d: %w", index, err)
		}
		exclusions[index] = PathRule{Root: strings.Clone(exclusion.Root), IncludeChildren: exclusion.Scope == DeclarationScopeSubtree}
	}
	defaults, err := projectProfile(input.Limits.Defaults.Selection)
	if err != nil {
		return HomeDeclaration{}, fmt.Errorf("home profile defaults: %w", err)
	}
	return HomeDeclaration{
		Limits: HomeLimits{
			ProviderPolicy: cloneProviderPolicyValue(input.Limits.ProviderPolicy),
			Defaults:       ProfileDefaults{Selection: defaults},
			Delivery:       input.Limits.Delivery,
			Runtime:        input.Limits.Runtime,
			Cache:          input.Limits.Cache,
		},
		Exclusions:  exclusions,
		Directories: directories,
	}, nil
}

func projectContributors(input map[ContributorName]evaluatedContributor) (map[ContributorName]Contributor, error) {
	result := make(map[ContributorName]Contributor, len(input))
	for name, value := range input {
		if name == "" {
			return nil, errors.New("contributor name is empty")
		}
		projected, err := projectContributorValue(value)
		if err != nil {
			return nil, fmt.Errorf("contributor %q: %w", name, err)
		}
		result[ContributorName(strings.Clone(string(name)))] = projected
	}
	return result, nil
}

func projectProfile(input evaluatedProfile) (ProfileSelection, error) {
	result := ProfileSelection{Role: strings.Clone(input.Role), GuidanceSet: strings.Clone(input.GuidanceSet), Preferences: make([]NamedValue, len(input.Preferences))}
	for index, preference := range input.Preferences {
		if preference.Name == "" {
			return ProfileSelection{}, fmt.Errorf("profile preference %d has an empty name", index)
		}
		result.Preferences[index] = NamedValue{
			Name:  strings.Clone(preference.Name),
			Value: FactValue{Kind: FactText, Text: strings.Clone(preference.Value)},
		}
	}
	return result, nil
}

func validateDeclarationScope(scope DeclarationScope) error {
	if scope != DeclarationScopeDirectory && scope != DeclarationScopeSubtree {
		return fmt.Errorf("invalid declaration scope %q", scope)
	}
	return nil
}

func cloneProviderPolicyValue(input ProviderPolicy) ProviderPolicy {
	result := ProviderPolicy{Mode: ProviderPolicyMode(strings.Clone(string(input.Mode))), Allowed: make([]ProviderID, len(input.Allowed))}
	for index, provider := range input.Allowed {
		result.Allowed[index] = ProviderID(strings.Clone(string(provider)))
	}
	return result
}

// projectContributorValue projects one evaluated flat Pkl value into the
// canonical typed contributor. It is the sole source-value projection; it is
// not a legacy activation converter and has no compatibility behavior.
func projectContributorValue(input evaluatedContributor) (Contributor, error) {
	result := Contributor{Enabled: input.Enabled, Kind: ContributorKind(strings.Clone(string(input.Kind)))}
	switch input.Kind {
	case ContributorKindAiContext:
		result.AiContext = AiContext{}
	case ContributorKindExecutable:
		if input.Executable == "" {
			return Contributor{}, fmt.Errorf("executable contributor has an empty executable path")
		}
		settings := append(json.RawMessage(nil), input.Settings...)
		if len(settings) == 0 {
			settings = json.RawMessage(`{}`)
		}
		if !json.Valid(settings) || settings[0] != '{' {
			return Contributor{}, fmt.Errorf("executable contributor settings must be a JSON object")
		}
		result.Executable = Executable{
			Executable:   strings.Clone(input.Executable),
			Arguments:    cloneStringsValue(input.Arguments),
			Capabilities: cloneCapabilitiesValue(input.Capabilities),
			Settings:     settings,
			Limits:       input.Limits,
		}
	default:
		return Contributor{}, fmt.Errorf("unknown contributor kind %q", input.Kind)
	}
	return result, nil
}

func cloneStringsValue(input []string) []string {
	result := make([]string, len(input))
	for index, value := range input {
		result[index] = strings.Clone(value)
	}
	return result
}

func cloneCapabilitiesValue(input []ProviderCapability) []ProviderCapability {
	result := make([]ProviderCapability, len(input))
	for index, value := range input {
		result[index] = ProviderCapability(strings.Clone(string(value)))
	}
	return result
}

// CapabilitiesForContributor derives profile selection from the typed value;
// there is no second authored profile-provider list.
func CapabilitiesForContributor(value Contributor) []ProviderCapability {
	if !value.Enabled {
		return []ProviderCapability{}
	}
	if value.Kind == ContributorKindAiContext {
		return []ProviderCapability{ProviderCapabilityContribute}
	}
	return cloneCapabilitiesValue(value.Executable.Capabilities)
}

// ProjectDeclaration is the canonical evaluated project Pkl value. Its
// authority, canonical root, and dependency revision come from the loader's
// actual module origin and captured inputs; they are deliberately not fields.
type ProjectDeclaration struct {
	Enabled      bool                            `json:"enabled"`
	Scope        DeclarationScope                `json:"scope"`
	Contributors map[ContributorName]Contributor `json:"contributors"`
	Profile      ProfileSelection                `json:"profile"`
}

// HomeDirectorySelection is one explicit home-owned directory declaration.
// Root is absolute in the evaluated value and is canonicalized before use.
type HomeDirectorySelection struct {
	Root         string                          `json:"root"`
	Enabled      bool                            `json:"enabled"`
	Scope        DeclarationScope                `json:"scope"`
	Contributors map[ContributorName]Contributor `json:"contributors"`
	Profile      ProfileSelection                `json:"profile"`
}

// HomeLimits is the typed home policy boundary. Runtime, delivery, and cache
// values are policy limits; the loader resolves their zero values and exposes
// the derived shapes in HomeSnapshot without making them project-authored.
type HomeLimits struct {
	ProviderPolicy ProviderPolicy  `json:"providerPolicy"`
	Defaults       ProfileDefaults `json:"defaults"`
	Delivery       EngineLimits    `json:"delivery"`
	Runtime        RuntimeLimits   `json:"runtime"`
	Cache          CacheLimits     `json:"cache"`
}

// HomeDeclaration is the canonical evaluated home Pkl value. It has no
// implicit subtree coverage: every directory selection names its root.
type HomeDeclaration struct {
	Limits      HomeLimits               `json:"limits"`
	Exclusions  []PathRule               `json:"exclusions"`
	Directories []HomeDirectorySelection `json:"directories"`
}

// RuntimeLimits are machine-wide bounds authored only in HomeDeclaration. Zero
// values are resolved by contextconfig; the shared package does not duplicate
// default constants owned by config or engine implementations.
type RuntimeLimits struct {
	HookDeadlineMs           uint64         `json:"hookDeadlineMs"`
	WholeHookDeadlineMs      uint64         `json:"wholeHookDeadlineMs"`
	MaxActivationAncestors   uint32         `json:"maxActivationAncestors"`
	MaxActivationConfigBytes uint64         `json:"maxActivationConfigBytes"`
	MaxActivationConfigFiles uint32         `json:"maxActivationConfigFiles"`
	MaxProviders             uint32         `json:"maxProviders"`
	MaxProfileFacts          uint32         `json:"maxProfileFacts"`
	MaxContributions         uint32         `json:"maxContributions"`
	MaxContributionBodyBytes uint64         `json:"maxContributionBodyBytes"`
	MaxActiveScopes          uint32         `json:"maxActiveScopes"`
	MaxActiveAudiences       uint32         `json:"maxActiveAudiences"`
	MaxProviderProcesses     uint32         `json:"maxProviderProcesses"`
	MaxPendingMemoryBytes    uint64         `json:"maxPendingMemoryBytes"`
	MaxPendingItems          uint32         `json:"maxPendingItems"`
	MaxOutstandingOffers     uint32         `json:"maxOutstandingOffers"`
	MaxReceipts              uint32         `json:"maxReceipts"`
	ProviderDefaults         ProviderLimits `json:"providerDefaults"`
	IdleTTLMs                uint64         `json:"idleTTLMs"`
}

// CacheLimits are home-only bounds for the disposable trace cache. The
// contexttrace owner converts these numeric JSON values to its Options type;
// zero values are defaulted by the configuration owner.
type CacheLimits struct {
	DiskCapBytes        uint64 `json:"diskCapBytes"`
	MaxRecordBytes      uint64 `json:"maxRecordBytes"`
	MaxInputBytes       uint64 `json:"maxInputBytes"`
	MaxStringBytes      uint64 `json:"maxStringBytes"`
	MaxReasons          uint32 `json:"maxReasons"`
	MaxReasonParameters uint32 `json:"maxReasonParameters"`
	MaxQueryRecords     uint32 `json:"maxQueryRecords"`
	MaxQueryBytes       uint64 `json:"maxQueryBytes"`
	MaxScanBytes        uint64 `json:"maxScanBytes"`
	MaxBlockBytes       uint64 `json:"maxBlockBytes"`
	MaxBlockRecords     uint32 `json:"maxBlockRecords"`
	MaxBlocks           uint32 `json:"maxBlocks"`
}

type ProviderPolicyMode string

const (
	ProviderPolicyUnrestricted ProviderPolicyMode = "unrestricted"
	ProviderPolicyAllowlist    ProviderPolicyMode = "allowlist"
	ProviderPolicyDenyAll      ProviderPolicyMode = "deny-all"
)

// Mode makes an explicit empty allowlist (deny-all) distinct from an absent
// policy (unrestricted). The loader supplies unrestricted when mode is absent.
type ProviderPolicy struct {
	Mode    ProviderPolicyMode `json:"mode,omitempty"`
	Allowed []ProviderID       `json:"allowed,omitempty"`
}

type ProfileDefaults struct {
	Selection ProfileSelection `json:"selection"`
}

type ProfileSelection struct {
	Role        string       `json:"role,omitempty"`
	GuidanceSet string       `json:"guidanceSet,omitempty"`
	Preferences []NamedValue `json:"preferences,omitempty"`
}

type NamedValue struct {
	Name  string    `json:"name"`
	Value FactValue `json:"value"`
}

type ProviderKind string

const (
	ProviderKindExecutable ProviderKind = "executable"
	ProviderKindBuiltin    ProviderKind = "builtin"
)

type ProviderCapability string

const (
	ProviderCapabilityProfile    ProviderCapability = "profile"
	ProviderCapabilityContribute ProviderCapability = "contribute"
)

// ProviderConfig describes an executable or a named builtin. Settings is an
// intentionally opaque JSON boundary owned by that provider; it is never
// interpreted by the shared runtime.
type ProviderConfig struct {
	ID           ProviderID           `json:"id"`
	Kind         ProviderKind         `json:"kind"`
	Executable   string               `json:"executable,omitempty"`
	Arguments    []string             `json:"arguments,omitempty"`
	Settings     json.RawMessage      `json:"settings,omitempty"`
	Capabilities []ProviderCapability `json:"capabilities"`
	Limits       ProviderLimits       `json:"limits"`
}

type ProviderLimits struct {
	DeadlineMs       uint64 `json:"deadlineMs"`
	MaxResponseBytes uint64 `json:"maxResponseBytes"`
	MaxFacts         uint32 `json:"maxFacts"`
	MaxContributions uint32 `json:"maxContributions"`
	MaxBodyBytes     uint64 `json:"maxBodyBytes"`
}

type QueueLimits struct {
	MaxItemBytes    uint64 `json:"maxItemBytes"`
	MaxPendingBytes uint64 `json:"maxPendingBytes"`
	MaxPendingItems uint32 `json:"maxPendingItems"`
	MaxAgeMs        uint64 `json:"maxAgeMs"`
}

// ScopeIdentity is loader-derived identity. ConfigDigest is the digest of this
// scope declaration's authored bytes, not a user-maintained revision string.
// It is distinct from ActivationSnapshot.ConfigDigest, which covers the
// combined effective activation input.
type ScopeIdentity struct {
	ID            ScopeID        `json:"id"`
	Authority     ScopeAuthority `json:"authority"`
	CanonicalRoot string         `json:"canonicalRoot"`
	ConfigDigest  ConfigDigest   `json:"configDigest"`
}

type ProjectSnapshot struct {
	SourcePath string             `json:"sourcePath"`
	Scope      ScopeIdentity      `json:"scope"`
	Config     ProjectDeclaration `json:"config"`
}

type HomeDirectorySnapshot struct {
	Scope  ScopeIdentity          `json:"scope"`
	Config HomeDirectorySelection `json:"config"`
}

type HomeSnapshot struct {
	ConfigDigest   ConfigDigest            `json:"configDigest"`
	Scopes         []HomeDirectorySnapshot `json:"scopes"`
	Exclusions     []PathRule              `json:"exclusions"`
	ProviderPolicy ProviderPolicy          `json:"providerPolicy"`
	Defaults       ProfileDefaults         `json:"defaults"`
	Delivery       EngineLimits            `json:"delivery"`
	Runtime        RuntimeLimits           `json:"runtime"`
	Cache          CacheLimits             `json:"cache"`
}

// ActivationSnapshot is produced by loading actual home/project files. Its
// ConfigDigest covers the combined effective activation inputs. A project file
// contributes only ProjectDeclaration; it cannot author HomeDeclaration fields.
type ActivationSnapshot struct {
	ConfigDigest ConfigDigest      `json:"configDigest"`
	Home         HomeSnapshot      `json:"home"`
	Projects     []ProjectSnapshot `json:"projects"`
}

type ActivationInput struct {
	WorkingDirectory string             `json:"workingDirectory"`
	Now              time.Time          `json:"now"`
	Config           ActivationSnapshot `json:"config"`
}

type ActivationState string

const (
	ActivationInactive ActivationState = "inactive"
	ActivationEnabled  ActivationState = "enabled"
	ActivationConflict ActivationState = "conflict"
	ActivationInvalid  ActivationState = "invalid"
)

// EffectiveConfiguration.ConfigDigest is the combined activation digest, not
// the declaration digest carried by EffectiveConfiguration.Scope.
type EffectiveConfiguration struct {
	Scope        ScopeIdentity    `json:"scope"`
	ConfigDigest ConfigDigest     `json:"configDigest"`
	Providers    []ProviderConfig `json:"providers"`
	Profile      ProfileSelection `json:"profile"`
	Delivery     EngineLimits     `json:"delivery"`
	Runtime      RuntimeLimits    `json:"runtime"`
	Cache        CacheLimits      `json:"cache"`
}

type ActivationResult struct {
	State     ActivationState        `json:"state"`
	Scope     ScopeIdentity          `json:"scope"`
	Effective EffectiveConfiguration `json:"effective"`
	Reasons   []Reason               `json:"reasons"`
}
