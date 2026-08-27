package acceptance_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type v010CompatibilityManifest struct {
	Release       string            `json:"release"`
	SubjectBranch string            `json:"subjectBranch"`
	Repositories  map[string]string `json:"repositories"`
}

func TestWorkbenchV010LegacySkillSourceFailsWithRecreationGuidance(t *testing.T) {
	encoded, err := os.ReadFile(filepath.Join("testdata", "v010", "compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest v010CompatibilityManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Release != "0.1.0" || manifest.SubjectBranch != "workbench/proof-0.1.0" {
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
	environment := publicEnvironment(t, moduleRoot, testRoot, anonymousHome)
	binary := buildPublicCLI(t, moduleRoot)
	beforeRefs := make(map[string]string, len(manifest.Repositories))
	for identity, revision := range manifest.Repositories {
		if revision == "" {
			t.Fatalf("compatibility manifest omits revision for %q", identity)
		}
		remote := "https://github.com/" + identity
		beforeRefs[identity] = remoteBranchRevision(t, environment, remote, manifest.SubjectBranch)
		if beforeRefs[identity] != revision {
			t.Fatalf("0.1 proof ref for %q = %q, want immutable manifest revision %q", identity, beforeRefs[identity], revision)
		}
	}
	contractURI := releasePackageURI + "#/WorkbenchSubject.pkl"
	workbench := newPublicWorkbenchForContractAndBranch(t, testRoot, "v010-compatibility", contractURI, manifest.SubjectBranch, environment)
	command := exec.Command(binary, "setup")
	command.Dir = workbench
	command.Env = environment
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("Workbench 0.5 consumed the retired 0.1 skill source:\n%s", output)
	}
	for _, fact := range []string{
		"phosphorco/workbench-fixture-library:.agents/skills/workbench-fixture-engineering/SKILL.md",
		"recreate this Git-owned skill under skills/workbench-fixture-engineering/SKILL.md",
	} {
		if !strings.Contains(string(output), fact) {
			t.Fatalf("0.1 recreation refusal omits %q:\n%s", fact, output)
		}
	}
	if status := publicGit(t, environment, workbench, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
		t.Fatalf("0.1 recreation refusal changed the outer context: %q", status)
	}
	for _, target := range []string{"pkg", "repos", ".workbench", "package.json", "tsconfig.json", "bun.lock", "node_modules"} {
		if _, statErr := os.Lstat(filepath.Join(workbench, target)); !os.IsNotExist(statErr) {
			t.Fatalf("0.1 recreation refusal created %q: %v", target, statErr)
		}
	}
	for identity, before := range beforeRefs {
		remote := "https://github.com/" + identity
		if after := remoteBranchRevision(t, environment, remote, manifest.SubjectBranch); after != before {
			t.Fatalf("0.1 refusal changed %q proof ref from %q to %q", identity, before, after)
		}
	}
}
