package report_test

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/verify/report"
)

func visibility() report.Visibility {
	return report.Visibility{UnitID: "u", Tree: "5f0c6e1a9b2d4c3e8f7a6b5c4d3e2f1a0b9c8d7e", Files: map[string]bool{"src/a.ts": true, "/秘密/file": true}}
}

func TestMalformedRefused(t *testing.T) {
	malformed := []string{
		`{`, `null`, `[]`, `"x"`, `1`, `{}`, `{"unitId":"u"}`, `{"unitId":"other","diagnostics":[]}`,
		`{"unitId":1,"diagnostics":[]}`, `{"unitId":"u","diagnostics":null}`, `{"unitId":"u","diagnostics":[null]}`,
		`{"unitId":"u","diagnostics":[1]}`, `{"unitId":"u","diagnostics":[],"members":1}`,
		`{"unitId":"u","diagnostics":[],"members":[null]}`, `{"unitId":"u","diagnostics":[],"members":[{"name":"n","status":"maybe"}]}`,
		`{"unitId":"u","diagnostics":[],"members":[{"name":1,"status":"pass"}]}`,
		`{"unitId":"u","diagnostics":[],"adviceById":[]}`, `{"unitId":"u","diagnostics":[],"adviceById":null}`,
		`{"unitId":"u","diagnostics":[]} {}`, `{"unitId":"u","diagnostics":[],"members":null}`,
	}
	fields := []string{
		`"errorKey":1`, `"message":null`, `"severity":"fatal"`, `"code":7`, `"file":null`, `"file":"src/b.ts"`,
		`"range":null`, `"range":{"start":null}`, `"range":{"start":{"line":0,"column":1}}`,
		`"range":{"start":{"line":1}}`, `"range":{"start":{"line":1.5,"column":1}}`,
		`"range":{"start":{"line":9007199254740992,"column":1}}`, `"range":{"start":{"line":"1","column":1}}`,
		`"range":{"start":{"line":1,"column":1},"end":"x"}`,
	}
	for _, field := range fields {
		malformed = append(malformed, `{"unitId":"u","diagnostics":[{"errorKey":"k","severity":"error","message":"m",`+field+`}]}`)
	}
	for i, data := range malformed {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			decoded := report.Decode([]byte(data), visibility())
			if decoded.Refused == "" || decoded.Report != nil {
				t.Fatalf("accepted %s: %+v", data, decoded)
			}
		})
	}
	for _, data := range []string{
		`{"unitId":"u","diagnostics":[{"errorKey":"k","severity":"error","message":"/秘密/file"}]}`,
		`{"unitId":"u","diagnostics":[{"errorKey":"k","severity":"error","message":"m","file":"/秘密/file","range":{"start":{"line":1,"column":1}}}]}`,
	} {
		if d := report.Decode([]byte(data), visibility()); d.Report == nil {
			t.Fatalf("unicode absolute path refused: %+v", d)
		}
	}
}

func TestSandboxPathRefused(t *testing.T) {
	for _, leak := range []string{"/work/x", "/upstream/x/y", "/scratch/cache", "/out/report.json", "/tmp/junit/x", visibility().Tree} {
		for _, value := range []any{
			map[string]any{"unitId": "u", "diagnostics": []any{map[string]any{"errorKey": "k", "severity": "error", "message": leak}}},
			map[string]any{"unitId": "u", "diagnostics": []any{}, "members": []any{map[string]any{"name": leak, "status": "pass"}}},
			map[string]any{"unitId": "u", "diagnostics": []any{}, "adviceById": map[string]any{leak: []any{map[string]any{"nested": leak}}}},
			map[string]any{"unitId": "u", "diagnostics": []any{}, "discarded": map[string]any{leak: "safe"}},
			map[string]any{"unitId": "u", "diagnostics": []any{map[string]any{"errorKey": "k", "severity": "error", "message": "safe", "extra": leak}}},
		} {
			bytes, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if d := report.Decode(bytes, visibility()); d.Report != nil || d.Refused == "" {
				t.Fatalf("leak accepted: %s", bytes)
			}
		}
	}
}

func TestKeyChurnGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name  string
		Files map[string][]int
		Units []struct {
			Verifier string
			UnitID   string `json:"unitId"`
			Report   report.DiagnosticReport
		}
		Occurrences []report.Occurrence
		Members     []report.MemberStatus
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	var first []report.Occurrence
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			units := make([]report.Unit, 0, len(fixture.Units))
			for _, unit := range fixture.Units {
				units = append(units, report.Unit{Verifier: unit.Verifier, UnitID: unit.UnitID, Report: unit.Report})
			}
			files := map[string][]byte{}
			for path, numbers := range fixture.Files {
				for _, n := range numbers {
					files[path] = append(files[path], byte(n))
				}
			}
			got := report.Occurrences(units, files)
			if !reflect.DeepEqual(got, fixture.Occurrences) {
				t.Fatalf("occurrences differ\ngot: %+v\nwant: %+v", got, fixture.Occurrences)
			}
			if gotMembers := report.Members(units); !reflect.DeepEqual(gotMembers, fixture.Members) {
				t.Fatalf("members differ: %+v", gotMembers)
			}
			if fixture.Name == "before" {
				first = got
				if !strings.HasSuffix(got[0].Anchor, ":0") || !strings.HasSuffix(got[2].Anchor, ":1") {
					t.Fatal("duplicate line ordinals missing")
				}
			}
			if fixture.Name == "edited-above" {
				for i := range got {
					if got[i].Key != first[i].Key {
						t.Fatal("edit above changed key")
					}
				}
			}
			if fixture.Name == "changed-line" && got[0].Key == first[0].Key {
				t.Fatal("line edit did not change key")
			}
		})
	}
}

func TestMemberStatuses(t *testing.T) {
	data := []byte(`{"unitId":"u","diagnostics":[],"members":[{"name":"a","status":"pass"},{"name":"b","status":"fail"},{"name":"c","status":"skip"}],"adviceById":{"safe":[1,null,"ok"]}}`)
	d := report.Decode(data, visibility())
	if d.Report == nil {
		t.Fatal(d.Refused)
	}
	got := report.Members([]report.Unit{{Verifier: "v", UnitID: "u", Report: *d.Report}})
	if len(got) != 3 || got[0].Status != "pass" || got[1].Status != "fail" || got[2].Status != "skip" || got[0].Verifier != "v" || got[0].UnitID != "u" {
		t.Fatalf("bad member statuses: %+v", got)
	}
}

func FuzzDecode(f *testing.F) {
	for _, seed := range []string{`{`, `null`, `{"unitId":"u","diagnostics":[]}`, `{"unitId":"u","diagnostics":[null]}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		d := report.Decode(data, visibility())
		if (d.Report == nil) == (d.Refused == "") {
			t.Fatal("decoder did not return exactly one alternative")
		}
	})
}
