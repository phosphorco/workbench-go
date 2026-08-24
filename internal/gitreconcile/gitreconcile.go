package gitreconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Checkout is the Git state requested for one canonical repository placement.
type Checkout struct {
	Path       string
	RemoteURL  string
	Branch     string
	BaseBranch string
}

// Observation is the read-only Git state used to plan one checkout.
type Observation struct {
	Checkout      Checkout
	Exists        bool
	CurrentBranch string
	Dirty         bool
	LocalBranch   bool
	RemoteBranch  bool
	RemoteBase    bool
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
}

// ChangeSet is an opaque, deterministic sequence of Git effects produced by Plan.
// It can only be applied by this package.
type ChangeSet struct {
	operations []operation
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
			operations = append(operations, operation{kind: operationClone, checkout: checkout, fromRemoteBranch: observed.RemoteBranch})
			continue
		}

		if observed.Dirty && observed.CurrentBranch != checkout.Branch {
			return ChangeSet{}, fmt.Errorf("plan checkout %q: dirty checkout is on branch %q, not Subject branch %q", checkout.Path, observed.CurrentBranch, checkout.Branch)
		}

		if observed.CurrentBranch == checkout.Branch {
			if observed.RemoteBranch {
				operations = append(operations, operation{kind: operationFetchBranch, checkout: checkout})
			}
			continue
		}

		switch {
		case observed.LocalBranch:
			if observed.RemoteBranch {
				operations = append(operations, operation{kind: operationFetchBranch, checkout: checkout})
			}
			operations = append(operations, operation{kind: operationCheckoutLocal, checkout: checkout})
		case observed.RemoteBranch:
			operations = append(operations,
				operation{kind: operationFetchBranch, checkout: checkout},
				operation{kind: operationCheckoutRemote, checkout: checkout},
			)
		case observed.RemoteBase:
			operations = append(operations,
				operation{kind: operationFetchBase, checkout: checkout},
				operation{kind: operationCreateFromBase, checkout: checkout},
			)
		default:
			return ChangeSet{}, fmt.Errorf("plan checkout %q: neither subject branch %q nor base branch %q exists at %q", checkout.Path, checkout.Branch, checkout.BaseBranch, checkout.RemoteURL)
		}
	}
	return ChangeSet{operations: operations}, nil
}

// Apply performs exactly the Git effects represented by a planned ChangeSet.
// It never merges, rebases, resets, rewrites, or deletes a branch.
func Apply(ctx context.Context, changes ChangeSet) error {
	for _, operation := range changes.operations {
		if err := applyOperation(ctx, operation); err != nil {
			return err
		}
	}
	return nil
}

// Reconcile observes all checkouts, derives one pure ChangeSet, and only then
// applies it. An unsafe existing checkout prevents every checkout mutation.
func Reconcile(ctx context.Context, desired []Checkout) error {
	observations, err := Observe(ctx, desired)
	if err != nil {
		return err
	}
	changes, err := Plan(observations)
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
	remoteBranch, remoteBase, err := observeRemoteBranches(ctx, checkout)
	if err != nil {
		return Observation{}, err
	}
	observed := Observation{
		Checkout:     checkout,
		RemoteBranch: remoteBranch,
		RemoteBase:   remoteBase,
	}

	info, err := os.Stat(checkout.Path)
	if errors.Is(err, os.ErrNotExist) {
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
	status, err := runGit(ctx, checkout.Path, "status", "--porcelain=v1", "--untracked-files=all", "-z")
	if err != nil {
		return Observation{}, fmt.Errorf("read worktree status: %w", err)
	}
	observed.Dirty = len(status) != 0
	observed.LocalBranch, err = localBranchExists(ctx, checkout.Path, checkout.Branch)
	if err != nil {
		return Observation{}, fmt.Errorf("inspect local Subject branch: %w", err)
	}
	return observed, nil
}

func observeRemoteBranches(ctx context.Context, checkout Checkout) (bool, bool, error) {
	output, err := runGit(ctx, "", "ls-remote", "--heads", checkout.RemoteURL,
		"refs/heads/"+checkout.Branch,
		"refs/heads/"+checkout.BaseBranch,
	)
	if err != nil {
		return false, false, fmt.Errorf("inspect remote %q: %w", checkout.RemoteURL, err)
	}
	var subject, base bool
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[1] {
		case "refs/heads/" + checkout.Branch:
			subject = true
		case "refs/heads/" + checkout.BaseBranch:
			base = true
		}
	}
	return subject, base, nil
}

func localBranchExists(ctx context.Context, directory string, branch string) (bool, error) {
	_, err := runGit(ctx, directory, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var commandError *gitCommandError
	if errors.As(err, &commandError) && commandError.ExitCode == 1 {
		return false, nil
	}
	return false, err
}

func applyOperation(ctx context.Context, operation operation) error {
	checkout := operation.checkout
	var directory string
	var arguments []string
	switch operation.kind {
	case operationClone:
		arguments = []string{"clone", "--no-checkout", "--origin", "origin", "--", checkout.RemoteURL, checkout.Path}
		if _, err := runGit(ctx, "", arguments...); err != nil {
			return fmt.Errorf("clone %q into %q: %w", checkout.RemoteURL, checkout.Path, err)
		}
		if _, err := runGit(ctx, checkout.Path, "config", "remote.origin.url", checkout.RemoteURL); err != nil {
			return fmt.Errorf("record canonical origin %q in %q: %w", checkout.RemoteURL, checkout.Path, err)
		}
		if _, err := runGit(ctx, checkout.Path, cloneCheckoutArguments(checkout, operation.fromRemoteBranch)...); err != nil {
			return fmt.Errorf("place cloned checkout %q on Subject branch %q: %w", checkout.Path, checkout.Branch, err)
		}
		return nil
	case operationFetchBranch:
		directory = checkout.Path
		arguments = []string{"fetch", "--no-tags", "origin", "refs/heads/" + checkout.Branch + ":refs/remotes/origin/" + checkout.Branch}
	case operationFetchBase:
		directory = checkout.Path
		arguments = []string{"fetch", "--no-tags", "origin", "refs/heads/" + checkout.BaseBranch + ":refs/remotes/origin/" + checkout.BaseBranch}
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
