package contextapi

import "time"

type ReasonOrigin string

const (
	ReasonObserved   ReasonOrigin = "observed"
	ReasonConfigured ReasonOrigin = "configured"
	ReasonInferred   ReasonOrigin = "inferred"
	ReasonProvider   ReasonOrigin = "provider"
	ReasonAdapter    ReasonOrigin = "adapter"
	ReasonRuntime    ReasonOrigin = "runtime"
)

type ReasonCode string

const (
	ReasonInactive              ReasonCode = "activation.inactive"
	ReasonExcluded              ReasonCode = "activation.excluded"
	ReasonConflict              ReasonCode = "activation.conflict"
	ReasonInvalidConfig         ReasonCode = "activation.invalid-config"
	ReasonWithdrawn             ReasonCode = "activation.withdrawn"
	ReasonNoMatch               ReasonCode = "contribution.no-match"
	ReasonProviderNotSelected   ReasonCode = "provider.not-selected"
	ReasonProviderFailed        ReasonCode = "provider.failed"
	ReasonMalformedContribution ReasonCode = "contribution.malformed"
	ReasonQueueFull             ReasonCode = "delivery.queue-full"
	ReasonQueueExpired          ReasonCode = "delivery.queue-expired"
	ReasonProfileExpired        ReasonCode = "profile.expired"
	ReasonProfileRefresh        ReasonCode = "profile.refresh-required"
	ReasonProfileChanged        ReasonCode = "profile.changed"
	ReasonProfileConflict       ReasonCode = "profile.conflict"
	ReasonSourceChanged         ReasonCode = "source.changed"
	ReasonNoDeliveryCapability  ReasonCode = "delivery.unavailable"
	ReasonNoRoom                ReasonCode = "delivery.no-room"
	ReasonOfferExpired          ReasonCode = "delivery.offer-expired"
	ReasonOfferMismatch         ReasonCode = "delivery.offer-mismatch"
	ReasonAudienceMismatch      ReasonCode = "delivery.audience-mismatch"
	ReasonEpochMismatch         ReasonCode = "delivery.epoch-mismatch"
	ReasonOutputFailed          ReasonCode = "delivery.output-failed"
	ReasonOutputUnknown         ReasonCode = "delivery.output-unknown"
	ReasonRuntimeRestarted      ReasonCode = "runtime.restarted"
	ReasonEvidenceUnavailable   ReasonCode = "evidence.unavailable"
	ReasonScopeMismatch         ReasonCode = "delivery.scope-mismatch"
	ReasonGenerationMismatch    ReasonCode = "delivery.generation-mismatch"
	ReasonInvalidInput          ReasonCode = "delivery.invalid-input"
	ReasonReceiptLimit          ReasonCode = "delivery.receipt-limit"
	ReasonAlreadyDelivered      ReasonCode = "delivery.already-delivered"
)

type ReasonValueKind string

const (
	ReasonValueText     ReasonValueKind = "text"
	ReasonValueNumber   ReasonValueKind = "number"
	ReasonValueBoolean  ReasonValueKind = "boolean"
	ReasonValueBytes    ReasonValueKind = "bytes"
	ReasonValueDuration ReasonValueKind = "duration-nanos"
)

type ReasonValue struct {
	Kind          ReasonValueKind `json:"kind"`
	Text          string          `json:"text,omitempty"`
	Number        int64           `json:"number,omitempty"`
	Boolean       bool            `json:"boolean,omitempty"`
	Bytes         uint64          `json:"bytes,omitempty"`
	DurationNanos int64           `json:"durationNanos,omitempty"`
}

type ReasonParameter struct {
	Key   string      `json:"key"`
	Value ReasonValue `json:"value"`
}

// Reason is structured evidence, not the suppression decision itself. Params
// preserve the values used at decision time so an old explanation does not
// depend on today's configuration or files.
type Reason struct {
	Code     ReasonCode        `json:"code"`
	Origin   ReasonOrigin      `json:"origin"`
	Provider ProviderID        `json:"provider,omitempty"`
	Rule     string            `json:"rule,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Params   []ReasonParameter `json:"params,omitempty"`
	Evidence []EvidenceID      `json:"evidence,omitempty"`
	At       time.Time         `json:"at"`
}

type DeliverySurface string

const (
	DeliveryClaudeContext DeliverySurface = "claude-context"
	DeliveryCodexContext  DeliverySurface = "codex-context"
)

type DeliveryOpportunity struct {
	ID         string          `json:"id"`
	Surface    DeliverySurface `json:"surface"`
	Available  bool            `json:"available"`
	MaxBytes   uint64          `json:"maxBytes"`
	MaxItems   uint32          `json:"maxItems"`
	Invocation InvocationRef   `json:"invocation"`
}

// GlobalAdmission is the residual machine-wide capacity granted to the target
// engine call. Each field is a maximum resulting total for this engine after
// other engine partitions and runtime-owned metadata/reservations have been
// charged; fields are not increments. Zero is a real no-capacity grant.
type GlobalAdmission struct {
	MaxPendingItems       uint32 `json:"maxPendingItems"`
	MaxPendingMemoryBytes uint64 `json:"maxPendingMemoryBytes"`
	MaxOutstandingOffers  uint32 `json:"maxOutstandingOffers"`
	MaxReceipts           uint32 `json:"maxReceipts"`
}

// EngineLimits are fixed when an owned contextengine.Engine is constructed.
// Zero values mean the runtime must supply its documented defaults; a live
// engine never widens these bounds per request. MaxRetainedBytes covers the
// engine's retained metadata, pending bodies, live offer bodies, and bounded
// replay identities.
type EngineLimits struct {
	Queue            QueueLimits `json:"queue"`
	MaxLiveOffers    uint32      `json:"maxLiveOffers"`
	MaxReceipts      uint32      `json:"maxReceipts"`
	MaxOfferAgeMs    uint64      `json:"maxOfferAgeMs"`
	MaxRetainedBytes uint64      `json:"maxRetainedBytes"`
}

// DeliveryPartition is the immutable identity owned by one engine. Scope's
// ConfigDigest is the declaration digest; ConfigDigest is the combined
// effective activation digest. State for another scope, audience, epoch, or
// runtime generation is never co-mingled.
type DeliveryPartition struct {
	Scope        ScopeIdentity     `json:"scope"`
	Audience     Audience          `json:"audience"`
	Generation   RuntimeGeneration `json:"generation"`
	ConfigDigest ConfigDigest      `json:"configDigest"`
}

type DeliveryMode string

const (
	DeliveryFullBody       DeliveryMode = "full-body"
	DeliveryElidedReminder DeliveryMode = "elided-reminder"
)

type OfferItemIdentity struct {
	Slot           ContributionSlot `json:"slot"`
	Source         SourceIdentity   `json:"source"`
	SourceRevision SourceRevision   `json:"sourceRevision"`
	Content        ContentIdentity  `json:"content"`
	Mode           DeliveryMode     `json:"mode"`
}

type OfferItem struct {
	Identity    OfferItemIdentity `json:"identity"`
	Source      SourceRef         `json:"source"`
	Recruitment SourceRecruitment `json:"recruitment,omitempty"`
	Body        string            `json:"body"`
}

// PendingSource is the body-free metadata runtime may retain for recruitment
// evidence. Identity is the exact full-body pending item key.
type PendingSource struct {
	Identity    OfferItemIdentity `json:"identity"`
	Source      SourceRef         `json:"source"`
	Recruitment SourceRecruitment `json:"recruitment,omitempty"`
}

// DeliveryWithdrawal names one provider-validated stale source revision. Scope
// carries the declaration digest; ConfigDigest carries the effective
// activation digest. The partition fields fence a delayed validation result
// from another engine generation, audience, scope, or configuration.
type DeliveryWithdrawal struct {
	Now          time.Time         `json:"now"`
	Scope        ScopeIdentity     `json:"scope"`
	Audience     Audience          `json:"audience"`
	Generation   RuntimeGeneration `json:"generation"`
	ConfigDigest ConfigDigest      `json:"configDigest"`
	Item         OfferItemIdentity `json:"item"`
}

// WithdrawalResult reports only exact pending/live state removed by a
// withdrawal. Receipts and unrelated pending items are never removed.
type WithdrawalResult struct {
	RemovedItems  uint32   `json:"removedItems"`
	RevokedOffers uint32   `json:"revokedOffers"`
	Reasons       []Reason `json:"reasons,omitempty"`
}

// OfferIdentity is copied into confirmation. Matching only OfferID is not
// sufficient: generation, audience/epoch, opportunity, source revisions,
// content identities, and the exact rendered body must all agree.
type OfferIdentity struct {
	Generation    RuntimeGeneration   `json:"generation"`
	ID            OfferID             `json:"id"`
	Audience      Audience            `json:"audience"`
	OpportunityID string              `json:"opportunityId"`
	Surface       DeliverySurface     `json:"surface"`
	Items         []OfferItemIdentity `json:"items"`
	Body          ContentIdentity     `json:"body"`
}

type Offer struct {
	Identity OfferIdentity `json:"identity"`
	Items    []OfferItem   `json:"items"`
	Body     string        `json:"body"`
}

type Receipt struct {
	Generation  RuntimeGeneration `json:"generation"`
	Audience    Audience          `json:"audience"`
	Item        OfferItemIdentity `json:"item"`
	ConfirmedAt time.Time         `json:"confirmedAt"`
}

type DeliveryState string

const (
	DeliveryIdle       DeliveryState = "idle"
	DeliveryQueued     DeliveryState = "queued"
	DeliveryOffered    DeliveryState = "offered"
	DeliveryConfirmed  DeliveryState = "confirmed"
	DeliverySuppressed DeliveryState = "suppressed"
	DeliveryDeferred   DeliveryState = "deferred"
	DeliveryRejected   DeliveryState = "rejected"
	DeliveryFailed     DeliveryState = "failed"
	DeliveryWithdrawn  DeliveryState = "withdrawn"
)

type DeliveryPlanInput struct {
	Now           time.Time           `json:"now"`
	Active        bool                `json:"active"`
	Scope         ScopeIdentity       `json:"scope"`
	ConfigDigest  ConfigDigest        `json:"configDigest"`
	Audience      Audience            `json:"audience"`
	Generation    RuntimeGeneration   `json:"generation"`
	Transition    AudienceTransition  `json:"transition"`
	Profile       ProfileSnapshot     `json:"profile"`
	Contributions []Contribution      `json:"contributions"`
	Opportunity   DeliveryOpportunity `json:"opportunity"`
}

type DeliveryDecision struct {
	State   DeliveryState `json:"state"`
	Offer   Offer         `json:"offer"`
	Reasons []Reason      `json:"reasons"`
}

type HandoffState string

const (
	HandoffConfirmed HandoffState = "confirmed"
	HandoffFailed    HandoffState = "failed"
	HandoffUnknown   HandoffState = "unknown"
)

type HandoffOutcome struct {
	State          HandoffState    `json:"state"`
	EmittedContent ContentIdentity `json:"emittedContent"`
	Reasons        []Reason        `json:"reasons"`
}

type OfferConfirmation struct {
	Identity        OfferIdentity   `json:"identity"`
	Handoff         HandoffOutcome  `json:"handoff"`
	ConfirmedAt     time.Time       `json:"confirmedAt"`
	Active          bool            `json:"active"`
	Scope           ScopeIdentity   `json:"scope"`
	Audience        Audience        `json:"audience"`
	ConfigDigest    ConfigDigest    `json:"configDigest"`
	ProfileRevision ProfileRevision `json:"profileRevision"`
	// ProfileValidUntil is the exact profile deadline used to make the offer.
	// Confirm compares the instant, not time.Time's location/monotonic fields,
	// because this value commonly crosses JSON-RPC.
	ProfileValidUntil time.Time `json:"profileValidUntil,omitempty"`
}

type ConfirmationResult struct {
	State    DeliveryState `json:"state"`
	Receipts []Receipt     `json:"receipts"`
	Reasons  []Reason      `json:"reasons"`
}
