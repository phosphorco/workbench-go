package workspace

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Root reassembly problems reported for closures with verbatim packages.
const (
	// UnresolvableCatalogReason: a catalog: reference needs a catalog in the
	// root manifest, and the generated Workbench root carries none.
	UnresolvableCatalogReason = "unresolvable catalog"
	// NonExactReason: an exact package declares a non-exact version.
	NonExactReason = "non-exact"
	// ConflictingReason: packages declare one dependency with different specifiers.
	ConflictingReason = "conflicting"
	// NonExternalReason: the TypeScript authority is not an external package.
	NonExternalReason = "non-external"
)

// RootDependencyConflictsError reports every external dependency that the
// generated root cannot reassemble from a closure containing verbatim packages:
// catalog references it cannot resolve, non-exact versions in exact packages,
// and dependencies declared with different specifiers. Reporting them together
// lets an author see the whole set at once.
type RootDependencyConflictsError struct {
	Conflicts []*RootDependencyAuthorityError
}

func (failure *RootDependencyConflictsError) Error() string {
	lines := make([]string, 0, len(failure.Conflicts))
	for _, conflict := range failure.Conflicts {
		description := describeRootProblem(conflict.Reason)
		if conflict.Reason != ConflictingReason && distinctSpecifiers(conflict.Authorities) > 1 {
			description += "; its specifiers also disagree"
		}
		lines = append(lines, fmt.Sprintf("  %q (%s): %s", conflict.Dependency, description, summarizeAuthorities(conflict.Authorities)))
	}
	return fmt.Sprintf("root external dependencies cannot be reassembled for %d dependency(ies); remedy: give each external dependency one specifier the generated root can resolve (no catalog: references, since the generated root carries no catalog; exact versions in exact packages):\n%s", len(lines), strings.Join(lines, "\n"))
}

func distinctSpecifiers(authorities []RootDependencyAuthority) int {
	specifiers := make(map[string]struct{}, len(authorities))
	for _, authority := range authorities {
		specifiers[authority.Version] = struct{}{}
	}
	return len(specifiers)
}

func describeRootProblem(reason string) string {
	switch reason {
	case UnresolvableCatalogReason:
		return "catalog indirection the generated Workbench root cannot resolve; it carries no catalog"
	case NonExactReason:
		return "non-exact version in a package with exact dependencyVersions"
	case NonExternalReason:
		return "TypeScript authority is not an external package"
	default:
		return "declared with different specifiers"
	}
}

// summarizeAuthorities groups declarations by specifier, least common first,
// naming at most three declaring packages per specifier.
func summarizeAuthorities(authorities []RootDependencyAuthority) string {
	bySpecifier := make(map[string][]string)
	for _, authority := range authorities {
		bySpecifier[authority.Version] = append(bySpecifier[authority.Version], authority.Package+" "+authority.Class)
	}
	specifiers := make([]string, 0, len(bySpecifier))
	for specifier := range bySpecifier {
		specifiers = append(specifiers, specifier)
	}
	sort.Slice(specifiers, func(left, right int) bool {
		if len(bySpecifier[specifiers[left]]) != len(bySpecifier[specifiers[right]]) {
			return len(bySpecifier[specifiers[left]]) < len(bySpecifier[specifiers[right]])
		}
		return specifiers[left] < specifiers[right]
	})
	parts := make([]string, 0, len(specifiers))
	for _, specifier := range specifiers {
		declarers := bySpecifier[specifier]
		shown := declarers
		if len(shown) > 3 {
			shown = append(append([]string(nil), declarers[:3]...), fmt.Sprintf("%d more", len(declarers)-3))
		}
		parts = append(parts, fmt.Sprintf("%q by %s", specifier, strings.Join(shown, ", ")))
	}
	return strings.Join(parts, "; ")
}

func verbatimPackages(packages []Package) map[string]bool {
	verbatim := make(map[string]bool)
	for _, pkg := range packages {
		if pkg.Policy.Verbatim() {
			verbatim[pkg.Name] = true
		}
	}
	return verbatim
}

// verbatimRootExternalDependencies reassembles root external dependencies for
// a closure with at least one verbatim package, collecting every problem
// instead of stopping at the first.
func verbatimRootExternalDependencies(packages []Package, verbatim map[string]bool) (map[string]string, error) {
	typeScript := make([]TypeScriptAuthority, 0)
	for _, pkg := range packages {
		if version, exists := pkg.Policy.DevDependencies["typescript"]; exists {
			typeScript = append(typeScript, TypeScriptAuthority{Package: pkg.Name, Version: version})
		}
	}
	if len(typeScript) == 0 {
		return nil, &TypeScriptAuthorityError{Reason: "missing"}
	}
	problems := make([]*RootDependencyAuthorityError, 0)
	nonExternal := make([]RootDependencyAuthority, 0)
	for _, authority := range typeScript {
		if isWorkspaceProtocol(authority.Version) {
			nonExternal = append(nonExternal, RootDependencyAuthority{Package: authority.Package, Class: "devDependencies", Name: "typescript", Version: authority.Version})
		}
	}
	if len(nonExternal) != 0 {
		problems = append(problems, &RootDependencyAuthorityError{Dependency: "typescript", Reason: NonExternalReason, Authorities: nonExternal})
	}

	authorities := make(map[string][]RootDependencyAuthority)
	for _, pkg := range packages {
		for _, class := range dependencyClasses(pkg.Policy) {
			for _, name := range sortedMapKeys(class.values) {
				version := class.values[name]
				if isWorkspaceProtocol(version) {
					continue
				}
				if verbatim[pkg.Name] {
					version = rootRelativeSpecifier(pkg.Directory, version)
				}
				authorities[name] = append(authorities[name], RootDependencyAuthority{
					Package: pkg.Name, Class: class.name, Name: name, Version: version,
				})
			}
		}
	}
	root := make(map[string]string, len(authorities))
	for _, name := range sortedMapKeysOfAuthorities(authorities) {
		declarations := authorities[name]
		catalog, nonExact, conflicting := false, false, false
		for _, declaration := range declarations {
			switch {
			case strings.HasPrefix(declaration.Version, "catalog:"):
				catalog = true
			case !verbatim[declaration.Package] && !exactPackageVersion.MatchString(declaration.Version):
				nonExact = true
			}
			if declaration.Version != declarations[0].Version {
				conflicting = true
			}
		}
		reason := ""
		switch {
		case catalog:
			reason = UnresolvableCatalogReason
		case nonExact:
			reason = NonExactReason
		case conflicting:
			reason = ConflictingReason
		}
		if reason != "" {
			problems = append(problems, &RootDependencyAuthorityError{Dependency: name, Reason: reason, Authorities: declarations})
			continue
		}
		root[name] = declarations[0].Version
	}
	if len(problems) != 0 {
		return nil, &RootDependencyConflictsError{Conflicts: problems}
	}
	return root, nil
}

// validateVerbatimSpecifier refuses verbatim path specifiers that name nothing
// inside the assembled Workbench: absolute paths, a link: to a globally
// registered package (bun link), and paths that escape the Workbench root.
func validateVerbatimSpecifier(pkg Package, class, name, specifier string) error {
	protocol, target, isPath := pathSpecifier(specifier)
	if protocol == "" {
		return nil
	}
	refuse := func(problem string) error {
		return fmt.Errorf("package %q dependency %q in %s declares %q: %s", pkg.Name, name, class, specifier, problem)
	}
	if target == "" {
		return refuse("the path is empty")
	}
	if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "~") || filepath.IsAbs(target) || (len(target) >= 2 && target[1] == ':') {
		return refuse("absolute paths are not portable between Workbenches; name a path relative to the package")
	}
	if !isPath {
		return refuse("link:<name> names a package registered with bun link, which the assembled Workbench does not carry; use link:./<path> for a directory")
	}
	if rebased := path.Join(filepath.ToSlash(pkg.Directory), target); rebased == ".." || strings.HasPrefix(rebased, "../") {
		return refuse(fmt.Sprintf("the path resolves to %q, outside the Workbench root", rebased))
	}
	return nil
}

// pathSpecifier splits a file: or link: specifier. A file: target is always a
// path; a link: target is a path only when it is path-shaped (./, ../, or a
// relative path containing / that is not a scoped package name), and otherwise
// names a package registered with bun link.
func pathSpecifier(specifier string) (protocol, target string, isPath bool) {
	for _, candidate := range []string{"file:", "link:"} {
		if rest, ok := strings.CutPrefix(specifier, candidate); ok {
			if candidate == "file:" {
				return candidate, rest, true
			}
			pathShaped := strings.HasPrefix(rest, "./") || strings.HasPrefix(rest, "../") || (strings.Contains(rest, "/") && !strings.HasPrefix(rest, "@"))
			return candidate, rest, pathShaped
		}
	}
	return "", "", false
}

// rootRelativeSpecifier re-expresses a package-relative file: or link: path
// relative to the Workbench root, where reassembled root dependencies live.
// The package manifest keeps the authored specifier; only the root's copy and
// the comparison use this form, so equal text naming different files in two
// packages is a conflict, not an agreement. validateVerbatimSpecifier has
// already refused absolute, bare link: and escaping targets.
func rootRelativeSpecifier(packageDirectory, specifier string) string {
	protocol, target, isPath := pathSpecifier(specifier)
	if protocol == "" || !isPath || target == "" || strings.HasPrefix(target, "/") {
		return specifier
	}
	return protocol + path.Join(filepath.ToSlash(packageDirectory), target)
}
