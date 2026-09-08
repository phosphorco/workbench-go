package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/phosphorco/workbench-go/internal/plan"
)

// Recover the representation even when a later invocation check fails. Values
// of other flags are skipped, so note text cannot select an output format.
func planOutputFormat(arguments []string) string {
	format := "agent"
	if len(arguments) > 0 && arguments[0] == "export" {
		format = "json"
	}
	for i := 1; i < len(arguments); i++ {
		arg := arguments[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, value, inline := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !inline {
			i++
			if i >= len(arguments) {
				break
			}
			value = arguments[i]
		}
		if name == "format" && (value == "agent" || value == "json") {
			format = value
		}
	}
	return format
}

func writePlanAgent(output io.Writer, body string) error {
	_, err := fmt.Fprintf(output, "<workbench-plan>\n%s</workbench-plan>\n", body)
	return err
}

// Markdown quoting preserves source bytes. A fence distinguishes multiline
// literals and quoted boundary markers from the surrounding reader context.
func planLiteral(value string) string {
	width, run := 1, 0
	for _, c := range value {
		if c == '`' {
			run++
			width = max(width, run+1)
		} else {
			run = 0
		}
	}
	if strings.ContainsAny(value, "\n\r") || strings.Contains(value, "workbench-plan>") {
		fence := strings.Repeat("`", max(3, width))
		return "\n" + fence + "\n" + value + "\n" + fence
	}
	fence := strings.Repeat("`", width)
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") {
		return fence + " " + value + " " + fence
	}
	return fence + value + fence
}

func planScope(body *strings.Builder, file, ledger string) {
	fmt.Fprintf(body, "Plan: %s\nLedger: %s\n", planLiteral(file), planLiteral(ledger))
}

func writePlanReport(output io.Writer, p planInvocation, report plan.Report) error {
	if p.format == "json" {
		return plan.WriteJSON(output, report)
	}
	var body strings.Builder
	fmt.Fprintf(&body, "%s · %d complete · %d ready · %d blocked · %d stale\n", planLiteral(report.Meta.Title), report.Counts.Complete, report.Counts.Ready, report.Counts.Blocked, report.Counts.Stale)
	fmt.Fprintf(&body, "Goal: %s\n", planLiteral(report.Meta.Goal))
	planScope(&body, report.Plan, report.Ledger)
	if !report.Valid {
		body.WriteString("\n**Invalid plan — repair diagnostics before acting**\n")
	}
	for _, d := range report.Diagnostics {
		fmt.Fprintf(&body, "- **%s**", d.Code)
		if d.Node != "" {
			fmt.Fprintf(&body, " · %s", planLiteral(d.Node))
		}
		if d.Related != "" {
			fmt.Fprintf(&body, " · related %s", planLiteral(d.Related))
		}
		fmt.Fprintf(&body, ": %s\n", planLiteral(d.Message))
	}
	writePlanNodes(&body, "Ready", report.Ready)
	writePlanNodes(&body, "Blocked", report.Blocked)
	if p.verb == "recall" || p.verb == "export" {
		writePlanNodes(&body, "Complete", report.Complete)
		body.WriteString("\n**Event history**\n")
		if len(report.Events) == 0 {
			body.WriteString("None.\n")
		}
		for _, event := range report.Events {
			writePlanEvent(&body, event)
		}
	} else {
		fmt.Fprintf(&body, "\nCompleted nodes and event history: workbench plan recall %s\n", planLiteral(report.Plan))
	}
	if len(report.CriticalPath) > 0 {
		fmt.Fprintf(&body, "\nCritical path: %s\n", strings.Join(report.CriticalPath, " → "))
	}
	if len(report.LegacyDigests) > 0 {
		fmt.Fprintf(&body, "Legacy evidence digests: %s\n", planLiteral(strings.Join(report.LegacyDigests, ", ")))
	}
	body.WriteString("Readiness does not assign work or grant scope.\n")
	return writePlanAgent(output, body.String())
}

func writePlanNodes(body *strings.Builder, label string, nodes []plan.NodeView) {
	fmt.Fprintf(body, "\n**%s**\n", label)
	if len(nodes) == 0 {
		body.WriteString("None.\n")
	}
	for _, node := range nodes {
		fmt.Fprintf(body, "- **%s** · %s · owner %s", node.ID, node.Kind, planLiteral(node.Owner))
		if node.Outcome != "" {
			fmt.Fprintf(body, " · %s", planLiteral(node.Outcome))
		}
		body.WriteByte('\n')
		if node.Kind == "Action" {
			grant := node.Grant
			if node.EffectiveGrant != nil {
				grant = node.EffectiveGrant
				fmt.Fprintf(body, "Authored scope: %s\n", planLiteral(strings.Join(node.Grant, ", ")))
			}
			if len(grant) == 0 {
				body.WriteString("Scope: none.\n")
			} else {
				fmt.Fprintf(body, "Scope: %s\n", planLiteral(strings.Join(grant, ", ")))
			}
		}
		if len(node.Observes) > 0 {
			fmt.Fprintf(body, "Observes: %s\n", planLiteral(strings.Join(node.Observes, ", ")))
		}
		if len(node.Needs) > 0 {
			fmt.Fprintf(body, "Needs: %s\n", strings.Join(node.Needs, ", "))
		}
		for _, need := range node.Missing {
			state := "missing"
			if slices.Contains(node.Stale, need) {
				state = "stale"
			}
			kind := "Dependency"
			switch {
			case strings.HasPrefix(need, "produced("):
				kind = "Artifact"
			case strings.HasPrefix(need, "verified("):
				kind = "Evidence"
			case strings.HasPrefix(need, "ruled("):
				kind = "Ruling"
			case strings.HasPrefix(need, "granted("):
				kind = "Grant"
			}
			fmt.Fprintf(body, "%s %s: %s\n", kind, state, need)
		}
		if node.Oracle != nil {
			if node.Oracle.Kind == "Mechanical" {
				fmt.Fprintf(body, "Proof: %s", planLiteral(node.Oracle.Run))
				if node.Oracle.Once {
					body.WriteString(" · once")
				}
				body.WriteByte('\n')
			} else {
				fmt.Fprintf(body, "Judgment by %s: %s\n", planLiteral(node.Oracle.Owner), planLiteral(node.Oracle.Rubric))
			}
		}
		if node.Kind == "Selector" {
			fmt.Fprintf(body, "Decision: %s\nOptions: %s\n", planLiteral(node.Question), planLiteral(strings.Join(node.Options, ", ")))
			if node.Probe != "" {
				fmt.Fprintf(body, "Probe: %s\n", planLiteral(node.Probe))
			}
		}
	}
}

type planRecorded struct {
	Plan   string     `json:"plan"`
	Ledger string     `json:"ledger"`
	Event  plan.Event `json:"event"`
}

func writePlanRecorded(output io.Writer, format string, report planRecorded) error {
	if format == "json" {
		return plan.WriteJSON(output, report)
	}
	var body strings.Builder
	planScope(&body, report.Plan, report.Ledger)
	body.WriteString("\n**Recorded**\n")
	writePlanEvent(&body, report.Event)
	return writePlanAgent(output, body.String())
}

type planVerified struct {
	Plan    string             `json:"plan"`
	Ledger  string             `json:"ledger"`
	Results []planVerification `json:"results"`
}

func writePlanVerified(output io.Writer, format string, report planVerified) error {
	if format == "json" {
		return plan.WriteJSON(output, report)
	}
	var body strings.Builder
	planScope(&body, report.Plan, report.Ledger)
	body.WriteString("\n**Verification**\n")
	if len(report.Results) == 0 {
		body.WriteString("No verification results recorded.\n")
	}
	for _, result := range report.Results {
		fmt.Fprintf(&body, "- **%s** · %s · exit %d\n", result.Node, result.Result, result.Exit)
		if result.Event != nil {
			writePlanEvent(&body, *result.Event)
		}
	}
	return writePlanAgent(output, body.String())
}

func writePlanEvent(body *strings.Builder, event plan.Event) {
	fmt.Fprintf(body, "- **%s**", event.Kind)
	for _, field := range []struct{ label, value string }{
		{"node", event.Node}, {"result", event.Result}, {"by", event.By},
		{"choice", event.Choice}, {"paths", strings.Join(event.Paths, ", ")},
		{"reason", event.Reason}, {"text", event.Text}, {"at", event.At}, {"digest", event.Digest},
	} {
		if field.value != "" {
			fmt.Fprintf(body, " · %s %s", field.label, planLiteral(field.value))
		}
	}
	body.WriteByte('\n')
}
