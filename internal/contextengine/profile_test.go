package contextengine

import (
	"reflect"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

func profileInput(now time.Time) contextapi.ProfileCompositionInput {
	return contextapi.ProfileCompositionInput{
		Scope: contextapi.ScopeIdentity{
			ID:            "scope:test",
			Authority:     contextapi.ScopeAuthorityProject,
			CanonicalRoot: "/repo",
			ConfigDigest:  "config:test",
		},
		WorkingDirectory: "/repo/sub",
		Audience:         contextapi.Audience{ID: "session:test", Epoch: 4, Continuity: contextapi.ContinuityKnown},
		Task:             contextapi.TaskRef{ID: "task:test", Kind: "review"},
		ConfigDigest:     "config:test",
		Now:              now,
	}
}

func profileProviderFact(key, value string, provider contextapi.ProviderID, directory string) contextapi.ProfileFact {
	return contextapi.ProfileFact{
		Key:       key,
		Value:     contextapi.FactValue{Kind: contextapi.FactText, Text: value},
		AppliesTo: contextapi.ProfileScope{Directory: directory},
		Validity:  contextapi.FactValidity{Policy: contextapi.ValidityNone},
		Provenance: contextapi.FactProvenance{
			Origin:   contextapi.FactProvider,
			Provider: provider,
			Rule:     "provider.rule",
		},
	}
}

func TestComposeUsesDirectionalApplicabilityAndPrecedence(t *testing.T) {
	now := time.UnixMilli(1000)
	input := profileInput(now)
	input.Explicit.Role = "configured"
	input.HostFacts = []contextapi.Fact{{Key: "role", Value: contextapi.FactValue{Kind: contextapi.FactText, Text: "host"}}}
	input.ProviderFacts = []contextapi.ProfileFact{
		profileProviderFact("role", "provider", "provider-b", "/repo"),
		profileProviderFact("ignored", "child-only", "provider-a", "/repo/child"),
	}
	result := Compose(input)
	if len(result.Snapshot.Facts) != 1 || result.Snapshot.Facts[0].Key != "role" || result.Snapshot.Facts[0].Value.Text != "configured" {
		t.Fatalf("precedence/applicability result = %#v", result)
	}
	if result.Snapshot.Facts[0].Provenance.Origin != contextapi.FactConfigured || result.Snapshot.Facts[0].Provenance.Rule == "" {
		t.Fatalf("configured provenance = %#v", result.Snapshot.Facts[0].Provenance)
	}
	if result.Snapshot.Facts[0].AppliesTo.Directory != "/repo" {
		t.Fatalf("configured applicability = %#v", result.Snapshot.Facts[0].AppliesTo)
	}
}

func TestComposeProviderTieIsPermutationIndependent(t *testing.T) {
	now := time.UnixMilli(1000)
	first := profileInput(now)
	first.ProviderFacts = []contextapi.ProfileFact{
		profileProviderFact("color", "blue", "provider-b", "/repo"),
		profileProviderFact("color", "red", "provider-a", "/repo"),
	}
	second := profileInput(now)
	second.ProviderFacts = []contextapi.ProfileFact{first.ProviderFacts[1], first.ProviderFacts[0]}
	a := Compose(first)
	b := Compose(second)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("provider order changed composition:\nfirst=%#v\nsecond=%#v", a, b)
	}
	if len(a.Reasons) != 1 || a.Reasons[0].Code != contextapi.ReasonProfileConflict {
		t.Fatalf("conflict reason = %#v", a.Reasons)
	}
}

func TestComposeRevisionIgnoresClockWhenFactsDoNotChange(t *testing.T) {
	first := profileInput(time.UnixMilli(1000))
	first.ProviderFacts = []contextapi.ProfileFact{profileProviderFact("mode", "steady", "provider-a", "/repo")}
	second := first
	second.Now = first.Now.Add(24 * time.Hour)
	if a, b := Compose(first).Snapshot.Revision, Compose(second).Snapshot.Revision; a != b {
		t.Fatalf("clock perturbed unchanged profile revision: %q != %q", a, b)
	}
}

func TestComposeDistinguishesExpiryAndRefreshWithInputs(t *testing.T) {
	now := time.UnixMilli(1000)
	input := profileInput(now)
	input.ProviderFacts = []contextapi.ProfileFact{{
		Key:       "expired",
		Value:     contextapi.FactValue{Kind: contextapi.FactBoolean, Boolean: true},
		AppliesTo: contextapi.ProfileScope{Directory: "/repo"},
		Validity: contextapi.FactValidity{
			Policy:    contextapi.ValidityUntil,
			ExpiresAt: now,
			Rule:      "expires-after-activity",
			Inputs: []contextapi.ReasonParameter{{
				Key:   "activityCount",
				Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: 7},
			}},
		},
		Provenance: contextapi.FactProvenance{Origin: contextapi.FactProvider, Provider: "provider-a"},
	}}
	result := Compose(input)
	if len(result.Snapshot.Facts) != 0 || len(result.Reasons) != 1 || result.Reasons[0].Code != contextapi.ReasonProfileExpired {
		t.Fatalf("expiry result = %#v", result)
	}
	if len(result.Reasons[0].Params) != 3 || result.Reasons[0].Params[2].Key != "activityCount" || result.Reasons[0].Params[2].Value.Number != 7 {
		t.Fatalf("expiry inputs were not retained: %#v", result.Reasons[0].Params)
	}

	input.ProviderFacts[0].Validity = contextapi.FactValidity{
		Policy:       contextapi.ValidityRefreshAfter,
		RefreshAfter: now,
		Rule:         "refresh-activity",
	}
	refreshed := Compose(input)
	if len(refreshed.Reasons) != 1 || refreshed.Reasons[0].Code != contextapi.ReasonProfileRefresh {
		t.Fatalf("refresh result = %#v", refreshed.Reasons)
	}
}
