package evaluate_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/evaluate"
)

const (
	subjectURI    = "workbench-contract:/WorkbenchSubject.pkl"
	repositoryURI = "workbench-contract:/PackageScopeRepository.pkl"
)

func TestEvaluatesSubjectAndRepositoryThroughTheirSchemas(t *testing.T) {
	subjectSchema := localContract(t, subjectURI, "WorkbenchSubject.pkl")
	repositorySchema := localContract(t, repositoryURI, "PackageScopeRepository.pkl")
	subjectSource := []byte("amends \"" + subjectURI + "\"\n" + `
workLine {
  branch = "cole/slice"
  baseBranch = "main"
}
entrypoints { "https://github.com/phosphorco/basindb" }
`)
	subject, err := evaluate.EvaluateSubject(context.Background(), subjectSource, subjectSchema)
	if err != nil {
		t.Fatal(err)
	}
	if subject.WorkLine.Branch != "cole/slice" || len(subject.Entrypoints) != 1 {
		t.Fatalf("Subject = %#v", subject)
	}

	repositorySource := []byte("amends \"" + repositoryURI + "\"\n" + `
scope = "@basindb"
includes {
  ["@phosphorco"] {
    github = "phosphorco/community-packages"
  }
}
`)
	repository, err := evaluate.EvaluatePackageScopeRepository(context.Background(), repositorySource, repositorySchema)
	if err != nil {
		t.Fatal(err)
	}
	if repository.Scope != "@basindb" || repository.Includes["@phosphorco"].GitHub != "phosphorco/community-packages" {
		t.Fatalf("repository = %#v", repository)
	}
}

func TestSchemaRejectsInvalidSource(t *testing.T) {
	schema := localContract(t, subjectURI, "WorkbenchSubject.pkl")
	source := []byte("amends \"" + subjectURI + "\"\n" + `
workLine { branch = ""; baseBranch = "main" }
entrypoints { "https://github.com/phosphorco/basindb" }
`)
	if _, err := evaluate.EvaluateSubject(context.Background(), source, schema); err == nil {
		t.Fatal("invalid Subject passed schema evaluation")
	}
}

func TestRefusesAmbientEnvironmentAndResources(t *testing.T) {
	t.Setenv("WORKBENCH_EVALUATE_SECRET", "must-not-be-readable")
	schema := localContract(t, subjectURI, "WorkbenchSubject.pkl")
	for name, expression := range map[string]string{
		"environment": `read("env:WORKBENCH_EVALUATE_SECRET").text`,
		"filesystem":  `read("file:/etc/hostname").text`,
	} {
		t.Run(name, func(t *testing.T) {
			source := []byte("amends \"" + subjectURI + "\"\nworkLine {\n  branch = " + expression + "\n  baseBranch = \"main\"\n}\nentrypoints { \"https://github.com/phosphorco/basindb\" }\n")
			if _, err := evaluate.EvaluateSubject(context.Background(), source, schema); err == nil {
				t.Fatalf("ambient %s resource was readable", name)
			} else if strings.Contains(err.Error(), "must-not-be-readable") {
				t.Fatalf("ambient %s resource leaked its contents: %v", name, err)
			}
		})
	}
}

func TestReleasedContractRequiresImmutablePackageModule(t *testing.T) {
	const released = "package://github.com/phosphorco/workbench-go/releases/download/1.0.0/workbench@1.0.0#/WorkbenchSubject.pkl"
	if _, err := evaluate.ReleasedContract(released); err != nil {
		t.Fatalf("immutable released contract was rejected: %v", err)
	}
	for _, uri := range []string{
		"https://github.com/phosphorco/workbench-go/WorkbenchSubject.pkl",
		"package://github.com/phosphorco/workbench-go/workbench@1.0.0",
		"package://example.com/phosphorco/workbench-go/releases/download/1.0.0/workbench@1.0.0#/WorkbenchSubject.pkl",
		"package://github.com/phosphorco/adjacent/releases/download/1.0.0/workbench@1.0.0#/WorkbenchSubject.pkl",
		"package://github.com/phosphorco/workbench-go/releases/download/1.0.0/workbench@1.0.1#/WorkbenchSubject.pkl",
		"package://github.com/phosphorco/workbench-go/releases/download/1.0.0/%77orkbench@1.0.0#/WorkbenchSubject.pkl",
		"package://github.com/phosphorco/workbench-go/releases/download/1.0.0/workbench@1.0.0?#/WorkbenchSubject.pkl",
	} {
		if _, err := evaluate.ReleasedContract(uri); err == nil {
			t.Fatalf("non-module release designation %q was accepted", uri)
		}
	}
}

func TestReleasedContractReachesMissingPackageTransport(t *testing.T) {
	const released = "package://github.com/phosphorco/workbench-go/releases/download/999.999.999/workbench@999.999.999#/WorkbenchSubject.pkl"
	schema, err := evaluate.ReleasedContract(released)
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("amends \"" + released + "\"\nworkLine { branch = \"cole/slice\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/workbench-fixture-entry\" }\n")
	_, err = evaluate.EvaluateSubject(context.Background(), source, schema)
	if err == nil {
		t.Fatal("missing released package unexpectedly evaluated")
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "not allowed") || strings.Contains(lower, "matching entry in allowed") {
		t.Fatalf("designated package transport was denied before reaching the remote: %v", err)
	}
	if !strings.Contains(lower, "404") && !strings.Contains(lower, "not found") {
		t.Fatalf("missing package did not reach the remote missing-release response: %v", err)
	}
}

func TestEvaluatesPublishedReleasedSubject(t *testing.T) {
	const released = "package://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0#/WorkbenchSubject.pkl"
	schema, err := evaluate.ReleasedContract(released)
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("amends \"" + released + "\"\nworkLine { branch = \"workbench/proof-0.1.0\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/workbench-fixture-entry\" }\n")
	subject, err := evaluate.EvaluateSubject(t.Context(), source, schema)
	if err != nil {
		t.Fatal(err)
	}
	if subject.WorkLine.Branch != "workbench/proof-0.1.0" || len(subject.Entrypoints) != 1 || subject.Entrypoints[0] != "https://github.com/phosphorco/workbench-fixture-entry" {
		t.Fatalf("published Subject = %#v", subject)
	}
}

func TestEvaluatesPublishedReleasedRepository(t *testing.T) {
	const released = "package://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0#/PackageScopeRepository.pkl"
	schema, err := evaluate.ReleasedContract(released)
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("amends \"" + released + "\"\nscope = \"@workbench-entry\"\nincludes { [\"@workbench-library\"] { github = \"phosphorco/workbench-fixture-library\"; skills { workbench { domains = Set(\"engineering\") } } } }\npackages { [\"@workbench-entry/app\"] { requiredButNotReferenced { [\"typescript\"] = \"^5.9.2\" } } }\n")
	repository, err := evaluate.EvaluatePackageScopeRepository(t.Context(), source, schema)
	if err != nil {
		t.Fatal(err)
	}
	if repository.Scope != "@workbench-entry" || repository.Includes["@workbench-library"].GitHub != "phosphorco/workbench-fixture-library" {
		t.Fatalf("published repository = %#v", repository)
	}
}

func TestSubjectAndRepositoryContractsCannotBeInterchanged(t *testing.T) {
	subjectSchema := localContract(t, subjectURI, "WorkbenchSubject.pkl")
	repositorySchema := localContract(t, repositoryURI, "PackageScopeRepository.pkl")
	subject := []byte("amends \"" + subjectURI + "\"\nworkLine { branch = \"cole/slice\"; baseBranch = \"main\" }\nentrypoints { \"https://github.com/phosphorco/basindb\" }\n")
	repository := []byte("amends \"" + repositoryURI + "\"\nscope = \"@basindb\"\n")

	if _, err := evaluate.EvaluatePackageScopeRepository(context.Background(), subject, repositorySchema); err == nil || !strings.Contains(err.Error(), "not designated contract") {
		t.Fatalf("Subject evaluated as repository: %v", err)
	}
	if _, err := evaluate.EvaluateSubject(context.Background(), repository, subjectSchema); err == nil || !strings.Contains(err.Error(), "not designated contract") {
		t.Fatalf("repository evaluated as Subject: %v", err)
	}
}

func localContract(t *testing.T, uri, filename string) evaluate.Contract {
	t.Helper()
	contents, err := os.ReadFile("../../pkl/" + filename)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := evaluate.LocalContract(uri, string(contents))
	if err != nil {
		t.Fatal(err)
	}
	return schema
}
