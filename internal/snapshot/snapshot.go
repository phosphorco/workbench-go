package snapshot

import (
	"fmt"
	"sort"

	"github.com/phosphorco/workbench-go/internal/contract"
)

// Resource is the explicit immutable observation recorded for one composed
// World member. Branch intent deliberately does not appear here.
type Resource struct {
	Identity      string
	Shape         contract.ResourceShape
	GitHub        string
	CanonicalPath string
	Commit        string
}

func Record(resources []Resource) (contract.WorkbenchWorldSnapshot, error) {
	snapshot := contract.WorkbenchWorldSnapshot{Resources: make(map[string]contract.SnapshotResource, len(resources))}
	for _, resource := range resources {
		if _, exists := snapshot.Resources[resource.Identity]; exists {
			return contract.WorkbenchWorldSnapshot{}, fmt.Errorf("record snapshot: resource identity %q is duplicated", resource.Identity)
		}
		snapshot.Resources[resource.Identity] = contract.SnapshotResource{
			Shape:         resource.Shape,
			GitHub:        resource.GitHub,
			CanonicalPath: resource.CanonicalPath,
			Commit:        resource.Commit,
		}
	}
	if err := snapshot.Validate(); err != nil {
		return contract.WorkbenchWorldSnapshot{}, fmt.Errorf("record snapshot: %w", err)
	}
	return snapshot, nil
}

type Checkout struct {
	Exists   bool
	GitHub   string
	Identity string
	Commit   string
	Clean    bool
}

type Observer interface {
	Observe(canonicalPath string) (Checkout, error)
}

type Acquisition struct {
	Identity      string
	Shape         contract.ResourceShape
	GitHub        string
	CanonicalPath string
	Commit        string
}

type Reproduction struct {
	authorization *reproductionAuthorization
}

type reproductionAuthorization struct {
	observer     Observer
	destinations []plannedDestination
}

type plannedDestination struct {
	acquisition Acquisition
	existed     bool
}

// Counts reports the destinations Plan proved absent and exact, respectively.
func (reproduction Reproduction) Counts() (acquire, verified int) {
	if reproduction.authorization == nil {
		return 0, 0
	}
	for _, destination := range reproduction.authorization.destinations {
		if destination.existed {
			verified++
		} else {
			acquire++
		}
	}
	return acquire, verified
}

type ConflictError struct {
	CanonicalPath string
	Reason        string
}

func (err *ConflictError) Error() string {
	return fmt.Sprintf("snapshot checkout conflict at %q: %s", err.CanonicalPath, err.Reason)
}

// Plan observes every destination before granting any acquisition authority.
// Existing checkouts are verification inputs and are never reset or rewritten.
func Plan(snapshot contract.WorkbenchWorldSnapshot, observer Observer) (Reproduction, error) {
	if observer == nil {
		return Reproduction{}, fmt.Errorf("plan snapshot reproduction: observer is nil")
	}
	if err := snapshot.Validate(); err != nil {
		return Reproduction{}, fmt.Errorf("plan snapshot reproduction: %w", err)
	}

	identities := make([]string, 0, len(snapshot.Resources))
	for identity := range snapshot.Resources {
		identities = append(identities, identity)
	}
	sort.Strings(identities)

	authorization := &reproductionAuthorization{observer: observer}
	for _, identity := range identities {
		resource := snapshot.Resources[identity]
		github, err := contract.NormalizeGitHubRepository(resource.GitHub)
		if err != nil {
			return Reproduction{}, fmt.Errorf("plan snapshot reproduction: resource %q origin: %w", identity, err)
		}
		action := Acquisition{Identity: identity, Shape: resource.Shape, GitHub: github, CanonicalPath: resource.CanonicalPath, Commit: resource.Commit}
		checkout, err := observer.Observe(resource.CanonicalPath)
		if err != nil {
			return Reproduction{}, fmt.Errorf("observe snapshot checkout %q: %w", resource.CanonicalPath, err)
		}
		if !checkout.Exists {
			authorization.destinations = append(authorization.destinations, plannedDestination{acquisition: action})
			continue
		}
		if err := validateExactCheckout(action, checkout); err != nil {
			return Reproduction{}, err
		}
		authorization.destinations = append(authorization.destinations, plannedDestination{acquisition: action, existed: true})
	}
	return Reproduction{authorization: authorization}, nil
}

type Acquirer interface {
	// CreateExactIfAbsent must refuse every destination that exists when the
	// operation begins; it may never reset or replace an existing checkout.
	CreateExactIfAbsent(acquisition Acquisition) error
}

// Apply first re-observes every planned destination, then acquires only paths
// that remain absent. Failure leaves honest recoverable partial progress.
func Apply(plan Reproduction, acquirer Acquirer) error {
	if plan.authorization == nil || plan.authorization.observer == nil {
		return fmt.Errorf("apply snapshot reproduction: plan is not authorized")
	}
	if acquirer == nil {
		return fmt.Errorf("apply snapshot reproduction: acquirer is nil")
	}

	// This complete fence occurs before the acquirer receives any authority.
	for _, destination := range plan.authorization.destinations {
		checkout, err := plan.authorization.observer.Observe(destination.acquisition.CanonicalPath)
		if err != nil {
			return fmt.Errorf("re-observe snapshot checkout %q before acquisition: %w", destination.acquisition.CanonicalPath, err)
		}
		if !destination.existed {
			if checkout.Exists {
				return &ConflictError{CanonicalPath: destination.acquisition.CanonicalPath, Reason: "checkout appeared after planning; no acquisition was attempted"}
			}
			continue
		}
		if err := validateExactCheckout(destination.acquisition, checkout); err != nil {
			return err
		}
	}

	for _, destination := range plan.authorization.destinations {
		if destination.existed {
			continue
		}
		acquisition := destination.acquisition
		if err := acquirer.CreateExactIfAbsent(acquisition); err != nil {
			return fmt.Errorf("acquire snapshot resource %q at %q: %w", acquisition.Identity, acquisition.CanonicalPath, err)
		}
		checkout, err := plan.authorization.observer.Observe(acquisition.CanonicalPath)
		if err != nil {
			return fmt.Errorf("verify acquired snapshot checkout %q: %w", acquisition.CanonicalPath, err)
		}
		if err := validateExactCheckout(acquisition, checkout); err != nil {
			return fmt.Errorf("verify acquired snapshot resource %q: %w", acquisition.Identity, err)
		}
	}

	// Re-observe the complete World so concurrent drift cannot be reported as a
	// successful exact reproduction.
	for _, destination := range plan.authorization.destinations {
		checkout, err := plan.authorization.observer.Observe(destination.acquisition.CanonicalPath)
		if err != nil {
			return fmt.Errorf("verify reproduced snapshot checkout %q: %w", destination.acquisition.CanonicalPath, err)
		}
		if err := validateExactCheckout(destination.acquisition, checkout); err != nil {
			return fmt.Errorf("verify reproduced snapshot resource %q: %w", destination.acquisition.Identity, err)
		}
	}
	return nil
}

func validateExactCheckout(acquisition Acquisition, checkout Checkout) error {
	if !checkout.Exists {
		return &ConflictError{CanonicalPath: acquisition.CanonicalPath, Reason: "required checkout is absent"}
	}
	github, err := contract.NormalizeGitHubRepository(checkout.GitHub)
	if err != nil {
		return &ConflictError{CanonicalPath: acquisition.CanonicalPath, Reason: fmt.Sprintf("origin %q is not an exact GitHub repository", checkout.GitHub)}
	}
	if github != acquisition.GitHub {
		return &ConflictError{CanonicalPath: acquisition.CanonicalPath, Reason: fmt.Sprintf("origin is %q, snapshot requires %q", github, acquisition.GitHub)}
	}
	identity, err := (contract.Declaration{Shape: acquisition.Shape}).Identity(github)
	if err != nil {
		return &ConflictError{CanonicalPath: acquisition.CanonicalPath, Reason: fmt.Sprintf("cannot derive identity from origin %q and snapshot shape: %v", github, err)}
	}
	if identity != acquisition.Identity || checkout.Identity != identity {
		return &ConflictError{CanonicalPath: acquisition.CanonicalPath, Reason: fmt.Sprintf("derived identity is %q and observed identity is %q, snapshot requires %q", identity, checkout.Identity, acquisition.Identity)}
	}
	if checkout.Commit != acquisition.Commit {
		return &ConflictError{CanonicalPath: acquisition.CanonicalPath, Reason: fmt.Sprintf("commit is %q, snapshot requires %q; existing checkout will not be reset", checkout.Commit, acquisition.Commit)}
	}
	if !checkout.Clean {
		return &ConflictError{CanonicalPath: acquisition.CanonicalPath, Reason: "checkout has Git-owned changes"}
	}
	return nil
}
