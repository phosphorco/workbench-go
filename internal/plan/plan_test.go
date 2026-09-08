package plan_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/plan"
)

func TestTypeScriptParity(t *testing.T) {
	data, err := os.ReadFile("testdata/typescript-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	type blocked struct {
		ID      string   `json:"id"`
		Missing []string `json:"missing"`
		Stale   []string `json:"stale"`
	}
	var corpus struct {
		Scenarios []struct {
			Name         string                                `json:"name"`
			Definition   json.RawMessage                       `json:"definition"`
			Events       []plan.Event                          `json:"events"`
			Fingerprints []struct{ Kind, Node, Digest string } `json:"fingerprints"`
			Expected     struct {
				Ready, Complete, CriticalPath []string
				Blocked                       []blocked
			} `json:"expected"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range corpus.Scenarios {
		t.Run(fixture.Name, func(t *testing.T) {
			d, err := plan.Decode(fixture.Definition)
			if err != nil {
				t.Fatal(err)
			}
			legacy := map[plan.Need]string{}
			for _, v := range fixture.Fingerprints {
				legacy[plan.Need{Kind: v.Kind, Node: v.Node}] = v.Digest
			}
			d = d.WithLegacyFingerprints(legacy)
			report := plan.Tick(d, fixture.Events, true)
			if len(report.Diagnostics) > 0 {
				t.Fatalf("invalid parity fixture: %+v", report.Diagnostics)
			}
			ids := func(nodes []plan.NodeView) []string {
				ids := []string{}
				for _, n := range nodes {
					ids = append(ids, n.ID)
				}
				return ids
			}
			actualBlocked := []blocked{}
			for _, n := range report.Blocked {
				missing, stale := n.Missing, n.Stale
				if missing == nil {
					missing = []string{}
				}
				if stale == nil {
					stale = []string{}
				}
				actualBlocked = append(actualBlocked, blocked{n.ID, missing, stale})
			}
			for name, pair := range map[string][2]any{
				"ready": {ids(report.Ready), fixture.Expected.Ready}, "complete": {ids(report.Complete), fixture.Expected.Complete},
				"blocked": {actualBlocked, fixture.Expected.Blocked}, "criticalPath": {report.CriticalPath, fixture.Expected.CriticalPath},
			} {
				if !reflect.DeepEqual(pair[0], pair[1]) {
					t.Errorf("%s: got %#v, want %#v", name, pair[0], pair[1])
				}
			}
		})
	}
}

func TestCompactJSONPreservesValuesAndNodeLines(t *testing.T) {
	value := struct {
		Meta  map[string]string   `json:"meta"`
		Ready []map[string]string `json:"ready"`
	}{
		map[string]string{"title": "Quotes \" and <tags> & newlines\nremain data"},
		[]map[string]string{{"id": "api", "oracle": "printf '%s' '{\"x\": [1,2]}'"}, {"id": "view", "outcome": "x -> y"}},
	}
	var first, second bytes.Buffer
	if err := plan.WriteJSON(&first, value); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteJSON(&second, value); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) || !json.Valid(first.Bytes()) {
		t.Fatalf("unstable or invalid JSON: %s", &first)
	}
	var decoded any
	if err := json.Unmarshal(first.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var expected any
	if err := json.Unmarshal(encoded, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, expected) {
		t.Fatal("renderer changed JSON meaning")
	}
	if strings.Contains(first.String(), `\u003c`) {
		t.Fatal("unnecessary HTML escaping")
	}
	for _, line := range strings.Split(first.String(), "\n") {
		if strings.Contains(line, `"id":"api"`) && strings.Contains(line, `"id":"view"`) {
			t.Fatal("nodes must have separate lines")
		}
	}
}

func TestGraphRefusals(t *testing.T) {
	base := `{"meta":{"title":"test","goal":"test"},"nodes":[{"kind":"Action","id":"a","owner":"x","grant":["src/**"],"needs":[],"oracle":{"kind":"Mechanical","run":"true"}},{"kind":"Action","id":"b","owner":"x","grant":["src/file"],"needs":[],"oracle":{"kind":"Mechanical","run":"true"}}]}`
	for _, test := range []struct{ name, text, code string }{
		{"collision", base, "FootprintCollision"},
		{"disconnected", strings.Replace(base, `"src/file"`, `"other/file"`, 1), "DisconnectedComponent"},
		{"duplicate", strings.Replace(base, `"id":"b"`, `"id":"a"`, 1), "DuplicateNodeId"},
		{"dangling", strings.Replace(base, `"needs":[]`, `"needs":[{"kind":"Evidence","node":"absent"}]`, 1), "UnregisteredNeed"},
		{"self cycle", strings.Replace(base, `"needs":[]`, `"needs":[{"kind":"Evidence","node":"a"}]`, 1), "Cycle"},
		{"wrong producer", strings.Replace(base, `"needs":[]`, `"needs":[{"kind":"Fork","node":"b"}]`, 1), "WrongProducerKind"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, err := plan.Decode([]byte(test.text))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, diagnostic := range plan.Check(d, plan.Fold(nil)) {
				if diagnostic.Code == test.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s", test.code)
			}
		})
	}
}

func TestLedgerFailuresAreNotEmptySuccess(t *testing.T) {
	for _, input := range []string{`{"_tag":"Evidence","node":"a","result":"maybe","by":"x"}`, `{"_tag":"FutureKind"}`, `{"_tag":"Note","text":"incomplete"}`, `broken`} {
		if _, err := plan.DecodeLedger([]byte("# header\n" + input)); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("expected located ledger failure: %v", err)
		}
	}
}

func FuzzCompactJSON(f *testing.F) {
	for _, seed := range []string{"plain", "[{}]", "\"quoted\"\nline", "<tag> &", "\\"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		var output bytes.Buffer
		if err := plan.WriteJSON(&output, struct {
			Value string   `json:"value"`
			Rows  []string `json:"rows"`
		}{value, []string{value}}); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(output.Bytes()) {
			t.Fatalf("invalid JSON: %q", output.String())
		}
	})
}

func TestFootprintComparisonKeepsTypeScriptPrefixMeaning(t *testing.T) {
	for _, test := range []struct {
		name, left, right string
		collision         bool
	}{
		{"directory wildcard", "src/**", "src/file.go", true},
		{"directory slash", "src/", "src/file.go", true},
		{"literal filename star", "src/file*", "src/file", false},
		{"sibling prefix", "src/api", "src/api-client", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := plan.Definition{Nodes: []plan.Node{
				{Kind: "Action", ID: "left", Grant: []string{test.left}},
				{Kind: "Action", ID: "right", Grant: []string{test.right}},
			}}
			collision := false
			for _, diagnostic := range plan.Check(d, plan.Fold(nil)) {
				if diagnostic.Code == "FootprintCollision" {
					collision = true
				}
			}
			if collision != test.collision {
				t.Fatalf("collision between %q and %q: got %t, want %t", test.left, test.right, collision, test.collision)
			}
		})
	}
}

func TestCompletedConsumerKeepsItsOwnEvidenceAfterUpstreamEdit(t *testing.T) {
	d, err := plan.Decode([]byte(`{"meta":{"title":"Independent observations","goal":"Do not retract a consumer's own evidence."},"nodes":[
 {"kind":"Guard","id":"producer","owner":"testing","oracle":{"kind":"Mechanical","run":"old proof"}},
 {"kind":"Guard","id":"consumer","owner":"testing","needs":[{"kind":"Evidence","node":"producer"}],"oracle":{"kind":"Mechanical","run":"consumer proof"}}
 ]}`))
	if err != nil {
		t.Fatal(err)
	}
	events := []plan.Event{}
	for _, node := range d.Nodes {
		events = append(events, plan.Event{Kind: "Evidence", Node: node.ID, Result: "pass", By: "testing", Digest: d.Fingerprint(node.Self())})
	}
	d.Nodes[0].Oracle.Run = "new proof"
	report := plan.Tick(d, events, true)
	if len(report.Complete) != 1 || report.Complete[0].ID != "consumer" || len(report.Blocked) != 1 || report.Blocked[0].ID != "producer" || !reflect.DeepEqual(report.Blocked[0].Stale, []string{"verified(producer)"}) {
		t.Fatalf("upstream edit retracted completed consumer: %#v", report)
	}
	events = append(events, plan.Event{Kind: "Evidence", Node: "consumer", Result: "fail", By: "testing"})
	report = plan.Tick(d, events, true)
	if report.Counts.Complete != 0 || len(report.Blocked) != 2 || !reflect.DeepEqual(report.Blocked[1].Stale, []string{"verified(producer)"}) {
		t.Fatalf("consumer failure did not retract own evidence: %#v", report)
	}
}
