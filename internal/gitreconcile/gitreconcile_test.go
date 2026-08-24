package gitreconcile_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/gitreconcile"
)

func TestMissingCheckoutCreatesSubjectBranchFromRemoteBase(t *testing.T) {
	fixture := newGitFixture(t, false)
	target := filepath.Join(t.TempDir(), "pkg", "resource")

	err := gitreconcile.Reconcile(context.Background(), []gitreconcile.Checkout{{
		Path:       target,
		RemoteURL:  fixture.remote,
		Branch:     "cole/shared-line",
		BaseBranch: "main",
	}})
	if err != nil {
		t.Fatal(err)
	}

	if branch := git(t, target, "branch", "--show-current"); branch != "cole/shared-line" {
		t.Fatalf("branch = %q, want cole/shared-line", branch)
	}
	if head := git(t, target, "rev-parse", "HEAD"); head != fixture.baseCommit {
		t.Fatalf("HEAD = %q, want remote base %q", head, fixture.baseCommit)
	}
	if origin := git(t, target, "remote", "get-url", "origin"); origin != fixture.remote {
		t.Fatalf("origin = %q, want %q", origin, fixture.remote)
	}
}

func TestExistingCheckoutUsesRemoteSubjectBranch(t *testing.T) {
	fixture := newGitFixture(t, true)
	target := filepath.Join(t.TempDir(), "resource")
	git(t, "", "clone", fixture.remote, target)

	err := gitreconcile.Reconcile(context.Background(), []gitreconcile.Checkout{{
		Path:       target,
		RemoteURL:  fixture.remote,
		Branch:     "cole/shared-line",
		BaseBranch: "main",
	}})
	if err != nil {
		t.Fatal(err)
	}

	if branch := git(t, target, "branch", "--show-current"); branch != "cole/shared-line" {
		t.Fatalf("branch = %q, want cole/shared-line", branch)
	}
	if head := git(t, target, "rev-parse", "HEAD"); head != fixture.subjectCommit {
		t.Fatalf("HEAD = %q, want remote Subject %q", head, fixture.subjectCommit)
	}
	if upstream := git(t, target, "rev-parse", "--abbrev-ref", "@{upstream}"); upstream != "origin/cole/shared-line" {
		t.Fatalf("upstream = %q, want origin/cole/shared-line", upstream)
	}
}

func TestExistingLocalSubjectBranchIsSelectedWithoutRewritingIt(t *testing.T) {
	fixture := newGitFixture(t, false)
	target := filepath.Join(t.TempDir(), "resource")
	git(t, "", "clone", fixture.remote, target)
	git(t, target, "config", "user.email", "gitreconcile@example.invalid")
	git(t, target, "config", "user.name", "Git Reconcile Test")
	git(t, target, "checkout", "-b", "cole/shared-line")
	writeFile(t, filepath.Join(target, "local.txt"), "local subject commit\n")
	git(t, target, "add", "local.txt")
	git(t, target, "commit", "-m", "local subject work")
	localHead := git(t, target, "rev-parse", "HEAD")
	git(t, target, "checkout", "main")

	err := gitreconcile.Reconcile(context.Background(), []gitreconcile.Checkout{{
		Path:       target,
		RemoteURL:  fixture.remote,
		Branch:     "cole/shared-line",
		BaseBranch: "main",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if branch := git(t, target, "branch", "--show-current"); branch != "cole/shared-line" {
		t.Fatalf("branch = %q, want cole/shared-line", branch)
	}
	if head := git(t, target, "rev-parse", "HEAD"); head != localHead {
		t.Fatalf("local Subject HEAD changed from %q to %q", localHead, head)
	}
}

func TestDirtyOtherBranchRefusesBeforeAnyCheckoutMutation(t *testing.T) {
	fixture := newGitFixture(t, true)
	root := t.TempDir()
	missing := filepath.Join(root, "first-missing")
	dirty := filepath.Join(root, "second-dirty")
	git(t, "", "clone", fixture.remote, dirty)
	writeFile(t, filepath.Join(dirty, "source.txt"), "dirty work on main\n")

	refsBefore := git(t, dirty, "for-each-ref", "--format=%(refname) %(objectname)", "refs")
	statusBefore := gitStatus(t, dirty)
	headBefore := git(t, dirty, "rev-parse", "HEAD")

	err := gitreconcile.Reconcile(context.Background(), []gitreconcile.Checkout{
		{Path: missing, RemoteURL: fixture.remote, Branch: "cole/shared-line", BaseBranch: "main"},
		{Path: dirty, RemoteURL: fixture.remote, Branch: "cole/shared-line", BaseBranch: "main"},
	})
	if err == nil {
		t.Fatal("dirty checkout on another branch was reconciled")
	}
	if !strings.Contains(err.Error(), "dirty checkout is on branch") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("earlier missing checkout was mutated before full preflight: stat error = %v", statErr)
	}
	if refsAfter := git(t, dirty, "for-each-ref", "--format=%(refname) %(objectname)", "refs"); refsAfter != refsBefore {
		t.Fatalf("refs changed after refusal\nbefore:\n%s\nafter:\n%s", refsBefore, refsAfter)
	}
	if statusAfter := gitStatus(t, dirty); statusAfter != statusBefore {
		t.Fatalf("status changed after refusal\nbefore: %q\nafter:  %q", statusBefore, statusAfter)
	}
	if headAfter := git(t, dirty, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Fatalf("HEAD changed after refusal: before %q, after %q", headBefore, headAfter)
	}
}

func TestDirtySourceOnSubjectBranchIsPreserved(t *testing.T) {
	fixture := newGitFixture(t, true)
	target := filepath.Join(t.TempDir(), "resource")
	git(t, "", "clone", "--branch", "cole/shared-line", fixture.remote, target)
	sourcePath := filepath.Join(target, "source.txt")
	contents := "representative pre-existing Git-owned source change\n"
	writeFile(t, sourcePath, contents)

	refsBefore := git(t, target, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	statusBefore := gitStatus(t, target)
	headBefore := git(t, target, "rev-parse", "HEAD")

	err := gitreconcile.Reconcile(context.Background(), []gitreconcile.Checkout{{
		Path:       target,
		RemoteURL:  fixture.remote,
		Branch:     "cole/shared-line",
		BaseBranch: "main",
	}})
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != contents {
		t.Fatalf("source contents = %q, want %q", encoded, contents)
	}
	if statusAfter := gitStatus(t, target); statusAfter != statusBefore {
		t.Fatalf("status changed during reconciliation\nbefore: %q\nafter:  %q", statusBefore, statusAfter)
	}
	if headAfter := git(t, target, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Fatalf("HEAD changed from %q to %q", headBefore, headAfter)
	}
	if refsAfter := git(t, target, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); refsAfter != refsBefore {
		t.Fatalf("local branches changed\nbefore:\n%s\nafter:\n%s", refsBefore, refsAfter)
	}
}

type gitFixture struct {
	remote        string
	baseCommit    string
	subjectCommit string
}

func newGitFixture(t *testing.T, withSubject bool) gitFixture {
	t.Helper()
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "resource.git")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, seed, "init", "-b", "main")
	git(t, seed, "config", "user.email", "gitreconcile@example.invalid")
	git(t, seed, "config", "user.name", "Git Reconcile Test")
	writeFile(t, filepath.Join(seed, "source.txt"), "base source\n")
	git(t, seed, "add", "source.txt")
	git(t, seed, "commit", "-m", "base")
	fixture := gitFixture{remote: remote, baseCommit: git(t, seed, "rev-parse", "HEAD")}
	if withSubject {
		git(t, seed, "checkout", "-b", "cole/shared-line")
		writeFile(t, filepath.Join(seed, "subject.txt"), "shared line\n")
		git(t, seed, "add", "subject.txt")
		git(t, seed, "commit", "-m", "subject")
		fixture.subjectCommit = git(t, seed, "rev-parse", "HEAD")
		git(t, seed, "checkout", "main")
	}
	git(t, "", "clone", "--bare", seed, remote)
	return fixture
}

func gitStatus(t *testing.T, directory string) string {
	t.Helper()
	return git(t, directory, "status", "--porcelain=v1", "--untracked-files=all")
}

func git(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
