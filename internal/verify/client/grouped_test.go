package client_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/verify/client"
	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/report"
)

var updateGrouped = flag.Bool("update-grouped", false, "record reviewed client output snapshots")

type groupedFixture struct {
	Name     string
	Input    client.FoldInput
	All      bool
	Blocking []string
	Exit     int
}

func groupFixtures(t *testing.T) []groupedFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/delta.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []groupedFixture
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	makeOccurrence := func(key, file, message string, line int64) report.Occurrence {
		o := occurrence(key, file)
		o.Code = new("TS2339")
		o.ErrorKey = "typescript:TS2339"
		o.Message = message
		o.Range.Start.Line = line
		return o
	}
	a := makeOccurrence("a", "src/money.ts", "Property 'tag' does not exist on type 'Money'.", 12)
	b := makeOccurrence("b", "src/money.ts", "Property 'currency' does not exist on type 'Money'.", 19)
	c := makeOccurrence("c", "src/invoice.ts", "Property 'amount' does not exist on type 'Invoice'.", 5)
	o := observation()
	o.Tree = "0123456789abcdef"
	o.Results = []observe.Result{{Verifier: "lint", UnitID: "src/money.ts", Provenance: observe.Provenance{Recomputed: true}}, {Verifier: "lint", UnitID: "src/invoice.ts", Provenance: observe.Provenance{CarriedFrom: "before"}}}
	_, last := client.Fold(client.FoldInput{Observation: o, Occurrences: []report.Occurrence{b, c}})
	mixed := client.FoldInput{Observation: o, Occurrences: []report.Occurrence{a, b}, LastSeen: last, Gate: &client.GateStatus{Name: "push", Blocking: 2, Missing: []string{}}}
	fixtures = append(fixtures, groupedFixture{Name: "templates-cross-status", Input: mixed, Exit: 1})
	focus := mixed
	focus.Focus = client.Focus{Codes: []string{"TS2339"}}
	fixtures = append(fixtures, groupedFixture{Name: "templates-focus", Input: focus, Exit: 1})
	fixtures = append(fixtures, groupedFixture{Name: "templates-all", Input: mixed, All: true, Exit: 1})
	onlyFile := mixed
	onlyFile.Focus = client.Focus{Files: []string{"src/money.ts"}}
	fixtures = append(fixtures, groupedFixture{Name: "templates-file-focus", Input: onlyFile, Exit: 1})
	unknown := mixed
	unknown.Observation.Unknown = []observe.Unknown{{Verifier: "test", UnitID: "a", Reason: "select exited 1"}, {Verifier: "test", UnitID: "b", Reason: "select exited 1"}, {Verifier: "test", UnitID: "c", Reason: "select exited 1"}, {Verifier: "test", UnitID: "d", Reason: "select exited 1"}}
	unknown.Observation.Verifiers = append(unknown.Observation.Verifiers, observe.Verifier{ID: "test", DisplayNoun: "test file"})
	fixtures = append(fixtures, groupedFixture{Name: "unknown-first-even-when-blocked", Input: unknown, Exit: 1})
	clean := client.FoldInput{Observation: o, Gate: &client.GateStatus{Name: "push", Satisfied: true, Missing: []string{}}}
	fixtures = append(fixtures, groupedFixture{Name: "clean-gate", Input: clean})

	empty := occurrence("empty", "src/empty.txt")
	empty.Message = ""
	_, emptyLast := client.Fold(client.FoldInput{Observation: o, Occurrences: []report.Occurrence{empty}})
	fixtures = append(fixtures, groupedFixture{Name: "empty-message-resolution", Input: client.FoldInput{Observation: o, LastSeen: emptyLast}})
	legacy := emptyLast
	legacy.Occurrences = append([]client.SeenEntry{}, emptyLast.Occurrences...)
	legacy.Occurrences[0].Seen.Message = nil
	fixtures = append(fixtures, groupedFixture{Name: "legacy-session-resolution", Input: client.FoldInput{Observation: o, LastSeen: legacy}})
	return fixtures
}
func snapshotOutputs(t *testing.T, jsonMode bool) {
	t.Helper()
	path := "testdata/grouped-text.json"
	if jsonMode {
		path = "testdata/grouped-json.json"
	}
	expected := map[string]string{}
	if !*updateGrouped {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &expected); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range groupFixtures(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			options := client.Options{Home: t.TempDir(), Session: "snapshot", All: fixture.All, JSON: jsonMode}
			if err := client.WriteLastSeen(client.LastSeenFile(options), fixture.Input.LastSeen); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			exit, err := client.Present(client.Presentation{Options: options, Observation: fixture.Input.Observation, Occurrences: fixture.Input.Occurrences, Members: fixture.Input.Members, Focus: fixture.Input.Focus, Gate: fixture.Input.Gate, Blocking: fixture.Blocking}, &out)
			if err != nil || exit != fixture.Exit {
				t.Fatalf("presentation: exit %d, want %d, err %v", exit, fixture.Exit, err)
			}
			if *updateGrouped {
				expected[fixture.Name] = out.String()
			} else if out.String() != expected[fixture.Name] {
				t.Fatalf("output differs:\n%s\nwant:\n%s", out.String(), expected[fixture.Name])
			}
		})
	}
	if *updateGrouped {
		data, err := json.MarshalIndent(expected, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
func TestGroupedRender(t *testing.T) { snapshotOutputs(t, false) }
func TestGroupedJSON(t *testing.T)   { snapshotOutputs(t, true) }
func TestDeltaCommutesWithGrouping(t *testing.T) {
	fixtures := groupFixtures(t)
	var mixed client.FoldInput
	for _, fixture := range fixtures {
		if fixture.Name == "templates-cross-status" {
			mixed = fixture.Input
		}
	}
	d, _ := client.Fold(mixed)
	resolved := mixed.LastSeen.Occurrences[1].Seen
	all := append([]report.Occurrence{}, mixed.Occurrences...)
	all = append(all, report.Occurrence{Key: "c", Verifier: resolved.Verifier, UnitID: resolved.UnitID, Diagnostic: report.Diagnostic{ErrorKey: resolved.ErrorKey, Code: resolved.Code, Severity: resolved.Severity, Message: *resolved.Message, File: resolved.File, Range: resolved.Range}, Anchor: resolved.Anchor})
	before, _ := client.Fold(client.FoldInput{Observation: mixed.Observation, Occurrences: all})
	after := d.Output.Groups
	if len(after) != 1 || len(before.Output.Groups) != 1 {
		t.Fatal("statuses fragmented the family")
	}
	g := after[0]
	if g.Message != before.Output.Groups[0].Message || !g.Parameterized || g.Counts != (client.Elsewhere{New: 1, Unchanged: 1, Resolved: 1}) {
		t.Fatalf("status changed grouping: %+v", g)
	}
	for i, file := range g.Files {
		for j, occ := range file.Occurrences {
			prior := before.Output.Groups[0].Files[i].Occurrences[j]
			if strings.Join(occ.Args, "\x00") != strings.Join(prior.Args, "\x00") || *occ.Line != *prior.Line {
				t.Fatal("delta changed args or positions")
			}
		}
	}
	focusedInput := mixed
	focusedInput.Focus = client.Focus{Files: []string{"src/money.ts"}}
	focused, _ := client.Fold(focusedInput)
	if len(focused.Output.Groups) != 1 || focused.Output.Groups[0].Message != g.Message || focused.Output.Elsewhere.Resolved != 1 {
		t.Fatal("Focus changed the family template or lost the resolution elsewhere")
	}

	// Last Seen stores the actual occurrence messages, so a later resolution
	// joins its surviving family with its original args and last location.
	_, next := client.Fold(mixed)
	again, _ := client.Fold(client.FoldInput{Observation: mixed.Observation, Occurrences: mixed.Occurrences[:1], LastSeen: next})
	if len(again.Output.Groups) != 1 || again.Output.Groups[0].Counts != (client.Elsewhere{Resolved: 1, Unchanged: 1}) || again.Output.Groups[0].Files[0].Occurrences[1].Status != "resolved" {
		t.Fatal("persisted messages did not commute with grouping")
	}
}
func TestGroupedOptionsAndHelp(t *testing.T) {
	o, err := client.Parse([]string{"--json=observation"}, nil)
	if err != nil || !o.JSON || !o.ObservationJSON {
		t.Fatalf("observation option: %+v %v", o, err)
	}
	o, err = client.Parse([]string{"--json=observation", "--json"}, nil)
	if err != nil || !o.JSON || o.ObservationJSON {
		t.Fatal("last JSON mode must win")
	}
	if !strings.HasPrefix(client.Usage, "workbench verify [--gate <name>] [--json]\n") {
		t.Fatal("help must lead with the next-action options")
	}
}
