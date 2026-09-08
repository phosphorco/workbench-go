package buildable_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/buildable"
)

func TestMultiOutputDeclarationValidatesAndProjectsRuntimeDestinations(t *testing.T) {
	declaration := moduleDeclaration()
	if err := declaration.ValidateForName("browser-module"); err != nil {
		t.Fatal(err)
	}

	encoded, err := buildable.EncodeProjection([]buildable.ProjectionOwner{{
		Identity: "example/browser-module", RepositoryPath: "repos/browser-module", Source: []byte("module fixture\n"),
		Buildables: map[string]buildable.Buildable{"browser-module": declaration},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"outputs"`) || !strings.Contains(string(encoded), `"destination": "runtime/browser-module.js"`) {
		t.Fatalf("projection omitted output-set destination:\n%s", encoded)
	}
}

func TestMaterializeCopiesTheValidatedMultiOutputSetToItsDestination(t *testing.T) {
	workbench, repository := multiOutputFixture(t)
	destination := filepath.Join(t.TempDir(), "runtime")
	write(t, filepath.Join(destination, "stale.txt"), "must be removed\n", 0o644)

	receipt, err := buildable.Materialize(context.Background(), workbench, "browser-module", "browser-wasm", destination)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SchemaVersion != 1 || receipt.Status != "materialized" || receipt.Buildable != "browser-module" || receipt.Candidate != "local" || receipt.Platform != "browser-wasm" || filepath.Clean(receipt.Destination) != filepath.Clean(destination) || len(receipt.Outputs) != 4 {
		t.Fatalf("materialize receipt = %#v", receipt)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"status":"materialized"`) || !strings.Contains(string(encoded), `"destination":"runtime/browser-module.js"`) || strings.Contains(string(encoded), ".local-build/browser-module") {
		t.Fatalf("materialize receipt JSON = %s", encoded)
	}
	if strings.Contains(string(encoded), `"sha256"`) {
		t.Fatalf("public materialize receipt boundary leaked sealed-manifest sha256: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"digest":"`) {
		t.Fatalf("materialize receipt omitted public generic digest: %s", encoded)
	}
	for path, want := range map[string]string{
		"runtime/browser-module.js":         "javascript module\n",
		"runtime/browser-module_bg.wasm":    "wasm bytes\n",
		"types/browser-module.d.ts":         "declare const browserModule: unknown\n",
		"types/browser-module_bg.wasm.d.ts": "declare const wasm: unknown\n",
	} {
		got, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read materialized %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("materialized %s = %q, want %q", path, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("materialization copied candidate manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "stale.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("materialization left an undeclared destination file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repository, ".local-build/browser-module")); err != nil {
		t.Fatalf("candidate disappeared during materialization: %v", err)
	}
}

func TestResolveDeclaredPlatformReturnsTheCompleteMultiOutputSet(t *testing.T) {
	_, repository := multiOutputFixture(t)
	resolution, err := buildable.ResolveDeclaredPlatform(context.Background(), repository, "browser-module", moduleDeclaration(), "browser-wasm")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Candidate != "local" || resolution.Platform != "browser-wasm" || len(resolution.Outputs) != 4 {
		t.Fatalf("resolution = %#v", resolution)
	}
	for _, output := range resolution.Outputs {
		if output.Destination == "" || len(output.Digest) != 64 || output.Size <= 0 || output.Path == "" {
			t.Fatalf("incomplete resolved output = %#v", output)
		}
	}
	encoded, err := json.Marshal(resolution)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"candidate":".local-build/browser-module"`) {
		t.Fatalf("resolution JSON leaked candidate root: %s", encoded)
	}
	if strings.Contains(string(encoded), `"sha256"`) {
		t.Fatalf("public resolution boundary leaked sealed-manifest sha256: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"digest":"`) || !strings.Contains(string(encoded), `"outputs"`) {
		t.Fatalf("resolution JSON = %s", encoded)
	}
}

func TestOriginMainBasinDBManifestWithoutDeclarationIdentityRemainsVerifiable(t *testing.T) {
	workbench, repository := multiOutputFixture(t)
	const name = "basindb-state-sql-browser"
	const candidateRoot = ".local-build/basindb-state-sql-browser"
	writeMultiOutputCandidate(t, repository, candidateRoot, "local")
	manifestPath := filepath.Join(repository, candidateRoot, "manifest.json")
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		t.Fatal(err)
	}
	delete(manifest, "declarationIdentity")
	legacy, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, manifestPath, string(legacy)+"\n", 0o644)

	var origin struct {
		DeclarationIdentity string `json:"declarationIdentity"`
		Outputs             []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(legacy, &origin); err != nil {
		t.Fatal(err)
	}
	if origin.DeclarationIdentity != "" {
		t.Fatal("origin/main manifest unexpectedly gained declarationIdentity")
	}
	wantPaths := []string{
		"basindb_sql_browser.js",
		"basindb_sql_browser_bg.wasm",
		"basindb_sql_browser.d.ts",
		"basindb_sql_browser_bg.wasm.d.ts",
	}
	if len(origin.Outputs) != len(wantPaths) {
		t.Fatalf("origin/main output count = %d, want %d", len(origin.Outputs), len(wantPaths))
	}
	for index, output := range origin.Outputs {
		if output.Path != wantPaths[index] || len(output.SHA256) != 64 {
			t.Fatalf("origin/main output[%d] = %#v, want path %q and internal SHA-256", index, output, wantPaths[index])
		}
	}

	declaration := moduleDeclaration()
	declaration.Candidates[0].Root = candidateRoot
	declaration.Candidates[1].Root = ".ci-build/basindb-state-sql-browser"
	source, err := os.ReadFile(filepath.Join(repository, "workbench.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	encodedProjection, err := buildable.EncodeProjection([]buildable.ProjectionOwner{{
		Identity: "example/browser-module", RepositoryPath: "repos/browser-module", Source: source,
		Buildables: map[string]buildable.Buildable{name: declaration},
	}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, buildable.ProjectionPath), string(encodedProjection), 0o644)
	if err := buildable.Verify(context.Background(), workbench, name, candidateRoot, false); err != nil {
		t.Fatalf("origin/main manifest verify = %v", err)
	}
	head := gitOutput(t, repository, "rev-parse", "HEAD")
	git(t, repository, "update-ref", "refs/remotes/origin/main", head)
	if err := buildable.CheckFresh(context.Background(), workbench, name, candidateRoot, head, "origin/main"); err != nil {
		t.Fatalf("origin/main manifest check-fresh = %v", err)
	}
}

func TestMaterializeRefusesCorruptPreferredCandidateWithoutFallingThrough(t *testing.T) {
	workbench, repository := multiOutputFixture(t)
	writeMultiOutputCandidate(t, repository, ".ci-build/browser-module", "committed")
	writeMultiOutputCandidate(t, repository, ".local-build/browser-module", "local")
	write(t, filepath.Join(repository, ".local-build/browser-module", "basindb_sql_browser.js"), "mutated\n", 0o644)

	_, err := materializeWithDestination(t, workbench)
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid || refusal.Candidate != "local" {
		t.Fatalf("Materialize error = %T %v, want corrupt preferred candidate refusal", err, err)
	}
	if !strings.Contains(refusal.Reason, "hash mismatch") || !strings.Contains(refusal.Remedy, "local") {
		t.Fatalf("refusal = %#v", refusal)
	}
}

func TestMaterializeRefusesStalePreferredCandidateWithoutFallingThrough(t *testing.T) {
	workbench, repository := multiOutputFixture(t)
	writeMultiOutputCandidate(t, repository, ".ci-build/browser-module", "committed")
	writeMultiOutputCandidate(t, repository, ".local-build/browser-module", "local")
	write(t, filepath.Join(repository, "producer.txt"), "changed producer\n", 0o644)

	_, err := materializeWithDestination(t, workbench)
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalStaleProducerInputs || refusal.Candidate != "local" {
		t.Fatalf("Materialize error = %T %v, want stale preferred candidate refusal", err, err)
	}
	if !strings.Contains(refusal.Reason, "stale artifact") || !strings.Contains(refusal.Remedy, "local") {
		t.Fatalf("refusal = %#v", refusal)
	}
}

func TestMaterializeRefusesACandidateDestination(t *testing.T) {
	workbench, repository := multiOutputFixture(t)
	destination := filepath.Join(repository, ".local-build", "browser-module", "runtime")

	_, err := buildable.Materialize(context.Background(), workbench, "browser-module", "browser-wasm", destination)
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid {
		t.Fatalf("Materialize error = %T %v, want candidate-path refusal", err, err)
	}
	if !strings.Contains(refusal.Reason, "overlaps a declared candidate") || strings.Contains(refusal.Reason, `candidate ".local-build/browser-module"`) {
		t.Fatalf("refusal = %#v", refusal)
	}
}

func TestRunRefusesAValidNonExecutableModule(t *testing.T) {
	workbench, repository := multiOutputFixture(t)
	declaration := moduleDeclaration()
	declaration.Platforms["browser-wasm"] = buildable.Platform{
		OS: []string{"linux"}, Arch: []string{"amd64"}, Outputs: declaration.Platforms["browser-wasm"].Outputs,
	}
	project(t, workbench, repository, declaration)
	if err := buildable.Seal(context.Background(), workbench, "browser-module", ".local-build/browser-module"); err != nil {
		t.Fatal(err)
	}
	_, err := buildable.Resolve(context.Background(), workbench, "browser-module", "browser", "wasm32")
	if err == nil {
		t.Fatal("Resolve accepted an unsupported test platform")
	}
	if err := buildable.Run(context.Background(), workbench, "browser-module", nil); err == nil {
		t.Fatal("Run accepted a valid non-executable module")
	} else {
		var refusal *buildable.Refusal
		if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalNotExecutable {
			t.Fatalf("Run error = %T %v, want not-executable refusal", err, err)
		}
		if !strings.Contains(refusal.Remedy, "materialize") {
			t.Fatalf("Run remedy = %q, want materialization remedy", refusal.Remedy)
		}
	}
}

func TestSingleNonExecutableOutputUsesTheMaterializationContract(t *testing.T) {
	declaration := moduleDeclaration()
	platform := declaration.Platforms["browser-wasm"]
	platform.Outputs = platform.Outputs[:1]
	declaration.Platforms["browser-wasm"] = platform
	if err := declaration.ValidateForName("browser-module"); err != nil {
		t.Fatal(err)
	}
}

func moduleDeclaration() buildable.Buildable {
	return buildable.Buildable{
		InputDetection: buildable.InputDetection{Strategy: "gitHeadTree", Paths: []string{"producer.txt"}},
		BuildCommand:   buildable.BuildCommand{Executable: "true"},
		Manifest: buildable.ManifestContract{
			SchemaVersion: 1, Kind: "browser-module-manifest", ContractID: "browser-module-v1",
			ExpectedSource:       map[string]string{"repository": "https://example.test/browser-module"},
			RequiredSourceFields: []string{"revision"}, RequiredCapabilities: []string{"browser-wasm-v1"},
		},
		Candidates: []buildable.Candidate{
			{Root: ".local-build/browser-module", InputStrategy: "gitWorktree", InvalidRemedy: "Run the local browser-module producer again."},
			{Root: ".ci-build/browser-module", InputStrategy: "gitHeadTree", InvalidRemedy: "Restore the committed browser-module candidate."},
		},
		Platforms: map[string]buildable.Platform{
			"browser-wasm": {
				OS: []string{"browser"}, Arch: []string{"wasm32"},
				Outputs: []buildable.Output{
					{Path: "basindb_sql_browser.js", Destination: "runtime/browser-module.js", Kind: "module", Executable: false},
					{Path: "basindb_sql_browser_bg.wasm", Destination: "runtime/browser-module_bg.wasm", Kind: "wasm", Executable: false},
					{Path: "basindb_sql_browser.d.ts", Destination: "types/browser-module.d.ts", Kind: "declaration", Executable: false},
					{Path: "basindb_sql_browser_bg.wasm.d.ts", Destination: "types/browser-module_bg.wasm.d.ts", Kind: "declaration", Executable: false},
				},
			},
		},
	}
}

func multiOutputFixture(t *testing.T) (string, string) {
	t.Helper()
	workbench := t.TempDir()
	repository := filepath.Join(workbench, "repos", "browser-module")
	write(t, filepath.Join(repository, "producer.txt"), "producer\n", 0o644)
	write(t, filepath.Join(repository, "workbench.pkl"), "module fixture\n", 0o644)
	git(t, repository, "init", "-q", "-b", "main")
	git(t, repository, "config", "user.name", "Buildable Test")
	git(t, repository, "config", "user.email", "buildable@example.test")
	git(t, repository, "add", "producer.txt", "workbench.pkl")
	git(t, repository, "commit", "-qm", "producer")
	project(t, workbench, repository, moduleDeclaration())
	writeMultiOutputCandidate(t, repository, ".local-build/browser-module", "local")
	return workbench, repository
}

func materializeWithDestination(t *testing.T, workbench string) (string, error) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "runtime")
	_, err := buildable.Materialize(context.Background(), workbench, "browser-module", "browser-wasm", destination)
	return destination, err
}

func writeMultiOutputCandidate(t *testing.T, repository, root, label string) {
	t.Helper()
	contents := map[string]string{
		"basindb_sql_browser.js":           "javascript module\n",
		"basindb_sql_browser_bg.wasm":      "wasm bytes\n",
		"basindb_sql_browser.d.ts":         "declare const browserModule: unknown\n",
		"basindb_sql_browser_bg.wasm.d.ts": "declare const wasm: unknown\n",
	}
	for path, value := range contents {
		write(t, filepath.Join(repository, root, path), value, 0o644)
	}
	source := map[string]string{"repository": "https://example.test/browser-module", "revision": "fixture"}
	outputs := make([]map[string]any, 0, len(contents))
	for _, output := range moduleDeclaration().Platforms["browser-wasm"].Outputs {
		value := contents[output.Path]
		digest := sha256.Sum256([]byte(value))
		outputs = append(outputs, map[string]any{
			"platform": "browser-wasm", "path": output.Path, "destination": output.Destination,
			"kind": output.Kind, "sha256": hex.EncodeToString(digest[:]), "size": len(value), "executable": false,
		})
	}
	manifest := map[string]any{
		"schemaVersion": 1, "kind": "browser-module-manifest", "contractId": "browser-module-v1",
		"declarationIdentity": mustDeclarationIdentity(t, "browser-module", moduleDeclaration()),
		"source":              source, "producerInputs": map[string]any{"algorithm": "sha256", "digest": producerDigest(t, repository)},
		"capabilities": []string{"browser-wasm-v1"}, "outputs": outputs,
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repository, root, "manifest.json"), string(encoded)+"\n", 0o644)
	write(t, filepath.Join(repository, root, buildable.SourceDescriptorFilename), fmt.Sprintf(`{"source":%s,"capabilities":["browser-wasm-v1"]}`+"\n", mustJSON(t, source)), 0o644)
	if label == "" {
		t.Fatal("candidate label is empty")
	}
}

func project(t *testing.T, workbench, repository string, declaration buildable.Buildable) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(repository, "workbench.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := buildable.EncodeProjection([]buildable.ProjectionOwner{{
		Identity: "example/browser-module", RepositoryPath: "repos/browser-module", Source: source,
		Buildables: map[string]buildable.Buildable{"browser-module": declaration},
	}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, buildable.ProjectionPath), string(encoded), 0o644)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
