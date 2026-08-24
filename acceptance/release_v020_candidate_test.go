package acceptance_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseV020PackageCandidate(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	packageReleaseCandidate(t, filepath.Clean(".."), first)
	packageReleaseCandidate(t, filepath.Clean(".."), second)

	for _, name := range []string{
		"workbench@0.2.0",
		"workbench@0.2.0.sha256",
		"workbench@0.2.0.zip",
		"workbench@0.2.0.zip.sha256",
	} {
		left, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(second, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(left) != string(right) {
			t.Fatalf("0.2 release candidate %q is not byte-convergent", name)
		}
	}
}
