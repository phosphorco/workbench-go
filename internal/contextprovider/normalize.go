package contextprovider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

const (
	hardProviderFacts         = 4096
	hardProviderContributions = 4096
	hardProviderReasons       = 4096
	hardValidityInputs        = 4096
)

func normalizeProfileRequest(input contextapi.ProfileRequest, resource contextapi.ProviderResource) contextapi.ProfileRequest {
	if isZeroScope(input.Scope) {
		input.Scope = resource.Scope
	}
	if input.ConfigDigest == "" {
		input.ConfigDigest = resource.ConfigDigest
	}
	return input
}

func normalizeContributionRequest(input contextapi.ContributionRequest, resource contextapi.ProviderResource) contextapi.ContributionRequest {
	if isZeroScope(input.Scope) {
		input.Scope = resource.Scope
	}
	if input.ConfigDigest == "" {
		input.ConfigDigest = resource.ConfigDigest
	}
	return input
}

func normalizeProfileResponse(
	input contextapi.ProfileResponse,
	request contextapi.ProfileRequest,
	provider contextapi.ProviderID,
	maxFacts uint32,
) (contextapi.ProfileResponse, error) {
	if maxFacts == 0 {
		maxFacts = hardProviderFacts
	}
	if maxFacts != 0 && uint32(len(input.Facts)) > maxFacts {
		return contextapi.ProfileResponse{}, fmt.Errorf("%w: provider returned %d facts, maximum is %d", ErrResponseTooLarge, len(input.Facts), maxFacts)
	}
	output := contextapi.ProfileResponse{
		Facts:   make([]contextapi.ProfileFact, 0, len(input.Facts)),
		Reasons: normalizeProviderReasons(input.Reasons, provider),
	}
	for _, fact := range input.Facts {
		if fact.Key == "" || !validFactValue(fact.Value) {
			output.Reasons = append(output.Reasons, malformedFactReason(provider, "provider returned a malformed fact value", request))
			continue
		}
		if fact.AppliesTo.Audience != (contextapi.Audience{}) && fact.AppliesTo.Audience != request.Audience {
			output.Reasons = append(output.Reasons, malformedFactReason(provider, "provider returned a fact scoped to another audience", request))
			continue
		}
		if fact.Provenance.Provider != "" && fact.Provenance.Provider != provider {
			output.Reasons = append(output.Reasons, malformedFactReason(provider, "provider returned a fact attributed to another provider", request))
			continue
		}
		if fact.Provenance.Origin != "" && fact.Provenance.Origin != contextapi.FactProvider {
			output.Reasons = append(output.Reasons, malformedFactReason(provider, "provider returned a fact with non-provider authority", request))
			continue
		}
		if fact.Provenance.SourceRevision.Source.Provider != "" && fact.Provenance.SourceRevision.Source.Provider != provider {
			output.Reasons = append(output.Reasons, malformedFactReason(provider, "provider returned a fact source attributed to another provider", request))
			continue
		}
		fact.Provenance.Provider = provider
		fact.Provenance.Origin = contextapi.FactProvider
		if fact.Provenance.SourceRevision.Source.ID != "" {
			fact.Provenance.SourceRevision.Source.Provider = provider
		}
		fact.Validity = cloneFactValidity(fact.Validity)
		output.Facts = append(output.Facts, fact)
	}
	if request.Limits.MaxReasonBytes != 0 && reasonBytes(output.Reasons) > request.Limits.MaxReasonBytes {
		return contextapi.ProfileResponse{}, fmt.Errorf("%w: profile reasons exceed their bound", ErrResponseTooLarge)
	}
	return output, nil
}

func normalizeContributionResponse(
	input contextapi.ContributionResponse,
	request contextapi.ContributionRequest,
	resource contextapi.ProviderResource,
	providerLimits contextapi.ProviderLimits,
) (contextapi.ContributionResponse, error) {
	output := contextapi.ContributionResponse{
		Contributions: nil,
		Reasons:       normalizeProviderReasons(input.Reasons, resource.Provider),
	}
	maxContributions := minUint32NonZero(providerLimits.MaxContributions, request.Limits.MaxContributions)
	if maxContributions == 0 {
		maxContributions = hardProviderContributions
	}
	capacity := len(input.Contributions)
	if uint64(capacity) > uint64(maxContributions) {
		capacity = int(maxContributions)
	}
	output.Contributions = make([]contextapi.Contribution, 0, capacity)
	maxBodyBytes := minUint64NonZero(providerLimits.MaxBodyBytes, request.Limits.MaxBodyBytes)
	for index, contribution := range input.Contributions {
		if maxContributions != 0 && uint32(len(output.Contributions)) >= maxContributions {
			output.Reasons = append(output.Reasons, *malformedContributionReason(resource.Provider, "provider contribution count exceeds its bound", request, index))
			break
		}
		normalized, reason := hydrateContribution(contribution, request, resource)
		if reason != nil {
			output.Reasons = append(output.Reasons, *reason)
			continue
		}
		if maxBodyBytes != 0 && uint64(len([]byte(normalized.Body))) > maxBodyBytes {
			output.Reasons = append(output.Reasons, *malformedContributionReason(resource.Provider, "contribution body exceeds its bound", request, index))
			continue
		}
		output.Contributions = append(output.Contributions, normalized)
	}
	if request.Limits.MaxReasonBytes != 0 {
		total := reasonBytes(output.Reasons)
		for _, contribution := range output.Contributions {
			total += reasonBytes(contribution.Reasons)
			if total > request.Limits.MaxReasonBytes {
				return contextapi.ContributionResponse{}, fmt.Errorf("%w: contribution reasons exceed their bound", ErrResponseTooLarge)
			}
		}
	}
	return output, nil
}

func hydrateContribution(
	input contextapi.Contribution,
	request contextapi.ContributionRequest,
	resource contextapi.ProviderResource,
) (contextapi.Contribution, *contextapi.Reason) {
	provider := resource.Provider
	if input.Contributor != "" && input.Contributor != provider {
		return contextapi.Contribution{}, malformedContributionReason(provider, "contributor identity conflicts with the runtime resource", request, -1)
	}
	if input.Slot.Provider != "" && input.Slot.Provider != provider {
		return contextapi.Contribution{}, malformedContributionReason(provider, "slot provider conflicts with the runtime resource", request, -1)
	}
	if input.Source.Identity.Provider != "" && input.Source.Identity.Provider != provider {
		return contextapi.Contribution{}, malformedContributionReason(provider, "source provider conflicts with the runtime resource", request, -1)
	}
	if input.Slot.Key == "" {
		return contextapi.Contribution{}, malformedContributionReason(provider, "contribution is missing a stable slot key", request, -1)
	}
	if input.Source.Identity.ID == "" {
		return contextapi.Contribution{}, malformedContributionReason(provider, "contribution is missing a stable source id", request, -1)
	}
	if input.SourceRevision.Source.Provider != "" && input.SourceRevision.Source.Provider != provider {
		return contextapi.Contribution{}, malformedContributionReason(provider, "source revision provider conflicts with the runtime resource", request, -1)
	}
	if input.SourceRevision.Source.ID != "" && input.SourceRevision.Source.ID != input.Source.Identity.ID {
		return contextapi.Contribution{}, malformedContributionReason(provider, "source revision id conflicts with the source identity", request, -1)
	}
	if input.ConfigDigest != "" && input.ConfigDigest != request.ConfigDigest {
		return contextapi.Contribution{}, malformedContributionReason(provider, "contribution config digest conflicts with the request", request, -1)
	}
	if input.ProfileRevision != "" && input.ProfileRevision != request.Profile.Revision {
		return contextapi.Contribution{}, malformedContributionReason(provider, "contribution profile revision conflicts with the request", request, -1)
	}
	if !utf8.ValidString(input.Body) {
		return contextapi.Contribution{}, malformedContributionReason(provider, "contribution body is not valid UTF-8", request, -1)
	}
	if input.Body == "" {
		return contextapi.Contribution{}, malformedContributionReason(provider, "contribution body is empty", request, -1)
	}

	input.Contributor = provider
	input.Slot.Provider = provider
	input.Source.Identity.Provider = provider
	input.SourceRevision.Source = input.Source.Identity
	if input.Source.Kind == "" {
		input.Source.Kind = contextapi.SourceFile
	}
	input.ConfigDigest = request.ConfigDigest
	input.ProfileRevision = request.Profile.Revision
	content := contentIdentity(input.Body)
	if input.Content.ID != "" && input.Content.ID != content.ID {
		return contextapi.Contribution{}, malformedContributionReason(provider, "content identity conflicts with the complete body", request, -1)
	}
	if input.Content.UTF8Bytes != 0 && input.Content.UTF8Bytes != content.UTF8Bytes {
		return contextapi.Contribution{}, malformedContributionReason(provider, "content byte length conflicts with the complete body", request, -1)
	}
	input.Content = content
	if input.SourceRevision.Revision == "" {
		input.SourceRevision.Revision = contextapi.SourceRevisionID(content.ID)
	}
	input.Reasons = normalizeProviderReasons(input.Reasons, provider)
	return input, nil
}

func normalizeProviderReasons(input []contextapi.Reason, provider contextapi.ProviderID) []contextapi.Reason {
	if len(input) > hardProviderReasons {
		input = input[:hardProviderReasons]
	}
	output := cloneReasons(input)
	for index := range output {
		// Every reason crossing an executable-provider boundary is provider
		// evidence. A child cannot claim host, configured, runtime, or another
		// provider authority. Runtime-generated diagnostics are authored by the
		// runtime after this boundary and never pass through this function.
		output[index].Origin = contextapi.ReasonProvider
		output[index].Provider = provider
	}
	return output
}

func validFactValue(value contextapi.FactValue) bool {
	switch value.Kind {
	case contextapi.FactText, contextapi.FactNumber, contextapi.FactBoolean:
		return true
	default:
		return false
	}
}

func contentIdentity(body string) contextapi.ContentIdentity {
	digest := sha256.Sum256([]byte(body))
	return contextapi.ContentIdentity{
		ID:        contextapi.ContentID("sha256:" + hex.EncodeToString(digest[:])),
		UTF8Bytes: uint64(len([]byte(body))),
	}
}

func sourceRevisionDigest(manifest []byte, includes []includeSnapshot) contextapi.SourceRevisionID {
	hash := sha256.New()
	_, _ = hash.Write([]byte("ai-context-source-v1\x00"))
	_, _ = hash.Write(manifest)
	for _, include := range includes {
		_, _ = hash.Write([]byte{'\x00'})
		_, _ = hash.Write([]byte(include.path))
		_, _ = hash.Write([]byte{'\x00', byte(include.status)})
		_, _ = hash.Write(include.body)
	}
	return contextapi.SourceRevisionID("sha256:" + hex.EncodeToString(hash.Sum(nil)))
}

func malformedContributionReason(provider contextapi.ProviderID, summary string, request contextapi.ContributionRequest, index int) *contextapi.Reason {
	params := []contextapi.ReasonParameter{}
	if index >= 0 {
		params = append(params, contextapi.ReasonParameter{Key: "index", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: int64(index)}})
	}
	return &contextapi.Reason{
		Code:     contextapi.ReasonMalformedContribution,
		Origin:   contextapi.ReasonProvider,
		Provider: provider,
		Rule:     "provider-response",
		Summary:  summary,
		Params:   params,
		Evidence: evidenceForRequest(request),
		At:       requestAt(request),
	}
}

func malformedFactReason(provider contextapi.ProviderID, summary string, request contextapi.ProfileRequest) contextapi.Reason {
	return contextapi.Reason{
		Code:     contextapi.ReasonInvalidInput,
		Origin:   contextapi.ReasonProvider,
		Provider: provider,
		Rule:     "provider-profile-response",
		Summary:  summary,
		At:       request.Now,
	}
}

func evidenceForRequest(request contextapi.ContributionRequest) []contextapi.EvidenceID {
	if request.Observation.ID == 0 {
		return nil
	}
	return []contextapi.EvidenceID{request.Observation.ID}
}

func requestAt(request contextapi.ContributionRequest) time.Time {
	if !request.Observation.At.IsZero() {
		return request.Observation.At
	}
	return time.Now().UTC()
}

func cloneReasons(input []contextapi.Reason) []contextapi.Reason {
	if input == nil {
		return nil
	}
	output := make([]contextapi.Reason, len(input))
	for index, reason := range input {
		output[index] = reason
		output[index].Params = cloneReasonParameters(reason.Params)
		output[index].Evidence = append([]contextapi.EvidenceID(nil), reason.Evidence...)
	}
	return output
}

func cloneReasonParameters(input []contextapi.ReasonParameter) []contextapi.ReasonParameter {
	return append([]contextapi.ReasonParameter(nil), input...)
}

func cloneFactValidity(input contextapi.FactValidity) contextapi.FactValidity {
	if len(input.Inputs) > hardValidityInputs {
		input.Inputs = input.Inputs[:hardValidityInputs]
	}
	input.Inputs = cloneReasonParameters(input.Inputs)
	return input
}

func reasonBytes(input []contextapi.Reason) uint64 {
	var total uint64
	for _, reason := range input {
		encoded, err := json.Marshal(reason)
		if err != nil {
			return ^uint64(0)
		}
		if ^uint64(0)-total < uint64(len(encoded)) {
			return ^uint64(0)
		}
		total += uint64(len(encoded))
	}
	return total
}

func minUint32NonZero(a, b uint32) uint32 {
	if a == 0 {
		return b
	}
	if b == 0 || a < b {
		return a
	}
	return b
}

func minUint64NonZero(a, b uint64) uint64 {
	if a == 0 {
		return b
	}
	if b == 0 || a < b {
		return a
	}
	return b
}
