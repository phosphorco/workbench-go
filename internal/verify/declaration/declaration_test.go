package declaration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/verify/declaration"
)

func evaluate(t *testing.T, files map[string][]byte, home string) (declaration.Declaration, error) {
	t.Helper()
	return declaration.Evaluate(context.Background(), files, home)
}

func source(body string) []byte { return []byte("amends \"modulepath:/Repository.pkl\"\n" + body) }

func TestEnvRefused(t *testing.T) {
	t.Setenv("DECLARATION_SECRET", "leaked")
	for _, name := range []string{"HOME", "PATH", "DECLARATION_SECRET"} {
		t.Run(name, func(t *testing.T) {
			_, err := evaluate(t, map[string][]byte{"workbench.pkl": source(`focuses { ["secret"] = new Focus { codes { read("env:` + name + `") } } }`)}, "")
			if err == nil || !strings.Contains(err.Error(), "env:"+name) {
				t.Fatalf("environment read accepted or wrong refusal: %v", err)
			}
		})
	}
}

func TestUntrackedImportRefused(t *testing.T) {
	untracked := filepath.Join(t.TempDir(), "extra.pkl")
	if err := os.WriteFile(untracked, []byte(`value = "secret"`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"extra.pkl", untracked, "https://example.invalid/extra.pkl"} {
		t.Run(name, func(t *testing.T) {
			_, err := evaluate(t, map[string][]byte{"workbench.pkl": source(`focuses { ["extra"] = new Focus { codes { import("` + name + `").value } } }`)}, "")
			if err == nil || !strings.Contains(err.Error(), "extra.pkl") {
				t.Fatalf("untracked import accepted or wrong refusal: %v", err)
			}
		})
	}
}

func TestLateEditInvisible(t *testing.T) {
	root := t.TempDir()
	original := source(`focuses { ["original"] = new Focus {} }`)
	tracked := map[string][]byte{"workbench.pkl": bytes.Clone(original)}
	if err := os.WriteFile(filepath.Join(root, "workbench.pkl"), source(`focuses { ["late"] = new Focus {} }`), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := evaluate(t, tracked, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Repository.Focuses) != 1 {
		t.Fatalf("focuses: %v", value.Repository.Focuses)
	}
	if _, ok := value.Repository.Focuses["original"]; !ok {
		t.Fatalf("late checkout edit entered evaluation: %v", value.Repository.Focuses)
	}
}

func demo(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	if err := filepath.WalkDir("testdata/demo", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel("testdata/demo", name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestVersionGoldenParity(t *testing.T) {
	value, err := evaluate(t, demo(t), "")
	if err != nil {
		t.Fatal(err)
	}
	registered, err := declaration.Order(value)
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct{ ID, ImageDigest, ManifestDigest, Version string }
	data, err := os.ReadFile("testdata/versions.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 3 || len(golden) != 3 {
		t.Fatal("expected three demo verifiers")
	}
	for i, r := range registered {
		g := golden[i]
		if r.Manifest.ID != g.ID || r.ImageDigest != g.ImageDigest || r.ManifestDigest != g.ManifestDigest || r.Version != g.Version {
			t.Fatalf("%s version differs from TypeScript: got %+v want %+v", r.Manifest.ID, r, g)
		}
	}
}

func TestRegisterStringifyParity(t *testing.T) {
	var golden struct {
		Manifest                json.RawMessage
		ManifestDigest, Version string
	}
	data, err := os.ReadFile("testdata/unusual.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	value, err := declaration.Register(golden.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if value.ManifestDigest != golden.ManifestDigest || value.Version != golden.Version {
		t.Fatalf("JSON.stringify parity: %+v", value)
	}
}

func TestOrderCyclePath(t *testing.T) {
	files := demo(t)
	files["verifiers/workspace-sync/VERIFIER.pkl"] = append(files["verifiers/workspace-sync/VERIFIER.pkl"], []byte("\nrequires { [\"bun-install\"] = \"bun-install\" }\n")...)
	value, err := evaluate(t, files, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = declaration.Order(value)
	if err == nil || err.Error() != "requires cycle: bun-install -> workspace-sync -> bun-install" {
		t.Fatalf("cycle refusal: %v", err)
	}
}

func TestOrderRequiresTopological(t *testing.T) {
	value, err := evaluate(t, demo(t), "")
	if err != nil {
		t.Fatal(err)
	}
	registered, err := declaration.Order(value)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range registered {
		ids = append(ids, r.Manifest.ID)
	}
	if !reflect.DeepEqual(ids, []string{"workspace-sync", "bun-install", "bun-test"}) {
		t.Fatalf("order: %v", ids)
	}
}

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func offlineFixture(t *testing.T) (map[string][]byte, string, string, string) {
	t.Helper()
	const uri = "package://offline.invalid/tool@1.0.0"
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("value.pkl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(`value = import("nested/sibling.pkl").value`)); err != nil {
		t.Fatal(err)
	}
	sibling, err := writer.Create("nested/sibling.pkl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sibling.Write([]byte(`value = "offline"`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{"name": "tool", "packageUri": uri, "version": "1.0.0", "packageZipUrl": "https://download.invalid/tool.zip", "packageZipChecksums": map[string]string{"sha256": hash(archive.Bytes())}, "dependencies": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := json.Marshal(map[string]any{"schemaVersion": 1, "resolvedDependencies": map[string]any{uri: map[string]any{"type": "remote", "uri": uri, "checksums": map[string]string{"sha256": hash(metadata)}}}})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	metadataPath, archivePath, err := declaration.PackageCachePaths(home, uri)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"workbench.pkl": source(`focuses { ["package"] = new Focus { codes { import("` + uri + `#/value.pkl").value } } }`), "PklProject.deps.json": lock}
	return files, home, metadataPath, archivePath
}

func TestOfflinePackages(t *testing.T) {
	t.Run("pinned cached succeeds", func(t *testing.T) {
		files, home, _, _ := offlineFixture(t)
		value, err := evaluate(t, files, home)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(value.Repository.Focuses["package"].Codes, []string{"offline"}) {
			t.Fatalf("package value: %v", value.Repository.Focuses)
		}
	})
	for _, test := range []string{"absent lock", "absent metadata", "absent archive", "corrupt metadata", "corrupt archive", "unlocked package"} {
		t.Run(test, func(t *testing.T) {
			files, home, metadata, archive := offlineFixture(t)
			switch test {
			case "absent lock":
				delete(files, "PklProject.deps.json")
			case "absent metadata":
				if err := os.Remove(metadata); err != nil {
					t.Fatal(err)
				}
			case "absent archive":
				if err := os.Remove(archive); err != nil {
					t.Fatal(err)
				}
			case "corrupt metadata":
				if err := os.WriteFile(metadata, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt archive":
				if err := os.WriteFile(archive, []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unlocked package":
				files["workbench.pkl"] = bytes.ReplaceAll(files["workbench.pkl"], []byte("tool@1.0.0"), []byte("tool@2.0.0"))
			}
			if _, err := evaluate(t, files, home); err == nil {
				t.Fatal("accepted unavailable or unpinned package")
			}
		})
	}
}

func TestInvalidTrackedPathRefused(t *testing.T) {
	for _, name := range []string{"../escape.pkl", "/escape.pkl", "nested/../../escape.pkl", "nested\\escape.pkl"} {
		_, err := evaluate(t, map[string][]byte{"workbench.pkl": source(""), name: []byte("value = 1")}, "")
		if err == nil || !strings.Contains(err.Error(), "invalid tracked Pkl path") {
			t.Fatalf("path %q: %v", name, err)
		}
	}
}

func TestResourcesRefused(t *testing.T) {
	for _, uri := range []string{"file:/etc/passwd", "https://example.invalid/file", "modulepath:/Verifier.pkl", "prop:other"} {
		_, err := evaluate(t, map[string][]byte{"workbench.pkl": source(`focuses { ["resource"] = new Focus { codes { read("` + uri + `").text } } }`)}, "")
		if err == nil || !strings.Contains(err.Error(), uri) {
			t.Fatalf("resource %s: %v", uri, err)
		}
	}
}

func TestMissingRootRefused(t *testing.T) {
	if _, err := evaluate(t, map[string][]byte{"nested/workbench.pkl": source("")}, ""); err == nil {
		t.Fatal("accepted missing root declaration")
	}
}

func TestOfflinePackagesTransitiveLock(t *testing.T) {
	const tool = "package://offline.invalid/tool@1.0.0"
	const helper = "package://offline.invalid/helper@1.0.0"
	home := t.TempDir()
	install := func(uri string, modules map[string]string, dependencies map[string]any) map[string]any {
		t.Helper()
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		for name, source := range modules {
			file, err := writer.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte(source)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		metadata, err := json.Marshal(map[string]any{"name": "fixture", "packageUri": uri, "version": "1.0.0", "packageZipUrl": "https://download.invalid/" + filepath.Base(uri) + ".zip", "packageZipChecksums": map[string]string{"sha256": hash(archive.Bytes())}, "dependencies": dependencies})
		if err != nil {
			t.Fatal(err)
		}
		metadataPath, archivePath, err := declaration.PackageCachePaths(home, uri)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(metadataPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"type": "remote", "uri": uri, "checksums": map[string]string{"sha256": hash(metadata)}}
	}
	helperPin := install(helper, map[string]string{"value.pkl": `value = "transitive"`}, map[string]any{})
	toolPin := install(tool, map[string]string{"value.pkl": `value = import("@helper/value.pkl").value`}, map[string]any{"helper": map[string]any{"uri": helper, "checksums": helperPin["checksums"]}})
	deps := map[string]any{tool: toolPin, helper: helperPin}
	lock := func() []byte {
		data, err := json.Marshal(map[string]any{"schemaVersion": 1, "resolvedDependencies": deps})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	files := map[string][]byte{"workbench.pkl": source(`focuses { ["package"] = new Focus { codes { import("` + tool + `#/value.pkl").value } } }`), "PklProject.deps.json": lock()}
	value, err := evaluate(t, files, home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value.Repository.Focuses["package"].Codes, []string{"transitive"}) {
		t.Fatalf("transitive package value: %v", value.Repository.Focuses)
	}
	delete(deps, helper)
	files["PklProject.deps.json"] = lock()
	if _, err := evaluate(t, files, home); err == nil || !strings.Contains(err.Error(), "unpinned transitive dependency") {
		t.Fatalf("missing transitive pin: %v", err)
	}
}

func TestOfflinePackagesCacheUnchanged(t *testing.T) {
	files, home, metadata, archive := offlineFixture(t)
	beforeMetadata, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	beforeArchive, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluate(t, files, home); err != nil {
		t.Fatal(err)
	}
	afterMetadata, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	afterArchive, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeMetadata, afterMetadata) || !bytes.Equal(beforeArchive, afterArchive) {
		t.Fatal("offline evaluation modified provisioned cache")
	}
	entries, err := os.ReadDir(filepath.Join(home, "pkl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "cache" {
		t.Fatalf("evaluation retained package staging: %v", entries)
	}
	files["workbench.pkl"] = source(`focuses { ["env"] = new Focus { codes { read("env:PATH") } } }`)
	if _, err := evaluate(t, files, home); err == nil {
		t.Fatal("expected failed evaluation")
	}
	entries, err = os.ReadDir(filepath.Join(home, "pkl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "cache" {
		t.Fatalf("failed evaluation retained package staging: %v", entries)
	}
}

func TestProjectSettingsInvisible(t *testing.T) {
	files := map[string][]byte{
		"workbench.pkl": source(`focuses { ["env"] = new Focus { codes { read("env:PATH") } } }`),
		"PklProject": []byte(`amends "pkl:Project"
evaluatorSettings { allowedResources { "env:" } }
`),
	}
	if _, err := evaluate(t, files, ""); err == nil || !strings.Contains(err.Error(), "env:PATH") {
		t.Fatalf("project widened evaluator settings: %v", err)
	}
}
