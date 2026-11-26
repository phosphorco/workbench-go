package contract

import (
	"slices"
	"strings"
	"testing"
)

func TestPackagePolicyDefaultsKeepTheReleasedLayout(t *testing.T) {
	policy := PackagePolicy{}
	if got := policy.EffectiveSourceRoots(); !slices.Equal(got, []string{"src"}) {
		t.Fatalf("default source roots = %q", got)
	}
	if policy.Verbatim() {
		t.Fatal("default policy is verbatim")
	}
	if err := policy.ValidateLayout(PackageScopeShape); err != nil {
		t.Fatal(err)
	}
}

func TestScannedSourceRootsDropNestedRoots(t *testing.T) {
	policy := PackagePolicy{SourceRoots: []string{"src", "__tests__", "src/__tests__", "scripts/tools", "scripts"}}
	if err := policy.ValidateLayout(RepositoryShape); err != nil {
		t.Fatal(err)
	}
	if got := policy.ScannedSourceRoots(); !slices.Equal(got, []string{"src", "__tests__", "scripts"}) {
		t.Fatalf("scanned roots = %q", got)
	}
}

func TestValidateLayoutRefusesUnsafeOrIncoherentFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy PackagePolicy
		shape  ResourceShapeKind
		want   string
	}{
		{name: "directory on a scope", policy: PackagePolicy{Directory: "pkg/a"}, shape: PackageScopeShape, want: "only declarable by a Repository"},
		{name: "parent directory", policy: PackagePolicy{Directory: "a/../b"}, want: `segment ".." must not be hidden`},
		{name: "absolute directory", policy: PackagePolicy{Directory: "/a"}, want: "without empty segments"},
		{name: "trailing slash", policy: PackagePolicy{Directory: "a/"}, want: "without empty segments"},
		{name: "current directory", policy: PackagePolicy{Directory: "."}, want: `segment "." must not be hidden`},
		{name: "backslash", policy: PackagePolicy{Directory: `a\b`}, want: "backslashes"},
		{name: "dependency directory", policy: PackagePolicy{SourceRoots: []string{"node_modules"}}, want: "node_modules"},
		{name: "hidden root", policy: PackagePolicy{SourceRoots: []string{".prototypes"}}, want: "must not be hidden"},
		{name: "duplicate root", policy: PackagePolicy{SourceRoots: []string{"src", "src"}}, want: "declared twice"},
		{name: "root in output", policy: PackagePolicy{SourceRoots: []string{"dist/types"}}, want: "generated output directory"},
		{name: "exclude outside roots", policy: PackagePolicy{SourceExcludes: []string{"fixtures"}}, want: "not strictly inside a source root"},
		{name: "exclude is a root", policy: PackagePolicy{SourceExcludes: []string{"src"}}, want: "not strictly inside a source root"},
		{name: "duplicate exclude", policy: PackagePolicy{SourceExcludes: []string{"src/a", "src/a"}}, want: "declared twice"},
		{name: "unknown versions", policy: PackagePolicy{DependencyVersions: "loose"}, want: "neither"},
		{name: "exclude equals a nested root", policy: PackagePolicy{SourceRoots: []string{"src", "src/__tests__"}, SourceExcludes: []string{"src/__tests__"}}, want: "is also a declared source root"},
		{name: "glob star root", policy: PackagePolicy{SourceRoots: []string{"src/*"}}, want: "glob character"},
		{name: "glob question exclude", policy: PackagePolicy{SourceExcludes: []string{"src/a?"}}, want: "glob character"},
		{name: "glob class exclude", policy: PackagePolicy{SourceExcludes: []string{"src/[ab]"}}, want: "glob character"},
		{name: "glob brace exclude", policy: PackagePolicy{SourceExcludes: []string{"src/{a,b}"}}, want: "glob character"},
		{name: "glob negation exclude", policy: PackagePolicy{SourceExcludes: []string{"src/!a"}}, want: "glob character"},
		{name: "glob in directory", policy: PackagePolicy{Directory: "pkg/**"}, want: "glob character"},
	} {
		t.Run(test.name, func(t *testing.T) {
			shape := test.shape
			if shape == "" {
				shape = RepositoryShape
			}
			err := test.policy.ValidateLayout(shape)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateLayout() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeRepositoryDeclarationCarriesLayoutAndScopes(t *testing.T) {
	declaration, err := DecodeRepositoryDeclaration([]byte(`{
  "includes": {},
  "packages": {"tool": {"directory": "tools/cli", "sourceRoots": ["src", "__tests__"], "sourceExcludes": ["__tests__/fixtures"], "dependencyVersions": "verbatim",
    "dependencies": {"effect": "catalog:"}, "devDependencies": {}, "requiredButNotReferenced": {}, "peerDependencies": {}, "optionalDependencies": {}, "imports": {}, "exports": {}}},
  "scopes": ["@alpha", "@beta"],
  "buildables": {}, "verifiers": {}, "gates": {}, "focuses": {}
}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := declaration.Packages["tool"]
	if policy.Directory != "tools/cli" || !policy.Verbatim() || !slices.Equal(policy.SourceRoots, []string{"src", "__tests__"}) || !slices.Equal(declaration.Scopes, []string{"@alpha", "@beta"}) {
		t.Fatalf("decoded declaration = %#v", declaration)
	}
}

func TestDeclarationRefusesInvalidScopes(t *testing.T) {
	for _, test := range []struct {
		declaration Declaration
		want        string
	}{
		{Declaration{Shape: ResourceShape{Kind: RepositoryShape}, Scopes: []string{"alpha"}}, "is not a package scope"},
		{Declaration{Shape: ResourceShape{Kind: RepositoryShape}, Scopes: []string{"@alpha", "@alpha"}}, "declared twice"},
		{Declaration{Shape: ResourceShape{Kind: PackageScopeShape, Scope: "@alpha"}, Scopes: []string{"@beta"}}, "only a Repository"},
	} {
		if err := test.declaration.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("Validate(%v) = %v, want %q", test.declaration.Scopes, err, test.want)
		}
	}
}

func TestDecodePackageScopeDeclarationRefusesADirectory(t *testing.T) {
	_, err := DecodePackageScopeDeclarationV030([]byte(`{"scope": "@alpha", "includes": {}, "buildables": {},
  "packages": {"@alpha/core": {"directory": "elsewhere", "dependencies": {}, "devDependencies": {}, "requiredButNotReferenced": {}, "peerDependencies": {}, "optionalDependencies": {}, "imports": {}, "exports": {}}}}`))
	if err == nil || !strings.Contains(err.Error(), "only declarable by a Repository") {
		t.Fatalf("PackageScope directory = %v", err)
	}
}
