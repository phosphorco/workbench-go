package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/phosphorco/workbench-go/internal/contract"
)

func TestFilesystemCatalogExportsTheWholeSkill(t *testing.T) {
	files := fstest.MapFS{
		"example/SKILL.md":                   {Data: []byte("---\nname: example\ndescription: Exercise a portable skill.\nmetadata:\n  domain: general\n---\nRead [instructions](references/instructions.md).\n")},
		"example/references/instructions.md": {Data: []byte("Use the packaged asset.\n")},
		"example/assets/input.bin":           {Data: []byte{0, 255, 1, 2}},
		"example/scripts/run.sh":             {Data: []byte("#!/bin/sh\nprintf done\n")},
	}
	catalog, err := Load([]Source{{Name: "embedded", Root: ".", FS: files}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(catalog, contract.SkillSelection{All: true})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	exported, err := Export(root, selected)
	if err != nil || len(exported) != len(files) {
		t.Fatalf("export: %v %v", exported, err)
	}
	for name, file := range files {
		actual, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !bytes.Equal(actual, file.Data) {
			t.Fatalf("asset %s changed: %v", name, err)
		}
	}
	delete(files, "example/references/instructions.md")
	broken, err := Load([]Source{{Name: "embedded", Root: ".", FS: files}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(broken, contract.SkillSelection{All: true}); err == nil {
		t.Fatal("catalog accepted a broken reference")
	}
}
