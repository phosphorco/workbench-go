package setup

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// TestGolden0130ProjectionIsByteIdentical projects every 0.13 fixture with the
// current code and requires the exact Projection.Files the released 0.13.0
// code produced (testdata/golden-0.13.0, written at tag 0.13.0 by
// TestWriteGolden0130). A context Workbench projects a 0.14.0 declaration with
// the same projection, so the 0.14.0 twin must produce the same bytes.
func TestGolden0130ProjectionIsByteIdentical(t *testing.T) {
	bun := requireBunUnlessCI(t)
	for _, version := range []string{"0.13.0", "0.14.0"} {
		for _, fixture := range golden0130Fixtures() {
			t.Run(version+"/"+fixture.name, func(t *testing.T) {
				assertGolden0130(t, bun, version, fixture)
			})
		}
	}
}

// requireBunUnlessCI skips without Bun locally but fails in CI, where the
// compatibility proof must run.
func requireBunUnlessCI(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil && os.Getenv("CI") != "" {
		t.Fatal("bun is required in CI for the 0.13 compatibility proof")
	}
	return requireBun(t)
}

func assertGolden0130(t *testing.T, bun, version string, fixture golden0130Fixture) {
	t.Helper()
	{
		{
			root := t.TempDir()
			for name, contents := range fixture.files {
				write(t, filepath.Join(root, filepath.FromSlash(name)), contents)
			}
			roots := map[string]string{}
			for _, resource := range fixture.resources {
				roots[resource.Identity] = root
			}
			projection, err := projectWorkspace(context.Background(), fixture.resources, version, roots, bun)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "golden-0.13.0", fixture.name)
			want := map[string][]byte{}
			if err := filepath.WalkDir(golden, func(path string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				contents, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				relative, err := filepath.Rel(golden, path)
				want[relative] = contents
				return err
			}); err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(want))
			for name := range want {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(projection.Files) != len(want) {
				t.Fatalf("projected %d files, golden has %d", len(projection.Files), len(want))
			}
			for _, name := range names {
				if got, exists := projection.Files[name]; !exists || !bytes.Equal(got, want[name]) {
					t.Errorf("%s differs from the 0.13.0 projection:\n%s\nwant:\n%s", name, got, want[name])
				}
			}
		}
	}
}
