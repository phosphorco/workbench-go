package contract

import (
	"fmt"
	"strings"
)

const (
	// DefaultSourceRoot is observed when a package declares no source roots.
	DefaultSourceRoot = "src"
	// ExactDependencyVersions requires exact external versions.
	ExactDependencyVersions = "exact"
	// VerbatimDependencyVersions passes external specifiers through unchanged.
	VerbatimDependencyVersions = "verbatim"
	// generatedOutputDirectory is the outDir of every generated package tsconfig.
	generatedOutputDirectory = "dist"
)

// EffectiveSourceRoots returns the declared source roots, or the conventional
// src root when none are declared. Roots may nest, as TypeScript includes may;
// ScannedSourceRoots removes the nested ones so each file is observed once.
func (policy PackagePolicy) EffectiveSourceRoots() []string {
	if len(policy.SourceRoots) == 0 {
		return []string{DefaultSourceRoot}
	}
	return append([]string(nil), policy.SourceRoots...)
}

// Verbatim reports whether external dependency specifiers pass through unchanged.
func (policy PackagePolicy) Verbatim() bool {
	return policy.DependencyVersions == VerbatimDependencyVersions
}

// ValidateLayout checks the 0.13 layout fields independently of the Pkl schema,
// so a decoded declaration is safe to place and observe whatever produced it.
func (policy PackagePolicy) ValidateLayout(shape ResourceShapeKind) error {
	if policy.Directory != "" {
		if shape != RepositoryShape {
			return fmt.Errorf("directory %q is only declarable by a Repository; a PackageScope derives <leaf>", policy.Directory)
		}
		if err := ValidatePackagePath(policy.Directory); err != nil {
			return fmt.Errorf("directory %q: %w", policy.Directory, err)
		}
	}
	switch policy.DependencyVersions {
	case "", ExactDependencyVersions, VerbatimDependencyVersions:
	default:
		return fmt.Errorf("dependencyVersions %q is neither %q nor %q", policy.DependencyVersions, ExactDependencyVersions, VerbatimDependencyVersions)
	}
	seenRoots := make(map[string]struct{}, len(policy.SourceRoots))
	for _, root := range policy.SourceRoots {
		if err := ValidatePackagePath(root); err != nil {
			return fmt.Errorf("source root %q: %w", root, err)
		}
		if _, duplicate := seenRoots[root]; duplicate {
			return fmt.Errorf("source root %q is declared twice", root)
		}
		seenRoots[root] = struct{}{}
		if PathWithin(root, generatedOutputDirectory) {
			return fmt.Errorf("source root %q lies inside the generated output directory %q", root, generatedOutputDirectory)
		}
	}
	seenExcludes := make(map[string]struct{}, len(policy.SourceExcludes))
	for _, exclude := range policy.SourceExcludes {
		if err := ValidatePackagePath(exclude); err != nil {
			return fmt.Errorf("source exclude %q: %w", exclude, err)
		}
		if _, duplicate := seenExcludes[exclude]; duplicate {
			return fmt.Errorf("source exclude %q is declared twice", exclude)
		}
		seenExcludes[exclude] = struct{}{}
		inside := false
		for _, root := range policy.EffectiveSourceRoots() {
			if exclude != root && PathWithin(exclude, root) {
				inside = true
			}
		}
		if !inside {
			return fmt.Errorf("source exclude %q is not strictly inside a source root %q", exclude, policy.EffectiveSourceRoots())
		}
	}
	return nil
}

// ValidatePackagePath accepts a slash-separated relative path whose segments
// are non-empty and neither hidden (`.`-prefixed, including `.` and `..`) nor
// node_modules. It mirrors the Pkl PackagePath type.
func ValidatePackagePath(value string) error {
	if value == "" {
		return fmt.Errorf("must be a non-empty relative path")
	}
	for _, character := range value {
		if character == '\\' || character < 0x20 || character == 0x7f {
			return fmt.Errorf("must not contain backslashes or control characters")
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" {
			return fmt.Errorf("must be a relative path without empty segments")
		}
		if strings.HasPrefix(segment, ".") {
			return fmt.Errorf("segment %q must not be hidden, \".\" or \"..\"", segment)
		}
		if segment == "node_modules" {
			return fmt.Errorf("must not name node_modules")
		}
	}
	return nil
}

// PathWithin reports whether the slash-separated path equals parent or lies below it.
func PathWithin(path, parent string) bool {
	return path == parent || strings.HasPrefix(path, parent+"/")
}

// ScannedSourceRoots returns the effective roots that are not inside another
// effective root, in declared order.
func (policy PackagePolicy) ScannedSourceRoots() []string {
	roots := policy.EffectiveSourceRoots()
	scanned := make([]string, 0, len(roots))
	for _, root := range roots {
		nested := false
		for _, other := range roots {
			if other != root && PathWithin(root, other) {
				nested = true
			}
		}
		if !nested {
			scanned = append(scanned, root)
		}
	}
	return scanned
}
