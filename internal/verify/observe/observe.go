package observe

import (
	"encoding/json"
	"strings"
)

const AllUnits = "*"

type Requirement struct {
	Name     string
	Verifier string
}

type Verifier struct {
	ID          string        `json:"id"`
	Version     string        `json:"version"`
	DisplayNoun string        `json:"displayNoun"`
	Produces    string        `json:"produces"`
	Requires    []Requirement `json:"-"`
}

type Unit struct {
	UnitID string
	Inputs []string
}

// Selection records selected Units or a failure before selection completed.
type Selection struct {
	Units   []Unit
	Unknown string
}

type UnitKey struct {
	Verifier string
	UnitID   string
}

type VerifierVersion struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type Attestation struct {
	ActionKey string            `json:"actionKey"`
	Verifier  VerifierVersion   `json:"verifier"`
	UnitID    string            `json:"unitId"`
	Exit      int               `json:"exit"`
	Outputs   map[string]string `json:"outputs"`
}

// Provenance has exactly one of Recomputed or a nonempty CarriedFrom Tree.
type Provenance struct {
	Recomputed  bool
	CarriedFrom string
}

func (p Provenance) MarshalJSON() ([]byte, error) {
	if p.Recomputed {
		return json.Marshal("recomputed")
	}
	return json.Marshal(struct {
		CarriedFrom string `json:"carriedFrom"`
	}{p.CarriedFrom})
}

type Result struct {
	Verifier    string      `json:"verifier"`
	UnitID      string      `json:"unitId"`
	Display     string      `json:"display"`
	ActionKey   string      `json:"actionKey"`
	Attestation Attestation `json:"attestation"`
	Provenance  Provenance  `json:"provenance"`
	Scratch     string      `json:"scratch,omitempty"`
	Audit       string      `json:"audit,omitempty"`
}

type Unknown struct {
	Verifier string `json:"verifier"`
	UnitID   string `json:"unitId"`
	Display  string `json:"display"`
	Reason   string `json:"reason"`
}

type OutcomeKind string

const (
	Attested      OutcomeKind = "attested"
	Refused       OutcomeKind = "refused"
	ToolFailure   OutcomeKind = "tool-failure"
	Indeterminate OutcomeKind = "unknown"
)

type Outcome struct {
	Kind        OutcomeKind
	Attestation *Attestation
	Provenance  Provenance
	Reason      string
	Scratch     string
	Audit       string
}

type Observation struct {
	Tree      string     `json:"tree"`
	Verifiers []Verifier `json:"verifiers"`
	Results   []Result   `json:"results"`
	Unknown   []Unknown  `json:"unknown"`
	Complete  bool       `json:"complete"`
}

// Fold is pure: callers supply Verifiers in requires order, selections, and
// outcomes. Complete means coverage, including explicit unknowns; absent
// selections or outcomes leave coverage incomplete. Unknown is never a result.
func Fold(tree string, ordered []Verifier, selections map[string]Selection, outcomes map[UnitKey]Outcome) Observation {
	o := Observation{Tree: tree, Verifiers: make([]Verifier, 0, len(ordered)), Results: []Result{}, Unknown: []Unknown{}, Complete: true}
	artifacts := map[string]string{}
	for _, verifier := range ordered {
		metadata := verifier
		metadata.Requires = nil
		o.Verifiers = append(o.Verifiers, metadata)
		selection, selected := selections[verifier.ID]
		blocked := []string{}
		for _, requirement := range verifier.Requires {
			if artifacts[requirement.Verifier] == "" {
				blocked = append(blocked, "upstream "+requirement.Verifier+" unknown")
			}
		}
		reason := strings.Join(blocked, "; ")
		if reason == "" {
			reason = selection.Unknown
		}
		if reason != "" {
			if len(selection.Units) == 0 {
				o.Unknown = append(o.Unknown, Unknown{verifier.ID, AllUnits, "every " + verifier.DisplayNoun, reason})
			} else {
				for _, unit := range selection.Units {
					o.Unknown = append(o.Unknown, Unknown{verifier.ID, unit.UnitID, unit.UnitID, reason})
				}
			}
			continue
		}
		if !selected {
			o.Complete = false
			continue
		}
		start := len(o.Results)
		for _, unit := range selection.Units {
			outcome, exists := outcomes[UnitKey{verifier.ID, unit.UnitID}]
			if !exists {
				o.Complete = false
				continue
			}
			reason := outcomeReason(verifier, unit, outcome)
			if reason != "" {
				o.Unknown = append(o.Unknown, Unknown{verifier.ID, unit.UnitID, unit.UnitID, reason})
				continue
			}
			a := *outcome.Attestation
			a.Outputs = cloneOutputs(a.Outputs)
			o.Results = append(o.Results, Result{verifier.ID, unit.UnitID, unit.UnitID, a.ActionKey, a, outcome.Provenance, outcome.Scratch, outcome.Audit})
		}
		if verifier.Produces == "artifact" && len(selection.Units) == 1 && len(o.Results)-start == 1 {
			artifacts[verifier.ID] = o.Results[start].Attestation.Outputs["artifact"]
		}
	}
	return o
}

func outcomeReason(verifier Verifier, unit Unit, outcome Outcome) string {
	switch outcome.Kind {
	case Refused, ToolFailure, Indeterminate:
		if outcome.Reason != "" {
			return outcome.Reason
		}
		return string(outcome.Kind)
	case Attested:
		a := outcome.Attestation
		if a == nil || a.ActionKey == "" || a.Verifier.ID != verifier.ID || a.Verifier.Version != verifier.Version || a.UnitID != unit.UnitID || a.Exit != 0 {
			return "invalid attestation"
		}
		if outcome.Provenance.Recomputed == (outcome.Provenance.CarriedFrom != "") {
			return "invalid provenance"
		}
		if verifier.Produces == "artifact" && a.Outputs["artifact"] == "" {
			return "attestation has no artifact"
		}
		if verifier.Produces == "report" && a.Outputs["report.json"] == "" {
			return "attestation has no report.json"
		}
		return ""
	default:
		return "invalid outcome kind"
	}
}

func cloneOutputs(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
