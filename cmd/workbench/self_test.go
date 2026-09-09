package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/skills"
)

func TestSelfSkillsAreIndependentAndAvailableWithoutEnvironment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	for _, name := range []string{"workbench", "workbench-plan"} {
		var output, diagnostics bytes.Buffer
		err := run(context.Background(), []string{"self", "skills", "export", name, "--skills-dir", root, "--pkl-package-version", "1.2.3"}, func() (string, error) {
			t.Fatal("bundled skill tried to acquire a working directory")
			return "", nil
		}, &output, &diagnostics)
		if err != nil || diagnostics.Len() != 0 {
			t.Fatalf("export %s: %v %s", name, err, &diagnostics)
		}
		directory := filepath.Join(root, name)
		if !json.Valid(output.Bytes()) {
			t.Fatalf("export receipt is not JSON: %s", &output)
		}
		reference := "references/contracts.md"
		if name == "workbench-plan" {
			reference = "references/authoring.md"
		}
		contents, err := os.ReadFile(filepath.Join(directory, reference))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "releases/download/1.2.3/workbench@1.2.3#/") || strings.Contains(string(contents), "{{") {
			t.Fatalf("release URL was not rendered in exported reference: %s", contents)
		}

	}
	catalog, err := skills.Load([]skills.Source{{Name: "installed", Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	report := catalog.Report()
	if report.SkillCount != 2 || len(report.Issues) != 0 || len(report.Warnings) != 0 || report.CompositionEdgeCount != 0 {
		t.Fatalf("exported skills do not form an independent portable catalog: %+v", report)
	}
}

func TestSelfSkillDiscoveryAndRefusal(t *testing.T) {
	for _, args := range [][]string{{}, {"self"}, {"self", "--help"}, {"self", "skills"}, {"self", "skills", "--help"}, {"self", "skills", "export", "--help"}, {"self", "skills", "export", "workbench-plan", "--help"}, {"--help"}} {
		var output bytes.Buffer
		err := run(context.Background(), args, func() (string, error) {
			t.Fatal("help acquired cwd")
			return "", nil
		}, &output, &bytes.Buffer{})
		if err != nil || !strings.Contains(output.String(), "self skills") {
			t.Fatalf("help %v: %v %s", args, err, &output)
		}
	}
	for _, args := range [][]string{{"self", "skills", "export", "unknown"}, {"self", "skills", "export", "workbench", "extra"}, {"self", "install"}} {
		var output bytes.Buffer
		err := run(context.Background(), args, func() (string, error) {
			t.Fatal("invalid invocation acquired cwd")
			return "", nil
		}, &output, &bytes.Buffer{})
		if err == nil || output.Len() != 0 || !strings.Contains(err.Error(), "workbench self skills") {
			t.Fatalf("refusal %v: %v %s", args, err, &output)
		}
	}
}

func TestBundledPlanSkillExampleRuns(t *testing.T) {
	var output bytes.Buffer
	catalogRoot := t.TempDir()
	if err := run(context.Background(), []string{"self", "skills", "export", "workbench-plan", "--skills-dir", catalogRoot}, os.Getwd, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	reference, err := os.ReadFile(filepath.Join(catalogRoot, "workbench-plan", "references", "authoring.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(reference), "```pkl\n")
	if !ok {
		t.Fatal("skill is missing its standalone Pkl example")
	}
	source, _, ok := strings.Cut(after, "\n```")
	if !ok {
		t.Fatal("Pkl example is not closed")
	}
	root := t.TempDir()
	path := writePlanFixture(t, root, "feature.plan.pkl", source)
	report, _, _, err := invokePlan(t, root, "tick", path)
	if err != nil || len(report["ready"].([]any)) != 1 || report["ready"].([]any)[0].(map[string]any)["id"] != "api" {
		t.Fatalf("example frontier: %v %+v", err, report)
	}
	if _, _, _, err := invokePlan(t, root, "verify", path, "--node", "api", "--cwd", root); err == nil {
		t.Fatal("example oracle passed before the artifact existed")
	}
	writePlanFixture(t, root, "artifact.txt", "Agreed contents.\n")
	for _, args := range [][]string{{"land", path, "--node", "api"}, {"verify", path, "--node", "api", "--cwd", root}} {
		if _, _, _, err := invokePlan(t, root, args[0], args[1], args[2:]...); err != nil {
			t.Fatal(err)
		}
	}
	report, _, _, err = invokePlan(t, root, "tick", path)
	if err != nil || len(report["ready"].([]any)) != 1 || report["ready"].([]any)[0].(map[string]any)["id"] != "review" {
		t.Fatalf("example did not unlock review: %v %+v", err, report)
	}
}

func TestSelfSkillExportPreservesOtherSkillsAndRejectsCollisions(t *testing.T) {
	root := t.TempDir()
	invoke := func(name string, flags ...string) error {
		return run(context.Background(), append([]string{"self", "skills", "export", name, "--skills-dir", root}, flags...), os.Getwd, &bytes.Buffer{}, &bytes.Buffer{})
	}
	if err := invoke("workbench"); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(root, "workbench", "SKILL.md")
	original, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := invoke("workbench-plan"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("workbench", "--pkl-package-version", "1.2.3"); err == nil {
		t.Fatal("export overwrote an existing skill")
	}
	after, err := os.ReadFile(mainPath)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("another export changed the existing skill")
	}
	if err := os.RemoveAll(filepath.Join(root, "workbench-plan")); err != nil {
		t.Fatal(err)
	}
	if err := invoke("workbench-plan", "--pkl-package-version", "1.2.3\nInjected"); err == nil {
		t.Fatal("accepted malformed version")
	}
	if _, err := os.Stat(filepath.Join(root, "workbench-plan")); !os.IsNotExist(err) {
		t.Fatal("invalid parameters wrote a folder")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "workbench-plan")); err != nil {
		t.Fatal(err)
	}
	if err := invoke("workbench-plan"); err == nil {
		t.Fatal("export followed an existing skill symlink")
	}
}

func TestSelfSkillDefaultDirectoryAndEqualsFlags(t *testing.T) {
	root := t.TempDir()
	workingDirectory := func() (string, error) { return root, nil }
	var output bytes.Buffer
	if err := run(context.Background(), []string{"self", "skills", "export", "workbench"}, workingDirectory, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "workbench", "references", "contracts.md")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"self", "skills", "export", "workbench-plan", "--skills-dir=.claude/skills", "--pkl-package-version=2.3.4"}, workingDirectory, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Directory         string
		PklPackageVersion string
	}
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Directory != filepath.Join(root, ".claude", "skills", "workbench-plan") || receipt.PklPackageVersion != "2.3.4" {
		t.Fatalf("wrong effective settings: %+v", receipt)
	}
}

func TestSelfSkillsListIsReadOnlyAndDeterministic(t *testing.T) {
	var previous string
	for i := 0; i < 2; i++ {
		var output bytes.Buffer
		err := run(context.Background(), []string{"self", "skills", "list"}, func() (string, error) { t.Fatal("list acquired cwd"); return "", nil }, &output, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Skills []struct {
				Name          string
				ExportCommand string
			}
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Skills) != 2 || result.Skills[0].Name != "workbench" || result.Skills[1].ExportCommand != "workbench self skills export workbench-plan" {
			t.Fatalf("incomplete discovery: %s", &output)
		}
		if i > 0 && output.String() != previous {
			t.Fatal("list changed without input changes")
		}
		previous = output.String()
	}
}

func TestSelfSkillsCorrectionsPreserveIntentWithoutWriting(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"self", "skill", "workbench-plan", "--skills-dir", root, "--pkl-version=1.2.3"},
		{"self", "skills", "export", "workbench-plan", "--skills-dire", root, "--pkl-package-version=1.2.3"},
		{"self", "skills", "export", "workbench-plan", "--skills-dir", root, "--pkl-version=1.2.3"},
	} {
		var output bytes.Buffer
		err := run(context.Background(), args, func() (string, error) { t.Fatal("refusal acquired cwd"); return "", nil }, &output, &bytes.Buffer{})
		if err == nil || output.Len() != 0 || !strings.Contains(err.Error(), "workbench self skills export workbench-plan") || !strings.Contains(err.Error(), root) || !strings.Contains(err.Error(), "--pkl-package-version=1.2.3") {
			t.Fatalf("lost invocation scope: %v", err)
		}
		if exitCode(err) != 2 {
			t.Fatalf("usage exit = %d", exitCode(err))
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("correction wrote files")
	}
}

func TestSelfSkillsExportAcceptsFlagsBeforeNameAndHasRecovery(t *testing.T) {
	root := t.TempDir()
	args := []string{"self", "skills", "export", "--skills-dir", root, "--pkl-package-version=1.2.3", "workbench-plan"}
	if err := run(context.Background(), args, os.Getwd, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), args, os.Getwd, &bytes.Buffer{}, &bytes.Buffer{})
	if exitCode(err) != 3 || !strings.Contains(err.Error(), `--skills-dir "$(mktemp -d)"`) || !strings.Contains(err.Error(), "--pkl-package-version 1.2.3") {
		t.Fatalf("missing safe recovery: %v", err)
	}
}

func TestSelfSkillsAliasAndOverwriteRefusalsAreActionable(t *testing.T) {
	for _, fixture := range []struct {
		args []string
		hint string
	}{
		{[]string{"self", "skills", "ls"}, "workbench self skills list"},
		{[]string{"self", "skills", "exprot", "workbench-plan"}, "workbench self skills export workbench-plan"},
		{[]string{"self", "skills", "export", "workbench-plan", "--force"}, `--skills-dir "$(mktemp -d)"`},
	} {
		err := run(context.Background(), fixture.args, func() (string, error) { t.Fatal("refusal acquired cwd"); return "", nil }, &bytes.Buffer{}, &bytes.Buffer{})
		if exitCode(err) != 2 || !strings.Contains(err.Error(), fixture.hint) {
			t.Fatalf("missing correction: %v", err)
		}
	}
}

func TestSelfSkillExportUsesPublishedContractCoordinates(t *testing.T) {
	for _, fixture := range []struct {
		flags []string
		uri   string
	}{
		{nil, "releases/download/0.8.0/workbench@0.8.0"},
		{[]string{"--pkl-package-version", "0.6.1"}, "releases/download/0.6.2/workbench@0.6.1"},
	} {
		root := t.TempDir()
		args := append([]string{"self", "skills", "export", "workbench", "--skills-dir", root}, fixture.flags...)
		if err := run(context.Background(), args, os.Getwd, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(filepath.Join(root, "workbench", "references", "contracts.md"))
		if err != nil || !strings.Contains(string(contents), fixture.uri) {
			t.Fatalf("missing published URI %s: %v\n%s", fixture.uri, err, contents)
		}
	}
}
