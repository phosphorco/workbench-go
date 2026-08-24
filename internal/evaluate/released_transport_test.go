package evaluate

import (
	"regexp"
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

func matchesAny(patterns []string, resource string) bool {
	for _, pattern := range patterns {
		if regexp.MustCompile(pattern).MatchString(resource) {
			return true
		}
	}
	return false
}
