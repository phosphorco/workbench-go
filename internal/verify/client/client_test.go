package client_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/verify/client"
	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/report"
)

func TestDeltaGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/delta.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name     string
		Input    client.FoldInput
		All      bool
		Blocking []string
		Exit     int
		Text     string
		NextJSON string
		JSON     string
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			d, next := client.Fold(f.Input)
			exit := client.ExitCode(f.Input.Observation, f.Input.Occurrences, f.Input.Focus, f.Input.Gate, f.Blocking)
			if exit != f.Exit {
				t.Fatalf("exit %d, want %d", exit, f.Exit)
			}
			if got := client.Render(d, client.RenderOptions{All: f.All, Exit: exit}); got != f.Text {
				t.Fatalf("render:\n%q\nwant:\n%q", got, f.Text)
			}
			got, err := client.JSONOutput(f.Input.Observation, f.Input.Occurrences, f.Input.Members, d, f.Input.Focus)
			if err != nil {
				t.Fatal(err)
			}
			if got != f.JSON {
				t.Fatalf("JSON:\n%s\nwant:\n%s", got, f.JSON)
			}
			path := filepath.Join(t.TempDir(), "last-seen.json")
			if err = client.WriteLastSeen(path, next); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != f.NextJSON {
				t.Fatalf("next %s, want %s", raw, f.NextJSON)
			}
			again, err := client.ReadLastSeen(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again, next) {
				t.Fatalf("Last Seen roundtrip: %+v, %+v", again, next)
			}
		})
	}
}
func occurrence(key, file string) report.Occurrence {
	code := "TODO"
	return report.Occurrence{Key: key, Verifier: "lint", UnitID: file, Diagnostic: report.Diagnostic{ErrorKey: "todo", Code: &code, Severity: "error", Message: "TODO", File: &file, Range: &report.Range{Start: report.Position{Line: 2, Column: 3}}}, Anchor: "anchor"}
}
func observation() observe.Observation {
	return observe.Observation{Tree: "tree", Verifiers: []observe.Verifier{{ID: "lint", DisplayNoun: "source file"}}, Results: []observe.Result{}, Unknown: []observe.Unknown{}, Complete: true}
}
func TestResolvedForgottenUnderFocus(t *testing.T) {
	a, b := occurrence("a", "src/a"), occurrence("b", "src/b")
	o := observation()
	_, first := client.Fold(client.FoldInput{Observation: o, Occurrences: []report.Occurrence{a, b}})
	f := client.EmptyFocus()
	f.Files = []string{"src/a"}
	d, next := client.Fold(client.FoldInput{Observation: o, Occurrences: []report.Occurrence{a}, LastSeen: first, Focus: f})
	if len(d.Resolved) != 0 || d.Focus.Elsewhere.Resolved != 1 || len(next.Occurrences) != 1 {
		t.Fatalf("resolution not forgotten: %+v %+v", d, next)
	}
	d, _ = client.Fold(client.FoldInput{Observation: o, Occurrences: []report.Occurrence{a, b}, LastSeen: next})
	if len(d.New) != 1 || d.New[0].Key != "b" {
		t.Fatalf("reappearance is not new: %+v", d)
	}
	f = client.EmptyFocus()
	f.Codes = []string{"TODO"}
	d, next = client.Fold(client.FoldInput{Observation: o, LastSeen: first, Focus: f})
	if len(d.Resolved) != 2 || len(next.Occurrences) != 0 || !strings.Contains(client.Render(d, client.RenderOptions{}), "todo [lint] src/b:2:3") {
		t.Fatalf("code Focus failed resolution: %+v", d)
	}
	d, _ = client.Fold(client.FoldInput{Observation: o, LastSeen: next, Focus: f})
	if len(d.Resolved) != 0 {
		t.Fatal("resolution repeated")
	}
}
func TestIncompleteExitsThree(t *testing.T) {
	o := observation()
	o.Unknown = []observe.Unknown{{Verifier: "lint", UnitID: "src/a", Display: "src/a", Reason: "tool failed"}}
	f := client.EmptyFocus()
	f.Files = []string{"unrelated/**"}
	f.Codes = []string{"OTHER"}
	f.Severities = []string{"warning"}
	exit := client.ExitCode(o, nil, f, nil, []string{"error"})
	if exit != 3 {
		t.Fatalf("unknown excluded by occurrence Focus: %d", exit)
	}
	d, _ := client.Fold(client.FoldInput{Observation: o, Focus: f})
	if !strings.HasPrefix(client.Render(d, client.RenderOptions{Exit: exit}), "Unknown: source file src/a [lint] — tool failed\n") {
		t.Fatal("Unknown must render first")
	}
	f.Units = []string{"other"}
	if client.ExitCode(o, nil, f, nil, []string{"error"}) != 0 {
		t.Fatal("Unit Focus did not exclude known different unit")
	}
	o.Unknown[0].UnitID = "*"
	if client.ExitCode(o, nil, f, nil, []string{"error"}) != 3 {
		t.Fatal("whole verifier unknown excluded by Unit Focus")
	}
	f.Verifiers = []string{"other"}
	if client.ExitCode(o, nil, f, nil, []string{"error"}) != 0 {
		t.Fatal("Verifier Focus did not exclude unknown")
	}
	if client.ExitCode(o, nil, f, &client.GateStatus{Satisfied: false}, []string{"error"}) != 1 {
		t.Fatal("unsatisfied Gate must precede incomplete")
	}
	f = client.EmptyFocus()
	if client.ExitCode(o, []report.Occurrence{occurrence("a", "a")}, f, nil, []string{"error"}) != 1 {
		t.Fatal("blocking must precede incomplete")
	}
}
func TestFocusGrammar(t *testing.T) {
	o, err := client.Parse([]string{"file:src/**", "code:A,,B", "verifier:v,w", "unit:u,v", "member:name: with spaces", "severity:warning,error", "focus:declared", "--gate", "commit", "--all", "--reset", "--json", "--home", "/tmp/client", "--session", "cli"}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !o.All || !o.Reset || !o.JSON || *o.Gate != "commit" || o.Home != "/tmp/client" || o.Session != "cli" {
		t.Fatalf("flags: %+v", o)
	}
	want := client.Focus{Files: []string{"src/**"}, Codes: []string{"A", "B"}, Verifiers: []string{"v", "w"}, Units: []string{"u,v"}, Members: []string{"name: with spaces"}, Severities: []string{"warning", "error"}}
	if !reflect.DeepEqual(o.Focus, want) {
		t.Fatalf("grammar: %+v, want %+v", o.Focus, want)
	}
	named := client.EmptyFocus()
	named.Codes = []string{"B", "C"}
	f, err := client.ResolveFocus(o, map[string]client.Focus{"declared": named})
	if err != nil || !reflect.DeepEqual(f.Codes, []string{"A", "B", "C"}) {
		t.Fatalf("merge: %+v %v", f, err)
	}
	if _, err = client.ResolveFocus(o, nil); err == nil {
		t.Fatal("unknown named Focus accepted")
	}
	for _, args := range [][]string{{"--bogus"}, {"file:"}, {"severity:fatal"}, {"severity:error,"}, {"--home"}, {"--session"}, {"--gate"}, {"bare"}} {
		_, err := client.Parse(args, nil)
		var usage *client.UsageError
		if !errors.As(err, &usage) || usage.ExitCode() != 2 {
			t.Fatalf("usage %v: %v", args, err)
		}
	}
	for _, arg := range []string{"-h", "--help"} {
		o, err := client.Parse([]string{arg}, nil)
		if err != nil || !o.Help {
			t.Fatalf("help %v", err)
		}
	}
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{{nil, "default"}, {map[string]string{"BB_THREAD_ID": "thread"}, "thread"}, {map[string]string{"PHOSPHOR_VERIFY_SESSION": "session", "BB_THREAD_ID": "thread"}, "session"}} {
		o, err := client.Parse(nil, tc.env)
		if err != nil || o.Session != tc.want {
			t.Fatalf("session: %+v %v", o, err)
		}
	}
	data, err := os.ReadFile("testdata/globs.json")
	if err != nil {
		t.Fatal(err)
	}
	var globs []struct {
		Pattern, File string
		Match         bool
	}
	if err = json.Unmarshal(data, &globs); err != nil {
		t.Fatal(err)
	}
	for _, g := range globs {
		t.Run(g.Pattern+"/"+g.File, func(t *testing.T) {
			f := client.EmptyFocus()
			f.Files = []string{g.Pattern}
			s := client.Seen{File: &g.File}
			if got := client.Focused(f, s); got != g.Match {
				t.Fatalf("glob %q on %q: %v, want %v", g.Pattern, g.File, got, g.Match)
			}
		})
	}
}
func TestUnknownRetainsLastLocation(t *testing.T) {
	a := occurrence("a", "src/a")
	o := observation()
	_, last := client.Fold(client.FoldInput{Observation: o, Occurrences: []report.Occurrence{a}})
	for _, unit := range []string{"src/a", "*"} {
		o.Unknown = []observe.Unknown{{Verifier: "lint", UnitID: unit}}
		d, next := client.Fold(client.FoldInput{Observation: o, LastSeen: last})
		if len(d.Resolved) != 0 || !reflect.DeepEqual(next.Occurrences, last.Occurrences) {
			t.Fatalf("unknown resolved previous occurrence: %+v", d)
		}
		o.Unknown = nil
		d, _ = client.Fold(client.FoldInput{Observation: o, LastSeen: next})
		if len(d.Resolved) != 1 {
			t.Fatal("known absence failed to resolve")
		}
	}
}
func TestGateIgnoresFocus(t *testing.T) {
	o := observation()
	status := client.EvaluateGate("commit", client.Gate{Requires: []string{"lint", "missing"}, Blocking: []string{"error"}}, o, []report.Occurrence{occurrence("a", "a")})
	f := client.EmptyFocus()
	f.Files = []string{"elsewhere"}
	if status.Satisfied || status.Blocking != 1 || !reflect.DeepEqual(status.Missing, []string{"missing"}) || client.ExitCode(o, nil, f, &status, []string{"error"}) != 1 {
		t.Fatalf("gate: %+v", status)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write(p []byte) (int, error) { return 0, fmt.Errorf("output failed") }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return 0, nil }
func TestPresentationCommitsOnlyAfterOutput(t *testing.T) {
	o := observation()
	options := client.Options{Home: t.TempDir(), Session: "session / 雪"}
	path := client.LastSeenFile(options)
	if !strings.HasSuffix(path, "sessions/session%20%2F%20%E9%9B%AA/last-seen.json") {
		t.Fatalf("session path: %s", path)
	}
	p := client.Presentation{Options: options, Observation: o, Occurrences: []report.Occurrence{occurrence("a", "a")}, Focus: client.EmptyFocus()}
	if _, err := client.Present(p, brokenWriter{}); err == nil {
		t.Fatal("writer failure accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state written before output: %v", err)
	}
	if _, err := client.Present(p, shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v", err)
	}
	var out bytes.Buffer
	if exit, err := client.Present(p, &out); err != nil || exit != 1 || !strings.HasPrefix(out.String(), "1 new\n") {
		t.Fatalf("present: %d %v %q", exit, err, out.String())
	}
	before, _ := os.ReadFile(path)
	out.Reset()
	if _, err := client.Present(p, &out); err != nil || out.String() != "1 unchanged\n" {
		t.Fatalf("second: %v %q", err, out.String())
	}
	p.Options.All = true
	p.Observation.Tree = "different"
	out.Reset()
	if _, err := client.Present(p, &out); err != nil || !strings.HasPrefix(out.String(), "1 occurrence\n") {
		t.Fatalf("all: %v %q", err, out.String())
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("--all changed Last Seen")
	}
	p.Options.All = false
	p.Options.Reset = true
	out.Reset()
	if _, err := client.Present(p, &out); err != nil || !strings.HasPrefix(out.String(), "1 new\n") {
		t.Fatalf("reset: %v %q", err, out.String())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("atomic temp files remain: %v", entries)
	}
}

func TestJSONResultProvenance(t *testing.T) {
	data, err := os.ReadFile("testdata/results.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err = json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	for i, p := range []observe.Provenance{{Recomputed: true}, {CarriedFrom: "previous-tree"}} {
		o := observe.Observation{Tree: "result", Verifiers: []observe.Verifier{{ID: "lint", Version: "v1", DisplayNoun: "source file", Produces: "report"}, {ID: "test", Version: "v1", DisplayNoun: "test file", Produces: "report"}}, Results: []observe.Result{{Verifier: "lint", UnitID: "a", Display: "a", ActionKey: "action", Attestation: observe.Attestation{ActionKey: "action", Verifier: observe.VerifierVersion{ID: "lint", Version: "v1"}, UnitID: "a", Outputs: map[string]string{"report.json": "report-digest"}}, Provenance: p, Scratch: "absent", Audit: "matched"}}}
		d, _ := client.Fold(client.FoldInput{Observation: o})
		got, err := client.JSONOutput(o, nil, nil, d, client.EmptyFocus())
		if err != nil {
			t.Fatal(err)
		}
		if got != want[i] {
			t.Fatalf("result JSON differs:\n%s\nwant:\n%s", got, want[i])
		}
	}
}

func TestFocusPredicates(t *testing.T) {
	code, file := "CODE", "src/a.txt"
	subject := client.Seen{Verifier: "lint", UnitID: "unit", ErrorKey: "member", Code: &code, Severity: "warning", File: &file}
	matching := client.Focus{Files: []string{"src/**"}, Codes: []string{"CODE"}, Verifiers: []string{"lint"}, Units: []string{"unit"}, Members: []string{"member"}, Severities: []string{"warning"}}
	if !client.Focused(matching, subject) {
		t.Fatal("conjoined Focus did not match")
	}
	for _, f := range []client.Focus{{Files: []string{"other/**"}}, {Codes: []string{"other"}}, {Verifiers: []string{"other"}}, {Units: []string{"other"}}, {Members: []string{"other"}}, {Severities: []string{"error"}}} {
		if client.Focused(f, subject) {
			t.Fatalf("restriction ignored: %+v", f)
		}
	}
	f := client.EmptyFocus()
	f.Codes = []string{"other", "member"}
	if !client.Focused(f, subject) {
		t.Fatal("codes must match the error key too")
	}
	subject.File = nil
	f.Files = []string{"!other/**"}
	if client.Focused(f, subject) {
		t.Fatal("fileless occurrence matched file Focus")
	}
}
