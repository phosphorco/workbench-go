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

	_, goErr := buildable.ResolveDeclared(context.Background(), repository, "tsgo", realTsgoBuildable(), "linux", "amd64")
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

func realTsgoBuildable() buildable.Buildable {
	return buildable.Buildable{
		InputDetection: buildable.InputDetection{
			Strategy: "gitHeadTree",
			Paths: []string{
				".gitmodules",
				"submodules/monorepo-tsgo",
				"submodules/monorepo-tsgo/_packages/tsgo/upstream.json",
				"scripts/tsgo-artifact-contract.mts",
				"tools/tsgo",
				"tools/ci/build-tsgo.mts",
				"tools/ci/publish-tsgo-ci-build.mts",
				".github/workflows/tsgo-artifacts-ci.yml",
			},
		},
		BuildCommand: buildable.BuildCommand{Executable: "mise", Arguments: []string{"run", "tsgo:build-local"}},
		Manifest: buildable.ManifestContract{
			SchemaVersion:    2,
			Kind:             "tsgo-artifact-manifest",
			ContractID:       "tsgo-artifacts-v2",
			SourceRepository: "https://github.com/phosphorco/monorepo-tsgo",
			SourceChannel:    "latest",
		},
		Candidates: []buildable.Candidate{
			{
				Root:          ".local-build/tsgo",
				InvalidRemedy: "Run 'mise run tsgo:build-local' to rebuild it, or remove '.local-build/tsgo' completely.",
			},
			{
				Root:          ".ci-build/tsgo",
				InvalidRemedy: "Restore '.ci-build/tsgo' from Git or run the authorized TSGo CI publication workflow.",
			},
		},
		Platforms: map[string]buildable.Platform{
			"linux-x86_64": {OS: []string{"linux"}, Arch: []string{"amd64"}, Path: "linux-x86_64/tsgo", Executable: true},
			"macos-arm64":  {OS: []string{"darwin"}, Arch: []string{"arm64"}, Path: "macos-arm64/tsgo", Executable: true},
		},
	}
}
