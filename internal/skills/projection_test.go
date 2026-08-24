package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
)

func TestDomainSelectionFollowsExplicitCompositionDependency(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "basindb", "engineering", "Compose with [`$writing-go`](../writing-go/SKILL.md).")
	writeSkill(t, root, "writing-go", "general", "Go semantics.")
	writeSkill(t, root, "unselected", "general", "Not selected.")

	inventory, err := Discover([]Source{{Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(inventory, contract.SkillSelection{Domains: []string{"engineering"}})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(selected))
	for _, skill := range selected {
		names = append(names, skill.Name)
	}
	if !reflect.DeepEqual(names, []string{"basindb", "writing-go"}) {
		t.Fatalf("selected skills = %#v", names)
	}

	destination := t.TempDir()
	first, err := Apply(destination, selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("first projection = %#v", first)
	}
	second, err := Apply(destination, selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second projection changed %#v", second)
	}
	if _, err := os.Stat(filepath.Join(destination, ".agents/skills/writing-go/SKILL.md")); err != nil {
		t.Fatalf("composition dependency was not projected: %v", err)
	}
}

func TestSelectionRejectsMissingNamedAndComposedSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "basindb", "engineering", "Compose with [`$missing`](../missing/SKILL.md).")
	inventory, err := Discover([]Source{{Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(inventory, contract.SkillSelection{Names: []string{"absent"}}); err == nil {
		t.Fatal("absent selected root was accepted")
	}
	if _, err := Select(inventory, contract.SkillSelection{Names: []string{"basindb"}}); err == nil {
		t.Fatal("absent composition dependency was accepted")
	}
}

func TestDiscoverRejectsConflictingSkillSources(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeSkill(t, first, "shared", "engineering", "first")
	writeSkill(t, second, "shared", "engineering", "second")
	if _, err := Discover([]Source{{Root: first}, {Root: second}}); err == nil {
		t.Fatal("conflicting skill sources were accepted")
	}
}

func writeSkill(t *testing.T, root string, name string, domain string, body string) {
	t.Helper()
	path := filepath.Join(root, ".agents", "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "---\nname: " + name + "\nmetadata:\n  domain: " + domain + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
