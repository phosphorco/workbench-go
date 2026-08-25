package orphan_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/orphan"
)

func TestReportOrphansNeverDeletes(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "repos", "library")
	mustMkdir(t, checkout)
	mustWrite(t, filepath.Join(checkout, "source.txt"), "preserve me\n")
	candidate := repositoryCandidate(root, "phosphorco/library")

	report, err := orphan.Report(
		[]orphan.Resource{{Identity: "@entry", GitHub: "phosphorco/entry", Shape: contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@entry"}, CanonicalPath: "pkg/@entry"}},
		[]orphan.Candidate{candidate},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Orphans) != 1 || report.Orphans[0].Identity != candidate.Identity {
		t.Fatalf("orphan report = %#v", report)
	}
	if got := mustRead(t, filepath.Join(checkout, "source.txt")); got != "preserve me\n" {
		t.Fatalf("ordinary reporting mutated source: %q", got)
	}
}

func TestPreflightRefusesParticipatingRepository(t *testing.T) {
	root := t.TempDir()
	candidate := repositoryCandidate(root, "phosphorco/library")
	mustMkdir(t, candidate.Path)
	participatingRepository := orphan.Resource{
		Identity:      candidate.Identity,
		GitHub:        candidate.GitHub,
		Shape:         candidate.Shape,
		CanonicalPath: candidate.CanonicalPath,
	}
	observer := fixedObserver(map[string]orphan.Observation{candidate.Path: safeObservation(candidate)})

	plan, err := orphan.Preflight(orphan.Request{
		Root:              root,
		RepositoryClosure: []orphan.Resource{participatingRepository},
		Candidates:        []orphan.Candidate{candidate},
	}, observer)
	if err == nil {
		t.Fatalf("participating repository received deletion authorization: %#v", plan)
	}
	removeCalls := 0
	if _, applyErr := orphan.Apply(plan, observer, func(string) error { removeCalls++; return nil }); applyErr == nil {
		t.Fatal("refused plan applied")
	}
	if removeCalls != 0 {
		t.Fatalf("remover called %d times", removeCalls)
	}
}

func TestPreflightRefusalsAuthorizeZeroDeletion(t *testing.T) {
	root := t.TempDir()
	candidate := repositoryCandidate(root, "phosphorco/library")
	mustMkdir(t, candidate.Path)
	base := safeObservation(candidate)

	cases := map[string]func(orphan.Observation) orphan.Observation{
		"dirty":            func(value orphan.Observation) orphan.Observation { value.Status = " M source.txt"; return value },
		"unpushed":         func(value orphan.Observation) orphan.Observation { value.UpstreamHead = otherCommit; return value },
		"ambiguous origin": func(value orphan.Observation) orphan.Observation { value.OriginCount = 2; return value },
		"wrong origin": func(value orphan.Observation) orphan.Observation {
			value.OriginGitHub = "phosphorco/other"
			return value
		},
		"detached":           func(value orphan.Observation) orphan.Observation { value.Branch = ""; return value },
		"ambiguous upstream": func(value orphan.Observation) orphan.Observation { value.UpstreamBranch = ""; return value },
		"invalid ref":        func(value orphan.Observation) orphan.Observation { value.Head = "HEAD"; return value },
		"unreachable":        func(value orphan.Observation) orphan.Observation { value.RemoteHead = ""; return value },
		"remote moved":       func(value orphan.Observation) orphan.Observation { value.RemoteHead = otherCommit; return value },
		"nested":             func(value orphan.Observation) orphan.Observation { value.NestedCheckout = true; return value },
		"not disposable":     func(value orphan.Observation) orphan.Observation { value.Disposable = false; return value },
		"missing":            func(value orphan.Observation) orphan.Observation { value.Exists = false; return value },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			removeCalls := 0
			observer := fixedObserver(map[string]orphan.Observation{candidate.Path: mutate(base)})
			plan, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{candidate}}, observer)
			if err == nil {
				t.Fatalf("unsafe checkout received plan: %#v", plan)
			}
			if _, applyErr := orphan.Apply(plan, observer, func(string) error { removeCalls++; return nil }); applyErr == nil {
				t.Fatal("invalid plan applied")
			}
			if removeCalls != 0 {
				t.Fatalf("remover called %d times", removeCalls)
			}
		})
	}
}

func TestOneUnsafeCandidateBlocksWholePrune(t *testing.T) {
	root := t.TempDir()
	first := repositoryCandidate(root, "phosphorco/first")
	second := repositoryCandidate(root, "phosphorco/second")
	mustMkdir(t, first.Path)
	mustMkdir(t, second.Path)
	unsafe := safeObservation(second)
	unsafe.Status = "?? untracked.txt"
	observer := fixedObserver(map[string]orphan.Observation{
		first.Path:  safeObservation(first),
		second.Path: unsafe,
	})

	if _, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{first, second}}, observer); err == nil {
		t.Fatal("mixed-safe request received deletion authorization")
	}
}

func TestApplyReobservesAllCandidatesBeforeDeletion(t *testing.T) {
	root := t.TempDir()
	first := repositoryCandidate(root, "phosphorco/first")
	second := repositoryCandidate(root, "phosphorco/second")
	mustMkdir(t, first.Path)
	mustMkdir(t, second.Path)
	states := map[string]orphan.Observation{first.Path: safeObservation(first), second.Path: safeObservation(second)}
	observer := fixedObserver(states)
	plan, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{first, second}}, observer)
	if err != nil {
		t.Fatal(err)
	}
	changed := states[second.Path]
	changed.Status = " M source.txt"
	states[second.Path] = changed
	removeCalls := 0
	if _, err := orphan.Apply(plan, observer, func(string) error { removeCalls++; return nil }); err == nil {
		t.Fatal("stale plan applied")
	}
	if removeCalls != 0 {
		t.Fatalf("remover called %d times before all re-observations passed", removeCalls)
	}
}

func TestPreflightRefusesStaleTrackingAfterLiveRemoteChanges(t *testing.T) {
	for _, changeRemote := range []struct {
		name string
		run  func(*testing.T, string, string)
	}{
		{name: "advanced", run: func(t *testing.T, remote, checkout string) {
			other := filepath.Join(filepath.Dir(remote), "other")
			git(t, filepath.Dir(remote), "clone", remote, other)
			git(t, other, "config", "user.name", "Remote Writer")
			git(t, other, "config", "user.email", "remote@example.invalid")
			mustWrite(t, filepath.Join(other, "new.txt"), "advanced\n")
			git(t, other, "add", "new.txt")
			git(t, other, "commit", "-m", "advance remote")
			git(t, other, "push", "origin", "main")
		}},
		{name: "deleted", run: func(t *testing.T, remote, _ string) {
			git(t, remote, "update-ref", "-d", "refs/heads/main")
		}},
	} {
		t.Run(changeRemote.name, func(t *testing.T) {
			root := t.TempDir()
			remote := filepath.Join(root, "recoverable.git")
			checkout := filepath.Join(root, "repos", "library")
			git(t, root, "init", "--bare", remote)
			git(t, root, "clone", remote, checkout)
			git(t, checkout, "config", "user.name", "Workbench Test")
			git(t, checkout, "config", "user.email", "workbench@example.invalid")
			mustWrite(t, filepath.Join(checkout, "source.txt"), "recoverable\n")
			git(t, checkout, "add", "source.txt")
			git(t, checkout, "commit", "-m", "fixture")
			git(t, checkout, "branch", "-M", "main")
			git(t, checkout, "push", "-u", "origin", "main")
			git(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
			localHead := git(t, checkout, "rev-parse", "HEAD")
			changeRemote.run(t, remote, checkout)

			candidate := repositoryCandidate(root, "phosphorco/library")
			observer := func(subject orphan.Candidate) (orphan.Observation, error) {
				return orphan.Observation{
					Exists: true, OriginCount: 1, OriginGitHub: subject.GitHub,
					Branch: "main", Head: localHead, UpstreamBranch: "origin/main",
					UpstreamHead: localHead, RemoteHead: observeRemoteHead(t, checkout, "main"),
					Disposable: true,
				}, nil
			}
			removeCalls := 0
			if _, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{candidate}}, observer); err == nil {
				t.Fatal("stale tracking ref received deletion authorization")
			}
			if removeCalls != 0 {
				t.Fatalf("remover called %d times", removeCalls)
			}
			if got := mustRead(t, filepath.Join(checkout, "source.txt")); got != "recoverable\n" {
				t.Fatalf("checkout content changed: %q", got)
			}
		})
	}
}

func TestPruneRefusesSymlinkedOrChangedAncestryBeforeRemove(t *testing.T) {
	t.Run("symlink root", func(t *testing.T) {
		parent := t.TempDir()
		realRoot := filepath.Join(parent, "real")
		mustMkdir(t, filepath.Join(realRoot, "repos", "library"))
		linkedRoot := filepath.Join(parent, "linked")
		if err := os.Symlink(realRoot, linkedRoot); err != nil {
			t.Fatal(err)
		}
		candidate := repositoryCandidate(linkedRoot, "phosphorco/library")
		if _, err := orphan.Preflight(orphan.Request{Root: linkedRoot, Candidates: []orphan.Candidate{candidate}}, fixedObserver(map[string]orphan.Observation{candidate.Path: safeObservation(candidate)})); err == nil {
			t.Fatal("symlinked root received deletion authorization")
		}
	})

	t.Run("candidate symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "elsewhere")
		mustMkdir(t, target)
		candidate := repositoryCandidate(root, "phosphorco/library")
		mustMkdir(t, filepath.Dir(candidate.Path))
		if err := os.Symlink(target, candidate.Path); err != nil {
			t.Fatal(err)
		}
		if _, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{candidate}}, fixedObserver(map[string]orphan.Observation{candidate.Path: safeObservation(candidate)})); err == nil {
			t.Fatal("symlinked candidate received deletion authorization")
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("symlink target changed: %v", err)
		}
	})

	t.Run("parent swapped after preflight", func(t *testing.T) {
		root := t.TempDir()
		candidate := repositoryCandidate(root, "phosphorco/library")
		mustMkdir(t, candidate.Path)
		observer := fixedObserver(map[string]orphan.Observation{candidate.Path: safeObservation(candidate)})
		plan, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{candidate}}, observer)
		if err != nil {
			t.Fatal(err)
		}
		originalParent := filepath.Join(root, "repos-original")
		if err := os.Rename(filepath.Join(root, "repos"), originalParent); err != nil {
			t.Fatal(err)
		}
		replacementParent := filepath.Join(root, "replacement")
		mustMkdir(t, filepath.Join(replacementParent, "library"))
		if err := os.Symlink(replacementParent, filepath.Join(root, "repos")); err != nil {
			t.Fatal(err)
		}
		removeCalls := 0
		if _, err := orphan.Apply(plan, observer, func(string) error { removeCalls++; return nil }); err == nil {
			t.Fatal("changed parent ancestry reached remover")
		}
		if removeCalls != 0 {
			t.Fatalf("remover called %d times", removeCalls)
		}
		if _, err := os.Stat(filepath.Join(originalParent, "library")); err != nil {
			t.Fatalf("original checkout changed: %v", err)
		}
	})
}

func TestApplyPrunesOnlyRecoverableDisposableCheckout(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "recoverable.git")
	checkout := filepath.Join(root, "repos", "library")
	git(t, root, "init", "--bare", remote)
	git(t, root, "clone", remote, checkout)
	git(t, checkout, "config", "user.name", "Workbench Test")
	git(t, checkout, "config", "user.email", "workbench@example.invalid")
	mustWrite(t, filepath.Join(checkout, "source.txt"), "recoverable\n")
	git(t, checkout, "add", "source.txt")
	git(t, checkout, "commit", "-m", "fixture")
	git(t, checkout, "branch", "-M", "main")
	git(t, checkout, "push", "-u", "origin", "main")

	candidate := repositoryCandidate(root, "phosphorco/library")
	before := captureGitState(t, checkout, remote)
	removed := false
	observer := func(subject orphan.Candidate) (orphan.Observation, error) {
		if removed {
			return orphan.Observation{Exists: false}, nil
		}
		return orphan.Observation{
			Exists:         true,
			OriginCount:    1,
			OriginGitHub:   subject.GitHub,
			Branch:         git(t, checkout, "branch", "--show-current"),
			Head:           git(t, checkout, "rev-parse", "HEAD"),
			UpstreamBranch: "origin/main",
			UpstreamHead:   git(t, checkout, "rev-parse", "origin/main"),
			RemoteHead:     remoteHead(t, checkout, "main"),
			Status:         git(t, checkout, "status", "--porcelain=v1", "--untracked-files=all"),
			Disposable:     true,
		}, nil
	}
	plan, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{candidate}}, observer)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := orphan.Apply(plan, observer, func(path string) error {
		if path != checkout {
			return errors.New("unexpected deletion target")
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		removed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(receipt.RemovedPaths, []string{checkout}) {
		t.Fatalf("receipt = %#v", receipt)
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("checkout still exists or stat failed unexpectedly: %v", err)
	}
	after := captureRemoteState(t, remote)
	if before.RemoteHead != after.RemoteHead {
		t.Fatalf("recoverable remote changed: before %#v after %#v", before, after)
	}
	if before.Status != "" || before.Head != before.UpstreamHead || before.IndexTree == "" || before.Content != "recoverable\n" {
		t.Fatalf("pre-delete proof was incomplete: %#v", before)
	}
}

func TestDirtyRefusalPreservesFilesystemAndExactGitState(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "recoverable.git")
	checkout := filepath.Join(root, "repos", "library")
	git(t, root, "init", "--bare", remote)
	git(t, root, "clone", remote, checkout)
	git(t, checkout, "config", "user.name", "Workbench Test")
	git(t, checkout, "config", "user.email", "workbench@example.invalid")
	mustWrite(t, filepath.Join(checkout, "source.txt"), "recoverable\n")
	git(t, checkout, "add", "source.txt")
	git(t, checkout, "commit", "-m", "fixture")
	git(t, checkout, "branch", "-M", "main")
	git(t, checkout, "push", "-u", "origin", "main")
	mustWrite(t, filepath.Join(checkout, "source.txt"), "dirty but precious\n")

	candidate := repositoryCandidate(root, "phosphorco/library")
	before := captureGitState(t, checkout, remote)
	observer := func(subject orphan.Candidate) (orphan.Observation, error) {
		return orphan.Observation{
			Exists:         true,
			OriginCount:    1,
			OriginGitHub:   subject.GitHub,
			Branch:         git(t, checkout, "branch", "--show-current"),
			Head:           git(t, checkout, "rev-parse", "HEAD"),
			UpstreamBranch: "origin/main",
			UpstreamHead:   git(t, checkout, "rev-parse", "origin/main"),
			RemoteHead:     git(t, checkout, "rev-parse", "origin/main"),
			Status:         git(t, checkout, "status", "--porcelain=v1", "--untracked-files=all"),
			Disposable:     true,
		}, nil
	}
	if _, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{candidate}}, observer); err == nil {
		t.Fatal("dirty checkout received deletion authorization")
	}
	after := captureGitState(t, checkout, remote)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("dirty refusal mutated checkout or remote:\nbefore %#v\nafter  %#v", before, after)
	}
	if !before.Exists || before.Status == "" || before.Content != "dirty but precious\n" {
		t.Fatalf("refusal witness did not capture dirty source state: %#v", before)
	}
}

const (
	testCommit  = "0123456789abcdef0123456789abcdef01234567"
	otherCommit = "89abcdef0123456789abcdef0123456789abcdef"
)

func repositoryCandidate(root, github string) orphan.Candidate {
	name := github[strings.LastIndex(github, "/")+1:]
	return orphan.Candidate{
		Identity:      github,
		GitHub:        github,
		Shape:         contract.ResourceShape{Kind: contract.RepositoryShape},
		CanonicalPath: "repos/" + name,
		Path:          filepath.Join(root, "repos", name),
	}
}

func safeObservation(candidate orphan.Candidate) orphan.Observation {
	return orphan.Observation{
		Exists:         true,
		OriginCount:    1,
		OriginGitHub:   candidate.GitHub,
		Branch:         "main",
		Head:           testCommit,
		UpstreamBranch: "origin/main",
		UpstreamHead:   testCommit,
		RemoteHead:     testCommit,
		Disposable:     true,
	}
}

func fixedObserver(states map[string]orphan.Observation) orphan.Observe {
	return func(candidate orphan.Candidate) (orphan.Observation, error) {
		return states[candidate.Path], nil
	}
}

type gitState struct {
	Exists       bool
	Origin       string
	Head         string
	Refs         string
	UpstreamHead string
	IndexTree    string
	Status       string
	Content      string
	RemoteHead   string
}

func captureGitState(t *testing.T, checkout, remote string) gitState {
	t.Helper()
	return gitState{
		Exists:       true,
		Origin:       git(t, checkout, "remote", "get-url", "origin"),
		Head:         git(t, checkout, "rev-parse", "HEAD"),
		Refs:         git(t, checkout, "show-ref", "--head"),
		UpstreamHead: git(t, checkout, "rev-parse", "origin/main"),
		IndexTree:    git(t, checkout, "write-tree"),
		Status:       git(t, checkout, "status", "--porcelain=v1", "--untracked-files=all"),
		Content:      mustRead(t, filepath.Join(checkout, "source.txt")),
		RemoteHead:   git(t, remote, "rev-parse", "refs/heads/main"),
	}
}

func captureRemoteState(t *testing.T, remote string) gitState {
	t.Helper()
	return gitState{RemoteHead: git(t, remote, "rev-parse", "refs/heads/main")}
}

func remoteHead(t *testing.T, checkout, branch string) string {
	t.Helper()
	head := observeRemoteHead(t, checkout, branch)
	if head == "" {
		t.Fatalf("remote branch %q is absent", branch)
	}
	return head
}

func observeRemoteHead(t *testing.T, checkout, branch string) string {
	t.Helper()
	command := exec.Command("git", "ls-remote", "--refs", "origin", "refs/heads/"+branch)
	command.Dir = checkout
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("observe remote head: %v\n%s", err, output)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return ""
	}
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch {
		t.Fatalf("ambiguous remote observation: %q", output)
	}
	return fields[0]
}

func git(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
