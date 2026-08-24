package contract

import (
	"strings"
	"testing"
)

func TestV020ClosedResourceShapesDeriveIdentityAndPlacement(t *testing.T) {
	packageScope, err := DecodePackageScopeDeclaration([]byte(`{
  "scope":"@workbench-entry",
  "includes":{"phosphorco/workbench-fixture-library":{"skills":{"editing":{"domains":["engineering"],"names":[]},"workbench":{"domains":[],"names":[]}}}},
  "packages":{}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := packageScope.Identity("phosphorco/workbench-fixture-entry"); err != nil || got != "@workbench-entry" {
		t.Fatalf("package-scope identity = %q", got)
	}
	if got, err := packageScope.CanonicalPath("phosphorco/workbench-fixture-entry"); err != nil || got != "pkg/@workbench-entry" {
		t.Fatalf("package-scope canonical path = %q", got)
	}
	if include := packageScope.Includes["phosphorco/workbench-fixture-library"]; include.Skills == nil {
		t.Fatal("include skill policy was not decoded")
	}

	repository, err := DecodeRepositoryDeclaration([]byte(`{
  "includes":{},
  "packages":{}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repository.Identity("PhosphorCo/Workbench-Fixture-Library"); err != nil || got != "phosphorco/workbench-fixture-library" {
		t.Fatalf("repository identity = %q", got)
	}
	if got, err := repository.CanonicalPath("PhosphorCo/Workbench-Fixture-Library"); err != nil || got != "repos/workbench-fixture-library" {
		t.Fatalf("repository canonical path = %q", got)
	}

	if _, err := DecodePackageScopeDeclaration([]byte(`{"scope":"@entry","includes":{"not a repo":{}},"packages":{}}`)); err == nil {
		t.Fatal("invalid include designation decoded")
	}
	if _, err := DecodeRepositoryDeclaration([]byte(`{"identity":"hand-authored","includes":{},"packages":{}}`)); err == nil {
		t.Fatal("repository declaration accepted redundant identity")
	}
}

func TestV020AgentInstructionsAcceptOnlyExplicitFacts(t *testing.T) {
	instructions, err := DecodeAgentInstructions([]byte(`{
  "prose":"Work inside this assembled World.",
  "subject":{"workLine":{"branch":"workbench/proof-0.2.0","baseBranch":"main"},"entrypoints":["https://github.com/phosphorco/workbench-fixture-entry"]},
  "resources":[{"identity":"@workbench-entry","github":"phosphorco/workbench-fixture-entry","shape":{"kind":"packageScope","scope":"@workbench-entry"},"canonicalPath":"pkg/@workbench-entry","branch":"workbench/proof-0.2.0","health":"healthy"}],
  "generatedPaths":["AGENTS.md","package.json"],
  "handOwnedPaths":["AGENTS.pkl","src/"]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if instructions.Resources[0].Identity != "@workbench-entry" {
		t.Fatalf("resource identity = %q", instructions.Resources[0].Identity)
	}

	ambient := strings.Replace(`{
  "prose":"x",
  "subject":{"workLine":{"branch":"b","baseBranch":"main"},"entrypoints":["https://github.com/phosphorco/workbench-fixture-entry"]},
  "resources":[],"generatedPaths":[],"handOwnedPaths":[]
}`, `"prose":"x"`, `"prose":"x","environment":{"TOKEN":"secret"}`, 1)
	if _, err := DecodeAgentInstructions([]byte(ambient)); err == nil {
		t.Fatal("ambient environment input decoded")
	}
}

func TestV020CommitPlanModelsExactAtomicSelections(t *testing.T) {
	plan, err := DecodeWorkbenchCommitPlan([]byte(`{
  "changeId":"fixture-cross-repository",
  "summary":"Exercise a cross-repository fixture change",
  "commits":{"@workbench-entry":{"title":"feat(fixture): consume shared value","description":"Consume the exact library value.","filePaths":["src/index.ts"],"hunkIds":[],"unrelatedDeletedPaths":[]}}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.ChangeID != "fixture-cross-repository" {
		t.Fatalf("change id = %q", plan.ChangeID)
	}

	invalid := []string{
		`{"changeId":"x","summary":"x","commits":{}}`,
		`{"changeId":"x","summary":"x","commits":{"@entry":{"title":"t","description":"d","filePaths":[],"hunkIds":[],"unrelatedDeletedPaths":[]}}}`,
		`{"changeId":"x","summary":"x","commits":{"@entry":{"title":"t","description":"d","filePaths":["../outside"],"hunkIds":[],"unrelatedDeletedPaths":[]}}}`,
		`{"changeId":"x","summary":"x","commits":{"@entry":{"title":"t","description":"d","filePaths":["gone.txt"],"hunkIds":[],"unrelatedDeletedPaths":["gone.txt"]}}}`,
	}
	for _, encoded := range invalid {
		if _, err := DecodeWorkbenchCommitPlan([]byte(encoded)); err == nil {
			t.Fatalf("invalid commit plan decoded: %s", encoded)
		}
	}
}

func TestV020WorldSnapshotRecordsExactWorldWithoutBranchAuthority(t *testing.T) {
	snapshot, err := DecodeWorkbenchWorldSnapshot([]byte(`{
  "resources":{
    "@workbench-entry":{"shape":{"kind":"packageScope","scope":"@workbench-entry"},"github":"phosphorco/workbench-fixture-entry","canonicalPath":"pkg/@workbench-entry","commit":"0123456789abcdef0123456789abcdef01234567"},
    "phosphorco/workbench-fixture-library":{"shape":{"kind":"repository"},"github":"phosphorco/workbench-fixture-library","canonicalPath":"repos/workbench-fixture-library","commit":"89abcdef0123456789abcdef0123456789abcdef"}
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Resources) != 2 {
		t.Fatalf("snapshot resource count = %d", len(snapshot.Resources))
	}

	for _, encoded := range []string{
		`{"workLine":{"branch":"snapshot-lock"},"resources":{}}`,
		`{"resources":{"wrong":{"shape":{"kind":"packageScope","scope":"@entry"},"github":"phosphorco/entry","canonicalPath":"pkg/@entry","commit":"0123456789abcdef0123456789abcdef01234567"}}}`,
		`{"resources":{"phosphorco/library":{"shape":{"kind":"repository"},"github":"phosphorco/library","canonicalPath":"repos/elsewhere","commit":"0123456789abcdef0123456789abcdef01234567"}}}`,
	} {
		if _, err := DecodeWorkbenchWorldSnapshot([]byte(encoded)); err == nil {
			t.Fatalf("invalid snapshot decoded: %s", encoded)
		}
	}
}
