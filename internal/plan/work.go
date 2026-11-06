package plan

import (
	"fmt"
	"strings"
)

// Stall is one current impediment. Historical stalls remain in NodeUpdated events.
type Stall struct {
	Reason         string `json:"reason"`
	ResumeWhen     string `json:"resumeWhen"`
	WaitingOnActor *Actor `json:"waitingOnActor,omitempty"`
	WaitingOnNeed  *Need  `json:"waitingOnNeed,omitempty"`
}

func (s Stall) Validate() error {
	if strings.TrimSpace(s.Reason) == "" || strings.TrimSpace(s.ResumeWhen) == "" {
		return fmt.Errorf("stalled requires --reason and --resume-when")
	}
	if s.WaitingOnActor != nil {
		if err := s.WaitingOnActor.Validate(); err != nil {
			return err
		}
	}
	if s.WaitingOnNeed != nil {
		if _, err := ParseNeed(s.WaitingOnNeed.String()); err != nil {
			return err
		}
	}
	return nil
}

type WorkState struct {
	Revision      uint64 `json:"revision"`
	Assignee      *Actor `json:"assignee,omitempty"`
	Status        string `json:"status"`
	StatusSince   string `json:"statusSince,omitempty"`
	Text          string `json:"text,omitempty"`
	Stall         *Stall `json:"stall,omitempty"`
	StallRaisedAt string `json:"stallRaisedAt,omitempty"`
	StallRaisedBy *Actor `json:"stallRaisedBy,omitempty"`
	// Computed from current evidence, never an automatic resume or graph edge.
	WaitingOnNeedError     string `json:"waitingOnNeedError,omitempty"`
	WaitingOnNeedSatisfied *bool  `json:"waitingOnNeedSatisfied,omitempty"`
	UpdatedAt              string `json:"updatedAt,omitempty"`
	UpdatedBy              *Actor `json:"updatedBy,omitempty"`
}

func ValidWorkStatus(status string) bool {
	return status == "unstarted" || status == "in-progress" || status == "paused" || status == "stalled"
}

func (n Node) IsAssignable() bool { return n.Assignable == nil || *n.Assignable }

func (s State) Work(node string) WorkState {
	w, ok := s.work[node]
	if !ok {
		w.Status = "unstarted"
	}
	return w
}

func ParseNeed(value string) (Need, error) {
	verb, node, ok := strings.Cut(value, "(")
	kind := map[string]string{"produced": "Artifact", "verified": "Evidence", "ruled": "Fork", "granted": "Grant"}[verb]
	if !ok || kind == "" || !strings.HasSuffix(node, ")") || !nodeID.MatchString(strings.TrimSuffix(node, ")")) {
		return Need{}, fmt.Errorf("invalid need %q; use produced(ID), verified(ID), ruled(ID), or granted(ID)", value)
	}
	return Need{Kind: kind, Node: strings.TrimSuffix(node, ")")}, nil
}

func (d Definition) ValidateNeed(need Need) error {
	n, ok := d.Find(need.Node)
	if !ok {
		return fmt.Errorf("waiting-on need references absent node %q", need.Node)
	}
	valid := need.Kind == "Evidence" && n.Kind != "Selector" || need.Kind == "Fork" && n.Kind == "Selector" || (need.Kind == "Artifact" || need.Kind == "Grant") && n.Kind == "Action"
	if !valid {
		return fmt.Errorf("%s has the wrong producer kind %s", need.String(), n.Kind)
	}
	return nil
}

func applyWorkUpdate(w WorkState, e Event) WorkState {
	w.Revision++
	if e.Assignee != nil {
		w.Assignee = e.Assignee
	}
	if e.Unassign {
		w.Assignee = nil
	}
	if e.Status != "" {
		if w.Status != e.Status {
			w.StatusSince = e.At
		}
		w.Status = e.Status
		w.Text = e.Text
		if e.Status == "stalled" {
			// Re-reporting a stall may refine its explanation, but is not a new interval.
			if w.Stall == nil {
				w.StallRaisedAt = e.At
				w.StallRaisedBy = e.Actor
			}
			w.Stall = e.Stall
		} else {
			w.Stall = nil
			w.StallRaisedAt = ""
			w.StallRaisedBy = nil
		}
	}
	w.UpdatedAt = e.At
	w.UpdatedBy = e.Actor
	return w
}
