// Package plan derives task readiness from an authored graph and an evidence log.
// It does not dispatch agents or acquire process or filesystem authority.
package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

type Meta struct {
	Title string `json:"title"`
	Goal  string `json:"goal"`
}

type Oracle struct {
	Kind   string `json:"kind"`
	Run    string `json:"run,omitempty"`
	Once   bool   `json:"once,omitempty"`
	Rubric string `json:"rubric,omitempty"`
	Owner  string `json:"owner,omitempty"`
}

type Need struct {
	Kind string `json:"kind"`
	Node string `json:"node"`
}

func (n Need) String() string {
	verb := map[string]string{"Artifact": "produced", "Evidence": "verified", "Fork": "ruled", "Grant": "granted"}[n.Kind]
	return verb + "(" + n.Node + ")"
}

type Node struct {
	Kind     string   `json:"kind"`
	ID       string   `json:"id"`
	Owner    string   `json:"owner"`
	Outcome  string   `json:"outcome,omitempty"`
	Needs    []Need   `json:"needs"`
	Grant    []string `json:"grant,omitempty"`
	Observes []string `json:"observes,omitempty"`
	Oracle   *Oracle  `json:"oracle,omitempty"`
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Probe    *Node    `json:"probe,omitempty"`
}

type Definition struct {
	legacy map[Need]string

	Meta  Meta   `json:"meta"`
	Nodes []Node `json:"nodes"`
}

var nodeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]*$`)

func Decode(data []byte) (Definition, error) {
	var definition Definition
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&definition); err != nil {
		return Definition{}, fmt.Errorf("decode plan: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Definition{}, fmt.Errorf("plan must contain one JSON value")
	}
	if strings.TrimSpace(definition.Meta.Title) == "" || strings.TrimSpace(definition.Meta.Goal) == "" {
		return Definition{}, fmt.Errorf("plan.meta requires title and goal")
	}
	if definition.Nodes == nil {
		return Definition{}, fmt.Errorf("plan.nodes must be a listing")
	}
	for _, n := range definition.Nodes {
		if !nodeID.MatchString(n.ID) || strings.TrimSpace(n.Owner) == "" {
			return Definition{}, fmt.Errorf("node %q requires a valid id and owner", n.ID)
		}
		if n.Kind == "Selector" {
			if n.Oracle != nil || len(n.Grant) > 0 || len(n.Observes) > 0 {
				return Definition{}, fmt.Errorf("Selector %q cannot own an oracle or footprint", n.ID)
			}
		} else if n.Kind == "Action" || n.Kind == "Guard" {
			if n.Probe != nil || len(n.Options) > 0 || n.Question != "" {
				return Definition{}, fmt.Errorf("%s %q cannot own selector fields", n.Kind, n.ID)
			}
			if n.Kind == "Action" && len(n.Observes) > 0 || n.Kind == "Guard" && len(n.Grant) > 0 {
				return Definition{}, fmt.Errorf("%s %q has the wrong footprint field", n.Kind, n.ID)
			}
			if n.Oracle == nil {
				return Definition{}, fmt.Errorf("%s %q requires oracle", n.Kind, n.ID)
			}
			o := n.Oracle
			if o.Kind == "Mechanical" {
				if strings.TrimSpace(o.Run) == "" || o.Rubric != "" || o.Owner != "" {
					return Definition{}, fmt.Errorf("%s.oracle requires Mechanical run only", n.ID)
				}
			} else if o.Kind == "Adjudicated" {
				if strings.TrimSpace(o.Rubric) == "" || strings.TrimSpace(o.Owner) == "" || o.Run != "" || o.Once {
					return Definition{}, fmt.Errorf("%s.oracle requires Adjudicated rubric and owner only", n.ID)
				}
			} else {
				return Definition{}, fmt.Errorf("%s.oracle has unknown kind %q", n.ID, o.Kind)
			}
		} else {
			return Definition{}, fmt.Errorf("node %q has unknown kind %q", n.ID, n.Kind)
		}
	}
	return definition, nil
}

func (d Definition) Find(id string) (Node, bool) {
	for _, n := range d.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

func (n Node) Dependencies() []Need {
	needs := slices.Clone(n.Needs)
	if n.Probe != nil {
		needs = append(needs, Need{Kind: "Evidence", Node: n.Probe.ID})
	}
	return needs
}

func (n Node) Self() Need {
	if n.Kind == "Selector" {
		return Need{Kind: "Fork", Node: n.ID}
	}
	return Need{Kind: "Evidence", Node: n.ID}
}

// Fingerprint binds evidence to meaning. It is portable across hosts and runtimes.
// Legacy evidence without a fingerprint retains its explicit presence-only law.
func (d Definition) FingerprintParts(need Need) []string {
	n, ok := d.Find(need.Node)
	if !ok {
		return nil
	}
	var parts []string
	switch need.Kind {
	case "Artifact":
		parts = slices.Clone(n.Grant)
		if n.Kind == "Guard" {
			parts = slices.Clone(n.Observes)
		}
		slices.Sort(parts)
	case "Fork":
		parts = slices.Clone(n.Options)
		slices.Sort(parts)
	case "Evidence":
		if n.Oracle == nil {
			return nil
		}
		if n.Oracle.Kind == "Mechanical" {
			parts = []string{"Mechanical", n.Oracle.Run}
		} else {
			parts = []string{"Adjudicated", n.Oracle.Rubric, n.Oracle.Owner}
		}
	default:
		return nil
	}
	if parts == nil {
		parts = []string{}
	}
	return parts
}

func (d Definition) Fingerprint(need Need) string {
	parts := d.FingerprintParts(need)
	if parts == nil {
		return ""
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(parts)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))))
}

// WithLegacyFingerprints attaches a disposable compatibility projection of this definition.
func (d Definition) WithLegacyFingerprints(values map[Need]string) Definition {
	d.legacy = make(map[Need]string, len(values))
	for k, v := range values {
		d.legacy[k] = v
	}
	return d
}
