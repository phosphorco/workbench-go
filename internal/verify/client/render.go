package client

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/phosphorco/workbench-go/internal/verify/report"
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
func position(r *report.Range) string {
	if r == nil {
		return "(file)"
	}
	return fmt.Sprintf("%d:%d", r.Start.Line, r.Start.Column)
}
func jsLess(a, b string) bool {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}
func grouped(occurrences []FocusOccurrence, indent string, markNew bool) []string {
	type identity struct{ severity, verifier, code, errorKey, message string }
	groups := [][]FocusOccurrence{}
	indices := map[identity]int{}
	for _, o := range occurrences {
		code := o.ErrorKey
		if o.Code != nil {
			code = *o.Code
		}
		key := identity{o.Severity, o.Verifier, code, o.ErrorKey, o.Message}
		i, ok := indices[key]
		if !ok {
			i = len(groups)
			indices[key] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], o)
	}
	lines := []string{}
	for _, group := range groups {
		h := group[0]
		code := h.ErrorKey
		if h.Code != nil {
			code = *h.Code
		}
		lines = append(lines, indent+h.Severity+" "+code+" ["+h.Verifier+"]")
		message := h.Message
		if h.Code != nil && h.ErrorKey != *h.Code && !strings.Contains(message, h.ErrorKey) {
			message = h.ErrorKey + ": " + message
		}
		for _, line := range strings.Split(message, "\n") {
			if line != "" {
				line = indent + "  " + line
			}
			lines = append(lines, line)
		}
		files := map[string][]FocusOccurrence{}
		for _, o := range group {
			file := "(" + o.UnitID + ")"
			if o.File != nil {
				file = *o.File
			}
			files[file] = append(files[file], o)
		}
		names := []string{}
		for file := range files {
			names = append(names, file)
		}
		sort.Slice(names, func(i, j int) bool { return jsLess(names[i], names[j]) })
		for _, file := range names {
			lines = append(lines, indent+"  "+file)
			located := files[file]
			sort.SliceStable(located, func(i, j int) bool {
				var a, b report.Position
				if located[i].Range != nil {
					a = located[i].Range.Start
				}
				if located[j].Range != nil {
					b = located[j].Range.Start
				}
				if a.Line != b.Line {
					return a.Line < b.Line
				}
				return a.Column < b.Column
			})
			for _, o := range located {
				suffix := ""
				if markNew && !o.Seen {
					suffix = "  new"
				}
				lines = append(lines, indent+"    "+position(o.Range)+suffix)
			}
		}
	}
	return lines
}
func resolvedLines(resolved []Resolved) []string {
	lines := []string{}
	for _, r := range resolved {
		file := ""
		if r.File != nil {
			file = *r.File
		}
		anchor := ""
		if r.LastAnchor.Range != nil {
			anchor = ":" + position(r.LastAnchor.Range)
		}
		lines = append(lines, strings.TrimRight("  "+r.ErrorKey+" ["+r.Verifier+"] "+file+anchor, " \t\n\r\v\f\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"))
	}
	return lines
}

// Render follows the reference's error, file, position grouping byte for byte.
func Render(d Delta, options RenderOptions) string {
	unknown := []string{}
	for _, u := range d.Unknown {
		noun := "unit"
		for _, v := range d.Observation.Verifiers {
			if v.ID == u.Verifier {
				noun = v.DisplayNoun
				break
			}
		}
		line := "Unknown: " + noun + " " + u.Display + " [" + u.Verifier + "] — " + u.Reason
		if u.UnitID == "*" {
			line = "Unknown: every " + noun + " of " + u.Verifier + " — " + u.Reason
		}
		unknown = append(unknown, line)
	}
	lines := []string{}
	if options.Exit == 3 {
		lines = append(lines, unknown...)
	}
	if f := d.Focus; f != nil {
		header := "In focus: " + plural(len(f.Occurrences), "occurrence")
		if len(d.New) > 0 {
			header += fmt.Sprintf(", %d new", len(d.New))
		}
		lines = append(lines, header)
		lines = append(lines, grouped(f.Occurrences, "  ", !options.All)...)
		for _, m := range f.Members {
			lines = append(lines, "  "+m.Status+" \""+m.Name+"\" ["+m.Verifier+"] "+m.UnitID)
		}
		if len(d.Resolved) > 0 {
			lines = append(lines, "Resolved in focus")
			lines = append(lines, resolvedLines(d.Resolved)...)
		}
		e := f.Elsewhere
		if e.New+e.Resolved+e.Unchanged > 0 {
			lines = append(lines, fmt.Sprintf("Elsewhere (not shown): %d new, %d resolved, %d unchanged", e.New, e.Resolved, e.Unchanged))
		}
	} else {
		if len(d.New) > 0 {
			header := fmt.Sprintf("%d new", len(d.New))
			if options.All {
				header = plural(len(d.New), "occurrence")
			}
			lines = append(lines, header)
			occurrences := []FocusOccurrence{}
			for _, o := range d.New {
				occurrences = append(occurrences, FocusOccurrence{Occurrence: o})
			}
			lines = append(lines, grouped(occurrences, "", false)...)
		}
		if len(d.Resolved) > 0 {
			lines = append(lines, fmt.Sprintf("%d resolved", len(d.Resolved)))
			lines = append(lines, resolvedLines(d.Resolved)...)
		}
		if !options.All {
			lines = append(lines, fmt.Sprintf("%d unchanged", d.Unchanged))
		}
	}
	if options.Exit != 3 {
		lines = append(lines, unknown...)
	}
	if g := d.Gate; g != nil {
		status := "satisfied"
		if !g.Satisfied {
			reasons := []string{}
			if len(g.Missing) > 0 {
				reasons = append(reasons, "not attested: "+strings.Join(g.Missing, ", "))
			}
			if g.Blocking > 0 {
				reasons = append(reasons, plural(g.Blocking, "blocking occurrence"))
			}
			status = "not satisfied (" + strings.Join(reasons, "; ") + ")"
		}
		lines = append(lines, "Gate "+g.Name+": "+status)
	}
	return strings.Join(lines, "\n") + "\n"
}
