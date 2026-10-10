package observe_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/verify/observe"
)

func attested(v observe.Verifier, unit, output string, p observe.Provenance) observe.Outcome {
	key := "report.json"
	if v.Produces == "artifact" {
		key = "artifact"
	}
	return observe.Outcome{Kind: observe.Attested, Attestation: &observe.Attestation{ActionKey: "action", Verifier: observe.VerifierVersion{ID: v.ID, Version: v.Version}, UnitID: unit, Outputs: map[string]string{key: output}}, Provenance: p}
}

func TestUpstreamUnknownPropagates(t *testing.T) {
	build := observe.Verifier{ID: "build", Version: "v1", DisplayNoun: "build", Produces: "artifact"}
	check := observe.Verifier{ID: "check", Version: "v1", DisplayNoun: "file", Produces: "artifact", Requires: []observe.Requirement{{Name: "tool", Verifier: "build"}}}
	leaf := observe.Verifier{ID: "leaf", Version: "v1", DisplayNoun: "test", Produces: "report", Requires: []observe.Requirement{{Name: "checked", Verifier: "check"}, {Name: "tool", Verifier: "build"}}}
	independent := observe.Verifier{ID: "independent", Version: "v1", Produces: "report"}
	ordered := []observe.Verifier{build, check, leaf, independent}
	selections := map[string]observe.Selection{"build": {Units: []observe.Unit{{UnitID: "b"}}}, "check": {Units: []observe.Unit{{UnitID: "c1"}, {UnitID: "c2"}}}, "leaf": {Units: []observe.Unit{{UnitID: "l"}}}, "independent": {Units: []observe.Unit{{UnitID: "i"}}}}
	for _, kind := range []observe.OutcomeKind{observe.Refused, observe.ToolFailure, observe.Indeterminate} {
		outcomes := map[observe.UnitKey]observe.Outcome{
			{Verifier: "build", UnitID: "b"}:       {Kind: kind, Reason: "tool broke"},
			{Verifier: "check", UnitID: "c1"}:      attested(check, "c1", "stale", observe.Provenance{Recomputed: true}),
			{Verifier: "leaf", UnitID: "l"}:        attested(leaf, "l", "stale", observe.Provenance{Recomputed: true}),
			{Verifier: "independent", UnitID: "i"}: attested(independent, "i", "report", observe.Provenance{CarriedFrom: "prior-tree"}),
		}
		got := observe.Fold("tree", ordered, selections, outcomes)
		if !got.Complete || len(got.Results) != 1 || len(got.Unknown) != 4 {
			t.Fatalf("bad coverage: %+v", got)
		}
		if got.Unknown[0].Reason != "tool broke" || got.Unknown[1].Reason != "upstream build unknown" || got.Unknown[2].Reason != "upstream build unknown" || got.Unknown[3].Reason != "upstream check unknown; upstream build unknown" {
			t.Fatalf("bad propagation: %+v", got.Unknown)
		}
		if got.Results[0].Provenance.CarriedFrom != "prior-tree" {
			t.Fatal("lost provenance")
		}
	}
}

func TestCoverageAndPureFold(t *testing.T) {
	v := observe.Verifier{ID: "v", Version: "1", Produces: "report"}
	selected := map[string]observe.Selection{"v": {Units: []observe.Unit{{UnitID: "a"}, {UnitID: "b"}}}}
	outcomes := map[observe.UnitKey]observe.Outcome{{Verifier: "v", UnitID: "a"}: attested(v, "a", "report", observe.Provenance{Recomputed: true})}
	if got := observe.Fold("tree", []observe.Verifier{v}, selected, outcomes); got.Complete || len(got.Results) != 1 || len(got.Unknown) != 0 {
		t.Fatalf("missing outcome incorrectly covered: %+v", got)
	}
	outcomes[observe.UnitKey{Verifier: "v", UnitID: "b"}] = observe.Outcome{Kind: observe.Indeterminate, Reason: "unknown"}
	before, _ := json.Marshal(outcomes[observe.UnitKey{Verifier: "v", UnitID: "a"}])
	got := observe.Fold("tree", []observe.Verifier{v}, selected, outcomes)
	again := observe.Fold("tree", []observe.Verifier{v}, selected, outcomes)
	if !got.Complete || !reflect.DeepEqual(got, again) {
		t.Fatalf("fold not deterministic: %+v", got)
	}
	got.Results[0].Attestation.Outputs["report.json"] = "changed"
	after, _ := json.Marshal(outcomes[observe.UnitKey{Verifier: "v", UnitID: "a"}])
	if string(before) != string(after) {
		t.Fatal("output aliases caller's attestation")
	}
	if observe.Fold("tree", []observe.Verifier{v}, nil, nil).Complete {
		t.Fatal("absent selection covered")
	}
	if !observe.Fold("tree", []observe.Verifier{v}, map[string]observe.Selection{"v": {}}, nil).Complete {
		t.Fatal("empty selection incomplete")
	}
}

func TestAttestationAndProvenance(t *testing.T) {
	v := observe.Verifier{ID: "v", Version: "1", Produces: "report"}
	selections := map[string]observe.Selection{"v": {Units: []observe.Unit{{UnitID: "a"}}}}
	for _, p := range []observe.Provenance{{Recomputed: true}, {CarriedFrom: "older"}} {
		got := observe.Fold("tree", []observe.Verifier{v}, selections, map[observe.UnitKey]observe.Outcome{{Verifier: "v", UnitID: "a"}: attested(v, "a", "report", p)})
		if len(got.Results) != 1 || got.Results[0].Provenance != p {
			t.Fatalf("lost result: %+v", got)
		}
		bytes, err := json.Marshal(got.Results[0])
		if err != nil {
			t.Fatal(err)
		}
		want := `"provenance":"recomputed"`
		if !p.Recomputed {
			want = `"provenance":{"carriedFrom":"older"}`
		}
		if !strings.Contains(string(bytes), want) {
			t.Fatalf("wrong wire provenance: %s", bytes)
		}
	}
	mutations := []func(*observe.Outcome){
		func(o *observe.Outcome) { o.Attestation = nil }, func(o *observe.Outcome) { o.Attestation.Exit = 1 },
		func(o *observe.Outcome) { o.Attestation.Verifier.ID = "other" }, func(o *observe.Outcome) { o.Attestation.Verifier.Version = "2" },
		func(o *observe.Outcome) { o.Attestation.UnitID = "other" }, func(o *observe.Outcome) { o.Provenance = observe.Provenance{} },
		func(o *observe.Outcome) { o.Provenance.CarriedFrom = "older" }, func(o *observe.Outcome) { o.Attestation.Outputs = nil },
	}
	for _, mutate := range mutations {
		outcome := attested(v, "a", "report", observe.Provenance{Recomputed: true})
		mutate(&outcome)
		got := observe.Fold("tree", []observe.Verifier{v}, selections, map[observe.UnitKey]observe.Outcome{{Verifier: "v", UnitID: "a"}: outcome})
		if len(got.Results) != 0 || len(got.Unknown) != 1 || !got.Complete {
			t.Fatalf("invalid outcome accepted: %+v", got)
		}
	}
}

func TestArtifactAndSelectionFailures(t *testing.T) {
	build := observe.Verifier{ID: "build", Version: "1", Produces: "artifact", DisplayNoun: "build"}
	check := observe.Verifier{ID: "check", Version: "1", Produces: "report", DisplayNoun: "file", Requires: []observe.Requirement{{Verifier: "build"}}}
	ordered := []observe.Verifier{build, check}
	selections := map[string]observe.Selection{"build": {Units: []observe.Unit{{UnitID: "b"}}}, "check": {Units: []observe.Unit{{UnitID: "c"}}}}
	outcomes := map[observe.UnitKey]observe.Outcome{{Verifier: "build", UnitID: "b"}: attested(build, "b", "tool", observe.Provenance{Recomputed: true}), {Verifier: "check", UnitID: "c"}: attested(check, "c", "report", observe.Provenance{Recomputed: true})}
	if got := observe.Fold("tree", ordered, selections, outcomes); len(got.Results) != 2 || len(got.Unknown) != 0 || !got.Complete {
		t.Fatalf("upstream success failed: %+v", got)
	}
	selections["build"] = observe.Selection{Unknown: "selection refused"}
	delete(selections, "check")
	got := observe.Fold("tree", ordered, selections, outcomes)
	if !got.Complete || len(got.Unknown) != 2 || got.Unknown[0].UnitID != "*" || got.Unknown[1].Display != "every file" {
		t.Fatalf("whole unknown lost: %+v", got)
	}
	selections["build"] = observe.Selection{Units: []observe.Unit{{UnitID: "b"}, {UnitID: "b2"}}}
	outcomes[observe.UnitKey{Verifier: "build", UnitID: "b2"}] = attested(build, "b2", "tool2", observe.Provenance{Recomputed: true})
	got = observe.Fold("tree", ordered, selections, outcomes)
	if len(got.Unknown) != 1 || got.Unknown[0].Reason != "upstream build unknown" {
		t.Fatalf("multi-unit artifact consumed: %+v", got)
	}
}
