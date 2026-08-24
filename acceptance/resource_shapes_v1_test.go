package acceptance

import (
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
)

func TestClosedResourceShapesV1DeriveIdentityAndCanonicalPlacement(t *testing.T) {
	tests := []struct {
		name        string
		declaration contract.Declaration
		github      string
		identity    string
		path        string
	}{
		{"package scope", contract.Declaration{Shape: contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"}}, "phosphorco/workbench-fixture-entry", "@workbench-entry", "pkg/@workbench-entry"},
		{"repository", contract.Declaration{Shape: contract.ResourceShape{Kind: contract.RepositoryShape}}, "phosphorco/workbench-fixture-library", "phosphorco/workbench-fixture-library", "repos/workbench-fixture-library"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity, err := test.declaration.Identity(test.github)
			if err != nil {
				t.Fatal(err)
			}
			path, err := test.declaration.CanonicalPath(test.github)
			if err != nil {
				t.Fatal(err)
			}
			if identity != test.identity || path != test.path {
				t.Fatalf("derived (%q, %q), want (%q, %q)", identity, path, test.identity, test.path)
			}
		})
	}
	if err := (contract.ResourceShape{Kind: "plugin"}).Validate(); err == nil {
		t.Fatal("unreleased resource shape was accepted")
	}
	if err := (contract.ResourceShape{Kind: contract.RepositoryShape, Scope: "@hidden"}).Validate(); err == nil {
		t.Fatal("Repository shape accepted PackageScope authority")
	}
}
