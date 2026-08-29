package setup

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/workspace"
)

func TestObserveTypeScriptImportsIgnoresCommentsAndQuotedProse(t *testing.T) {
	source := []byte(`/** Documentation says from "@false/jsdoc". */
// import "@false/line-comment"
const prose = "from '@false/string'"
const template = ` + "`import(\"@false/template\") ${import(\"@real/interpolation\")}`" + `
const matcher = /import\s+"@false\/regex"/g
import "@real/side-effect"
import type { Static } from "@real/static"
import Equals = require("@real/import-equals")
export { forwarded } from '@real/exported'
const loaded = import("@real/dynamic")
`)

	want := []observedTypeScriptImport{
		{specifier: "@real/interpolation", line: 4, kind: "dynamic-import"},
		{specifier: "@real/side-effect", line: 6, kind: "import-statement"},
		{specifier: "@real/static", line: 7, kind: "import-statement"},
		{specifier: "@real/import-equals", line: 8, kind: "require-call"},
		{specifier: "@real/exported", line: 9, kind: "import-statement"},
		{specifier: "@real/dynamic", line: 10, kind: "dynamic-import"},
	}
	if got := observeTypeScriptImports(source); !reflect.DeepEqual(got, want) {
		t.Fatalf("observed imports = %#v, want %#v", got, want)
	}
}

func TestSourceImportsUsesBunParserTruthAndKeepsLexicalLines(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "index.ts"), `if (ok) {} /import("@false\/pkg")/ + ""
import Equals = require("@real/equals")
const ordinary = require("@false/ordinary")
const dynamic = import("@real/dynamic")
`)

	imports, err := sourceImports(context.Background(), root, bun)
	if err != nil {
		t.Fatal(err)
	}
	want := []workspace.Import{
		{Specifier: "@real/equals", Source: "index.ts", Line: 2},
		{Specifier: "@real/dynamic", Source: "index.ts", Line: 4},
	}
	if !reflect.DeepEqual(imports, want) {
		t.Fatalf("parser-backed imports = %#v, want %#v", imports, want)
	}
}

func TestSourceImportsRefusesBunParserFailure(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "broken.ts"), `if (ok) {} /import("@false/pkg")/ + ""`)

	_, err = sourceImports(context.Background(), root, bun)
	if err == nil || !strings.Contains(err.Error(), "parse TypeScript imports in broken.ts with Bun") {
		t.Fatalf("Bun parser failure = %v", err)
	}
}

func TestSourceImportsRefusesAmbiguousSamePathProvenance(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "ambiguous.ts"), `if (ok) {} /import("@same\/pkg")/ + ""
const real = import("@same/pkg")
`)

	_, err = sourceImports(context.Background(), root, bun)
	var provenance *ImportProvenanceError
	if !errors.As(err, &provenance) {
		t.Fatalf("ambiguous provenance error = %T %v", err, err)
	}
	if provenance.Source != "ambiguous.ts" || provenance.Kind != "dynamic-import" || provenance.Path != "@same/pkg" || provenance.ParserCount != 1 || !reflect.DeepEqual(provenance.CandidateLines, []int{1, 2}) {
		t.Fatalf("ambiguous provenance = %#v", provenance)
	}
}
