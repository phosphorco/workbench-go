package evaluate

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestReleasedContractGrantsOnlyItsExactPackageTransport(t *testing.T) {
	const module = "package://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0#/WorkbenchSubject.pkl"
	contract, err := ReleasedContract(module)
	if err != nil {
		t.Fatal(err)
	}

	patterns := exactResourcePatterns(contract.packageResources...)
	for _, resource := range []string{
		"prop:pkl.outputFormat",
		"https://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0",
		"https://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0.zip",
	} {
		if !matchesAny(patterns, resource) {
			t.Errorf("designated resource %q was denied", resource)
		}
	}

	for _, resource := range []string{
		"env:HOME",
		"file:/etc/hostname",
		"https://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.1",
		"https://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0.zip?download=1",
		"https://github.com/phosphorco/workbench-go/releases/download/0.1.0/adjacent@0.1.0.zip",
		"https://example.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0.zip",
	} {
		if matchesAny(patterns, resource) {
			t.Errorf("undesignated resource %q was allowed", resource)
		}
	}
}

func TestReleasedContractGrantsOnlyItsExactBackingModule(t *testing.T) {
	const module = "package://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0#/WorkbenchSubject.pkl"
	contract, err := ReleasedContract(module)
	if err != nil {
		t.Fatal(err)
	}
	patterns := contractModulePatterns(contract.uri)
	for _, allowed := range []string{
		module,
		"package://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0#/pkl/WorkbenchSubject.pkl",
	} {
		if !matchesAny(patterns, allowed) {
			t.Errorf("designated module %q was denied", allowed)
		}
	}
	for _, denied := range []string{
		"package://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0#/PackageScopeRepository.pkl",
		"package://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0#/pkl/PackageScopeRepository.pkl",
		"package://github.com/phosphorco/workbench-go/releases/download/0.1.1/workbench@0.1.1#/pkl/WorkbenchSubject.pkl",
	} {
		if matchesAny(patterns, denied) {
			t.Errorf("undesignated module %q was allowed", denied)
		}
	}
}

func TestReleaseAssetRedirectPatternAttenuatesToObservedAsset(t *testing.T) {
	const (
		resource = "https://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0.zip"
		asset    = "https://release-assets.githubusercontent.com/github-production-release-asset/1344586756/586fc85b-1200-4731-be01-1c37d7272891"
		query    = "sp=r&sv=2018-11-09&sr=b&spr=https&se=future&rscd=attachment%3B+filename%3Dworkbench%400.1.0.zip&rsct=application%2Foctet-stream&sig=signature&jwt=token&response-content-disposition=attachment%3B%20filename%3Dworkbench%400.1.0.zip"
	)
	pattern, err := releaseAssetRedirectPattern(resource, asset+"?"+query)
	if err != nil {
		t.Fatal(err)
	}
	allowed := regexp.MustCompile(pattern)
	if !allowed.MatchString(asset + "?" + strings.ReplaceAll(query, "signature", "fresh-signature")) {
		t.Fatal("fresh signature for the observed asset path was denied")
	}
	for _, undesignated := range []string{
		strings.Replace(asset, "586fc85b-1200-4731-be01-1c37d7272891", "7f9f990a-de03-418d-81f1-0cb0cd2b9917", 1) + "?" + query,
		strings.Replace(asset, "release-assets.githubusercontent.com", "objects.githubusercontent.com", 1) + "?" + query,
		asset + "?" + query + "&adjacent=true",
	} {
		if allowed.MatchString(undesignated) {
			t.Errorf("undesignated redirect %q was allowed", undesignated)
		}
	}
}

func TestReleaseAssetRedirectPatternRejectsInvalidAuthority(t *testing.T) {
	const (
		resource = "https://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0"
		asset    = "https://release-assets.githubusercontent.com/github-production-release-asset/1344586756/7f9f990a-de03-418d-81f1-0cb0cd2b9917"
		query    = "sp=r&sr=b&spr=https&sig=signature&jwt=token&response-content-disposition=attachment%3B%20filename%3Dworkbench%400.1.0"
	)
	for name, location := range map[string]string{
		"scheme":         strings.Replace(asset, "https://", "http://", 1) + "?" + query,
		"host":           strings.Replace(asset, "release-assets.githubusercontent.com", "example.com", 1) + "?" + query,
		"path":           strings.Replace(asset, "github-production-release-asset", "user-content", 1) + "?" + query,
		"write grant":    asset + "?" + strings.Replace(query, "sp=r", "sp=rw", 1),
		"missing sig":    asset + "?" + strings.Replace(query, "&sig=signature", "", 1),
		"wrong filename": asset + "?" + strings.Replace(query, "workbench%400.1.0", "adjacent%400.1.0", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := releaseAssetRedirectPattern(resource, location); err == nil {
				t.Fatalf("invalid redirect %q was accepted", location)
			}
		})
	}
}

func TestReleasedResourceObservationDoesNotFollowRedirect(t *testing.T) {
	const asset = "https://release-assets.githubusercontent.com/github-production-release-asset/1344586756/7f9f990a-de03-418d-81f1-0cb0cd2b9917?sp=r&sr=b&spr=https&sig=signature&jwt=token&response-content-disposition=attachment%3B%20filename%3Dworkbench%400.1.0"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodHead {
			t.Errorf("request method = %q, want HEAD", request.Method)
		}
		response.Header().Set("Location", asset)
		response.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	patterns, err := releasedResourcePatterns(t.Context(), []string{server.URL + "/workbench@0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !matchesAny(patterns, asset) {
		t.Fatal("observed exact release asset redirect was denied")
	}
}

func matchesAny(patterns []string, resource string) bool {
	for _, pattern := range patterns {
		if regexp.MustCompile(pattern).MatchString(resource) {
			return true
		}
	}
	return false
}
