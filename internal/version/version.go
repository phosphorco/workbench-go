package version

import (
	"fmt"
	"regexp"
)

var (
	release  = developmentRelease
	revision = developmentRevision

	releasePattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

const (
	developmentRelease  = "dev"
	developmentRevision = "unknown"

	// CurrentContractVersion is the semantic version of the Pkl contract
	// package embedded by the current Workbench release.
	CurrentContractVersion = "0.6.1"
	// ReleaseCoordinate is the independent GitHub release coordinate that
	// publishes CurrentContractVersion.
	ReleaseCoordinate = "0.6.2"
)

// Info identifies one released Workbench binary and its exact source revision.
type Info struct {
	Release  string
	Revision string
}

// Current returns build facts injected by the release workflow.
func Current() (Info, error) {
	if !releasePattern.MatchString(release) {
		return Info{}, fmt.Errorf("Workbench release was not injected: %q", release)
	}
	if !revisionPattern.MatchString(revision) {
		return Info{}, fmt.Errorf("Workbench revision was not injected: %q", revision)
	}
	return Info{Release: release, Revision: revision}, nil
}

// IsDevelopment reports whether both build facts retain their exact local
// defaults. Partially injected or malformed build facts are not development
// builds and remain invalid through Current.
func IsDevelopment() bool {
	return release == developmentRelease && revision == developmentRevision
}

func (info Info) String() string {
	return fmt.Sprintf("workbench %s (%s)", info.Release, info.Revision)
}
