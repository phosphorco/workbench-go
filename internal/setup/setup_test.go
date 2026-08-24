package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGeneratedLocalContractsMatchPublishedSourceCandidates(t *testing.T) {
	for path, generated := range map[string]string{
		"../../pkl/WorkbenchSubject.pkl":       localSubjectContract,
		"../../pkl/PackageScopeRepository.pkl": localRepositoryContract,
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != generated {
			t.Fatalf("generated contract is stale for %s", path)
		}
	}
}

func TestRunAssemblesClosureConvergesAndPreservesSource(t *testing.T) {
	fixture := newSetupFixture(t)
	first, err := Run(context.Background(), fixture.workbench)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.World.Resources) != 2 {
		t.Fatalf("resources = %#v", first.World.Resources)
	}
	for _, path := range []string{"pkg/@basindb", "pkg/@phosphorco"} {
		if branch := git(t, filepath.Join(fixture.workbench, filepath.FromSlash(path)), "branch", "--show-current"); branch != "local/meaningful-slice" {
			t.Fatalf("%s branch = %q", path, branch)
		}
	}
	sourcePath := filepath.Join(fixture.workbench, "pkg/@basindb/source-owned.txt")
	if err := os.WriteFile(sourcePath, []byte("preserve me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := git(t, filepath.Join(fixture.workbench, "pkg/@basindb"), "status", "--porcelain=v1", "--untracked-files=all")
	second, err := Run(context.Background(), fixture.workbench)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ChangedPaths) != 0 {
		t.Fatalf("second setup changed %#v", second.ChangedPaths)
	}
	after := git(t, filepath.Join(fixture.workbench, "pkg/@basindb"), "status", "--porcelain=v1", "--untracked-files=all")
	if before != after || !strings.Contains(after, "source-owned.txt") {
		t.Fatalf("source status changed: before %q after %q", before, after)
	}
}

func TestRunRefusesDirtyOtherBranchWithoutCanonicalGitMutation(t *testing.T) {
	fixture := newSetupFixture(t)
	if _, err := Run(context.Background(), fixture.workbench); err != nil {
		t.Fatal(err)
	}
	basindb := filepath.Join(fixture.workbench, "pkg/@basindb")
	git(t, basindb, "checkout", "main")
	if err := os.WriteFile(filepath.Join(basindb, "dirty.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := gitSnapshot(t, basindb)
	if _, err := Run(context.Background(), fixture.workbench); err == nil || !strings.Contains(err.Error(), "dirty checkout") {
		t.Fatalf("unsafe setup error = %v", err)
	}
	after := gitSnapshot(t, basindb)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("canonical Git state mutated:\nbefore=%#v\nafter=%#v", before, after)
	}
}

type setupFixture struct {
	workbench string
}

func newSetupFixture(t *testing.T) setupFixture {
	t.Helper()
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "community-packages", fmt.Sprintf("amends %q\n\nscope = \"@phosphorco\"\n", localRepositoryURI))
	createRemote(t, root, remotes, "basindb", fmt.Sprintf("amends %q\n\nscope = \"@basindb\"\nincludes { [\"@phosphorco\"] { github = \"phosphorco/community-packages\" } }\n", localRepositoryURI))
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = \"local/meaningful-slice\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/basindb\" }\n", localSubjectURI))
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	return setupFixture{workbench: workbench}
}

func createRemote(t *testing.T, root string, remotes string, name string, declaration string) {
	t.Helper()
	seed := filepath.Join(root, "seeds", name)
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "init", "-b", "main")
	git(t, seed, "config", "user.email", "setup@example.invalid")
	git(t, seed, "config", "user.name", "Setup Test")
	write(t, filepath.Join(seed, "workbench.pkl"), declaration)
	git(t, seed, "add", "workbench.pkl")
	git(t, seed, "commit", "-m", "declare resource")
	git(t, root, "clone", "--bare", seed, filepath.Join(remotes, name))
}

type snapshot struct {
	head   string
	branch string
	refs   string
	status string
}

func gitSnapshot(t *testing.T, root string) snapshot {
	t.Helper()
	return snapshot{
		head:   git(t, root, "rev-parse", "HEAD"),
		branch: git(t, root, "branch", "--show-current"),
		refs:   git(t, root, "show-ref"),
		status: git(t, root, "status", "--porcelain=v1", "--untracked-files=all"),
	}
}

func git(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func write(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
