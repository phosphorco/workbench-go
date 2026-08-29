package buildable_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/phosphorco/workbench-go/internal/buildable"
	tsgospike "github.com/phosphorco/workbench-go/pkl-spike/tsgo"
)

func TestRealTsgoManifestVerdictMatchesTheTypeScriptContract(t *testing.T) {
	repository := os.Getenv("WORKBENCH_TSGO_PARITY_ROOT")
	if repository == "" {
		t.Skip("set WORKBENCH_TSGO_PARITY_ROOT to the monorepo checkout")
	}
	repository, err := filepath.Abs(repository)
	if err != nil {
		t.Fatal(err)
	}

	var typescriptOutput bytes.Buffer
	typescript := exec.Command(
		"bun",
		filepath.Join(repository, "scripts/tsgo-artifact-contract.mts"),
		"check",
		"--consumer",
		"--repo-root",
		repository,
	)
	typescript.Dir = repository
	typescript.Stdout = &typescriptOutput
	typescript.Stderr = &typescriptOutput
	typescriptErr := typescript.Run()

	_, goErr := buildable.ResolveDeclared(context.Background(), repository, "tsgo", tsgospike.Definition(), "linux", "amd64")
	if (typescriptErr == nil) != (goErr == nil) {
		t.Fatalf("verdict mismatch: TypeScript error = %v\n%s\nGo error = %v", typescriptErr, typescriptOutput.String(), goErr)
	}
	if goErr != nil {
		var refusal *buildable.Refusal
		if !errors.As(goErr, &refusal) {
			t.Fatalf("Go rejected the real manifest without a typed refusal: %T %v", goErr, goErr)
		}
		t.Logf("parity verdict: REFUSED; Go: %s", refusal.Reason)
		return
	}
	t.Log("parity verdict: VALID")
}
