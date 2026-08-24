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
