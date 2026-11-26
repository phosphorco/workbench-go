package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
	workbenchversion "github.com/phosphorco/workbench-go/internal/version"
)

// supportsRepositoryLayout reports whether a contract version carries the 0.13
// layout semantics: declared directories and source roots, in-repository
// scopes, verbatim versions, and TypeScript-include-shaped observation.
// Earlier versions keep their released observation byte for byte.
func supportsRepositoryLayout(version string) bool {
	return version == "0.13.0" || version == "0.14.0" || version == workbenchversion.CurrentContractVersion
}

// inRepositoryScopeDeclarationPath is the one place a Repository's scope is declared.
func inRepositoryScopeDeclarationPath(scope string) string {
	return "pkg/" + scope + "/workbench.pkl"
}

// mergeInRepositoryScopes evaluates every pkg/<scope>/workbench.pkl named by a
// Repository declaration on its own, against the PackageScopeRepository
// contract of the same version, and merges its packages (placed at
// pkg/<scope>/<leaf>) and includes into the Repository declaration. No scope
// file can import another module: each is an independent evaluation.
func (source *discoverySource) mergeInRepositoryScopes(github, checkout string, declaration *contract.Declaration) error {
	if len(declaration.Scopes) == 0 {
		return nil
	}
	if declaration.Shape.Kind != contract.RepositoryShape || !supportsRepositoryLayout(source.version) {
		return fmt.Errorf("repository %q: in-repository scopes require a 0.13.0 or later Repository declaration", github)
	}
	for name := range declaration.Packages {
		for _, scope := range declaration.Scopes {
			if strings.HasPrefix(name, scope+"/") {
				return fmt.Errorf("repository %q package %q is in listed scope %s; declare it in %s, not in the repository workbench.pkl", github, name, scope, inRepositoryScopeDeclarationPath(scope))
			}
		}
	}
	packages := make(map[string]contract.PackagePolicy, len(declaration.Packages))
	for name, policy := range declaration.Packages {
		packages[name] = policy
	}
	includes := make(map[string]contract.ResourceInclude, len(declaration.Includes))
	for designation, include := range declaration.Includes {
		includes[designation] = include
	}
	for _, scope := range declaration.Scopes {
		relative := inRepositoryScopeDeclarationPath(scope)
		scoped, err := source.loadInRepositoryScope(checkout, scope)
		if err != nil {
			return fmt.Errorf("repository %q scope %s: %w", github, relative, err)
		}
		names := make([]string, 0, len(scoped.Packages))
		for name := range scoped.Packages {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if _, declared := packages[name]; declared {
				return fmt.Errorf("repository %q package %q is declared both by the repository workbench.pkl and by %s; declare it once", github, name, relative)
			}
			leaf, err := scoped.PackageDirectory(name)
			if err != nil {
				return fmt.Errorf("repository %q scope %s: %w", github, relative, err)
			}
			policy := scoped.Packages[name]
			policy.Directory = "pkg/" + scope + "/" + leaf
			packages[name] = policy
		}
		for designation, include := range scoped.Includes {
			includes[designation] = mergeResourceInclude(includes[designation], include)
		}
	}
	declaration.Packages = packages
	declaration.Includes = includes
	return declaration.Validate()
}

func (source *discoverySource) loadInRepositoryScope(checkout, scope string) (contract.Declaration, error) {
	relative := inRepositoryScopeDeclarationPath(scope)
	encoded, err := readRealFileWithin(checkout, relative)
	if err != nil {
		return contract.Declaration{}, err
	}
	filename, err := amendedFilename(encoded)
	if err != nil {
		return contract.Declaration{}, err
	}
	if filename != "PackageScopeRepository.pkl" {
		return contract.Declaration{}, fmt.Errorf("must amend the PackageScopeRepository.pkl contract, not %q", filename)
	}
	schema, version, err := schemaForSource(encoded, filename)
	if err != nil {
		return contract.Declaration{}, err
	}
	if version != source.version {
		return contract.Declaration{}, fmt.Errorf("declaration is %s, want exact %s like its Repository", version, source.version)
	}
	scoped, err := source.evaluatorVersioned.EvaluatePackageScopeDeclarationV030(source.ctx, encoded, schema)
	if err != nil {
		return contract.Declaration{}, fmt.Errorf("evaluate: %w", err)
	}
	if scoped.Shape.Scope != scope {
		return contract.Declaration{}, fmt.Errorf("declares scope %q, want %q", scoped.Shape.Scope, scope)
	}
	if len(scoped.Buildables) != 0 {
		return contract.Declaration{}, fmt.Errorf("declares buildables; an in-repository scope declares packages and includes only, so declare buildables in the repository workbench.pkl")
	}
	return scoped, nil
}

// mergeResourceInclude unions skill selections. Setup already unions every
// include's selection per consumer, so a merged include selects exactly what
// the root and scope includes would have selected separately.
func mergeResourceInclude(left, right contract.ResourceInclude) contract.ResourceInclude {
	if left.Skills == nil {
		return right
	}
	if right.Skills == nil {
		return left
	}
	merged := contract.SkillPolicy{}
	merged.Editing = mergeOptionalSelection(left.Skills.Editing, right.Skills.Editing)
	merged.Workbench = mergeOptionalSelection(left.Skills.Workbench, right.Skills.Workbench)
	return contract.ResourceInclude{Skills: &merged}
}

func mergeOptionalSelection(left, right *contract.SkillSelection) *contract.SkillSelection {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	merged := mergeSelection(*left, *right)
	return &merged
}

// readRealFileWithin reads root/relative only when every component is a real
// directory or regular file: no symlink, so the bytes cannot come from outside.
func readRealFileWithin(root, relative string) ([]byte, error) {
	path, err := realPathWithin(root, relative, regularFile)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

type pathKind uint8

const (
	anyKind pathKind = iota
	directoryKind
	regularFile
)

// realPathWithin resolves a slash-separated relative path below root, refusing
// a missing component, a symlink anywhere on the path, or a final entry of the
// wrong kind.
func realPathWithin(root, relative string, kind pathKind) (string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("observe checkout root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", fmt.Errorf("checkout root must be a real directory, not a symlink")
	}
	segments := strings.Split(relative, "/")
	current := root
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%q is not a confined relative path", relative)
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%q does not exist", relative)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%q is reached through symlink %q; it must be a real path inside the checkout", relative, strings.Join(segments[:index+1], "/"))
		}
		last := index == len(segments)-1
		switch {
		case !last && !info.IsDir():
			return "", fmt.Errorf("%q is not a directory", strings.Join(segments[:index+1], "/"))
		case last && kind == directoryKind && !info.IsDir():
			return "", fmt.Errorf("%q is not a directory", relative)
		case last && kind == anyKind && !info.IsDir() && !info.Mode().IsRegular():
			return "", fmt.Errorf("%q is neither a directory nor a regular file", relative)
		case last && kind == regularFile && !info.Mode().IsRegular():
			return "", fmt.Errorf("%q is not a regular file", relative)
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve checkout root: %w", err)
	}
	if err := requireRealDirectoryWithin(realRoot, filepath.Dir(current)); err != nil {
		return "", err
	}
	return current, nil
}

// locateDeclaredPackage places a package at its declared directory. The
// directory and every declared source root must be real directories inside the
// checkout, reached without symlinks.
func locateDeclaredPackage(resourceRoot string, policy contract.PackagePolicy) (string, error) {
	packageRoot, err := realPathWithin(resourceRoot, policy.Directory, directoryKind)
	if err != nil {
		return "", fmt.Errorf("declared directory: %w", err)
	}
	for _, sourceRoot := range policy.EffectiveSourceRoots() {
		if _, err := realPathWithin(resourceRoot, policy.Directory+"/"+sourceRoot, directoryKind); err != nil {
			return "", fmt.Errorf("declared source root %q: %w", sourceRoot, err)
		}
	}
	return packageRoot, nil
}

// requireRealPathComponents refuses a symlink or non-directory among the
// intermediate components of a slash-separated path below root. The final
// component is checked by the caller; a missing component wraps os.ErrNotExist.
func requireRealPathComponents(root, relative string) error {
	segments := strings.Split(relative, "/")
	current := root
	for index, segment := range segments[:len(segments)-1] {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%q must be a real directory, not a symlink", strings.Join(segments[:index+1], "/"))
		}
	}
	return nil
}
