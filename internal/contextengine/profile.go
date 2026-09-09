package contextengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

const (
	profileConfigured = 3
	profileHost       = 2
	profileProvider   = 1
)

type profileCandidate struct {
	key        string
	value      contextapi.FactValue
	precedence int
	origin     contextapi.FactOrigin
	provider   contextapi.ProviderID
	rule       string
	validity   contextapi.FactValidity
	provenance contextapi.FactProvenance
	appliesTo  contextapi.ProfileScope
	order      string
}

// Compose deterministically combines configured, host, and provider facts.
// It reads no clock: input.Now is the only time value used to evaluate the
// declared validity windows.
func Compose(input contextapi.ProfileCompositionInput) contextapi.ProfileCompositionResult {
	candidates := make(map[string][]profileCandidate)
	add := func(candidate profileCandidate) {
		if candidate.key != "" {
			candidates[candidate.key] = append(candidates[candidate.key], candidate)
		}
	}

	if input.Explicit.Role != "" {
		add(explicitCandidate("role", input.Explicit.Role, input))
	}
	if input.Explicit.GuidanceSet != "" {
		add(explicitCandidate("guidanceSet", input.Explicit.GuidanceSet, input))
	}
	preferences := append([]contextapi.NamedValue(nil), input.Explicit.Preferences...)
	for _, preference := range preferences {
		if preference.Name == "" {
			continue
		}
		add(profileCandidate{
			key:        "preference:" + preference.Name,
			value:      preference.Value,
			precedence: profileConfigured,
			origin:     contextapi.FactConfigured,
			rule:       "profile.explicit.preference",
			provenance: contextapi.FactProvenance{Origin: contextapi.FactConfigured, Rule: "profile.explicit.preference"},
			appliesTo:  currentProfileScope(input),
			order:      "configured|" + preference.Name + "|" + factValueKey(preference.Value),
		})
	}

	for _, fact := range input.HostFacts {
		if fact.Key == "" {
			continue
		}
		add(profileCandidate{
			key:        fact.Key,
			value:      fact.Value,
			precedence: profileHost,
			origin:     contextapi.FactHost,
			provider:   fact.Provider,
			rule:       "profile.host.fact",
			provenance: contextapi.FactProvenance{Origin: contextapi.FactHost, Provider: fact.Provider, Rule: "profile.host.fact"},
			appliesTo:  currentProfileScope(input),
			order:      "host|" + string(fact.Provider) + "|" + factValueKey(fact.Value),
		})
	}

	reasons := make([]contextapi.Reason, 0)
	for _, fact := range input.ProviderFacts {
		if fact.Key == "" || !profileScopeApplies(fact.AppliesTo, input) {
			continue
		}
		if notBeforeFuture(fact.Validity, input.Now) {
			continue
		}
		if status, deadline := factValidityState(fact.Validity, input.Now); status != validityCurrent {
			reasons = append(reasons, expiredReason(fact, deadline, status, input.Now))
			continue
		}
		provider := fact.Provenance.Provider
		add(profileCandidate{
			key:        fact.Key,
			value:      fact.Value,
			precedence: profileProvider,
			origin:     contextapi.FactProvider,
			provider:   provider,
			rule:       fact.Provenance.Rule,
			validity:   cloneValidity(fact.Validity),
			provenance: cloneProvenance(fact.Provenance),
			appliesTo:  fact.AppliesTo,
			order:      "provider|" + string(provider) + "|" + canonicalFactOrder(fact),
		})
	}

	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	selected := make([]contextapi.ProfileFact, 0, len(keys))
	validUntil := time.Time{}
	for _, key := range keys {
		options := candidates[key]
		sort.SliceStable(options, func(i, j int) bool {
			if options[i].precedence != options[j].precedence {
				return options[i].precedence > options[j].precedence
			}
			return options[i].order < options[j].order
		})
		winner := options[0]
		for _, option := range options[1:] {
			if option.precedence == winner.precedence && !sameFactValue(option.value, winner.value) {
				reasons = append(reasons, profileConflictReason(key, winner, option, input.Now))
			}
		}
		fact := contextapi.ProfileFact{
			Key:        winner.key,
			Value:      winner.value,
			AppliesTo:  winner.appliesTo,
			Validity:   cloneValidity(winner.validity),
			Provenance: cloneProvenance(winner.provenance),
		}
		if fact.AppliesTo.Directory == "" {
			fact.AppliesTo.Directory = input.Scope.CanonicalRoot
		}
		if fact.AppliesTo.Audience.ID == "" && fact.AppliesTo.Audience.Epoch == 0 {
			fact.AppliesTo.Audience = input.Audience
		}
		if fact.AppliesTo.Task.ID == "" && fact.AppliesTo.Task.Kind == "" {
			fact.AppliesTo.Task = input.Task
		}
		selected = append(selected, fact)
		if deadline := validityDeadline(winner.validity); !deadline.IsZero() && (validUntil.IsZero() || deadline.Before(validUntil)) {
			validUntil = deadline
		}
	}

	canonicalPreferences := append([]contextapi.NamedValue(nil), input.Explicit.Preferences...)
	sort.SliceStable(canonicalPreferences, func(i, j int) bool {
		if canonicalPreferences[i].Name != canonicalPreferences[j].Name {
			return canonicalPreferences[i].Name < canonicalPreferences[j].Name
		}
		return factValueKey(canonicalPreferences[i].Value) < factValueKey(canonicalPreferences[j].Value)
	})
	canonicalExplicit := input.Explicit
	canonicalExplicit.Preferences = canonicalPreferences
	revisionInput := struct {
		Scope            contextapi.ScopeIdentity
		WorkingDirectory string
		Audience         contextapi.Audience
		Task             contextapi.TaskRef
		ConfigDigest     contextapi.ConfigDigest
		Explicit         contextapi.ProfileSelection
		Facts            []contextapi.ProfileFact
	}{input.Scope, input.WorkingDirectory, input.Audience, input.Task, input.ConfigDigest, canonicalExplicit, selected}
	revisionBytes, _ := json.Marshal(revisionInput)
	revisionDigest := sha256.Sum256(revisionBytes)

	return contextapi.ProfileCompositionResult{
		Snapshot: contextapi.ProfileSnapshot{
			Revision:    contextapi.ProfileRevision("sha256:" + hex.EncodeToString(revisionDigest[:])),
			Audience:    input.Audience,
			Facts:       cloneProfileFacts(selected),
			GeneratedAt: input.Now,
			ValidUntil:  validUntil,
		},
		Reasons: cloneReasons(reasons),
	}
}

func explicitCandidate(key, value string, input contextapi.ProfileCompositionInput) profileCandidate {
	factValue := contextapi.FactValue{Kind: contextapi.FactText, Text: value}
	return profileCandidate{
		key:        key,
		value:      factValue,
		precedence: profileConfigured,
		origin:     contextapi.FactConfigured,
		rule:       "profile.explicit." + key,
		provenance: contextapi.FactProvenance{Origin: contextapi.FactConfigured, Rule: "profile.explicit." + key},
		appliesTo:  currentProfileScope(input),
		order:      "configured|" + key + "|" + value,
	}
}

func currentProfileScope(input contextapi.ProfileCompositionInput) contextapi.ProfileScope {
	return contextapi.ProfileScope{Directory: input.Scope.CanonicalRoot, Audience: input.Audience, Task: input.Task}
}

func profileScopeApplies(scope contextapi.ProfileScope, input contextapi.ProfileCompositionInput) bool {
	workingDirectory := input.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = input.Scope.CanonicalRoot
	}
	if scope.Directory != "" && !directoryContains(scope.Directory, workingDirectory) {
		return false
	}
	if scope.Audience.ID != "" && scope.Audience.ID != input.Audience.ID {
		return false
	}
	if scope.Audience.Epoch != 0 && scope.Audience.Epoch != input.Audience.Epoch {
		return false
	}
	if scope.Task.ID != "" && scope.Task.ID != input.Task.ID {
		return false
	}
	if scope.Task.Kind != "" && scope.Task.Kind != input.Task.Kind {
		return false
	}
	return true
}

func directoryContains(scope, working string) bool {
	scope = filepath.Clean(scope)
	working = filepath.Clean(working)
	return scope == working || strings.HasPrefix(working, scope+string(filepath.Separator))
}

func notBeforeFuture(validity contextapi.FactValidity, now time.Time) bool {
	return !validity.NotBefore.IsZero() && !now.IsZero() && now.Before(validity.NotBefore)
}

type validityState uint8

const (
	validityCurrent validityState = iota
	validityExpired
	validityRefresh
)

func factValidityState(validity contextapi.FactValidity, now time.Time) (validityState, time.Time) {
	if now.IsZero() {
		return validityCurrent, time.Time{}
	}
	var deadline time.Time
	switch validity.Policy {
	case contextapi.ValidityNone:
		return validityCurrent, time.Time{}
	case contextapi.ValidityUntil, contextapi.ValidityActivityTTL:
		deadline = validity.ExpiresAt
	case contextapi.ValidityRefreshAfter:
		deadline = validity.RefreshAfter
		if deadline.IsZero() {
			deadline = validity.ExpiresAt
		}
	default:
		deadline = validity.ExpiresAt
	}
	if validity.Policy != contextapi.ValidityNone && !validity.ExpiresAt.IsZero() && !now.Before(validity.ExpiresAt) {
		return validityExpired, validity.ExpiresAt
	}
	if deadline.IsZero() || now.Before(deadline) {
		return validityCurrent, deadline
	}
	if validity.Policy == contextapi.ValidityRefreshAfter {
		return validityRefresh, deadline
	}
	return validityExpired, deadline
}

func validityDeadline(validity contextapi.FactValidity) time.Time {
	switch validity.Policy {
	case contextapi.ValidityNone:
		return time.Time{}
	case contextapi.ValidityRefreshAfter:
		deadline := validity.RefreshAfter
		if !validity.ExpiresAt.IsZero() && (deadline.IsZero() || validity.ExpiresAt.Before(deadline)) {
			deadline = validity.ExpiresAt
		}
		return deadline
	}
	return validity.ExpiresAt
}

func expiredReason(fact contextapi.ProfileFact, deadline time.Time, status validityState, at time.Time) contextapi.Reason {
	params := []contextapi.ReasonParameter{{Key: "key", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueText, Text: fact.Key}}}
	if !deadline.IsZero() {
		params = append(params, contextapi.ReasonParameter{Key: "deadlineUnixMs", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: deadline.UnixMilli()}})
	}
	params = append(params, cloneReasonParameters(fact.Validity.Inputs)...)
	code := contextapi.ReasonProfileExpired
	summary := "provider profile fact expired"
	if status == validityRefresh {
		code = contextapi.ReasonProfileRefresh
		summary = "provider profile fact requires refresh"
	}
	return contextapi.Reason{Code: code, Origin: contextapi.ReasonProvider, Provider: fact.Provenance.Provider, Rule: fact.Validity.Rule, Summary: summary, Params: params, At: at}
}

func profileConflictReason(key string, winner, rejected profileCandidate, at time.Time) contextapi.Reason {
	return contextapi.Reason{
		Code:     contextapi.ReasonProfileConflict,
		Origin:   contextapi.ReasonRuntime,
		Provider: winner.provider,
		Rule:     winner.rule,
		Summary:  "same-precedence profile facts disagree",
		Params: []contextapi.ReasonParameter{
			{Key: "key", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueText, Text: key}},
			{Key: "chosen", Value: factReasonValue(winner.value)},
			{Key: "rejected", Value: factReasonValue(rejected.value)},
			{Key: "rejectedProvider", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueText, Text: string(rejected.provider)}},
		},
		At: at,
	}
}

func factReasonValue(value contextapi.FactValue) contextapi.ReasonValue {
	switch value.Kind {
	case contextapi.FactText:
		return contextapi.ReasonValue{Kind: contextapi.ReasonValueText, Text: value.Text}
	case contextapi.FactNumber:
		return contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: value.Number}
	case contextapi.FactBoolean:
		return contextapi.ReasonValue{Kind: contextapi.ReasonValueBoolean, Boolean: value.Boolean}
	default:
		return contextapi.ReasonValue{Kind: contextapi.ReasonValueText, Text: "invalid"}
	}
}

func sameFactValue(a, b contextapi.FactValue) bool {
	return a.Kind == b.Kind && a.Text == b.Text && a.Number == b.Number && a.Boolean == b.Boolean
}

func factValueKey(value contextapi.FactValue) string {
	switch value.Kind {
	case contextapi.FactText:
		return "text:" + value.Text
	case contextapi.FactNumber:
		return "number:" + strconv.FormatInt(value.Number, 10)
	case contextapi.FactBoolean:
		return "bool:" + strconv.FormatBool(value.Boolean)
	default:
		return "kind:" + string(value.Kind)
	}
}

func canonicalFactOrder(fact contextapi.ProfileFact) string {
	copyFact := fact
	copyFact.Validity.Inputs = cloneReasonParameters(fact.Validity.Inputs)
	sort.SliceStable(copyFact.Validity.Inputs, func(i, j int) bool {
		left := copyFact.Validity.Inputs[i]
		right := copyFact.Validity.Inputs[j]
		if left.Key != right.Key {
			return left.Key < right.Key
		}
		return reasonValueKey(left.Value) < reasonValueKey(right.Value)
	})
	encoded, _ := json.Marshal(copyFact)
	return string(encoded)
}

func reasonValueKey(value contextapi.ReasonValue) string {
	return string(value.Kind) + "|" + value.Text + "|" + strconv.FormatInt(value.Number, 10) + "|" + strconv.FormatBool(value.Boolean) + "|" + strconv.FormatUint(value.Bytes, 10) + "|" + strconv.FormatInt(value.DurationNanos, 10)
}

func cloneReasonParameters(input []contextapi.ReasonParameter) []contextapi.ReasonParameter {
	if input == nil {
		return nil
	}
	result := make([]contextapi.ReasonParameter, len(input))
	copy(result, input)
	for i := range result {
		result[i].Key = strings.Clone(result[i].Key)
		result[i].Value.Kind = cloneString(result[i].Value.Kind)
		result[i].Value.Text = strings.Clone(result[i].Value.Text)
	}
	return result
}

func cloneValidity(input contextapi.FactValidity) contextapi.FactValidity {
	result := input
	result.Rule = strings.Clone(input.Rule)
	result.Inputs = cloneReasonParameters(input.Inputs)
	return result
}

func cloneProvenance(input contextapi.FactProvenance) contextapi.FactProvenance {
	result := input
	result.Origin = cloneString(result.Origin)
	result.Provider = cloneString(result.Provider)
	result.Rule = strings.Clone(result.Rule)
	result.SourceRevision = cloneSourceRevision(result.SourceRevision)
	return result
}

func cloneProfileFacts(input []contextapi.ProfileFact) []contextapi.ProfileFact {
	if input == nil {
		return nil
	}
	result := make([]contextapi.ProfileFact, len(input))
	for i, fact := range input {
		result[i] = fact
		result[i].Key = strings.Clone(fact.Key)
		result[i].Value.Kind = cloneString(fact.Value.Kind)
		result[i].Value.Text = strings.Clone(fact.Value.Text)
		result[i].AppliesTo.Directory = strings.Clone(fact.AppliesTo.Directory)
		result[i].AppliesTo.Audience = cloneAudience(fact.AppliesTo.Audience)
		result[i].AppliesTo.Task.ID = cloneString(fact.AppliesTo.Task.ID)
		result[i].AppliesTo.Task.Kind = strings.Clone(fact.AppliesTo.Task.Kind)
		result[i].Validity = cloneValidity(fact.Validity)
		result[i].Provenance = cloneProvenance(fact.Provenance)
	}
	return result
}
