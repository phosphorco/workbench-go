package acceptance_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type v010CompatibilityManifest struct {
	Release       string            `json:"release"`
	SubjectBranch string            `json:"subjectBranch"`
	Repositories  map[string]string `json:"repositories"`
}

func TestWorkbenchV010PublicPathRemainsReproducible(t *testing.T) {
	encoded, err := os.ReadFile(filepath.Join("testdata", "v010", "compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest v010CompatibilityManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Release != "0.1.0" || manifest.SubjectBranch != proofBranch {
		t.Fatalf("0.1 compatibility manifest = %#v", manifest)
	}

	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	testRoot := t.TempDir()
	anonymousHome := filepath.Join(testRoot, "anonymous-home")
	for _, path := range []string{anonymousHome, filepath.Join(testRoot, "bun-cache"), filepath.Join(testRoot, "runtime"), filepath.Join(testRoot, "tmp")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := publicEnvironment(testRoot, anonymousHome)
	binary := buildPublicCLI(t, moduleRoot)
	workbench := newPublicWorkbenchForBranch(t, testRoot, "v010-compatibility", manifest.SubjectBranch, environment)
	runSetup(t, binary, workbench, environment)

	checkouts := map[string]string{
		"phosphorco/workbench-fixture-entry":   filepath.Join(workbench, "pkg", "@workbench-entry"),
		"phosphorco/workbench-fixture-library": filepath.Join(workbench, "pkg", "@workbench-library"),
	}
	for identity, checkout := range checkouts {
		want := manifest.Repositories[identity]
		if want == "" {
			t.Fatalf("compatibility manifest omits %q", identity)
		}
		if got := publicGit(t, environment, checkout, "rev-parse", "HEAD"); got != want {
			t.Fatalf("0.1 checkout %q revision = %q, want preserved %q", identity, got, want)
		}
		if got := publicGit(t, environment, checkout, "branch", "--show-current"); got != manifest.SubjectBranch {
			t.Fatalf("0.1 checkout %q branch = %q, want %q", identity, got, manifest.SubjectBranch)
		}
	}
}
