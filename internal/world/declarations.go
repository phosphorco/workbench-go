package world

import (
	"fmt"
	"sort"

	"github.com/phosphorco/workbench-go/internal/contract"
)

// DeclarationSource is the read-only authority needed to assemble the closed
// 0.2 resource-shape world. Repositories are addressed by transport identity;
// their declarations derive the resource identity and canonical placement.
type DeclarationSource interface {
	LoadDeclaration(github string) (contract.Declaration, error)
	IdentityAt(canonicalPath string) (identity string, occupied bool, err error)
}

// DeclaredResource keeps acquisition identity separate from the identity and
// placement derived by its released resource shape.
type DeclaredResource struct {
	Identity      string
	GitHub        string
	CanonicalPath string
	Declaration   contract.Declaration
}

type DeclaredWorld struct {
	Resources []DeclaredResource
}

// DiscoverDeclarations computes the least closure of 0.2 declarations. It
// neither acquires repositories nor changes an existing checkout.
func DiscoverDeclarations(subject contract.Subject, source DeclarationSource) (DeclaredWorld, error) {
	if source == nil {
		return DeclaredWorld{}, fmt.Errorf("discover declared world: source is nil")
	}
	if err := subject.Validate(); err != nil {
		return DeclaredWorld{}, fmt.Errorf("discover declared world: %w", err)
	}

	pending := make([]string, 0, len(subject.Entrypoints))
	for _, entrypoint := range subject.Entrypoints {
		github, err := contract.GitHubIdentity(entrypoint)
		if err != nil {
			return DeclaredWorld{}, fmt.Errorf("discover entrypoint %q: %w", entrypoint, err)
		}
		pending = append(pending, github)
	}

	loadedByGitHub := make(map[string]DeclaredResource)
	githubByIdentity := make(map[string]string)
	identityByPath := make(map[string]string)
	for len(pending) > 0 {
		sort.Strings(pending)
		github := pending[0]
		pending = pending[1:]
		if _, loaded := loadedByGitHub[github]; loaded {
			continue
		}

		declaration, err := source.LoadDeclaration(github)
		if err != nil {
			return DeclaredWorld{}, fmt.Errorf("load declaration %q: %w", github, err)
		}
		if err := declaration.Validate(); err != nil {
			return DeclaredWorld{}, fmt.Errorf("validate declaration %q: %w", github, err)
		}
		identity, err := declaration.Identity(github)
		if err != nil {
			return DeclaredWorld{}, fmt.Errorf("derive identity %q: %w", github, err)
		}
		canonicalPath, err := declaration.CanonicalPath(github)
		if err != nil {
			return DeclaredWorld{}, fmt.Errorf("derive canonical path %q: %w", github, err)
		}

		if existing, claimed := githubByIdentity[identity]; claimed && existing != github {
			return DeclaredWorld{}, conflict(IdentityConflict, identity, existing, github)
		}
		if existing, claimed := identityByPath[canonicalPath]; claimed && existing != identity {
			return DeclaredWorld{}, conflict(PathConflict, canonicalPath, existing, identity)
		}
		observed, occupied, err := source.IdentityAt(canonicalPath)
		if err != nil {
			return DeclaredWorld{}, fmt.Errorf("observe canonical path %q: %w", canonicalPath, err)
		}
		if occupied && observed != identity {
			return DeclaredWorld{}, conflict(PathConflict, canonicalPath, observed, identity)
		}

		githubByIdentity[identity] = github
		identityByPath[canonicalPath] = identity
		loadedByGitHub[github] = DeclaredResource{
			Identity:      identity,
			GitHub:        github,
			CanonicalPath: canonicalPath,
			Declaration:   cloneV020Declaration(declaration),
		}

		includes := make([]string, 0, len(declaration.Includes))
		for includedGitHub := range declaration.Includes {
			normalized, err := contract.NormalizeGitHubRepository(includedGitHub)
			if err != nil {
				return DeclaredWorld{}, fmt.Errorf("declaration %q include %q: %w", github, includedGitHub, err)
			}
			includes = append(includes, normalized)
		}
		sort.Strings(includes)
		pending = append(pending, includes...)
	}

	resources := make([]DeclaredResource, 0, len(loadedByGitHub))
	for _, resource := range loadedByGitHub {
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(left, right int) bool {
		if resources[left].CanonicalPath == resources[right].CanonicalPath {
			return resources[left].Identity < resources[right].Identity
		}
		return resources[left].CanonicalPath < resources[right].CanonicalPath
	})
	return DeclaredWorld{Resources: resources}, nil
}

func cloneV020Declaration(declaration contract.Declaration) contract.Declaration {
	includes := make(map[string]contract.ResourceInclude, len(declaration.Includes))
	for github, include := range declaration.Includes {
		include.Skills = cloneSkillPolicy(include.Skills)
		includes[github] = include
	}
	packages := make(map[string]contract.PackagePolicy, len(declaration.Packages))
	for name, policy := range declaration.Packages {
		policy.RequiredButNotReferenced = cloneStringMap(policy.RequiredButNotReferenced)
		policy.PeerDependencies = cloneStringMap(policy.PeerDependencies)
		policy.OptionalDependencies = cloneStringMap(policy.OptionalDependencies)
		packages[name] = policy
	}
	return contract.Declaration{Shape: declaration.Shape, Includes: includes, Packages: packages}
}
