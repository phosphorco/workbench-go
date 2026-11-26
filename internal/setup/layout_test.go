package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/evaluate"
	"github.com/phosphorco/workbench-go/internal/workspace"
)

func requireBun(t *testing.T) string {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	bun, err = filepath.Abs(bun)
	if err != nil {
		t.Fatal(err)
	}
	return bun
}

func requireEvaluator(t *testing.T) evaluate.Evaluator {
	t.Helper()
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	pkl, err = filepath.Abs(pkl)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	return evaluator
}

func specifiersBySource(imports []workspace.Import) map[string][]string {
	result := make(map[string][]string)
	for _, observed := range imports {
		key := observed.Source
		if observed.Development {
			key += " (dev)"
		}
		result[key] = append(result[key], observed.Specifier)
	}
	return result
}

// writeLayoutPackage writes one package whose files exercise every 0.13
// observation rule. The released walk would refuse the shebang file and the
// Bun-unparseable ambient declaration, and would observe the imports below
// hidden, dependency and excluded directories.
func writeLayoutPackage(t *testing.T, root string) {
	t.Helper()
	write(t, filepath.Join(root, "src", "index.ts"), "import { a } from \"@real/runtime\"\nexport const value = a\n")
	write(t, filepath.Join(root, "src", "cli.ts"), "#!/usr/bin/env bun\n\nimport { b } from \"@real/shebang\"\nexport const cli = b\n")
	write(t, filepath.Join(root, "src", "ambient.d.ts"), "import type { M } from \"@real/ambient\"\nexport const modules: ReadonlyArray<M>\n")
	write(t, filepath.Join(root, "src", ".prototypes", "draft.ts"), "import \"@false/hidden-directory\"\n")
	write(t, filepath.Join(root, "src", ".hidden.ts"), "import \"@false/hidden-file\"\n")
	write(t, filepath.Join(root, "src", "node_modules", "dep", "index.ts"), "import \"@false/dependency\"\n")
	write(t, filepath.Join(root, "src", "__tests__", "inner.ts"), "import \"@real/src-tests\"\n")
	write(t, filepath.Join(root, "__tests__", "unit.ts"), "import \"@real/tests-root\"\n")
	write(t, filepath.Join(root, "__tests__", "fixtures", "bad.ts"), "import \"@false/fixture\"\n")
	write(t, filepath.Join(root, "outside", "ignored.ts"), "import \"@false/outside-roots\"\n")
}

func TestSourceImportsFollowTypeScriptIncludeSemantics(t *testing.T) {
	bun := requireBun(t)
	root := t.TempDir()
	writeLayoutPackage(t, root)
	policy := contract.PackagePolicy{SourceRoots: []string{"src", "__tests__", "src/__tests__"}, SourceExcludes: []string{"__tests__/fixtures"}}
	imports, err := sourceImports(context.Background(), root, policy.ScannedSourceRoots(), policy.SourceExcludes, typeScriptIncludeObservation, bun)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"__tests__/unit.ts (dev)":      {"@real/tests-root"},
		"src/__tests__/inner.ts (dev)": {"@real/src-tests"},
		"src/ambient.d.ts":             {"@real/ambient"},
		"src/cli.ts":                   {"@real/shebang"},
		"src/index.ts":                 {"@real/runtime"},
	}
	if got := specifiersBySource(imports); !reflect.DeepEqual(got, want) {
		t.Fatalf("observed imports = %#v, want %#v", got, want)
	}
	for _, observed := range imports {
		if observed.Specifier == "@real/shebang" && observed.Line != 3 {
			t.Fatalf("shebang rewrite moved line numbers: %#v", observed)
		}
	}
}

func TestReleasedObservationStillWalksEverySourceFile(t *testing.T) {
	bun := requireBun(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "src", "index.ts"), "import \"@real/runtime\"\n")
	write(t, filepath.Join(root, "src", ".prototypes", "draft.ts"), "import \"@released/hidden\"\n")
	write(t, filepath.Join(root, "src", "node_modules", "dep", "index.ts"), "import \"@released/dependency\"\n")
	write(t, filepath.Join(root, "src", "types.d.ts"), "import \"@released/declaration\"\nexport declare const x: number\n")
	write(t, filepath.Join(root, "src", "__tests__", "inner.ts"), "import \"@released/src-tests\"\n")
	write(t, filepath.Join(root, "src", "nested", "__tests__", "deep.ts"), "import \"@released/nested-tests\"\n")
	imports, err := sourceImports(context.Background(), root, []string{"src"}, nil, legacySourceObservation, bun)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"src/.prototypes/draft.ts":           {"@released/hidden"},
		"src/__tests__/inner.ts":             {"@released/src-tests"},
		"src/index.ts":                       {"@real/runtime"},
		"src/nested/__tests__/deep.ts (dev)": {"@released/nested-tests"},
		"src/node_modules/dep/index.ts":      {"@released/dependency"},
		"src/types.d.ts":                     {"@released/declaration"},
	}
	if got := specifiersBySource(imports); !reflect.DeepEqual(got, want) {
		t.Fatalf("released observation = %#v, want %#v", got, want)
	}
}

func TestLocateDeclaredPackageRequiresRealConfinedDirectories(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "tools", "cli", "src", "index.ts"), "export {}\n")
	write(t, filepath.Join(root, "tools", "cli", "__tests__", "a.ts"), "export {}\n")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "pkg", "src", "index.ts"), "export {}\n")
	mustSymlink(t, outside, filepath.Join(root, "escape"))
	mustSymlink(t, filepath.Join(root, "tools"), filepath.Join(root, "alias"))
	mustSymlink(t, filepath.Join(root, "tools", "cli", "src"), filepath.Join(root, "tools", "cli", "linked"))

	located, err := locateDeclaredPackage(root, contract.PackagePolicy{Directory: "tools/cli", SourceRoots: []string{"src", "__tests__"}})
	if err != nil || located != filepath.Join(root, "tools", "cli") {
		t.Fatalf("declared directory = %q, %v", located, err)
	}
	for _, test := range []struct {
		policy contract.PackagePolicy
		want   string
	}{
		{contract.PackagePolicy{Directory: "tools/missing"}, `"tools/missing" does not exist`},
		{contract.PackagePolicy{Directory: "escape/pkg"}, "reached through symlink \"escape\""},
		{contract.PackagePolicy{Directory: "alias/cli"}, "reached through symlink \"alias\""},
		{contract.PackagePolicy{Directory: "tools/cli", SourceRoots: []string{"lib"}}, `declared source root "lib"`},
		{contract.PackagePolicy{Directory: "tools/cli", SourceRoots: []string{"linked"}}, "reached through symlink \"tools/cli/linked\""},
		{contract.PackagePolicy{Directory: "tools/cli/src/index.ts"}, "is not a directory"},
	} {
		if _, err := locateDeclaredPackage(root, test.policy); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("locate %#v = %v, want %q", test.policy, err, test.want)
		}
	}
}

func TestLeafSearchAndPackageScopePlacementFollowDeclaredSourceRoots(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "packages", "cli", "lib", "index.ts"), "export {}\n")
	located, err := locateLegacyPackage(root, "@a/cli", false, []string{"lib"})
	if err != nil || located != filepath.Join(root, "packages", "cli") {
		t.Fatalf("leaf search = %q, %v", located, err)
	}
	if _, err := locateLegacyPackage(root, "@a/cli", false, []string{"src"}); err == nil {
		t.Fatal("leaf search ignored the default src root")
	}
	scopeRoot := t.TempDir()
	write(t, filepath.Join(scopeRoot, "core", "src", "index.ts"), "export {}\n")
	write(t, filepath.Join(scopeRoot, "core", "__tests__", "a.ts"), "export {}\n")
	located, err = locatePackageScopePackage(scopeRoot, "@a", "@a/core", []string{"src", "__tests__"})
	if err != nil || located != filepath.Join(scopeRoot, "core") {
		t.Fatalf("scope placement = %q, %v", located, err)
	}
	if _, err := locatePackageScopePackage(scopeRoot, "@a", "@a/core", []string{"src", "test"}); err == nil || !strings.Contains(err.Error(), `requires canonical source directory "core/test"`) {
		t.Fatalf("missing scope source root = %v", err)
	}
}

func TestObservationRefusesTwoPackagesInOneDeclaredDirectory(t *testing.T) {
	bun := requireBun(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "shared", "src", "index.ts"), "export {}\n")
	resource := Resource{Identity: "phosphorco/mono", CanonicalPath: "repos/mono", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Packages: map[string]contract.PackagePolicy{
		"one": {Directory: "shared"}, "two": {Directory: "shared"},
	}}
	_, err := observePackagesAt(context.Background(), []Resource{resource}, "0.13.0", map[string]string{resource.Identity: root}, bun)
	if err == nil || !strings.Contains(err.Error(), `packages "one" and "two" in "phosphorco/mono" both resolve to directory "shared"`) {
		t.Fatalf("shared directory = %v", err)
	}
}

func TestContractSelectionFreezes0120AndAdmits0130(t *testing.T) {
	evaluator := requireEvaluator(t)
	for _, test := range []struct {
		uri, version, frozen string
	}{
		{localV0120RepositoryURI, "0.12.0", historicalRepository0120},
		{localV0120PackageScopeURI, "0.12.0", historicalPackageScope0120},
		{localV0130RepositoryURI, "0.13.0", localRepositoryDeclarationContract},
		{localV0130PackageScopeURI, "0.13.0", localRepositoryContract},
		{"package://github.com/phosphorco/workbench-go/releases/download/0.12.0/workbench@0.12.0#/Repository.pkl", "0.12.0", ""},
		{"package://github.com/phosphorco/workbench-go/releases/download/0.13.0/workbench@0.13.0#/Repository.pkl", "0.13.0", ""},
	} {
		filename := filepath.Base(test.uri)
		if hash := strings.LastIndex(test.uri, "#/"); hash >= 0 {
			filename = test.uri[hash+2:]
		}
		_, version, err := schemaForSource([]byte(fmt.Sprintf("amends %q\n", test.uri)), filename)
		if err != nil || version != test.version {
			t.Fatalf("select %s = %s, %v", test.uri, version, err)
		}
	}
	if historicalRepository0120 == localRepositoryDeclarationContract || historicalPackageScope0120 == localRepositoryContract {
		t.Fatal("0.13 schemas equal the frozen 0.12 schemas")
	}
	// A 0.12.0 declaration cannot reach the 0.13 layout fields.
	for _, field := range []string{`directory = "tools/cli"`, `sourceRoots { "lib" }`, `dependencyVersions = "verbatim"`} {
		source := []byte(fmt.Sprintf("amends %q\npackages { [\"cli\"] { %s } }\n", localV0120RepositoryURI, field))
		if _, err := EvaluateCurrentDeclaration(context.Background(), evaluator, source); err == nil {
			t.Fatalf("0.12.0 Repository admitted %s", field)
		}
	}
	if _, err := EvaluateCurrentDeclaration(context.Background(), evaluator, []byte(fmt.Sprintf("amends %q\nscopes { \"@a\" }\n", localV0120RepositoryURI))); err == nil {
		t.Fatal("0.12.0 Repository admitted scopes")
	}
	// The 0.13 Listing defaults are empty, so amending and assigning agree.
	for _, roots := range []string{`sourceRoots { "lib"; "test" }`, `sourceRoots = new Listing { "lib"; "test" }`} {
		source := []byte(fmt.Sprintf("amends %q\npackages { [\"cli\"] { %s } }\n", localV0130RepositoryURI, roots))
		declaration, err := EvaluateCurrentDeclaration(context.Background(), evaluator, source)
		if err != nil {
			t.Fatal(err)
		}
		if got := declaration.Packages["cli"].EffectiveSourceRoots(); !reflect.DeepEqual(got, []string{"lib", "test"}) {
			t.Fatalf("%s evaluated to %q", roots, got)
		}
	}
	declaration, err := EvaluateCurrentDeclaration(context.Background(), evaluator, []byte(fmt.Sprintf("amends %q\npackages { [\"cli\"] {} }\n", localV0130RepositoryURI)))
	if err != nil || !reflect.DeepEqual(declaration.Packages["cli"].EffectiveSourceRoots(), []string{"src"}) || declaration.Packages["cli"].Verbatim() {
		t.Fatalf("0.13 defaults = %#v, %v", declaration.Packages["cli"], err)
	}
}

func inRepositoryScopeSource(t *testing.T, version string) *discoverySource {
	t.Helper()
	evaluator := requireEvaluator(t)
	return &discoverySource{ctx: context.Background(), evaluator: evaluator, evaluatorVersioned: evaluator, version: version}
}

const scopeFixtureDeclaration = `amends "workbench-contract:/0.13.0/PackageScopeRepository.pkl"

scope = "@alpha"
includes {
  ["phosphorco/library"] { skills { editing { names = Set("from-scope") } } }
  ["phosphorco/tools"] {}
}
packages {
  ["@alpha/core"] { sourceRoots { "src"; "__tests__" }; dependencyVersions = "verbatim"; dependencies { ["effect"] = "catalog:" } }
  ["@alpha/web"] {}
}
`

func TestInRepositoryScopesMergePackagesAndIncludes(t *testing.T) {
	source := inRepositoryScopeSource(t, "0.13.0")
	checkout := t.TempDir()
	write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), scopeFixtureDeclaration)
	editing := contract.SkillSelection{Names: []string{"from-root"}}
	declaration := contract.Declaration{
		Shape:    contract.ResourceShape{Kind: contract.RepositoryShape},
		Scopes:   []string{"@alpha"},
		Includes: map[string]contract.ResourceInclude{"phosphorco/library": {Skills: &contract.SkillPolicy{Editing: &editing}}},
		Packages: map[string]contract.PackagePolicy{"tool": {Directory: "tools/cli"}},
	}
	if err := source.mergeInRepositoryScopes("phosphorco/mono", checkout, &declaration); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(declaration.Packages))
	for name := range declaration.Packages {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"@alpha/core", "@alpha/web", "tool"}) {
		t.Fatalf("merged packages = %q", names)
	}
	core := declaration.Packages["@alpha/core"]
	if core.Directory != "pkg/@alpha/core" || !core.Verbatim() || !reflect.DeepEqual(core.SourceRoots, []string{"src", "__tests__"}) || declaration.Packages["@alpha/web"].Directory != "pkg/@alpha/web" {
		t.Fatalf("scope packages = %#v", declaration.Packages)
	}
	library := declaration.Includes["phosphorco/library"]
	if library.Skills == nil || library.Skills.Editing == nil || !reflect.DeepEqual(library.Skills.Editing.Names, []string{"from-root", "from-scope"}) {
		t.Fatalf("merged library include = %#v", library.Skills)
	}
	if _, included := declaration.Includes["phosphorco/tools"]; !included {
		t.Fatalf("scope include was not merged: %#v", declaration.Includes)
	}
}

func TestInRepositoryScopesRefuseInconsistentDeclarations(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, checkout string)
		root    map[string]contract.PackagePolicy
		version string
		want    string
	}{
		{name: "missing file", prepare: func(*testing.T, string) {}, want: `scope pkg/@alpha/workbench.pkl: "pkg/@alpha/workbench.pkl" does not exist`},
		{name: "symlinked file", prepare: func(t *testing.T, checkout string) {
			outside := filepath.Join(t.TempDir(), "workbench.pkl")
			write(t, outside, scopeFixtureDeclaration)
			if err := os.MkdirAll(filepath.Join(checkout, "pkg", "@alpha"), 0o755); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, outside, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"))
		}, want: "reached through symlink"},
		{name: "different scope", prepare: func(t *testing.T, checkout string) {
			write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), strings.NewReplacer(`"@alpha"`, `"@beta"`, `@alpha/`, `@beta/`).Replace(scopeFixtureDeclaration))
		}, want: `declares scope "@beta", want "@alpha"`},
		{name: "package also at root", prepare: func(t *testing.T, checkout string) {
			write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), scopeFixtureDeclaration)
		}, root: map[string]contract.PackagePolicy{"@alpha/web": {Directory: "web"}}, want: `package "@alpha/web" is in listed scope @alpha; declare it in pkg/@alpha/workbench.pkl`},
		{name: "repository contract", prepare: func(t *testing.T, checkout string) {
			write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), "amends \"workbench-contract:/0.13.0/Repository.pkl\"\n")
		}, want: `must amend the PackageScopeRepository.pkl contract, not "Repository.pkl"`},
		{name: "older contract", prepare: func(t *testing.T, checkout string) {
			write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), strings.Replace(scopeFixtureDeclaration, "/0.13.0/", "/0.12.0/", 1))
		}, want: "declaration is 0.12.0, want exact 0.13.0"},
		{name: "buildables", prepare: func(t *testing.T, checkout string) {
			write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), "amends \"workbench-contract:/0.13.0/PackageScopeRepository.pkl\"\nscope = \"@alpha\"\n"+buildablePklFixture)
		}, want: "declares buildables"},
		{name: "declaration error", prepare: func(t *testing.T, checkout string) {
			write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), "amends \"workbench-contract:/0.13.0/PackageScopeRepository.pkl\"\nscope = \"@alpha\"\npackages { [\"@other/x\"] {} }\n")
		}, want: "scope pkg/@alpha/workbench.pkl: evaluate"},
		{name: "scope file imports", prepare: func(t *testing.T, checkout string) {
			write(t, filepath.Join(checkout, "pkg", "@alpha", "shared.pkl"), "packages {}\n")
			write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), "amends \"workbench-contract:/0.13.0/PackageScopeRepository.pkl\"\nimport \"shared.pkl\"\nscope = \"@alpha\"\n")
		}, want: "scope pkg/@alpha/workbench.pkl: evaluate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := inRepositoryScopeSource(t, "0.13.0")
			checkout := t.TempDir()
			test.prepare(t, checkout)
			declaration := contract.Declaration{Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Scopes: []string{"@alpha"}, Packages: test.root}
			if err := source.mergeInRepositoryScopes("phosphorco/mono", checkout, &declaration); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("merge = %v, want %q", err, test.want)
			}
		})
	}
}

func TestInRepositoryScopesRequireTheCurrentContract(t *testing.T) {
	source := inRepositoryScopeSource(t, "0.12.0")
	declaration := contract.Declaration{Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Scopes: []string{"@alpha"}}
	if err := source.mergeInRepositoryScopes("phosphorco/mono", t.TempDir(), &declaration); err == nil || !strings.Contains(err.Error(), "require a 0.13.0 or later Repository declaration") {
		t.Fatalf("0.12 scopes = %v", err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
