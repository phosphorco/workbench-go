package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
)

func TestBuildDerivesCrossRepositoryWorkspaceAndTypeScriptReferences(t *testing.T) {
	projection, err := Build([]Package{
		{Name: "@phosphorco/community", Directory: "pkg/@phosphorco/community", Imports: []string{}},
		{
			Name:      "@basindb/core",
			Directory: "pkg/@basindb/core",
			Imports:   []string{"@phosphorco/community/tracing", "node:fs"},
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
