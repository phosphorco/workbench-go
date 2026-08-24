package world_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/world"
)

func TestDiscoverDeclarationsComposesBothClosedShapes(t *testing.T) {
	source := &declarationSource{declarations: map[string]contract.Declaration{
		"phosphorco/workbench-fixture-entry": {
			Shape: contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"},
			Includes: map[string]contract.ResourceInclude{
				"PhosphorCo/Workbench-Fixture-Library": {},
			},
			Packages: map[string]contract.PackagePolicy{},
		},
		"phosphorco/workbench-fixture-library": {
			Shape:    contract.ResourceShape{Kind: contract.RepositoryShape},
			Includes: map[string]contract.ResourceInclude{},
			Packages: map[string]contract.PackagePolicy{},
		},
	}}

	discovered, err := world.DiscoverDeclarations(subject("https://github.com/PhosphorCo/Workbench-Fixture-Entry.git"), source)
	if err != nil {
		t.Fatal(err)
	}
	want := []world.DeclaredResource{
		{Identity: "@workbench-entry", GitHub: "phosphorco/workbench-fixture-entry", CanonicalPath: "pkg/@workbench-entry", Declaration: source.declarations["phosphorco/workbench-fixture-entry"]},
		{Identity: "phosphorco/workbench-fixture-library", GitHub: "phosphorco/workbench-fixture-library", CanonicalPath: "repos/workbench-fixture-library", Declaration: source.declarations["phosphorco/workbench-fixture-library"]},
	}
	// Discovery snapshots declarations; normalize the authored include key only
	// for acquisition, without rewriting repository-owned meaning.
	if !reflect.DeepEqual(discovered.Resources, want) {
		t.Fatalf("resources = %#v, want %#v", discovered.Resources, want)
	}
	if !reflect.DeepEqual(source.loads, []string{"phosphorco/workbench-fixture-entry", "phosphorco/workbench-fixture-library"}) {
		t.Fatalf("loads = %#v", source.loads)
	}
}

func TestDiscoverDeclarationsRejectsDerivedIdentityAndPathConflicts(t *testing.T) {
	tests := []struct {
		name   string
		source *declarationSource
		kind   world.ConflictKind
	}{
		{
			name: "two package-scope repositories derive one identity",
			source: &declarationSource{declarations: map[string]contract.Declaration{
				"example/root":  {Shape: contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@same"}, Includes: map[string]contract.ResourceInclude{"example/other": {}}, Packages: map[string]contract.PackagePolicy{}},
				"example/other": {Shape: contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@same"}, Includes: map[string]contract.ResourceInclude{}, Packages: map[string]contract.PackagePolicy{}},
			}},
			kind: world.IdentityConflict,
		},
		{
			name: "repository names collide in canonical placement",
			source: &declarationSource{declarations: map[string]contract.Declaration{
				"example/root": {Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Includes: map[string]contract.ResourceInclude{"another/root": {}}, Packages: map[string]contract.PackagePolicy{}},
				"another/root": {Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Includes: map[string]contract.ResourceInclude{}, Packages: map[string]contract.PackagePolicy{}},
			}},
			kind: world.PathConflict,
		},
		{
			name: "occupied canonical path belongs to another identity",
			source: &declarationSource{
				declarations: map[string]contract.Declaration{"example/root": {Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Includes: map[string]contract.ResourceInclude{}, Packages: map[string]contract.PackagePolicy{}}},
				occupants:    map[string]string{"repos/root": "someone/else"},
			},
			kind: world.PathConflict,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := world.DiscoverDeclarations(subject("https://github.com/example/root"), test.source)
			var conflict *world.ConflictError
			if !errors.As(err, &conflict) || conflict.Kind != test.kind {
				t.Fatalf("error = %#v, want %s ConflictError", err, test.kind)
			}
		})
	}
}

func TestDiscoverDeclarationsLoadsOnlyDeclaredClosureAndSnapshotsIt(t *testing.T) {
	root := contract.Declaration{
		Shape:    contract.ResourceShape{Kind: contract.RepositoryShape},
		Includes: map[string]contract.ResourceInclude{"example/included": {Skills: &contract.SkillPolicy{}}},
		Packages: map[string]contract.PackagePolicy{},
	}
	source := &declarationSource{declarations: map[string]contract.Declaration{
		"example/root":        root,
		"example/included":    {Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Includes: map[string]contract.ResourceInclude{}, Packages: map[string]contract.PackagePolicy{}},
		"example/unreachable": {Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Includes: map[string]contract.ResourceInclude{}, Packages: map[string]contract.PackagePolicy{}},
	}}

	discovered, err := world.DiscoverDeclarations(subject("https://github.com/example/root"), source)
	if err != nil {
		t.Fatal(err)
	}
	delete(root.Includes, "example/included")
	if len(discovered.Resources) != 2 || discovered.Resources[1].Declaration.Includes["example/included"].Skills == nil {
		t.Fatalf("discovered declaration was aliased or closure was wrong: %#v", discovered.Resources)
	}
	for _, loaded := range source.loads {
		if loaded == "example/unreachable" {
			t.Fatal("unreachable repository was loaded as a central registry entry")
		}
	}
}

type declarationSource struct {
	declarations map[string]contract.Declaration
	occupants    map[string]string
	loads        []string
}

func (source *declarationSource) LoadDeclaration(github string) (contract.Declaration, error) {
	source.loads = append(source.loads, github)
	declaration, ok := source.declarations[github]
	if !ok {
		return contract.Declaration{}, errors.New("declaration not found")
	}
	return declaration, nil
}

func (source *declarationSource) IdentityAt(canonicalPath string) (string, bool, error) {
	identity, ok := source.occupants[canonicalPath]
	return identity, ok, nil
}
