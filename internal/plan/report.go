package plan

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
)

type NodeView struct {
	Kind           string   `json:"kind"`
	ID             string   `json:"id"`
	Owner          string   `json:"owner"`
	Outcome        string   `json:"outcome,omitempty"`
	Needs          []string `json:"needs"`
	Grant          []string `json:"grant,omitempty"`
	EffectiveGrant []string `json:"effectiveGrant,omitempty"`
	Observes       []string `json:"observes,omitempty"`
	Oracle         *Oracle  `json:"oracle,omitempty"`
	Question       string   `json:"question,omitempty"`
	Options        []string `json:"options,omitempty"`
	Probe          string   `json:"probe,omitempty"`
	Missing        []string `json:"missing,omitempty"`
	Stale          []string `json:"stale,omitempty"`
}

type Counts struct {
	Complete int `json:"complete"`
	Ready    int `json:"ready"`
	Blocked  int `json:"blocked"`
	Stale    int `json:"stale"`
}

type Report struct {
	Valid         bool         `json:"valid"`
	Plan          string       `json:"plan"`
	Ledger        string       `json:"ledger"`
	Meta          Meta         `json:"meta"`
	Counts        Counts       `json:"counts"`
	Ready         []NodeView   `json:"ready"`
	Blocked       []NodeView   `json:"blocked"`
	CriticalPath  []string     `json:"criticalPath"`
	Complete      []NodeView   `json:"complete,omitempty"`
	Events        []Event      `json:"events,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
	LegacyDigests []string     `json:"legacyDigests,omitempty"`
}

func Tick(d Definition, events []Event, full bool) Report {
	s := Fold(events)
	r := Report{Meta: d.Meta, Ready: []NodeView{}, Blocked: []NodeView{}, CriticalPath: d.CriticalPath(), Diagnostics: Check(d, s), LegacyDigests: LegacyDigests(events)}
	for _, n := range d.Nodes {
		v := NodeView{Kind: n.Kind, ID: n.ID, Owner: n.Owner, Outcome: n.Outcome, Needs: []string{}, Grant: n.Grant, Observes: n.Observes, Oracle: n.Oracle, Question: n.Question, Options: n.Options}
		if n.Probe != nil {
			v.Probe = n.Probe.ID
		}
		for _, need := range n.Dependencies() {
			v.Needs = append(v.Needs, need.String())
		}
		if n.Kind == "Action" {
			effective := s.EffectiveGrant(n)
			if !slices.Equal(effective, n.Grant) {
				v.EffectiveGrant = effective
			}
		}
		if s.Satisfies(d, n.Self()) {
			r.Counts.Complete++
			if full {
				r.Complete = append(r.Complete, v)
			}
			continue
		}
		for _, need := range s.Missing(d, n) {
			v.Missing = append(v.Missing, need.String())
			if s.Stale(d, need) {
				v.Stale = append(v.Stale, need.String())
			}
		}
		if len(v.Stale) > 0 {
			r.Counts.Stale++
		}
		if len(v.Missing) > 0 {
			r.Blocked = append(r.Blocked, v)
		} else {
			r.Ready = append(r.Ready, v)
		}
	}
	r.Valid = len(r.Diagnostics) == 0
	r.Counts.Ready = len(r.Ready)
	r.Counts.Blocked = len(r.Blocked)
	if full {
		r.Events = slices.Clone(events)
	}
	return r
}

// WriteJSON keeps each top-level field and each array record on its own line.
// Node objects stay compact. This is ordinary JSON, including when piped to jq.
func WriteJSON(w io.Writer, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	data := bytes.TrimSpace(buffer.Bytes())
	var output bytes.Buffer
	stack := []byte{}
	quoted := false
	escaped := false
	records := false
	for i, b := range data {
		if !quoted && b == ']' && len(stack) == 2 && records {
			output.WriteByte('\n')
		}
		output.WriteByte(b)
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		if b == '"' {
			quoted = true
			continue
		}
		switch b {
		case '{', '[':
			stack = append(stack, b)
			if b == '[' && len(stack) == 2 {
				records = i+1 < len(data) && data[i+1] == '{'
				if records {
					output.WriteByte('\n')
				}
			}
		case '}', ']':
			stack = stack[:len(stack)-1]
		case ',':
			if len(stack) == 1 || len(stack) == 2 && stack[1] == '[' && records {
				output.WriteByte('\n')
			}
		}
	}
	output.WriteByte('\n')
	_, err := w.Write(output.Bytes())
	return err
}
