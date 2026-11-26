package acceptance

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRepositoryLayoutRunMatchesThe0130Projection runs the whole released
// path (Pkl evaluation, in-repository scope merge, observation and setup's
// writes) for a 0.13.0 Repository and requires every generated workspace
// manifest to equal the bytes the released 0.13.0 binary wrote
// (testdata/golden-0.13.0-run). Those bytes were written by running this test
// in a worktree at tag 0.13.0 with WORKBENCH_GOLDEN_OUTPUT set.
func TestRepositoryLayoutRunMatchesThe0130Projection(t *testing.T) {
	fixture := newPackageScopeFixture(t)
	declaration, files := layoutRepository()
	createRemote(t, fixture.root, fixture.remotes, "mono", declaration, files)
	workbench := fixture.newWorkbench(t, "layout-golden", layoutSubjectContract, "", "mono")
	fixture.runSetup(t, workbench, true)
	got := map[string][]byte{}
	if err := filepath.WalkDir(workbench, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == "node_modules" || entry.Name() == ".git" || entry.Name() == "vendor") {
			return filepath.SkipDir
		}
		if entry.IsDir() || (entry.Name() != "package.json" && entry.Name() != "tsconfig.json") {
			return nil
		}
		relative, err := filepath.Rel(workbench, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		got[filepath.ToSlash(relative)] = contents
		return err
	}); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden-0.13.0-run")
	if output := os.Getenv("WORKBENCH_GOLDEN_OUTPUT"); output != "" {
		for name, contents := range got {
			target := filepath.Join(output, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, contents, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	want := map[string][]byte{}
	if err := filepath.WalkDir(golden, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(golden, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		want[filepath.ToSlash(relative)] = contents
		return err
	}); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(got) != len(want) {
		t.Fatalf("setup wrote %d manifests (%s), the 0.13.0 binary wrote %d", len(got), strings.Join(names, ", "), len(want))
	}
	for _, name := range names {
		if !bytes.Equal(got[name], want[name]) {
			t.Errorf("%s differs from the 0.13.0 binary's bytes:\n%s\nwant:\n%s", name, got[name], want[name])
		}
	}
}
