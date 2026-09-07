package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Event retains the TypeScript ledger's tags and optional historical fields.
type Event struct {
	Kind   string   `json:"_tag"`
	Node   string   `json:"node,omitempty"`
	Result string   `json:"result,omitempty"`
	By     string   `json:"by,omitempty"`
	Digest string   `json:"digest,omitempty"`
	Choice string   `json:"choice,omitempty"`
	Paths  []string `json:"paths,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Text   string   `json:"text,omitempty"`
	At     string   `json:"at,omitempty"`
}

func DecodeLedger(data []byte) ([]Event, error) {
	events := []Event{}
	for line, raw := range bytes.Split(data, []byte("\n")) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] == '#' {
			continue
		}
		var event Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", line+1, err)
		}
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", line+1, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func (e Event) Validate() error {
	if e.Kind != "Note" && e.Node == "" {
		return fmt.Errorf("%s event requires node", e.Kind)
	}
	switch e.Kind {
	case "Artifact":
	case "Evidence":
		if (e.Result != "pass" && e.Result != "fail") || e.By == "" {
			return fmt.Errorf("Evidence requires result pass|fail and by")
		}
	case "ForkRuled":
		if e.Choice == "" {
			return fmt.Errorf("ForkRuled requires choice")
		}
	case "Granted", "Revoked":
		if len(e.Paths) == 0 || e.Reason == "" {
			return fmt.Errorf("%s requires paths and reason", e.Kind)
		}
	case "Note":
		if e.Text == "" || e.By == "" {
			return fmt.Errorf("Note requires text and by")
		}
	default:
		return fmt.Errorf("unknown ledger event %q", e.Kind)
	}
	return nil
}

type State struct {
	artifacts map[string]Event
	evidence  map[string]Event
	forks     map[string]Event
	grants    map[string][]string
}

func Fold(events []Event) State {
	s := State{map[string]Event{}, map[string]Event{}, map[string]Event{}, map[string][]string{}}
	for _, e := range events {
		switch e.Kind {
		case "Artifact":
			s.artifacts[e.Node] = e
		case "Evidence":
			if e.Result == "pass" {
				s.evidence[e.Node] = e
			} else {
				delete(s.evidence, e.Node)
			}
		case "ForkRuled":
			s.forks[e.Node] = e
		case "Granted":
			s.grants[e.Node] = append(s.grants[e.Node], e.Paths...)
		case "Revoked":
			remaining := slices.DeleteFunc(s.grants[e.Node], func(path string) bool { return slices.Contains(e.Paths, path) })
			if len(remaining) == 0 {
				delete(s.grants, e.Node)
			} else {
				s.grants[e.Node] = remaining
			}
		}
	}
	return s
}

func (s State) observed(need Need) (Event, bool) {
	switch need.Kind {
	case "Artifact":
		e, ok := s.artifacts[need.Node]
		return e, ok
	case "Evidence":
		e, ok := s.evidence[need.Node]
		return e, ok
	case "Fork":
		e, ok := s.forks[need.Node]
		return e, ok
	case "Grant":
		_, ok := s.grants[need.Node]
		return Event{}, ok
	default:
		return Event{}, false
	}
}

func (s State) Satisfies(d Definition, need Need) bool {
	e, ok := s.observed(need)
	if !ok {
		return false
	}
	n, found := d.Find(need.Node)
	if !found {
		return false
	}
	if need.Kind == "Evidence" && n.Oracle != nil && n.Oracle.Once {
		return true
	}
	return e.Digest == "" || e.Digest == d.Fingerprint(need) || e.Digest == d.legacy[need]
}

func (s State) Stale(d Definition, need Need) bool {
	_, observed := s.observed(need)
	return observed && !s.Satisfies(d, need)
}

func (s State) Missing(d Definition, n Node) []Need {
	missing := []Need{}
	if s.Stale(d, n.Self()) {
		missing = append(missing, n.Self())
	}
	for _, need := range n.Dependencies() {
		if !s.Satisfies(d, need) {
			missing = append(missing, need)
		}
	}
	return missing
}

func (s State) EffectiveGrant(n Node) []string {
	return append(slices.Clone(n.Grant), s.grants[n.ID]...)
}

// LegacyDigests identifies evidence requiring the Bun fingerprint compatibility reader.
func LegacyDigests(events []Event) []string {
	ids := []string{}
	for _, e := range events {
		if e.Digest != "" && !strings.HasPrefix(e.Digest, "sha256:") && !slices.Contains(ids, e.Node) {
			ids = append(ids, e.Node)
		}
	}
	return ids
}
