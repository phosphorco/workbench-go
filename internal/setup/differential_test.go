package setup

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/phosphorco/workbench-go/internal/tsfiles"
	"github.com/phosphorco/workbench-go/internal/workspace"
)

// TestDifferentialObservationDump writes the file set Workbench observes for
// every role, standalone project and base of an in-place repository, for the
// acceptance gate's comparison with TypeScript's own parseJsonConfigFileContent.
// It runs only when WORKBENCH_DIFFERENTIAL_ROOT names a checkout.
func TestDifferentialObservationDump(t *testing.T) {
	root, output := os.Getenv("WORKBENCH_DIFFERENTIAL_ROOT"), os.Getenv("WORKBENCH_DIFFERENTIAL_OUT")
	if root == "" || output == "" {
		t.Skip("WORKBENCH_DIFFERENTIAL_ROOT and WORKBENCH_DIFFERENTIAL_OUT unset")
	}
	ctx := context.Background()
	toolchain := NewToolchain(requireEvaluator(t), requireBun(t))
	declaration, err := loadInPlaceDeclaration(ctx, root, "phosphorco/monorepo", toolchain)
	if err != nil {
		t.Fatal(err)
	}
	files, err := repositoryFiles(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	tree := newLayoutTree(root, files)
	input, _, err := layoutInput(ctx, toolchain.Bun, tree, root, declaration)
	if err != nil {
		t.Fatal(err)
	}
	observed := map[string][]string{}
	for _, pkg := range input.Packages {
		for _, role := range pkg.Roles {
			observed[role.Config] = sourcesOnly(role.Files)
		}
	}
	for _, project := range input.Projects {
		observed[project.Config] = sourcesOnly(project.Files)
	}
	for _, base := range input.Bases {
		directory := cleanRelative(base.Directory)
		spec := tsfiles.Spec{Directory: directory}
		if base.Include != nil {
			spec.Include = resolvePatterns(*base.Include, base.Directory, base.Directory)
		}
		if base.Exclude != nil {
			spec.Exclude = resolvePatterns(*base.Exclude, base.Directory, base.Directory)
		}
		selected, err := tsfiles.Select(spec, tree.files)
		if err != nil {
			t.Fatal(err)
		}
		observed[workspace.BaseFile(base.Directory)] = sourcesOnly(selected)
	}
	encoded, err := json.MarshalIndent(observed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sourcesOnly(files []string) []string {
	result := []string{}
	for _, file := range files {
		if tsfiles.HasSourceExtension(file) {
			result = append(result, file)
		}
	}
	sort.Strings(result)
	return result
}
