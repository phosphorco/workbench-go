package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/buildable"
	"github.com/phosphorco/workbench-go/internal/change"
	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/evaluate"
	"github.com/phosphorco/workbench-go/internal/legacy/v020v030snapshot"
)

func TestColdBuildableLifecycleEvaluatesLocalDeclarationWithoutProjection(t *testing.T) {
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	pkl, err = filepath.Abs(pkl)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := `amends "workbench-contract:/0.6.0/Repository.pkl"
buildables {
  ["hello"] = new Buildable {
    inputDetection = new GitHeadTreeInputDetection { paths { "producer.txt" } }
    buildCommand = new BuildCommand { executable = "true" }
    manifest = new ManifestContract { schemaVersion = 1; kind = "hello-manifest"; contractId = "hello-v1" }
    candidates {
      new BuildableCandidate { root = ".local-build/hello"; inputStrategy = "gitWorktree"; invalidRemedy = "Rebuild it." }
      new BuildableCandidate { root = ".ci-build/hello"; inputStrategy = "gitHeadTree"; invalidRemedy = "Restore it." }
    }
    platforms {
      ["linux-x86_64"] = new BuildablePlatformOutput { os { "linux" }; arch { "amd64" }; path = "bin/hello" }
    }
  }
}
`
	writeHandlerFile(t, filepath.Join(root, "workbench.pkl"), []byte(source), 0o644)
	writeHandlerFile(t, filepath.Join(root, ".local-build/hello/bin/hello"), []byte("#!/bin/sh\n"), 0o755)
	descriptor, err := json.Marshal(map[string]any{"source": map[string]string{}, "capabilities": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	writeHandlerFile(t, filepath.Join(root, ".local-build/hello", buildable.SourceDescriptorFilename), descriptor, 0o644)
	application := applicationsForEnvironment(func() (commandEnvironment, error) {
		return commandEnvironment{evaluator: evaluator}, nil
	})
	if err := application.buildBuildable(context.Background(), root, "hello", "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, buildable.ProjectionPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cold lifecycle acquired a projection: %v", err)
	}
}

func writeHandlerFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedProjectionPolicyRefusesWorkbenchOwnedPathsBeforeRefAdvance(t *testing.T) {
	t.Parallel()
	generated := []string{
		"package.json",
		"packages/app/tsconfig.json",
		"AGENTS.md",
		"docs/AGENTS.md",
		"bun.lock",
		"packages/app/bun.lock",
		".agents/skills",
		".agents/skills/editing/SKILL.md",
		".workbench/buildables.json",
		"node_modules",
		"node_modules/@workbench/library/package.json",
	}
	for _, selectedPath := range generated {
		selectedPath := selectedPath
		t.Run(selectedPath, func(t *testing.T) {
			repository := newCommitPolicyRepository(t, selectedPath)
			before := gitOutput(t, repository, "rev-parse", "HEAD")
			request := change.Request{
				ResourceID: "@fixture", Repository: repository, Branch: "main", Remote: "origin",
				ChangeID: "generated-policy", Title: "Generated policy witness", Paths: []string{selectedPath},
				GeneratedPathPolicyID: generatedPolicyID, RejectPath: generatedProjectionPath,
			}
			if _, err := change.PrepareAll(context.Background(), []change.Request{request}); err == nil || !strings.Contains(err.Error(), "generated or Workbench-owned") {
				t.Fatalf("PrepareAll(%q) error = %v", selectedPath, err)
			}
			if after := gitOutput(t, repository, "rev-parse", "HEAD"); after != before {
				t.Fatalf("generated path advanced HEAD from %s to %s", before, after)
			}
		})
	}

	for _, sourcePath := range []string{
		"docs/package.json.md",
		"config/tsconfig.json.example",
		"AGENTS.md.template",
		"bun.lock.notes",
		".agents/skills-old/SKILL.md",
		"node_modules-source/index.ts",
	} {
		if generatedProjectionPath(sourcePath) {
			t.Errorf("source path %q overmatched generated policy", sourcePath)
		}
	}
}

func TestCommitPlanContractsMatchTheExactSubjectRelease(t *testing.T) {
	t.Parallel()
	for _, contractVersion := range []string{"0.2.0", "0.3.0", "0.4.0", "0.5.0", currentContractVersion} {
		contractVersion := contractVersion
		t.Run(contractVersion, func(t *testing.T) {
			t.Parallel()
			uri := releasedContractURI(contractVersion, "WorkbenchCommitPlan.pkl")
			source := []byte("amends \"" + uri + "\"\n")
			if _, err := releasedCommitPlanContract(source, contractVersion); err != nil {
				t.Fatal(err)
			}
		})
	}

	v020 := []byte("amends \"" + releasedContractURI("0.2.0", "WorkbenchCommitPlan.pkl") + "\"\n")
	if _, err := releasedCommitPlanContract(v020, "0.3.0"); err == nil || !strings.Contains(err.Error(), "want exact Workbench 0.3.0") {
		t.Fatalf("cross-release commit plan error = %v", err)
	}
	if _, err := releasedCommitPlanContract(v020, "0.1.0"); err == nil || !strings.Contains(err.Error(), "has no released WorkbenchCommitPlan.pkl") {
		t.Fatalf("0.1 lifecycle error = %v", err)
	}
}

func TestSnapshotContractSelectionDistinguishesCurrentAndExactLegacyIdentities(t *testing.T) {
	t.Parallel()
	currentURI := releasedContractURI(currentContractVersion, "WorkbenchSnapshot.pkl")
	if _, kind, err := releasedSnapshotContractFromSource([]byte("amends \"" + currentURI + "\"\n")); err != nil {
		t.Fatalf("current snapshot contract: %v", err)
	} else if kind != currentSnapshotContract {
		t.Fatalf("current snapshot kind = %v", kind)
	}
	for _, contractVersion := range []string{"0.2.0", "0.3.0"} {
		uri, err := v020v030snapshot.ContractURI(contractVersion)
		if err != nil {
			t.Fatal(err)
		}
		if _, kind, err := releasedSnapshotContractFromSource([]byte("amends \"" + uri + "\"\n")); err != nil {
			t.Fatalf("%s snapshot contract: %v", contractVersion, err)
		} else if kind != legacyV020V030SnapshotContract {
			t.Fatalf("%s snapshot kind = %v", contractVersion, kind)
		}
	}
	for _, version := range []string{"0.4.0", "0.5.0"} {
		previousURI := releasedContractURI(version, "WorkbenchSnapshot.pkl")
		if _, kind, err := releasedSnapshotContractFromSource([]byte("amends \"" + previousURI + "\"\n")); err != nil {
			t.Fatalf("%s snapshot contract: %v", version, err)
		} else if kind != currentSnapshotContract {
			t.Fatalf("%s snapshot kind = %v", version, kind)
		}
	}
	for _, invalid := range []struct {
		uri  string
		want string
	}{
		{uri: releasedContractURI("0.1.0", v020v030snapshot.Filename), want: "0.1.0 has no released snapshot contract"},
		{uri: releasedContractURI(currentContractVersion, v020v030snapshot.Filename), want: "unsupported Workbench Snapshot contract"},
		{uri: releasedContractURI("0.3.0", "WorkbenchSnapshot.pkl"), want: "unsupported Workbench Snapshot contract"},
	} {
		if _, _, err := releasedSnapshotContractFromSource([]byte("amends \"" + invalid.uri + "\"\n")); err == nil || !strings.Contains(err.Error(), invalid.want) {
			t.Fatalf("snapshot source %q error = %v", invalid.uri, err)
		}
	}
}

func TestReleasedSubjectContractRetainsAllSupportedSubjectLines(t *testing.T) {
	t.Parallel()
	for _, contractVersion := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0", "0.5.0", currentContractVersion} {
		uri := releasedContractURI(contractVersion, "WorkbenchSubject.pkl")
		if _, err := releasedContractForSubjectLine([]byte("amends \""+uri+"\"\n"), "WorkbenchSubject.pkl", contractVersion, "0.1.0", "0.2.0", "0.3.0", "0.4.0", "0.5.0", currentContractVersion); err != nil {
			t.Fatalf("%s Subject contract: %v", contractVersion, err)
		}
	}
}

func TestRenderSnapshotIsDeterministicByIdentity(t *testing.T) {
	t.Parallel()
	resourceA := contract.SnapshotResource{
		Shape:  contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@a"},
		GitHub: "phosphorco/a", CanonicalPath: "pkg/@a", Commit: strings.Repeat("a", 40),
	}
	resourceZ := contract.SnapshotResource{
		Shape:  contract.ResourceShape{Kind: contract.RepositoryShape},
		GitHub: "phosphorco/z", CanonicalPath: "repos/z", Commit: strings.Repeat("f", 40),
	}
	contractURI := releasedContractURI(currentContractVersion, "WorkbenchSnapshot.pkl")
	forward := renderSnapshot(contract.WorkbenchSnapshot{Resources: map[string]contract.SnapshotResource{"@a": resourceA, "phosphorco/z": resourceZ}}, contractURI)
	reverse := renderSnapshot(contract.WorkbenchSnapshot{Resources: map[string]contract.SnapshotResource{"phosphorco/z": resourceZ, "@a": resourceA}}, contractURI)
	if string(forward) != string(reverse) {
		t.Fatalf("snapshot rendering depends on map insertion order:\n%s\n---\n%s", forward, reverse)
	}
	if strings.Index(string(forward), `["@a"]`) > strings.Index(string(forward), `["phosphorco/z"]`) {
		t.Fatalf("snapshot identities are not sorted:\n%s", forward)
	}
	if !strings.HasPrefix(string(forward), "amends \""+contractURI+"\"") {
		t.Fatalf("snapshot did not retain selected contract line:\n%s", forward)
	}
}

func TestSnapshotReproductionReportUsesRepositoryGrammar(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		count int
		want  string
	}{
		{count: 1, want: "Reproduced and verified 1 exact repository"},
		{count: 2, want: "Reproduced and verified 2 exact repositories"},
	} {
		if got := snapshotReproductionReport(test.count); got != test.want {
			t.Errorf("snapshotReproductionReport(%d) = %q, want %q", test.count, got, test.want)
		}
	}
}

func TestWorkbenchPathRefusesTraversalAndAbsoluteAuthority(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, invalid := range []string{"", ".", "../outside", "nested/../../outside", filepath.Join(root, "absolute.pkl")} {
		if path, err := workbenchPath(root, invalid); err == nil {
			t.Errorf("workbenchPath(%q) = %q, want refusal", invalid, path)
		}
	}
	path, err := workbenchPath(root, ".workbench/workbench-snapshot.pkl")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".workbench", "workbench-snapshot.pkl"); path != want {
		t.Fatalf("workbenchPath = %q, want %q", path, want)
	}
}

func TestUnsupportedHistoricalSnapshotIsRefusedWithoutChangingTheUserFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	relative := "preserved-snapshot.pkl"
	path := filepath.Join(root, relative)
	source := []byte("amends \"" + releasedContractURI("0.1.0", v020v030snapshot.Filename) + "\"\n")
	if err := os.WriteFile(path, source, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reproduceSnapshot(context.Background(), root, relative, evaluate.Evaluator{}); err == nil || !strings.Contains(err.Error(), "0.1.0 has no released snapshot contract") {
		t.Fatalf("reproduce error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(source) {
		t.Fatalf("user-authored snapshot changed: got %q want %q", after, source)
	}
}

func newCommitPolicyRepository(t *testing.T, selectedPath string) string {
	t.Helper()
	repository := t.TempDir()
	gitOutput(t, repository, "init", "-b", "main")
	gitOutput(t, repository, "config", "user.name", "Workbench Test")
	gitOutput(t, repository, "config", "user.email", "workbench@example.invalid")
	writeTestFile(t, repository, "README.md", "fixture\n")
	gitOutput(t, repository, "add", "README.md")
	gitOutput(t, repository, "commit", "-m", "Initial fixture")
	gitOutput(t, repository, "remote", "add", "origin", "https://github.com/phosphorco/workbench-fixture-entry.git")
	writeTestFile(t, repository, selectedPath, "generated\n")
	return repository
}

func writeTestFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
