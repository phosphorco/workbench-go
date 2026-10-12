package client

import (
	"slices"
	"strings"

	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/report"
	"github.com/phosphorco/workbench-go/internal/verify/simplify"
)

type GroupOccurrence struct {
	Line   *int64   `json:"line"`
	Column *int64   `json:"column"`
	Args   []string `json:"args"`
	Status string   `json:"status"`
}
type GroupFile struct {
	Path        string            `json:"path"`
	Occurrences []GroupOccurrence `json:"occurrences"`
}
type Group struct {
	Code          string      `json:"code"`
	ErrorKey      string      `json:"errorKey"`
	Verifier      string      `json:"verifier"`
	Severity      string      `json:"severity"`
	Message       string      `json:"message"`
	Parameterized bool        `json:"parameterized"`
	Files         []GroupFile `json:"files"`
	Counts        Elsewhere   `json:"counts"`
}
type Units struct {
	Total            int      `json:"total"`
	Ran              []string `json:"ran"`
	UnknownVerifiers []string `json:"unknownVerifiers,omitempty"`
}
type GroupUnknown struct {
	Verifier string   `json:"verifier"`
	Units    *int     `json:"units"`
	Scope    string   `json:"scope,omitempty"`
	UnitIDs  []string `json:"unitIds,omitempty"`
	Reason   string   `json:"reason"`
}

// GroupedOutput is the common reading for text and the default JSON output.
type GroupedOutput struct {
	Tree      string         `json:"tree"`
	Units     Units          `json:"units"`
	Groups    []Group        `json:"groups"`
	Unknown   []GroupUnknown `json:"unknown"`
	Gate      *GateStatus    `json:"gate"`
	Elsewhere *Elsewhere     `json:"elsewhere,omitempty"`
}
type taggedOccurrence struct {
	occurrence report.Occurrence
	status     string
}

func groupOccurrences(entries []taggedOccurrence, focus Focus) []Group {
	input := simplify.Input[taggedOccurrence]{Diagnostics: []simplify.Entry[taggedOccurrence]{}}
	for _, entry := range entries {
		o := entry.occurrence
		path := "(" + o.UnitID + ")"
		if o.File != nil {
			path = *o.File
		}
		input.Diagnostics = append(input.Diagnostics, simplify.Entry[taggedOccurrence]{Diagnostic: entry, ErrorMessage: o.Message, ErrorKey: &o.ErrorKey, ErrorCode: o.Code, Verifier: o.Verifier, FilePath: &path})
	}
	result := []Group{}
	for _, simplified := range simplify.Simplify(input).Groups {
		first := simplified.Occurrences[0].Files[0].Occurrences[0].Diagnostic.occurrence
		code := simplified.ErrorKey
		if simplified.ErrorCode != nil {
			code = *simplified.ErrorCode
		}
		group := Group{Code: code, ErrorKey: simplified.ErrorKey, Verifier: first.Verifier, Severity: first.Severity, Message: simplified.ErrorMessage, Parameterized: simplified.IsParameterized, Files: []GroupFile{}}
		for _, root := range simplified.Occurrences {
			for _, file := range root.Files {
				f := GroupFile{Path: file.FilePath, Occurrences: []GroupOccurrence{}}
				for _, item := range file.Occurrences {
					entry := item.Diagnostic
					if !Focused(focus, seen(entry.occurrence)) {
						continue
					}
					occurrence := GroupOccurrence{Args: item.Placeholders, Status: entry.status}
					if r := entry.occurrence.Range; r != nil {
						occurrence.Line = &r.Start.Line
						occurrence.Column = &r.Start.Column
					}
					f.Occurrences = append(f.Occurrences, occurrence)
					switch entry.status {
					case "new":
						group.Counts.New++
					case "resolved":
						group.Counts.Resolved++
					case "unchanged":
						group.Counts.Unchanged++
					}
				}
				if len(f.Occurrences) > 0 {
					group.Files = append(group.Files, f)
				}
			}
		}
		if len(group.Files) > 0 {
			result = append(result, group)
		}
	}
	return result
}
func output(o observe.Observation, d Delta, groups []Group) GroupedOutput {
	tree := o.Tree
	if len(tree) > 8 {
		tree = tree[:8]
	}
	units := Units{Ran: []string{}}
	for _, r := range o.Results {
		units.Total++
		if r.Provenance.Recomputed {
			units.Ran = append(units.Ran, r.Verifier+":"+r.UnitID)
		}
	}
	slices.Sort(units.Ran)
	unknown := []GroupUnknown{}
	type key struct{ verifier, reason string }
	indices := map[key]int{}
	for _, u := range o.Unknown {
		if strings.HasPrefix(u.Reason, "select exit ") {
			u.Reason = "select exited " + strings.TrimPrefix(u.Reason, "select exit ")
		}
		if u.UnitID == observe.AllUnits {
			if !slices.Contains(units.UnknownVerifiers, u.Verifier) {
				units.UnknownVerifiers = append(units.UnknownVerifiers, u.Verifier)
			}
		} else {
			units.Total++
		}
		k := key{u.Verifier, u.Reason}
		i, ok := indices[k]
		if !ok {
			i = len(unknown)
			indices[k] = i
			unknown = append(unknown, GroupUnknown{Verifier: u.Verifier, Reason: u.Reason})
		}
		if u.UnitID == observe.AllUnits {
			unknown[i].Units = nil
			unknown[i].Scope = "all"
			unknown[i].UnitIDs = nil
		} else if unknown[i].Scope != "all" {
			if unknown[i].Units == nil {
				unknown[i].Units = new(int)
			}
			*unknown[i].Units++
			unknown[i].UnitIDs = append(unknown[i].UnitIDs, u.UnitID)
		}
	}
	if groups == nil {
		groups = []Group{}
	}
	out := GroupedOutput{tree, units, groups, unknown, d.Gate, nil}
	if d.Focus != nil {
		out.Elsewhere = &d.Focus.Elsewhere
	}
	return out
}
