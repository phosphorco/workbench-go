package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/change"
	"github.com/phosphorco/workbench-go/internal/contract"
)

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
	forward := renderSnapshot(contract.WorkbenchWorldSnapshot{Resources: map[string]contract.SnapshotResource{"@a": resourceA, "phosphorco/z": resourceZ}})
	reverse := renderSnapshot(contract.WorkbenchWorldSnapshot{Resources: map[string]contract.SnapshotResource{"phosphorco/z": resourceZ, "@a": resourceA}})
	if string(forward) != string(reverse) {
		t.Fatalf("snapshot rendering depends on map insertion order:\n%s\n---\n%s", forward, reverse)
	}
	if strings.Index(string(forward), `["@a"]`) > strings.Index(string(forward), `["phosphorco/z"]`) {
		t.Fatalf("snapshot identities are not sorted:\n%s", forward)
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
	path, err := workbenchPath(root, ".workbench/world.pkl")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".workbench", "world.pkl"); path != want {
		t.Fatalf("workbenchPath = %q, want %q", path, want)
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
