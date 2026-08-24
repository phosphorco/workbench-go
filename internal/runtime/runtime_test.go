package runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFromExecutableResolvesOnlyPrivateInstalledTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Workbench 0.2.0 does not distribute Windows archives")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	libexec := filepath.Join(root, "libexec", "workbench")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libexec, 0o755); err != nil {
		t.Fatal(err)
	}
	workbench := filepath.Join(bin, "workbench")
	writeExecutable(t, workbench)
	writeExecutable(t, filepath.Join(libexec, "pkl"))
	writeExecutable(t, filepath.Join(libexec, "bun"))

	got, err := FromExecutable(workbench)
	if err != nil {
		t.Fatalf("FromExecutable(): %v", err)
	}
	if got.PklPath() != filepath.Join(libexec, "pkl") {
		t.Fatalf("PklPath() = %q", got.PklPath())
	}
	if got.BunPath() != filepath.Join(libexec, "bun") {
		t.Fatalf("BunPath() = %q", got.BunPath())
	}
}

func TestFromExecutableRefusesMissingPrivateToolWithoutPATHFallback(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	workbench := filepath.Join(bin, "workbench")
	writeExecutable(t, workbench)
	t.Setenv("PATH", t.TempDir())

	if _, err := FromExecutable(workbench); err == nil {
		t.Fatal("FromExecutable() succeeded without private Pkl and Bun")
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
}
