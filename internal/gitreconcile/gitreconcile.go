package gitreconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
)

// Checkout is the Git state requested for one canonical repository placement.
type Checkout struct {
	Path       string
	RemoteURL  string
	Branch     string
	BaseBranch string
	// ExpectedCommit, when present, binds preparation to a revision already
	// validated by the caller. A different selected local or remote revision is
	// refused before any checkout mutation.
	ExpectedCommit string
}

// Observation is the read-only Git state used to plan one checkout.
type Observation struct {
	Checkout      Checkout
	Exists        bool
	CurrentBranch string
	CurrentCommit string
	CurrentStatus string
	Dirty         bool
	LocalBranch   bool
	LocalCommit   string
	RemoteBranch  bool
	RemoteCommit  string
	RemoteBase    bool
	BaseCommit    string
	// Commit is the exact revision that the prepared reconciliation will place
	// at the checkout. For an already-current Subject branch it is the local
	// HEAD; otherwise it is the selected local or observed remote revision.
	Commit string
}

// PreparedCheckout is the immutable revision fact exposed by a prepared
// reconciliation. Consumers may validate content at Commit before Apply;
// Apply refuses if any observed local or remote predicate has changed.
type PreparedCheckout struct {
	Checkout Checkout
	Commit   string
}

type operationKind uint8

const (
	operationClone operationKind = iota
	operationFetchBranch
	operationFetchBase
	operationCheckoutLocal
	operationCheckoutRemote
	operationCreateFromBase
)

type operation struct {
	kind             operationKind
	checkout         Checkout
	fromRemoteBranch bool
	commit           string
}

// ChangeSet is an opaque, deterministic sequence of Git effects produced by Plan.
// It can only be applied by this package.
type ChangeSet struct {
	observations []Observation
	operations   []operation
}

// Prepared returns a defensive copy of the exact checkout revisions bound by
// this ChangeSet.
func (changes ChangeSet) Prepared() []PreparedCheckout {
	prepared := make([]PreparedCheckout, 0, len(changes.observations))
	for _, observation := range changes.observations {
		prepared = append(prepared, PreparedCheckout{Checkout: observation.Checkout, Commit: observation.Commit})
	}
	return prepared
}

// Observe reads every desired checkout and its remote without changing local Git
// state. All existing checkouts are therefore preflighted before Apply can run.
func Observe(ctx context.Context, desired []Checkout) ([]Observation, error) {
	checkouts, err := validateDesired(ctx, desired)
	if err != nil {
		return nil, err
	}

	observations := make([]Observation, 0, len(checkouts))
	for _, checkout := range checkouts {
		observation, err := observeCheckout(ctx, checkout)
		if err != nil {
			return nil, fmt.Errorf("observe checkout %q: %w", checkout.Path, err)
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

// Plan derives the Git effects required to put every observed checkout on its
// Subject branch. It performs no I/O.
func Plan(observations []Observation) (ChangeSet, error) {
	operations := make([]operation, 0, len(observations)*2)
	for _, observed := range observations {
		checkout := observed.Checkout
		if !observed.Exists {
			if !observed.RemoteBranch && !observed.RemoteBase {
				return ChangeSet{}, fmt.Errorf("plan checkout %q: neither subject branch %q nor base branch %q exists at %q", checkout.Path, checkout.Branch, checkout.BaseBranch, checkout.RemoteURL)
			}
			operations = append(operations, operation{kind: operationClone, checkout: checkout, fromRemoteBranch: observed.RemoteBranch, commit: observed.Commit})
			continue
		}

		if observed.Dirty && observed.CurrentBranch != checkout.Branch {
			return ChangeSet{}, fmt.Errorf("plan checkout %q: dirty checkout is on branch %q, not Subject branch %q", checkout.Path, observed.CurrentBranch, checkout.Branch)
		}

		if observed.CurrentBranch == checkout.Branch {
			if observed.RemoteBranch {
				operations = append(operations, operation{kind: operationFetchBranch, checkout: checkout, commit: observed.RemoteCommit})
			}
			continue
		}

		switch {
		case observed.LocalBranch:
			if observed.RemoteBranch {
				operations = append(operations, operation{kind: operationFetchBranch, checkout: checkout, commit: observed.RemoteCommit})
			}
			operations = append(operations, operation{kind: operationCheckoutLocal, checkout: checkout})
		case observed.RemoteBranch:
			operations = append(operations,
				operation{kind: operationFetchBranch, checkout: checkout, commit: observed.RemoteCommit},
				operation{kind: operationCheckoutRemote, checkout: checkout, commit: observed.Commit},
			)
		case observed.RemoteBase:
			operations = append(operations,
				operation{kind: operationFetchBase, checkout: checkout, commit: observed.BaseCommit},
				operation{kind: operationCreateFromBase, checkout: checkout, commit: observed.Commit},
			)
		default:
			return ChangeSet{}, fmt.Errorf("plan checkout %q: neither subject branch %q nor base branch %q exists at %q", checkout.Path, checkout.Branch, checkout.BaseBranch, checkout.RemoteURL)
		}
	}
	return ChangeSet{observations: append([]Observation(nil), observations...), operations: operations}, nil
}

// Apply performs exactly the Git effects represented by a planned ChangeSet.
// It never merges, rebases, resets, rewrites, or deletes a branch.
func Apply(ctx context.Context, changes ChangeSet) error {
	if len(changes.observations) == 0 && len(changes.operations) != 0 {
		return fmt.Errorf("apply unprepared Git changes")
	}
	for _, expected := range changes.observations {
		actual, err := observeCheckout(ctx, expected.Checkout)
		if err != nil {
			return fmt.Errorf("reobserve checkout %q before apply: %w", expected.Checkout.Path, err)
		}
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("checkout %q or its remote changed after reconciliation was prepared", expected.Checkout.Path)
		}
	}
	for _, operation := range changes.operations {
		if err := applyOperation(ctx, operation); err != nil {
			return err
		}
	}
	return nil
}

// Prepare observes and plans one revision-bound reconciliation without
// changing local Git state.
func Prepare(ctx context.Context, desired []Checkout) (ChangeSet, error) {
	observations, err := Observe(ctx, desired)
	if err != nil {
		return ChangeSet{}, err
	}
	return Plan(observations)
}

// Reconcile observes all checkouts, derives one pure ChangeSet, and only then
// applies it. An unsafe existing checkout prevents every checkout mutation.
func Reconcile(ctx context.Context, desired []Checkout) error {
	changes, err := Prepare(ctx, desired)
	if err != nil {
		return err
	}
	return Apply(ctx, changes)
}

func validateDesired(ctx context.Context, desired []Checkout) ([]Checkout, error) {
	result := make([]Checkout, 0, len(desired))
	paths := make(map[string]struct{}, len(desired))
	for index, checkout := range desired {
		if strings.TrimSpace(checkout.Path) == "" {
			return nil, fmt.Errorf("checkout %d: path is empty", index)
		}
		if strings.TrimSpace(checkout.RemoteURL) == "" {
			return nil, fmt.Errorf("checkout %q: remote URL is empty", checkout.Path)
		}
		absolute, err := filepath.Abs(checkout.Path)
		if err != nil {
			return nil, fmt.Errorf("checkout %q: resolve canonical path: %w", checkout.Path, err)
		}
		checkout.Path = filepath.Clean(absolute)
		if _, duplicate := paths[checkout.Path]; duplicate {
			return nil, fmt.Errorf("checkout %q: duplicate canonical path", checkout.Path)
		}
		paths[checkout.Path] = struct{}{}

		if err := checkBranchName(ctx, checkout.Branch); err != nil {
			return nil, fmt.Errorf("checkout %q: invalid Subject branch %q: %w", checkout.Path, checkout.Branch, err)
		}
		if err := checkBranchName(ctx, checkout.BaseBranch); err != nil {
			return nil, fmt.Errorf("checkout %q: invalid base branch %q: %w", checkout.Path, checkout.BaseBranch, err)
		}
		if checkout.ExpectedCommit != "" && !isFullObjectID(checkout.ExpectedCommit) {
			return nil, fmt.Errorf("checkout %q: expected commit %q is not a full object ID", checkout.Path, checkout.ExpectedCommit)
		}
		result = append(result, checkout)
	}
	return result, nil
}

func checkBranchName(ctx context.Context, branch string) error {
	if strings.TrimSpace(branch) == "" {
		return errors.New("branch is empty")
	}
	_, err := runGit(ctx, "", "check-ref-format", "--branch", branch)
	return err
}

func observeCheckout(ctx context.Context, checkout Checkout) (Observation, error) {
	remoteCommit, baseCommit, err := observeRemoteBranches(ctx, checkout)
	if err != nil {
		return Observation{}, err
	}
	observed := Observation{
		Checkout:     checkout,
		RemoteBranch: remoteCommit != "",
		RemoteCommit: remoteCommit,
		RemoteBase:   baseCommit != "",
		BaseCommit:   baseCommit,
	}

	info, err := os.Stat(checkout.Path)
	if errors.Is(err, os.ErrNotExist) {
		if observed.RemoteBranch {
			observed.Commit = observed.RemoteCommit
		} else {
			observed.Commit = observed.BaseCommit
		}
		if checkout.ExpectedCommit != "" && observed.Commit != checkout.ExpectedCommit {
			return Observation{}, fmt.Errorf("selected commit is %q, want previously validated commit %q", observed.Commit, checkout.ExpectedCommit)
		}
		return observed, nil
	}
	if err != nil {
		return Observation{}, fmt.Errorf("stat target: %w", err)
	}
	if !info.IsDir() {
		return Observation{}, fmt.Errorf("target exists and is not a directory")
	}
	observed.Exists = true

	topLevel, err := runGit(ctx, checkout.Path, "rev-parse", "--show-toplevel")
	if err != nil {
		return Observation{}, fmt.Errorf("identify Git worktree: %w", err)
	}
	actualRoot, err := filepath.Abs(strings.TrimSpace(topLevel))
	if err != nil {
		return Observation{}, fmt.Errorf("resolve Git worktree root: %w", err)
	}
	actualRoot = filepath.Clean(actualRoot)
	if actualRoot != checkout.Path {
		return Observation{}, fmt.Errorf("target is inside Git worktree %q rather than its canonical root", actualRoot)
	}

	origin, err := runGit(ctx, checkout.Path, "config", "--get", "remote.origin.url")
	if err != nil {
		return Observation{}, fmt.Errorf("read origin remote: %w", err)
	}
	if strings.TrimSpace(origin) != checkout.RemoteURL {
		return Observation{}, fmt.Errorf("origin remote is %q, want canonical remote %q", strings.TrimSpace(origin), checkout.RemoteURL)
	}

	branch, err := runGit(ctx, checkout.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return Observation{}, fmt.Errorf("read current branch (detached HEAD is unsafe): %w", err)
	}
	observed.CurrentBranch = strings.TrimSpace(branch)
	head, err := runGit(ctx, checkout.Path, "rev-parse", "HEAD")
	if err != nil {
		return Observation{}, fmt.Errorf("read current commit: %w", err)
	}
	observed.CurrentCommit = strings.TrimSpace(head)
	status, err := runGit(ctx, checkout.Path, "status", "--porcelain=v1", "--untracked-files=all", "-z")
	if err != nil {
		return Observation{}, fmt.Errorf("read worktree status: %w", err)
	}
	observed.CurrentStatus = status
	observed.Dirty = len(status) != 0
	observed.LocalCommit, err = localBranchCommit(ctx, checkout.Path, checkout.Branch)
	if err != nil {
		return Observation{}, fmt.Errorf("inspect local Subject branch: %w", err)
	}
	observed.LocalBranch = observed.LocalCommit != ""
	switch {
	case observed.CurrentBranch == checkout.Branch:
		observed.Commit = observed.CurrentCommit
	case observed.LocalBranch:
		observed.Commit = observed.LocalCommit
	case observed.RemoteBranch:
		observed.Commit = observed.RemoteCommit
	default:
		observed.Commit = observed.BaseCommit
	}
	if checkout.ExpectedCommit != "" && observed.Commit != checkout.ExpectedCommit {
		return Observation{}, fmt.Errorf("selected commit is %q, want previously validated commit %q", observed.Commit, checkout.ExpectedCommit)
	}
	return observed, nil
}

func isFullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func observeRemoteBranches(ctx context.Context, checkout Checkout) (string, string, error) {
	output, err := runGit(ctx, "", "ls-remote", "--heads", checkout.RemoteURL,
		"refs/heads/"+checkout.Branch,
		"refs/heads/"+checkout.BaseBranch,
	)
	if err != nil {
		return "", "", fmt.Errorf("inspect remote %q: %w", checkout.RemoteURL, err)
	}
	var subject, base string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[1] {
		case "refs/heads/" + checkout.Branch:
			subject = fields[0]
		case "refs/heads/" + checkout.BaseBranch:
			base = fields[0]
		}
	}
	return subject, base, nil
}

func localBranchCommit(ctx context.Context, directory string, branch string) (string, error) {
	output, err := runGit(ctx, directory, "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(output)
	if commit == "" {
		return "", nil
	}
	if strings.Contains(commit, "\n") {
		return "", fmt.Errorf("local branch %q resolved more than once", branch)
	}
	return commit, nil
}

func applyOperation(ctx context.Context, operation operation) error {
	checkout := operation.checkout
	var directory string
	var arguments []string
	switch operation.kind {
	case operationClone:
		if err := os.MkdirAll(filepath.Dir(checkout.Path), 0o755); err != nil {
			return fmt.Errorf("create checkout parent for %q: %w", checkout.Path, err)
		}
		arguments = []string{"clone", "--no-checkout", "--origin", "origin", "--", checkout.RemoteURL, checkout.Path}
		if _, err := runGit(ctx, "", arguments...); err != nil {
			return fmt.Errorf("clone %q into %q: %w", checkout.RemoteURL, checkout.Path, err)
		}
		if _, err := runGit(ctx, checkout.Path, "config", "remote.origin.url", checkout.RemoteURL); err != nil {
			return fmt.Errorf("record canonical origin %q in %q: %w", checkout.RemoteURL, checkout.Path, err)
		}
		remoteRef := "refs/remotes/origin/" + checkout.BaseBranch
		if operation.fromRemoteBranch {
			remoteRef = "refs/remotes/origin/" + checkout.Branch
		}
		if _, err := runGit(ctx, checkout.Path, "fetch", "--no-tags", "origin", "+"+operation.commit+":"+remoteRef); err != nil {
			return fmt.Errorf("fetch prepared commit %q for cloned checkout %q: %w", operation.commit, checkout.Path, err)
		}
		if _, err := runGit(ctx, checkout.Path, cloneCheckoutArguments(checkout, operation.fromRemoteBranch)...); err != nil {
			return fmt.Errorf("place cloned checkout %q on Subject branch %q: %w", checkout.Path, checkout.Branch, err)
		}
		return nil
	case operationFetchBranch:
		directory = checkout.Path
		arguments = []string{"fetch", "--no-tags", "origin", "+" + operation.commit + ":refs/remotes/origin/" + checkout.Branch}
	case operationFetchBase:
		directory = checkout.Path
		arguments = []string{"fetch", "--no-tags", "origin", "+" + operation.commit + ":refs/remotes/origin/" + checkout.BaseBranch}
	case operationCheckoutLocal:
		directory = checkout.Path
		arguments = []string{"checkout", checkout.Branch}
	case operationCheckoutRemote:
		directory = checkout.Path
		arguments = []string{"checkout", "-b", checkout.Branch, "--track", "origin/" + checkout.Branch}
	case operationCreateFromBase:
		directory = checkout.Path
		arguments = []string{"checkout", "-b", checkout.Branch, "origin/" + checkout.BaseBranch}
	default:
		return fmt.Errorf("apply checkout %q: unknown operation %d", checkout.Path, operation.kind)
	}
	if _, err := runGit(ctx, directory, arguments...); err != nil {
		return fmt.Errorf("reconcile checkout %q with git %s: %w", checkout.Path, strings.Join(arguments, " "), err)
	}
	return nil
}

func cloneCheckoutArguments(checkout Checkout, fromRemoteBranch bool) []string {
	if fromRemoteBranch {
		return []string{"checkout", "-b", checkout.Branch, "--track", "origin/" + checkout.Branch}
	}
	return []string{"checkout", "-b", checkout.Branch, "origin/" + checkout.BaseBranch}
}

type gitCommandError struct {
	ExitCode int
	Output   string
}

func (failure *gitCommandError) Error() string {
	if failure.Output == "" {
		return fmt.Sprintf("git exited with status %d", failure.ExitCode)
	}
	return fmt.Sprintf("git exited with status %d: %s", failure.ExitCode, failure.Output)
}

func runGit(ctx context.Context, directory string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return "", &gitCommandError{ExitCode: exitError.ExitCode(), Output: strings.TrimSpace(string(output))}
	}
	return "", fmt.Errorf("start git %s: %w", strings.Join(arguments, " "), err)
}
