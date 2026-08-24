package acceptance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	publicSubjectContract = releasePackageURI + "#/WorkbenchSubject.pkl"
	proofBranch           = "workbench/proof-0.1.0"
	subjectBranch         = proofBranch
	entryRepositoryURL    = "https://github.com/phosphorco/workbench-fixture-entry"
	libraryRepositoryURL  = "https://github.com/phosphorco/workbench-fixture-library"
	entryProofRevision    = "74ee45909df2540b4056209d9e0d39e8dcabc56a"
	libraryProofRevision  = "d4a36b29b5a8e66de6b2410c2ee8c32150741123"
)

// TestWorkbenchFirstMeaningfulSlice exercises Workbench only through its
// released contract, public CLI, anonymous HTTPS repositories, and resulting
// filesystem and Git state. The repositories and release are permanent proof
// inputs; this test creates only disposable developer workbenches.
func TestWorkbenchFirstMeaningfulSlice(t *testing.T) {
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
	anonymousEnvironment := publicEnvironment(testRoot, anonymousHome)
	binary := buildPublicCLI(t, moduleRoot)

	t.Run("assembles and converges", func(t *testing.T) {
		workbench := newPublicWorkbench(t, testRoot, "convergent-workbench", anonymousEnvironment)
		firstOutput := runSetup(t, binary, workbench, anonymousEnvironment)
		if !strings.Contains(firstOutput, "Workbench reconciled 2 repositories") {
			t.Fatalf("first setup did not report the real two-repository world:\n%s", firstOutput)
		}

		entry := filepath.Join(workbench, "pkg", "@workbench-entry")
		library := filepath.Join(workbench, "pkg", "@workbench-library")
		assertCheckout(t, anonymousEnvironment, entry, entryRepositoryURL)
		assertCheckout(t, anonymousEnvironment, library, libraryRepositoryURL)
		if entryRoot := publicGit(t, anonymousEnvironment, entry, "rev-parse", "--show-toplevel"); entryRoot == publicGit(t, anonymousEnvironment, library, "rev-parse", "--show-toplevel") {
			t.Fatalf("entry and library resolve to the same Git authority %q", entryRoot)
		}
		assertRemoteProofBranch(t, anonymousEnvironment, entryRepositoryURL, entryProofRevision)
		assertRemoteProofBranch(t, anonymousEnvironment, libraryRepositoryURL, libraryProofRevision)

		assertWorkspaceProjection(t, workbench)
		assertWorkspaceLink(t, entry, library)
		assertProjectedSkills(t, workbench)
		runCrossRepositoryTypecheck(t, anonymousEnvironment, workbench, entry)
		beforeProjection := projectionDigest(t, workbench)

		trackedSource := filepath.Join(entry, "src", "index.ts")
		beforeSource, err := os.ReadFile(trackedSource)
		if err != nil {
			t.Fatalf("read Git-owned entry source: %v", err)
		}
		wantSource := append([]byte(nil), beforeSource...)
		wantSource = append(wantSource, []byte("\n// preserved local source state\n")...)
		if err := os.WriteFile(trackedSource, wantSource, 0o644); err != nil {
			t.Fatalf("create pre-existing Git-owned source change: %v", err)
		}
		beforeStatus := publicGit(t, anonymousEnvironment, entry, "status", "--porcelain=v1", "--untracked-files=all")
		if !strings.Contains(beforeStatus, "src/index.ts") {
			t.Fatalf("pre-existing source change is not visible to Git: %q", beforeStatus)
		}

		secondOutput := runSetup(t, binary, workbench, anonymousEnvironment)
		if !strings.Contains(secondOutput, "0 generated paths changed") {
			t.Fatalf("second setup did not converge:\n%s", secondOutput)
		}
		if afterProjection := projectionDigest(t, workbench); afterProjection != beforeProjection {
			t.Fatalf("second setup changed Workbench-owned projection: before %s after %s", beforeProjection, afterProjection)
		}
		afterStatus := publicGit(t, anonymousEnvironment, entry, "status", "--porcelain=v1", "--untracked-files=all")
		afterSource, err := os.ReadFile(trackedSource)
		if err != nil {
			t.Fatalf("read preserved Git-owned entry source: %v", err)
		}
		if beforeStatus != afterStatus || !slices.Equal(afterSource, wantSource) {
			t.Fatalf("setup consumed Git-owned source state: status before %q after %q", beforeStatus, afterStatus)
		}
		if status := publicGit(t, anonymousEnvironment, workbench, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
			t.Fatalf("outer context observes ignored local/generated world:\n%s", status)
		}
	})

	t.Run("refuses dirty other branch without Git mutation", func(t *testing.T) {
		workbench := newPublicWorkbench(t, testRoot, "unsafe-workbench", anonymousEnvironment)
		runSetup(t, binary, workbench, anonymousEnvironment)
		entry := filepath.Join(workbench, "pkg", "@workbench-entry")
		library := filepath.Join(workbench, "pkg", "@workbench-library")

		publicGit(t, anonymousEnvironment, entry, "checkout", "main")
		source := filepath.Join(entry, "src", "index.ts")
		contents, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read source before adversarial edit: %v", err)
		}
		if err := os.WriteFile(source, append(contents, []byte("\n// dirty on the other branch\n")...), 0o644); err != nil {
			t.Fatalf("make checkout dirty on main: %v", err)
		}

		before := map[string]gitState{
			entry:   observeGitState(t, anonymousEnvironment, entry),
			library: observeGitState(t, anonymousEnvironment, library),
		}
		command := exec.Command(binary, "setup")
		command.Dir = workbench
		command.Env = anonymousEnvironment
		output, err := command.CombinedOutput()
		if err == nil {
			t.Fatalf("setup accepted a dirty checkout on main:\n%s", output)
		}
		if !strings.Contains(string(output), "dirty checkout") || !strings.Contains(string(output), subjectBranch) {
			t.Fatalf("setup returned the wrong public refusal: %v\n%s", err, output)
		}
		after := map[string]gitState{
			entry:   observeGitState(t, anonymousEnvironment, entry),
			library: observeGitState(t, anonymousEnvironment, library),
		}
		for _, checkout := range []string{entry, library} {
			if before[checkout] != after[checkout] {
				t.Errorf("refused setup mutated Git state in %q:\nbefore: %#v\nafter:  %#v", checkout, before[checkout], after[checkout])
			}
		}
	})
}

func buildPublicCLI(t *testing.T, moduleRoot string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "workbench")
	command := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/workbench")
	command.Dir = moduleRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build public Workbench CLI: %v\n%s", err, output)
	}
	return binary
}

func newPublicWorkbench(t *testing.T, testRoot, name string, environment []string) string {
	return newPublicWorkbenchForBranch(t, testRoot, name, subjectBranch, environment)
}

func newPublicWorkbenchForBranch(t *testing.T, testRoot, name, branch string, environment []string) string {
	t.Helper()
	root := filepath.Join(testRoot, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writePublicFile(t, filepath.Join(root, ".gitignore"), `workbench-subject.pkl
.workbench/
.agents/skills/
bun.lock
node_modules/
package.json
tsconfig.json
pkg/
repos/
`)
	publicGit(t, environment, root, "init", "-b", "main")
	publicGit(t, environment, root, "config", "user.email", "workbench-proof@example.invalid")
	publicGit(t, environment, root, "config", "user.name", "Workbench Public Proof")
	publicGit(t, environment, root, "add", ".gitignore")
	publicGit(t, environment, root, "commit", "-m", "initialize disposable Workbench context")
	writePublicFile(t, filepath.Join(root, "workbench-subject.pkl"), fmt.Sprintf(`amends %q

workLine {
  branch = %q
  baseBranch = "main"
}

entrypoints {
  %q
}
`, publicSubjectContract, branch, entryRepositoryURL))
	if ignored := publicGit(t, environment, root, "check-ignore", "workbench-subject.pkl"); ignored != "workbench-subject.pkl" {
		t.Fatalf("Subject is not ignored by the outer context: %q", ignored)
	}
	return root
}

func publicEnvironment(testRoot, anonymousHome string) []string {
	drop := []string{
		"GH_TOKEN=", "GITHUB_TOKEN=", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_CONFIG_",
		"HOME=", "XDG_CONFIG_HOME=", "XDG_CACHE_HOME=", "XDG_RUNTIME_DIR=", "NPM_CONFIG_USERCONFIG=", "NODE_AUTH_TOKEN=", "NPM_TOKEN=",
	}
	environment := make([]string, 0, len(os.Environ())+8)
	for _, variable := range os.Environ() {
		excluded := false
		for _, prefix := range drop {
			if strings.HasPrefix(variable, prefix) {
				excluded = true
				break
			}
		}
		if !excluded {
			environment = append(environment, variable)
		}
	}
	return append(environment,
		"HOME="+anonymousHome,
		"XDG_CONFIG_HOME="+filepath.Join(anonymousHome, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(anonymousHome, ".cache"),
		"XDG_RUNTIME_DIR="+filepath.Join(testRoot, "runtime"),
		"GSETTINGS_BACKEND=memory",
		"NPM_CONFIG_USERCONFIG=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"BUN_INSTALL_CACHE_DIR="+filepath.Join(testRoot, "bun-cache"),
		"TMPDIR="+filepath.Join(testRoot, "tmp"),
	)
}

func runSetup(t *testing.T, binary, workbench string, environment []string) string {
	t.Helper()
	command := exec.Command(binary, "setup")
	command.Dir = workbench
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("workbench setup through anonymous public boundaries: %v\n%s", err, output)
	}
	return string(output)
}

func assertCheckout(t *testing.T, environment []string, root, remote string) {
	t.Helper()
	if actual := publicGit(t, environment, root, "config", "--get", "remote.origin.url"); actual != remote {
		t.Errorf("origin = %q, want canonical anonymous HTTPS %q", actual, remote)
	}
	if branch := publicGit(t, environment, root, "branch", "--show-current"); branch != subjectBranch {
		t.Errorf("branch = %q, want Subject branch %q", branch, subjectBranch)
	}
	// The durable 0.1 proof branch records the exact main revision from which
	// the collaboration line was created. Fixture main may evolve afterward,
	// so current main must descend from that preserved anchor.
	command := exec.Command("git", "merge-base", "--is-ancestor", "HEAD", "refs/remotes/origin/main")
	command.Dir = root
	command.Env = environment
	if output, err := command.CombinedOutput(); err != nil {
		t.Errorf("origin/main does not descend from the preserved Subject anchor: %v\n%s", err, output)
	}
}

func assertRemoteProofBranch(t *testing.T, environment []string, remote, wantRevision string) {
	t.Helper()
	command := exec.Command("git", "ls-remote", "--heads", remote, "refs/heads/"+proofBranch)
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("observe public remote branches for %q: %v\n%s", remote, err, output)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[0] != wantRevision || fields[1] != "refs/heads/"+proofBranch {
		t.Fatalf("Subject proof branch at %q = %q, want %s refs/heads/%s", remote, strings.TrimSpace(string(output)), wantRevision, proofBranch)
	}
}

func assertWorkspaceProjection(t *testing.T, workbench string) {
	t.Helper()
	rootManifest := decodeJSON[struct {
		Workspaces []string `json:"workspaces"`
	}](t, filepath.Join(workbench, "package.json"))
	wantWorkspaces := []string{"pkg/@workbench-entry", "pkg/@workbench-library"}
	if !slices.Equal(rootManifest.Workspaces, wantWorkspaces) {
		t.Errorf("root workspaces = %v, want %v", rootManifest.Workspaces, wantWorkspaces)
	}
	entryManifest := decodeJSON[struct {
		Name         string            `json:"name"`
		Dependencies map[string]string `json:"dependencies"`
	}](t, filepath.Join(workbench, "pkg", "@workbench-entry", "package.json"))
	if entryManifest.Name != "@workbench-entry/app" {
		t.Errorf("entry package name = %q", entryManifest.Name)
	}
	if entryManifest.Dependencies["@workbench-library/shared"] != "workspace:*" {
		t.Errorf("entry workspace dependency = %q, want workspace:*", entryManifest.Dependencies["@workbench-library/shared"])
	}
	if entryManifest.Dependencies["typescript"] != "^5.9.2" {
		t.Errorf("entry declared TypeScript policy = %q, want ^5.9.2", entryManifest.Dependencies["typescript"])
	}
	libraryManifest := decodeJSON[struct {
		Name string `json:"name"`
	}](t, filepath.Join(workbench, "pkg", "@workbench-library", "package.json"))
	if libraryManifest.Name != "@workbench-library/shared" {
		t.Errorf("library package name = %q", libraryManifest.Name)
	}
	entryDeclaration, err := os.ReadFile(filepath.Join(workbench, "pkg", "@workbench-entry", "workbench.pkl"))
	if err != nil {
		t.Fatalf("read repository-owned entry declaration: %v", err)
	}
	if !strings.Contains(string(entryDeclaration), "phosphorco/workbench-fixture-library") {
		t.Fatalf("entry declaration does not own the library include:\n%s", entryDeclaration)
	}
	rootTSConfig := decodeJSON[struct {
		References []struct {
			Path string `json:"path"`
		} `json:"references"`
	}](t, filepath.Join(workbench, "tsconfig.json"))
	references := make([]string, 0, len(rootTSConfig.References))
	for _, reference := range rootTSConfig.References {
		references = append(references, strings.TrimPrefix(reference.Path, "./"))
	}
	if !slices.Equal(references, wantWorkspaces) {
		t.Errorf("root TypeScript references = %v, want %v", references, wantWorkspaces)
	}
}

func assertWorkspaceLink(t *testing.T, entry, library string) {
	t.Helper()
	link := filepath.Join(entry, "node_modules", "@workbench-library", "shared")
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("resolve real cross-repository workspace link %q: %v", link, err)
	}
	want, err := filepath.EvalSymlinks(library)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("workspace link resolves to %q, want independent checkout %q", resolved, want)
	}
}

func assertProjectedSkills(t *testing.T, workbench string) {
	t.Helper()
	engineering := filepath.Join(workbench, ".agents", "skills", "workbench-fixture-engineering", "SKILL.md")
	contents, err := os.ReadFile(engineering)
	if err != nil {
		t.Fatalf("read domain-selected skill: %v", err)
	}
	if !strings.Contains(string(contents), "domain: engineering") || !strings.Contains(string(contents), "`$workbench-fixture-support`") {
		t.Fatalf("selected skill does not expose its domain and explicit composition dependency:\n%s", contents)
	}
	if _, err := os.Stat(filepath.Join(workbench, ".agents", "skills", "workbench-fixture-support", "SKILL.md")); err != nil {
		t.Fatalf("explicitly composed support skill was not projected: %v", err)
	}
}

func runCrossRepositoryTypecheck(t *testing.T, environment []string, workbench, entry string) {
	t.Helper()
	entrySource, err := os.ReadFile(filepath.Join(entry, "src", "index.ts"))
	if err != nil {
		t.Fatalf("read entry package source: %v", err)
	}
	librarySource, err := os.ReadFile(filepath.Join(workbench, "pkg", "@workbench-library", "src", "index.ts"))
	if err != nil {
		t.Fatalf("read library package source: %v", err)
	}
	if !strings.Contains(string(entrySource), "sharedMessage") || !strings.Contains(string(librarySource), "sharedMessage") {
		t.Fatalf("fixture sources do not carry the promised sharedMessage cross-repository edge")
	}
	compiler := filepath.Join(entry, "node_modules", ".bin", "tsc")
	if _, err := os.Stat(compiler); err != nil {
		t.Fatalf("fixture-declared TypeScript compiler is not installed at %q: %v", compiler, err)
	}
	command := exec.Command(compiler, "-b", filepath.Join(workbench, "tsconfig.json"), "--pretty", "false")
	command.Dir = workbench
	command.Env = environment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("real cross-repository TypeScript build resolving sharedMessage: %v\n%s", err, output)
	}
}

func projectionDigest(t *testing.T, workbench string) string {
	t.Helper()
	paths := []string{
		"package.json",
		"tsconfig.json",
		"bun.lock",
		"pkg/@workbench-entry/package.json",
		"pkg/@workbench-entry/tsconfig.json",
		"pkg/@workbench-library/package.json",
		"pkg/@workbench-library/tsconfig.json",
		".agents/skills/workbench-fixture-engineering/SKILL.md",
		".agents/skills/workbench-fixture-support/SKILL.md",
	}
	digest := sha256.New()
	for _, relative := range paths {
		contents, err := os.ReadFile(filepath.Join(workbench, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read convergent projection %q: %v", relative, err)
		}
		_, _ = digest.Write([]byte(relative))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write(contents)
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

type gitState struct {
	HEAD   string
	Refs   string
	Index  string
	Status string
}

func observeGitState(t *testing.T, environment []string, root string) gitState {
	t.Helper()
	indexPath := publicGit(t, environment, root, "rev-parse", "--git-path", "index")
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(root, indexPath)
	}
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read Git index for %q: %v", root, err)
	}
	digest := sha256.Sum256(index)
	return gitState{
		HEAD:   publicGit(t, environment, root, "rev-parse", "HEAD"),
		Refs:   publicGit(t, environment, root, "for-each-ref", "--sort=refname", "--format=%(refname) %(objectname)"),
		Index:  hex.EncodeToString(digest[:]),
		Status: publicGit(t, environment, root, "status", "--porcelain=v1", "--untracked-files=all"),
	}
}

func publicGit(t *testing.T, environment []string, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	command.Env = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writePublicFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func decodeJSON[T any](t *testing.T, path string) T {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated JSON %q: %v", path, err)
	}
	var value T
	if err := json.Unmarshal(contents, &value); err != nil {
		t.Fatalf("decode generated JSON %q: %v", path, err)
	}
	return value
}
