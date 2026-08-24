package contract

import (
	"strings"
	"testing"
)

func TestV030PackageScopePackagesDeriveCanonicalChildDirectories(t *testing.T) {
	declaration, err := DecodePackageScopeDeclarationV030([]byte(`{
  "scope":"@workbench-entry",
  "includes":{},
  "packages":{"@workbench-entry/app":{},"@workbench-entry/tool":{}}
}`))
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"@workbench-entry/app":  "app",
		"@workbench-entry/tool": "tool",
	} {
		got, err := declaration.PackageDirectory(name)
		if err != nil {
			t.Fatalf("package directory for %q: %v", name, err)
		}
		if got != want {
			t.Fatalf("package directory for %q = %q, want %q", name, got, want)
		}
	}
}

func TestV030PackageScopeRejectsPackagesOutsideItsExactScope(t *testing.T) {
	for _, name := range []string{
		"app",
		"@other/app",
		"@workbench-entry",
		"@workbench-entry/",
		"@workbench-entry/app/nested",
		"@workbench-entry/../app",
		"@workbench-entry/" + strings.Repeat("a", 214),
	} {
		encoded := `{"scope":"@workbench-entry","includes":{},"packages":{"` + name + `":{}}}`
		_, err := DecodePackageScopeDeclarationV030([]byte(encoded))
		if err == nil {
			t.Fatalf("invalid package identity %q decoded", name)
		}
		if !strings.Contains(err.Error(), `package identity "`+name+`"`) {
			t.Fatalf("invalid package identity %q error = %v", name, err)
		}
	}
}

func TestV020PackageScopeDecoderRetainsHistoricalPackageNames(t *testing.T) {
	declaration, err := DecodePackageScopeDeclaration([]byte(`{
  "scope":"@workbench-entry",
  "includes":{},
  "packages":{"app":{},"@other/library":{}}
}`))
	if err != nil {
		t.Fatalf("0.2 decoder silently adopted the 0.3 package law: %v", err)
	}
	if len(declaration.Packages) != 2 {
		t.Fatalf("0.2 package count = %d", len(declaration.Packages))
	}
}

func TestPackageDirectoryRefusesRepositoryShape(t *testing.T) {
	repository, err := DecodeRepositoryDeclaration([]byte(`{"includes":{},"packages":{"library":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PackageDirectory("library"); err == nil || !strings.Contains(err.Error(), "Repository shape") {
		t.Fatalf("Repository package directory error = %v", err)
	}
}
