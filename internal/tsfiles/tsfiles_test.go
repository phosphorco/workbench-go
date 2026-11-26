package tsfiles

import (
	"reflect"
	"testing"
)

func TestSelectFollowsTypeScriptIncludeSemantics(t *testing.T) {
	tracked := []string{
		"p/src/a.ts", "p/src/b.tsx", "p/src/c.mts", "p/src/d.d.ts", "p/src/e.js", "p/src/data.json",
		"p/src/.hidden/x.ts", "p/src/node_modules/y.ts", "p/src/fixtures/f.ts", "p/__tests__/t.test.ts",
		"p/vite.config.mts", "p/.amp/q.ts", "p/dist/out.d.ts", "p/top.ts", "p/deep/top.ts",
	}
	for name, test := range map[string]struct {
		spec Spec
		want []string
	}{
		"directory include is an implicit glob that skips dot and package folders": {
			Spec{Include: []string{"p/src"}, Directory: "p"},
			[]string{"p/src/a.ts", "p/src/b.tsx", "p/src/c.mts", "p/src/d.d.ts", "p/src/fixtures/f.ts"},
		},
		"excludes match directories and everything below": {
			Spec{Include: []string{"p/src"}, Exclude: []string{"**/fixtures", "p/dist"}, Directory: "p"},
			[]string{"p/src/a.ts", "p/src/b.tsx", "p/src/c.mts", "p/src/d.d.ts"},
		},
		"a literal file include and an explicit dot directory": {
			Spec{Include: []string{"p/vite.config.mts", "p/.amp/**/*.ts"}, Directory: "p"},
			[]string{"p/.amp/q.ts", "p/vite.config.mts"},
		},
		"a single star does not descend": {
			Spec{Include: []string{"p/*.ts"}, Directory: "p"},
			[]string{"p/top.ts"},
		},
		"files are selected whatever the extension and despite excludes": {
			Spec{Files: []string{"p/src/data.json", "p/src/a.ts", "p/missing.ts"}, Exclude: []string{"p/src"}, Directory: "p"},
			[]string{"p/src/a.ts", "p/src/data.json"},
		},
		"no include and no files selects everything below the directory": {
			Spec{Directory: "p/deep"},
			[]string{"p/deep/top.ts"},
		},
		"empty include selects nothing": {
			Spec{Include: []string{}, Files: []string{}, Directory: "p"},
			[]string{},
		},
	} {
		got, err := Select(test.spec, tracked)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: got %q, want %q", name, got, test.want)
		}
	}
}

func TestSelectEdgeCases(t *testing.T) {
	tracked := []string{
		"r/a.ts", "r/b.d.ts", "r/c.d.mts", "r/d.cts", "r/e.mts", "r/f.tsx", "r/g.js", "r/h.json", "r/i.ts.bak",
		"r/x/.cache/k.ts", "r/x/bower_components/l.ts", "r/x/jspm_packages/m.ts", "r/x/node_modules/n.ts", "r/x/ok/o.ts",
		"r/dist-types/p.d.ts", "r/dist-types2/q.ts", "r/fixtures/s.ts", "r/deep/fixtures/t.ts", "r/deep/fixturesque/u.ts",
	}
	for name, test := range map[string]struct {
		spec Spec
		want []string
	}{
		"the extension set is TypeScript's without allowJs": {
			Spec{Include: []string{"r/*"}},
			[]string{"r/a.ts", "r/b.d.ts", "r/c.d.mts", "r/d.cts", "r/e.mts", "r/f.tsx"},
		},
		"a double star never enters dot directories or package folders": {
			Spec{Include: []string{"r/x"}},
			[]string{"r/x/ok/o.ts"},
		},
		"an explicit package folder or dot directory segment is literal; a dotted last segment is a file pattern, not a directory": {
			Spec{Include: []string{"r/x/node_modules", "r/x/.cache/*.ts", "r/x/.cache"}},
			[]string{"r/x/.cache/k.ts", "r/x/node_modules/n.ts"},
		},
		"a directory exclude is a prefix of whole segments only": {
			Spec{Include: []string{"r"}, Exclude: []string{"r/dist-types", "r/**/fixtures", "r/x"}},
			[]string{"r/a.ts", "r/b.d.ts", "r/c.d.mts", "r/d.cts", "r/deep/fixturesque/u.ts", "r/dist-types2/q.ts", "r/e.mts", "r/f.tsx"},
		},
		"an exclude naming nothing excludes nothing": {
			Spec{Include: []string{"r/fixtures"}, Exclude: []string{"r/missing", "r/fixtures/s.d.ts"}},
			[]string{"r/fixtures/s.ts"},
		},
	} {
		got, err := Select(test.spec, tracked)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: got %q, want %q", name, got, test.want)
		}
	}
	if !Excluded([]string{"r/**/fixtures"}, "r/deep/fixtures/t.ts") || Excluded([]string{"r/**/fixtures"}, "r/deep/fixturesque/u.ts") {
		t.Fatal("Excluded disagrees with Select")
	}
}
