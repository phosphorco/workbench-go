// Package tsgospike expresses the existing TSGo artifact contract as one
// generic Buildable declaration for parity and latency proofs.
package tsgospike

import "github.com/phosphorco/workbench-go/internal/buildable"

func Definition() buildable.Buildable {
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
			SchemaVersion: 2,
			Kind:          "tsgo-artifact-manifest",
			ContractID:    "tsgo-artifacts-v2",
			ExpectedSource: map[string]string{
				"repository": "https://github.com/phosphorco/monorepo-tsgo",
				"channel":    "latest",
			},
			RequiredSourceFields: []string{"revision", "version", "nestedRevision"},
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
