package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
)

func TestCatalogCarriesValidatedCompositionIntoSelection(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "shared-tool", "general", "Shared capability.", "")
	writeCatalogSkill(t, root, "planner", "orchestration", "Planning capability.", "Read [`$shared-tool`](../shared-tool/SKILL.md).\n")

	catalog := loadCatalog(t, Source{Name: "phosphorco/control", Root: root})
	report := catalog.Report()
	if report.SkillCount != 2 || report.CompositionEdgeCount != 1 {
		t.Fatalf("catalog counts = skills %d, edges %d", report.SkillCount, report.CompositionEdgeCount)
	}
	if len(report.Issues) != 0 || len(report.Warnings) != 0 {
		t.Fatalf("valid catalog diagnostics = issues %#v warnings %#v", report.Issues, report.Warnings)
	}
	selected, err := Select(catalog, contract.SkillSelection{Names: []string{"planner"}})
	if err != nil {
		t.Fatal(err)
	}
	if names := selectedSkillNames(selected); !reflect.DeepEqual(names, []string{"planner", "shared-tool"}) {
		t.Fatalf("selected composition closure = %#v", names)
	}
}

func TestCatalogAssemblesCrossSourceCompositionBeforeProjection(t *testing.T) {
	alphaRoot := t.TempDir()
	betaRoot := t.TempDir()
	writeCatalogSkill(t, alphaRoot, "alpha", "engineering", "Alpha capability.", "Compose [`$beta`](../beta/SKILL.md).\n")
	writeCatalogSkill(t, betaRoot, "beta", "general", "Beta capability.", "")

	catalog := loadCatalog(t,
		Source{Name: "phosphorco/alpha", Root: alphaRoot},
		Source{Name: "phosphorco/beta", Root: betaRoot},
	)
	if report := catalog.Report(); len(report.Issues) != 0 || report.CompositionEdgeCount != 1 {
		t.Fatalf("cross-source catalog = issues %#v, edges %d", report.Issues, report.CompositionEdgeCount)
	}
	selected, err := Select(catalog, contract.SkillSelection{Names: []string{"alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	if names := selectedSkillNames(selected); !reflect.DeepEqual(names, []string{"alpha", "beta"}) {
		t.Fatalf("cross-source selection = %#v", names)
	}
	destination := t.TempDir()
	if _, err := Apply(destination, selected); err != nil {
		t.Fatalf("destination-valid cross-source composition refused: %v", err)
	}
	for _, skill := range selected {
		for relative, expected := range skill.Files {
			actual, err := os.ReadFile(filepath.Join(destination, ".agents", "skills", skill.Name, relative))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("projected %s/%s bytes changed", skill.Name, relative)
			}
		}
	}
}

func TestCatalogDefersCrossSourceCompositionOnlyToDestinationPortability(t *testing.T) {
	alphaRoot := t.TempDir()
	betaRoot := t.TempDir()
	writeCatalogSkill(t, alphaRoot, "alpha", "engineering", "Alpha capability.", "Compose [`$beta`](../../beta/SKILL.md).\n")
	writeCatalogSkill(t, betaRoot, "beta", "general", "Beta capability.", "")

	catalog := loadCatalog(t,
		Source{Name: "phosphorco/alpha", Root: alphaRoot},
		Source{Name: "phosphorco/beta", Root: betaRoot},
	)
	if report := catalog.Report(); len(report.Issues) != 0 {
		t.Fatalf("assembled cross-source catalog issues = %#v", report.Issues)
	}
	selected, err := Select(catalog, contract.SkillSelection{Names: []string{"alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	before := snapshotTree(t, destination)
	if _, err := Plan(destination, selected); err == nil || !strings.Contains(err.Error(), "projected link target") {
		t.Fatalf("destination-invalid cross-source composition = %v", err)
	}
	if after := snapshotTree(t, destination); !reflect.DeepEqual(after, before) {
		t.Fatalf("portability refusal mutated destination: before %#v after %#v", before, after)
	}
}

func TestCatalogReportsBlockingContractsWithSourceAndLine(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "shared-tool", "general", "Shared capability.", "")
	writeCatalogSkill(t, root, "planner", "orchestration", "Planning capability.", "Read [shared capability](../shared-tool/SKILL.md).\nRead [missing](references/missing.md).\n")

	catalog := loadCatalog(t, Source{Name: "phosphorco/control", Root: root})
	issues := catalog.Report().Issues
	if len(issues) != 2 {
		t.Fatalf("issues = %#v", issues)
	}
	if issues[0].Location() != "phosphorco/control:planner/SKILL.md:8" || issues[0].Message != `composition link to "shared-tool" must name "$shared-tool" in its label` {
		t.Fatalf("composition issue = %#v", issues[0])
	}
	if issues[1].Location() != "phosphorco/control:planner/SKILL.md:9" || issues[1].Message != "missing link target references/missing.md" {
		t.Fatalf("missing-link issue = %#v", issues[1])
	}
	if _, err := Select(catalog, contract.SkillSelection{All: true}); err == nil {
		t.Fatal("invalid catalog was selectable")
	}
}

func TestCatalogRejectsMalformedFrontmatterAtTheAuthoredContract(t *testing.T) {
	root := t.TempDir()
	writeCatalogFile(t, root, "invalid-yaml", "SKILL.md", "---\nname: [\n---\n")
	writeCatalogFile(t, root, "invalid-domain", "SKILL.md", "---\nname: invalid-domain\ndescription: Invalid domain.\nmetadata:\n  domain: magic\n---\n")

	report := loadCatalog(t, Source{Name: "phosphorco/control", Root: root}).Report()
	if !reflect.DeepEqual(report.Issues, []Diagnostic{
		{Source: "phosphorco/control", Path: "invalid-domain/SKILL.md", Line: 1, Message: "frontmatter must declare name and a valid metadata.domain"},
		{Source: "phosphorco/control", Path: "invalid-yaml/SKILL.md", Line: 1, Message: "frontmatter is not valid YAML"},
	}) {
		t.Fatalf("frontmatter issues = %#v", report.Issues)
	}
}

func TestCatalogChecksFlatnessFrontmatterReferencesAndDomainWarnings(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "shared-tool", "general", "Shared capability.", "")
	writeCatalogSkill(t, root, "engineer", "engineering", "Engineering capability.", "")
	writeCatalogSkill(t, root, "planner", "orchestration", "Planning capability.", "Use shared-tool, `/shared-tool`, or the shared tool. Read [`$engineer`](../engineer/SKILL.md).\n")
	if err := os.MkdirAll(filepath.Join(root, "group", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	report := loadCatalog(t, Source{Root: root}).Report()
	issueMessages := diagnosticMessages(report.Issues)
	for _, expected := range []string{
		"skill directory has no SKILL.md",
		`skill reference "shared-tool" must use "$shared-tool"`,
		`skill reference "/shared-tool" must use "$shared-tool"`,
	} {
		if !containsString(issueMessages, expected) {
			t.Fatalf("issues %#v do not contain %q", report.Issues, expected)
		}
	}
	warningMessages := diagnosticMessages(report.Warnings)
	for _, expected := range []string{
		`possible skill reference "shared tool"; use "$shared-tool" if the phrase names the skill`,
		`$planner (orchestration) references $engineer (engineering); cross-domain skill references may target only "general"`,
	} {
		if !containsString(warningMessages, expected) {
			t.Fatalf("warnings %#v do not contain %q", report.Warnings, expected)
		}
	}
}

func TestCatalogValidatesDescriptionMarkers(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "marked", "general", "Recognize alpha.", "")
	writeCatalogFile(t, root, "marked", ".skill-meta.yaml", "maintenance:\n  description-markers:\n    - text: alpha\n      kind: mystery\n      why: Matches alpha.\n      extra: not-allowed\n    - text: beta\n      kind: encounter\n      why: Matches beta.\n")

	report := loadCatalog(t, Source{Root: root}).Report()
	if !reflect.DeepEqual(diagnosticMessages(report.Issues), []string{
		`maintenance.description-markers[0] has unknown field "extra"; allowed fields are text, kind, why`,
		"maintenance.description-markers[0].kind must be one of request, encounter, contract, boundary",
	}) {
		t.Fatalf("marker issues = %#v", report.Issues)
	}
	if !reflect.DeepEqual(diagnosticMessages(report.Warnings), []string{
		`description marker text "beta" does not occur in the normalized frontmatter description`,
	}) {
		t.Fatalf("marker warnings = %#v", report.Warnings)
	}
}

func TestCatalogSortsExplicitSourcesAndRetainsRepositoryProvenance(t *testing.T) {
	alpha := t.TempDir()
	zeta := t.TempDir()
	writeCatalogSkill(t, alpha, "alpha", "general", "Alpha.", "Read [missing](missing.md).\n")
	writeCatalogSkill(t, zeta, "zeta", "general", "Zeta.", "Read [missing](missing.md).\n")

	report := loadCatalog(t,
		Source{Name: "zeta/repository", Root: zeta},
		Source{Name: "alpha/repository", Root: alpha},
	).Report()
	if got := []string{report.Issues[0].Source, report.Issues[1].Source}; !reflect.DeepEqual(got, []string{"alpha/repository", "zeta/repository"}) {
		t.Fatalf("diagnostic source order = %#v", got)
	}
}

func TestProjectionPreflightAcceptsCopiedInternalAndPeerLinks(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "shared", "general", "Shared.", "")
	writeCatalogSkill(t, root, "planner", "orchestration", "Planner.", "Read [details](references/details.md).\nCompose [`$shared`](../shared/SKILL.md).\n")
	writeCatalogFile(t, root, "planner", "references/details.md", "Details.\n")
	catalog := loadCatalog(t, Source{Name: "phosphorco/resource", Root: root})
	selected, err := Select(catalog, contract.SkillSelection{Names: []string{"planner"}})
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := Plan(destination, selected); err != nil {
		t.Fatalf("portable projection refused: %v", err)
	}
}

func TestProjectionPreflightRefusesSourceOnlyRelativeLinkWithoutMutation(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(repository, "skills")
	writeCatalogSkill(t, root, "planner", "orchestration", "Planner.", "Read [repository guide](../../docs/guide.md).\n")
	writeFile(t, filepath.Join(repository, "docs", "guide.md"), []byte("Repository-owned guide.\n"))
	catalog := loadCatalog(t, Source{Name: "phosphorco/resource", Root: root})
	if report := catalog.Report(); len(report.Issues) != 0 {
		t.Fatalf("source catalog should be valid in place: %#v", report.Issues)
	}
	selected, err := Select(catalog, contract.SkillSelection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || len(selected[0].Links) != 1 {
		t.Fatalf("selected projection links = %#v", selected)
	}
	destination := t.TempDir()
	before := snapshotTree(t, destination)
	_, err = Plan(destination, selected)
	if err == nil {
		t.Fatal("projection with an absent destination link was accepted")
	}
	for _, fact := range []string{"phosphorco/resource:planner/SKILL.md:8", "../../docs/guide.md", "copies skill bytes without rewriting authored paths"} {
		if !strings.Contains(err.Error(), fact) {
			t.Fatalf("projection refusal %q omits %q", err, fact)
		}
	}
	if after := snapshotTree(t, destination); !reflect.DeepEqual(after, before) {
		t.Fatalf("projection preflight mutated destination: before %#v after %#v", before, after)
	}
}

func TestProjectionBatchRevalidatesLinkTargetsBeforeFirstWrite(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(repository, "skills")
	writeCatalogSkill(t, root, "planner", "orchestration", "Planner.", "Read [destination guide](../../docs/guide.md).\n")
	writeFile(t, filepath.Join(repository, "docs", "guide.md"), []byte("Repository-owned guide.\n"))
	catalog := loadCatalog(t, Source{Name: "phosphorco/resource", Root: root})
	selected, err := Select(catalog, contract.SkillSelection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || len(selected[0].Links) != 1 {
		t.Fatalf("selected projection links = %#v", selected)
	}
	destination := t.TempDir()
	projectedGuide := filepath.Join(destination, ".agents", "docs", "guide.md")
	writeFile(t, projectedGuide, []byte("Context-owned guide.\n"))
	plan, err := Plan(destination, selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(projectedGuide); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, destination)
	if _, err := ApplyPlan(plan); err == nil || !strings.Contains(err.Error(), "revalidate projected skill links") {
		t.Fatalf("late broken link refusal = %v", err)
	}
	if after := snapshotTree(t, destination); !reflect.DeepEqual(after, before) {
		t.Fatalf("late link refusal mutated destination: before %#v after %#v", before, after)
	}
}

func loadCatalog(t *testing.T, sources ...Source) Catalog {
	t.Helper()
	catalog, err := Load(sources)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func writeCatalogSkill(t *testing.T, root string, name string, domain string, description string, body string) {
	t.Helper()
	writeCatalogFile(t, root, name, "SKILL.md", "---\nname: "+name+"\ndescription: "+description+"\nmetadata:\n  domain: "+domain+"\n---\n\n"+body)
}

func writeCatalogFile(t *testing.T, root string, skill string, relative string, contents string) {
	t.Helper()
	path := filepath.Join(root, skill, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func selectedSkillNames(skills []Skill) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	return names
}

func diagnosticMessages(diagnostics []Diagnostic) []string {
	messages := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		messages = append(messages, diagnostic.Message)
	}
	return messages
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
