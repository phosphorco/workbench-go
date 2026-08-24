package evaluate_test

import (
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/evaluate"
)

func TestV030EvaluatorUsesScopeDerivedPackageIdentities(t *testing.T) {
	const uri = "workbench-contract:/0.3.0/PackageScopeRepository.pkl"
	schema := localContract(t, uri, "PackageScopeRepository.pkl")
	evaluator := explicitTestEvaluator(t)

	valid := []byte("amends \"" + uri + "\"\n" + `
scope = "@workbench-entry"
packages {
  ["@workbench-entry/app"] {}
  ["@workbench-entry/tool"] {}
}
`)
	declaration, err := evaluator.EvaluatePackageScopeDeclarationV030(t.Context(), valid, schema)
	if err != nil {
		t.Fatal(err)
	}
	if directory, err := declaration.PackageDirectory("@workbench-entry/app"); err != nil || directory != "app" {
		t.Fatalf("app directory = %q, error = %v", directory, err)
	}

	invalid := []byte("amends \"" + uri + "\"\n" + `
scope = "@workbench-entry"
packages { ["@other/app"] {} }
`)
	if _, err := evaluator.EvaluatePackageScopeDeclarationV030(t.Context(), invalid, schema); err == nil || !strings.Contains(err.Error(), "Type constraint") {
		t.Fatalf("out-of-scope package evaluation error = %v", err)
	}
}

func TestV020AndV030EvaluatorsSelectDifferentGoDecoders(t *testing.T) {
	const (
		uri          = "workbench-contract:/decoder-selection/PackageScopeRepository.pkl"
		schemaSource = `module decoderSelection
class PackagePolicy {}
scope: String
includes: Mapping<String, Dynamic> = new {}
packages: Mapping<String, PackagePolicy> = new {}
`
	)
	schema, err := evaluate.LocalContract(uri, schemaSource)
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("amends \"" + uri + "\"\nscope = \"@workbench-entry\"\npackages { [\"app\"] {} }\n")
	evaluator := explicitTestEvaluator(t)

	if _, err := evaluator.EvaluatePackageScopeDeclaration(t.Context(), source, schema); err != nil {
		t.Fatalf("0.2 evaluator silently adopted 0.3 package validation: %v", err)
	}
	if _, err := evaluator.EvaluatePackageScopeDeclarationV030(t.Context(), source, schema); err == nil || !strings.Contains(err.Error(), `package identity "app"`) {
		t.Fatalf("0.3 evaluator error = %v", err)
	}
}
