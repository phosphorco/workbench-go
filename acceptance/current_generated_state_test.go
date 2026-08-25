package acceptance

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreshCurrentSetupEmitsOnlyCurrentVocabularyAndPaths(t *testing.T) {
	fixture := newNarrativeFixture(t, true)
	workbench := fixture.newWorkbench(t, "current-generated-state", currentSubjectContract, currentAgentContract, "entry")
	output := fixture.runSetup(t, workbench, true)
	retiredVocabulary := "wo" + "rld"
	if strings.Contains(strings.ToLower(output), retiredVocabulary) {
		t.Fatalf("fresh setup output contains retired vocabulary:\n%s", output)
	}

	for _, absent := range []string{
		filepath.Join(workbench, ".workbench", retiredVocabulary+".json"),
		filepath.Join(workbench, ".workbench", retiredVocabulary+"-snapshot.pkl"),
	} {
		if _, err := os.Lstat(absent); !os.IsNotExist(err) {
			t.Fatalf("fresh setup emitted retired path %q: %v", absent, err)
		}
	}

	for _, root := range []string{
		filepath.Join(workbench, ".workbench"),
		filepath.Join(workbench, ".agents", "skills"),
	} {
		assertTreeUsesCurrentVocabulary(t, workbench, root, retiredVocabulary)
	}
	for _, path := range []string{
		filepath.Join(workbench, "AGENTS.md"),
		filepath.Join(workbench, "package.json"),
		filepath.Join(workbench, "tsconfig.json"),
	} {
		assertCurrentVocabularyFile(t, workbench, path, retiredVocabulary)
	}
}

func assertTreeUsesCurrentVocabulary(t *testing.T, workbench, root, retiredVocabulary string) {
	t.Helper()
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return
	} else if err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.Contains(strings.ToLower(filepath.ToSlash(path)), retiredVocabulary) {
			t.Errorf("fresh setup emitted retired path %q", path)
		}
		if entry.Type().IsRegular() {
			assertCurrentVocabularyFile(t, workbench, path, retiredVocabulary)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect generated tree %q: %v", root, err)
	}
}

func assertCurrentVocabularyFile(t *testing.T, workbench, path, retiredVocabulary string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(contents)), retiredVocabulary) {
		relative, relativeErr := filepath.Rel(workbench, path)
		if relativeErr != nil {
			relative = path
		}
		t.Errorf("fresh setup emitted retired vocabulary in %q", filepath.ToSlash(relative))
	}
}
