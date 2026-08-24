// Package runtime resolves the private executables shipped beside a released
// Workbench binary. It deliberately never searches PATH.
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
)

// Toolchain designates the exact private Pkl and Bun executables in one
// Workbench installation.
type Toolchain struct {
	pkl string
	bun string
}

// Installed resolves the toolchain beside the currently running executable.
func Installed() (Toolchain, error) {
	executable, err := os.Executable()
	if err != nil {
		return Toolchain{}, fmt.Errorf("resolve Workbench executable: %w", err)
	}
	return FromExecutable(executable)
}

// FromExecutable resolves the private toolchain beside executable. This is
// exported for composition roots and archive-level tests that already possess
// an exact executable designation.
func FromExecutable(executable string) (Toolchain, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return Toolchain{}, fmt.Errorf("resolve Workbench executable %q: %w", executable, err)
	}
	installationRoot := filepath.Clean(filepath.Join(filepath.Dir(resolved), ".."))
	toolchain := Toolchain{
		pkl: filepath.Join(installationRoot, "libexec", "workbench", "pkl"),
		bun: filepath.Join(installationRoot, "libexec", "workbench", "bun"),
	}
	if err := requireExecutable("Pkl", toolchain.pkl); err != nil {
		return Toolchain{}, err
	}
	if err := requireExecutable("Bun", toolchain.bun); err != nil {
		return Toolchain{}, err
	}
	return toolchain, nil
}

func (toolchain Toolchain) PklPath() string {
	return toolchain.pkl
}

func (toolchain Toolchain) BunPath() string {
	return toolchain.bun
}

func requireExecutable(name, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("resolve private %s executable %q: %w", name, path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("private %s path %q is not an executable regular file", name, path)
	}
	return nil
}
