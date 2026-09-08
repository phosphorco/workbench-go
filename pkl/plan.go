// Package pkl contains the authored contracts shipped with Workbench.
package pkl

import (
	_ "embed"

	"github.com/phosphorco/workbench-go/internal/version"
)

// PackageVersion identifies the Pkl publication independently of binary releases.
const PackageVersion = version.CurrentContractVersion

//go:embed Plan.pkl
var Plan string
