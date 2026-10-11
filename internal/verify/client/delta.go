package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/report"
)

type Seen struct {
	Verifier string        `json:"verifier"`
	UnitID   string        `json:"unitId"`
	ErrorKey string        `json:"errorKey"`
	Code     *string       `json:"code,omitempty"`
	Severity string        `json:"severity"`
	File     *string       `json:"file,omitempty"`
	Range    *report.Range `json:"range,omitempty"`
	Anchor   string        `json:"anchor"`
}

type SeenEntry struct {
	Key  string
	Seen Seen
}

// LastSeen keeps occurrence insertion order, including across persistence.
type LastSeen struct {
	Tree        *string
	Occurrences []SeenEntry
}

func (s LastSeen) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"tree":`)
	t, err := encode(s.Tree, false)
	if err != nil {
		return nil, err
	}
	b.Write(t)
	b.WriteString(`,"occurrences":{`)
	for i, e := range s.Occurrences {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := encode(e.Key, false)
		v, err := encode(e.Seen, false)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteString(`}}`)
	return b.Bytes(), nil
}

func (s *LastSeen) UnmarshalJSON(data []byte) error {
	var wire struct {
		Tree        *string         `json:"tree"`
		Occurrences json.RawMessage `json:"occurrences"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(wire.Occurrences))
	t, err := d.Token()
	if err != nil {
		return err
	}
	if t != json.Delim('{') {
		return fmt.Errorf("last-seen occurrences must be an object")
	}
	next := LastSeen{Tree: wire.Tree, Occurrences: []SeenEntry{}}
	indices := map[string]int{}
	for d.More() {
		k, err := d.Token()
		if err != nil {
			return err
		}
		var v Seen
		if err := d.Decode(&v); err != nil {
			return err
		}
		next.Occurrences = upsert(next.Occurrences, indices, SeenEntry{k.(string), v})
	}
	if _, err := d.Token(); err != nil {
		return err
	}
	*s = next
	return nil
}

type Resolved struct {
	OccurrenceKey string  `json:"occurrenceKey"`
	Verifier      string  `json:"verifier"`
	ErrorKey      string  `json:"errorKey"`
	File          *string `json:"file,omitempty"`
	LastAnchor    Anchor  `json:"lastAnchor"`
}

type Gate struct {
	Requires []string `json:"requires"`
	Blocking []string `json:"blocking"`
}
type GateStatus struct {
	Name      string   `json:"name"`
	Satisfied bool     `json:"satisfied"`
	Missing   []string `json:"missing"`
	Blocking  int      `json:"blocking"`
}
type Elsewhere struct {
	New       int `json:"new"`
	Resolved  int `json:"resolved"`
	Unchanged int `json:"unchanged"`
}
type FocusOccurrence struct {
	report.Occurrence
	Seen bool `json:"seen"`
}
type FocusDelta struct {
	Occurrences []FocusOccurrence     `json:"occurrences"`
	Members     []report.MemberStatus `json:"members"`
	Elsewhere   Elsewhere             `json:"elsewhere"`
}
type ObservationSummary struct {
	Tree      string             `json:"tree"`
	Verifiers []observe.Verifier `json:"verifiers"`
}
type Delta struct {
	Observation ObservationSummary  `json:"observation"`
	New         []report.Occurrence `json:"new"`
	Resolved    []Resolved          `json:"resolved"`
	Unchanged   int                 `json:"unchanged"`
	Unknown     []observe.Unknown   `json:"unknown"`
	Focus       *FocusDelta         `json:"focus,omitempty"`
	Gate        *GateStatus         `json:"gate,omitempty"`
}
type FoldInput struct {
	Observation observe.Observation
	Occurrences []report.Occurrence
	Members     []report.MemberStatus
	LastSeen    LastSeen
	Focus       Focus
	Gate        *GateStatus
}

// Fold forgets resolved occurrences regardless of Focus and retains unknown ones.
func Fold(in FoldInput) (Delta, LastSeen) {
	d := Delta{Observation: ObservationSummary{in.Observation.Tree, append([]observe.Verifier{}, in.Observation.Verifiers...)}, New: []report.Occurrence{}, Resolved: []Resolved{}, Unknown: append([]observe.Unknown{}, in.Observation.Unknown...), Gate: in.Gate}
	next := LastSeen{Tree: &in.Observation.Tree, Occurrences: []SeenEntry{}}
	indices := map[string]int{}
	prior := map[string]Seen{}
	present := map[string]bool{}
	for _, e := range in.LastSeen.Occurrences {
		prior[e.Key] = e.Seen
	}
	for _, o := range in.Occurrences {
		present[o.Key] = true
	}
	elsewhere := Elsewhere{}
	for _, e := range in.LastSeen.Occurrences {
		if present[e.Key] {
			continue
		}
		unobserved := slices.ContainsFunc(in.Observation.Unknown, func(u observe.Unknown) bool {
			return u.Verifier == e.Seen.Verifier && (u.UnitID == e.Seen.UnitID || u.UnitID == observe.AllUnits)
		})
		if unobserved {
			next.Occurrences = upsert(next.Occurrences, indices, e)
			continue
		}
		if !Focused(in.Focus, e.Seen) {
			elsewhere.Resolved++
			continue
		}
		anchor := Anchor{Range: e.Seen.Range, Text: e.Seen.Anchor}
		d.Resolved = append(d.Resolved, Resolved{e.Key, e.Seen.Verifier, e.Seen.ErrorKey, e.Seen.File, anchor})
	}
	if !in.Focus.Unrestricted() {
		d.Focus = &FocusDelta{Occurrences: []FocusOccurrence{}, Members: []report.MemberStatus{}}
	}
	for _, o := range in.Occurrences {
		_, wasSeen := prior[o.Key]
		s := seen(o)
		focused := Focused(in.Focus, s)
		if focused {
			if wasSeen {
				d.Unchanged++
			} else {
				d.New = append(d.New, o)
			}
			if d.Focus != nil {
				d.Focus.Occurrences = append(d.Focus.Occurrences, FocusOccurrence{o, wasSeen})
			}
		} else if wasSeen {
			elsewhere.Unchanged++
		} else {
			elsewhere.New++
		}
		if wasSeen || focused {
			next.Occurrences = upsert(next.Occurrences, indices, SeenEntry{o.Key, s})
		}
	}
	if d.Focus != nil {
		d.Focus.Elsewhere = elsewhere
		for _, m := range in.Members {
			if slices.Contains(in.Focus.Members, m.Name) && within(in.Focus.Verifiers, m.Verifier) && within(in.Focus.Units, m.UnitID) {
				d.Focus.Members = append(d.Focus.Members, m)
			}
		}
	}
	return d, next
}
func seen(o report.Occurrence) Seen {
	return Seen{o.Verifier, o.UnitID, o.ErrorKey, o.Code, o.Severity, o.File, o.Range, o.Anchor}
}

// EvaluateGate reads the whole Observation and has no Focus input.
func EvaluateGate(name string, g Gate, o observe.Observation, occurrences []report.Occurrence) GateStatus {
	s := GateStatus{Name: name, Missing: []string{}}
	for _, id := range g.Requires {
		if slices.ContainsFunc(o.Unknown, func(u observe.Unknown) bool { return u.Verifier == id }) || !slices.ContainsFunc(o.Verifiers, func(v observe.Verifier) bool { return v.ID == id }) {
			s.Missing = append(s.Missing, id)
		}
	}
	for _, c := range occurrences {
		if slices.Contains(g.Blocking, c.Severity) {
			s.Blocking++
		}
	}
	s.Satisfied = len(s.Missing) == 0 && s.Blocking == 0
	return s
}

// ExitCode depends on observed truth, never on whether occurrences were seen.
func ExitCode(o observe.Observation, occurrences []report.Occurrence, f Focus, g *GateStatus, blocking []string) int {
	if g != nil && !g.Satisfied {
		return 1
	}
	for _, c := range occurrences {
		if Focused(f, seen(c)) && slices.Contains(blocking, c.Severity) {
			return 1
		}
	}
	for _, u := range o.Unknown {
		if within(f.Verifiers, u.Verifier) && (u.UnitID == observe.AllUnits || within(f.Units, u.UnitID)) {
			return 3
		}
	}
	return 0
}

func upsert(entries []SeenEntry, indices map[string]int, e SeenEntry) []SeenEntry {
	if i, exists := indices[e.Key]; exists {
		entries[i] = e
		return entries
	}
	indices[e.Key] = len(entries)
	return append(entries, e)
}

// Anchor retains the last range when known, otherwise the occurrence anchor text.
type Anchor struct {
	Range *report.Range
	Text  string
}

func (a Anchor) MarshalJSON() ([]byte, error) {
	if a.Range != nil {
		return encode(a.Range, false)
	}
	return encode(a.Text, false)
}
func (a *Anchor) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*a = Anchor{Text: text}
		return nil
	}
	var r report.Range
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	*a = Anchor{Range: &r}
	return nil
}
