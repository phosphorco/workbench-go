package evaluate_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/evaluate"
)

func TestExplicitEvaluatorDoesNotResolvePklThroughPath(t *testing.T) {
	evaluator := explicitTestEvaluator(t)
	t.Setenv("PATH", t.TempDir())

	schema := localContract(t, subjectURI, "WorkbenchSubject.pkl")
	source := []byte("amends \"" + subjectURI + "\"\nworkLine { branch = \"cole/explicit\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/workbench-fixture-entry\" }\n")
	subject, err := evaluator.EvaluateSubject(context.Background(), source, schema)
	if err != nil {
		t.Fatal(err)
	}
	if subject.WorkLine.Branch != "cole/explicit" {
		t.Fatalf("Subject = %#v", subject)
	}
}

func TestExplicitEvaluatorRequiresAbsolutePklExecutable(t *testing.T) {
	if _, err := evaluate.NewEvaluator("pkl"); err == nil {
		t.Fatal("relative Pkl executable was accepted")
	}
}

func TestEvaluatesVersionedDeclarationsThroughStrictDecoders(t *testing.T) {
	ctx := context.Background()
	evaluator := explicitTestEvaluator(t)

	packageScopeURI := "workbench-contract:/PackageScopeRepository.pkl"
	packageScopeSource := []byte("amends \"" + packageScopeURI + "\"\n" + `
scope = "@workbench-entry"
includes { ["phosphorco/workbench-fixture-library"] {} }
`)
	packageScope, err := evaluator.EvaluatePackageScopeDeclaration(ctx, packageScopeSource, localContract(t, packageScopeURI, "PackageScopeRepository.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	if packageScope.Shape != (contract.ResourceShape{Kind: contract.PackageScopeShape, Scope: "@workbench-entry"}) {
		t.Fatalf("package-scope shape = %#v", packageScope.Shape)
	}

	repositoryURI := "workbench-contract:/Repository.pkl"
	repositorySource := []byte("amends \"" + repositoryURI + "\"\nincludes { [\"phosphorco/workbench-fixture-library\"] {} }\n")
	repository, err := evaluator.EvaluateRepositoryDeclaration(ctx, repositorySource, localContract(t, repositoryURI, "Repository.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	if repository.Shape != (contract.ResourceShape{Kind: contract.RepositoryShape}) {
		t.Fatalf("repository shape = %#v", repository.Shape)
	}
}

func TestEvaluatesAgentCommitAndSnapshotContractsThroughStrictDecoders(t *testing.T) {
	ctx := context.Background()
	evaluator := explicitTestEvaluator(t)
	tests := []struct {
		name     string
		uri      string
		filename string
		source   string
		evaluate func(context.Context, []byte, evaluate.Contract) error
	}{
		{
			name:     "agent instructions",
			uri:      "workbench-contract:/AgentInstructions.pkl",
			filename: "AgentInstructions.pkl",
			source: `
prose = "Keep repository authority independent."
subject {
  workLine { branch = "cole/orientation"; baseBranch = "main" }
  entrypoints { "https://github.com/phosphorco/workbench-fixture-entry" }
}
resources {
  new {
    identity = "@workbench-entry"
    github = "phosphorco/workbench-fixture-entry"
    shape = new PackageScopeShape { scope = "@workbench-entry" }
    canonicalPath = "pkg/@workbench-entry"
    branch = "cole/orientation"
    health = "healthy"
  }
}
generatedPaths { "AGENTS.md" }
handOwnedPaths { "AGENTS.pkl" }
`,
			evaluate: func(ctx context.Context, source []byte, schema evaluate.Contract) error {
				value, err := evaluator.EvaluateAgentInstructions(ctx, source, schema)
				if err == nil && len(value.Resources) != 1 {
					t.Fatalf("AgentInstructions = %#v", value)
				}
				return err
			},
		},
		{
			name:     "commit plan",
			uri:      "workbench-contract:/WorkbenchCommitPlan.pkl",
			filename: "WorkbenchCommitPlan.pkl",
			source: `
changeId = "change-001"
summary = "Coordinate one exact change."
commits {
  ["@workbench-entry"] {
    title = "Add fixture proof"
    description = "Prove the cross-repository path."
    filePaths { "src/proof.ts" }
  }
}
`,
			evaluate: func(ctx context.Context, source []byte, schema evaluate.Contract) error {
				value, err := evaluator.EvaluateWorkbenchCommitPlan(ctx, source, schema)
				if err == nil && value.ChangeID != "change-001" {
					t.Fatalf("WorkbenchCommitPlan = %#v", value)
				}
				return err
			},
		},
		{
			name:     "workbench snapshot",
			uri:      "workbench-contract:/WorkbenchSnapshot.pkl",
			filename: "WorkbenchSnapshot.pkl",
			source: `
resources {
  ["@workbench-entry"] {
    shape = new PackageScopeShape { scope = "@workbench-entry" }
    github = "phosphorco/workbench-fixture-entry"
    canonicalPath = "pkg/@workbench-entry"
    commit = "0123456789abcdef0123456789abcdef01234567"
  }
}
`,
			evaluate: func(ctx context.Context, source []byte, schema evaluate.Contract) error {
				value, err := evaluator.EvaluateWorkbenchSnapshot(ctx, source, schema)
				if err == nil && len(value.Resources) != 1 {
					t.Fatalf("WorkbenchSnapshot = %#v", value)
				}
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := localContract(t, test.uri, test.filename)
			source := []byte("amends \"" + test.uri + "\"\n" + test.source)
			if err := test.evaluate(ctx, source, schema); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentInstructionsUsesStrictGoDecoderAfterPklEvaluation(t *testing.T) {
	const uri = "workbench-contract:/AgentInstructions.pkl"
	source := []byte("amends \"" + uri + "\"\n" + `
prose = "Keep repository authority independent."
subject {
  workLine { branch = "cole/orientation"; baseBranch = "main" }
  entrypoints { "https://github.com/phosphorco/workbench-fixture-entry" }
}
resources {}
generatedPaths { "../outside" }
handOwnedPaths { "AGENTS.pkl" }
`)
	_, err := explicitTestEvaluator(t).EvaluateAgentInstructions(t.Context(), source, localContract(t, uri, "AgentInstructions.pkl"))
	if err == nil {
		t.Fatal("Pkl-valid path that violates the Go contract was accepted")
	}
	if !strings.Contains(err.Error(), "decode evaluated AgentInstructions") || !strings.Contains(err.Error(), "must not contain ..") {
		t.Fatalf("failure did not come from the strict Go decoder: %v", err)
	}
}

func explicitTestEvaluator(t *testing.T) evaluate.Evaluator {
	t.Helper()
	executable, err := exec.LookPath("pkl")
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := evaluate.NewEvaluator(executable)
	if err != nil {
		t.Fatal(err)
	}
	return evaluator
}
