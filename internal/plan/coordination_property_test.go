package plan_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/phosphorco/workbench-go/internal/plan"
)

// Arbitrary histories must preserve the acceptance projection when coordination
// events are inserted, and must replay identically after a JSONL round trip.
func FuzzCoordinationLedgerProjection(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{2, 2, 2, 3, 1, 4, 0})
	f.Add([]byte{10, 23, 12, 25, 11, 24, 5, 6})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 256 {
			input = input[:256]
		}
		d := plan.Definition{Nodes: []plan.Node{{Kind: "Guard", ID: "api", Owner: "reviewer", Oracle: &plan.Oracle{Kind: "Mechanical", Run: "true"}}}}
		actors := []plan.Actor{{Kind: "thread", ID: "thr_a"}, {Kind: "thread", ID: "thr_b"}}
		events, proof := []plan.Event{}, []plan.Event{}
		expectedStatus := "unstarted"
		var expectedAssignee *plan.Actor
		var expectedRevision uint64
		expectedUpdatedAt := ""
		var expectedUpdatedBy *plan.Actor
		expectedStall := false
		expectedSince := ""
		for i, b := range input {
			actor := &actors[(int(b)/13)%len(actors)]
			e := plan.Event{Kind: "NodeUpdated", Node: "api", By: actor.String(), Actor: actor, At: time.Unix(int64(i+1), int64(b)).UTC().Format(time.RFC3339Nano)}
			switch b % 13 {
			case 0:
				e.Status = "in-progress"
			case 1:
				e.Status = "paused"
				e.Text = "Paused"
			case 2:
				e.Status = "stalled"
				e.Stall = &plan.Stall{Reason: "Waiting", ResumeWhen: "Available"}
			case 3:
				e.Assignee = actor
				expectedAssignee = actor
			case 4:
				e.Unassign = true
				expectedAssignee = nil
			case 5:
				e.Kind = "Evidence"
				e.Result = "pass"
				e.Digest = d.Fingerprint(d.Nodes[0].Self())
			case 6:
				e.Kind = "Evidence"
				e.Result = "fail"
			case 7:
				e.Kind = "Note"
				e.Text = string([]rune{'a', rune(b)})
			case 8:
				e.Kind = "Artifact"
			case 9:
				e.Status = "unstarted"
			case 10:
				e.Assignee = actor
				expectedAssignee = actor
				e.Status = "in-progress"
			case 11:
				e.Unassign = true
				expectedAssignee = nil
				e.Status = "paused"
				e.Text = "Handoff"
			case 12:
				e.Assignee = actor
				expectedAssignee = actor
				e.Status = "stalled"
				e.Stall = &plan.Stall{Reason: "Waiting", ResumeWhen: "Available", WaitingOnActor: &actors[(int(b)/13+1)%len(actors)]}
			}
			if e.Kind == "NodeUpdated" {
				expectedRevision++
				expectedUpdatedAt = e.At
				expectedUpdatedBy = actor
			}
			if e.Kind == "NodeUpdated" && e.Status != "" {
				if e.Status != expectedStatus {
					expectedSince = e.At
				}
				expectedStatus = e.Status
				expectedStall = e.Status == "stalled"
			}
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
			events = append(events, e)
			if e.Kind != "NodeUpdated" {
				proof = append(proof, e)
			}
		}
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		for _, e := range events {
			if err := encoder.Encode(e); err != nil {
				t.Fatal(err)
			}
		}
		replayed, err := plan.DecodeLedger(encoded.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		report := plan.Tick(d, events, true)
		replay := plan.Tick(d, replayed, true)
		if !reflect.DeepEqual(report, replay) {
			t.Fatal("JSONL round trip changed projection")
		}
		reference := plan.Tick(d, proof, true)
		if report.Counts != reference.Counts || report.Valid != reference.Valid {
			t.Fatal("coordination affected acceptance")
		}
		w := plan.Fold(replayed).Work("api")
		if w.Status != expectedStatus || w.StatusSince != expectedSince || !reflect.DeepEqual(w.Assignee, expectedAssignee) || (w.Stall != nil) != expectedStall {
			t.Fatalf("history projection diverged: %+v", w)
		}
		if w.Revision != expectedRevision || w.UpdatedAt != expectedUpdatedAt || !reflect.DeepEqual(w.UpdatedBy, expectedUpdatedBy) {
			t.Fatalf("node revision or last updater diverged: %+v", w)
		}
	})
}

// Recognition must be order independent; aliases may never yield an unchecked
// id. Adding a duplicate kind must cause ambiguity, resolved by qualification.
func FuzzActorRecognition(f *testing.F) {
	f.Add("abc", false)
	f.Add("thr_123", true)
	f.Add("\n", false)
	f.Fuzz(func(t *testing.T, suffix string, duplicate bool) {
		if len(suffix) > 200 {
			return
		}
		kind := plan.ActorKind{Pattern: `thr_[A-Za-z0-9_]+`, Aliases: []plan.ActorAlias{
			{Pattern: `@thread:(thr_[A-Za-z0-9_]+)`, Replacement: "$1"},
			{Pattern: `@thread:(thr_[A-Za-z0-9_]+)`, Replacement: "${1}"},
		}}
		a := plan.Actors{Kinds: map[string]plan.ActorKind{"thread": kind}}
		if duplicate {
			a.Kinds["other"] = kind
		}
		if err := a.Validate(); err != nil {
			t.Fatal(err)
		}
		input := "thr_" + suffix
		canonical, err := a.Resolve("thread:" + input)
		alias, aliasErr := a.Resolve("thread:@thread:" + input)
		if (err == nil) != (aliasErr == nil) || err == nil && canonical != alias {
			t.Fatal("alias changed canonical identity")
		}
		if err == nil {
			if canonical.ID != input || canonical.Kind != "thread" {
				t.Fatal("resolved identity changed")
			}
			if _, err := a.Resolve("@thread:" + input); (err != nil) != duplicate {
				t.Fatal("ambiguity did not reflect candidate set")
			}
			// Registry insertion order must not select a different candidate.
			b := plan.Actors{Kinds: map[string]plan.ActorKind{}}
			if duplicate {
				b.Kinds["other"] = kind
			}
			b.Kinds["thread"] = kind
			left, leftErr := a.Resolve(input)
			right, rightErr := b.Resolve(input)
			if !reflect.DeepEqual(left, right) || fmt.Sprint(leftErr) != fmt.Sprint(rightErr) {
				t.Fatal("registry order changed resolution")
			}
		}
	})
}

// Canonical qualification must be a stable representation even when that same
// string also matches an alias. Generated hex ids keep every seed admissible.
func FuzzActorCanonicalRoundTrip(f *testing.F) {
	f.Add("abc")
	f.Add("")
	f.Add("é\x00")
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 128 {
			return
		}
		suffix := hex.EncodeToString([]byte(input)) + "0"
		a := plan.Actors{Kinds: map[string]plan.ActorKind{"thread": {
			Pattern: `id_[0-9a-f]+`,
			Aliases: []plan.ActorAlias{
				{Pattern: `source_([0-9a-f]+)`, Replacement: `id_${1}`},
				{Pattern: `(id_[0-9a-f]+)`, Replacement: `${1}ff`},
			},
		}}}
		if err := a.Validate(); err != nil {
			t.Fatal(err)
		}
		actor, err := a.Resolve("source_" + suffix)
		if err != nil {
			t.Fatal(err)
		}
		expected := plan.Actor{Kind: "thread", ID: "id_" + suffix}
		if actor != expected {
			t.Fatalf("normalization changed id: %v", actor)
		}
		for _, input := range []string{actor.String(), "thread:source_" + suffix} {
			again, err := a.Resolve(input)
			if err != nil || again != actor {
				t.Fatalf("identity failed round trip: %q -> %v %v", input, again, err)
			}
		}
		if _, err := a.Resolve(actor.ID); err == nil {
			t.Fatal("unqualified canonical/alias conflict silently chose an identity")
		}
	})
}

// Go's expansion grammar decides where unbraced capture names end. Exercise
// Unicode boundaries against the actual expansion implementation, not a copy
// of our validator's parser.
func FuzzActorReplacementBoundaries(f *testing.F) {
	for _, r := range []rune{'é', 'a', '0', '_', '-', '\n'} {
		f.Add(uint32(r))
	}
	f.Fuzz(func(t *testing.T, raw uint32) {
		r := rune(raw % (utf8.MaxRune + 1))
		if r == '$' {
			return
		} // Literal dollars use the documented $$ escape.
		suffix := string(r)
		re := regexp.MustCompile(`(x)`)
		match := re.FindStringSubmatchIndex("x")
		for _, replacement := range []string{"id_$1" + suffix, "id_${1}" + suffix} {
			expanded := string(re.ExpandString(nil, replacement, "x", match))
			a := plan.Actors{Kinds: map[string]plan.ActorKind{"test": {
				Pattern: `(?s)id_.+`, Aliases: []plan.ActorAlias{{Pattern: re.String(), Replacement: replacement}},
			}}}
			err := a.Validate()
			unknown := replacement == "id_$1"+suffix && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
			if unknown {
				if expanded != "id_" {
					t.Fatalf("unexpected expansion oracle: %q -> %q", replacement, expanded)
				}
				if err == nil {
					t.Fatalf("unknown reference accepted: %q", replacement)
				}
				continue
			}
			if err != nil {
				t.Fatalf("valid expansion rejected: %q: %v", replacement, err)
			}
			actor, err := a.Resolve("x")
			if err != nil || actor.ID != expanded || actor.ID != "id_x"+suffix {
				t.Fatalf("expansion changed identity: %q -> %v %v, want %q", replacement, actor, err, expanded)
			}
		}
	})
}

// Numeric-looking named captures differ from numeric references: $100 names
// numeric group100, while ${01} can name group01. Go is the reference oracle.
func FuzzActorNumericCaptureReferences(f *testing.F) {
	f.Add(uint32(100), false)
	f.Add(uint32(1), true)
	f.Add(uint32(1000000000), false)
	f.Add(uint32(0), false)
	f.Fuzz(func(t *testing.T, n uint32, leadingZero bool) {
		name := strconv.FormatUint(uint64(n), 10)
		if leadingZero {
			name = "0" + name
		}
		re, err := regexp.Compile("(?P<" + name + ">x)")
		if err != nil {
			t.Fatal(err)
		}
		replacement := "id_${" + name + "}"
		expanded := string(re.ExpandString(nil, replacement, "x", re.FindStringSubmatchIndex("x")))
		a := plan.Actors{Kinds: map[string]plan.ActorKind{"test": {
			Pattern: `id_.+`, Aliases: []plan.ActorAlias{{Pattern: re.String(), Replacement: replacement}},
		}}}
		err = a.Validate()
		if expanded == "id_" {
			if err == nil {
				t.Fatalf("unknown numeric capture accepted: %s", name)
			}
			return
		}
		if expanded != "id_x" || err != nil {
			t.Fatalf("valid named/numeric capture rejected: %s -> %q: %v", name, expanded, err)
		}
		actor, err := a.Resolve("x")
		if err != nil || actor.ID != expanded {
			t.Fatalf("capture %s did not preserve identity: %v %v", name, actor, err)
		}
	})
}
