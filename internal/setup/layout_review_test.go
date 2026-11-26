package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/workspace"
)

func TestLayoutVersionGateNamesItsVersion(t *testing.T) {
	for version, want := range map[string]bool{"0.13.0": true, "0.12.0": false, "0.11.0": false, "0.6.1": false, "0.1.0": false} {
		if got := supportsRepositoryLayout(version); got != want {
			t.Fatalf("supportsRepositoryLayout(%q) = %t, want %t", version, got, want)
		}
	}
}

func TestShebangFilesAreScannedUnder0130Only(t *testing.T) {
	bun := requireBun(t)
	for name, contents := range map[string]string{
		"shebang":         "#!/usr/bin/env bun\nimport \"@real/shebang\"\n",
		"bom and shebang": "\xEF\xBB\xBF#!/usr/bin/env bun\nimport \"@real/shebang\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "src", "bin.ts"), contents)
			if _, err := sourceImports(context.Background(), root, []string{"src"}, nil, legacySourceObservation, bun); err == nil {
				t.Fatal("the released walk admitted a shebang file")
			}
			imports, err := sourceImports(context.Background(), root, []string{"src"}, nil, typeScriptIncludeObservation, bun)
			if err != nil || len(imports) != 1 || imports[0].Specifier != "@real/shebang" || imports[0].Line != 2 {
				t.Fatalf("0.13 shebang imports = %#v, %v", imports, err)
			}
		})
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "src", "only.ts"), "#!")
	if imports, err := sourceImports(context.Background(), root, []string{"src"}, nil, typeScriptIncludeObservation, bun); err != nil || len(imports) != 0 {
		t.Fatalf("a file that is only #! = %#v, %v", imports, err)
	}
	for input, want := range map[string]string{"#!x\n": "//x\n", "\xEF\xBB\xBF#!x": "\xEF\xBB\xBF//x", "#!": "//", "x#!": "x#!", "\xEF\xBB\xBF": "\xEF\xBB\xBF", "": ""} {
		original := []byte(input)
		if got := string(withoutShebang(original)); got != want {
			t.Fatalf("withoutShebang(%q) = %q, want %q", input, got, want)
		}
		if string(original) != input {
			t.Fatal("withoutShebang mutated the source it was given")
		}
	}
}

func TestDeclarationFilesAreObservedWithoutBun(t *testing.T) {
	bun := requireBun(t)
	root := t.TempDir()
	// Valid ambient TypeScript that Bun parses as an implementation file and
	// refuses, shaped like the monorepo's components-dom scripts/play-mounts.d.ts.
	write(t, filepath.Join(root, "src", "mounts.d.ts"), `/** Mount modules, as "@false/doc" says. */
import type { Mount } from "@scope/mounts"
import "@scope/side-effect"
export const modules: ReadonlyArray<Mount>
export type Lazy = typeof import("@scope/lazy")
declare module "@scope/augmented" { export const x: number }
// import "@false/comment"
export type Name = "@false/string-type"
`)
	if _, err := sourceImports(context.Background(), root, []string{"src"}, nil, legacySourceObservation, bun); err == nil || !strings.Contains(err.Error(), "must be initialized") {
		t.Fatalf("released walk of an ambient declaration = %v", err)
	}
	imports, err := sourceImports(context.Background(), root, []string{"src"}, nil, typeScriptIncludeObservation, bun)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := specifiersBySource(imports), map[string][]string{"src/mounts.d.ts": {"@scope/mounts", "@scope/side-effect", "@scope/lazy"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("declaration imports = %#v, want %#v", got, want)
	}
	if imports[0].Line != 2 || imports[2].Line != 5 {
		t.Fatalf("declaration import lines = %#v", imports)
	}
}

func TestDeclarationFileImportCreatesAWorkspaceEdge(t *testing.T) {
	bun := requireBun(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "app", "src", "index.ts"), "export const app = 1\n")
	write(t, filepath.Join(root, "app", "src", "global.d.ts"), "import type { Theme } from \"@a/theme\"\nexport const theme: Theme\n")
	write(t, filepath.Join(root, "theme", "src", "index.ts"), "export type Theme = string\n")
	resource := Resource{Identity: "phosphorco/mono", CanonicalPath: "repos/mono", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Packages: map[string]contract.PackagePolicy{
		"@a/app": {Directory: "app", DevDependencies: map[string]string{"typescript": "5.9.3"}}, "@a/theme": {Directory: "theme"},
	}}
	packages, err := observePackagesAt(context.Background(), []Resource{resource}, "0.13.0", map[string]string{resource.Identity: root}, bun)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := workspace.BuildWithOptions(packages, workspace.BuildOptions{ReassembleRootDependencies: true, ProductionTypeScript: true, RuntimeBuiltins: workspace.RuntimeBuiltinModulesV0130()})
	if err != nil {
		t.Fatal(err)
	}
	if manifest := string(projection.Files[filepath.Join("repos", "mono", "app", "package.json")]); !strings.Contains(manifest, `"@a/theme": "workspace:*"`) {
		t.Fatalf("declaration import did not create a workspace edge:\n%s", manifest)
	}
}

func TestReleasedObservationStillAllowsSharedDirectories(t *testing.T) {
	bun := requireBun(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "shared", "src", "index.ts"), "export {}\n")
	resource := Resource{Identity: "phosphorco/mono", CanonicalPath: "repos/mono", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Packages: map[string]contract.PackagePolicy{
		"@a/shared": {}, "@b/shared": {},
	}}
	packages, err := observePackagesAt(context.Background(), []Resource{resource}, "0.12.0", map[string]string{resource.Identity: root}, bun)
	if err != nil || len(packages) != 2 || packages[0].Directory != packages[1].Directory {
		t.Fatalf("0.12 leaf collision = %#v, %v", packages, err)
	}
	if _, err := observePackagesAt(context.Background(), []Resource{resource}, "0.13.0", map[string]string{resource.Identity: root}, bun); err == nil || !strings.Contains(err.Error(), "both resolve to directory") {
		t.Fatalf("0.13 leaf collision = %v", err)
	}
}

func TestObservationRefusesNestedPackagesAndMissingExcludes(t *testing.T) {
	bun := requireBun(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "outer", "src", "index.ts"), "export {}\n")
	write(t, filepath.Join(root, "outer", "src", "inner", "src", "index.ts"), "export {}\n")
	write(t, filepath.Join(root, "outer", "testing", "src", "index.ts"), "export {}\n")
	for _, test := range []struct {
		name     string
		packages map[string]contract.PackagePolicy
		want     string
	}{
		{name: "inside a source root", packages: map[string]contract.PackagePolicy{"outer": {Directory: "outer"}, "inner": {Directory: "outer/src/inner"}},
			want: `package "inner" in "phosphorco/mono" lies at "outer/src/inner", inside source root "outer/src" of package "outer"`},
		{name: "missing exclude", packages: map[string]contract.PackagePolicy{"outer": {Directory: "outer", SourceExcludes: []string{"src/fixtures"}}},
			want: `source exclude "src/fixtures": "src/fixtures" does not exist`},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource := Resource{Identity: "phosphorco/mono", CanonicalPath: "repos/mono", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Packages: test.packages}
			if _, err := observePackagesAt(context.Background(), []Resource{resource}, "0.13.0", map[string]string{resource.Identity: root}, bun); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("observe = %v, want %q", err, test.want)
			}
		})
	}
	// A package beside another package's source roots, not inside them, is fine.
	resource := Resource{Identity: "phosphorco/mono", CanonicalPath: "repos/mono", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Packages: map[string]contract.PackagePolicy{
		"outer": {Directory: "outer"}, "testing": {Directory: "outer/testing"},
	}}
	if _, err := observePackagesAt(context.Background(), []Resource{resource}, "0.13.0", map[string]string{resource.Identity: root}, bun); err != nil {
		t.Fatal(err)
	}
}

// The frozen 0.13.0 built-in set is the pinned Bun's builtinModules without
// its npm-named polyfills. This cross-check runs against the Bun on PATH, which
// CI pins through mise; the set itself never reads it.
func TestFrozenRuntimeBuiltinsMatchThePinnedBunWithoutNpmPolyfills(t *testing.T) {
	bun := requireBun(t)
	output, err := exec.Command(bun, "-e", `process.stdout.write(JSON.stringify(require("node:module").builtinModules))`).Output()
	if err != nil {
		t.Fatal(err)
	}
	var modules []string
	if err := json.Unmarshal(output, &modules); err != nil {
		t.Fatal(err)
	}
	frozen := workspace.RuntimeBuiltinModulesV0130()
	extra := make([]string, 0)
	for _, module := range modules {
		if !frozen[module] && !strings.HasPrefix(module, "bun:") {
			extra = append(extra, module)
		}
		delete(frozen, module)
	}
	sort.Strings(extra)
	if !reflect.DeepEqual(extra, []string{"undici", "ws"}) || len(frozen) != 0 {
		t.Fatalf("pinned Bun differs from the frozen set: Bun-only %q, frozen-only %v", extra, frozen)
	}
}

func TestInRepositoryScopesRefuseRootPackagesInsideAListedScope(t *testing.T) {
	source := inRepositoryScopeSource(t, "0.13.0")
	checkout := t.TempDir()
	write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), scopeFixtureDeclaration)
	declaration := contract.Declaration{Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Scopes: []string{"@alpha"}, Packages: map[string]contract.PackagePolicy{"@alpha/extra": {Directory: "elsewhere"}}}
	err := source.mergeInRepositoryScopes("phosphorco/mono", checkout, &declaration)
	if err == nil || !strings.Contains(err.Error(), `package "@alpha/extra" is in listed scope @alpha; declare it in pkg/@alpha/workbench.pkl`) {
		t.Fatalf("root package inside a listed scope = %v", err)
	}
}

func TestInRepositoryScopeIncludesUnionEverySelectionForm(t *testing.T) {
	source := inRepositoryScopeSource(t, "0.13.0")
	checkout := t.TempDir()
	scope := func(name, editing string) string {
		return fmt.Sprintf("amends \"workbench-contract:/0.13.0/PackageScopeRepository.pkl\"\nscope = %q\nincludes {\n  [\"phosphorco/library\"] { skills { editing %s } }\n  [\"phosphorco/mono\"] {}\n}\n", name, editing)
	}
	write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), scope("@alpha", `{ domains = Set("engineering") }`))
	write(t, filepath.Join(checkout, "pkg", "@beta", "workbench.pkl"), scope("@beta", `{ names = Set("beta-skill"); domains = Set("general") }`))
	root := contract.SkillSelection{Names: []string{"root-skill"}}
	declaration := contract.Declaration{
		Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Scopes: []string{"@alpha", "@beta"},
		Includes: map[string]contract.ResourceInclude{"phosphorco/library": {Skills: &contract.SkillPolicy{Editing: &root}}},
	}
	if err := source.mergeInRepositoryScopes("phosphorco/mono", checkout, &declaration); err != nil {
		t.Fatal(err)
	}
	editing := declaration.Includes["phosphorco/library"].Skills.Editing
	if editing.All || !reflect.DeepEqual(editing.Domains, []string{"engineering", "general"}) || !reflect.DeepEqual(editing.Names, []string{"beta-skill", "root-skill"}) {
		t.Fatalf("two scopes contributing to one include = %#v", editing)
	}
	// A scope include naming the Repository itself is the same no-op include a
	// root declaration may carry: the closure already contains the Repository.
	if _, included := declaration.Includes["phosphorco/mono"]; !included {
		t.Fatalf("self include was not merged: %#v", declaration.Includes)
	}
	write(t, filepath.Join(checkout, "pkg", "@beta", "workbench.pkl"), scope("@beta", `= "all"`))
	declaration = contract.Declaration{Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Scopes: []string{"@alpha", "@beta"}}
	if err := source.mergeInRepositoryScopes("phosphorco/mono", checkout, &declaration); err != nil {
		t.Fatal(err)
	}
	if editing := declaration.Includes["phosphorco/library"].Skills.Editing; !editing.All {
		t.Fatalf("an all selection did not survive the union: %#v", editing)
	}
}

func TestInRepositoryScopeFilesCannotReachLocalModulesOrResources(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "Shared.pkl")
	write(t, outside, "scope = \"@alpha\"\n")
	for name, test := range map[string]struct{ body, want string }{
		"absolute module import": {fmt.Sprintf("local shared = import(%q)\nscope = shared.scope\n", "file://"+outside), "file://" + outside},
		"file resource read":     {fmt.Sprintf("local leaked = read(%q)\nscope = leaked.text\n", "file://"+outside), "file://" + outside},
		"environment read":       {"local leaked = read(\"env:HOME\")\nscope = leaked\n", "env:HOME"},
	} {
		t.Run(name, func(t *testing.T) {
			source := inRepositoryScopeSource(t, "0.13.0")
			checkout := t.TempDir()
			write(t, filepath.Join(checkout, "pkg", "@alpha", "workbench.pkl"), "amends \"workbench-contract:/0.13.0/PackageScopeRepository.pkl\"\n"+test.body)
			declaration := contract.Declaration{Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, Scopes: []string{"@alpha"}}
			err := source.mergeInRepositoryScopes("phosphorco/mono", checkout, &declaration)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(strings.ToLower(err.Error()), "allowlist") {
				t.Fatalf("scope file reached %s: %v", test.want, err)
			}
		})
	}
}

// Every released Subject contract evaluates against this one schema, so its
// bytes are pinned like a frozen historical contract.
func TestSubjectContractBytesArePinned(t *testing.T) {
	got := sha256.Sum256([]byte(localSubjectContract))
	if hex.EncodeToString(got[:]) != "e119dc2246b2ff4091255848a6fd997fb6b19b99be623154293a68c56922b101" {
		t.Fatalf("WorkbenchSubject.pkl changed (sha256 %x); every released Subject contract evaluates against it", got)
	}
}
