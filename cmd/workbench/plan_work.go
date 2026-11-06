package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/phosphorco/workbench-go/internal/plan"
)

func resolvePlanActor(p *planInvocation, actors plan.Actors) error {
	required := p.verb == "update" || p.verb == "note" || p.verb == "rule" || p.verb == "adjudicate"
	if p.by == "" && len(actors.Kinds) == 0 {
		if required {
			return fmt.Errorf("plan %s requires --by or configured actor inference", p.verb)
		}
		return nil // Retain legacy optional attribution and verify's workbench fallback.
	}
	var actor plan.Actor
	var err error
	if p.by == "" || p.by == "@me" {
		actor, err = actors.Infer(os.LookupEnv)
	} else {
		actor, err = actors.Resolve(p.by)
	}
	if err != nil {
		return err
	}
	p.actor = &actor
	p.by = actor.String()
	return nil
}

func planActorOwns(p planInvocation, actors plan.Actors, owner string) bool {
	if len(actors.Kinds) == 0 {
		return p.by == owner
	}
	actor, err := actors.Resolve(owner)
	return err == nil && p.actor != nil && actor == *p.actor
}

func planWorkEvent(p planInvocation, d plan.Definition, s plan.State, n plan.Node) (plan.Event, error) {
	if p.expectRevision != nil && *p.expectRevision != s.Work(n.ID).Revision {
		return plan.Event{}, fmt.Errorf("%s work revision is %d, expected %d; inspect tick before retrying", n.ID, s.Work(n.ID).Revision, *p.expectRevision)
	}
	e := plan.Event{Kind: "NodeUpdated", Node: n.ID, By: p.by, Actor: p.actor, Unassign: p.unassign, Status: p.status, Text: p.text}
	if p.assignee != "" {
		if !n.IsAssignable() {
			return e, fmt.Errorf("%s is not assignable", n.ID)
		}
		actor := p.actor
		if p.assignee != "@me" {
			resolved, err := d.Actors.Resolve(p.assignee)
			if err != nil {
				return e, fmt.Errorf("assignee: %w", err)
			}
			actor = &resolved
		}
		if actor == nil {
			return e, fmt.Errorf("--assignee @me requires a resolved caller")
		}
		current := s.Work(n.ID).Assignee
		if current != nil && *current != *actor && !p.reassign {
			return e, fmt.Errorf("%s is assigned to %s; use --reassign to replace the assignee", n.ID, current.String())
		}
		e.Assignee = actor
	}
	if p.status == "stalled" {
		e.Stall = &plan.Stall{Reason: p.reason, ResumeWhen: p.resumeWhen}
		if p.waitingActor != "" {
			actor := p.actor
			if p.waitingActor != "@me" {
				resolved, err := d.Actors.Resolve(p.waitingActor)
				if err != nil {
					return e, fmt.Errorf("waiting-on actor: %w", err)
				}
				actor = &resolved
			}
			e.Stall.WaitingOnActor = actor
		}
		if p.waitingNeed != "" {
			need, err := plan.ParseNeed(p.waitingNeed)
			if err != nil {
				return e, err
			}
			if err := d.ValidateNeed(need); err != nil {
				return e, err
			}
			e.Stall.WaitingOnNeed = &need
		}
	}
	// Unassignment remains possible after a node becomes nonassignable.
	return e, nil
}

func writePlanWork(body *strings.Builder, nodes []plan.WorkView) {
	if len(nodes) == 0 {
		return
	}
	body.WriteString("\n**Reported work (independent of acceptance)**\n")
	for _, n := range nodes {
		w := n.Work
		fmt.Fprintf(body, "- **%s** · %s · revision %d", n.ID, w.Status, w.Revision)
		if w.Assignee != nil {
			fmt.Fprintf(body, " · assignee %s", planLiteral(w.Assignee.String()))
		}
		if w.StatusSince != "" {
			fmt.Fprintf(body, " · since %s", planLiteral(w.StatusSince))
		}
		body.WriteByte('\n')
		if w.Text != "" {
			fmt.Fprintf(body, "Context: %s\n", planLiteral(w.Text))
		}
		if w.Stall == nil {
			continue
		}
		fmt.Fprintf(body, "Reason: %s\nResume when: %s\n", planLiteral(w.Stall.Reason), planLiteral(w.Stall.ResumeWhen))
		fmt.Fprintf(body, "Stall raised at: %s", planLiteral(w.StallRaisedAt))
		if w.StallRaisedBy != nil {
			fmt.Fprintf(body, " · by %s", planLiteral(w.StallRaisedBy.String()))
		}
		body.WriteByte('\n')
		if w.Stall.WaitingOnActor != nil {
			fmt.Fprintf(body, "Waiting on actor: %s\n", planLiteral(w.Stall.WaitingOnActor.String()))
		}
		if w.Stall.WaitingOnNeed != nil {
			fmt.Fprintf(body, "Waiting on need: %s\n", planLiteral(w.Stall.WaitingOnNeed.String()))
			if w.WaitingOnNeedError != "" {
				fmt.Fprintf(body, "Stall reference warning: %s\n", planLiteral(w.WaitingOnNeedError))
			}
			if w.WaitingOnNeedSatisfied != nil && *w.WaitingOnNeedSatisfied {
				body.WriteString("Referenced need satisfied; awaiting explicit resume.\n")
			}
		}
	}
}
