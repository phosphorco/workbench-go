package snapshot_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/snapshot"
)

const (
	entryCommit   = "0123456789abcdef0123456789abcdef01234567"
	libraryCommit = "89abcdef0123456789abcdef0123456789abcdef"
)

func TestRecordAndPlanExactWorldWithoutBranchAuthority(t *testing.T) {
	recorded, err := snapshot.Record([]snapshot.Resource{
		{Identity: "@workbench-entry", Shape: contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"}, GitHub: "phosphorco/workbench-fixture-entry", CanonicalPath: "pkg/@workbench-entry", Commit: entryCommit},
		{Identity: "phosphorco/workbench-fixture-library", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, GitHub: "phosphorco/workbench-fixture-library", CanonicalPath: "repos/workbench-fixture-library", Commit: libraryCommit},
	})
	if err != nil {
		t.Fatal(err)
	}

	observer := &memoryObserver{checkouts: map[string]snapshot.Checkout{
		"pkg/@workbench-entry": {Exists: true, GitHub: "phosphorco/workbench-fixture-entry", Identity: "@workbench-entry", Commit: entryCommit, Clean: true},
	}}
	plan, err := snapshot.Plan(recorded, observer)
	if err != nil {
		t.Fatal(err)
	}
	if acquire, verified := plan.Counts(); acquire != 1 || verified != 1 {
		t.Fatalf("plan counts = (%d, %d), want (1, 1)", acquire, verified)
	}
	if len(observer.observed) != 2 {
		t.Fatalf("observed paths = %#v", observer.observed)
	}
}

func TestPlanRefusesEveryExistingCheckoutConflictWithoutMutation(t *testing.T) {
	recorded, err := snapshot.Record([]snapshot.Resource{{
		Identity: "phosphorco/library", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, GitHub: "phosphorco/library", CanonicalPath: "repos/library", Commit: libraryCommit,
	}})
	if err != nil {
		t.Fatal(err)
	}

	tests := []snapshot.Checkout{
		{Exists: true, GitHub: "someone/else", Identity: "someone/else", Commit: libraryCommit, Clean: true},
		{Exists: true, GitHub: "phosphorco/library", Identity: "someone/else", Commit: libraryCommit, Clean: true},
		{Exists: true, GitHub: "phosphorco/library", Identity: "phosphorco/library", Commit: entryCommit, Clean: true},
		{Exists: true, GitHub: "phosphorco/library", Identity: "phosphorco/library", Commit: libraryCommit, Clean: false},
	}
	for _, checkout := range tests {
		observer := &memoryObserver{checkouts: map[string]snapshot.Checkout{"repos/library": checkout}}
		before := cloneCheckouts(observer.checkouts)
		if _, err := snapshot.Plan(recorded, observer); err == nil {
			t.Fatalf("checkout %#v did not conflict", checkout)
		}
		if !reflect.DeepEqual(observer.checkouts, before) {
			t.Fatalf("checkout mutated: got %#v want %#v", observer.checkouts, before)
		}
	}
}

func TestApplyRetainsRecoverableExactProgress(t *testing.T) {
	recorded := mustRecord(t,
		snapshot.Resource{Identity: "example/first", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, GitHub: "example/first", CanonicalPath: "repos/first", Commit: entryCommit},
		snapshot.Resource{Identity: "example/second", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, GitHub: "example/second", CanonicalPath: "repos/second", Commit: libraryCommit},
	)
	observer := &memoryObserver{checkouts: map[string]snapshot.Checkout{}}
	plan, err := snapshot.Plan(recorded, observer)
	if err != nil {
		t.Fatal(err)
	}
	acquirer := &memoryAcquirer{observer: observer, failAt: "example/second"}
	err = snapshot.Apply(plan, acquirer)
	if err == nil {
		t.Fatal("partial acquisition succeeded")
	}
	if got, want := acquirer.acquired, []string{"example/first"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recoverable progress = %#v, want %#v", got, want)
	}
}

func TestApplyRefusesAppearanceBeforeAnyAcquisition(t *testing.T) {
	recorded := mustRecord(t,
		snapshot.Resource{Identity: "example/first", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, GitHub: "example/first", CanonicalPath: "repos/first", Commit: entryCommit},
		snapshot.Resource{Identity: "example/second", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, GitHub: "example/second", CanonicalPath: "repos/second", Commit: libraryCommit},
	)
	observer := &memoryObserver{checkouts: map[string]snapshot.Checkout{}}
	plan, err := snapshot.Plan(recorded, observer)
	if err != nil {
		t.Fatal(err)
	}
	observer.checkouts["repos/second"] = exactCheckout("example/second", libraryCommit)
	acquirer := &memoryAcquirer{observer: observer}
	if err := snapshot.Apply(plan, acquirer); err == nil {
		t.Fatal("destination appearance was accepted")
	}
	if len(acquirer.acquired) != 0 {
		t.Fatalf("acquired after preflight conflict: %v", acquirer.acquired)
	}
}

func TestApplyRejectsUnplannedAndPostAcquisitionState(t *testing.T) {
	var unplanned snapshot.Reproduction
	if err := snapshot.Apply(unplanned, &memoryAcquirer{}); err == nil {
		t.Fatal("caller-forged zero reproduction was accepted")
	}

	recorded := mustRecord(t, snapshot.Resource{Identity: "example/first", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, GitHub: "example/first", CanonicalPath: "repos/first", Commit: entryCommit})
	observer := &memoryObserver{checkouts: map[string]snapshot.Checkout{}}
	plan, err := snapshot.Plan(recorded, observer)
	if err != nil {
		t.Fatal(err)
	}
	acquirer := &memoryAcquirer{observer: observer, wrongOrigin: true}
	if err := snapshot.Apply(plan, acquirer); err == nil {
		t.Fatal("wrong post-acquisition origin was accepted")
	}
}

func TestRealGitOriginConflictAndAppearanceRaceRefuseAcquisition(t *testing.T) {
	t.Run("origin conflict", func(t *testing.T) {
		root := t.TempDir()
		checkout := initGitCheckout(t, root, "repos/library", "someone/else")
		commit := gitOutput(t, checkout, "rev-parse", "HEAD")
		recorded := mustRecord(t, snapshot.Resource{Identity: "phosphorco/library", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, GitHub: "phosphorco/library", CanonicalPath: "repos/library", Commit: commit})
		if _, err := snapshot.Plan(recorded, gitObserver{root: root}); err == nil {
			t.Fatal("real checkout with conflicting origin was accepted")
		}
	})

	t.Run("appearance after plan", func(t *testing.T) {
		root := t.TempDir()
		recorded := mustRecord(t, snapshot.Resource{Identity: "phosphorco/library", Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, GitHub: "phosphorco/library", CanonicalPath: "repos/library", Commit: libraryCommit})
		observer := gitObserver{root: root}
		plan, err := snapshot.Plan(recorded, observer)
		if err != nil {
			t.Fatal(err)
		}
		initGitCheckout(t, root, "repos/library", "phosphorco/library")
		acquirer := &countingAcquirer{}
		if err := snapshot.Apply(plan, acquirer); err == nil {
			t.Fatal("real checkout appearance after planning was accepted")
		}
		if acquirer.calls != 0 {
			t.Fatalf("acquirer called %d times after appearance race", acquirer.calls)
		}
	})
}

type memoryObserver struct {
	checkouts map[string]snapshot.Checkout
	observed  []string
}

func (observer *memoryObserver) Observe(path string) (snapshot.Checkout, error) {
	observer.observed = append(observer.observed, path)
	return observer.checkouts[path], nil
}

type memoryAcquirer struct {
	observer    *memoryObserver
	failAt      string
	wrongOrigin bool
	acquired    []string
}

func (acquirer *memoryAcquirer) CreateExactIfAbsent(acquisition snapshot.Acquisition) error {
	if acquisition.Identity == acquirer.failAt {
		return errors.New("remote unavailable")
	}
	if _, exists := acquirer.observer.checkouts[acquisition.CanonicalPath]; exists {
		return errors.New("destination exists")
	}
	acquirer.acquired = append(acquirer.acquired, acquisition.Identity)
	checkout := exactCheckout(acquisition.GitHub, acquisition.Commit)
	checkout.Identity = acquisition.Identity
	if acquirer.wrongOrigin {
		checkout.GitHub = "someone/else"
	}
	acquirer.observer.checkouts[acquisition.CanonicalPath] = checkout
	return nil
}

func exactCheckout(github, commit string) snapshot.Checkout {
	return snapshot.Checkout{Exists: true, GitHub: github, Identity: github, Commit: commit, Clean: true}
}

func mustRecord(t *testing.T, resources ...snapshot.Resource) contract.WorkbenchWorldSnapshot {
	t.Helper()
	recorded, err := snapshot.Record(resources)
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}

type countingAcquirer struct {
	calls int
}

func (acquirer *countingAcquirer) CreateExactIfAbsent(snapshot.Acquisition) error {
	acquirer.calls++
	return nil
}

type gitObserver struct {
	root string
}

func (observer gitObserver) Observe(canonicalPath string) (snapshot.Checkout, error) {
	path := filepath.Join(observer.root, filepath.FromSlash(canonicalPath))
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return snapshot.Checkout{}, nil
	} else if err != nil {
		return snapshot.Checkout{}, err
	}
	origin, err := gitCommand(path, "config", "--get", "remote.origin.url")
	if err != nil {
		return snapshot.Checkout{}, err
	}
	github := strings.TrimSuffix(strings.TrimPrefix(origin, "https://github.com/"), ".git")
	head, err := gitCommand(path, "rev-parse", "HEAD")
	if err != nil {
		return snapshot.Checkout{}, err
	}
	status, err := gitCommand(path, "status", "--porcelain=v2", "--untracked-files=all")
	if err != nil {
		return snapshot.Checkout{}, err
	}
	return snapshot.Checkout{Exists: true, GitHub: github, Identity: github, Commit: head, Clean: status == ""}, nil
}

func initGitCheckout(t *testing.T, root, canonicalPath, github string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(canonicalPath))
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "Workbench Snapshot Test"},
		{"config", "user.email", "workbench-snapshot@example.invalid"},
		{"remote", "add", "origin", "https://github.com/" + github + ".git"},
	} {
		if _, err := gitCommand(path, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "source.txt"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "source.txt"}, {"commit", "-q", "-m", "fixture"}} {
		if _, err := gitCommand(path, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func gitOutput(t *testing.T, path string, arguments ...string) string {
	t.Helper()
	output, err := gitCommand(path, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func gitCommand(path string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = path
	output, err := command.CombinedOutput()
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func cloneCheckouts(source map[string]snapshot.Checkout) map[string]snapshot.Checkout {
	copy := make(map[string]snapshot.Checkout, len(source))
	for path, checkout := range source {
		copy[path] = checkout
	}
	return copy
}
