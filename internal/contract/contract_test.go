package contract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSubjectValidationAndGitHubIdentity(t *testing.T) {
	subject, err := DecodeSubject([]byte(`{
  "workLine":{"branch":"cole/slice","baseBranch":"main"},
  "entrypoints":["https://github.com/phosphorco/basindb.git"]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if subject.WorkLine.Branch != "cole/slice" {
		t.Fatalf("branch = %q", subject.WorkLine.Branch)
	}
	identity, err := GitHubIdentity(subject.Entrypoints[0])
	if err != nil {
		t.Fatal(err)
	}
	if identity != "phosphorco/basindb" {
		t.Fatalf("identity = %q", identity)
	}

	for _, encoded := range []string{
		`{"workLine":{"branch":"","baseBranch":"main"},"entrypoints":["https://github.com/phosphorco/basindb"]}`,
		`{"workLine":{"branch":"slice","baseBranch":"main"},"entrypoints":[]}`,
		`{"workLine":{"branch":"slice","baseBranch":"main"},"entrypoints":["file:///tmp/basindb"]}`,
	} {
		if _, err := DecodeSubject([]byte(encoded)); err == nil {
			t.Fatalf("invalid Subject decoded: %s", encoded)
		}
	}
}

func TestRepositoryValidationAndCanonicalPlacement(t *testing.T) {
	repository, err := DecodePackageScopeRepository([]byte(`{
  "scope":"@basindb",
  "includes":{"@phosphorco":{"github":"phosphorco/community-packages","skills":{"editing":{"domains":["engineering"],"names":["basindb","basindb"]},"workbench":"all"}}},
  "packages":{}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if repository.CanonicalPath() != "pkg/@basindb" {
		t.Fatalf("canonical path = %q", repository.CanonicalPath())
	}
	selection := repository.Includes["@phosphorco"].Skills
	if selection == nil || selection.Editing == nil || selection.Workbench == nil {
		t.Fatal("skill selections were not decoded")
	}
	if !reflect.DeepEqual(selection.Editing.Names, []string{"basindb"}) {
		t.Fatalf("editing names = %#v", selection.Editing.Names)
	}
	if !selection.Workbench.All {
		t.Fatal("workbench all selection was not preserved")
	}
}

func TestStrictDecodingRejectsUnknownAndInvalidRepositoryFacts(t *testing.T) {
	for _, encoded := range []string{
		`{"scope":"basindb","includes":{},"packages":{}}`,
		`{"scope":"@basindb","includes":{"phosphorco":{"github":"phosphorco/community-packages"}},"packages":{}}`,
		`{"scope":"@basindb","includes":{"@phosphorco":{"github":"not-a-repository"}},"packages":{}}`,
		`{"scope":"@basindb","includes":{},"packages":{},"identity":"central-registry-id"}`,
		`{"scope":"@basindb","includes":{},"packages":{}} {"scope":"@other","includes":{},"packages":{}}`,
	} {
		if _, err := DecodePackageScopeRepository([]byte(encoded)); err == nil {
			t.Fatalf("invalid repository decoded: %s", encoded)
		}
	}
}

func TestSkillSelectionRejectsUnknownShape(t *testing.T) {
	var selection SkillSelection
	if err := json.Unmarshal([]byte(`{"domains":[],"names":[],"followCompositionDependencies":false}`), &selection); err == nil {
		t.Fatal("policy was allowed to override derived skill composition")
	} else if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}
}
