package pkl_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	contracts "github.com/phosphorco/workbench-go/pkl"
)

func evaluate(t *testing.T, modulePath, filename string, expression ...string) ([]byte, error) {
	t.Helper()
	arguments := []string{"eval", "-f", "json", "--module-path", modulePath}
	if len(expression) > 0 {
		arguments = append(arguments, "-x", expression[0])
	}
	arguments = append(arguments, filename)
	return exec.Command("pkl", arguments...).CombinedOutput()
}

func evaluated(t *testing.T, modulePath, filename string, expression ...string) []byte {
	t.Helper()
	data, err := evaluate(t, modulePath, filename, expression...)
	if err != nil {
		t.Fatalf("evaluate %s: %v\n%s", filename, err, data)
	}
	return data
}

func TestVerifyExampleDecodes(t *testing.T) {
	data := evaluated(t, ".", "../examples/verify/workbench.pkl")
	repository, err := contracts.DecodeVerifyRepository(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.Verifiers) != 3 {
		t.Fatalf("verifiers: %#v", repository.Verifiers)
	}
	install := repository.Verifiers["bun-install"]
	if install.Selector.Kind != "Paths" || !reflect.DeepEqual(install.Selector.Patterns, []string{"bun.lock"}) || install.Produces != "artifact" || install.Toolchain.Kind != "weak" || install.Toolchain.AuditRate == nil || *install.Toolchain.AuditRate != 0.25 || !reflect.DeepEqual(install.Toolchain.Retained, []string{".bun-cache/**"}) || install.Network != "unrestricted" || install.Timeout != 300 {
		t.Fatalf("bun-install: %#v", install)
	}
	tests := repository.Verifiers["bun-test"]
	if tests.Selector.Kind != "Own" || tests.Toolchain.Kind != "strong" || !tests.Members || tests.Network != "none" || tests.Requires["bun-install"] != "bun-install" || tests.Timeout != 300 {
		t.Fatalf("bun-test: %#v", tests)
	}
	sync := repository.Verifiers["workspace-sync"]
	if sync.Timeout != 120 || len(sync.Program) != 1 || len(sync.Outputs) != 4 {
		t.Fatalf("workspace-sync: %#v", sync)
	}
	if !reflect.DeepEqual(repository.Gates["commit"].Requires, []string{"bun-test"}) || !reflect.DeepEqual(repository.Gates["commit"].Blocking, []string{"error"}) || !reflect.DeepEqual(repository.Focuses["tests"].Verifiers, []string{"bun-test"}) {
		t.Fatalf("repository policy: %#v", repository)
	}
}

func TestDemoVerifierJSONMatchesReferenceBytes(t *testing.T) {
	for _, id := range []string{"workspace-sync", "bun-install", "bun-test"} {
		t.Run(id, func(t *testing.T) {
			filename := "../examples/verify/demo/verifiers/" + id + "/VERIFIER.pkl"
			reference := evaluated(t, "../examples/verify/reference", filename)
			actual := evaluated(t, ".", filename)
			if !bytes.Equal(actual, reference) {
				t.Fatalf("Verifier JSON differs\nreference:\n%s\nactual:\n%s", reference, actual)
			}
			manifest, err := contracts.DecodeVerifier(actual)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.ID != id {
				t.Fatalf("id = %q", manifest.ID)
			}
		})
	}
	reference := evaluated(t, "../examples/verify/reference", "../examples/verify/demo/workbench.pkl")
	actual := evaluated(t, ".", "../examples/verify/demo/workbench.pkl")
	var expected, got map[string]json.RawMessage
	if err := json.Unmarshal(reference, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"verifiers", "gates", "focuses"} {
		if !bytes.Equal(got[key], expected[key]) {
			t.Fatalf("repository %s differs from reference", key)
		}
	}
}

const validVerifier = `amends "modulepath:/Verifier.pkl"
id = "test"
image = "example/tool@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
command { "run" }
selector = new WholeTree {}
produces = "report"
`

func verifierModule(t *testing.T, source string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "VERIFIER.pkl")
	if err := os.WriteFile(filename, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestWeakToolchainWithoutRetainedPathsIsRejected(t *testing.T) {
	filename := verifierModule(t, validVerifier+"toolchain = new Weak {}\n")
	data, err := evaluate(t, ".", filename)
	if err == nil || !bytes.Contains(data, []byte("retained")) {
		t.Fatalf("expected retained-path constraint failure: %v\n%s", err, data)
	}
}

func TestInvalidNetworkIsRejected(t *testing.T) {
	filename := verifierModule(t, validVerifier+"network = \"restricted\"\n")
	data, err := evaluate(t, ".", filename)
	if err == nil || !bytes.Contains(data, []byte("network")) {
		t.Fatalf("expected network constraint failure: %v\n%s", err, data)
	}
}

func TestVerifierSchemaConstraints(t *testing.T) {
	for _, test := range []struct{ name, source, field string }{
		{"image tag", strings.Replace(validVerifier, "example/tool@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "example/tool:latest", 1), "image"},
		{"absolute program", validVerifier + "program { \"/host/file\" }", "program"},
		{"traversing program", validVerifier + "program { \"src/../file\" }", "program"},
		{"duplicate program", validVerifier + "program { \"run\" \"run\" }", "program"},
		{"empty command", strings.Replace(validVerifier, "command { \"run\" }", "command {}", 1), "command"},
		{"empty paths", strings.Replace(validVerifier, "new WholeTree {}", "new Paths {}", 1), "patterns"},
		{"report outputs", validVerifier + "outputs { \"file\" }", "outputs"},
		{"artifact without outputs", strings.Replace(validVerifier, "produces = \"report\"", "produces = \"artifact\"", 1), "outputs"},
		{"artifact members", strings.Replace(validVerifier, "produces = \"report\"", "produces = \"artifact\"\noutputs { \"file\" }\nmembers = true", 1), "members"},
		{"negative audit", validVerifier + "toolchain = new Weak { retained { \"cache/**\" }; auditRate = -0.1 }", "auditRate"},
		{"large audit", validVerifier + "toolchain = new Weak { retained { \"cache/**\" }; auditRate = 1.1 }", "auditRate"},
		{"negative timeout", validVerifier + "timeout = -1.s", "timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := evaluate(t, ".", verifierModule(t, test.source))
			if err == nil || !bytes.Contains(data, []byte(test.field)) {
				t.Fatalf("expected %s constraint failure: %v\n%s", test.field, err, data)
			}
		})
	}
}

func TestVerifierDefaultsMatchReference(t *testing.T) {
	filename := verifierModule(t, validVerifier)
	actual := evaluated(t, ".", filename)
	reference := evaluated(t, "../examples/verify/reference", filename)
	if !bytes.Equal(actual, reference) {
		t.Fatalf("defaults differ:\n%s\n%s", reference, actual)
	}
	value, err := contracts.DecodeVerifier(actual)
	if err != nil {
		t.Fatal(err)
	}
	if value.Selector.Kind != "WholeTree" || value.DisplayNoun != "tree" || value.Toolchain.Kind != "strong" || value.Timeout != 300 || value.Network != "none" || !value.Deterministic || len(value.Program) != 0 {
		t.Fatalf("defaults: %#v", value)
	}
}

func TestRepositoryRejectsInvalidReferences(t *testing.T) {
	for _, declaration := range []string{
		`verifiers { ["wrong"] = import("../examples/verify/demo/verifiers/bun-test/VERIFIER.pkl") }`,
		`verifiers { ["bun-test"] = import("../examples/verify/demo/verifiers/bun-test/VERIFIER.pkl") }`,
		`gates { ["commit"] = new Gate { requires { "missing" } } }`,
	} {
		// Absolute imports let the temporary repository reach the committed fixture.
		absolute, err := filepath.Abs("../examples/verify/demo/verifiers/bun-test/VERIFIER.pkl")
		if err != nil {
			t.Fatal(err)
		}
		declaration = strings.ReplaceAll(declaration, "../examples/verify/demo/verifiers/bun-test/VERIFIER.pkl", absolute)
		filename := verifierModule(t, "amends \"modulepath:/Repository.pkl\"\n"+declaration)
		if data, err := evaluate(t, ".", filename); err == nil {
			t.Fatalf("invalid reference evaluated:\n%s", data)
		}
	}
}

func TestVerifyDecodingRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	data := evaluated(t, ".", "../examples/verify/demo/verifiers/bun-test/VERIFIER.pkl")
	for _, value := range [][]byte{
		[]byte("null"),
		append(bytes.Clone(data), []byte(" {}")...),
		bytes.Replace(data, []byte(`"id":`), []byte(`"unexpected": 1, "id":`), 1),
		bytes.Replace(data, []byte(`"kind": "Own"`), []byte(`"kind": "Own", "patterns": []`), 1),
		bytes.Replace(data, []byte(`"kind": "Own"`), []byte(`"kind": "Other"`), 1),
		bytes.Replace(data, []byte(`"kind": "strong"`), []byte(`"kind": "strong", "retained": []`), 1),
		bytes.Replace(data, []byte(`"kind": "strong"`), []byte(`"kind": "weak"`), 1),
	} {
		if _, err := contracts.DecodeVerifier(value); err == nil {
			t.Fatalf("accepted invalid verifier: %s", value)
		}
	}
	for _, value := range []string{`null`, `{"unknown": 1}`, `{} {}`, `{"focuses":{"test":{"unknown":1}}}`, `{"gates":{"commit":{"unknown":1}}}`} {
		if _, err := contracts.DecodeVerifyRepository([]byte(value)); err == nil {
			t.Fatalf("accepted invalid repository: %s", value)
		}
	}
}

func TestExistingDeclarationsStillEvaluate(t *testing.T) {
	root := evaluated(t, ".", "../Repository.pkl")
	repository, err := contracts.DecodeVerifyRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.Verifiers) != 0 || len(repository.Gates) != 0 || len(repository.Focuses) != 0 {
		t.Fatalf("unexpected verification defaults: %s", root)
	}
	legacy, err := filepath.Abs("../examples/verify/reference/LegacyRepository.pkl")
	if err != nil {
		t.Fatal(err)
	}
	source := `amends "modulepath:/Repository.pkl"
includes { ["example/library"] = new Include { skills { editing = "all" } } }
packages { ["library"] = new PackagePolicy { dependencies { ["other"] = "1.0.0" } } }
`
	actual := evaluated(t, ".", verifierModule(t, source))
	old := evaluated(t, ".", verifierModule(t, strings.Replace(source, "modulepath:/Repository.pkl", legacy, 1)))
	var got, expected map[string]json.RawMessage
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(old, &expected); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"includes", "packages", "buildables"} {
		if !bytes.Equal(got[key], expected[key]) {
			t.Fatalf("legacy %s changed", key)
		}
	}

}

func TestEmbeddedVerificationSchemasEvaluate(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{"Repository.pkl": contracts.RepositorySource, "Verifier.pkl": contracts.VerifierSource, "Focus.pkl": contracts.FocusSource} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	evaluated(t, root, "../examples/verify/demo/workbench.pkl")
}
