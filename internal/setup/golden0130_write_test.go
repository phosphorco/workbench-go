package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phosphorco/workbench-go/internal/workspace"
)

// TestWriteGolden0130 produced testdata/golden-0.13.0. It was run once in a
// worktree at tag 0.13.0 (with golden0130_fixtures_test.go copied there):
//
//	git worktree add /tmp/wb-0.13.0 0.13.0
//	cp internal/setup/golden0130_*_test.go /tmp/wb-0.13.0/internal/setup/
//	(cd /tmp/wb-0.13.0 && WORKBENCH_GOLDEN_OUTPUT=$PWD/../golden go test ./internal/setup -run TestWriteGolden0130)
//
// Running it on later code would regenerate from that code, so it is only a
// record of the procedure; the comparison is TestGolden0130ProjectionIsByteIdentical.
func TestWriteGolden0130(t *testing.T) {
	output := os.Getenv("WORKBENCH_GOLDEN_OUTPUT")
	if output == "" {
		t.Skip("WORKBENCH_GOLDEN_OUTPUT unset")
	}
	bun := requireBun(t)
	for _, fixture := range golden0130Fixtures() {
		root := t.TempDir()
		for name, contents := range fixture.files {
			write(t, filepath.Join(root, filepath.FromSlash(name)), contents)
		}
		roots := map[string]string{}
		for _, resource := range fixture.resources {
			roots[resource.Identity] = root
		}
		packages, err := observePackagesAt(context.Background(), fixture.resources, "0.13.0", roots, bun)
		if err != nil {
			t.Fatalf("%s: %v", fixture.name, err)
		}
		projection, err := workspace.BuildWithOptions(packages, workspace.BuildOptions{RuntimeBuiltins: workspace.RuntimeBuiltinModulesV0130(), ReassembleRootDependencies: true, ProductionTypeScript: true})
		if err != nil {
			t.Fatalf("%s: %v", fixture.name, err)
		}
		for name, contents := range projection.Files {
			target := filepath.Join(output, fixture.name, name)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, contents, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
