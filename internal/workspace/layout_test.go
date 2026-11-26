package workspace

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
)

func verbatimPolicy(policy contract.PackagePolicy) contract.PackagePolicy {
	policy.DependencyVersions = contract.VerbatimDependencyVersions
	return policy
}

func TestVerbatimPassesRootResolvableSpecifiersThroughUnchanged(t *testing.T) {
	specifiers := map[string]string{
		"left-pad":  "^1.3.0",
		"aliased":   "npm:@scope/real@1.2.3",
		"vendored":  "file:vendor/vendored-1.0.0.tgz",
		"sibling":   "file:../shared/sibling",
		"linked":    "link:./local/linked",
		"linked-up": "link:../linked-up",
	}
	projection, err := Build([]Package{{
		Name: "@entry/app", Directory: "repos/mono/pkg/@entry/app",
		Policy: verbatimPolicy(contract.PackagePolicy{
			Dependencies:    specifiers,
			DevDependencies: map[string]string{"typescript": "~5.9.3"},
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var manifest packageJSON
	if err := json.Unmarshal(projection.Files["repos/mono/pkg/@entry/app/package.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.Dependencies, specifiers) || manifest.DevDependencies["typescript"] != "~5.9.3" {
		t.Fatalf("package specifiers = %#v %#v", manifest.Dependencies, manifest.DevDependencies)
	}
	var root rootPackageJSON
	if err := json.Unmarshal(projection.Files["package.json"], &root); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"left-pad": "^1.3.0",
		"aliased":  "npm:@scope/real@1.2.3",
		// The root lives at the Workbench root, so its copy of a package-relative
		// path names the same file from there.
		"vendored":   "file:repos/mono/pkg/@entry/app/vendor/vendored-1.0.0.tgz",
		"sibling":    "file:repos/mono/pkg/@entry/shared/sibling",
		"linked":     "link:repos/mono/pkg/@entry/app/local/linked",
		"linked-up":  "link:repos/mono/pkg/@entry/linked-up",
		"typescript": "~5.9.3",
	}
	if !reflect.DeepEqual(root.DevDependencies, want) {
		t.Fatalf("root dependencies = %#v", root.DevDependencies)
	}
}

func TestVerbatimRefusesCatalogReferencesTheGeneratedRootCannotResolve(t *testing.T) {
	_, err := Build([]Package{
		{Name: "@a/one", Directory: "pkg/@a/one", Policy: verbatimPolicy(contract.PackagePolicy{
			Dependencies:    map[string]string{"effect": "catalog:", "zod": "catalog:schemas", "vite": "catalog:"},
			DevDependencies: map[string]string{"typescript": "5.9.3"},
		})},
		{Name: "@a/two", Directory: "pkg/@a/two", Policy: verbatimPolicy(contract.PackagePolicy{Dependencies: map[string]string{"effect": "catalog:", "vite": "^8.0.12"}})},
	})
	var problems *RootDependencyConflictsError
	if !errors.As(err, &problems) || len(problems.Conflicts) != 3 {
		t.Fatalf("verbatim catalog = %T %v", err, err)
	}
	for _, conflict := range problems.Conflicts {
		if conflict.Reason != UnresolvableCatalogReason {
			t.Fatalf("%q reason = %q", conflict.Dependency, conflict.Reason)
		}
	}
	// Agreeing catalog references are still refused: the indirection, not a
	// disagreement, is what the generated root cannot carry.
	for _, fact := range []string{`"effect" (catalog indirection the generated Workbench root cannot resolve; it carries no catalog): "catalog:" by @a/one dependencies, @a/two dependencies`, `"zod" (catalog indirection`, `"vite" (catalog indirection the generated Workbench root cannot resolve; it carries no catalog; its specifiers also disagree)`} {
		if !strings.Contains(err.Error(), fact) {
			t.Fatalf("catalog refusal lacks %q:\n%v", fact, err)
		}
	}
}

func TestVerbatimRefusesPathsOutsideTheAssembledWorkbench(t *testing.T) {
	for specifier, want := range map[string]string{
		"link:cool-pkg":                  "registered with bun link",
		"link:@scope/linked":             "registered with bun link",
		"file:/opt/vendor/x.tgz":         "absolute paths are not portable",
		"file:///opt/vendor/x.tgz":       "absolute paths are not portable",
		"link:/opt/linked":               "absolute paths are not portable",
		"file:~/x.tgz":                   "absolute paths are not portable",
		"file:../../../../../../x.tgz":   `resolves to "../x.tgz", outside the Workbench root`,
		"link:../../../../../../outside": `resolves to "../outside", outside the Workbench root`,
		"file:":                          "the path is empty",
	} {
		_, err := Build([]Package{{Name: "@a/app", Directory: "repos/mono/pkg/@a/app", Policy: verbatimPolicy(contract.PackagePolicy{
			Dependencies: map[string]string{"dep": specifier}, DevDependencies: map[string]string{"typescript": "5.9.3"},
		})}})
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), specifier) {
			t.Fatalf("%s = %v, want %q", specifier, err, want)
		}
	}
}

func TestVerbatimCollectsTypeScriptAndExactAuthorityWithConflicts(t *testing.T) {
	_, err := Build([]Package{
		{Name: "@a/exact", Directory: "pkg/@a/exact", Policy: contract.PackagePolicy{Dependencies: map[string]string{"effect": "^4.0.0"}, DevDependencies: map[string]string{"typescript": "5.9.3"}}},
		{Name: "@a/one", Directory: "pkg/@a/one", Policy: verbatimPolicy(contract.PackagePolicy{Dependencies: map[string]string{"vite": "^8.0.0"}, DevDependencies: map[string]string{"typescript": "~5.9.3"}})},
		{Name: "@a/two", Directory: "pkg/@a/two", Policy: verbatimPolicy(contract.PackagePolicy{Dependencies: map[string]string{"vite": "8.0.12"}})},
	})
	var problems *RootDependencyConflictsError
	if !errors.As(err, &problems) {
		t.Fatalf("combined problems = %T %v", err, err)
	}
	got := map[string]string{}
	for _, conflict := range problems.Conflicts {
		got[conflict.Dependency] = conflict.Reason
	}
	if want := map[string]string{"effect": NonExactReason, "typescript": ConflictingReason, "vite": ConflictingReason}; !reflect.DeepEqual(got, want) {
		t.Fatalf("collected problems = %#v, want %#v", got, want)
	}
}

func TestFrozenRuntimeBuiltinsAreNodeCoreAndBun(t *testing.T) {
	builtins := RuntimeBuiltinModulesV0130()
	if len(builtins) != 69 {
		t.Fatalf("frozen built-ins = %d names, want Node 22's 68 core modules plus bun", len(builtins))
	}
	for _, name := range []string{"bun", "fs", "fs/promises", "path", "events", "worker_threads"} {
		if !builtins[name] {
			t.Fatalf("frozen built-ins lack %q", name)
		}
	}
	for _, name := range []string{"ws", "undici", "effect", "bun:sqlite", "node:fs"} {
		if builtins[name] {
			t.Fatalf("frozen built-ins contain %q", name)
		}
	}
	_, err := BuildWithOptions(withTypeScript([]Package{{
		Name: "@a/app", Directory: "pkg/@a/app",
		Imports: []Import{{Specifier: "ws", Source: "pkg/@a/app/src/index.ts", Line: 1}, {Specifier: "undici", Source: "pkg/@a/app/src/index.ts", Line: 2}},
	}}), BuildOptions{ReassembleRootDependencies: true, ProductionTypeScript: true, RuntimeBuiltins: builtins})
	var closure *ClosureError
	if !errors.As(err, &closure) || len(closure.Diagnostics) != 2 || closure.Diagnostics[0].Kind != MissingPackage || closure.Diagnostics[0].MissingPackage != "ws" {
		t.Fatalf("undeclared ws/undici = %v", err)
	}
}

func TestVerbatimAgreeingSpecifiersAssembleOneRootDependency(t *testing.T) {
	projection, err := Build([]Package{
		{Name: "@a/one", Directory: "pkg/@a/one", Policy: verbatimPolicy(contract.PackagePolicy{Dependencies: map[string]string{"effect": "^4.0.0"}, DevDependencies: map[string]string{"typescript": "~5.9.3"}})},
		{Name: "@a/two", Directory: "pkg/@a/two", Policy: verbatimPolicy(contract.PackagePolicy{PeerDependencies: map[string]string{"effect": "^4.0.0"}})},
		// An exact package may agree with verbatim ones on an exact specifier.
		{Name: "@a/three", Directory: "pkg/@a/three", Policy: contract.PackagePolicy{Dependencies: map[string]string{"zod": "4.0.0"}}},
		{Name: "@a/four", Directory: "pkg/@a/four", Policy: verbatimPolicy(contract.PackagePolicy{Dependencies: map[string]string{"zod": "4.0.0"}})},
	})
	if err != nil {
		t.Fatal(err)
	}
	var root rootPackageJSON
	if err := json.Unmarshal(projection.Files["package.json"], &root); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(root.DevDependencies, map[string]string{"effect": "^4.0.0", "typescript": "~5.9.3", "zod": "4.0.0"}) {
		t.Fatalf("root dependencies = %#v", root.DevDependencies)
	}
}

func TestVerbatimReportsEveryConflictingSpecifier(t *testing.T) {
	_, err := Build([]Package{
		{Name: "@a/one", Directory: "pkg/@a/one", Policy: verbatimPolicy(contract.PackagePolicy{
			Dependencies:    map[string]string{"effect": "^4.0.0", "vite": "^8.0.0", "same": "file:vendor/x.tgz"},
			DevDependencies: map[string]string{"typescript": "5.9.3"},
		})},
		{Name: "@a/two", Directory: "pkg/@a/two", Policy: verbatimPolicy(contract.PackagePolicy{
			Dependencies: map[string]string{"effect": "npm:@phosphorco/effect@4.1.2", "vite": "^8.0.12", "same": "file:vendor/x.tgz"},
		})},
		{Name: "@a/three", Directory: "pkg/@a/three", Policy: verbatimPolicy(contract.PackagePolicy{
			PeerDependencies: map[string]string{"effect": "^4.0.0"},
		})},
	})
	var conflicts *RootDependencyConflictsError
	if !errors.As(err, &conflicts) {
		t.Fatalf("verbatim conflicts = %T %v", err, err)
	}
	got := make([]string, 0, len(conflicts.Conflicts))
	for _, conflict := range conflicts.Conflicts {
		got = append(got, conflict.Dependency)
	}
	// Equal file: text in two package directories names two different files.
	if !reflect.DeepEqual(got, []string{"effect", "same", "vite"}) {
		t.Fatalf("conflicting dependencies = %q", got)
	}
	for _, fact := range []string{
		"3 dependency(ies)",
		`"effect" (declared with different specifiers): "npm:@phosphorco/effect@4.1.2" by @a/two dependencies; "^4.0.0" by @a/one dependencies, @a/three peerDependencies`,
		`"file:pkg/@a/one/vendor/x.tgz" by @a/one dependencies`,
		`"^8.0.12" by @a/two dependencies`,
	} {
		if !strings.Contains(err.Error(), fact) {
			t.Fatalf("conflict report lacks %q:\n%v", fact, err)
		}
	}
}

func TestExactPackagesKeepExactAuthorityBesideVerbatimPackages(t *testing.T) {
	_, err := Build([]Package{
		{Name: "@a/exact", Directory: "pkg/@a/exact", Policy: contract.PackagePolicy{Dependencies: map[string]string{"effect": "^4.0.0"}, DevDependencies: map[string]string{"typescript": "5.9.3"}}},
		{Name: "@a/verbatim", Directory: "pkg/@a/verbatim", Policy: verbatimPolicy(contract.PackagePolicy{Dependencies: map[string]string{"effect": "^4.0.0"}})},
	})
	var problems *RootDependencyConflictsError
	if !errors.As(err, &problems) || len(problems.Conflicts) != 1 || problems.Conflicts[0].Reason != NonExactReason {
		t.Fatalf("exact package with a range = %T %v", err, err)
	}
	_, err = Build([]Package{{Name: "@a/exact", Directory: "pkg/@a/exact", Policy: contract.PackagePolicy{Dependencies: map[string]string{"effect": "catalog:"}, DevDependencies: map[string]string{"typescript": "5.9.3"}}}})
	if err == nil || !strings.Contains(err.Error(), "the generated root has no package catalog") {
		t.Fatalf("exact catalog refusal = %v", err)
	}
	_, err = Build([]Package{{Name: "@a/exact", Directory: "pkg/@a/exact", Policy: contract.PackagePolicy{DevDependencies: map[string]string{"typescript": "catalog:"}}}})
	if err == nil || !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("exact TypeScript catalog refusal = %v", err)
	}
}

func TestExactClosureKeepsTheReleasedFirstConflictRefusal(t *testing.T) {
	_, err := Build([]Package{
		{Name: "@a/one", Directory: "pkg/@a/one", Policy: contract.PackagePolicy{Dependencies: map[string]string{"effect": "4.0.0", "zod": "4.0.0"}, DevDependencies: map[string]string{"typescript": "5.9.3"}}},
		{Name: "@a/two", Directory: "pkg/@a/two", Policy: contract.PackagePolicy{Dependencies: map[string]string{"effect": "4.0.1", "zod": "4.0.1"}}},
	})
	var authority *RootDependencyAuthorityError
	if !errors.As(err, &authority) || authority.Dependency != "effect" || authority.Reason != "conflicting" {
		t.Fatalf("exact conflict = %T %v", err, err)
	}
}

func TestVerbatimTypeScriptAuthorityMustStillAgreeAndExist(t *testing.T) {
	_, err := Build([]Package{
		{Name: "@a/one", Directory: "pkg/@a/one", Policy: verbatimPolicy(contract.PackagePolicy{DevDependencies: map[string]string{"typescript": "~5.9.3"}})},
		{Name: "@a/two", Directory: "pkg/@a/two", Policy: verbatimPolicy(contract.PackagePolicy{DevDependencies: map[string]string{"typescript": "5.9.3"}})},
	})
	var problems *RootDependencyConflictsError
	if !errors.As(err, &problems) || len(problems.Conflicts) != 1 || problems.Conflicts[0].Dependency != "typescript" || problems.Conflicts[0].Reason != ConflictingReason {
		t.Fatalf("verbatim TypeScript conflict = %T %v", err, err)
	}
	_, err = Build([]Package{{Name: "@a/one", Directory: "pkg/@a/one", Policy: verbatimPolicy(contract.PackagePolicy{})}})
	var authority *TypeScriptAuthorityError
	if !errors.As(err, &authority) || authority.Reason != "missing" {
		t.Fatalf("verbatim missing TypeScript = %T %v", err, err)
	}
}

func TestRuntimeBuiltinsAreNotPackages(t *testing.T) {
	packages := func() []Package {
		return withTypeScript([]Package{{
			Name: "@a/app", Directory: "pkg/@a/app",
			Imports: []Import{
				{Specifier: "fs", Source: "pkg/@a/app/src/index.ts", Line: 1},
				{Specifier: "fs/promises", Source: "pkg/@a/app/src/index.ts", Line: 2},
				{Specifier: "bun", Source: "pkg/@a/app/src/index.ts", Line: 3},
			},
		}})
	}
	_, err := Build(packages())
	var closure *ClosureError
	if !errors.As(err, &closure) || len(closure.Diagnostics) != 3 {
		t.Fatalf("released rule treats bare built-ins as packages; got %T %v", err, err)
	}
	projection, err := BuildWithOptions(packages(), BuildOptions{
		ReassembleRootDependencies: true, ProductionTypeScript: true,
		RuntimeBuiltins: map[string]bool{"fs": true, "fs/promises": true, "bun": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var manifest packageJSON
	if err := json.Unmarshal(projection.Files["pkg/@a/app/package.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Dependencies) != 0 {
		t.Fatalf("built-ins became dependencies: %#v", manifest.Dependencies)
	}
	// A built-in is matched by exact specifier, not by its first segment.
	_, err = BuildWithOptions(withTypeScript([]Package{{
		Name: "@a/app", Directory: "pkg/@a/app",
		Imports: []Import{{Specifier: "fs/not-a-builtin", Source: "pkg/@a/app/src/index.ts", Line: 1}},
	}}), BuildOptions{RuntimeBuiltins: map[string]bool{"fs": true}})
	if !errors.As(err, &closure) || closure.Diagnostics[0].MissingPackage != "fs" {
		t.Fatalf("non-built-in subpath = %v", err)
	}
}

func TestSourceRootsShapeTheGeneratedTypeScriptProject(t *testing.T) {
	for _, test := range []struct {
		name    string
		policy  contract.PackagePolicy
		rootDir string
		include []string
		exclude []string
	}{
		{name: "default", policy: contract.PackagePolicy{}, rootDir: "src", include: []string{"src/**/*.ts", "src/**/*.tsx"}},
		{name: "one other root", policy: contract.PackagePolicy{SourceRoots: []string{"lib"}}, rootDir: "lib", include: []string{"lib/**/*.ts", "lib/**/*.tsx"}},
		{name: "sibling roots", policy: contract.PackagePolicy{SourceRoots: []string{"src", "__tests__"}, SourceExcludes: []string{"__tests__/fixtures"}},
			rootDir: ".", include: []string{"src/**/*.ts", "src/**/*.tsx", "__tests__/**/*.ts", "__tests__/**/*.tsx"}, exclude: []string{"__tests__/fixtures"}},
		{name: "nested roots", policy: contract.PackagePolicy{SourceRoots: []string{"src/main", "src/test"}}, rootDir: "src", include: []string{"src/main/**/*.ts", "src/main/**/*.tsx", "src/test/**/*.ts", "src/test/**/*.tsx"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection, err := Build(withTypeScript([]Package{{Name: "@a/app", Directory: "pkg/@a/app", Policy: test.policy}}))
			if err != nil {
				t.Fatal(err)
			}
			encoded := projection.Files["pkg/@a/app/tsconfig.json"]
			var tsconfig packageTSConfig
			if err := json.Unmarshal(encoded, &tsconfig); err != nil {
				t.Fatal(err)
			}
			if tsconfig.CompilerOptions["rootDir"] != test.rootDir || !reflect.DeepEqual(tsconfig.Include, test.include) || !reflect.DeepEqual(tsconfig.Exclude, test.exclude) {
				t.Fatalf("tsconfig = %s", encoded)
			}
			if test.exclude == nil && strings.Contains(string(encoded), `"exclude"`) {
				t.Fatalf("tsconfig without excludes gained an exclude field: %s", encoded)
			}
		})
	}
}
