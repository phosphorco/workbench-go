package runtime

import (
	"path/filepath"
	"testing"
)

func TestContextFromExecutableComposesOnlyContextPaths(t *testing.T) {
	toolchain, err := ContextFromExecutable(filepath.FromSlash("/opt/workbench/bin/workbench"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolchain.PklPath, "/opt/workbench/libexec/workbench/pkl"; got != want {
		t.Fatalf("PklPath = %q, want %q", got, want)
	}
	if got, want := toolchain.RuntimeLockPath, "/opt/workbench/share/workbench/runtime-lock.json"; got != want {
		t.Fatalf("RuntimeLockPath = %q, want %q", got, want)
	}
}

func TestContextFromExecutableDoesNotRequireInstallationFiles(t *testing.T) {
	if _, err := ContextFromExecutable(filepath.FromSlash("/not-installed/bin/workbench")); err != nil {
		t.Fatal(err)
	}
}
