package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal/setup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkl"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range contracts {
		if err := os.WriteFile(filepath.Join(root, c.path), []byte(c.name+" `literal`\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "internal/setup/legacy_contracts.go"), []byte("package setup\n\nconst legacyLocalRepositoryContract = `historical bytes`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Error(err)
		}
	})
	return root
}

func value(t *testing.T, path, name string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var eval func(ast.Expr) string
	eval = func(e ast.Expr) string {
		switch v := e.(type) {
		case *ast.BasicLit:
			s, err := strconv.Unquote(v.Value)
			if err != nil {
				t.Fatal(err)
			}
			return s
		case *ast.BinaryExpr:
			if v.Op != token.ADD {
				t.Fatal("unexpected expression")
			}
			return eval(v.X) + eval(v.Y)
		default:
			t.Fatalf("unexpected expression %T", e)
			return ""
		}
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			v, ok := s.(*ast.ValueSpec)
			if ok && len(v.Names) == 1 && v.Names[0].Name == name {
				return eval(v.Values[0])
			}
		}
	}
	t.Fatalf("missing %s", name)
	return ""
}

func TestCheckWriteAndByteIdentity(t *testing.T) {
	root := fixture(t)
	legacyBefore, err := os.ReadFile("internal/setup/legacy_contracts.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--check"}); err == nil {
		t.Fatal("missing contract passed --check")
	}
	if err := run([]string{"--write"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--check"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range contracts {
		want, err := os.ReadFile(filepath.Join(root, c.path))
		if err != nil {
			t.Fatal(err)
		}
		if got := value(t, outputPath, c.name); !bytes.Equal([]byte(got), want) {
			t.Fatalf("%s did not round-trip", c.name)
		}
	}
	if got := value(t, "internal/setup/legacy_contracts.go", "legacyLocalRepositoryContract"); got != "historical bytes" {
		t.Fatalf("legacy changed: %q", got)
	}
	first, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--write"}); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("regeneration changed stable output")
	}
	if err := os.WriteFile(outputPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--check"}); err == nil {
		t.Fatal("stale contract passed --check")
	}
	if err := os.Remove(outputPath); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--write"}); err != nil {
		t.Fatal(err)
	}
	recreated, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, recreated) {
		t.Fatal("recreated target differs from original")
	}
	if got := value(t, "internal/setup/legacy_contracts.go", "legacyLocalRepositoryContract"); got != "historical bytes" {
		t.Fatalf("legacy changed: %q", got)
	}
	legacyAfter, err := os.ReadFile("internal/setup/legacy_contracts.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyBefore, legacyAfter) {
		t.Fatal("historical file bytes changed")
	}
}

func TestRejectsUnsafeSourceAndInvalidModes(t *testing.T) {
	root := fixture(t)
	for _, data := range [][]byte{{'x', 0, 'y'}, {'x', '\r', '\n'}, {0xff}} {
		if err := os.WriteFile(filepath.Join(root, contracts[0].path), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := run([]string{"--write"}); err == nil {
			t.Fatal("unsafe source accepted")
		}
	}
	if err := run(nil); err == nil {
		t.Fatal("missing mode accepted")
	}
	if err := run([]string{"--check", "--write"}); err == nil {
		t.Fatal("ambiguous mode accepted")
	}
}
