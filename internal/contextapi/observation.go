package contextapi

import "time"

type Harness string

const (
	HarnessClaudeCode Harness = "claude-code"
	HarnessCodex      Harness = "codex"
)

type Continuity string

const (
	ContinuityKnown   Continuity = "known"
	ContinuityReset   Continuity = "reset"
	ContinuityUnknown Continuity = "unknown"
)

// Audience is supplied by an adapter and normalized by the runtime. ID is the
// host session identity when available. Epoch may be zero only before runtime
// epoch assignment; providers never mint, merge, or reinterpret identity.
type Audience struct {
	ID         AudienceID `json:"id"`
	Epoch      EpochID    `json:"epoch"`
	Continuity Continuity `json:"continuity"`
}

type AudienceTransitionKind string

const (
	TransitionSessionStart AudienceTransitionKind = "session-start"
	TransitionReset        AudienceTransitionKind = "reset"
	TransitionFork         AudienceTransitionKind = "fork"
	TransitionCompact      AudienceTransitionKind = "compact"
)

// A transition is explicit host evidence. Runtime uses it to revoke old live
// offers and choose a fresh epoch; it never infers one from cwd or profile.
type AudienceTransition struct {
	Kind     AudienceTransitionKind `json:"kind"`
	Previous Audience               `json:"previous"`
	Current  Audience               `json:"current"`
	CausalID CausalID               `json:"causalId,omitempty"`
	At       time.Time              `json:"at"`
}

type TaskRef struct {
	ID   TaskID `json:"id,omitempty"`
	Kind string `json:"kind,omitempty"`
}

type TurnState string

const (
	TurnKnown   TurnState = "known"
	TurnUnknown TurnState = "unknown"
)

type TurnRef struct {
	ID    TurnID    `json:"id,omitempty"`
	State TurnState `json:"state"`
}

type InvocationRef struct {
	ID        InvocationID `json:"id"`
	HookEvent string       `json:"hookEvent,omitempty"`
}

type IdentityAvailability string

const (
	IdentityAvailable IdentityAvailability = "available"
	IdentityMissing   IdentityAvailability = "missing"
	IdentityUnknown   IdentityAvailability = "unknown"
)

type AdapterCapabilities struct {
	Harness          Harness              `json:"harness"`
	AudienceIdentity IdentityAvailability `json:"audienceIdentity"`
	EpochIdentity    IdentityAvailability `json:"epochIdentity"`
	TurnIdentity     IdentityAvailability `json:"turnIdentity"`
	Observations     []ObservationKind    `json:"observations"`
	DeliverySurfaces []DeliverySurface    `json:"deliverySurfaces"`
	BudgetUnit       string               `json:"budgetUnit"`
}

type ObservationTrigger string

const (
	TriggerToolResult      ObservationTrigger = "tool-result"
	TriggerExplicitGather  ObservationTrigger = "explicit-gather"
	TriggerSearchDetection ObservationTrigger = "search-detection"
)

type ObservationKind string

const (
	ObservationResources ObservationKind = "resources"
	ObservationSelectors ObservationKind = "selectors"
	ObservationRuntime   ObservationKind = "runtime"
)

type ResourceKind string

const (
	ResourceFile      ResourceKind = "file"
	ResourceDirectory ResourceKind = "directory"
	ResourceUnknown   ResourceKind = "unknown"
)

type ResourceOperation string

const (
	ResourceRead  ResourceOperation = "read"
	ResourceWrite ResourceOperation = "write"
)

type ResourceOutcome string

const (
	ResourceResolved       ResourceOutcome = "resolved"
	ResourceMissing        ResourceOutcome = "missing"
	ResourceFailed         ResourceOutcome = "failed"
	ResourceOutcomeUnknown ResourceOutcome = "unknown"
)

type EvidenceConfidence string

const (
	ConfidenceObserved  EvidenceConfidence = "observed"
	ConfidenceInferred  EvidenceConfidence = "inferred"
	ConfidenceCandidate EvidenceConfidence = "candidate"
)

type ObservedResource struct {
	Path       string             `json:"path"`
	Kind       ResourceKind       `json:"kind"`
	Operation  ResourceOperation  `json:"operation"`
	Outcome    ResourceOutcome    `json:"outcome"`
	Confidence EvidenceConfidence `json:"confidence"`
}

type SelectorObservedAs string

const (
	SelectorSearchPattern SelectorObservedAs = "search-pattern"
	SelectorPathPattern   SelectorObservedAs = "path-pattern"
	SelectorToolArgument  SelectorObservedAs = "tool-argument"
)

type SelectorInterpretation string

const (
	SelectorKeyword    SelectorInterpretation = "keyword"
	SelectorRegex      SelectorInterpretation = "regex"
	SelectorGlob       SelectorInterpretation = "glob"
	SelectorDiagnostic SelectorInterpretation = "diagnostic-code"
	SelectorSymbol     SelectorInterpretation = "symbol"
)

type ObservedSelector struct {
	Raw             string                   `json:"raw"`
	ObservedAs      SelectorObservedAs       `json:"observedAs"`
	Interpretations []SelectorInterpretation `json:"interpretations"`
	Confidence      EvidenceConfidence       `json:"confidence"`
}

type FactValueKind string

const (
	FactText    FactValueKind = "text"
	FactNumber  FactValueKind = "number"
	FactBoolean FactValueKind = "boolean"
)

type FactValue struct {
	Kind    FactValueKind `json:"kind"`
	Text    string        `json:"text,omitempty"`
	Number  int64         `json:"number,omitempty"`
	Boolean bool          `json:"boolean,omitempty"`
}

type FactOrigin string

const (
	FactObserved   FactOrigin = "observed"
	FactHost       FactOrigin = "host"
	FactInferred   FactOrigin = "inferred"
	FactProvider   FactOrigin = "provider"
	FactConfigured FactOrigin = "configured"
)

type Fact struct {
	Key        string     `json:"key"`
	Value      FactValue  `json:"value"`
	Origin     FactOrigin `json:"origin"`
	Provider   ProviderID `json:"provider,omitempty"`
	ObservedAt time.Time  `json:"observedAt"`
}

type HostSnapshot struct {
	Harness          Harness             `json:"harness"`
	WorkingDirectory string              `json:"workingDirectory"`
	RepositoryRoot   string              `json:"repositoryRoot,omitempty"`
	Turn             TurnRef             `json:"turn"`
	Task             TaskRef             `json:"task"`
	Capabilities     AdapterCapabilities `json:"capabilities"`
	Facts            []Fact              `json:"facts,omitempty"`
}

// Observation is an attenuated, normalized hook snapshot. CausalID is the
// host-supplied stable evidence key when one exists; ID is runtime-local and
// numeric. It contains selectors and resource paths, never a transcript.
type Observation struct {
	ID         EvidenceID         `json:"id"`
	CausalID   CausalID           `json:"causalId,omitempty"`
	At         time.Time          `json:"at"`
	Audience   Audience           `json:"audience"`
	Scope      ScopeIdentity      `json:"scope"`
	Invocation InvocationRef      `json:"invocation"`
	Turn       TurnRef            `json:"turn"`
	Trigger    ObservationTrigger `json:"trigger"`
	Resources  []ObservedResource `json:"resources,omitempty"`
	Selectors  []ObservedSelector `json:"selectors,omitempty"`
	Facts      []Fact             `json:"facts,omitempty"`
}

type ProfileScope struct {
	Directory string   `json:"directory,omitempty"`
	Audience  Audience `json:"audience"`
	Task      TaskRef  `json:"task"`
}

type ValidityPolicy string

const (
	ValidityNone         ValidityPolicy = "none"
	ValidityUntil        ValidityPolicy = "until"
	ValidityActivityTTL  ValidityPolicy = "activity-ttl"
	ValidityRefreshAfter ValidityPolicy = "refresh-after"
)

type FactValidity struct {
	Policy       ValidityPolicy    `json:"policy"`
	NotBefore    time.Time         `json:"notBefore,omitempty"`
	ExpiresAt    time.Time         `json:"expiresAt,omitempty"`
	RefreshAfter time.Time         `json:"refreshAfter,omitempty"`
	Rule         string            `json:"rule,omitempty"`
	Inputs       []ReasonParameter `json:"inputs,omitempty"`
}

type FactProvenance struct {
	Origin         FactOrigin     `json:"origin"`
	Provider       ProviderID     `json:"provider,omitempty"`
	Rule           string         `json:"rule,omitempty"`
	SourceRevision SourceRevision `json:"sourceRevision,omitempty"`
}

type ProfileFact struct {
	Key        string         `json:"key"`
	Value      FactValue      `json:"value"`
	AppliesTo  ProfileScope   `json:"appliesTo"`
	Validity   FactValidity   `json:"validity"`
	Provenance FactProvenance `json:"provenance"`
}

type ProfileSnapshot struct {
	Revision    ProfileRevision `json:"revision"`
	Audience    Audience        `json:"audience"`
	Facts       []ProfileFact   `json:"facts"`
	GeneratedAt time.Time       `json:"generatedAt"`
	ValidUntil  time.Time       `json:"validUntil,omitempty"`
}

type ProfileLimits struct {
	MaxFacts       uint32 `json:"maxFacts"`
	MaxReasonBytes uint64 `json:"maxReasonBytes"`
}

type ProfileRequest struct {
	RequestID    RequestID        `json:"requestId"`
	Scope        ScopeIdentity    `json:"scope"`
	Audience     Audience         `json:"audience"`
	Task         TaskRef          `json:"task"`
	Host         HostSnapshot     `json:"host"`
	Explicit     ProfileSelection `json:"explicit"`
	ConfigDigest ConfigDigest     `json:"configDigest"`
	Now          time.Time        `json:"now"`
	Limits       ProfileLimits    `json:"limits"`
}

type ProfileResponse struct {
	Facts   []ProfileFact `json:"facts"`
	Reasons []Reason      `json:"reasons"`
}

// ProfileCompositionInput contains only the facts eligible for this one
// scope/audience/task. WorkingDirectory makes directory applicability
// directional: a fact rooted at /repo applies to /repo/sub, not the reverse.
// Now is used to expire facts, not to perturb a profile revision when the
// eligible facts have not changed.
type ProfileCompositionInput struct {
	Scope            ScopeIdentity    `json:"scope"`
	WorkingDirectory string           `json:"workingDirectory"`
	Audience         Audience         `json:"audience"`
	Task             TaskRef          `json:"task"`
	Explicit         ProfileSelection `json:"explicit"`
	HostFacts        []Fact           `json:"hostFacts"`
	ProviderFacts    []ProfileFact    `json:"providerFacts"`
	ConfigDigest     ConfigDigest     `json:"configDigest"`
	Now              time.Time        `json:"now"`
}

type ProfileCompositionResult struct {
	Snapshot ProfileSnapshot `json:"snapshot"`
	Reasons  []Reason        `json:"reasons"`
}
