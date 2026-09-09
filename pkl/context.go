// Package pkl contains the authored contracts shipped with Workbench.
package pkl

import _ "embed"

const (
	ContextTypesURI = "workbench:context-types"
	ContextURI      = "workbench:context"
	ContextHomeURI  = "workbench:context-home"
)

//go:embed WorkbenchContext.pkl
var Context string

//go:embed WorkbenchContextTypes.pkl
var ContextTypes string

//go:embed WorkbenchContextHome.pkl
var ContextHome string
