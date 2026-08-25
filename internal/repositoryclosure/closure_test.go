package repositoryclosure_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/repositoryclosure"
)

func TestDiscoverComputesDeterministicRepositoryClosure(t *testing.T) {
	source := repositorySource{declarations: map[string]contract.PackageScopeRepository{
		"phosphorco/entry": {
			Scope: "@entry",
			Includes: map[string]contract.Include{
				"@library": {GitHub: "PhosphorCo/Library"},
			},
		},
		"phosphorco/library": {Scope: "@library"},
	}}
	subject := contract.Subject{
		WorkLine:    contract.WorkLine{Branch: "workbench/current", BaseBranch: "main"},
		Entrypoints: []string{"https://github.com/PhosphorCo/Entry.git"},
	}

	closure, err := repositoryclosure.Discover(subject, source)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pkg/@entry", "pkg/@library"}
	got := make([]string, 0, len(closure.Resources))
	for _, resource := range closure.Resources {
		got = append(got, resource.CanonicalPath)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("participating repository paths = %v, want %v", got, want)
	}
}

func TestDiscoverRefusesConflictingParticipatingRepositoryIdentity(t *testing.T) {
	source := repositorySource{declarations: map[string]contract.PackageScopeRepository{
		"phosphorco/entry": {
			Scope: "@entry",
			Includes: map[string]contract.Include{
				"@shared": {GitHub: "phosphorco/one"},
				"@other":  {GitHub: "phosphorco/two"},
			},
		},
		"phosphorco/one": {Scope: "@shared"},
		"phosphorco/two": {Scope: "@shared"},
	}}
	subject := contract.Subject{
		WorkLine:    contract.WorkLine{Branch: "workbench/current", BaseBranch: "main"},
		Entrypoints: []string{"https://github.com/phosphorco/entry"},
	}

	_, err := repositoryclosure.Discover(subject, source)
	var conflict *repositoryclosure.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflict = %v", err)
	}
}

type repositorySource struct {
	declarations map[string]contract.PackageScopeRepository
}

func (source repositorySource) LoadRepository(identity string) (contract.PackageScopeRepository, error) {
	declaration, ok := source.declarations[identity]
	if !ok {
		return contract.PackageScopeRepository{}, errors.New("missing declaration")
	}
	return declaration, nil
}

func (repositorySource) IdentityAt(string) (string, bool, error) {
	return "", false, nil
}
