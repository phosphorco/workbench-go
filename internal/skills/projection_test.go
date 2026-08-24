package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
)

func TestDomainSelectionFollowsExplicitCompositionDependency(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skills", "basindb", "engineering", "Compose with [`$writing-go`](../writing-go/SKILL.md).")
	writeSkill(t, root, "skills", "writing-go", "general", "Go semantics.")
	writeSkill(t, root, "skills", "unselected", "general", "Not selected.")

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
	if !reflect.DeepEqual(first, []string{
		".agents/skills/.workbench-owned.json",
		".agents/skills/basindb/SKILL.md",
		".agents/skills/writing-go/SKILL.md",
	}) {
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
	writeSkill(t, root, "skills", "basindb", "engineering", "Compose with [`$missing`](../missing/SKILL.md).")
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
	writeSkill(t, first, "skills", "shared", "engineering", "first")
	writeSkill(t, second, "skills", "shared", "engineering", "second")
	if _, err := Discover([]Source{{Root: first}, {Root: second}}); err == nil {
		t.Fatal("conflicting skill sources were accepted")
	}
}

func TestDiscoverReadsOnlyGitOwnedSkillsForCurrentLine(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skills", "authored", "engineering", "Source skill.")
	writeSkill(t, root, filepath.Join(".agents", "skills"), "projected", "engineering", "Projection.")

	current, err := Discover([]Source{{Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inventoryNames(current), []string{"authored"}) {
		t.Fatalf("current inventory = %#v", inventoryNames(current))
	}
	legacy, err := DiscoverLegacy([]Source{{Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inventoryNames(legacy), []string{"projected"}) {
		t.Fatalf("legacy inventory = %#v", inventoryNames(legacy))
	}
}

func TestDiscoverRefusesSymlinkedSourceRoot(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeSkill(t, external, ".", "redirected", "engineering", "Redirected.")
	if err := os.Symlink(external, filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover([]Source{{Root: root}}); err == nil {
		t.Fatal("symlinked skill source root was accepted")
	}
}

func TestApplyPreservesForeignSiblingAndRemovesOnlyStaleOwnedSkill(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "skills", "exported", "engineering", "Exported.")
	writeSkill(t, source, "skills", "imported", "general", "Imported.")
	inventory, err := Discover([]Source{{Root: source}})
	if err != nil {
		t.Fatal(err)
	}

	destination := t.TempDir()
	foreignPath := filepath.Join(destination, ".agents", "skills", "context-owned", "SKILL.md")
	writeFile(t, foreignPath, []byte("context owned\n"))
	selected := []Skill{inventory["exported"], inventory["imported"]}
	if _, err := Apply(destination, selected); err != nil {
		t.Fatal(err)
	}
	foreignBefore := readFile(t, foreignPath)

	changed, err := Apply(destination, []Skill{inventory["imported"]})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []string{
		".agents/skills/.workbench-owned.json",
		".agents/skills/exported",
	}) {
		t.Fatalf("deselection changes = %#v", changed)
	}
	if _, err := os.Stat(filepath.Join(destination, ".agents", "skills", "exported")); !os.IsNotExist(err) {
		t.Fatalf("stale owned skill remains: %v", err)
	}
	if after := readFile(t, foreignPath); !bytes.Equal(after, foreignBefore) {
		t.Fatalf("foreign sibling changed: %q", after)
	}
}

func TestApplyRefusesForeignSelectedNameWithZeroMutation(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "skills", "shared", "engineering", "Selected source.")
	inventory, err := Discover([]Source{{Root: source}})
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	writeSkill(t, destination, filepath.Join(".agents", "skills"), "shared", "engineering", "Foreign projection.")
	before := snapshotTree(t, destination)

	if _, err := Apply(destination, []Skill{inventory["shared"]}); err == nil {
		t.Fatal("foreign selected-name collision was accepted")
	}
	if after := snapshotTree(t, destination); !reflect.DeepEqual(after, before) {
		t.Fatalf("collision mutated destination:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestApplyRefusesModifiedOwnedTreeBeforeDeselection(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "skills", "owned", "engineering", "Original.")
	inventory, err := Discover([]Source{{Root: source}})
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := Apply(destination, []Skill{inventory["owned"]}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(destination, ".agents", "skills", "owned", "local.txt"), []byte("foreign edit\n"))
	before := snapshotTree(t, destination)

	if _, err := Apply(destination, nil); err == nil {
		t.Fatal("modified owned tree was deleted")
	}
	if after := snapshotTree(t, destination); !reflect.DeepEqual(after, before) {
		t.Fatalf("refusal mutated destination:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestPlanWithTrackingRefusesTrackedManifestOrSkillSubtree(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "skills", "owned", "engineering", "Original.")
	inventory, err := Discover([]Source{{Root: source}})
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := Apply(destination, []Skill{inventory["owned"]}); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, destination)

	for _, trackedPath := range []string{
		".agents/skills/.workbench-owned.json",
		".agents/skills/owned",
	} {
		t.Run(trackedPath, func(t *testing.T) {
			observer := func(relative string) (bool, error) {
				return relative == trackedPath, nil
			}
			if _, err := PlanWithTracking(destination, nil, observer); err == nil {
				t.Fatalf("tracked projection path %q was accepted", trackedPath)
			}
			if after := snapshotTree(t, destination); !reflect.DeepEqual(after, before) {
				t.Fatalf("tracked-path refusal mutated destination:\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}
}

func TestApplyRefusesSymlinkedOwnershipManifest(t *testing.T) {
	destination := t.TempDir()
	manifest := filepath.Join(destination, ".agents", "skills", ".workbench-owned.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "manifest.json")
	writeFile(t, external, []byte("{\"version\":1,\"skills\":[]}\n"))
	if err := os.Symlink(external, manifest); err != nil {
		t.Fatal(err)
	}
	before := snapshotSymlinksAndFiles(t, destination)

	if _, err := Apply(destination, nil); err == nil {
		t.Fatal("symlinked ownership manifest was accepted")
	}
	if after := snapshotSymlinksAndFiles(t, destination); !reflect.DeepEqual(after, before) {
		t.Fatalf("symlink refusal mutated destination:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestApplyPlansRevalidatesEveryDestinationBeforeFirstMutation(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "skills", "selected", "engineering", "Selected.")
	inventory, err := Discover([]Source{{Root: source}})
	if err != nil {
		t.Fatal(err)
	}
	first := t.TempDir()
	second := t.TempDir()
	firstPlan, err := Plan(first, []Skill{inventory["selected"]})
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := Plan(second, []Skill{inventory["selected"]})
	if err != nil {
		t.Fatal(err)
	}
	firstBefore := snapshotTree(t, first)
	writeSkill(t, second, filepath.Join(".agents", "skills"), "selected", "engineering", "Late foreign collision.")
	secondBefore := snapshotTree(t, second)

	if _, err := ApplyPlans([]Projection{firstPlan, secondPlan}); err == nil {
		t.Fatal("late collision was accepted")
	}
	if after := snapshotTree(t, first); !reflect.DeepEqual(after, firstBefore) {
		t.Fatalf("earlier destination changed before late refusal:\nbefore=%#v\nafter=%#v", firstBefore, after)
	}
	if after := snapshotTree(t, second); !reflect.DeepEqual(after, secondBefore) {
		t.Fatalf("colliding destination changed:\nbefore=%#v\nafter=%#v", secondBefore, after)
	}
}

func TestApplyPlansRejectsUnmintedProjection(t *testing.T) {
	if _, err := ApplyPlans([]Projection{{}}); err == nil {
		t.Fatal("unminted projection was accepted")
	}
}

func TestApplyUpdatesValidSkillNamedPrevious(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "skills", "previous", "engineering", "First.")
	inventory, err := Discover([]Source{{Root: source}})
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := Apply(destination, []Skill{inventory["previous"]}); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, source, "skills", "previous", "engineering", "Second.")
	inventory, err = Discover([]Source{{Root: source}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(destination, []Skill{inventory["previous"]}); err != nil {
		t.Fatal(err)
	}
	contents := readFile(t, filepath.Join(destination, ".agents", "skills", "previous", "SKILL.md"))
	if !bytes.Contains(contents, []byte("Second.")) {
		t.Fatalf("updated skill contents = %q", contents)
	}
}

func TestSourceCanExportOneSkillAndReceiveAnotherConvergently(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "skills", "exported", "engineering", "Exported source.")
	writeSkill(t, root, "skills", "imported", "general", "Imported source.")
	inventory, err := Discover([]Source{{Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	sourceBefore := snapshotTreeAt(t, filepath.Join(root, "skills"))
	if _, err := Apply(root, []Skill{inventory["imported"]}); err != nil {
		t.Fatal(err)
	}
	first := snapshotTree(t, root)
	changed, err := Apply(root, []Skill{inventory["imported"]})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("convergent projection changed %#v", changed)
	}
	if second := snapshotTree(t, root); !reflect.DeepEqual(second, first) {
		t.Fatalf("second projection changed bytes:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if sourceAfter := snapshotTreeAt(t, filepath.Join(root, "skills")); !reflect.DeepEqual(sourceAfter, sourceBefore) {
		t.Fatalf("Git-owned skill source changed:\nbefore=%#v\nafter=%#v", sourceBefore, sourceAfter)
	}
}

func TestLegacyDiscoveryAndProjectionRetainHistoricalLayout(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join(".agents", "skills"), "historical", "engineering", "Historical source.")
	inventory, err := DiscoverLegacy([]Source{{Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	changed, err := ApplyLegacy(destination, []Skill{inventory["historical"]})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []string{".agents/skills/historical/SKILL.md"}) {
		t.Fatalf("legacy projection = %#v", changed)
	}
	if _, err := os.Stat(filepath.Join(destination, ".agents", "skills", ".workbench-owned.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy projection wrote ownership manifest: %v", err)
	}
}

func writeSkill(t *testing.T, root string, sourcePath string, name string, domain string, body string) {
	t.Helper()
	path := filepath.Join(root, sourcePath, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "---\nname: " + name + "\nmetadata:\n  domain: " + domain + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func inventoryNames(inventory Inventory) []string {
	names := make([]string, 0, len(inventory))
	for name := range inventory {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func writeFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func snapshotTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	return snapshotTreeAt(t, root)
}

func snapshotTreeAt(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = contents
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func snapshotSymlinksAndFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries[filepath.ToSlash(relative)] = "symlink:" + target
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries[filepath.ToSlash(relative)] = "file:" + string(contents)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
