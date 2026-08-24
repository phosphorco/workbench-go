package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/orientation"
)

// This is local-candidate evidence for the deterministic projection seam. The
// released contract and installed binary remain the final public proof boundary.
func TestAgentOrientationV1ConvergesUpdatesAndPreservesTrackedInput(t *testing.T) {
	root := t.TempDir()
	tracked := filepath.Join(root, "AGENTS.pkl")
	generated := filepath.Join(root, "AGENTS.md")
	writeFile(t, tracked, "tracked orientation prose\n")
	trackedBefore := readFile(t, tracked)

	instructions := contract.AgentInstructions{
		Prose: "Fixture orientation.",
		Subject: contract.AgentSubject{
			WorkLine:    contract.WorkLine{Branch: "workbench/v1", BaseBranch: "main"},
			Entrypoints: []string{"https://github.com/phosphorco/workbench-fixture-entry"},
		},
		Resources: []contract.AgentResource{
			{Identity: "phosphorco/workbench-fixture-library", GitHub: "phosphorco/workbench-fixture-library", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, CanonicalPath: "repos/workbench-fixture-library", Branch: "workbench/v1", Health: "healthy"},
			{Identity: "@workbench-entry", GitHub: "phosphorco/workbench-fixture-entry", Shape: contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"}, CanonicalPath: "pkg/@workbench-entry", Branch: "workbench/v1", Health: "healthy"},
		},
		GeneratedPaths: []string{"AGENTS.md", "package.json", "tsconfig.json"},
		HandOwnedPaths: []string{"AGENTS.pkl", "workbench-subject.pkl"},
	}

	first, err := orientation.Render(instructions)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, first, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := orientation.Render(instructions)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, second) {
		t.Fatal("identical explicit inputs did not byte-converge")
	}
	for _, fact := range []string{"workbench/v1", "main", "@workbench-entry", "pkg/@workbench-entry", "phosphorco/workbench-fixture-library", "repos/workbench-fixture-library", "workbench setup", "workbench commit", "workbench prune"} {
		if !strings.Contains(string(first), fact) {
			t.Errorf("orientation lacks explicit fact %q", fact)
		}
	}

	instructions.Resources = instructions.Resources[:1]
	updated, err := orientation.Render(instructions)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(first, updated) || strings.Contains(string(updated), "@workbench-entry") {
		t.Fatal("removed World member remained in regenerated orientation")
	}
	if got := readFile(t, tracked); got != trackedBefore {
		t.Fatalf("Git-owned AGENTS.pkl changed: before %q after %q", trackedBefore, got)
	}
}
