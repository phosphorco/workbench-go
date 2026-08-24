package world

import (
	"fmt"
	"sort"

	"github.com/phosphorco/workbench-go/internal/contract"
)

// Source is the read-only authority discovery needs to observe repository
// declarations and identities already occupying canonical paths.
type Source interface {
	LoadRepository(identity string) (contract.PackageScopeRepository, error)
	IdentityAt(canonicalPath string) (identity string, occupied bool, err error)
}

type Resource struct {
	Identity      string
	Designation   string
	CanonicalPath string
	Declaration   contract.PackageScopeRepository
}

type World struct {
	Resources []Resource
}

type ConflictKind string

const (
	IdentityConflict    ConflictKind = "identity"
	DesignationConflict ConflictKind = "designation"
	PathConflict        ConflictKind = "path"
)

type ConflictError struct {
	Kind     ConflictKind
	Key      string
	Existing string
	Claimed  string
}

func (err *ConflictError) Error() string {
	return fmt.Sprintf("%s conflict for %q: existing %q, claimed %q", err.Kind, err.Key, err.Existing, err.Claimed)
}

type claim struct {
	identity    string
	designation string
}

// Discover computes the least repository closure rooted at Subject entrypoints.
// It does not acquire repositories or mutate the observed world.
func Discover(subject contract.Subject, source Source) (World, error) {
	if source == nil {
		return World{}, fmt.Errorf("discover world: source is nil")
	}
	if err := subject.Validate(); err != nil {
		return World{}, fmt.Errorf("discover world: %w", err)
	}

	discovery := discovery{
		source:                source,
		designationByIdentity: make(map[string]string),
		identityByDesignation: make(map[string]string),
		identityByPath:        make(map[string]string),
		loaded:                make(map[string]Resource),
	}

	for _, entrypoint := range subject.Entrypoints {
		identity, err := contract.GitHubIdentity(entrypoint)
		if err != nil {
			return World{}, fmt.Errorf("discover entrypoint %q: %w", entrypoint, err)
		}
		discovery.pending = append(discovery.pending, claim{identity: identity})
	}

	for len(discovery.pending) > 0 {
		sort.Slice(discovery.pending, func(left, right int) bool {
			if discovery.pending[left].identity == discovery.pending[right].identity {
				return discovery.pending[left].designation < discovery.pending[right].designation
			}
			return discovery.pending[left].identity < discovery.pending[right].identity
		})
		next := discovery.pending[0]
		discovery.pending = discovery.pending[1:]
		if err := discovery.reach(next); err != nil {
			return World{}, err
		}
	}

	resources := make([]Resource, 0, len(discovery.loaded))
	for _, resource := range discovery.loaded {
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(left, right int) bool {
		if resources[left].CanonicalPath == resources[right].CanonicalPath {
			return resources[left].Identity < resources[right].Identity
		}
		return resources[left].CanonicalPath < resources[right].CanonicalPath
	})
	return World{Resources: resources}, nil
}

type discovery struct {
	source                Source
	pending               []claim
	designationByIdentity map[string]string
	identityByDesignation map[string]string
	identityByPath        map[string]string
	loaded                map[string]Resource
}

func (state *discovery) reach(next claim) error {
	if err := state.claimDesignation(next.identity, next.designation); err != nil {
		return err
	}
	if _, ok := state.loaded[next.identity]; ok {
		return nil
	}

	declaration, err := state.source.LoadRepository(next.identity)
	if err != nil {
		return fmt.Errorf("load repository %q: %w", next.identity, err)
	}
	if err := declaration.Validate(); err != nil {
		return fmt.Errorf("validate repository %q: %w", next.identity, err)
	}
	if err := state.claimDesignation(next.identity, declaration.Scope); err != nil {
		return err
	}

	canonicalPath := declaration.CanonicalPath()
	if existing, ok := state.identityByPath[canonicalPath]; ok && existing != next.identity {
		return conflict(PathConflict, canonicalPath, existing, next.identity)
	}
	observed, occupied, err := state.source.IdentityAt(canonicalPath)
	if err != nil {
		return fmt.Errorf("observe canonical path %q: %w", canonicalPath, err)
	}
	if occupied {
		observed, err = normalizeGitHubName(observed)
		if err != nil {
			return fmt.Errorf("observe canonical path %q identity: %w", canonicalPath, err)
		}
		if observed != next.identity {
			return conflict(PathConflict, canonicalPath, observed, next.identity)
		}
	}
	state.identityByPath[canonicalPath] = next.identity

	state.loaded[next.identity] = Resource{
		Identity:      next.identity,
		Designation:   declaration.Scope,
		CanonicalPath: canonicalPath,
		Declaration:   cloneDeclaration(declaration),
	}

	designations := make([]string, 0, len(declaration.Includes))
	for designation := range declaration.Includes {
		designations = append(designations, designation)
	}
	sort.Strings(designations)
	for _, designation := range designations {
		include := declaration.Includes[designation]
		identity, err := normalizeGitHubName(include.GitHub)
		if err != nil {
			return fmt.Errorf("repository %q include %q: %w", next.identity, designation, err)
		}
		if err := state.claimDesignation(identity, designation); err != nil {
			return err
		}
		state.pending = append(state.pending, claim{identity: identity, designation: designation})
	}
	return nil
}

func (state *discovery) claimDesignation(identity string, designation string) error {
	if designation == "" {
		return nil
	}
	if existing, ok := state.designationByIdentity[identity]; ok && existing != designation {
		return conflict(IdentityConflict, identity, existing, designation)
	}
	if existing, ok := state.identityByDesignation[designation]; ok && existing != identity {
		return conflict(DesignationConflict, designation, existing, identity)
	}
	state.designationByIdentity[identity] = designation
	state.identityByDesignation[designation] = identity
	return nil
}

func normalizeGitHubName(name string) (string, error) {
	identity, err := contract.GitHubIdentity("https://github.com/" + name)
	if err != nil {
		return "", fmt.Errorf("invalid GitHub repository %q: %w", name, err)
	}
	return identity, nil
}

func conflict(kind ConflictKind, key string, existing string, claimed string) error {
	return &ConflictError{Kind: kind, Key: key, Existing: existing, Claimed: claimed}
}

func cloneDeclaration(declaration contract.PackageScopeRepository) contract.PackageScopeRepository {
	includes := make(map[string]contract.Include, len(declaration.Includes))
	for designation, include := range declaration.Includes {
		include.Skills = cloneSkillPolicy(include.Skills)
		includes[designation] = include
	}
	packages := make(map[string]contract.PackagePolicy, len(declaration.Packages))
	for name, policy := range declaration.Packages {
		policy.RequiredButNotReferenced = cloneStringMap(policy.RequiredButNotReferenced)
		policy.PeerDependencies = cloneStringMap(policy.PeerDependencies)
		policy.OptionalDependencies = cloneStringMap(policy.OptionalDependencies)
		packages[name] = policy
	}
	return contract.PackageScopeRepository{
		Scope:    declaration.Scope,
		Includes: includes,
		Packages: packages,
	}
}

func cloneSkillPolicy(policy *contract.SkillPolicy) *contract.SkillPolicy {
	if policy == nil {
		return nil
	}
	return &contract.SkillPolicy{
		Editing:   cloneSkillSelection(policy.Editing),
		Workbench: cloneSkillSelection(policy.Workbench),
	}
}

func cloneSkillSelection(selection *contract.SkillSelection) *contract.SkillSelection {
	if selection == nil {
		return nil
	}
	return &contract.SkillSelection{
		All:     selection.All,
		Domains: append([]string(nil), selection.Domains...),
		Names:   append([]string(nil), selection.Names...),
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
