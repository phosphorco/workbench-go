package acceptance

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	layoutSubjectContract      = "workbench-contract:/0.13.0/WorkbenchSubject.pkl"
	layoutRepositoryContract   = "workbench-contract:/0.13.0/Repository.pkl"
	layoutPackageScopeContract = "workbench-contract:/0.13.0/PackageScopeRepository.pkl"
)

// layoutRepository is a monorepo-shaped Repository: one package at an explicit
// directory with tests beside src, and an in-repository scope declared by its
// own pkg/@alpha/workbench.pkl. Every dependency is local, so bun install runs
// offline; the TypeScript authority is a vendored stand-in.
func layoutRepository() (string, map[string]string) {
	root := fmt.Sprintf(`amends %q

scopes { "@alpha" }
packages {
  ["tool-cli"] {
    directory = "tools/cli"
    sourceRoots { "src"; "__tests__" }
    sourceExcludes { "__tests__/fixtures" }
    dependencyVersions = "verbatim"
    dependencies { ["thing"] = "file:./vendor/thing" }
    devDependencies { ["typescript"] = "file:./vendor/typescript" }
  }
}
`, layoutRepositoryContract)
	files := map[string]string{
		".gitignore": "/tools/cli/package.json\n/tools/cli/tsconfig.json\n/pkg/@alpha/*/package.json\n/pkg/@alpha/*/tsconfig.json\n/tools/cli/node_modules/\n/pkg/@alpha/*/node_modules/\n",
		"pkg/@alpha/workbench.pkl": fmt.Sprintf(`amends %q

scope = "@alpha"
packages {
  ["@alpha/core"] { exports { ["."] = "./src/index.ts" } }
  ["@alpha/web"] { sourceRoots { "src"; "__tests__" }; exports { ["."] = "./src/index.ts" } }
}
`, layoutPackageScopeContract),
		"pkg/@alpha/core/src/index.ts":         "import { readFileSync } from \"fs\"\nexport const core = readFileSync\n",
		"pkg/@alpha/web/src/index.ts":          "import { core } from \"@alpha/core\"\nexport const web = core\n",
		"pkg/@alpha/web/__tests__/web.test.ts": "import { web } from \"../src/index.ts\"\nexport const tested = web\n",
		"tools/cli/src/main.ts": `#!/usr/bin/env bun
import { core } from "@alpha/core"
import { $ } from "bun"
import thing from "thing"
export const main = [core, $, thing]
`,
		"tools/cli/src/ambient.d.ts":               "export const modules: ReadonlyArray<string>\n",
		"tools/cli/src/.prototypes/draft.ts":       "import \"@missing/prototype\"\n",
		"tools/cli/src/node_modules/x/index.ts":    "import \"@missing/vendored-source\"\n",
		"tools/cli/__tests__/cli.test.ts":          "import { web } from \"@alpha/web\"\nexport const tested = web\n",
		"tools/cli/__tests__/fixtures/broken.ts":   "import \"@missing/fixture\"\n",
		"tools/cli/vendor/thing/package.json":      `{"name": "thing", "version": "1.0.0", "main": "index.js"}` + "\n",
		"tools/cli/vendor/thing/index.js":          "module.exports = 1\n",
		"tools/cli/vendor/typescript/package.json": `{"name": "typescript", "version": "5.9.3"}` + "\n",
	}
	return root, files
}

func TestRepositoryLayoutPlacesDeclaredAndInRepositoryScopePackages(t *testing.T) {
	fixture := newPackageScopeFixture(t)
	declaration, files := layoutRepository()
	createRemote(t, fixture.root, fixture.remotes, "mono", declaration, files)
	workbench := fixture.newWorkbench(t, "layout", layoutSubjectContract, "", "mono")
	fixture.runSetup(t, workbench, true)

	var root struct {
		Workspaces      []string          `json:"workspaces"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	decodePackageScopeJSON(t, filepath.Join(workbench, "package.json"), &root)
	if want := []string{"repos/mono/pkg/@alpha/core", "repos/mono/pkg/@alpha/web", "repos/mono/tools/cli"}; !slices.Equal(root.Workspaces, want) {
		t.Fatalf("workspaces = %q, want %q", root.Workspaces, want)
	}
	if want := map[string]string{"thing": "file:repos/mono/tools/cli/vendor/thing", "typescript": "file:repos/mono/tools/cli/vendor/typescript"}; !maps.Equal(root.DevDependencies, want) {
		t.Fatalf("root dependencies = %#v", root.DevDependencies)
	}

	var cli struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	decodePackageScopeJSON(t, filepath.Join(workbench, "repos", "mono", "tools", "cli", "package.json"), &cli)
	// Verbatim specifiers are unchanged; "bun" is a runtime module; the shebang
	// file, the __tests__ root and the scope packages are all observed, while
	// hidden, dependency, excluded and ambient declaration files are not.
	if want := map[string]string{"@alpha/core": "workspace:*", "thing": "file:./vendor/thing"}; !maps.Equal(cli.Dependencies, want) {
		t.Fatalf("cli dependencies = %#v", cli.Dependencies)
	}
	if want := map[string]string{"@alpha/web": "workspace:*", "typescript": "file:./vendor/typescript"}; !maps.Equal(cli.DevDependencies, want) {
		t.Fatalf("cli devDependencies = %#v", cli.DevDependencies)
	}
	var tsconfig struct {
		CompilerOptions map[string]any `json:"compilerOptions"`
		Include         []string       `json:"include"`
		Exclude         []string       `json:"exclude"`
		References      []struct {
			Path string `json:"path"`
		} `json:"references"`
	}
	decodePackageScopeJSON(t, filepath.Join(workbench, "repos", "mono", "tools", "cli", "tsconfig.json"), &tsconfig)
	if tsconfig.CompilerOptions["rootDir"] != "." || !slices.Equal(tsconfig.Include, []string{"src/**/*.ts", "src/**/*.tsx", "__tests__/**/*.ts", "__tests__/**/*.tsx"}) || !slices.Equal(tsconfig.Exclude, []string{"__tests__/fixtures"}) {
		t.Fatalf("cli tsconfig = %#v", tsconfig)
	}
	if len(tsconfig.References) != 2 || tsconfig.References[0].Path != "../../pkg/@alpha/core" || tsconfig.References[1].Path != "../../pkg/@alpha/web" {
		t.Fatalf("cli references = %#v", tsconfig.References)
	}
	var web struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	decodePackageScopeJSON(t, filepath.Join(workbench, "repos", "mono", "pkg", "@alpha", "web", "package.json"), &web)
	if !maps.Equal(web.Dependencies, map[string]string{"@alpha/core": "workspace:*"}) {
		t.Fatalf("scope package dependencies = %#v", web.Dependencies)
	}
	// No resource is created for an in-repository scope.
	if _, err := os.Stat(filepath.Join(workbench, "pkg", "@alpha")); !os.IsNotExist(err) {
		t.Fatalf("in-repository scope became a resource: %v", err)
	}
	before := digestTree(t, workbench, "package.json", "tsconfig.json", "repos/mono/tools/cli/package.json", "repos/mono/pkg/@alpha/web/tsconfig.json")
	fixture.runSetup(t, workbench, true)
	if after := digestTree(t, workbench, "package.json", "tsconfig.json", "repos/mono/tools/cli/package.json", "repos/mono/pkg/@alpha/web/tsconfig.json"); after != before {
		t.Fatal("a second setup changed the layout projection")
	}
}

func TestRepositoryLayoutRefusesBeforeGeneratedMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(root string, files map[string]string) string
		want   []string
	}{
		{name: "missing scope file", mutate: func(root string, files map[string]string) string {
			delete(files, "pkg/@alpha/workbench.pkl")
			return root
		}, want: []string{`scope pkg/@alpha/workbench.pkl: "pkg/@alpha/workbench.pkl" does not exist`}},
		{name: "scope file declares another scope", mutate: func(root string, files map[string]string) string {
			files["pkg/@alpha/workbench.pkl"] = strings.ReplaceAll(strings.Replace(files["pkg/@alpha/workbench.pkl"], `scope = "@alpha"`, `scope = "@beta"`, 1), "@alpha/", "@beta/")
			return root
		}, want: []string{`declares scope "@beta", want "@alpha"`}},
		{name: "package declared at root and in scope", mutate: func(root string, files map[string]string) string {
			return strings.Replace(root, "packages {\n", "packages {\n  [\"@alpha/web\"] { directory = \"pkg/@alpha/web\" }\n", 1)
		}, want: []string{`package "@alpha/web" is declared both by the repository workbench.pkl and by pkg/@alpha/workbench.pkl`}},
		{name: "scope file at an older contract", mutate: func(root string, files map[string]string) string {
			files["pkg/@alpha/workbench.pkl"] = strings.Replace(files["pkg/@alpha/workbench.pkl"], "/0.13.0/", "/0.12.0/", 1)
			return root
		}, want: []string{"declaration is 0.12.0, want exact 0.13.0"}},
		{name: "missing declared directory", mutate: func(root string, files map[string]string) string {
			return strings.Replace(root, `directory = "tools/cli"`, `directory = "tools/absent"`, 1)
		}, want: []string{`"tools/absent" does not exist`}},
		{name: "escaping declared directory", mutate: func(root string, files map[string]string) string {
			return strings.Replace(root, `directory = "tools/cli"`, `directory = "../cli"`, 1)
		}, want: []string{"tool-cli", "directory"}},
		{name: "conflicting verbatim specifiers", mutate: func(root string, files map[string]string) string {
			files["pkg/@alpha/workbench.pkl"] = strings.Replace(files["pkg/@alpha/workbench.pkl"], `["@alpha/core"] {`, `["@alpha/core"] { dependencyVersions = "verbatim"; dependencies { ["thing"] = "^1.0.0"; ["typescript"] = "catalog:" };`, 1)
			return root
		}, want: []string{"conflicting authority in 2 dependency(ies)", `"thing": "^1.0.0" by @alpha/core dependencies; "file:repos/mono/tools/cli/vendor/thing" by tool-cli dependencies`, `"typescript":`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPackageScopeFixture(t)
			root, files := layoutRepository()
			root = test.mutate(root, files)
			createRemote(t, fixture.root, fixture.remotes, "mono", root, files)
			workbench := fixture.newWorkbench(t, "refused", layoutSubjectContract, "", "mono")
			writeFile(t, filepath.Join(workbench, "package.json"), "outer manifest must survive\n")
			output := fixture.runSetup(t, workbench, false)
			for _, want := range test.want {
				if !strings.Contains(output, want) {
					t.Fatalf("refusal lacks %q:\n%s", want, output)
				}
			}
			assertFileContents(t, filepath.Join(workbench, "package.json"), "outer manifest must survive\n")
			for _, generated := range []string{"tsconfig.json", "repos", ".workbench/managed-checkouts.json", "node_modules"} {
				if _, err := os.Stat(filepath.Join(workbench, filepath.FromSlash(generated))); !os.IsNotExist(err) {
					t.Fatalf("refused layout mutated %q: %v", generated, err)
				}
			}
		})
	}
}

// A released 0.12.0 declaration keeps its observation: hidden directories are
// still walked, so an unresolvable import there is still a closure gap.
func TestRepositoryLayoutLeaves0120ObservationUnchanged(t *testing.T) {
	files := map[string]string{
		".gitignore":               "/package.json\n/tsconfig.json\n/node_modules/\n/dist/\n",
		"src/index.ts":             "export const released = true\n",
		"src/.prototypes/draft.ts": "import \"@missing/prototype\"\n",
	}
	for _, test := range []struct {
		version string
		succeed bool
	}{{"0.12.0", false}, {"0.13.0", true}} {
		t.Run(test.version, func(t *testing.T) {
			fixture := newPackageScopeFixture(t)
			declaration := fmt.Sprintf("amends \"workbench-contract:/%s/Repository.pkl\"\n\npackages {\n  [\"released\"] { devDependencies { [\"typescript\"] = \"file:./vendor/typescript\" } }\n}\n", test.version)
			withVendor := maps.Clone(files)
			withVendor["vendor/typescript/package.json"] = `{"name": "typescript", "version": "5.9.3"}` + "\n"
			if test.version == "0.12.0" {
				// 0.12.0 requires exact versions; it never reaches bun install here.
				declaration = strings.Replace(declaration, "file:./vendor/typescript", "5.9.3", 1)
			} else {
				declaration = strings.Replace(declaration, "{ devDependencies", "{ dependencyVersions = \"verbatim\"; devDependencies", 1)
			}
			createRemote(t, fixture.root, fixture.remotes, "released", declaration, withVendor)
			workbench := fixture.newWorkbench(t, "released", "workbench-contract:/"+test.version+"/WorkbenchSubject.pkl", "", "released")
			output := fixture.runSetup(t, workbench, test.succeed)
			if !test.succeed && !strings.Contains(output, `src/.prototypes/draft.ts:1: package "released" imports "@missing/prototype"`) {
				t.Fatalf("0.12.0 observation changed:\n%s", output)
			}
		})
	}
}

func digestTree(t *testing.T, root string, paths ...string) string {
	t.Helper()
	contents := make(map[string]string, len(paths))
	for _, path := range paths {
		contents[path] = readFile(t, filepath.Join(root, filepath.FromSlash(path)))
	}
	encoded, err := json.Marshal(contents)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
