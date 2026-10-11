package selector_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/verify/sandbox"
	"github.com/phosphorco/workbench-go/internal/verify/selector"
)

const bunImage = "oven/bun@sha256:9114c058aeae42162ee16dd5084b95fe9473970bb6bcb5b232ab1630f0546895"
const alpineImage = "alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"

func fixture(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func TestBunGlobParity(t *testing.T) {
	var table struct {
		Paths []string
		Cases []struct {
			Pattern string
			Matches []string
			Units   []struct {
				UnitID string `json:"unitId"`
				Inputs []string
			}
		}
		Whole []struct {
			UnitID string `json:"unitId"`
			Inputs []string
		}
	}
	fixture(t, "globs.json", &table)
	for _, row := range table.Cases {
		t.Run(row.Pattern, func(t *testing.T) {
			units, err := selector.Paths(table.Paths, "v", []string{row.Pattern})
			if err != nil {
				t.Fatal(err)
			}
			if len(units) != 1 || units[0].UnitID != row.Units[0].UnitID || !reflect.DeepEqual(units[0].Inputs, row.Matches) || !reflect.DeepEqual(units[0].Inputs, row.Units[0].Inputs) {
				t.Fatalf("parity: %+v != %+v", units, row)
			}
		})
	}
	whole := selector.WholeTree(table.Paths, "v")
	if !reflect.DeepEqual(whole[0].Inputs, table.Whole[0].Inputs) {
		t.Fatalf("whole parity: %+v", whole)
	}
}

func TestPureSelection(t *testing.T) {
	tracked := []string{"a.ts", "bun.lock", "workspace.pkl"}
	original := append([]string{}, tracked...)
	whole := selector.WholeTree(tracked, "v")
	whole[0].Inputs[0] = "changed"
	units, err := selector.Paths(tracked, "v", []string{"*.ts", "a.*"})
	if err != nil || len(units[0].Inputs) != 1 || units[0].Inputs[0] != "a.ts" || !reflect.DeepEqual(tracked, original) {
		t.Fatalf("not pure: %+v %v %v", units, tracked, err)
	}
	units, err = selector.Paths(tracked, "v", nil)
	if err != nil || len(units) != 1 || len(units[0].Inputs) != 0 {
		t.Fatalf("empty: %+v %v", units, err)
	}
	if _, err := selector.Paths(tracked, "v", []string{"["}); err == nil {
		t.Fatal("invalid pattern accepted")
	}
}

func TestUntrackedInputRefused(t *testing.T) {
	units, err := selector.Decode([]byte(`{"units":[{"unitId":"u","inputs":["tracked","untracked"]}]}`), []string{"tracked"})
	if err == nil || units != nil || !strings.Contains(err.Error(), "untracked") {
		t.Fatalf("accepted: %+v %v", units, err)
	}
}

func TestMalformedSelectJSONRefused(t *testing.T) {
	cases := []string{"", "{", "null", "[]", `{}`, `{"units":null}`, `{"units":{}}`, `{"units":[null]}`, `{"units":[[]]}`, `{"units":[{}]}`, `{"units":[{"unitId":null,"inputs":[]}]}`, `{"units":[{"unitId":"","inputs":[]}]}`, `{"units":[{"unitId":"u","inputs":null}]}`, `{"units":[{"unitId":"u","inputs":[1]}]}`, `{"units":[{"unitId":"u","inputs":[],"display":null}]}`, `{"units":[]} {}`, `{"units":[],"units":[]}`, `{"units":[{"unitId":"u","unitId":"v","inputs":[]}]}`, "{\"units\":[]}\xff"}
	for _, data := range cases {
		t.Run(data, func(t *testing.T) {
			if units, err := selector.Decode([]byte(data), nil); err == nil || units != nil {
				t.Fatalf("accepted: %s %+v %v", data, units, err)
			}
		})
	}
}

func TestDuplicateUnitIDsRefused(t *testing.T) {
	if units, err := selector.Decode([]byte(`{"units":[{"unitId":"u","inputs":[]},{"unitId":"u","inputs":[]}]}`), nil); err == nil || units != nil {
		t.Fatalf("accepted: %+v %v", units, err)
	}
}

func TestEscapingPathsRefusedEvenIfTracked(t *testing.T) {
	for _, p := range []string{"../a", "a/../b", "/absolute", "a//b", "./a", ".", "", "a\\b", "a\x00b"} {
		t.Run(p, func(t *testing.T) {
			data, _ := json.Marshal(map[string]any{"units": []any{map[string]any{"unitId": "u", "inputs": []string{p}}}})
			if units, err := selector.Decode(data, []string{p}); err == nil || units != nil {
				t.Fatalf("accepted: %+v %v", units, err)
			}
		})
	}
}

func TestDecodeSortsDeduplicatesPreservesOrderAndUsesReferenceDisplay(t *testing.T) {
	units, err := selector.Decode([]byte(`{"units":[{"unitId":"z","display":"project z","inputs":["b","a","b"]},{"unitId":"a","inputs":[]}]}`), []string{"a", "b"})
	want := []selector.Unit{{UnitID: "z", Display: "z", Inputs: []string{"a", "b"}}, {UnitID: "a", Display: "a", Inputs: []string{}}}
	if err != nil || !reflect.DeepEqual(units, want) {
		t.Fatalf("%+v %v", units, err)
	}
}

func invocation(t *testing.T, script string) sandbox.Invocation {
	t.Helper()
	return sandbox.Invocation{Image: alpineImage, Command: []string{"sh", "-c", script}, WorkDir: t.TempDir(), VerifierDir: t.TempDir(), OutputDir: t.TempDir(), Network: "none", Timeout: 30 * time.Second}
}

func TestOwnProtocolAndReadOnlyWholeTree(t *testing.T) {
	in := invocation(t, `set -eu; test "$VERIFY_ROLE" = select; test "$VERIFY_UNIT" = '*'; test "$(cat)" = '{"unitId":"*"}'; test ! -e /scratch; test "$(cat /work/a)" = tracked; if touch /work/changed; then exit 9; fi; printf '{"units":[{"unitId":"u","inputs":["a"]}]}'`)
	in.Role, in.UnitID, in.ScratchDir = "run", "original", t.TempDir()
	if err := os.WriteFile(filepath.Join(in.WorkDir, "a"), []byte("tracked"), 0644); err != nil {
		t.Fatal(err)
	}
	units, outcome, err := selector.Own(context.Background(), []string{"a"}, in)
	if err != nil || len(units) != 1 || units[0].Inputs[0] != "a" || outcome.ExitCode != 0 || string(outcome.Stdout) != `{"units":[{"unitId":"u","inputs":["a"]}]}` {
		t.Fatalf("%+v %+v %v", units, outcome, err)
	}
}

func TestOwnRefusesMissingMalformedUntrackedAndFailedSelection(t *testing.T) {
	scripts := []string{
		`true`,
		`printf malformed`,
		`printf '{"units":[{"unitId":"u","inputs":["untracked"]}]}'`,
		`printf '{"units":[]}' > /out/select.json`,
		`printf '{"units":[]}' > /out/real; ln -s real /out/select.json`,
		`mkdir /out/select.json`,
		`printf '{"units":[]}'; exit 23`,
	}
	for _, script := range scripts {
		t.Run(script, func(t *testing.T) {
			units, _, err := selector.Own(context.Background(), nil, invocation(t, script))
			if err == nil || units != nil {
				t.Fatalf("accepted: %+v %v", units, err)
			}
		})
	}
}

func TestOwnDemoBunTestReferenceUnits(t *testing.T) {
	var reference struct {
		Tracked []string
		Units   []selector.Unit
	}
	fixture(t, "demo-units.json", &reference)
	absolute, err := filepath.Abs("testdata/demo")
	if err != nil {
		t.Fatal(err)
	}
	in := invocation(t, "")
	in.Command = []string{"bun", "/verifier/run.ts"}
	in.Image = bunImage
	in.WorkDir = filepath.Join(absolute, "work")
	in.VerifierDir = filepath.Join(in.WorkDir, "verifiers/bun-test")
	in.Upstream = []sandbox.Upstream{{Name: "workspace-sync", HostDir: filepath.Join(absolute, "workspace-sync")}, {Name: "bun-install", HostDir: t.TempDir()}}
	units, outcome, err := selector.Own(context.Background(), reference.Tracked, in)
	if err != nil || !reflect.DeepEqual(units, reference.Units) {
		t.Fatalf("demo units: %+v; reference: %+v; stderr: %s; error: %v", units, reference.Units, outcome.Stderr, err)
	}
}

func TestSelectActionExposesCompleteKeyInputsWithoutAliasing(t *testing.T) {
	whole := []selector.Input{{Path: "b", Digest: "blob-b", Mode: "120000"}, {Path: "a", Digest: "blob-a", Mode: "100755"}}
	spec := selector.RunSpec{VerifierID: "v", Version: "version", ImageDigest: "image", Command: []string{"tool", "select"}, Program: []selector.Input{{Path: "z", Digest: "program", Mode: "100644"}}, Upstream: []selector.Upstream{{Name: "z", Digest: "artifact-z"}, {Name: "a", Digest: "artifact-a"}}}
	action := selector.SelectAction(spec, whole)
	if action.Role != "select" || action.UnitID != "*" || action.VerifierID != spec.VerifierID || action.Version != spec.Version || action.ImageDigest != spec.ImageDigest || !reflect.DeepEqual(action.Command, spec.Command) || action.Inputs[0] != whole[1] || action.Inputs[1] != whole[0] || action.Program[0] != spec.Program[0] || action.Upstream[0] != spec.Upstream[1] {
		t.Fatalf("incomplete: %+v", action)
	}
	action.Inputs[0].Digest = "changed"
	action.Program[0].Digest = "changed"
	action.Upstream[0].Digest = "changed"
	action.Command[0] = "changed"
	if whole[1].Digest != "blob-a" || spec.Program[0].Digest != "program" || spec.Upstream[1].Digest != "artifact-a" || spec.Command[0] != "tool" {
		t.Fatal("aliased key inputs")
	}
}

func TestOwnDecodesStdoutRegardlessOfSelectJSONSymlink(t *testing.T) {
	in := invocation(t, `printf '{"units":[]}'; ln -s /host/not-granted /out/select.json`)
	units, outcome, err := selector.Own(context.Background(), nil, in)
	if err != nil || len(units) != 0 || len(outcome.Outputs) != 1 || outcome.Outputs[0].Mode != "120000" || string(outcome.Outputs[0].Bytes) != "/host/not-granted" {
		t.Fatalf("%+v %+v %v", units, outcome, err)
	}
}
