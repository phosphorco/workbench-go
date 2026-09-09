// Package contextapi contains the value contracts shared by Workbench context
// packages. It has no filesystem, process, clock, queue, or persistence
// behavior.
package contextapi

import (
	"encoding/json"
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

// PathRule is an explicit directory exclusion. Root is canonicalized by the
// activation owner before matching; IncludeChildren controls subtree coverage.
type PathRule struct {
	Root            string `json:"root"`
	IncludeChildren bool   `json:"includeChildren"`
}

// ProjectFile is the authored .workbench/context.json schema. Its directory,
// scope ID, canonical root, and digest come from the loader's actual file
// origin and bytes; they are deliberately not user fields.
type ProjectFile struct {
	SchemaVersion   int  `json:"schemaVersion"`
	OptIn           bool `json:"optIn"`
	IncludeChildren bool `json:"includeChildren"`
	// nil means the providers field was absent; a non-nil empty slice is an
	// explicit disable-all selection. The loader uses that distinction.
	Providers        []ProviderConfig `json:"providers"`
	ProfileProviders []ProviderID     `json:"profileProviders,omitempty"`
	Profile          ProfileSelection `json:"profile,omitempty"`
}

// HomeScopeFile is an authored scope in the XDG/user-home configuration. Home
// configuration is allowed to name its explicit directory scopes.
type HomeScopeFile struct {
	Root             string           `json:"root"`
	OptIn            bool             `json:"optIn"`
	IncludeChildren  bool             `json:"includeChildren"`
	Providers        []ProviderConfig `json:"providers,omitempty"`
	ProfileProviders []ProviderID     `json:"profileProviders,omitempty"`
	Profile          ProfileSelection `json:"profile,omitempty"`
}

type HomeFile struct {
	SchemaVersion  int             `json:"schemaVersion"`
	Scopes         []HomeScopeFile `json:"scopes"`
	Exclusions     []PathRule      `json:"exclusions,omitempty"`
	ProviderPolicy ProviderPolicy  `json:"providerPolicy"`
	Defaults       ProfileDefaults `json:"defaults"`
	Delivery       EngineLimits    `json:"delivery,omitempty"`
	Runtime        RuntimeLimits   `json:"runtime"`
	Cache          CacheLimits     `json:"cache"`
}

// RuntimeLimits are machine-wide bounds authored only in HomeFile. Zero
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
	Providers []ProviderID     `json:"providers,omitempty"`
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
	SourcePath string        `json:"sourcePath"`
	Scope      ScopeIdentity `json:"scope"`
	Config     ProjectFile   `json:"config"`
}

type HomeScopeSnapshot struct {
	Scope  ScopeIdentity `json:"scope"`
	Config HomeScopeFile `json:"config"`
}

type HomeSnapshot struct {
	ConfigDigest   ConfigDigest        `json:"configDigest"`
	Scopes         []HomeScopeSnapshot `json:"scopes"`
	Exclusions     []PathRule          `json:"exclusions,omitempty"`
	ProviderPolicy ProviderPolicy      `json:"providerPolicy"`
	Defaults       ProfileDefaults     `json:"defaults"`
	Delivery       EngineLimits        `json:"delivery"`
	Runtime        RuntimeLimits       `json:"runtime"`
	Cache          CacheLimits         `json:"cache"`
}

// ActivationSnapshot is produced by loading actual home/project files. Its
// ConfigDigest covers the combined effective activation inputs. A project file
// contributes only ProjectFile; it cannot author HomeFile fields.
type ActivationSnapshot struct {
	SchemaVersion int               `json:"schemaVersion"`
	ConfigDigest  ConfigDigest      `json:"configDigest"`
	Home          HomeSnapshot      `json:"home"`
	Projects      []ProjectSnapshot `json:"projects"`
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
	Scope            ScopeIdentity    `json:"scope"`
	ConfigDigest     ConfigDigest     `json:"configDigest"`
	Providers        []ProviderConfig `json:"providers"`
	ProfileProviders []ProviderID     `json:"profileProviders"`
	Profile          ProfileSelection `json:"profile"`
	Delivery         EngineLimits     `json:"delivery"`
	Runtime          RuntimeLimits    `json:"runtime"`
	Cache            CacheLimits      `json:"cache"`
}

type ActivationResult struct {
	State     ActivationState        `json:"state"`
	Scope     ScopeIdentity          `json:"scope"`
	Effective EffectiveConfiguration `json:"effective"`
	Reasons   []Reason               `json:"reasons"`
}
