package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
)

func TestBuildDerivesCrossRepositoryWorkspaceAndTypeScriptReferences(t *testing.T) {
	projection, err := Build([]Package{
		{
			Name: "@phosphorco/community", Directory: "pkg/@phosphorco/community",
			Policy: contract.PackagePolicy{Exports: map[string]string{"./tracing": "./src/tracing.ts"}},
		},
		{
			Name:      "@basindb/core",
			Directory: "pkg/@basindb/core",
			Imports: []Import{
				{Specifier: "@phosphorco/community/tracing", Source: "pkg/@basindb/core/src/index.ts", Line: 1},
				{Specifier: "node:fs", Source: "pkg/@basindb/core/src/index.ts", Line: 2},
			},
			Policy: contract.PackagePolicy{
				PeerDependencies: map[string]string{"effect": "^4.0.0"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var root rootPackageJSON
	if err := json.Unmarshal(projection.Files["package.json"], &root); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(root.Workspaces, []string{"pkg/@basindb/core", "pkg/@phosphorco/community"}) {
		t.Fatalf("workspaces = %#v", root.Workspaces)
	}

	var manifest packageJSON
	if err := json.Unmarshal(projection.Files["pkg/@basindb/core/package.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Dependencies["@phosphorco/community"] != "workspace:*" {
		t.Fatalf("dependencies = %#v", manifest.Dependencies)
	}
	if manifest.PeerDependencies["effect"] != "^4.0.0" {
		t.Fatalf("peer dependencies = %#v", manifest.PeerDependencies)
	}

	var tsconfig packageTSConfig
	if err := json.Unmarshal(projection.Files["pkg/@basindb/core/tsconfig.json"], &tsconfig); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tsconfig.References, []tsReference{{Path: "../../@phosphorco/community"}}) {
		t.Fatalf("references = %#v", tsconfig.References)
	}
	if got := tsconfig.CompilerOptions["tsBuildInfoFile"]; got != "dist/tsconfig.tsbuildinfo" {
		t.Fatalf("tsBuildInfoFile = %#v, want compiler state inside ignored dist", got)
	}
}

func TestBuildProjectsTypedPackageMetadata(t *testing.T) {
	projection, err := Build([]Package{{
		Name: "@infra/local-process-alchemy", Directory: "repos/services/packages/local-process-alchemy",
		Imports: []Import{
			{Specifier: "#src/internal/local-process.ts", Source: "repos/services/packages/local-process-alchemy/src/LocalProcess.ts", Line: 20},
			{Specifier: "@phosphor/test/Assert", Source: "repos/services/packages/local-process-alchemy/src/local-process.test.ts", Line: 6},
			{Specifier: "alchemy", Source: "repos/services/packages/local-process-alchemy/src/internal/local-process.ts", Line: 4},
		},
		Policy: contract.PackagePolicy{
			Dependencies:    map[string]string{"alchemy": "2.0.0-beta.52"},
			DevDependencies: map[string]string{"@phosphor/test": "^0.1.0"},
			Imports:         map[string]string{"#src/*": "./src/*"},
			Exports:         map[string]string{"./LocalProcess": "./src/LocalProcess.ts"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	var manifest packageJSON
	if err := json.Unmarshal(projection.Files["repos/services/packages/local-process-alchemy/package.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.Imports, map[string]string{"#src/*": "./src/*"}) {
		t.Fatalf("imports = %#v", manifest.Imports)
	}
	wantExports := map[string]any{"./LocalProcess": "./src/LocalProcess.ts"}
	if !reflect.DeepEqual(manifest.Exports, wantExports) {
		t.Fatalf("exports = %#v, want %#v", manifest.Exports, wantExports)
	}
	if manifest.Dependencies["alchemy"] != "2.0.0-beta.52" || manifest.DevDependencies["@phosphor/test"] != "^0.1.0" {
		t.Fatalf("dependency classes = dependencies %#v devDependencies %#v", manifest.Dependencies, manifest.DevDependencies)
	}
}

func TestBuildReportsClosureGapsWithSourceProvenanceAndRemedy(t *testing.T) {
	_, err := Build([]Package{
		{
			Name: "@infra/local-dev-server-alchemy", Directory: "repos/services/packages/local-dev-server-alchemy",
			Imports: []Import{
				{Specifier: "@application-hosts/local-dev-server/Paths", Source: "repos/services/packages/local-dev-server-alchemy/src/local-dev-server.ts", Line: 8},
				{Specifier: "@application-hosts/local-dev-server/Resource", Source: "repos/services/packages/local-dev-server-alchemy/src/local-dev-server.ts", Line: 9},
			},
			Policy: contract.PackagePolicy{Dependencies: map[string]string{"@application-hosts/local-dev-server": "workspace:*"}},
		},
		{
			Name: "@phosphor/test", Directory: "repos/test/src",
			Imports: []Import{
				{Specifier: "@phosphor/tracer-demultiplexer", Source: "repos/test/src/Tracer.test.ts", Line: 6, Development: true},
				{Specifier: "@phosphor/tracer-demultiplexer/Tracer", Source: "repos/test/src/Tracer.test.ts", Line: 7, Development: true},
			},
			Policy: contract.PackagePolicy{DevDependencies: map[string]string{"@phosphor/tracer-demultiplexer": "workspace:*"}},
		},
	})
	var closure *ClosureError
	if !errors.As(err, &closure) {
		t.Fatalf("closure error = %v", err)
	}
	if len(closure.Diagnostics) != 4 {
		t.Fatalf("diagnostics = %#v", closure.Diagnostics)
	}
	want := []struct {
		importer  string
		specifier string
		source    string
		line      int
	}{
		{"@infra/local-dev-server-alchemy", "@application-hosts/local-dev-server/Paths", "repos/services/packages/local-dev-server-alchemy/src/local-dev-server.ts", 8},
		{"@infra/local-dev-server-alchemy", "@application-hosts/local-dev-server/Resource", "repos/services/packages/local-dev-server-alchemy/src/local-dev-server.ts", 9},
		{"@phosphor/test", "@phosphor/tracer-demultiplexer", "repos/test/src/Tracer.test.ts", 6},
		{"@phosphor/test", "@phosphor/tracer-demultiplexer/Tracer", "repos/test/src/Tracer.test.ts", 7},
	}
	for index, diagnostic := range closure.Diagnostics {
		if diagnostic.Importer != want[index].importer || diagnostic.Specifier != want[index].specifier || diagnostic.Source != want[index].source || diagnostic.Line != want[index].line {
			t.Fatalf("diagnostic %d provenance = %#v, want %#v", index, diagnostic, want[index])
		}
		if !strings.Contains(diagnostic.Remedy, "add the Repository") || !strings.Contains(diagnostic.Remedy, `version "workspace:*" requires`) || strings.Contains(diagnostic.Remedy, "external dependency class") {
			t.Fatalf("diagnostic %d remedy = %q", index, diagnostic.Remedy)
		}
	}
}

func TestBuildRefusesConflictingDependencyTruth(t *testing.T) {
	t.Run("external classes", func(t *testing.T) {
		_, err := Build([]Package{{
			Name: "@entry/app", Directory: "pkg/@entry/app",
			Policy: contract.PackagePolicy{
				Dependencies:     map[string]string{"effect": "^4.0.0"},
				PeerDependencies: map[string]string{"effect": "^4.0.0"},
			},
		}})
		if err == nil || !strings.Contains(err.Error(), "exactly one class") {
			t.Fatalf("dependency class conflict = %v", err)
		}
	})

	t.Run("participating package uses registry version", func(t *testing.T) {
		_, err := Build([]Package{
			{Name: "@library/shared", Directory: "pkg/@library/shared"},
			{
				Name: "@entry/app", Directory: "pkg/@entry/app",
				Imports: []Import{{Specifier: "@library/shared", Source: "pkg/@entry/app/src/index.ts", Line: 1}},
				Policy:  contract.PackagePolicy{Dependencies: map[string]string{"@library/shared": "^1.0.0"}},
			},
		})
		if err == nil || !strings.Contains(err.Error(), `participating dependencies must use workspace:*`) || !strings.Contains(err.Error(), `dependencies declares version "^1.0.0"`) {
			t.Fatalf("participating version conflict = %v", err)
		}
	})

	t.Run("participating package uses nonexact workspace version", func(t *testing.T) {
		_, err := Build([]Package{
			{Name: "@library/shared", Directory: "pkg/@library/shared"},
			{Name: "@entry/app", Directory: "pkg/@entry/app", Policy: contract.PackagePolicy{DevDependencies: map[string]string{"@library/shared": "workspace:^"}}},
		})
		if err == nil || !strings.Contains(err.Error(), `devDependencies declares version "workspace:^"`) || !strings.Contains(err.Error(), `must use workspace:*`) {
			t.Fatalf("participating workspace protocol conflict = %v", err)
		}
	})

	t.Run("catalog dependency has no generated catalog authority", func(t *testing.T) {
		_, err := Build([]Package{{
			Name: "@entry/app", Directory: "pkg/@entry/app",
			Policy: contract.PackagePolicy{DevDependencies: map[string]string{"typescript": "catalog:"}},
		}})
		for _, fact := range []string{"@entry/app", "devDependencies", "typescript", "catalog:", "exact resolved version"} {
			if err == nil || !strings.Contains(err.Error(), fact) {
				t.Fatalf("catalog dependency refusal = %v; lacks %q", err, fact)
			}
		}
	})

}

func TestGeneratedTestScriptExcludesEmittedTests(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	projection, err := Build([]Package{{Name: "@entry/app", Directory: "pkg/@entry/app"}})
	if err != nil {
		t.Fatal(err)
	}
	var root rootPackageJSON
	if err := json.Unmarshal(projection.Files["package.json"], &root); err != nil {
		t.Fatal(err)
	}
	rootDir := t.TempDir()
	writeTestFile := func(relative, contents string) {
		t.Helper()
		path := filepath.Join(rootDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile("pkg/@entry/app/src/example.test.ts", `import { expect, test } from "bun:test"; test("source", () => expect(true).toBe(true));`)
	writeTestFile("pkg/@entry/app/dist/example.test.js", `import { expect, test } from "bun:test"; test("emitted duplicate", () => expect(true).toBe(false));`)
	command := exec.Command(bun, "test", "--path-ignore-patterns=**/dist/**")
	command.Dir = rootDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generated test script %q: %v\n%s", root.Scripts["test"], err, output)
	}
	if !strings.Contains(string(output), "1 pass") || strings.Contains(string(output), "emitted duplicate") {
		t.Fatalf("generated test script ran an emitted test:\n%s", output)
	}
	if root.Scripts["test"] != "bun test --path-ignore-patterns='**/dist/**'" {
		t.Fatalf("generated test script = %q", root.Scripts["test"])
	}
}

func TestBuildPreservesAuthoredWorkspaceClassAndDerivesOmittedClass(t *testing.T) {
	packages := []Package{
		{Name: "@library/shared", Directory: "pkg/@library/shared"},
		{
			Name: "@entry/app", Directory: "pkg/@entry/app",
			Imports: []Import{
				{Specifier: "@library/shared", Source: "pkg/@entry/app/src/index.ts", Line: 1},
				{Specifier: "@test/assert", Source: "pkg/@entry/app/src/index.test.ts", Line: 1, Development: true},
			},
			Policy: contract.PackagePolicy{DevDependencies: map[string]string{"@library/shared": "workspace:*"}},
		},
		{Name: "@test/assert", Directory: "pkg/@test/assert"},
	}
	projection, err := Build(packages)
	if err != nil {
		t.Fatal(err)
	}
	var manifest packageJSON
	if err := json.Unmarshal(projection.Files["pkg/@entry/app/package.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if _, exists := manifest.Dependencies["@library/shared"]; exists {
		t.Fatalf("authored dev dependency moved to runtime: %#v", manifest.Dependencies)
	}
	if manifest.DevDependencies["@library/shared"] != "workspace:*" {
		t.Fatalf("authored workspace class was not preserved: %#v", manifest.DevDependencies)
	}
	if manifest.DevDependencies["@test/assert"] != "workspace:*" {
		t.Fatalf("test-only omitted class was not derived as dev: %#v", manifest.DevDependencies)
	}
}

func TestBuildTreatsAbsentWorkspaceDependencyAsClosureGap(t *testing.T) {
	_, err := Build([]Package{{
		Name: "@entry/app", Directory: "pkg/@entry/app",
		Imports: []Import{{Specifier: "@missing/shared", Source: "pkg/@entry/app/src/index.ts", Line: 1}},
		Policy:  contract.PackagePolicy{DevDependencies: map[string]string{"@missing/shared": "workspace:*"}},
	}})
	var closure *ClosureError
	if !errors.As(err, &closure) || len(closure.Diagnostics) != 1 {
		t.Fatalf("workspace closure error = %v", err)
	}
	if !strings.Contains(closure.Diagnostics[0].Remedy, `version "workspace:*" requires`) || strings.Contains(closure.Diagnostics[0].Remedy, "external dependency class") {
		t.Fatalf("workspace remedy = %q", closure.Diagnostics[0].Remedy)
	}
}

func TestBuildTreatsEveryAbsentWorkspaceProtocolAsRequiredClosure(t *testing.T) {
	tests := []struct {
		name        string
		policy      contract.PackagePolicy
		imports     []Import
		wantClass   string
		wantVersion string
	}{
		{
			name:      "observed workspace caret",
			policy:    contract.PackagePolicy{Dependencies: map[string]string{"@missing/shared": "workspace:^"}},
			imports:   []Import{{Specifier: "@missing/shared/subpath", Source: "pkg/@entry/app/src/index.ts", Line: 4}},
			wantClass: "dependencies", wantVersion: "workspace:^",
		},
		{
			name:      "unreferenced workspace path",
			policy:    contract.PackagePolicy{RequiredButNotReferenced: map[string]string{"@missing/tool": "workspace:../tool"}},
			wantClass: "requiredButNotReferenced", wantVersion: "workspace:../tool",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Build([]Package{{Name: "@entry/app", Directory: "pkg/@entry/app", Imports: test.imports, Policy: test.policy}})
			var closure *ClosureError
			if !errors.As(err, &closure) || len(closure.Diagnostics) != 1 {
				t.Fatalf("workspace closure error = %v", err)
			}
			diagnostic := closure.Diagnostics[0]
			for _, fact := range []string{test.wantClass, test.wantVersion, "add the Repository", "exact workspace:*"} {
				if !strings.Contains(diagnostic.Remedy, fact) {
					t.Fatalf("workspace remedy = %q; lacks %q", diagnostic.Remedy, fact)
				}
			}
			if strings.Contains(diagnostic.Remedy, "external dependency class") {
				t.Fatalf("workspace protocol was classified external: %q", diagnostic.Remedy)
			}
			if len(test.imports) == 0 && diagnostic.Kind != MissingWorkspaceDependency {
				t.Fatalf("unreferenced workspace diagnostic = %#v", diagnostic)
			}
		})
	}
}

func TestBuildValidatesExplicitRootExportAndLegacyExternalClass(t *testing.T) {
	t.Run("explicit exports omit root", func(t *testing.T) {
		_, err := Build([]Package{
			{Name: "@library/shared", Directory: "pkg/@library/shared", Policy: contract.PackagePolicy{Exports: map[string]string{"./Subpath": "./src/subpath.ts"}}},
			{Name: "@entry/app", Directory: "pkg/@entry/app", Imports: []Import{{Specifier: "@library/shared", Source: "pkg/@entry/app/src/index.ts", Line: 1}}},
		})
		var closure *ClosureError
		if !errors.As(err, &closure) || closure.Diagnostics[0].Kind != MissingExport {
			t.Fatalf("root export gap = %v", err)
		}
		if !strings.Contains(closure.Diagnostics[0].Remedy, `exports["."]`) {
			t.Fatalf("root export remedy = %q", closure.Diagnostics[0].Remedy)
		}
	})

	t.Run("required but not referenced remains external", func(t *testing.T) {
		_, err := Build([]Package{{
			Name: "@entry/app", Directory: "pkg/@entry/app",
			Imports: []Import{{Specifier: "legacy-tool", Source: "pkg/@entry/app/src/index.ts", Line: 1}},
			Policy:  contract.PackagePolicy{RequiredButNotReferenced: map[string]string{"legacy-tool": "1.0.0"}},
		}})
		if err != nil {
			t.Fatalf("legacy external class was not honored: %v", err)
		}
	})
}

func TestBuildTreatsSelfPackageImportsAsExportChecksNotDependencies(t *testing.T) {
	projection, err := Build([]Package{{
		Name:      "@service/app",
		Directory: "pkg/@service/app",
		Imports: []Import{
			{Specifier: "@service/app", Source: "pkg/@service/app/src/index.ts", Line: 1},
			{Specifier: "@service/app/testing", Source: "pkg/@service/app/src/test.ts", Line: 1},
		},
		Policy: contract.PackagePolicy{Exports: map[string]string{
			".":         "./src/index.ts",
			"./testing": "./src/test.ts",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var manifest packageJSON
	if err := json.Unmarshal(projection.Files["pkg/@service/app/package.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Dependencies) != 0 || len(manifest.DevDependencies) != 0 {
		t.Fatalf("self imports became dependencies: runtime=%v dev=%v", manifest.Dependencies, manifest.DevDependencies)
	}
}

func TestBuildDesignatesPackageRootAtCompositeOutput(t *testing.T) {
	projection, err := Build([]Package{{Name: "@workbench-library/shared", Directory: "pkg/@workbench-library"}})
	if err != nil {
		t.Fatal(err)
	}

	var manifest struct {
		Exports map[string]struct {
			Types   string `json:"types"`
			Default string `json:"default"`
		} `json:"exports"`
	}
	if err := json.Unmarshal(projection.Files["pkg/@workbench-library/package.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		Types   string `json:"types"`
		Default string `json:"default"`
	}{
		".": {Types: "./dist/index.d.ts", Default: "./dist/index.js"},
	}
	if !reflect.DeepEqual(manifest.Exports, want) {
		t.Fatalf("exports = %#v, want %#v", manifest.Exports, want)
	}
}

func TestBuildKeepsPackageScopeChildrenStableAsCardinalityGrows(t *testing.T) {
	singleton, err := Build([]Package{{Name: "@workbench-entry/app", Directory: "pkg/@workbench-entry/app"}})
	if err != nil {
		t.Fatal(err)
	}
	multiple, err := Build([]Package{
		{Name: "@workbench-entry/app", Directory: "pkg/@workbench-entry/app"},
		{Name: "@workbench-entry/tool", Directory: "pkg/@workbench-entry/tool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"pkg/@workbench-entry/app/package.json",
		"pkg/@workbench-entry/app/tsconfig.json",
	} {
		if !reflect.DeepEqual(singleton.Files[path], multiple.Files[path]) {
			t.Fatalf("adding a same-scope package moved or changed first package projection %q", path)
		}
	}
	for _, path := range []string{
		"pkg/@workbench-entry/tool/package.json",
		"pkg/@workbench-entry/tool/tsconfig.json",
	} {
		if _, exists := multiple.Files[path]; !exists {
			t.Fatalf("multi-package projection lacks %q", path)
		}
	}
	if _, exists := multiple.Files["pkg/@workbench-entry/package.json"]; exists {
		t.Fatal("PackageScope checkout root was projected as a package")
	}
}

func TestApplyConvergesAndReplacesWholeOwnedOutputs(t *testing.T) {
	projection, err := Build([]Package{{Name: "@basindb/core", Directory: "pkg/@basindb/core"}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manifest := filepath.Join(root, "pkg/@basindb/core/package.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"handOwned":"must not survive"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := Apply(root, projection)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 4 {
		t.Fatalf("first changed paths = %#v", first)
	}
	second, err := Apply(root, projection)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second apply changed %#v", second)
	}
	contents, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(contents, &value); err != nil {
		t.Fatal(err)
	}
	if _, exists := value["handOwned"]; exists {
		t.Fatal("whole-output ownership preserved an unknown field")
	}
}

func TestApplyRefusesPathsOutsideOwnedProjection(t *testing.T) {
	_, err := Apply(t.TempDir(), Projection{Files: map[string][]byte{"../source.ts": []byte("lost")}})
	if err == nil {
		t.Fatal("escaping source path was accepted")
	}
}
