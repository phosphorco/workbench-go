package runtime

import (
	"fmt"
	"path/filepath"
)

// ContextToolchain contains only the private files needed by the context
// declaration evaluator. It deliberately omits Bun and performs no filesystem
// probing; callers may construct it before a source declaration is discovered.
type ContextToolchain struct {
	PklPath         string
	RuntimeLockPath string
}

// ContextFromExecutable derives the installed context files from an exact
// Workbench executable designation. Distribution layout places them beside the
// executable's installation root under libexec/workbench and share/workbench.
// This function is path composition only: it does not stat, resolve symlinks,
// or acquire the private toolchain.
func ContextFromExecutable(executable string) (ContextToolchain, error) {
	if executable == "" {
		return ContextToolchain{}, fmt.Errorf("context executable path is empty")
	}
	abs, err := filepath.Abs(executable)
	if err != nil {
		return ContextToolchain{}, fmt.Errorf("resolve context executable %q: %w", executable, err)
	}
	installationRoot := filepath.Clean(filepath.Join(filepath.Dir(abs), ".."))
	return ContextToolchain{
		PklPath:         filepath.Join(installationRoot, "libexec", "workbench", "pkl"),
		RuntimeLockPath: filepath.Join(installationRoot, "share", "workbench", "runtime-lock.json"),
	}, nil
}
