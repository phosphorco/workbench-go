package contextengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextconfig"
)

func testEngine(t *testing.T) (*Engine, contextapi.ScopeIdentity, contextapi.Audience, time.Time) {
	t.Helper()
	scope := contextapi.ScopeIdentity{
		ID:            "scope:test",
		Authority:     contextapi.ScopeAuthorityProject,
		CanonicalRoot: "/repo",
		ConfigDigest:  "config:test",
	}
	audience := contextapi.Audience{ID: "session:test", Epoch: 1, Continuity: contextapi.ContinuityKnown}
	partition := contextapi.DeliveryPartition{Scope: scope, Audience: audience, Generation: 1, ConfigDigest: scope.ConfigDigest}
	limits := contextapi.EngineLimits{
		Queue:            contextapi.QueueLimits{MaxItemBytes: 4096, MaxPendingBytes: 8192, MaxPendingItems: 8, MaxAgeMs: 1000},
		MaxLiveOffers:    4,
		MaxReceipts:      8,
		MaxOfferAgeMs:    500,
		MaxRetainedBytes: 16384,
	}
	engine, err := NewEngine(partition, limits)
	if err != nil {
		t.Fatal(err)
	}
	return engine, scope, audience, time.UnixMilli(1000)
}

func testContribution(body, sourceID, revision string) contextapi.Contribution {
	digest := sha256.Sum256([]byte(body))
	source := contextapi.SourceIdentity{Provider: "provider:test", ID: contextapi.SourceID(sourceID)}
	return contextapi.Contribution{
		Slot:            contextapi.ContributionSlot{Provider: "provider:test", Key: "guidance"},
		Source:          contextapi.SourceRef{Identity: source, Kind: contextapi.SourceFile, Path: "ai-context.md"},
		SourceRevision:  contextapi.SourceRevision{Source: source, Revision: contextapi.SourceRevisionID(revision)},
		Content:         contextapi.ContentIdentity{ID: contextapi.ContentID("sha256:" + hex.EncodeToString(digest[:])), UTF8Bytes: uint64(len(body))},
		Body:            body,
		Recruitment:     contextapi.SourceRecruitment{Kind: contextapi.RecruitmentObservedFile, Resource: "src/main.go"},
		Contributor:     "provider:test",
		ConfigDigest:    "config:test",
		ProfileRevision: "profile:test",
	}
}

func testPlan(scope contextapi.ScopeIdentity, audience contextapi.Audience, now time.Time, contributions ...contextapi.Contribution) contextapi.DeliveryPlanInput {
	return contextapi.DeliveryPlanInput{
		Now:           now,
		Active:        true,
		Scope:         scope,
		ConfigDigest:  scope.ConfigDigest,
		Audience:      audience,
		Generation:    1,
		Profile:       contextapi.ProfileSnapshot{Revision: "profile:test", Audience: audience},
		Contributions: contributions,
		Opportunity: contextapi.DeliveryOpportunity{
			ID:        "hook:1",
			Surface:   contextapi.DeliveryClaudeContext,
			Available: true,
			MaxBytes:  4096,
			MaxItems:  8,
		},
	}
}

func testConfirmation(scope contextapi.ScopeIdentity, audience contextapi.Audience, offer contextapi.Offer, at time.Time) contextapi.OfferConfirmation {
	return contextapi.OfferConfirmation{
		Identity:        offer.Identity,
		Handoff:         contextapi.HandoffOutcome{State: contextapi.HandoffConfirmed, EmittedContent: offer.Identity.Body},
		ConfirmedAt:     at,
		Active:          true,
		Scope:           scope,
		Audience:        audience,
		ConfigDigest:    scope.ConfigDigest,
		ProfileRevision: "profile:test",
	}
}

func testWithdrawal(scope contextapi.ScopeIdentity, audience contextapi.Audience, item contextapi.OfferItemIdentity, now time.Time) contextapi.DeliveryWithdrawal {
	return contextapi.DeliveryWithdrawal{
		Now:          now,
		Scope:        scope,
		Audience:     audience,
		Generation:   1,
		ConfigDigest: scope.ConfigDigest,
		Item:         item,
	}
}

func testAdmission() contextapi.GlobalAdmission {
	return contextapi.GlobalAdmission{
		MaxPendingItems:       1024,
		MaxPendingMemoryBytes: 1 << 30,
		MaxOutstandingOffers:  1024,
		MaxReceipts:           1024,
	}
}

func testPlanForEngine(engine *Engine, input contextapi.DeliveryPlanInput) contextapi.DeliveryDecision {
	return engine.Plan(input, testAdmission())
}

func testConfirmForEngine(engine *Engine, input contextapi.OfferConfirmation) contextapi.ConfirmationResult {
	return engine.Confirm(input, testAdmission())
}

func admissionForStats(stats Stats) contextapi.GlobalAdmission {
	return contextapi.GlobalAdmission{
		MaxPendingItems:       stats.PendingItems,
		MaxPendingMemoryBytes: stats.RetainedBytes,
		MaxOutstandingOffers:  stats.LiveOffers,
		MaxReceipts:           stats.Receipts,
	}
}

func TestNewEngineRequiresPartitionAndBoundedDurations(t *testing.T) {
	_, scope, audience, _ := testEngine(t)
	partition := contextapi.DeliveryPartition{Scope: scope, Audience: audience, Generation: 1, ConfigDigest: scope.ConfigDigest}
	valid := contextapi.EngineLimits{Queue: contextapi.QueueLimits{MaxItemBytes: 1, MaxPendingBytes: 1, MaxPendingItems: 1, MaxAgeMs: 1}, MaxLiveOffers: 1, MaxReceipts: 1, MaxOfferAgeMs: 1, MaxRetainedBytes: 1}
	if _, err := NewEngine(partition, valid); err != nil {
		t.Fatalf("valid partition rejected: %v", err)
	}
	badPartition := partition
	badPartition.Audience.Epoch = 0
	if _, err := NewEngine(badPartition, valid); err == nil {
		t.Fatal("zero epoch accepted")
	}
	badLimits := valid
	badLimits.MaxOfferAgeMs = ^uint64(0)
	if _, err := NewEngine(partition, badLimits); err == nil {
		t.Fatal("overflowing duration accepted")
	}
}

func TestLoadedDeclarationAndEffectiveDigestsReachEngine(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, ".workbench", "context.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"schemaVersion":1,"optIn":true,"includeChildren":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	loaded, err := contextconfig.Load(contextconfig.LoadOptions{WorkingDirectory: root, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("loaded activation = %#v", loaded.Result)
	}
	effective := loaded.Result.Effective
	if effective.Scope.ConfigDigest == effective.ConfigDigest {
		t.Fatalf("loader collapsed declaration and effective digests: %#v", effective)
	}
	audience := contextapi.Audience{ID: "session:loaded", Epoch: 1, Continuity: contextapi.ContinuityKnown}
	partition := contextapi.DeliveryPartition{
		Scope:        effective.Scope,
		Audience:     audience,
		Generation:   1,
		ConfigDigest: effective.ConfigDigest,
	}
	engine, err := NewEngine(partition, effective.Delivery)
	if err != nil {
		t.Fatalf("NewEngine rejected Load output: %v", err)
	}

	contribution := testContribution("loaded guidance", "source-loaded", "rev-loaded")
	contribution.ConfigDigest = effective.ConfigDigest
	plan := testPlan(effective.Scope, audience, now, contribution)
	plan.ConfigDigest = effective.ConfigDigest
	plan.Contributions[0].ConfigDigest = effective.ConfigDigest
	planned := testPlanForEngine(engine, plan)
	if planned.State != contextapi.DeliveryOffered {
		t.Fatalf("Plan with Load output = %#v", planned)
	}
	confirmation := testConfirmation(effective.Scope, audience, planned.Offer, now.Add(time.Millisecond))
	confirmation.ConfigDigest = effective.ConfigDigest
	confirmed := testConfirmForEngine(engine, confirmation)
	if confirmed.State != contextapi.DeliveryConfirmed {
		t.Fatalf("Confirm with Load output = %#v", confirmed)
	}
}

func TestPlanConfirmAndAcceptedReplay(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	contribution := testContribution("complete guidance", "source-a", "rev-1")
	planned := testPlanForEngine(engine, testPlan(scope, audience, now, contribution))
	if planned.State != contextapi.DeliveryOffered {
		t.Fatalf("Plan state = %s, reasons = %#v", planned.State, planned.Reasons)
	}
	if planned.Offer.Body != contribution.Body {
		t.Fatalf("offer body was clipped or rewritten: %q", planned.Offer.Body)
	}
	confirmed := testConfirmation(scope, audience, planned.Offer, now.Add(10*time.Millisecond))
	result := testConfirmForEngine(engine, confirmed)
	if result.State != contextapi.DeliveryConfirmed || len(result.Receipts) != 1 {
		t.Fatalf("Confirm = %#v", result)
	}
	replayed := testConfirmForEngine(engine, confirmed)
	if replayed.State != contextapi.DeliveryConfirmed || len(replayed.Receipts) != 1 {
		t.Fatalf("accepted replay = %#v", replayed)
	}
	suppressed := testPlanForEngine(engine, testPlan(scope, audience, now.Add(20*time.Millisecond), contribution))
	if suppressed.State != contextapi.DeliverySuppressed {
		t.Fatalf("repeat state = %s, reasons = %#v", suppressed.State, suppressed.Reasons)
	}
	stats := engine.Stats()
	if stats.PendingItems != 0 || stats.Receipts != 1 || stats.RetainedBytes == 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestPlanUsesExactGlobalTotalsAndRejectsAtomically(t *testing.T) {
	probe, scope, audience, now := testEngine(t)
	input := testPlan(scope, audience, now, testContribution("global boundary", "source-a", "rev-a"))
	if decision := testPlanForEngine(probe, input); decision.State != contextapi.DeliveryOffered {
		t.Fatal(decision.Reasons)
	}
	wanted := probe.Stats()
	exact := admissionForStats(wanted)

	target, _, _, _ := testEngine(t)
	if decision := target.Plan(input, exact); decision.State != contextapi.DeliveryOffered {
		t.Fatalf("exact target grant rejected: %#v", decision)
	}

	tests := []struct {
		name   string
		mutate func(*contextapi.GlobalAdmission)
	}{
		{name: "pending items", mutate: func(grant *contextapi.GlobalAdmission) { grant.MaxPendingItems-- }},
		{name: "retained metadata and bodies", mutate: func(grant *contextapi.GlobalAdmission) { grant.MaxPendingMemoryBytes-- }},
		{name: "outstanding offers", mutate: func(grant *contextapi.GlobalAdmission) { grant.MaxOutstandingOffers-- }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fresh, _, _, _ := testEngine(t)
			below := exact
			test.mutate(&below)
			decision := fresh.Plan(input, below)
			if decision.State == contextapi.DeliveryOffered {
				t.Fatalf("below-bound grant offered: %#v", decision)
			}
			if stats := fresh.Stats(); stats.PendingItems > below.MaxPendingItems || stats.RetainedBytes > below.MaxPendingMemoryBytes || stats.LiveOffers > below.MaxOutstandingOffers || stats.Receipts > below.MaxReceipts {
				t.Fatalf("deferred Plan exceeded its grant: stats=%#v grant=%#v", stats, below)
			}
		})
	}
}

func TestPlanGlobalResidualCoversOtherAudiences(t *testing.T) {
	first, scope, audience, now := testEngine(t)
	second, secondScope, secondAudience, _ := testEngine(t)
	secondAudience.ID = "session:two"
	second.partition.Audience = secondAudience
	firstDecision := first.Plan(testPlan(scope, audience, now, testContribution("audience-one", "source-a", "rev-a")), testAdmission())
	if firstDecision.State != contextapi.DeliveryOffered {
		t.Fatal(firstDecision.Reasons)
	}
	used := first.Stats().RetainedBytes
	const globalLimit uint64 = 1500
	if used >= globalLimit {
		t.Fatalf("single target fixture already exceeds global test limit: %d", used)
	}
	grant := testAdmission()
	grant.MaxPendingMemoryBytes = globalLimit - used
	secondDecision := second.Plan(testPlan(secondScope, secondAudience, now, testContribution("audience-two", "source-b", "rev-b")), grant)
	if secondDecision.State == contextapi.DeliveryOffered {
		t.Fatalf("second audience exceeded residual global memory grant: %#v", secondDecision)
	}
	if total := used + second.Stats().RetainedBytes; total > globalLimit {
		t.Fatalf("global retained memory exceeded: %d > %d", total, globalLimit)
	}
}

func TestConfirmUsesExactReplayAndReceiptBoundaries(t *testing.T) {
	probe, scope, audience, now := testEngine(t)
	input := testPlan(scope, audience, now, testContribution("confirm boundary", "source-a", "rev-a"))
	planned := testPlanForEngine(probe, input)
	if planned.State != contextapi.DeliveryOffered {
		t.Fatal(planned.Reasons)
	}
	if result := testConfirmForEngine(probe, testConfirmation(scope, audience, planned.Offer, now.Add(time.Millisecond))); result.State != contextapi.DeliveryConfirmed {
		t.Fatal(result)
	}
	wanted := probe.Stats()
	if wanted.Receipts != 1 || wanted.RetainedBytes == 0 {
		t.Fatalf("probe did not retain expected confirmation metadata: %#v", wanted)
	}

	target, _, _, _ := testEngine(t)
	targetPlan := testPlan(scope, audience, now, testContribution("confirm boundary", "source-a", "rev-a"))
	targetOffer := target.Plan(targetPlan, testAdmission())
	if targetOffer.State != contextapi.DeliveryOffered {
		t.Fatal(targetOffer.Reasons)
	}
	confirmation := testConfirmation(scope, audience, targetOffer.Offer, now.Add(time.Millisecond))
	if result := target.Confirm(confirmation, admissionForStats(wanted)); result.State != contextapi.DeliveryConfirmed {
		t.Fatalf("exact post-confirm grant rejected: %#v", result)
	}

	tests := []struct {
		name   string
		mutate func(*contextapi.GlobalAdmission)
	}{
		{name: "receipt delta", mutate: func(grant *contextapi.GlobalAdmission) { grant.MaxReceipts-- }},
		{name: "replay metadata", mutate: func(grant *contextapi.GlobalAdmission) { grant.MaxPendingMemoryBytes-- }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fresh, _, _, _ := testEngine(t)
			freshOffer := fresh.Plan(testPlan(scope, audience, now, testContribution("confirm boundary", "source-a", "rev-a")), testAdmission())
			if freshOffer.State != contextapi.DeliveryOffered {
				t.Fatal(freshOffer.Reasons)
			}
			before := fresh.Stats()
			below := admissionForStats(wanted)
			test.mutate(&below)
			result := fresh.Confirm(testConfirmation(scope, audience, freshOffer.Offer, now.Add(time.Millisecond)), below)
			if result.State == contextapi.DeliveryConfirmed {
				t.Fatalf("below-bound Confirm accepted: %#v", result)
			}
			if after := fresh.Stats(); after != before {
				t.Fatalf("rejected Confirm mutated state: before=%#v after=%#v", before, after)
			}
			if test.name == "receipt delta" {
				if len(result.Reasons) == 0 || !hasReasonNumber(result.Reasons[0], "receiptDelta", 1) {
					t.Fatalf("receipt delta evidence missing: %#v", result.Reasons)
				}
			}
		})
	}
}

func hasReasonNumber(reason contextapi.Reason, key string, want int64) bool {
	for _, parameter := range reason.Params {
		if parameter.Key == key && parameter.Value.Kind == contextapi.ReasonValueNumber && parameter.Value.Number == want {
			return true
		}
	}
	return false
}

func TestEqualContentCoalescesButReceiptsRemainPerSource(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	a := testContribution("same guidance", "source-a", "rev-a")
	b := testContribution("same guidance", "source-b", "rev-b")
	planned := testPlanForEngine(engine, testPlan(scope, audience, now, a, b))
	if planned.State != contextapi.DeliveryOffered || len(planned.Offer.Items) != 2 {
		t.Fatalf("coalesced offer = %#v", planned)
	}
	if planned.Offer.Body != "same guidance" {
		t.Fatalf("equal semantic bodies were duplicated: %q", planned.Offer.Body)
	}
	result := testConfirmForEngine(engine, testConfirmation(scope, audience, planned.Offer, now.Add(10*time.Millisecond)))
	if result.State != contextapi.DeliveryConfirmed || len(result.Receipts) != 2 {
		t.Fatalf("per-source receipts = %#v", result)
	}
}

func TestChangedSourceCannotReplayOrClearReplacement(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	old := testContribution("old guidance", "source-a", "rev-1")
	first := testPlanForEngine(engine, testPlan(scope, audience, now, old))
	if first.State != contextapi.DeliveryOffered {
		t.Fatal(first.Reasons)
	}
	newer := testContribution("new guidance", "source-a", "rev-2")
	second := testPlanForEngine(engine, testPlan(scope, audience, now.Add(1*time.Millisecond), newer))
	if second.State != contextapi.DeliveryOffered || second.Offer.Body != newer.Body {
		t.Fatalf("replacement offer = %#v", second)
	}
	oldResult := testConfirmForEngine(engine, testConfirmation(scope, audience, first.Offer, now.Add(2*time.Millisecond)))
	if oldResult.State == contextapi.DeliveryConfirmed || len(oldResult.Receipts) != 0 {
		t.Fatalf("stale confirmation accepted: %#v", oldResult)
	}
	newResult := testConfirmForEngine(engine, testConfirmation(scope, audience, second.Offer, now.Add(3*time.Millisecond)))
	if newResult.State != contextapi.DeliveryConfirmed || len(newResult.Receipts) != 1 {
		t.Fatalf("replacement confirmation = %#v", newResult)
	}
}

func TestContentIdentityMustMatchChangedBody(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	original := testContribution("same-size", "source-a", "rev-1")
	changed := testContribution("same-text", "source-a", "rev-2")
	changed.Content = original.Content
	decision := testPlanForEngine(engine, testPlan(scope, audience, now, changed))
	if decision.State != contextapi.DeliveryIdle || len(decision.Reasons) == 0 || decision.Reasons[0].Code != contextapi.ReasonMalformedContribution {
		t.Fatalf("changed body with reused digest = %#v", decision)
	}
}

func TestTransitionRevokesOutstandingState(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	planned := testPlanForEngine(engine, testPlan(scope, audience, now, testContribution("reset me", "source-a", "rev-1")))
	if planned.State != contextapi.DeliveryOffered {
		t.Fatal(planned.Reasons)
	}
	newAudience := audience
	newAudience.Epoch++
	transition := testPlan(scope, audience, now.Add(time.Millisecond))
	transition.Transition = contextapi.AudienceTransition{Kind: contextapi.TransitionReset, Previous: audience, Current: newAudience, CausalID: "reset:1", At: transition.Now}
	withdrawn := testPlanForEngine(engine, transition)
	if withdrawn.State != contextapi.DeliveryWithdrawn {
		t.Fatalf("transition state = %#v", withdrawn)
	}
	old := testConfirmForEngine(engine, testConfirmation(scope, audience, planned.Offer, now.Add(2*time.Millisecond)))
	if old.State == contextapi.DeliveryConfirmed || len(old.Receipts) != 0 {
		t.Fatalf("revoked confirmation accepted: %#v", old)
	}
}

func TestForeignTransitionCannotClearPartitionState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contextapi.DeliveryPlanInput)
	}{
		{name: "generation", mutate: func(input *contextapi.DeliveryPlanInput) { input.Generation++ }},
		{name: "scope", mutate: func(input *contextapi.DeliveryPlanInput) {
			input.Scope.CanonicalRoot = "/foreign"
		}},
		{name: "audience", mutate: func(input *contextapi.DeliveryPlanInput) {
			input.Audience.ID = "session:foreign"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, scope, audience, now := testEngine(t)
			planned := testPlanForEngine(engine, testPlan(scope, audience, now, testContribution("transition guard", "source-a", "rev-a")))
			if planned.State != contextapi.DeliveryOffered {
				t.Fatal(planned.Reasons)
			}
			before := engine.Stats()
			foreign := testPlan(scope, audience, now.Add(time.Millisecond))
			foreign.Transition = contextapi.AudienceTransition{Kind: contextapi.TransitionReset, Previous: audience, Current: contextapi.Audience{ID: "session:next", Epoch: audience.Epoch + 1}, At: foreign.Now}
			test.mutate(&foreign)
			result := testPlanForEngine(engine, foreign)
			if result.State != contextapi.DeliveryRejected {
				t.Fatalf("foreign transition result = %#v", result)
			}
			if after := engine.Stats(); after != before {
				t.Fatalf("foreign transition mutated state: before=%#v after=%#v", before, after)
			}
			if confirmed := testConfirmForEngine(engine, testConfirmation(scope, audience, planned.Offer, now.Add(2*time.Millisecond))); confirmed.State != contextapi.DeliveryConfirmed {
				t.Fatalf("valid confirmation after foreign transition = %#v", confirmed)
			}
		})
	}
}

func TestForeignOrMismatchedConfirmationCannotClearLiveOffer(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(contextapi.OfferConfirmation, contextapi.Audience) contextapi.OfferConfirmation
	}{
		{name: "foreign audience with stale config", mutate: func(input contextapi.OfferConfirmation, audience contextapi.Audience) contextapi.OfferConfirmation {
			input.Audience = contextapi.Audience{ID: "session:foreign", Epoch: audience.Epoch}
			input.ConfigDigest = "config:stale"
			return input
		}},
		{name: "wrong body with stale config", mutate: func(input contextapi.OfferConfirmation, _ contextapi.Audience) contextapi.OfferConfirmation {
			input.ConfigDigest = "config:stale"
			input.Identity.Body = contentIdentity("wrong body")
			return input
		}},
		{name: "wrong body with profile change", mutate: func(input contextapi.OfferConfirmation, _ contextapi.Audience) contextapi.OfferConfirmation {
			input.ProfileRevision = "profile:stale"
			input.Identity.Body = contentIdentity("wrong body")
			return input
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, scope, audience, now := testEngine(t)
			planned := testPlanForEngine(engine, testPlan(scope, audience, now, testContribution("confirmation guard", "source-a", "rev-a")))
			if planned.State != contextapi.DeliveryOffered {
				t.Fatal(planned.Reasons)
			}
			before := engine.Stats()
			confirmation := test.mutate(testConfirmation(scope, audience, planned.Offer, now.Add(time.Millisecond)), audience)
			result := testConfirmForEngine(engine, confirmation)
			if result.State != contextapi.DeliveryRejected {
				t.Fatalf("mismatched confirmation result = %#v", result)
			}
			if after := engine.Stats(); after != before {
				t.Fatalf("mismatched confirmation mutated state: before=%#v after=%#v", before, after)
			}
			if confirmed := testConfirmForEngine(engine, testConfirmation(scope, audience, planned.Offer, now.Add(2*time.Millisecond))); confirmed.State != contextapi.DeliveryConfirmed {
				t.Fatalf("valid confirmation after mismatched input = %#v", confirmed)
			}
		})
	}
}

func TestWithdrawStaleIdentityPreservesNewerReplacement(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	old := testContribution("old guidance", "source-a", "rev-1")
	first := testPlanForEngine(engine, testPlan(scope, audience, now, old))
	if first.State != contextapi.DeliveryOffered {
		t.Fatal(first.Reasons)
	}

	newer := testContribution("new guidance", "source-a", "rev-2")
	second := testPlanForEngine(engine, testPlan(scope, audience, now.Add(time.Millisecond), newer))
	if second.State != contextapi.DeliveryOffered {
		t.Fatal(second.Reasons)
	}

	withdrawn := engine.Withdraw(testWithdrawal(scope, audience, first.Offer.Identity.Items[0], now.Add(2*time.Millisecond)))
	if withdrawn.RemovedItems != 0 || withdrawn.RevokedOffers != 0 {
		t.Fatalf("stale withdrawal removed replacement: %#v", withdrawn)
	}
	if len(withdrawn.Reasons) != 1 || withdrawn.Reasons[0].Summary != "provider revalidation found no matching current item; newer replacement and unrelated state were preserved" {
		t.Fatalf("stale withdrawal explanation = %#v", withdrawn.Reasons)
	}
	sources := engine.PendingSources()
	if len(sources) != 1 || !sameOfferItem(sources[0].Identity, second.Offer.Identity.Items[0]) {
		t.Fatalf("replacement pending sources = %#v", sources)
	}
	if confirmed := testConfirmForEngine(engine, testConfirmation(scope, audience, second.Offer, now.Add(3*time.Millisecond))); confirmed.State != contextapi.DeliveryConfirmed {
		t.Fatalf("replacement confirmation = %#v", confirmed)
	}
}

func TestWithdrawCoalescedSourcePreservesUnrelatedPendingAndOffer(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	firstSource := testContribution("same guidance", "source-a", "rev-a")
	secondSource := testContribution("same guidance", "source-b", "rev-b")
	secondSource.Recruitment.Resource = "source-b.md"
	unrelated := testContribution("unrelated guidance", "source-c", "rev-c")

	coalesced := testPlanForEngine(engine, testPlan(scope, audience, now, firstSource, secondSource))
	if coalesced.State != contextapi.DeliveryOffered || len(coalesced.Offer.Items) != 2 {
		t.Fatalf("coalesced offer = %#v", coalesced)
	}
	unrelatedOffer := testPlanForEngine(engine, testPlan(scope, audience, now.Add(time.Millisecond), unrelated))
	if unrelatedOffer.State != contextapi.DeliveryOffered || len(unrelatedOffer.Offer.Items) != 1 {
		t.Fatalf("unrelated offer = %#v", unrelatedOffer)
	}

	withdrawn := engine.Withdraw(testWithdrawal(scope, audience, coalesced.Offer.Identity.Items[0], now.Add(2*time.Millisecond)))
	if withdrawn.RemovedItems != 1 || withdrawn.RevokedOffers != 1 {
		t.Fatalf("exact coalesced withdrawal = %#v", withdrawn)
	}
	if stats := engine.Stats(); stats.PendingItems != 2 || stats.LiveOffers != 1 || stats.Receipts != 0 {
		t.Fatalf("unrelated state was changed: %#v", stats)
	}

	sources := engine.PendingSources()
	if len(sources) != 2 {
		t.Fatalf("pending source count = %#v", sources)
	}
	foundSecond, foundUnrelated := false, false
	for _, source := range sources {
		switch {
		case sameOfferItem(source.Identity, coalesced.Offer.Identity.Items[1]):
			foundSecond = source.Recruitment.Resource == "source-b.md"
		case sameOfferItem(source.Identity, unrelatedOffer.Offer.Identity.Items[0]):
			foundUnrelated = true
		}
	}
	if !foundSecond || !foundUnrelated {
		t.Fatalf("pending source metadata = %#v", sources)
	}
	sources[0].Source.Path = "caller-mutated"
	refreshed := engine.PendingSources()
	for _, source := range refreshed {
		if source.Source.Path == "caller-mutated" {
			t.Fatal("PendingSources returned aliased metadata")
		}
	}

	if confirmed := testConfirmForEngine(engine, testConfirmation(scope, audience, unrelatedOffer.Offer, now.Add(3*time.Millisecond))); confirmed.State != contextapi.DeliveryConfirmed {
		t.Fatalf("unrelated offer confirmation = %#v", confirmed)
	}
	if stats := engine.Stats(); stats.PendingItems != 1 || stats.Receipts != 1 {
		t.Fatalf("coalesced source state after unrelated confirmation: %#v", stats)
	}
}

func TestConfirmationRevalidatesCurrentConfigurationAndProfile(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	planned := testPlanForEngine(engine, testPlan(scope, audience, now, testContribution("freshness", "source-a", "rev-1")))
	if planned.State != contextapi.DeliveryOffered {
		t.Fatal(planned.Reasons)
	}
	changedScope := scope
	changedScope.ConfigDigest = "config:changed"
	changedConfig := testConfirmation(scope, audience, planned.Offer, now.Add(time.Millisecond))
	changedConfig.Scope = changedScope
	changedConfig.ConfigDigest = changedScope.ConfigDigest
	if result := testConfirmForEngine(engine, changedConfig); result.State != contextapi.DeliveryWithdrawn {
		t.Fatalf("changed config confirmation = %#v", result)
	}

	engine, scope, audience, now = testEngine(t)
	planned = testPlanForEngine(engine, testPlan(scope, audience, now, testContribution("freshness", "source-a", "rev-1")))
	changedProfile := testConfirmation(scope, audience, planned.Offer, now.Add(time.Millisecond))
	changedProfile.ProfileRevision = "profile:changed"
	if result := testConfirmForEngine(engine, changedProfile); result.State != contextapi.DeliveryWithdrawn {
		t.Fatalf("changed profile confirmation = %#v", result)
	}

	engine, scope, audience, _ = testEngine(t)
	now = time.Now()
	profileExpiring := testPlan(scope, audience, now, testContribution("freshness", "source-a", "rev-1"))
	profileExpiring.Profile.ValidUntil = now.Add(time.Minute)
	planned = testPlanForEngine(engine, profileExpiring)
	validWire := testConfirmation(scope, audience, planned.Offer, now.Add(10*time.Millisecond))
	validWire.ProfileValidUntil = profileExpiring.Profile.ValidUntil
	encoded, err := json.Marshal(validWire)
	if err != nil {
		t.Fatal(err)
	}
	var decoded contextapi.OfferConfirmation
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if result := testConfirmForEngine(engine, decoded); result.State != contextapi.DeliveryConfirmed {
		t.Fatalf("JSON-roundtripped valid profile confirmation = %#v", result)
	}

	engine, scope, audience, _ = testEngine(t)
	now = time.Now()
	profileExpiring = testPlan(scope, audience, now, testContribution("freshness", "source-a", "rev-1"))
	profileExpiring.Profile.ValidUntil = now.Add(time.Second)
	planned = testPlanForEngine(engine, profileExpiring)
	expired := testConfirmation(scope, audience, planned.Offer, profileExpiring.Profile.ValidUntil)
	expired.ProfileValidUntil = profileExpiring.Profile.ValidUntil
	encoded, err = json.Marshal(expired)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if result := testConfirmForEngine(engine, decoded); result.State != contextapi.DeliveryWithdrawn {
		t.Fatalf("expired profile confirmation = %#v", result)
	}
}

func TestRetainedMemoryBudgetIsEnforced(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	engine.limits.MaxRetainedBytes = 64
	decision := testPlanForEngine(engine, testPlan(scope, audience, now, testContribution("retained metadata", "source-a", "rev-a")))
	if decision.State != contextapi.DeliveryRejected {
		t.Fatalf("retained budget decision = %#v", decision)
	}
	if stats := engine.Stats(); stats.PendingItems != 0 || stats.LiveOffers != 0 || stats.RetainedBytes > 64 {
		t.Fatalf("retained budget was not enforced transactionally: %#v", stats)
	}
}

func TestMixedReplayBatchMustNotReturnPartialOldOffer(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	a := testContribution("first", "source-a", "rev-a")
	first := testPlanForEngine(engine, testPlan(scope, audience, now, a))
	if first.State != contextapi.DeliveryOffered {
		t.Fatal(first.Reasons)
	}
	b := testContribution("second", "source-b", "rev-b")
	mixed := testPlanForEngine(engine, testPlan(scope, audience, now.Add(time.Millisecond), a, b))
	if mixed.State != contextapi.DeliveryOffered || mixed.Offer.Identity.ID == first.Offer.Identity.ID || len(mixed.Offer.Items) != 1 || mixed.Offer.Body != b.Body {
		t.Fatalf("mixed batch replayed partial offer: first=%#v mixed=%#v", first, mixed)
	}
}

func TestPlanIsolatesMalformedContributionFromHealthyBatch(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	healthy := testContribution("healthy guidance", "source-healthy", "rev-healthy")
	malformed := testContribution("", "source-malformed", "rev-malformed")
	decision := testPlanForEngine(engine, testPlan(scope, audience, now, healthy, malformed))
	if decision.State != contextapi.DeliveryOffered || decision.Offer.Body != healthy.Body {
		t.Fatalf("healthy contribution was suppressed by malformed sibling: %#v", decision)
	}
	found := false
	for _, reason := range decision.Reasons {
		if reason.Code == contextapi.ReasonMalformedContribution {
			found = true
		}
	}
	if !found {
		t.Fatalf("malformed sibling reason was not retained: %#v", decision.Reasons)
	}
	if stats := engine.Stats(); stats.PendingItems != 1 {
		t.Fatalf("malformed sibling changed healthy pending state: %#v", stats)
	}
}

func TestPlanIsolatesOversizedRenderedItemFromHealthyBatch(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	engine.limits.Queue.MaxItemBytes = 32
	oversized := testContribution(strings.Repeat("x", 24), "source-oversized", "rev-oversized")
	oversized.Source.Label = "oversized-label"
	healthy := testContribution("healthy guidance", "source-healthy", "rev-healthy")
	decision := testPlanForEngine(engine, testPlan(scope, audience, now, oversized, healthy))
	if decision.State != contextapi.DeliveryOffered || decision.Offer.Body != healthy.Body {
		t.Fatalf("healthy contribution was suppressed by oversized rendered sibling: %#v", decision)
	}
	found := false
	for _, reason := range decision.Reasons {
		if reason.Code == contextapi.ReasonQueueFull && reason.Provider == "provider:test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("oversized rendered sibling reason was not retained: %#v", decision.Reasons)
	}
}
func TestPlanIsolatesNewItemsFromPendingByteOverflow(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	engine.limits.Queue.MaxPendingBytes = 40
	old := testContribution(strings.Repeat("a", 20), "source-old", "rev-old")
	deferred := testPlan(scope, audience, now, old)
	deferred.Opportunity.Available = false
	if result := testPlanForEngine(engine, deferred); result.State != contextapi.DeliveryDeferred {
		t.Fatalf("old pending setup = %#v", result)
	}
	newer := testContribution(strings.Repeat("b", 20), "source-new", "rev-new")
	healthy := testContribution("ccccc", "source-healthy", "rev-healthy")
	decision := testPlanForEngine(engine, testPlan(scope, audience, now.Add(time.Millisecond), newer, healthy))
	if decision.State != contextapi.DeliveryOffered || !strings.Contains(decision.Offer.Body, newer.Body) || strings.Contains(decision.Offer.Body, healthy.Body) {
		t.Fatalf("pending-byte overflow suppressed the wrong items: %#v", decision)
	}
	found := false
	for _, reason := range decision.Reasons {
		if reason.Code == contextapi.ReasonQueueFull && reason.Provider == "provider:test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending-byte drop reason was not retained: %#v", decision.Reasons)
	}
}

func TestQueueExpiryAndExactBudget(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	large := testContribution("budgeted", "source-a", "rev-a")
	large.Source.Label = "label"
	planned := testPlan(scope, audience, now, large)
	planned.Opportunity.MaxBytes = uint64(len(large.Body))
	deferred := testPlanForEngine(engine, planned)
	if deferred.State != contextapi.DeliveryDeferred {
		t.Fatalf("budget decision = %#v", deferred)
	}
	if engine.Stats().PendingItems != 1 {
		t.Fatalf("deferred item was lost: %#v", engine.Stats())
	}
	expired := testPlan(scope, audience, now.Add(1000*time.Millisecond))
	expired.Opportunity.ID = "hook:2"
	decision := testPlanForEngine(engine, expired)
	if decision.State != contextapi.DeliveryIdle || len(decision.Reasons) == 0 || decision.Reasons[0].Code != contextapi.ReasonQueueExpired {
		t.Fatalf("expiry decision = %#v", decision)
	}
}

func TestPlanDoesNotRetainCallerSlices(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	contribution := testContribution("owned", "source-a", "rev-a")
	contribution.Reasons = []contextapi.Reason{{Code: contextapi.ReasonNoMatch, Params: []contextapi.ReasonParameter{{Key: "mutable", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueText, Text: "before"}}}}}
	planned := testPlan(scope, audience, now, contribution)
	first := testPlanForEngine(engine, planned)
	if first.State != contextapi.DeliveryOffered {
		t.Fatal(first.Reasons)
	}
	first.Offer.Identity.Items[0].Source.ID = "caller-mutated"
	first.Offer.Identity.Items[0].SourceRevision.Revision = "caller-mutated"
	replay := testPlanForEngine(engine, testPlan(scope, audience, now, contribution))
	if replay.State != contextapi.DeliveryOffered || replay.Offer.Identity.Items[0].Source.ID != "source-a" {
		t.Fatalf("internal offer aliased caller result: %#v", replay)
	}
}

func TestConfirmedOfferDoesNotRetainFullBodyInReplayState(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	body := strings.Repeat("0123456789abcdefghijklmnopqrstuvwxyz", 60)
	contribution := testContribution(body, "source-a", "rev-a")
	planned := testPlanForEngine(engine, testPlan(scope, audience, now, contribution))
	if planned.State != contextapi.DeliveryOffered {
		t.Fatal(planned.Reasons)
	}
	if result := testConfirmForEngine(engine, testConfirmation(scope, audience, planned.Offer, now.Add(time.Millisecond))); result.State != contextapi.DeliveryConfirmed {
		t.Fatal(result)
	}
	if retained := engine.Stats().RetainedBytes; retained >= uint64(len(body)) {
		t.Fatalf("completed replay retained body-sized state: %d bytes for %d-byte body", retained, len(body))
	}
}

func TestEngineStateIsRaceSafe(t *testing.T) {
	engine, scope, audience, now := testEngine(t)
	contribution := testContribution("concurrent", "source-a", "rev-a")
	input := testPlan(scope, audience, now, contribution)
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = testPlanForEngine(engine, input)
			_ = engine.Stats()
			_ = engine.PendingSources()
		}()
	}
	wait.Wait()
	if stats := engine.Stats(); stats.LiveOffers != 1 || stats.PendingItems != 1 {
		t.Fatalf("concurrent state = %#v", stats)
	}
}
