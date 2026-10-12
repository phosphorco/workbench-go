package client

import (
	"fmt"
	"strings"
)

type RenderOptions struct {
	All  bool
	Exit int
}

func plural(n int, noun string) string {
	suffix := "s"
	if n == 1 {
		suffix = ""
	}
	return fmt.Sprintf("%d %s%s", n, noun, suffix)
}
func summary(counts Elsewhere, gate *GateStatus, unknown bool) string {
	parts := []string{}
	if counts.New > 0 {
		parts = append(parts, fmt.Sprintf("%d new", counts.New))
	}
	if counts.Resolved > 0 {
		parts = append(parts, fmt.Sprintf("%d resolved", counts.Resolved))
	}
	if counts.Unchanged > 0 {
		parts = append(parts, fmt.Sprintf("%d unchanged", counts.Unchanged))
	}
	if len(parts) == 0 && !unknown {
		parts = append(parts, "clean")
	}
	if gate != nil {
		status := "satisfied"
		if !gate.Satisfied {
			status = "blocked"
		}
		parts = append(parts, gate.Name+" "+status)
	}
	return strings.Join(parts, " · ")
}

// Render reads the grouped fold: status annotates occurrences, never group identity.
func Render(d Delta, options RenderOptions) string {
	out := d.Output
	lines := []string{}
	for _, u := range out.Unknown {
		noun := "unit"
		for _, v := range d.Observation.Verifiers {
			if v.ID == u.Verifier {
				noun = v.DisplayNoun
				break
			}
		}
		scope := "every " + noun
		if u.Units != nil {
			scope = plural(*u.Units, noun)
		}
		lines = append(lines, "Unknown: "+scope+" of "+u.Verifier+" ("+u.Reason+")")
	}
	counts := Elsewhere{}
	for _, g := range out.Groups {
		counts.New += g.Counts.New
		counts.Resolved += g.Counts.Resolved
		counts.Unchanged += g.Counts.Unchanged
	}
	if s := summary(counts, out.Gate, len(out.Unknown) > 0); s != "" {
		lines = append(lines, s)
	}
	if len(out.Unknown) > 0 {
		lines = append(lines, "Rerun workbench verify to retry unknown Units.")
	}
	for _, g := range out.Groups {
		forced := options.All || d.Focus != nil
		if !forced && g.Counts.New+g.Counts.Resolved == 0 {
			continue
		}
		message := strings.ReplaceAll(g.Message, "\n", "\n  ")
		lines = append(lines, g.Severity+" "+g.Code+" ["+g.Verifier+"] "+message)
		for _, file := range g.Files {
			positions := []string{}
			for _, o := range file.Occurrences {
				if !forced && o.Status == "unchanged" {
					continue
				}
				pos := "(file)"
				if o.Line != nil && o.Column != nil {
					pos = fmt.Sprintf("%d:%d", *o.Line, *o.Column)
				}
				line := "    " + o.Status + " " + pos
				if len(o.Args) > 0 {
					line += "  " + strings.Join(o.Args, ", ")
				}
				positions = append(positions, line)
			}
			if len(positions) > 0 {
				lines = append(lines, "  "+file.Path)
				lines = append(lines, positions...)
			}
		}
	}
	if d.Focus != nil {
		for _, m := range d.Focus.Members {
			lines = append(lines, "  "+m.Status+" "+quote(m.Name)+" ["+m.Verifier+"] "+m.UnitID)
		}
		e := d.Focus.Elsewhere
		if e.New+e.Resolved+e.Unchanged > 0 {
			lines = append(lines, fmt.Sprintf("Elsewhere: %d new · %d resolved · %d unchanged", e.New, e.Resolved, e.Unchanged))
		}
	}
	if g := out.Gate; g != nil && len(g.Missing) > 0 {
		lines = append(lines, "Not attested: "+strings.Join(g.Missing, ", "))
	}
	return strings.Join(lines, "\n") + "\n"
}
