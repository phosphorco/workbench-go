package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/evaluate"
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

func TestReadManagedCheckoutsRejectsTamperedDeletionProvenance(t *testing.T) {
	tests := map[string]string{
		"trailing JSON":            `{"version":1,"resources":[]} {}`,
		"shape identity mismatch":  `{"version":1,"resources":[{"identity":"someone/else","github":"phosphorco/workbench-fixture-entry","shape":{"kind":"repository"},"canonicalPath":"repos/workbench-fixture-entry","createdByWorkbench":true}]}`,
		"duplicate canonical path": `{"version":1,"resources":[{"identity":"phosphorco/one","github":"phosphorco/one","shape":{"kind":"repository"},"canonicalPath":"repos/one","createdByWorkbench":true},{"identity":"another/one","github":"another/one","shape":{"kind":"repository"},"canonicalPath":"repos/one","createdByWorkbench":true}]}`,
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".workbench"), 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, ".workbench", "world.json"), encoded)
			if _, err := ReadManagedCheckouts(root); err == nil {
				t.Fatal("tampered Workbench-created provenance was accepted")
			}
		})
	}
}

func TestRunWithV020RoutesClosedShapesAndConvergesOrientation(t *testing.T) {
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}
	createRemote(t, root, remotes, "workbench-fixture-library", fmt.Sprintf("amends %q\n", localV020RepositoryURI))
	createRemote(t, root, remotes, "workbench-fixture-entry", fmt.Sprintf("amends %q\n\nscope = \"@workbench-entry\"\nincludes { [\"phosphorco/workbench-fixture-library\"] {} }\n", localV020PackageScopeURI))
	workbench := filepath.Join(root, "workbench")
	if err := os.MkdirAll(workbench, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, "workbench-subject.pkl"), fmt.Sprintf("amends %q\n\nworkLine { branch = \"workbench/v020\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/workbench-fixture-entry\" }\n", localV020SubjectURI))
	write(t, filepath.Join(workbench, "AGENTS.pkl"), fmt.Sprintf("amends %q\n\nprose = \"Fixture orientation.\"\n", localV020AgentInstructionsURI))
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+filepath.ToSlash(remotes)+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/phosphorco/")
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}

	first, err := RunWith(context.Background(), workbench, NewToolchain(evaluator, bun))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Resources) != 2 {
		t.Fatalf("resources = %#v", first.Resources)
	}
	identities := []string{first.Resources[0].Identity, first.Resources[1].Identity}
	slices.Sort(identities)
	wantIdentities := []string{"@workbench-entry", "phosphorco/workbench-fixture-library"}
	if !slices.Equal(identities, wantIdentities) {
		t.Fatalf("identities = %v, want %v", identities, wantIdentities)
	}
	for _, path := range []string{"pkg/@workbench-entry", "repos/workbench-fixture-library"} {
		if _, err := os.Stat(filepath.Join(workbench, filepath.FromSlash(path))); err != nil {
			t.Fatalf("canonical checkout %s: %v", path, err)
		}
	}
	agentsBefore, err := os.ReadFile(filepath.Join(workbench, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{"@workbench-entry", "phosphorco/workbench-fixture-entry", "repos/workbench-fixture-library", "workbench/v020"} {
		if !strings.Contains(string(agentsBefore), fact) {
			t.Errorf("AGENTS.md lacks %q", fact)
		}
	}

	second, err := RunWith(context.Background(), workbench, NewToolchain(evaluator, bun))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ChangedPaths) != 0 {
		t.Fatalf("second setup changed %#v", second.ChangedPaths)
	}
	agentsAfter, err := os.ReadFile(filepath.Join(workbench, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(agentsBefore, agentsAfter) {
		t.Fatal("AGENTS.md did not byte-converge")
	}
}

func TestRunWithRefusesAmbientBunAuthority(t *testing.T) {
	evaluator, err := evaluate.NewEvaluator("/does/not/need/to/run/pkl")
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunWith(context.Background(), t.TempDir(), NewToolchain(evaluator, "bun"))
	if err == nil || !strings.Contains(err.Error(), "exact absolute path") {
		t.Fatalf("RunWith relative Bun error = %v", err)
	}
}

func TestWorldReceiptKeepsOrphanProvenanceWithoutDeleting(t *testing.T) {
	root := t.TempDir()
	orphanPath := filepath.Join(root, "repos", "retired")
	if err := os.MkdirAll(orphanPath, 0o755); err != nil {
		t.Fatal(err)
	}
	previous := worldReceipt{Version: 1, Resources: []receiptResource{{
		Identity: "phosphorco/retired", GitHub: "phosphorco/retired",
		Shape:         contract.ResourceShape{Kind: contract.RepositoryShape},
		CanonicalPath: "repos/retired", CreatedByWorkbench: true,
	}}}
	if _, err := writeWorldReceipt(root, nil, previous, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := readWorldReceipt(root)
	if err != nil {
		t.Fatal(err)
	}
	orphans, err := reportOrphans(root, nil, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].Path != orphanPath {
		t.Fatalf("orphans = %#v", orphans)
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Fatalf("ordinary receipt/report removed checkout: %v", err)
	}
	managed, err := ReadManagedCheckouts(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 1 || !managed[0].CreatedByWorkbench {
		t.Fatalf("managed provenance = %#v", managed)
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
