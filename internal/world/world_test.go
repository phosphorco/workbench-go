package world_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/world"
)

func TestDiscoverBasinDBCommunityPackagesClosure(t *testing.T) {
	source := &memorySource{repositories: map[string]contract.PackageScopeRepository{
		"phosphorco/basindb": repository("@basindb", map[string]contract.Include{
			"@phosphorco": {GitHub: "PhosphorCo/Community-Packages"},
		}),
		"phosphorco/community-packages": repository("@phosphorco", nil),
	}}

	discovered, err := world.Discover(subject("https://github.com/PhosphorCo/BasinDB.git"), source)
	if err != nil {
		t.Fatal(err)
	}

	want := []world.Resource{
		{Identity: "phosphorco/basindb", Designation: "@basindb", CanonicalPath: "pkg/@basindb", Declaration: source.repositories["phosphorco/basindb"]},
		{Identity: "phosphorco/community-packages", Designation: "@phosphorco", CanonicalPath: "pkg/@phosphorco", Declaration: source.repositories["phosphorco/community-packages"]},
	}
	if !reflect.DeepEqual(discovered.Resources, want) {
		t.Fatalf("resources = %#v, want %#v", discovered.Resources, want)
	}
	if !reflect.DeepEqual(source.loads, []string{"phosphorco/basindb", "phosphorco/community-packages"}) {
		t.Fatalf("loads = %#v", source.loads)
	}
}

func TestDiscoverRecursiveClosureDeduplicatesRepeatedReaches(t *testing.T) {
	source := &memorySource{repositories: map[string]contract.PackageScopeRepository{
		"example/root": repository("@root", map[string]contract.Include{
			"@left":  {GitHub: "example/left"},
			"@right": {GitHub: "example/right"},
		}),
		"example/left": repository("@left", map[string]contract.Include{
			"@shared": {GitHub: "example/shared"},
		}),
		"example/right": repository("@right", map[string]contract.Include{
			"@shared": {GitHub: "example/shared"},
		}),
		"example/shared": repository("@shared", map[string]contract.Include{
			"@root": {GitHub: "example/root"},
		}),
	}}

	discovered, err := world.Discover(subject("https://github.com/example/root", "https://github.com/EXAMPLE/root.git"), source)
	if err != nil {
		t.Fatal(err)
	}

	paths := make([]string, 0, len(discovered.Resources))
	for _, resource := range discovered.Resources {
		paths = append(paths, resource.CanonicalPath)
	}
	wantPaths := []string{"pkg/@left", "pkg/@right", "pkg/@root", "pkg/@shared"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
	if got := count(source.loads, "example/root"); got != 1 {
		t.Fatalf("root loads = %d, want 1", got)
	}
	if got := count(source.loads, "example/shared"); got != 1 {
		t.Fatalf("shared loads = %d, want 1", got)
	}
}

func TestDiscoverRejectsConflictingClaims(t *testing.T) {
	tests := []struct {
		name       string
		source     *memorySource
		entrypoint string
		kind       world.ConflictKind
		key        string
	}{
		{
			name: "one identity through conflicting designations",
			source: &memorySource{repositories: map[string]contract.PackageScopeRepository{
				"example/root": repository("@root", map[string]contract.Include{
					"@first":  {GitHub: "example/shared"},
					"@second": {GitHub: "example/shared"},
				}),
			}},
			entrypoint: "https://github.com/example/root",
			kind:       world.IdentityConflict,
			key:        "example/shared",
		},
		{
			name: "one designation claims incompatible identities",
			source: &memorySource{repositories: map[string]contract.PackageScopeRepository{
				"example/root": repository("@root", map[string]contract.Include{
					"@shared": {GitHub: "example/first"},
				}),
				"example/first": repository("@shared", map[string]contract.Include{
					"@root": {GitHub: "example/other-root"},
				}),
				"example/other-root": repository("@root", nil),
			}},
			entrypoint: "https://github.com/example/root",
			kind:       world.DesignationConflict,
			key:        "@root",
		},
		{
			name: "canonical path occupied by another identity",
			source: &memorySource{
				repositories: map[string]contract.PackageScopeRepository{
					"example/root": repository("@root", nil),
				},
				occupants: map[string]string{"pkg/@root": "example/unrelated"},
			},
			entrypoint: "https://github.com/example/root",
			kind:       world.PathConflict,
			key:        "pkg/@root",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := world.Discover(subject(test.entrypoint), test.source)
			var conflict *world.ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("error = %v, want ConflictError", err)
			}
			if conflict.Kind != test.kind || conflict.Key != test.key {
				t.Fatalf("conflict = %#v, want kind %q key %q", conflict, test.kind, test.key)
			}
		})
	}
}

func TestDiscoverLoadsOnlyRepositoryOwnedClosure(t *testing.T) {
	source := &memorySource{repositories: map[string]contract.PackageScopeRepository{
		"example/root":         repository("@root", map[string]contract.Include{"@included": {GitHub: "example/included"}}),
		"example/included":     repository("@included", nil),
		"example/unregistered": repository("@unregistered", nil),
	}}

	discovered, err := world.Discover(subject("https://github.com/example/root"), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered.Resources) != 2 {
		t.Fatalf("resource count = %d, want 2", len(discovered.Resources))
	}
	if count(source.loads, "example/unregistered") != 0 {
		t.Fatal("repository absent from Subject/includes was loaded as if from a central registry")
	}
}

type memorySource struct {
	repositories map[string]contract.PackageScopeRepository
	occupants    map[string]string
	loads        []string
}

func (source *memorySource) LoadRepository(identity string) (contract.PackageScopeRepository, error) {
	source.loads = append(source.loads, identity)
	repository, ok := source.repositories[identity]
	if !ok {
		return contract.PackageScopeRepository{}, errors.New("repository not found")
	}
	return repository, nil
}

func (source *memorySource) IdentityAt(canonicalPath string) (string, bool, error) {
	identity, ok := source.occupants[canonicalPath]
	return identity, ok, nil
}

func subject(entrypoints ...string) contract.Subject {
	return contract.Subject{
		WorkLine:    contract.WorkLine{Branch: "cole/slice", BaseBranch: "main"},
		Entrypoints: entrypoints,
	}
}

func repository(scope string, includes map[string]contract.Include) contract.PackageScopeRepository {
	if includes == nil {
		includes = map[string]contract.Include{}
	}
	return contract.PackageScopeRepository{
		Scope:    scope,
		Includes: includes,
		Packages: map[string]contract.PackagePolicy{},
	}
}

func count(values []string, wanted string) int {
	result := 0
	for _, value := range values {
		if value == wanted {
			result++
		}
	}
	return result
}
