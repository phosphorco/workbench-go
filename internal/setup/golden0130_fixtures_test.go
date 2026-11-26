package setup

import (
	"github.com/phosphorco/workbench-go/internal/contract"
)

// golden0130Fixture is one 0.13 declaration closure and its source tree. The
// same fixtures were projected by the released 0.13.0 code (tag 0.13.0) to
// produce testdata/golden-0.13.0; the current code must reproduce those bytes
// for 0.13.0 declarations (contract 0.14 item 13).
type golden0130Fixture struct {
	name      string
	resources []Resource
	files     map[string]string
}

func golden0130Fixtures() []golden0130Fixture {
	repository := contract.ResourceShape{Kind: contract.RepositoryShape}
	return []golden0130Fixture{
		{
			name: "layout",
			resources: []Resource{{
				Identity: "phosphorco/mono", GitHub: "phosphorco/mono", CanonicalPath: "repos/mono", Shape: repository,
				Packages: map[string]contract.PackagePolicy{
					"tool-cli": {
						Directory: "tools/cli", SourceRoots: []string{"src", "__tests__"}, SourceExcludes: []string{"__tests__/fixtures"},
						DependencyVersions: "verbatim",
						Dependencies:       map[string]string{"thing": "file:./vendor/thing", "aliased-number": "npm:is-number@7.0.0"},
						DevDependencies:    map[string]string{"typescript": "file:./vendor/typescript"},
					},
					"@alpha/core": {Directory: "pkg/@alpha/core", Exports: map[string]string{".": "./src/index.ts"}},
					"@alpha/web": {
						Directory: "pkg/@alpha/web", SourceRoots: []string{"src", "__tests__"},
						Exports: map[string]string{".": "./src/index.ts", "./view": "./src/view.mts"},
						Imports: map[string]string{"#src/*": "./src/*"},
					},
				},
			}},
			files: map[string]string{
				"pkg/@alpha/core/src/index.ts":         "import { readFileSync } from \"fs\"\nexport const core = readFileSync\n",
				"pkg/@alpha/web/src/index.ts":          "import { core } from \"@alpha/core\"\nimport { x } from \"#src/x.ts\"\nexport const web = [core, x]\n",
				"pkg/@alpha/web/src/x.ts":              "export const x = 1\n",
				"pkg/@alpha/web/src/view.mts":          "import \"@unobserved/mts-under-0.13\"\nexport const view = 1\n",
				"pkg/@alpha/web/__tests__/web.test.ts": "import { web } from \"../src/index.ts\"\nexport const tested = web\n",
				"tools/cli/src/main.ts":                "#!/usr/bin/env bun\nimport { core } from \"@alpha/core\"\nimport { $ } from \"bun\"\nimport thing from \"thing\"\nimport aliased from \"aliased-number\"\nexport const main = [core, $, thing, aliased]\n",
				"tools/cli/src/ambient.d.ts":           "import type { Core } from \"@alpha/core\"\nexport const modules: ReadonlyArray<Core>\n",
				"tools/cli/src/.prototypes/draft.ts":   "import \"@missing/prototype\"\n",
				"tools/cli/src/node_modules/x/a.ts":    "import \"@missing/vendored-source\"\n",
				"tools/cli/__tests__/cli.test.ts":      "import { web } from \"@alpha/web\"\nexport const tested = web\n",
				"tools/cli/__tests__/fixtures/bad.ts":  "import \"@missing/fixture\"\n",
			},
		},
		{
			name: "exact-scope",
			resources: []Resource{{
				Identity: "@beta", GitHub: "phosphorco/beta", CanonicalPath: "pkg/@beta",
				Shape: contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@beta"},
				Packages: map[string]contract.PackagePolicy{
					"@beta/lib": {
						DevDependencies:  map[string]string{"typescript": "5.9.3"},
						PeerDependencies: map[string]string{"effect": "3.19.0"},
					},
					"@beta/app": {
						Dependencies: map[string]string{"zod": "4.1.0"},
						Exports:      map[string]string{".": "./src/index.ts", "./*": "./src/*.ts"},
					},
				},
			}},
			files: map[string]string{
				"lib/src/index.ts":              "import { Effect } from \"effect\"\nexport const lib = Effect\n",
				"lib/src/index.test.ts":         "import { app } from \"@beta/app\"\nexport const t = app\n",
				"app/src/index.ts":              "import { lib } from \"@beta/lib\"\nimport { z } from \"zod\"\nexport const app = [lib, z]\n",
				"app/src/__tests__/app.spec.ts": "import { app } from \"../index.ts\"\nexport const t = app\n",
				"app/src/nested/deep/thing.tsx": "export const thing = 1\n",
				"app/src/nested/deep/thing.cts": "import \"@unobserved/cts-under-0.13\"\n",
			},
		},
	}
}
