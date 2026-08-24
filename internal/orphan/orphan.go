// Package orphan reports checkouts outside the current World and gates their
// explicit removal behind recoverability proof.
package orphan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
)

var commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type Resource struct {
	Identity      string
	GitHub        string
	Shape         contract.ResourceShape
	CanonicalPath string
}

type Candidate struct {
	Identity      string
	GitHub        string
	Shape         contract.ResourceShape
	CanonicalPath string
	Path          string
}

type ReportResult struct {
	Orphans []Candidate
}

// Report returns present checkouts that are not members of the current World.
// It receives no deletion capability and cannot mutate a checkout.
func Report(world []Resource, present []Candidate) (ReportResult, error) {
	members := make(map[string]struct{}, len(world))
	canonicalPaths := make(map[string]string, len(world))
	for index, resource := range world {
		if err := validateResource(resource); err != nil {
			return ReportResult{}, fmt.Errorf("World resource %d: %w", index, err)
		}
		if _, exists := members[resource.Identity]; exists {
			return ReportResult{}, fmt.Errorf("World identity %q is ambiguous", resource.Identity)
		}
		if identity, exists := canonicalPaths[resource.CanonicalPath]; exists {
			return ReportResult{}, fmt.Errorf("World canonical path %q is claimed by %q and %q", resource.CanonicalPath, identity, resource.Identity)
		}
		members[resource.Identity] = struct{}{}
		canonicalPaths[resource.CanonicalPath] = resource.Identity
	}

	seenIdentities := make(map[string]struct{}, len(present))
	seenPaths := make(map[string]struct{}, len(present))
	orphans := make([]Candidate, 0)
	for index, candidate := range present {
		if err := validateCandidate(candidate); err != nil {
			return ReportResult{}, fmt.Errorf("present checkout %d: %w", index, err)
		}
		if _, exists := seenIdentities[candidate.Identity]; exists {
			return ReportResult{}, fmt.Errorf("present checkout identity %q is ambiguous", candidate.Identity)
		}
		if _, exists := seenPaths[candidate.Path]; exists {
			return ReportResult{}, fmt.Errorf("present checkout path %q is ambiguous", candidate.Path)
		}
		seenIdentities[candidate.Identity] = struct{}{}
		seenPaths[candidate.Path] = struct{}{}
		if _, member := members[candidate.Identity]; !member {
			orphans = append(orphans, candidate)
		}
	}
	slices.SortFunc(orphans, func(left, right Candidate) int {
		if left.Identity < right.Identity {
			return -1
		}
		if left.Identity > right.Identity {
			return 1
		}
		return 0
	})
	return ReportResult{Orphans: orphans}, nil
}

type Observation struct {
	Exists         bool
	OriginCount    int
	OriginGitHub   string
	Branch         string
	Head           string
	UpstreamBranch string
	UpstreamHead   string
	// RemoteHead is the exact branch object observed independently from the
	// remote, never a local remote-tracking ref or reachability boolean.
	RemoteHead     string
	Status         string
	NestedCheckout bool
	Disposable     bool
}

type Observe func(Candidate) (Observation, error)
type Remove func(path string) error

type Request struct {
	Root       string
	World      []Resource
	Candidates []Candidate
}

type approvedCandidate struct {
	candidate   Candidate
	observation Observation
	root        string
	path        string
}

// Plan is an opaque deletion authorization minted only by Preflight.
type Plan struct {
	approved []approvedCandidate
}

// Preflight proves every requested candidate is an orphan, exactly located,
// clean, pushed, unambiguous, non-nested, disposable, and independently
// recoverable. One refusal prevents a Plan for the entire request.
func Preflight(request Request, observe Observe) (Plan, error) {
	if observe == nil {
		return Plan{}, fmt.Errorf("prune observer is absent")
	}
	if !filepath.IsAbs(request.Root) || filepath.Clean(request.Root) != request.Root {
		return Plan{}, fmt.Errorf("prune root %q is not an absolute clean path", request.Root)
	}
	resolvedRoot, err := canonicalDirectory(request.Root)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve prune root: %w", err)
	}
	if resolvedRoot != request.Root {
		return Plan{}, fmt.Errorf("prune root %q resolves through a symlink to %q", request.Root, resolvedRoot)
	}
	if len(request.Candidates) == 0 {
		return Plan{}, fmt.Errorf("prune request has no candidates")
	}

	report, err := Report(request.World, request.Candidates)
	if err != nil {
		return Plan{}, err
	}
	if len(report.Orphans) != len(request.Candidates) {
		return Plan{}, fmt.Errorf("prune request contains a current World member")
	}

	approved := make([]approvedCandidate, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		expected := filepath.Join(request.Root, filepath.FromSlash(candidate.CanonicalPath))
		if filepath.Clean(candidate.Path) != expected || candidate.Path == request.Root {
			return Plan{}, fmt.Errorf("candidate %q path %q is not canonical path %q beneath prune root", candidate.Identity, candidate.Path, expected)
		}
		resolvedPath, err := canonicalDirectory(candidate.Path)
		if err != nil {
			return Plan{}, fmt.Errorf("resolve prune candidate %q: %w", candidate.Identity, err)
		}
		if resolvedPath != candidate.Path {
			return Plan{}, fmt.Errorf("candidate %q path %q resolves through a symlink to %q", candidate.Identity, candidate.Path, resolvedPath)
		}
		if !withinRoot(resolvedRoot, resolvedPath) {
			return Plan{}, fmt.Errorf("candidate %q path %q escapes prune root %q", candidate.Identity, resolvedPath, resolvedRoot)
		}
		observation, err := observe(candidate)
		if err != nil {
			return Plan{}, fmt.Errorf("observe prune candidate %q: %w", candidate.Identity, err)
		}
		if err := validateRecoverable(candidate, observation); err != nil {
			return Plan{}, fmt.Errorf("refuse prune candidate %q: %w", candidate.Identity, err)
		}
		approved = append(approved, approvedCandidate{candidate: candidate, observation: observation, root: resolvedRoot, path: resolvedPath})
	}
	return Plan{approved: approved}, nil
}

type Receipt struct {
	RemovedPaths []string
}

// Apply re-observes every approved candidate before invoking Remove even once.
// Removal failures may return a partial Receipt; every attempted checkout was
// nevertheless proven independently recoverable before the first attempt.
func Apply(plan Plan, observe Observe, remove Remove) (Receipt, error) {
	if len(plan.approved) == 0 {
		return Receipt{}, fmt.Errorf("prune plan is empty or was not authorized")
	}
	if observe == nil || remove == nil {
		return Receipt{}, fmt.Errorf("prune apply capabilities are incomplete")
	}

	for _, approved := range plan.approved {
		if err := recheckCanonicalPath(approved); err != nil {
			return Receipt{}, err
		}
		current, err := observe(approved.candidate)
		if err != nil {
			return Receipt{}, fmt.Errorf("re-observe prune candidate %q: %w", approved.candidate.Identity, err)
		}
		if current != approved.observation {
			return Receipt{}, fmt.Errorf("prune candidate %q changed after preflight", approved.candidate.Identity)
		}
		if err := validateRecoverable(approved.candidate, current); err != nil {
			return Receipt{}, fmt.Errorf("refuse changed prune candidate %q: %w", approved.candidate.Identity, err)
		}
	}

	receipt := Receipt{RemovedPaths: make([]string, 0, len(plan.approved))}
	for _, approved := range plan.approved {
		if err := recheckCanonicalPath(approved); err != nil {
			return receipt, err
		}
		current, err := observe(approved.candidate)
		if err != nil {
			return receipt, fmt.Errorf("final observe prune candidate %q: %w", approved.candidate.Identity, err)
		}
		if current != approved.observation {
			return receipt, fmt.Errorf("prune candidate %q changed before removal", approved.candidate.Identity)
		}
		// Keep the ancestry check adjacent to the destructive capability. The
		// no-lock design cannot eliminate a hostile nanosecond race, but no
		// observation or earlier candidate may authorize a changed path.
		if err := recheckCanonicalPath(approved); err != nil {
			return receipt, err
		}
		if err := remove(approved.candidate.Path); err != nil {
			return receipt, fmt.Errorf("remove prune candidate %q: %w", approved.candidate.Identity, err)
		}
		after, err := observe(approved.candidate)
		if err != nil {
			return receipt, fmt.Errorf("observe removed prune candidate %q: %w", approved.candidate.Identity, err)
		}
		if after.Exists {
			return receipt, fmt.Errorf("prune candidate %q still exists after removal", approved.candidate.Identity)
		}
		receipt.RemovedPaths = append(receipt.RemovedPaths, approved.candidate.Path)
	}
	return receipt, nil
}

func canonicalDirectory(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", absolute)
	}
	return absolute, nil
}

func withinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func recheckCanonicalPath(approved approvedCandidate) error {
	root, err := canonicalDirectory(approved.root)
	if err != nil {
		return fmt.Errorf("recheck prune root: %w", err)
	}
	if root != approved.root {
		return fmt.Errorf("prune root changed after preflight")
	}
	path, err := canonicalDirectory(approved.candidate.Path)
	if err != nil {
		return fmt.Errorf("recheck prune candidate %q path: %w", approved.candidate.Identity, err)
	}
	if path != approved.path || !withinRoot(root, path) {
		return fmt.Errorf("prune candidate %q path changed after preflight", approved.candidate.Identity)
	}
	return nil
}

func validateResource(resource Resource) error {
	declaration := contract.Declaration{Shape: resource.Shape}
	identity, err := declaration.Identity(resource.GitHub)
	if err != nil {
		return err
	}
	canonicalPath, err := declaration.CanonicalPath(resource.GitHub)
	if err != nil {
		return err
	}
	if resource.Identity != identity {
		return fmt.Errorf("identity %q disagrees with shape-derived identity %q", resource.Identity, identity)
	}
	if resource.CanonicalPath != canonicalPath {
		return fmt.Errorf("canonical path %q disagrees with shape-derived path %q", resource.CanonicalPath, canonicalPath)
	}
	normalized, err := contract.NormalizeGitHubRepository(resource.GitHub)
	if err != nil || normalized != resource.GitHub {
		return fmt.Errorf("GitHub designation %q is not canonical", resource.GitHub)
	}
	return nil
}

func validateCandidate(candidate Candidate) error {
	if err := validateResource(Resource{
		Identity:      candidate.Identity,
		GitHub:        candidate.GitHub,
		Shape:         candidate.Shape,
		CanonicalPath: candidate.CanonicalPath,
	}); err != nil {
		return err
	}
	if candidate.Path == "" {
		return fmt.Errorf("checkout path is empty")
	}
	return nil
}

func validateRecoverable(candidate Candidate, observation Observation) error {
	if !observation.Exists {
		return fmt.Errorf("checkout does not exist")
	}
	if observation.Status != "" {
		return fmt.Errorf("checkout is dirty")
	}
	if observation.OriginCount != 1 {
		return fmt.Errorf("origin is ambiguous")
	}
	if observation.OriginGitHub != candidate.GitHub {
		return fmt.Errorf("origin %q does not match %q", observation.OriginGitHub, candidate.GitHub)
	}
	if observation.Branch == "" {
		return fmt.Errorf("branch is detached or ambiguous")
	}
	if observation.UpstreamBranch != "origin/"+observation.Branch {
		return fmt.Errorf("upstream branch %q does not exactly track origin/%s", observation.UpstreamBranch, observation.Branch)
	}
	if !commitPattern.MatchString(observation.Head) || !commitPattern.MatchString(observation.UpstreamHead) || !commitPattern.MatchString(observation.RemoteHead) {
		return fmt.Errorf("HEAD, upstream ref, or live remote ref is not an exact Git object id")
	}
	if observation.Head != observation.UpstreamHead || observation.Head != observation.RemoteHead {
		return fmt.Errorf("checkout HEAD is not exactly present at both its upstream tracking ref and live remote branch")
	}
	if observation.NestedCheckout {
		return fmt.Errorf("checkout contains a nested checkout")
	}
	if !observation.Disposable {
		return fmt.Errorf("checkout was not designated disposable")
	}
	return nil
}
