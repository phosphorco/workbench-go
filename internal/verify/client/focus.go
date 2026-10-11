package client

import (
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

type Focus struct {
	Files      []string `json:"files"`
	Codes      []string `json:"codes"`
	Verifiers  []string `json:"verifiers"`
	Units      []string `json:"units"`
	Members    []string `json:"members"`
	Severities []string `json:"severities"`
}

func EmptyFocus() Focus {
	return Focus{[]string{}, []string{}, []string{}, []string{}, []string{}, []string{}}
}
func (f Focus) Unrestricted() bool {
	return len(f.Files)+len(f.Codes)+len(f.Verifiers)+len(f.Units)+len(f.Members)+len(f.Severities) == 0
}
func Merge(a, b Focus) Focus {
	return Focus{union(a.Files, b.Files), union(a.Codes, b.Codes), union(a.Verifiers, b.Verifiers), union(a.Units, b.Units), union(a.Members, b.Members), union(a.Severities, b.Severities)}
}
func union(a, b []string) []string {
	out := []string{}
	for _, list := range [][]string{a, b} {
		for _, s := range list {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}
func within(list []string, s string) bool { return len(list) == 0 || slices.Contains(list, s) }
func Focused(f Focus, s Seen) bool {
	if len(f.Files) > 0 {
		if s.File == nil {
			return false
		}
		matched := false
		for _, pattern := range f.Files {
			negated := false
			for strings.HasPrefix(pattern, "!") {
				negated = !negated
				pattern = strings.TrimPrefix(pattern, "!")
			}
			ok, _ := doublestar.Match(pattern, *s.File)
			if negated {
				ok = !ok
			}
			matched = matched || ok
		}
		if !matched {
			return false
		}
	}
	return (within(f.Codes, s.ErrorKey) || (s.Code != nil && within(f.Codes, *s.Code))) && within(f.Verifiers, s.Verifier) && within(f.Units, s.UnitID) && within(f.Members, s.ErrorKey) && within(f.Severities, s.Severity)
}

func normalizedFocus(f Focus) Focus {
	return Focus{append([]string{}, f.Files...), append([]string{}, f.Codes...), append([]string{}, f.Verifiers...), append([]string{}, f.Units...), append([]string{}, f.Members...), append([]string{}, f.Severities...)}
}
