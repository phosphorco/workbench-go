// Package contextengine owns bounded delivery state for one immutable
// scope/audience/epoch/runtime-generation partition. It performs no provider
// I/O, filesystem work, clock reads, or background work.
package contextengine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

var (
	ErrInvalidLimits    = errors.New("contextengine: invalid limits")
	ErrInvalidPartition = errors.New("contextengine: invalid partition")
)

const maxDurationMilliseconds = uint64((1<<63 - 1) / int64(time.Millisecond))

// Stats is a bounded read-only snapshot for runtime admission and status. It
// does not expose pending bodies, profile facts, or explanation evidence.
type Stats struct {
	PendingBytes  uint64
	PendingItems  uint32
	LiveOffers    uint32
	Receipts      uint32
	RetainedBytes uint64
}

// Engine is the owner of pending delivery, live offers, and receipts for one
// partition. The mutex only protects these small in-memory structures; callers
// must do provider work before calling Plan.
type Engine struct {
	mu        sync.Mutex
	partition contextapi.DeliveryPartition
	limits    contextapi.EngineLimits

	pending      []pendingGroup
	pendingBytes uint64
	live         map[uint64]liveOffer
	receipts     map[receiptKey]time.Time
	completed    map[uint64]completedOffer
	nextOfferID  uint64
}

type pendingGroup struct {
	items     []pendingItem
	firstSeen time.Time
	semantic  string
}

type pendingItem struct {
	contribution contextapi.Contribution
	admittedAt   time.Time
}

type liveOffer struct {
	offer             contextapi.Offer
	profileRevision   contextapi.ProfileRevision
	profileValidUntil time.Time
	configDigest      contextapi.ConfigDigest
	offeredAt         time.Time
}

type completedOffer struct {
	identity          contextapi.OfferIdentity
	profileRevision   contextapi.ProfileRevision
	profileValidUntil time.Time
	receipts          []contextapi.Receipt
}

type receiptKey struct {
	slot           contextapi.ContributionSlot
	source         contextapi.SourceIdentity
	sourceRevision contextapi.SourceRevisionID
	content        contextapi.ContentID
	contentBytes   uint64
}

// NewEngine creates an owned state partition. Zero bounds are rejected so a
// configuration owner must choose and document defaults before constructing an
// engine; a request cannot widen the resulting bounds.
func NewEngine(partition contextapi.DeliveryPartition, limits contextapi.EngineLimits) (*Engine, error) {
	if partition.Scope.ID == "" || partition.Scope.Authority == "" || partition.Scope.CanonicalRoot == "" ||
		partition.Scope.ConfigDigest == "" || partition.ConfigDigest == "" ||
		partition.Audience.ID == "" || partition.Audience.Epoch == 0 || partition.Generation == 0 {
		return nil, fmt.Errorf("%w: scope, audience, config, epoch, and generation are required", ErrInvalidPartition)
	}
	if limits.Queue.MaxItemBytes == 0 || limits.Queue.MaxPendingBytes == 0 ||
		limits.Queue.MaxPendingItems == 0 || limits.Queue.MaxAgeMs == 0 ||
		limits.MaxLiveOffers == 0 || limits.MaxReceipts == 0 || limits.MaxOfferAgeMs == 0 ||
		limits.MaxRetainedBytes == 0 ||
		limits.Queue.MaxAgeMs > maxDurationMilliseconds || limits.MaxOfferAgeMs > maxDurationMilliseconds {
		return nil, fmt.Errorf("%w: all queue, offer, and receipt bounds must be positive", ErrInvalidLimits)
	}
	return &Engine{
		partition: partition,
		limits:    limits,
		live:      make(map[uint64]liveOffer),
		receipts:  make(map[receiptKey]time.Time),
		completed: make(map[uint64]completedOffer),
	}, nil
}

// Stats returns authoritative bounded counts for the current partition.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Stats{
		PendingBytes:  e.pendingBytes,
		PendingItems:  uint32(pendingItemCount(e.pending)),
		LiveOffers:    uint32(len(e.live)),
		Receipts:      uint32(len(e.receipts)),
		RetainedBytes: e.retainedBytesLocked(),
	}
}

// PendingSources returns bounded, body-free metadata for every currently
// pending item. The returned values are owned copies; callers may retain or
// mutate them without changing engine state.
func (e *Engine) PendingSources() []contextapi.PendingSource {
	e.mu.Lock()
	defer e.mu.Unlock()

	result := make([]contextapi.PendingSource, 0, pendingItemCount(e.pending))
	for _, group := range e.pending {
		for _, item := range group.items {
			contribution := item.contribution
			result = append(result, contextapi.PendingSource{
				Identity:    cloneOfferItemIdentity(contributionItemIdentity(contribution, contextapi.DeliveryFullBody)),
				Source:      cloneSourceRef(contribution.Source),
				Recruitment: contextapi.SourceRecruitment{Kind: contribution.Recruitment.Kind, Resource: strings.Clone(contribution.Recruitment.Resource)},
			})
		}
	}
	return result
}

// Withdraw removes only the exact source revision/content named by input. A
// delayed stale validation cannot remove a newer replacement because neither
// pending nor live matching uses the source slot alone.
func (e *Engine) Withdraw(input contextapi.DeliveryWithdrawal) contextapi.WithdrawalResult {
	e.mu.Lock()
	defer e.mu.Unlock()

	if input.Generation != e.partition.Generation {
		return withdrawalRejected(reasonAt(contextapi.ReasonGenerationMismatch, contextapi.ReasonRuntime, input.Now, "withdrawal generation does not match engine partition"))
	}
	if !sameScopeRoot(input.Scope, e.partition.Scope) {
		return withdrawalRejected(reasonAt(contextapi.ReasonScopeMismatch, contextapi.ReasonRuntime, input.Now, "withdrawal scope does not match engine partition"))
	}
	if !sameAudience(input.Audience, e.partition.Audience) {
		return withdrawalRejected(reasonAt(contextapi.ReasonAudienceMismatch, contextapi.ReasonRuntime, input.Now, "withdrawal audience or epoch does not match engine partition"))
	}
	if input.ConfigDigest != e.partition.ConfigDigest || input.Scope.ConfigDigest != e.partition.Scope.ConfigDigest {
		return withdrawalRejected(reasonAt(contextapi.ReasonWithdrawn, contextapi.ReasonRuntime, input.Now, "withdrawal configuration digest is stale"))
	}
	if !validWithdrawalIdentity(input.Item) {
		return withdrawalRejected(reasonAt(contextapi.ReasonInvalidInput, contextapi.ReasonRuntime, input.Now, "withdrawal item identity is incomplete"))
	}

	working := clonePending(e.pending)
	working, removed := removePendingItem(working, input.Item)
	e.pending = working
	e.pendingBytes, _ = pendingBytes(e.pending, e.limits.Queue.MaxItemBytes)

	revoked := uint32(0)
	for id, offer := range e.live {
		if offerContainsItem(offer.offer.Identity.Items, input.Item) {
			delete(e.live, id)
			revoked++
		}
	}
	summary := "provider revalidation withdrew only the exact stale source identity"
	if removed == 0 && revoked == 0 {
		summary = "provider revalidation found no matching current item; newer replacement and unrelated state were preserved"
	}

	return contextapi.WithdrawalResult{
		RemovedItems:  removed,
		RevokedOffers: revoked,
		Reasons: []contextapi.Reason{reasonAt(
			contextapi.ReasonSourceChanged,
			contextapi.ReasonProvider,
			input.Now,
			summary,
		)},
	}
}

// Plan admits complete contributions and, when possible, returns one exact
// offer. The engine retains copies of every contribution it accepts.
func (e *Engine) Plan(input contextapi.DeliveryPlanInput, admission contextapi.GlobalAdmission) contextapi.DeliveryDecision {
	e.mu.Lock()
	defer e.mu.Unlock()

	if input.Generation != e.partition.Generation {
		return decision(contextapi.DeliveryRejected, reasonAt(contextapi.ReasonGenerationMismatch, contextapi.ReasonRuntime, input.Now, "runtime generation does not match engine partition"))
	}
	if !sameScopeRoot(input.Scope, e.partition.Scope) {
		return decision(contextapi.DeliveryRejected, reasonAt(contextapi.ReasonScopeMismatch, contextapi.ReasonRuntime, input.Now, "scope does not match engine partition"))
	}
	if !sameAudience(input.Audience, e.partition.Audience) {
		return decision(contextapi.DeliveryRejected, reasonAt(contextapi.ReasonAudienceMismatch, contextapi.ReasonRuntime, input.Now, "audience or epoch does not match engine partition"))
	}
	if input.ConfigDigest != e.partition.ConfigDigest || input.Scope.ConfigDigest != e.partition.Scope.ConfigDigest {
		e.clearLocked()
		return decision(contextapi.DeliveryWithdrawn, reasonAt(contextapi.ReasonWithdrawn, contextapi.ReasonRuntime, input.Now, "configuration digest changed"))
	}
	if input.Transition.Kind != "" {
		e.clearLocked()
		return decision(contextapi.DeliveryWithdrawn, reasonAt(contextapi.ReasonWithdrawn, contextapi.ReasonRuntime, input.Now, "explicit audience transition"))
	}
	if !input.Active {
		e.clearLocked()
		return decision(contextapi.DeliveryWithdrawn, reasonAt(contextapi.ReasonWithdrawn, contextapi.ReasonRuntime, input.Now, "scope is inactive"))
	}
	if !profileAudienceMatches(input.Profile, input.Audience) {
		return decision(contextapi.DeliveryRejected, reasonAt(contextapi.ReasonAudienceMismatch, contextapi.ReasonRuntime, input.Now, "profile audience does not match delivery audience"))
	}
	if profileExpired(input.Profile, input.Now) {
		e.clearPendingAndOffersLocked()
		return decision(contextapi.DeliveryDeferred, reasonAt(contextapi.ReasonProfileExpired, contextapi.ReasonRuntime, input.Now, "profile snapshot is expired"))
	}

	reasons := e.expireLocked(input.Now)
	if e.profileChangedLocked(input.Profile.Revision) {
		e.clearPendingAndOffersLocked()
		reasons = append(reasons, reasonAt(contextapi.ReasonProfileChanged, contextapi.ReasonRuntime, input.Now, "pending delivery used another profile revision"))
	}
	if existing := e.existingOfferLocked(input); existing != nil {
		if failure := globalAdmissionFailure(e.pending, e.live, e.receipts, e.completed, 0, input.Now, admission); failure != nil {
			return decision(contextapi.DeliveryDeferred, append(reasons, *failure)...)
		}
		return contextapi.DeliveryDecision{State: contextapi.DeliveryOffered, Offer: cloneOffer(existing.offer), Reasons: cloneReasons(reasons)}
	}

	working := clonePending(e.pending)
	changedSources := make([]sourceSlotKey, 0, len(input.Contributions))
	accepted := make(map[contextapi.OfferItemIdentity]struct{}, len(input.Contributions))
	suppressed := false
	for _, original := range input.Contributions {
		contribution, why := normalizeContribution(original, input, e.limits.Queue.MaxItemBytes)
		if why != nil {
			reason := *why
			reason.Provider = original.Contributor
			reasons = append(reasons, reason)
			continue
		}
		key := sourceSlot(contribution)
		if e.hasReceiptLocked(contribution) {
			suppressed = true
			working = removeSource(working, key)
			changedSources = appendUniqueSource(changedSources, key)
			continue
		}
		if containsPending(working, contribution) {
			continue
		}
		working = removeSource(working, key)
		changedSources = appendUniqueSource(changedSources, key)
		admittedAt := input.Now
		groupIndex := findGroup(working, contribution.Body)
		if groupIndex < 0 {
			working = append(working, pendingGroup{
				semantic:  contribution.Body,
				firstSeen: admittedAt,
				items: []pendingItem{{
					contribution: cloneContribution(contribution),
					admittedAt:   admittedAt,
				}},
			})
		} else {
			working[groupIndex].items = append(working[groupIndex].items, pendingItem{
				contribution: cloneContribution(contribution),
				admittedAt:   admittedAt,
			})
			working[groupIndex].firstSeen = earlier(working[groupIndex].firstSeen, admittedAt)
		}
		accepted[contributionItemIdentity(contribution, contextapi.DeliveryFullBody)] = struct{}{}
	}

	working = removeEmptyGroups(working)
	working, renderReasons := isolateOversizedItems(working, e.limits.Queue.MaxItemBytes, input.Now)
	reasons = append(reasons, renderReasons...)
	working, overflowReasons := trimNewItems(working, accepted, e.limits.Queue.MaxPendingItems, input.Now)
	reasons = append(reasons, overflowReasons...)
	working, byteReasons := trimNewItemsByBytes(working, accepted, e.limits.Queue.MaxPendingBytes, input.Now)
	reasons = append(reasons, byteReasons...)
	workingBytes, oversized := pendingBytes(working, e.limits.Queue.MaxItemBytes)
	// isolateOversizedItems applies the same render metric; keep this as a
	// defensive backstop for legacy state or accounting overflow.
	if oversized {
		return decision(contextapi.DeliveryRejected, append(reasons, reasonAt(contextapi.ReasonQueueFull, contextapi.ReasonRuntime, input.Now, "coalesced complete body exceeds item bound"))...)
	}
	if uint32(pendingItemCount(working)) > e.limits.Queue.MaxPendingItems || workingBytes > e.limits.Queue.MaxPendingBytes {
		return decision(contextapi.DeliveryRejected, append(reasons, reasonAt(contextapi.ReasonQueueFull, contextapi.ReasonRuntime, input.Now, "pending queue bound would be exceeded"))...)
	}
	prospectiveLive := cloneLive(e.live)
	for _, key := range changedSources {
		revokeSourceOffers(prospectiveLive, key)
	}
	if retainedBytesFor(working, prospectiveLive, e.receipts, e.completed) > e.limits.MaxRetainedBytes {
		return decision(contextapi.DeliveryRejected, append(reasons, reasonAt(contextapi.ReasonQueueFull, contextapi.ReasonRuntime, input.Now, "retained metadata and body budget would be exceeded"))...)
	}
	if failure := globalAdmissionFailure(working, prospectiveLive, e.receipts, e.completed, 0, input.Now, admission); failure != nil {
		return decision(contextapi.DeliveryDeferred, append(reasons, *failure)...)
	}
	e.pending = working
	e.pendingBytes = workingBytes
	for _, key := range changedSources {
		e.revokeSourceOffersLocked(key)
	}

	if len(e.pending) == 0 {
		if suppressed {
			return decision(contextapi.DeliverySuppressed, append(reasons, reasonAt(contextapi.ReasonAlreadyDelivered, contextapi.ReasonRuntime, input.Now, "all matching source content has a confirmed receipt"))...)
		}
		return contextapi.DeliveryDecision{State: contextapi.DeliveryIdle, Reasons: cloneReasons(reasons)}
	}
	if !input.Opportunity.Available {
		return decision(contextapi.DeliveryDeferred, append(reasons, reasonAt(contextapi.ReasonNoDeliveryCapability, contextapi.ReasonRuntime, input.Now, "delivery opportunity is unavailable"))...)
	}
	if input.Opportunity.MaxBytes == 0 || input.Opportunity.MaxItems == 0 {
		return decision(contextapi.DeliveryDeferred, append(reasons, reasonAt(contextapi.ReasonNoRoom, contextapi.ReasonRuntime, input.Now, "delivery opportunity has no room"))...)
	}
	if uint32(len(e.live)) >= e.limits.MaxLiveOffers {
		return decision(contextapi.DeliveryDeferred, append(reasons, reasonAt(contextapi.ReasonNoRoom, contextapi.ReasonRuntime, input.Now, "live offer bound is full"))...)
	}

	offer, selected := e.buildOfferLocked(input)
	if len(selected) == 0 {
		return decision(contextapi.DeliveryDeferred, append(reasons, reasonAt(contextapi.ReasonNoRoom, contextapi.ReasonRuntime, input.Now, "no complete pending item fits the opportunity"))...)
	}
	if e.nextOfferID == ^uint64(0) {
		return decision(contextapi.DeliveryRejected, append(reasons, reasonAt(contextapi.ReasonInvalidInput, contextapi.ReasonRuntime, input.Now, "offer ID space is exhausted"))...)
	}
	nextOfferID := e.nextOfferID + 1
	offer.Identity.ID = contextapi.OfferID(nextOfferID)
	offer.Identity.Generation = e.partition.Generation
	offer.Identity.Audience = e.partition.Audience
	offer.Identity.OpportunityID = input.Opportunity.ID
	offer.Identity.Surface = input.Opportunity.Surface
	offer.Identity.Body = contentIdentity(offer.Body)
	offer.Identity.Items = offerItemIdentities(offer.Items)
	offer.Identity = cloneOfferIdentity(offer.Identity)
	candidateLive := cloneLive(e.live)
	candidateLive[nextOfferID] = liveOffer{
		offer:             cloneOffer(offer),
		profileRevision:   input.Profile.Revision,
		profileValidUntil: input.Profile.ValidUntil,
		configDigest:      input.ConfigDigest,
		offeredAt:         input.Now,
	}
	if retainedBytesFor(e.pending, candidateLive, e.receipts, e.completed) > e.limits.MaxRetainedBytes {
		return decision(contextapi.DeliveryDeferred, append(reasons, reasonAt(contextapi.ReasonQueueFull, contextapi.ReasonRuntime, input.Now, "retained offer budget would be exceeded"))...)
	}
	if failure := globalAdmissionFailure(e.pending, candidateLive, e.receipts, e.completed, 0, input.Now, admission); failure != nil {
		return decision(contextapi.DeliveryDeferred, append(reasons, *failure)...)
	}
	e.nextOfferID = nextOfferID
	e.live = candidateLive
	return contextapi.DeliveryDecision{State: contextapi.DeliveryOffered, Offer: cloneOffer(offer), Reasons: cloneReasons(reasons)}
}

// Confirm accepts only an exact live offer and a confirmed exact handoff. It
// never uses a failed or uncertain output as a suppression receipt.
func (e *Engine) Confirm(input contextapi.OfferConfirmation, admission contextapi.GlobalAdmission) contextapi.ConfirmationResult {
	e.mu.Lock()
	defer e.mu.Unlock()

	if input.Identity.Generation != e.partition.Generation {
		return confirmationRejected(reasonAt(contextapi.ReasonGenerationMismatch, contextapi.ReasonRuntime, input.ConfirmedAt, "confirmation generation does not match engine partition"))
	}
	if !input.Active {
		return contextapi.ConfirmationResult{State: contextapi.DeliveryWithdrawn, Reasons: []contextapi.Reason{reasonAt(contextapi.ReasonWithdrawn, contextapi.ReasonRuntime, input.ConfirmedAt, "scope is inactive at confirmation")}}
	}
	if !sameScopeRoot(input.Scope, e.partition.Scope) {
		return confirmationRejected(reasonAt(contextapi.ReasonScopeMismatch, contextapi.ReasonRuntime, input.ConfirmedAt, "confirmation scope does not match engine partition"))
	}
	if !sameAudience(input.Audience, e.partition.Audience) || !sameAudience(input.Identity.Audience, e.partition.Audience) {
		return confirmationRejected(reasonAt(contextapi.ReasonAudienceMismatch, contextapi.ReasonRuntime, input.ConfirmedAt, "confirmation audience or epoch does not match engine partition"))
	}
	live, liveOK := e.live[uint64(input.Identity.ID)]
	if input.ConfigDigest != e.partition.ConfigDigest || input.Scope.ConfigDigest != e.partition.Scope.ConfigDigest {
		if liveOK {
			if !sameOfferIdentity(live.offer.Identity, input.Identity) {
				return confirmationRejected(reasonAt(contextapi.ReasonOfferMismatch, contextapi.ReasonRuntime, input.ConfirmedAt, "stale configuration confirmation identity differs from live offer"))
			}
			delete(e.live, uint64(input.Identity.ID))
		}
		return contextapi.ConfirmationResult{State: contextapi.DeliveryWithdrawn, Reasons: []contextapi.Reason{reasonAt(contextapi.ReasonWithdrawn, contextapi.ReasonRuntime, input.ConfirmedAt, "configuration changed before confirmation")}}
	}

	if completed, ok := e.completed[uint64(input.Identity.ID)]; ok {
		if sameOfferIdentity(completed.identity, input.Identity) &&
			input.ProfileRevision == completed.profileRevision &&
			input.ProfileValidUntil.Equal(completed.profileValidUntil) &&
			!profileDeadlinePassed(completed.profileValidUntil, input.ConfirmedAt) &&
			input.Handoff.State == contextapi.HandoffConfirmed &&
			sameContent(completed.identity.Body, input.Handoff.EmittedContent) {
			if failure := globalAdmissionFailure(e.pending, e.live, e.receipts, e.completed, 0, input.ConfirmedAt, admission); failure != nil {
				return confirmationRejected(*failure)
			}
			return contextapi.ConfirmationResult{State: contextapi.DeliveryConfirmed, Receipts: cloneReceipts(completed.receipts)}
		}
		return confirmationRejected(reasonAt(contextapi.ReasonOfferMismatch, contextapi.ReasonRuntime, input.ConfirmedAt, "replayed confirmation does not match accepted offer"))
	}

	if !liveOK {
		return confirmationRejected(reasonAt(contextapi.ReasonOfferMismatch, contextapi.ReasonRuntime, input.ConfirmedAt, "offer is not live"))
	}
	if !sameOfferIdentity(live.offer.Identity, input.Identity) {
		return confirmationRejected(reasonAt(contextapi.ReasonOfferMismatch, contextapi.ReasonRuntime, input.ConfirmedAt, "confirmation identity differs from live offer"))
	}
	if input.ProfileRevision != live.profileRevision {
		delete(e.live, uint64(input.Identity.ID))
		return contextapi.ConfirmationResult{State: contextapi.DeliveryWithdrawn, Reasons: []contextapi.Reason{reasonAt(contextapi.ReasonProfileChanged, contextapi.ReasonRuntime, input.ConfirmedAt, "profile revision changed before confirmation")}}
	}
	if !input.ProfileValidUntil.Equal(live.profileValidUntil) || profileDeadlinePassed(live.profileValidUntil, input.ConfirmedAt) {
		delete(e.live, uint64(input.Identity.ID))
		return contextapi.ConfirmationResult{State: contextapi.DeliveryWithdrawn, Reasons: []contextapi.Reason{reasonAt(contextapi.ReasonProfileExpired, contextapi.ReasonRuntime, input.ConfirmedAt, "profile expired before confirmation")}}
	}
	if offerExpired(live.offeredAt, input.ConfirmedAt, e.limits.MaxOfferAgeMs) {
		delete(e.live, uint64(input.Identity.ID))
		return contextapi.ConfirmationResult{State: contextapi.DeliveryFailed, Reasons: []contextapi.Reason{reasonAt(contextapi.ReasonOfferExpired, contextapi.ReasonRuntime, input.ConfirmedAt, "offer exceeded its age bound")}}
	}
	if input.Handoff.State == contextapi.HandoffFailed {
		delete(e.live, uint64(input.Identity.ID))
		return contextapi.ConfirmationResult{State: contextapi.DeliveryFailed, Reasons: []contextapi.Reason{reasonAt(contextapi.ReasonOutputFailed, contextapi.ReasonAdapter, input.ConfirmedAt, "adapter reported failed handoff")}}
	}
	if input.Handoff.State == contextapi.HandoffUnknown {
		delete(e.live, uint64(input.Identity.ID))
		return contextapi.ConfirmationResult{State: contextapi.DeliveryFailed, Reasons: []contextapi.Reason{reasonAt(contextapi.ReasonOutputUnknown, contextapi.ReasonAdapter, input.ConfirmedAt, "adapter could not establish handoff")}}
	}
	if input.Handoff.State != contextapi.HandoffConfirmed || !sameContent(live.offer.Identity.Body, input.Handoff.EmittedContent) {
		return confirmationRejected(reasonAt(contextapi.ReasonOfferMismatch, contextapi.ReasonAdapter, input.ConfirmedAt, "emitted content does not equal offered content"))
	}

	newReceipts := make([]contextapi.Receipt, 0, len(live.offer.Identity.Items))
	for _, item := range live.offer.Identity.Items {
		if item.Mode != contextapi.DeliveryFullBody {
			continue
		}
		key := makeReceiptKey(item)
		if _, already := e.receipts[key]; already {
			continue
		}
		newReceipts = append(newReceipts, contextapi.Receipt{
			Generation:  e.partition.Generation,
			Audience:    e.partition.Audience,
			Item:        cloneOfferItemIdentity(item),
			ConfirmedAt: input.ConfirmedAt,
		})
	}
	if uint32(len(e.receipts)+len(newReceipts)) > e.limits.MaxReceipts {
		return confirmationRejected(reasonAt(contextapi.ReasonReceiptLimit, contextapi.ReasonRuntime, input.ConfirmedAt, "receipt bound would be exceeded"))
	}
	receiptIdentities := receiptItems(live.offer.Identity.Items)
	prospectivePending := removeExactItems(clonePending(e.pending), receiptIdentities)
	prospectivePendingBytes, _ := pendingBytes(prospectivePending, e.limits.Queue.MaxItemBytes)
	prospectiveReceipts := cloneReceiptMap(e.receipts)
	for _, receipt := range newReceipts {
		prospectiveReceipts[makeReceiptKey(receipt.Item)] = receipt.ConfirmedAt
	}
	prospectiveLive := cloneLive(e.live)
	delete(prospectiveLive, uint64(input.Identity.ID))
	prospectiveCompleted := cloneCompleted(e.completed)
	prospectiveCompleted[uint64(input.Identity.ID)] = completedOffer{identity: cloneOfferIdentity(live.offer.Identity), profileRevision: live.profileRevision, profileValidUntil: live.profileValidUntil, receipts: cloneReceipts(newReceipts)}
	trimCompleted(prospectiveCompleted, e.limits.MaxReceipts)
	if retainedBytesFor(prospectivePending, prospectiveLive, prospectiveReceipts, prospectiveCompleted) > e.limits.MaxRetainedBytes {
		return confirmationRejected(reasonAt(contextapi.ReasonQueueFull, contextapi.ReasonRuntime, input.ConfirmedAt, "retained confirmation metadata budget would be exceeded"))
	}
	if failure := globalAdmissionFailure(prospectivePending, prospectiveLive, prospectiveReceipts, prospectiveCompleted, uint32(len(newReceipts)), input.ConfirmedAt, admission); failure != nil {
		return confirmationRejected(*failure)
	}
	e.receipts = prospectiveReceipts
	e.pending = prospectivePending
	e.pendingBytes = prospectivePendingBytes
	e.live = prospectiveLive
	e.completed = prospectiveCompleted
	return contextapi.ConfirmationResult{State: contextapi.DeliveryConfirmed, Receipts: cloneReceipts(newReceipts)}
}

func (e *Engine) clearLocked() {
	e.pending = nil
	e.pendingBytes = 0
	e.live = make(map[uint64]liveOffer)
	e.receipts = make(map[receiptKey]time.Time)
	e.completed = make(map[uint64]completedOffer)
}

func (e *Engine) clearPendingAndOffersLocked() {
	e.pending = nil
	e.pendingBytes = 0
	e.live = make(map[uint64]liveOffer)
}

func (e *Engine) expireLocked(now time.Time) []contextapi.Reason {
	reasons := make([]contextapi.Reason, 0)
	if !now.IsZero() {
		filtered := make([]pendingGroup, 0, len(e.pending))
		for _, group := range e.pending {
			kept := group.items[:0]
			for _, item := range group.items {
				if !ageExceeded(item.admittedAt, now, e.limits.Queue.MaxAgeMs) {
					kept = append(kept, item)
				}
			}
			group.items = kept
			if len(group.items) != 0 {
				filtered = append(filtered, group)
			} else {
				reasons = append(reasons, reasonAt(contextapi.ReasonQueueExpired, contextapi.ReasonRuntime, now, "pending contribution exceeded queue age"))
			}
		}
		e.pending = removeEmptyGroups(filtered)
		e.pendingBytes, _ = pendingBytes(e.pending, e.limits.Queue.MaxItemBytes)
	}
	for id, offer := range e.live {
		if ageExceeded(offer.offeredAt, now, e.limits.MaxOfferAgeMs) {
			delete(e.live, id)
			reasons = append(reasons, reasonAt(contextapi.ReasonOfferExpired, contextapi.ReasonRuntime, now, "live offer exceeded offer age"))
		}
	}
	return reasons
}

func (e *Engine) profileChangedLocked(revision contextapi.ProfileRevision) bool {
	for _, offer := range e.live {
		if offer.profileRevision != revision {
			return true
		}
	}
	for _, group := range e.pending {
		for _, item := range group.items {
			if item.contribution.ProfileRevision != revision {
				return true
			}
		}
	}
	return false
}

func (e *Engine) existingOfferLocked(input contextapi.DeliveryPlanInput) *liveOffer {
	ids := make([]uint64, 0, len(e.live))
	for id := range e.live {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		offer := e.live[id]
		if offer.configDigest != input.ConfigDigest || offer.profileRevision != input.Profile.Revision ||
			offer.offer.Identity.OpportunityID != input.Opportunity.ID || offer.offer.Identity.Surface != input.Opportunity.Surface {
			continue
		}
		if !input.Opportunity.Available || input.Opportunity.MaxBytes == 0 || input.Opportunity.MaxItems == 0 ||
			uint64(len(offer.offer.Body)) > input.Opportunity.MaxBytes || uint32(len(offer.offer.Items)) > input.Opportunity.MaxItems {
			continue
		}
		if len(input.Contributions) == 0 {
			return &offer
		}
		wanted := make(map[contextapi.OfferItemIdentity]uint32, len(input.Contributions))
		for _, contribution := range input.Contributions {
			normalized, why := normalizeContribution(contribution, input, e.limits.Queue.MaxItemBytes)
			if why != nil {
				return nil
			}
			wanted[contributionItemIdentity(normalized, contextapi.DeliveryFullBody)]++
		}
		if len(wanted) != len(offer.offer.Identity.Items) {
			continue
		}
		complete := true
		for _, item := range offer.offer.Identity.Items {
			if wanted[item] == 0 {
				complete = false
				break
			}
			wanted[item]--
		}
		if complete {
			return &offer
		}
	}
	return nil
}

func (e *Engine) revokeSourceOffersLocked(key sourceSlotKey) {
	for id, offer := range e.live {
		for _, item := range offer.offer.Identity.Items {
			if sourceSlotFromIdentity(item) == key {
				delete(e.live, id)
				break
			}
		}
	}
}

func (e *Engine) hasReceiptLocked(contribution contextapi.Contribution) bool {
	_, ok := e.receipts[makeReceiptKey(contributionItemIdentity(contribution, contextapi.DeliveryFullBody))]
	return ok
}

func (e *Engine) buildOfferLocked(input contextapi.DeliveryPlanInput) (contextapi.Offer, []contextapi.OfferItemIdentity) {
	groups := clonePending(e.pending)
	sort.SliceStable(groups, func(i, j int) bool {
		pi, pj := groupPriority(groups[i]), groupPriority(groups[j])
		if pi != pj {
			return pi > pj
		}
		return groups[i].semantic < groups[j].semantic
	})

	selectedGroups := make([]pendingGroup, 0, len(groups))
	itemCount := uint32(0)
	bodyBytes := uint64(0)
	for _, group := range groups {
		if groupAlreadyOffered(group, e.live) {
			continue
		}
		block := renderGroup(group)
		if uint64(len(block)) > input.Opportunity.MaxBytes || uint64(len(block)) > e.limits.Queue.MaxItemBytes {
			continue
		}
		if itemCount+uint32(len(group.items)) > input.Opportunity.MaxItems {
			continue
		}
		separator := uint64(0)
		if len(selectedGroups) > 0 {
			separator = 2
		}
		if bodyBytes+separator+uint64(len(block)) > input.Opportunity.MaxBytes {
			continue
		}
		selectedGroups = append(selectedGroups, group)
		itemCount += uint32(len(group.items))
		bodyBytes += separator + uint64(len(block))
	}
	if len(selectedGroups) == 0 {
		return contextapi.Offer{}, nil
	}

	items := make([]contextapi.OfferItem, 0, itemCount)
	blocks := make([]string, 0, len(selectedGroups))
	for _, group := range selectedGroups {
		block := renderGroup(group)
		blocks = append(blocks, block)
		for _, item := range group.items {
			identity := contributionItemIdentity(item.contribution, contextapi.DeliveryFullBody)
			items = append(items, contextapi.OfferItem{
				Identity:    identity,
				Source:      cloneSourceRef(item.contribution.Source),
				Recruitment: item.contribution.Recruitment,
				Body:        block,
			})
		}
	}
	return contextapi.Offer{Items: items, Body: strings.Join(blocks, "\n\n")}, offerItemIdentities(items)
}

func normalizeContribution(input contextapi.Contribution, plan contextapi.DeliveryPlanInput, maxBytes uint64) (contextapi.Contribution, *contextapi.Reason) {
	if !utf8.ValidString(input.Body) {
		return contextapi.Contribution{}, ptrReason(reasonAt(contextapi.ReasonMalformedContribution, contextapi.ReasonProvider, plan.Now, "contribution body is not valid UTF-8"))
	}
	if input.Body == "" {
		return contextapi.Contribution{}, ptrReason(reasonAt(contextapi.ReasonMalformedContribution, contextapi.ReasonProvider, plan.Now, "empty contribution body"))
	}
	if uint64(len(input.Body)) > maxBytes {
		return contextapi.Contribution{}, ptrReason(reasonAt(contextapi.ReasonQueueFull, contextapi.ReasonRuntime, plan.Now, "contribution body exceeds item bound"))
	}
	if input.Slot.Provider == "" || input.Slot.Key == "" || input.Source.Identity.Provider == "" || input.Source.Identity.ID == "" ||
		input.SourceRevision.Revision == "" || input.Contributor == "" {
		return contextapi.Contribution{}, ptrReason(reasonAt(contextapi.ReasonMalformedContribution, contextapi.ReasonProvider, plan.Now, "contribution identity is incomplete"))
	}
	if input.SourceRevision.Source != input.Source.Identity {
		return contextapi.Contribution{}, ptrReason(reasonAt(contextapi.ReasonMalformedContribution, contextapi.ReasonProvider, plan.Now, "source revision names another source"))
	}
	if input.ConfigDigest != plan.ConfigDigest {
		return contextapi.Contribution{}, ptrReason(reasonAt(contextapi.ReasonSourceChanged, contextapi.ReasonRuntime, plan.Now, "contribution config digest is stale"))
	}
	if input.ProfileRevision != plan.Profile.Revision {
		return contextapi.Contribution{}, ptrReason(reasonAt(contextapi.ReasonProfileChanged, contextapi.ReasonRuntime, plan.Now, "contribution profile revision is stale"))
	}
	expectedContent := contentIdentity(input.Body)
	if input.Content.ID == "" {
		input.Content.ID = expectedContent.ID
	} else if input.Content.ID != expectedContent.ID {
		return contextapi.Contribution{}, ptrReason(reasonAt(contextapi.ReasonMalformedContribution, contextapi.ReasonProvider, plan.Now, "content identity does not match complete body"))
	}
	if input.Content.UTF8Bytes == 0 {
		input.Content.UTF8Bytes = uint64(len(input.Body))
	}
	if input.Content.UTF8Bytes != uint64(len(input.Body)) {
		return contextapi.Contribution{}, ptrReason(reasonAt(contextapi.ReasonMalformedContribution, contextapi.ReasonProvider, plan.Now, "content byte length does not match complete body"))
	}
	return cloneContribution(input), nil
}

func profileExpired(profile contextapi.ProfileSnapshot, now time.Time) bool {
	return !profile.ValidUntil.IsZero() && !now.IsZero() && !now.Before(profile.ValidUntil)
}

func profileDeadlinePassed(deadline, now time.Time) bool {
	return !deadline.IsZero() && !now.IsZero() && !now.Before(deadline)
}

func profileAudienceMatches(profile contextapi.ProfileSnapshot, audience contextapi.Audience) bool {
	return (profile.Audience.ID == "" && profile.Audience.Epoch == 0) || sameAudience(profile.Audience, audience)
}

func offerExpired(at, now time.Time, maxAgeMs uint64) bool {
	return !at.IsZero() && !now.IsZero() && ageExceeded(at, now, maxAgeMs)
}

func ageExceeded(start, now time.Time, maxMs uint64) bool {
	if start.IsZero() || now.IsZero() || now.Before(start) {
		return false
	}
	return now.Sub(start) >= time.Duration(maxMs)*time.Millisecond
}

func sameScopeRoot(a, b contextapi.ScopeIdentity) bool {
	return a.ID == b.ID && a.Authority == b.Authority && a.CanonicalRoot == b.CanonicalRoot
}

func sameAudience(a, b contextapi.Audience) bool {
	return a.ID == b.ID && a.Epoch == b.Epoch
}

func sameContent(a, b contextapi.ContentIdentity) bool {
	return a.ID == b.ID && a.UTF8Bytes == b.UTF8Bytes
}

func contentIdentity(body string) contextapi.ContentIdentity {
	digest := sha256.Sum256([]byte(body))
	return contextapi.ContentIdentity{ID: contextapi.ContentID("sha256:" + hex.EncodeToString(digest[:])), UTF8Bytes: uint64(len(body))}
}

func contributionItemIdentity(c contextapi.Contribution, mode contextapi.DeliveryMode) contextapi.OfferItemIdentity {
	return contextapi.OfferItemIdentity{Slot: c.Slot, Source: c.Source.Identity, SourceRevision: c.SourceRevision, Content: c.Content, Mode: mode}
}

func makeReceiptKey(item contextapi.OfferItemIdentity) receiptKey {
	return receiptKey{slot: item.Slot, source: item.Source, sourceRevision: item.SourceRevision.Revision, content: item.Content.ID, contentBytes: item.Content.UTF8Bytes}
}

func sourceSlot(c contextapi.Contribution) sourceSlotKey {
	return sourceSlotKey{slot: c.Slot, source: c.Source.Identity}
}

type sourceSlotKey struct {
	slot   contextapi.ContributionSlot
	source contextapi.SourceIdentity
}

func sourceSlotFromIdentity(item contextapi.OfferItemIdentity) sourceSlotKey {
	return sourceSlotKey{slot: item.Slot, source: item.Source}
}

func findGroup(groups []pendingGroup, semantic string) int {
	for i := range groups {
		if groups[i].semantic == semantic {
			return i
		}
	}
	return -1
}

func removeSource(groups []pendingGroup, key sourceSlotKey) []pendingGroup {
	for i := range groups {
		items := groups[i].items[:0]
		for _, item := range groups[i].items {
			if sourceSlot(item.contribution) != key {
				items = append(items, item)
			}
		}
		groups[i].items = items
	}
	return removeEmptyGroups(groups)
}

func containsPending(groups []pendingGroup, c contextapi.Contribution) bool {
	want := contributionItemIdentity(c, contextapi.DeliveryFullBody)
	for _, group := range groups {
		for _, item := range group.items {
			if sameOfferItem(contributionItemIdentity(item.contribution, contextapi.DeliveryFullBody), want) {
				return true
			}
		}
	}
	return false
}

func removeExactItems(groups []pendingGroup, want []contextapi.OfferItemIdentity) []pendingGroup {
	for i := range groups {
		items := groups[i].items[:0]
		for _, item := range groups[i].items {
			identity := contributionItemIdentity(item.contribution, contextapi.DeliveryFullBody)
			matched := false
			for _, candidate := range want {
				if sameOfferItem(identity, candidate) {
					matched = true
					break
				}
			}
			if !matched {
				items = append(items, item)
			}
		}
		groups[i].items = items
	}
	return removeEmptyGroups(groups)
}

func removePendingItem(groups []pendingGroup, want contextapi.OfferItemIdentity) ([]pendingGroup, uint32) {
	var removed uint32
	for i := range groups {
		items := groups[i].items[:0]
		for _, item := range groups[i].items {
			if sameOfferItem(contributionItemIdentity(item.contribution, contextapi.DeliveryFullBody), want) {
				removed++
				continue
			}
			items = append(items, item)
		}
		groups[i].items = items
	}
	return removeEmptyGroups(groups), removed
}

func offerContainsItem(items []contextapi.OfferItemIdentity, want contextapi.OfferItemIdentity) bool {
	for _, item := range items {
		if sameOfferItem(item, want) {
			return true
		}
	}
	return false
}

func validWithdrawalIdentity(item contextapi.OfferItemIdentity) bool {
	return item.Mode == contextapi.DeliveryFullBody &&
		item.Slot.Provider != "" && item.Slot.Key != "" &&
		item.Source.Provider != "" && item.Source.ID != "" &&
		item.SourceRevision.Source == item.Source && item.SourceRevision.Revision != "" &&
		item.Content.ID != "" && item.Content.UTF8Bytes != 0
}

func receiptItems(items []contextapi.OfferItemIdentity) []contextapi.OfferItemIdentity {
	result := make([]contextapi.OfferItemIdentity, 0, len(items))
	for _, item := range items {
		if item.Mode == contextapi.DeliveryFullBody {
			result = append(result, cloneOfferItemIdentity(item))
		}
	}
	return result
}

func removeEmptyGroups(groups []pendingGroup) []pendingGroup {
	result := groups[:0]
	for _, group := range groups {
		if len(group.items) != 0 {
			result = append(result, group)
		}
	}
	return result
}

func pendingBytes(groups []pendingGroup, maxItemBytes uint64) (uint64, bool) {
	var total uint64
	for _, group := range groups {
		bytes := uint64(len(renderGroup(group)))
		if bytes > maxItemBytes {
			return 0, true
		}
		if bytes > ^uint64(0)-total {
			return 0, true
		}
		total += bytes
	}
	return total, false
}
func trimNewItems(groups []pendingGroup, newItems map[contextapi.OfferItemIdentity]struct{}, maxItems uint32, at time.Time) ([]pendingGroup, []contextapi.Reason) {
	var reasons []contextapi.Reason
	for uint32(pendingItemCount(groups)) > maxItems {
		found := false
		for groupIndex := len(groups) - 1; groupIndex >= 0 && !found; groupIndex-- {
			for itemIndex := len(groups[groupIndex].items) - 1; itemIndex >= 0; itemIndex-- {
				item := groups[groupIndex].items[itemIndex]
				identity := contributionItemIdentity(item.contribution, contextapi.DeliveryFullBody)
				if _, ok := newItems[identity]; !ok {
					continue
				}
				updated, removed := removePendingItem(groups, identity)
				if removed == 0 {
					continue
				}
				groups = updated
				delete(newItems, identity)
				reason := reasonAt(contextapi.ReasonQueueFull, contextapi.ReasonProvider, at, "new contribution exceeded the pending-item bound; contribution was dropped")
				reason.Provider = item.contribution.Contributor
				reasons = append(reasons, reason)
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	return groups, reasons
}
func trimNewItemsByBytes(groups []pendingGroup, newItems map[contextapi.OfferItemIdentity]struct{}, maxBytes uint64, at time.Time) ([]pendingGroup, []contextapi.Reason) {
	var reasons []contextapi.Reason
	for {
		total, overflow := pendingBytes(groups, ^uint64(0))
		if !overflow && total <= maxBytes {
			break
		}
		found := false
		for groupIndex := len(groups) - 1; groupIndex >= 0 && !found; groupIndex-- {
			for itemIndex := len(groups[groupIndex].items) - 1; itemIndex >= 0; itemIndex-- {
				item := groups[groupIndex].items[itemIndex]
				identity := contributionItemIdentity(item.contribution, contextapi.DeliveryFullBody)
				if _, ok := newItems[identity]; !ok {
					continue
				}
				updated, removed := removePendingItem(groups, identity)
				if removed == 0 {
					continue
				}
				groups = updated
				delete(newItems, identity)
				reason := reasonAt(contextapi.ReasonQueueFull, contextapi.ReasonProvider, at, "new contribution exceeded the pending-byte bound; contribution was dropped")
				reason.Provider = item.contribution.Contributor
				reasons = append(reasons, reason)
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	return groups, reasons
}
func isolateOversizedItems(groups []pendingGroup, maxItemBytes uint64, at time.Time) ([]pendingGroup, []contextapi.Reason) {
	result := make([]pendingGroup, 0, len(groups))
	var reasons []contextapi.Reason
	for _, group := range groups {
		group.items = append([]pendingItem(nil), group.items...)
		for len(group.items) > 0 && uint64(len(renderGroup(group))) > maxItemBytes {
			item := group.items[len(group.items)-1]
			group.items = group.items[:len(group.items)-1]
			reason := reasonAt(contextapi.ReasonQueueFull, contextapi.ReasonProvider, at, "contribution render exceeded the item bound; contribution was dropped")
			reason.Provider = item.contribution.Contributor
			reasons = append(reasons, reason)
		}
		if len(group.items) > 0 {
			result = append(result, group)
		}
	}
	return result, reasons
}

func pendingItemCount(groups []pendingGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.items)
	}
	return total
}

func groupAlreadyOffered(group pendingGroup, live map[uint64]liveOffer) bool {
	for _, item := range group.items {
		identity := contributionItemIdentity(item.contribution, contextapi.DeliveryFullBody)
		for _, offer := range live {
			for _, offered := range offer.offer.Identity.Items {
				if sameOfferItem(identity, offered) {
					return true
				}
			}
		}
	}
	return false
}

func groupPriority(group pendingGroup) int32 {
	var priority int32
	for i, item := range group.items {
		if i == 0 || item.contribution.Priority > priority {
			priority = item.contribution.Priority
		}
	}
	return priority
}

func renderGroup(group pendingGroup) string {
	if len(group.items) == 0 {
		return ""
	}
	body := group.items[0].contribution.Body
	labels := make([]string, 0, len(group.items))
	for _, item := range group.items {
		label := item.contribution.Source.Label
		if label == "" {
			continue
		}
		found := false
		for _, existing := range labels {
			if existing == label {
				found = true
				break
			}
		}
		if !found {
			labels = append(labels, label)
		}
	}
	if len(labels) == 0 {
		return body
	}
	sort.Strings(labels)
	return "Sources: " + strings.Join(labels, ", ") + "\n" + body
}

func appendUniqueSource(keys []sourceSlotKey, key sourceSlotKey) []sourceSlotKey {
	for _, existing := range keys {
		if existing == key {
			return keys
		}
	}
	return append(keys, key)
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func decision(state contextapi.DeliveryState, reasons ...contextapi.Reason) contextapi.DeliveryDecision {
	return contextapi.DeliveryDecision{State: state, Reasons: cloneReasons(reasons)}
}

func confirmationRejected(reason contextapi.Reason) contextapi.ConfirmationResult {
	return contextapi.ConfirmationResult{State: contextapi.DeliveryRejected, Reasons: []contextapi.Reason{cloneReason(reason)}}
}

func withdrawalRejected(reason contextapi.Reason) contextapi.WithdrawalResult {
	return contextapi.WithdrawalResult{Reasons: []contextapi.Reason{cloneReason(reason)}}
}

func globalAdmissionFailure(pending []pendingGroup, live map[uint64]liveOffer, receipts map[receiptKey]time.Time, completed map[uint64]completedOffer, receiptDelta uint32, at time.Time, admission contextapi.GlobalAdmission) *contextapi.Reason {
	pendingItems := uint32(pendingItemCount(pending))
	retainedBytes := retainedBytesFor(pending, live, receipts, completed)
	outstandingOffers := uint32(len(live))
	receiptCount := uint32(len(receipts))
	params := []contextapi.ReasonParameter{
		{Key: "pendingItems", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: int64(pendingItems)}},
		{Key: "retainedBytes", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueBytes, Bytes: retainedBytes}},
		{Key: "outstandingOffers", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: int64(outstandingOffers)}},
		{Key: "receipts", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: int64(receiptCount)}},
		{Key: "receiptDelta", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: int64(receiptDelta)}},
	}
	var code contextapi.ReasonCode
	var summary string
	switch {
	case pendingItems > admission.MaxPendingItems:
		code = contextapi.ReasonQueueFull
		summary = "global pending-item grant would be exceeded by the target engine total"
	case retainedBytes > admission.MaxPendingMemoryBytes:
		code = contextapi.ReasonQueueFull
		summary = "global retained-memory grant would be exceeded by the target engine total"
	case outstandingOffers > admission.MaxOutstandingOffers:
		code = contextapi.ReasonNoRoom
		summary = "global outstanding-offer grant would be exceeded by the target engine total"
	case receiptCount > admission.MaxReceipts:
		code = contextapi.ReasonReceiptLimit
		summary = "global receipt grant would be exceeded by the target engine total"
	default:
		return nil
	}
	reason := reasonAt(code, contextapi.ReasonRuntime, at, summary)
	reason.Params = params
	return &reason
}

func reasonAt(code contextapi.ReasonCode, origin contextapi.ReasonOrigin, at time.Time, summary string) contextapi.Reason {
	return contextapi.Reason{Code: code, Origin: origin, Summary: summary, At: at}
}

func ptrReason(reason contextapi.Reason) *contextapi.Reason {
	return &reason
}

func sameOfferItem(a, b contextapi.OfferItemIdentity) bool {
	return a.Slot == b.Slot && a.Source == b.Source && a.SourceRevision == b.SourceRevision && a.Content == b.Content && a.Mode == b.Mode
}

func sameOfferIdentity(a, b contextapi.OfferIdentity) bool {
	if a.Generation != b.Generation || a.ID != b.ID || !sameAudience(a.Audience, b.Audience) || a.OpportunityID != b.OpportunityID || a.Surface != b.Surface || !sameContent(a.Body, b.Body) || len(a.Items) != len(b.Items) {
		return false
	}
	for i := range a.Items {
		if !sameOfferItem(a.Items[i], b.Items[i]) {
			return false
		}
	}
	return true
}

func offerItemIdentities(items []contextapi.OfferItem) []contextapi.OfferItemIdentity {
	result := make([]contextapi.OfferItemIdentity, len(items))
	for i, item := range items {
		result[i] = cloneOfferItemIdentity(item.Identity)
	}
	return result
}

func clonePending(groups []pendingGroup) []pendingGroup {
	result := make([]pendingGroup, len(groups))
	for i, group := range groups {
		result[i] = pendingGroup{semantic: group.semantic, firstSeen: group.firstSeen, items: make([]pendingItem, len(group.items))}
		for j, item := range group.items {
			result[i].items[j] = pendingItem{contribution: cloneContribution(item.contribution), admittedAt: item.admittedAt}
		}
	}
	return result
}

func cloneContribution(input contextapi.Contribution) contextapi.Contribution {
	result := input
	result.Slot.Provider = cloneString(result.Slot.Provider)
	result.Slot.Key = cloneString(result.Slot.Key)
	result.Reasons = cloneReasons(input.Reasons)
	result.Source = cloneSourceRef(input.Source)
	result.SourceRevision = cloneSourceRevision(input.SourceRevision)
	result.Content.ID = cloneString(result.Content.ID)
	result.Body = strings.Clone(result.Body)
	result.Recruitment.Resource = strings.Clone(result.Recruitment.Resource)
	result.Contributor = cloneString(result.Contributor)
	result.ConfigDigest = cloneString(result.ConfigDigest)
	result.ProfileRevision = cloneString(result.ProfileRevision)
	return result
}

func cloneSourceRef(input contextapi.SourceRef) contextapi.SourceRef {
	return contextapi.SourceRef{
		Identity: cloneSourceIdentity(input.Identity),
		Kind:     input.Kind,
		Path:     strings.Clone(input.Path),
		Label:    strings.Clone(input.Label),
	}
}

func cloneSourceIdentity(input contextapi.SourceIdentity) contextapi.SourceIdentity {
	return contextapi.SourceIdentity{Provider: cloneString(input.Provider), ID: cloneString(input.ID)}
}

func cloneSourceRevision(input contextapi.SourceRevision) contextapi.SourceRevision {
	return contextapi.SourceRevision{Source: cloneSourceIdentity(input.Source), Revision: cloneString(input.Revision)}
}

func cloneString[T ~string](input T) T {
	return T(strings.Clone(string(input)))
}

func cloneOffer(input contextapi.Offer) contextapi.Offer {
	result := input
	result.Body = strings.Clone(result.Body)
	result.Identity = cloneOfferIdentity(input.Identity)
	result.Items = make([]contextapi.OfferItem, len(input.Items))
	for i, item := range input.Items {
		result.Items[i] = item
		result.Items[i].Identity = cloneOfferItemIdentity(item.Identity)
		result.Items[i].Source = cloneSourceRef(item.Source)
		result.Items[i].Recruitment.Resource = strings.Clone(item.Recruitment.Resource)
		result.Items[i].Body = strings.Clone(item.Body)
	}
	return result
}

func cloneOfferIdentity(input contextapi.OfferIdentity) contextapi.OfferIdentity {
	result := input
	result.Audience = cloneAudience(input.Audience)
	result.OpportunityID = strings.Clone(input.OpportunityID)
	result.Surface = cloneString(input.Surface)
	result.Body.ID = cloneString(result.Body.ID)
	result.Items = make([]contextapi.OfferItemIdentity, len(input.Items))
	for i, item := range input.Items {
		result.Items[i] = cloneOfferItemIdentity(item)
	}
	return result
}

func cloneOfferItemIdentity(input contextapi.OfferItemIdentity) contextapi.OfferItemIdentity {
	result := input
	result.Slot.Provider = cloneString(result.Slot.Provider)
	result.Slot.Key = cloneString(result.Slot.Key)
	result.Source = cloneSourceIdentity(result.Source)
	result.SourceRevision = cloneSourceRevision(result.SourceRevision)
	result.Content.ID = cloneString(result.Content.ID)
	result.Mode = cloneString(result.Mode)
	return result
}

func cloneAudience(input contextapi.Audience) contextapi.Audience {
	input.ID = cloneString(input.ID)
	input.Continuity = cloneString(input.Continuity)
	return input
}

func cloneReceipts(input []contextapi.Receipt) []contextapi.Receipt {
	result := make([]contextapi.Receipt, len(input))
	copy(result, input)
	for i := range result {
		result[i].Item = cloneOfferItemIdentity(result[i].Item)
	}
	return result
}

func cloneReason(input contextapi.Reason) contextapi.Reason {
	result := input
	result.Code = cloneString(result.Code)
	result.Origin = cloneString(result.Origin)
	result.Provider = cloneString(result.Provider)
	result.Rule = strings.Clone(result.Rule)
	result.Summary = strings.Clone(result.Summary)
	result.Params = make([]contextapi.ReasonParameter, len(input.Params))
	copy(result.Params, input.Params)
	for i := range result.Params {
		result.Params[i].Key = strings.Clone(result.Params[i].Key)
		result.Params[i].Value.Kind = cloneString(result.Params[i].Value.Kind)
		result.Params[i].Value.Text = strings.Clone(result.Params[i].Value.Text)
	}
	result.Evidence = make([]contextapi.EvidenceID, len(input.Evidence))
	copy(result.Evidence, input.Evidence)
	return result
}

func cloneReasons(input []contextapi.Reason) []contextapi.Reason {
	if input == nil {
		return nil
	}
	result := make([]contextapi.Reason, len(input))
	for i, reason := range input {
		result[i] = cloneReason(reason)
	}
	return result
}

func (e *Engine) retainedBytesLocked() uint64 {
	return retainedBytesFor(e.pending, e.live, e.receipts, e.completed)
}

func retainedBytesFor(pending []pendingGroup, live map[uint64]liveOffer, receipts map[receiptKey]time.Time, completed map[uint64]completedOffer) uint64 {
	var total uint64
	for _, group := range pending {
		addRetained(&total, uint64(len(group.semantic))+32)
		for _, item := range group.items {
			addRetained(&total, contributionRetainedBytes(item.contribution)+32)
		}
	}
	for _, offer := range live {
		addRetained(&total, offerRetainedBytes(offer.offer)+64)
	}
	for key := range receipts {
		addRetained(&total, receiptKeyRetainedBytes(key)+32)
	}
	for _, completed := range completed {
		addRetained(&total, offerIdentityRetainedBytes(completed.identity)+64)
		for _, receipt := range completed.receipts {
			addRetained(&total, offerItemIdentityRetainedBytes(receipt.Item)+32)
		}
	}
	return total
}

func cloneLive(input map[uint64]liveOffer) map[uint64]liveOffer {
	result := make(map[uint64]liveOffer, len(input))
	for id, offer := range input {
		result[id] = liveOffer{
			offer:             cloneOffer(offer.offer),
			profileRevision:   cloneString(offer.profileRevision),
			profileValidUntil: offer.profileValidUntil,
			configDigest:      cloneString(offer.configDigest),
			offeredAt:         offer.offeredAt,
		}
	}
	return result
}

func revokeSourceOffers(live map[uint64]liveOffer, key sourceSlotKey) {
	for id, offer := range live {
		for _, item := range offer.offer.Identity.Items {
			if sourceSlotFromIdentity(item) == key {
				delete(live, id)
				break
			}
		}
	}
}

func cloneReceiptMap(input map[receiptKey]time.Time) map[receiptKey]time.Time {
	result := make(map[receiptKey]time.Time, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneCompleted(input map[uint64]completedOffer) map[uint64]completedOffer {
	result := make(map[uint64]completedOffer, len(input))
	for id, offer := range input {
		result[id] = completedOffer{
			identity:          cloneOfferIdentity(offer.identity),
			profileRevision:   cloneString(offer.profileRevision),
			profileValidUntil: offer.profileValidUntil,
			receipts:          cloneReceipts(offer.receipts),
		}
	}
	return result
}

func trimCompleted(input map[uint64]completedOffer, max uint32) {
	for uint32(len(input)) > max {
		var oldest uint64
		for id := range input {
			if oldest == 0 || id < oldest {
				oldest = id
			}
		}
		delete(input, oldest)
	}
}

func addRetained(total *uint64, amount uint64) {
	if amount > ^uint64(0)-*total {
		*total = ^uint64(0)
		return
	}
	*total += amount
}

func contributionRetainedBytes(input contextapi.Contribution) uint64 {
	var total uint64
	addRetained(&total, uint64(len(input.Body)))
	addRetained(&total, uint64(len(input.Slot.Provider)+len(input.Slot.Key)))
	addRetained(&total, sourceIdentityRetainedBytes(input.Source.Identity))
	addRetained(&total, uint64(len(input.Source.Path)+len(input.Source.Label)))
	addRetained(&total, uint64(len(input.SourceRevision.Revision)+len(input.Recruitment.Resource)))
	addRetained(&total, uint64(len(input.Content.ID)+len(input.Contributor)+len(input.ConfigDigest)+len(input.ProfileRevision)))
	for _, reason := range input.Reasons {
		addRetained(&total, uint64(len(reason.Provider)+len(reason.Rule)+len(reason.Summary))+32)
		for _, parameter := range reason.Params {
			addRetained(&total, uint64(len(parameter.Key)+len(parameter.Value.Text))+24)
		}
	}
	return total
}

func sourceIdentityRetainedBytes(input contextapi.SourceIdentity) uint64 {
	return uint64(len(input.Provider) + len(input.ID))
}

func offerRetainedBytes(input contextapi.Offer) uint64 {
	var total uint64
	addRetained(&total, uint64(len(input.Body))+offerIdentityRetainedBytes(input.Identity))
	for _, item := range input.Items {
		addRetained(&total, uint64(len(item.Body)+len(item.Source.Path)+len(item.Source.Label)+len(item.Recruitment.Resource))+offerItemIdentityRetainedBytes(item.Identity))
	}
	return total
}

func offerIdentityRetainedBytes(input contextapi.OfferIdentity) uint64 {
	var total uint64
	addRetained(&total, uint64(len(input.Audience.ID)+len(input.OpportunityID)+len(input.Surface)+len(input.Body.ID)))
	for _, item := range input.Items {
		addRetained(&total, offerItemIdentityRetainedBytes(item))
	}
	return total
}

func offerItemIdentityRetainedBytes(input contextapi.OfferItemIdentity) uint64 {
	return uint64(len(input.Slot.Provider)+len(input.Slot.Key)) + sourceIdentityRetainedBytes(input.Source) + sourceIdentityRetainedBytes(input.SourceRevision.Source) + uint64(len(input.SourceRevision.Revision)+len(input.Content.ID))
}

func receiptKeyRetainedBytes(input receiptKey) uint64 {
	return uint64(len(input.slot.Provider)+len(input.slot.Key)) + sourceIdentityRetainedBytes(input.source) + uint64(len(input.sourceRevision)+len(input.content))
}
