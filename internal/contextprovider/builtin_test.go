package contextprovider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

func builtinRequest(resources []contextapi.ObservedResource, selectors []contextapi.ObservedSelector) contextapi.ContributionRequest {
	return contextapi.ContributionRequest{
		RequestID: 7,
		Scope: contextapi.ScopeIdentity{
			ID:           "project-scope",
			Authority:    contextapi.ScopeAuthorityProject,
			ConfigDigest: "sha256:scope-declaration",
		},
		Audience: contextapi.Audience{ID: "audience", Epoch: 3},
		Observation: contextapi.Observation{
			ID:        42,
			At:        time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
			Resources: resources,
			Selectors: selectors,
		},
		Profile:      contextapi.ProfileSnapshot{Revision: "profile:3"},
		ConfigDigest: "sha256:effective-config",
		Limits: contextapi.ContributionLimits{
			MaxContributions: 16,
			MaxBodyBytes:     1 << 20,
			MaxReasonBytes:   1 << 20,
		},
	}
}

func observedFile(path string, outcome contextapi.ResourceOutcome, confidence contextapi.EvidenceConfidence) contextapi.ObservedResource {
	return contextapi.ObservedResource{
		Path:       path,
		Kind:       contextapi.ResourceFile,
		Operation:  contextapi.ResourceRead,
		Outcome:    outcome,
		Confidence: confidence,
	}
}

func writeBuiltinFile(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func builtinTestLimits() BuiltinLimits {
	return BuiltinLimits{
		MaxDepth:             8,
		MaxContextFiles:      16,
		MaxManifestBytes:     64 << 10,
		MaxIncludeBytes:      64 << 10,
		MaxMentionBytes:      64 << 10,
		MaxIncludes:          16,
		MaxContributions:     16,
		MaxBodyBytes:         64 << 10,
		MaxOutputBytes:       256 << 10,
		MaxReasonBytes:       64 << 10,
		MaxObservedResources: 16,
		MaxSelectors:         16,
		MaxSelectorBytes:     4 << 10,
	}
}

func TestBuiltinPreservesFrontmatterSemanticsAndCompleteBodies(t *testing.T) {
	root := t.TempDir()
	writeBuiltinFile(t, root, "ai-context.md", `---
root: true
docs:
  - files: ["src/**/*.go"]
    include: ["guidance.txt"]
    message: "Keep the package boundary."
commands:
  - files: ["src/**/*.go"]
    command: "go test {fileRelative}"
    label: verify
    cwd: "repo"
---
This markdown body is deliberately not a contribution.
`)
	include := "first line é\n\nsecond line\n"
	writeBuiltinFile(t, root, "guidance.txt", include)
	request := builtinRequest([]contextapi.ObservedResource{
		observedFile("src/main.go", contextapi.ResourceOutcomeUnknown, contextapi.ConfidenceInferred),
	}, nil)
	response := ContributeBuiltin(context.Background(), request, "ai-context", root, builtinTestLimits())
	if len(response.Contributions) != 2 {
		t.Fatalf("got %d contributions, want docs and command: %#v", len(response.Contributions), response)
	}
	if !strings.Contains(response.Contributions[0].Body, "Keep the package boundary.") || !strings.Contains(response.Contributions[0].Body, include) {
		t.Fatalf("docs body lost message or complete include bytes: %q", response.Contributions[0].Body)
	}
	if strings.Contains(response.Contributions[0].Body, "This markdown body is deliberately not a contribution") {
		t.Fatal("contribution injected the whole markdown file instead of declared docs")
	}
	if got, want := response.Contributions[1].Body, "command verify (cwd: repo): go test src/main.go"; got != want {
		t.Fatalf("command guidance was not interpolated as guidance: %q, want %q", got, want)
	}
	for _, contribution := range response.Contributions {
		if contribution.Recruitment.Kind != contextapi.RecruitmentObservedFile || contribution.Recruitment.Resource != "src/main.go" {
			t.Fatalf("recruitment did not preserve the actual inferred attention: %#v", contribution.Recruitment)
		}
		if contribution.Content.UTF8Bytes != uint64(len([]byte(contribution.Body))) || contribution.Content.ID == "" || contribution.SourceRevision.Revision == "" {
			t.Fatalf("contribution lacks complete content/source identity: %#v", contribution)
		}
		if contribution.ConfigDigest != request.ConfigDigest || contribution.ProfileRevision != request.Profile.Revision {
			t.Fatalf("runtime-owned identity was not hydrated: %#v", contribution)
		}
	}
}

func TestNamedBuiltinIdentityIsPreservedAcrossContributionAndRevalidation(t *testing.T) {
	root := t.TempDir()
	writeBuiltinFile(t, root, "ai-context.md", "---\ndocs:\n  - message: named guidance\n---\n")
	request := builtinRequest([]contextapi.ObservedResource{
		observedFile("README.md", contextapi.ResourceResolved, contextapi.ConfidenceObserved),
	}, nil)
	first := ContributeBuiltin(context.Background(), request, "named-one", root, builtinTestLimits())
	second := ContributeBuiltin(context.Background(), request, "named-two", root, builtinTestLimits())
	if len(first.Contributions) != 1 || len(second.Contributions) != 1 {
		t.Fatalf("named builtin fixture did not contribute once: first=%#v second=%#v", first, second)
	}
	for _, item := range []struct {
		name     string
		response contextapi.ContributionResponse
	}{{"named-one", first}, {"named-two", second}} {
		contribution := item.response.Contributions[0]
		if contribution.Contributor != contextapi.ProviderID(item.name) || contribution.Slot.Provider != contextapi.ProviderID(item.name) || contribution.Source.Identity.Provider != contextapi.ProviderID(item.name) {
			t.Fatalf("builtin %q identity collided: %#v", item.name, contribution)
		}
		if len(contribution.Reasons) != 1 || contribution.Reasons[0].Provider != contextapi.ProviderID(item.name) {
			t.Fatalf("builtin %q reason identity collided: %#v", item.name, contribution.Reasons)
		}
		fresh, err := Revalidate(context.Background(), contextapi.ProviderID(item.name), root, contribution.Source, contribution.SourceRevision, builtinTestLimits())
		if err != nil || !fresh {
			t.Fatalf("builtin %q did not revalidate under its configured identity: fresh=%v err=%v", item.name, fresh, err)
		}
	}
}

func TestBuiltinRootStopsAncestorRecruitmentAndSupportsSelectorOnlyAttention(t *testing.T) {
	root := t.TempDir()
	writeBuiltinFile(t, root, "ai-context.md", "---\nroot: true\ndocs:\n  - message: root\n---\n")
	writeBuiltinFile(t, root, "pkg/ai-context.md", "---\nroot: true\ndocs:\n  - message: nested\n---\n")
	request := builtinRequest([]contextapi.ObservedResource{observedFile("pkg/file.go", contextapi.ResourceResolved, contextapi.ConfidenceObserved)}, nil)
	response := ContributeBuiltin(context.Background(), request, "ai-context", root, builtinTestLimits())
	if len(response.Contributions) != 1 || response.Contributions[0].Body != "nested" {
		t.Fatalf("root stopping selected the wrong manifest: %#v", response)
	}

	selectorRequest := builtinRequest(nil, []contextapi.ObservedSelector{{Raw: "deploy", ObservedAs: contextapi.SelectorToolArgument}})
	writeBuiltinFile(t, root, "ai-context.md", "---\ndocs:\n  - mentions: [deploy]\n    message: selector guidance\ncommands:\n  - command: \"run {file}\"\n---\n")
	selectorResponse := ContributeBuiltin(context.Background(), selectorRequest, "ai-context", root, builtinTestLimits())
	if len(selectorResponse.Contributions) != 1 || selectorResponse.Contributions[0].Body != "selector guidance" {
		t.Fatalf("selector-only recruitment failed or executed a file command: %#v", selectorResponse)
	}
	if selectorResponse.Contributions[0].Recruitment.Kind != contextapi.RecruitmentSelectors {
		t.Fatalf("selector recruitment was not recorded: %#v", selectorResponse.Contributions[0].Recruitment)
	}
}

func TestBuiltinTOMLUnknownMetadataAndInferredResource(t *testing.T) {
	root := t.TempDir()
	writeBuiltinFile(t, root, "ai-context.md", `---
path = "pkg"
summary = "metadata is not a rule"
[[docs]]
files = ["pkg/*.go"]
message = "TOML guidance"
[[commands]]
files = ["pkg/*.go"]
command = "go test {fileName}"
---
ignored markdown
`)
	response := ContributeBuiltin(context.Background(), builtinRequest([]contextapi.ObservedResource{
		observedFile("pkg/api.go", contextapi.ResourceOutcomeUnknown, contextapi.ConfidenceCandidate),
	}, nil), "ai-context", root, builtinTestLimits())
	if len(response.Contributions) != 2 {
		t.Fatalf("TOML manifest did not produce declared docs and command: %#v", response)
	}
	if !strings.Contains(response.Contributions[0].Body, "TOML guidance") {
		t.Fatalf("TOML docs missing: %#v", response.Contributions[0])
	}
	if response.Contributions[1].Body != "command: go test api.go" {
		t.Fatalf("TOML command was not guidance: %q", response.Contributions[1].Body)
	}
}

func TestBuiltinExcludesExplicitFailureAndHonorsWholeCallReadBudget(t *testing.T) {
	root := t.TempDir()
	writeBuiltinFile(t, root, "ai-context.md", "---\ndocs:\n  - message: root guidance\n---\n")
	failed := ContributeBuiltin(context.Background(), builtinRequest([]contextapi.ObservedResource{
		observedFile("failed.go", contextapi.ResourceFailed, contextapi.ConfidenceObserved),
	}, nil), "ai-context", root, builtinTestLimits())
	if len(failed.Contributions) != 0 {
		t.Fatalf("explicitly failed read was recruited: %#v", failed.Contributions)
	}

	writeBuiltinFile(t, root, "a/one.go", "package a\n")
	writeBuiltinFile(t, root, "b/two.go", "package b\n")
	limited := builtinTestLimits()
	limited.MaxContextFiles = 2 // a/ai-context.md miss plus the shared root manifest
	limited.MaxObservedResources = 2
	response := ContributeBuiltin(context.Background(), builtinRequest([]contextapi.ObservedResource{
		observedFile("a/one.go", contextapi.ResourceOutcomeUnknown, contextapi.ConfidenceInferred),
		observedFile("b/two.go", contextapi.ResourceOutcomeUnknown, contextapi.ConfidenceInferred),
	}, nil), "ai-context", root, limited)
	if len(response.Contributions) != 1 {
		t.Fatalf("manifest budget was multiplied per attention: got %d contributions (%#v)", len(response.Contributions), response)
	}
}

func TestBuiltinReusesIncludedBytesAcrossManifestsWithinOneCallBudget(t *testing.T) {
	root := t.TempDir()
	writeBuiltinFile(t, root, "guide.txt", "shared include\n")
	writeBuiltinFile(t, root, "ai-context.md", "---\nroot: true\ndocs:\n  - include: [guide.txt]\n    message: root\n---\n")
	writeBuiltinFile(t, root, "pkg/ai-context.md", "---\ndocs:\n  - include: [../guide.txt]\n    message: nested\n---\n")
	limits := builtinTestLimits()
	limits.MaxIncludes = 1
	response := ContributeBuiltin(context.Background(), builtinRequest([]contextapi.ObservedResource{
		observedFile("pkg/file.go", contextapi.ResourceOutcomeUnknown, contextapi.ConfidenceInferred),
	}, nil), "ai-context", root, limits)
	if len(response.Contributions) != 2 {
		t.Fatalf("expected both ancestor rules: %#v", response)
	}
	for _, contribution := range response.Contributions {
		if !strings.Contains(contribution.Body, "shared include\n") {
			t.Fatalf("included bytes were reread/bounded per manifest instead of memoized: %q", contribution.Body)
		}
	}
}

func TestBuiltinRevalidateDetectsManifestIncludeAndDeletionChanges(t *testing.T) {
	root := t.TempDir()
	writeBuiltinFile(t, root, "ai-context.md", "---\ndocs:\n  - include: [guide.txt]\n    message: stable\n---\n")
	writeBuiltinFile(t, root, "guide.txt", "one\n")
	response := ContributeBuiltin(context.Background(), builtinRequest([]contextapi.ObservedResource{
		observedFile("src/file.go", contextapi.ResourceOutcomeUnknown, contextapi.ConfidenceInferred),
	}, nil), "ai-context", root, builtinTestLimits())
	if len(response.Contributions) != 1 {
		t.Fatalf("expected one contribution for revalidation fixture: %#v", response)
	}
	contribution := response.Contributions[0]
	current, err := Revalidate(context.Background(), "ai-context", root, contribution.Source, contribution.SourceRevision, builtinTestLimits())
	if err != nil || !current {
		t.Fatalf("unchanged builtin source was not fresh: current=%v err=%v", current, err)
	}
	writeBuiltinFile(t, root, "guide.txt", "two\n")
	current, err = Revalidate(context.Background(), "ai-context", root, contribution.Source, contribution.SourceRevision, builtinTestLimits())
	if err != nil || current {
		t.Fatalf("include change did not invalidate source: current=%v err=%v", current, err)
	}
	if err := os.Remove(filepath.Join(root, "ai-context.md")); err != nil {
		t.Fatal(err)
	}
	current, err = Revalidate(context.Background(), "ai-context", root, contribution.Source, contribution.SourceRevision, builtinTestLimits())
	if err != nil || current {
		t.Fatalf("manifest deletion did not withdraw freshness: current=%v err=%v", current, err)
	}
}

func TestBuiltinBoundsReasonsAndPathContainment(t *testing.T) {
	root := t.TempDir()
	writeBuiltinFile(t, root, "ai-context.md", "---\ndocs:\n  - include: [../outside.txt]\n    message: bounded\n---\n")
	limits := builtinTestLimits()
	limits.MaxReasonBytes = 1
	response := ContributeBuiltin(context.Background(), builtinRequest([]contextapi.ObservedResource{
		observedFile("file.go", contextapi.ResourceOutcomeUnknown, contextapi.ConfidenceInferred),
	}, nil), "ai-context", root, limits)
	if len(response.Contributions) != 1 || !strings.Contains(response.Contributions[0].Body, "out-of-root") {
		t.Fatalf("out-of-root include was not represented safely: %#v", response)
	}
	if reasonBytes(response.Reasons) > limits.MaxReasonBytes {
		t.Fatalf("reasons exceeded whole-call bound: %d > %d", reasonBytes(response.Reasons), limits.MaxReasonBytes)
	}
}
