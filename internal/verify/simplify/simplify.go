package simplify

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

type RootFolder struct {
	Path  string  `json:"path"`
	Label *string `json:"label,omitempty"`
}
type Entry[T any] struct {
	Diagnostic        T        `json:"diagnostic"`
	ErrorMessage      string   `json:"errorMessage"`
	ErrorKey          *string  `json:"errorKey,omitempty"`
	ErrorCode         *string  `json:"errorCode,omitempty"`
	Verifier          string   `json:"verifier,omitempty"`
	FilePath          *string  `json:"filePath,omitempty"`
	Annotations       []string `json:"annotations,omitempty"`
	FormattedPosition *string  `json:"formattedPosition,omitempty"`
}
type Input[T any] struct {
	RootFolders []RootFolder `json:"rootFolders,omitempty"`
	Diagnostics []Entry[T]   `json:"diagnostics"`
}
type Occurrence[T any] struct {
	Diagnostic        T        `json:"diagnostic"`
	OriginalMessage   string   `json:"originalMessage"`
	Placeholders      []string `json:"placeholders"`
	Annotations       []string `json:"annotations"`
	FormattedPosition *string  `json:"formattedPosition,omitempty"`
}
type File[T any] struct {
	FilePath    string          `json:"filePath"`
	Occurrences []Occurrence[T] `json:"occurrences"`
}
type Root[T any] struct {
	RootFolder *string   `json:"rootFolder,omitempty"`
	Files      []File[T] `json:"files"`
}
type Group[T any] struct {
	ErrorKey        string    `json:"errorKey"`
	ErrorCode       *string   `json:"errorCode,omitempty"`
	ErrorMessage    string    `json:"errorMessage"`
	IsParameterized bool      `json:"isParameterized"`
	Occurrences     []Root[T] `json:"occurrences"`
}
type Result[T any] struct {
	Groups []Group[T] `json:"groups"`
}
type quoted struct {
	delimiter rune
	value     string
}
type parsed struct {
	literals []string
	values   []quoted
}

func parse(message string) *parsed {
	p := &parsed{}
	var current strings.Builder
	var delimiter rune
	escaping := false
	for _, c := range message {
		if delimiter != 0 {
			if escaping {
				current.WriteRune(c)
				escaping = false
				continue
			}
			if c == '\\' {
				current.WriteRune(c)
				escaping = true
				continue
			}
			if c == delimiter {
				p.values = append(p.values, quoted{delimiter, current.String()})
				current.Reset()
				delimiter = 0
				continue
			}
			current.WriteRune(c)
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			p.literals = append(p.literals, current.String())
			current.Reset()
			delimiter = c
			continue
		}
		current.WriteRune(c)
	}
	if delimiter != 0 || len(p.values) == 0 {
		return nil
	}
	p.literals = append(p.literals, current.String())
	return p
}

type item[T any] struct {
	entry Entry[T]
	args  []string
}
type messages[T any] struct {
	message       string
	parameterized bool
	items         []item[T]
}

func exact[T any](entries []Entry[T]) []messages[T] {
	out := []messages[T]{}
	indices := map[string]int{}
	for _, e := range entries {
		i, ok := indices[e.ErrorMessage]
		if !ok {
			i = len(out)
			indices[e.ErrorMessage] = i
			out = append(out, messages[T]{message: e.ErrorMessage})
		}
		out[i].items = append(out[i].items, item[T]{e, []string{}})
	}
	return out
}
func groupMessages[T any](entries []Entry[T]) []messages[T] {
	type scaffoldEntry struct {
		entry  Entry[T]
		parsed *parsed
	}
	scaffolds := [][]scaffoldEntry{}
	indices := map[string]int{}
	unquoted := []Entry[T]{}
	for _, e := range entries {
		p := parse(e.ErrorMessage)
		if p == nil {
			unquoted = append(unquoted, e)
			continue
		}
		key, _ := json.Marshal(p.literals)
		i, ok := indices[string(key)]
		if !ok {
			i = len(scaffolds)
			indices[string(key)] = i
			scaffolds = append(scaffolds, nil)
		}
		scaffolds[i] = append(scaffolds[i], scaffoldEntry{e, p})
	}
	out := []messages[T]{}
	for _, scaffold := range scaffolds {
		es := []Entry[T]{}
		for _, s := range scaffold {
			es = append(es, s.entry)
		}
		fallback := exact(es)
		if len(fallback) <= 1 {
			out = append(out, fallback...)
			continue
		}
		first := scaffold[0].parsed
		variable := make([]bool, len(first.values))
		for _, s := range scaffold {
			for i, q := range s.parsed.values {
				if q.value != first.values[i].value {
					variable[i] = true
				}
			}
		}
		var message strings.Builder
		message.WriteString(first.literals[0])
		arg := 1
		for i, q := range first.values {
			value := q.value
			if variable[i] {
				value = fmt.Sprintf("<arg%d>", arg)
				arg++
			}
			message.WriteRune(q.delimiter)
			message.WriteString(value)
			message.WriteRune(q.delimiter)
			message.WriteString(first.literals[i+1])
		}
		cost := 0
		for _, g := range fallback {
			cost += len(utf16.Encode([]rune(g.message)))
		}
		if arg == 1 || len(utf16.Encode([]rune(message.String()))) >= cost {
			out = append(out, fallback...)
			continue
		}
		g := messages[T]{message: message.String(), parameterized: true}
		for _, s := range scaffold {
			args := []string{}
			for i, q := range s.parsed.values {
				if variable[i] {
					args = append(args, q.value)
				}
			}
			g.items = append(g.items, item[T]{s.entry, args})
		}
		out = append(out, g)
	}
	return append(out, exact(unquoted)...)
}
func normalize(path string) string {
	parts := strings.FieldsFunc(strings.ReplaceAll(path, "\\", "/"), func(c rune) bool { return c == '/' })
	normalized := strings.Join(parts, "/")
	if strings.HasPrefix(strings.ReplaceAll(path, "\\", "/"), "/") && normalized != "" {
		normalized = "/" + normalized
	}
	return normalized
}
func groupFiles[T any](items []item[T], roots []RootFolder) []Root[T] {
	out := []Root[T]{}
	rootIndices := map[string]int{}
	fileIndices := map[int]map[string]int{}
	for _, item := range items {
		path := ""
		if item.entry.FilePath != nil {
			path = normalize(*item.entry.FilePath)
		}
		label := ""
		for _, root := range roots {
			if root.Path == "" || path == root.Path || strings.HasPrefix(path, root.Path+"/") {
				if root.Path != "" {
					path = strings.TrimPrefix(strings.TrimPrefix(path, root.Path), "/")
				}
				label = root.Path
				if root.Label != nil {
					label = *root.Label
				}
				break
			}
		}
		r, ok := rootIndices[label]
		if !ok {
			r = len(out)
			rootIndices[label] = r
			root := Root[T]{Files: []File[T]{}}
			if label != "" {
				root.RootFolder = &label
			}
			out = append(out, root)
			fileIndices[r] = map[string]int{}
		}
		f, ok := fileIndices[r][path]
		if !ok {
			f = len(out[r].Files)
			fileIndices[r][path] = f
			out[r].Files = append(out[r].Files, File[T]{FilePath: path, Occurrences: []Occurrence[T]{}})
		}
		out[r].Files[f].Occurrences = append(out[r].Files[f].Occurrences, Occurrence[T]{item.entry.Diagnostic, item.entry.ErrorMessage, item.args, append([]string{}, item.entry.Annotations...), item.entry.FormattedPosition})
	}
	return out
}

// Simplify preserves occurrence truth and first-seen family/file order, using
// the reference's quote scaffold and UTF-16 header cost to select templates.
// Verifier, code, and error key jointly identify a family.
func Simplify[T any](input Input[T]) Result[T] {
	type identity struct {
		verifier string
		code     string
		hasCode  bool
		key      string
	}
	families := [][]Entry[T]{}
	identities := []identity{}
	indices := map[identity]int{}
	for _, e := range input.Diagnostics {
		id := identity{verifier: e.Verifier, key: e.ErrorMessage}
		if e.ErrorCode != nil {
			id.code = *e.ErrorCode
			id.hasCode = true
			id.key = *e.ErrorCode
		}
		if e.ErrorKey != nil {
			id.key = *e.ErrorKey
		}
		i, ok := indices[id]
		if !ok {
			i = len(families)
			indices[id] = i
			families = append(families, nil)
			identities = append(identities, id)
		}
		families[i] = append(families[i], e)
	}
	roots := append([]RootFolder{}, input.RootFolders...)
	for i := range roots {
		roots[i].Path = normalize(roots[i].Path)
	}
	sort.SliceStable(roots, func(i, j int) bool {
		return len(utf16.Encode([]rune(roots[i].Path))) > len(utf16.Encode([]rune(roots[j].Path)))
	})
	out := Result[T]{Groups: []Group[T]{}}
	for i, family := range families {
		for _, g := range groupMessages(family) {
			out.Groups = append(out.Groups, Group[T]{identities[i].key, family[0].ErrorCode, g.message, g.parameterized, groupFiles(g.items, roots)})
		}
	}
	return out
}
