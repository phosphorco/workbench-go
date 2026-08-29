package setup

import (
	"context"
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
	got := observeTypeScriptImports(source)
	for index := range got {
		got[index].spanStart = 0
		got[index].spanEnd = 0
	}
	if !reflect.DeepEqual(got, want) {
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

func TestSourceImportsDifferentialProbeSelectsRealSamePathCandidate(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "ambiguous.ts"), `if (ok) {} /import("@same\/pkg")/ + ""
const real = import("@same/pkg")
`)

	imports, err := sourceImports(context.Background(), root, bun)
	if err != nil {
		t.Fatal(err)
	}
	want := []workspace.Import{{Specifier: "@same/pkg", Source: "ambiguous.ts", Line: 2}}
	if !reflect.DeepEqual(imports, want) {
		t.Fatalf("same-path differential imports = %#v, want %#v", imports, want)
	}
}

func TestSourceImportsDifferentialProbePreservesDuplicateRealImports(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "Resource.ts"), `const padding1 = 1
const padding2 = 2
const padding3 = 3
const padding4 = 4
const padding5 = 5
import { Resource } from "alchemy/Resource"
import { Resource as OtherResource } from "alchemy/Resource"
export { Resource, OtherResource }
`)

	imports, err := sourceImports(context.Background(), root, bun)
	if err != nil {
		t.Fatal(err)
	}
	want := []workspace.Import{
		{Specifier: "alchemy/Resource", Source: "Resource.ts", Line: 6},
		{Specifier: "alchemy/Resource", Source: "Resource.ts", Line: 7},
	}
	if !reflect.DeepEqual(imports, want) {
		t.Fatalf("duplicate real imports = %#v, want %#v", imports, want)
	}
}

func TestSourceImportsUsesBunCookedSpecifierAndPreservesEscapedNewlineProvenance(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "escaped.ts"), `import hex from "@scope/\x70kg"
import unicode from "@scope/\u0070kg"
import continued from "\
@scope/continued"
const later = import("@scope/later")
`)

	imports, err := sourceImports(context.Background(), root, bun)
	if err != nil {
		t.Fatal(err)
	}
	want := []workspace.Import{
		{Specifier: "@scope/pkg", Source: "escaped.ts", Line: 1},
		{Specifier: "@scope/pkg", Source: "escaped.ts", Line: 2},
		{Specifier: "@scope/continued", Source: "escaped.ts", Line: 3},
		{Specifier: "@scope/later", Source: "escaped.ts", Line: 5},
	}
	if !reflect.DeepEqual(imports, want) {
		t.Fatalf("cooked imports = %#v, want %#v", imports, want)
	}
}

func TestSourceImportsStopsNamespaceAliasAtStatementBoundary(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "aliases.ts"), `import Foo = Namespace.Bar
import { Real } from "@real/pkg"
export { Real }
`)

	imports, err := sourceImports(context.Background(), root, bun)
	if err != nil {
		t.Fatal(err)
	}
	want := []workspace.Import{{Specifier: "@real/pkg", Source: "aliases.ts", Line: 2}}
	if !reflect.DeepEqual(imports, want) {
		t.Fatalf("namespace alias imports = %#v, want %#v", imports, want)
	}
}

func TestSourceImportsStopsSuccessiveExportsAtTheirStatementBoundary(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "LocalProcess.ts"), `export const one = 1
export function two() { return 2 }
export type Three = string
export {
  one,
  two,
}
export {
  one,
  two,
} from
  "#src/internal/local-process.ts"
`)

	imports, err := sourceImports(context.Background(), root, bun)
	if err != nil {
		t.Fatal(err)
	}
	want := []workspace.Import{{Specifier: "#src/internal/local-process.ts", Source: "LocalProcess.ts", Line: 12}}
	if !reflect.DeepEqual(imports, want) {
		t.Fatalf("successive export imports = %#v, want %#v", imports, want)
	}
}
