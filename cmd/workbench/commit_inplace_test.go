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
	"github.com/phosphorco/workbench-go/internal/evaluate"
	"github.com/phosphorco/workbench-go/internal/setup"
)

const commitInPlaceDeclaration = `amends "workbench-contract:/0.14.0/Repository.pkl"

root = new RootManifest {
  packageJson {
    fields {
      ["name"] = "mono"
      ["private"] = true
      ["devDependencies"] = new Mapping { ["typescript"] = "5.9.3" }
    }
  }
}
packages {
  ["@a/core"] {
    directory = "pkg/@a/core"
    exports { ["."] = "./src/index.ts" }
    roles { ["sources"] { include = new Listing { "./src" } } }
  }
}
`

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeInPlaceFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// inPlaceCommitFixture is an in-place checkout on branch work whose origin
// names phosphorco/mono; insteadOf routes it to a local bare remote.
func inPlaceCommitFixture(t *testing.T) (string, string, commandEnvironment) {
	t.Helper()
	pkl, pklErr := exec.LookPath("pkl")
	bun, bunErr := exec.LookPath("bun")
	if pklErr != nil || bunErr != nil {
		t.Skip("pkl and bun are required")
	}
	pkl, _ = filepath.Abs(pkl)
	bun, _ = filepath.Abs(bun)
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	root := filepath.Join(base, "work")
	runGit(t, base, "init", "-q", "--bare", "-b", "main", remote)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+remote+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "git@github.com:phosphorco/mono.git")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	runGit(t, base, "init", "-q", "-b", "main", root)
	runGit(t, root, "remote", "add", "origin", "git@github.com:phosphorco/mono.git")
	writeInPlaceFile(t, filepath.Join(root, "workbench-subject.pkl"), "amends \"workbench-contract:/0.14.0/InPlaceSubject.pkl\"\n\nentrypoint = \"https://github.com/phosphorco/mono\"\n")
	writeInPlaceFile(t, filepath.Join(root, "workbench.pkl"), commitInPlaceDeclaration)
	writeInPlaceFile(t, filepath.Join(root, ".gitignore"), ".workbench/\n")
	writeInPlaceFile(t, filepath.Join(root, "pkg/@a/core/src/index.ts"), "export const core = 1\n")
	toolchain := setup.NewToolchain(evaluator, bun)
	if _, err := setup.RunWith(context.Background(), root, toolchain); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "init")
	runGit(t, root, "push", "-q", "-u", "origin", "main")
	runGit(t, root, "remote", "set-head", "origin", "main")
	runGit(t, root, "checkout", "-q", "-b", "work")
	runGit(t, root, "push", "-q", "-u", "origin", "work")
	environment := commandEnvironment{evaluator: evaluator, bun: bun, setup: func(ctx context.Context, root string) (setup.Result, error) {
		t.Fatal("an in-place commit ran the writing setup")
		return setup.Result{}, nil
	}}
	return root, remote, environment
}

func commitPlan(id string, selection contract.CommitSelection) contract.WorkbenchCommitPlan {
	selection.Title, selection.Description = "test: "+id, "Deliver "+id+"."
	return contract.WorkbenchCommitPlan{ChangeID: id, Summary: id, Commits: map[string]contract.CommitSelection{"phosphorco/mono": selection}}
}

func TestInPlaceCommitRefusesOwnedFilesThatDifferFromTheProjection(t *testing.T) {
	root, _, environment := inPlaceCommitFixture(t)
	ctx := context.Background()
	manifest := filepath.Join(root, "pkg/@a/core/package.json")
	original, _ := os.ReadFile(manifest)
	writeInPlaceFile(t, manifest, strings.Replace(string(original), `"type": "module"`, `"type": "commonjs"`, 1))
	_, err := commitInPlace(ctx, root, commitPlan("hand-edit", contract.CommitSelection{FilePaths: []string{"pkg/@a/core/package.json"}}), environment)
	if err == nil || !strings.Contains(err.Error(), "differ from the projection (1 differing") || !strings.Contains(err.Error(), "remedy: run workbench setup") {
		t.Fatalf("hand-edited owned file = %v", err)
	}
	if contents, _ := os.ReadFile(manifest); !strings.Contains(string(contents), "commonjs") {
		t.Fatal("commit regenerated the hand-edited owned file")
	}
}

func TestInPlaceCommitRefusesHunkSelectionOfAnOwnedFile(t *testing.T) {
	root, _, environment := inPlaceCommitFixture(t)
	ctx := context.Background()
	// A declaration change makes setup rewrite an owned file in two places.
	writeInPlaceFile(t, filepath.Join(root, "workbench.pkl"), strings.Replace(commitInPlaceDeclaration, `exports { ["."] = "./src/index.ts" }`, `exports { ["."] = "./src/index.ts" }
    fields { ["version"] = "1.0.0"; ["zz"] = true }`, 1))
	if _, err := setup.RunWith(ctx, root, setup.NewToolchain(environment.evaluator, environment.bun)); err != nil {
		t.Fatal(err)
	}
	hunks, err := change.ListHunks(ctx, root, "pkg/@a/core/package.json")
	if err != nil || len(hunks) == 0 {
		t.Fatalf("hunks = %v %v", hunks, err)
	}
	_, err = commitInPlace(ctx, root, commitPlan("hunk", contract.CommitSelection{HunkIDs: []string{hunks[0].ID}, FilePaths: []string{"workbench.pkl"}}), environment)
	if err == nil || !strings.Contains(err.Error(), "select owned files whole") {
		t.Fatalf("hunk-selected owned file = %v", err)
	}
	// Selected whole, the owned file carries the projection and is delivered.
	output, err := commitInPlace(ctx, root, commitPlan("whole", contract.CommitSelection{FilePaths: []string{"pkg/@a/core/package.json", "workbench.pkl"}}), environment)
	if err != nil || !strings.Contains(output, "completed") {
		t.Fatalf("whole owned file = %q %v", output, err)
	}
	if pushed := runGit(t, root, "show", "origin/work:pkg/@a/core/package.json"); !strings.Contains(pushed, `"version": "1.0.0"`) {
		t.Fatalf("pushed manifest:\n%s", pushed)
	}
}

func TestInPlaceCommitGuardsTheDefaultBranch(t *testing.T) {
	root, _, environment := inPlaceCommitFixture(t)
	ctx := context.Background()
	runGit(t, root, "checkout", "-q", "main")
	writeInPlaceFile(t, filepath.Join(root, "pkg/@a/core/src/index.ts"), "export const core = 2\n")
	_, err := commitInPlace(ctx, root, commitPlan("main", contract.CommitSelection{FilePaths: []string{"pkg/@a/core/src/index.ts"}}), environment)
	if err == nil || !strings.Contains(err.Error(), `"main" is the remote's default branch`) {
		t.Fatalf("default branch = %v", err)
	}
	output, err := commitInPlace(ctx, root, commitPlan("main-allowed", contract.CommitSelection{FilePaths: []string{"pkg/@a/core/src/index.ts"}, AllowDefaultBranch: true}), environment)
	if err != nil || !strings.Contains(output, "completed") {
		t.Fatalf("allowed default branch = %q %v", output, err)
	}
}
