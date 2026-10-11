package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf16"
)

type Position struct {
	Line   int64 `json:"line"`
	Column int64 `json:"column"`
}

type Range struct {
	Start Position  `json:"start"`
	End   *Position `json:"end,omitempty"`
}

type Diagnostic struct {
	ErrorKey string  `json:"errorKey"`
	Code     *string `json:"code,omitempty"`
	Severity string  `json:"severity"`
	Message  string  `json:"message"`
	File     *string `json:"file,omitempty"`
	Range    *Range  `json:"range,omitempty"`
}

type Member struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type DiagnosticReport struct {
	UnitID      string         `json:"unitId"`
	Diagnostics []Diagnostic   `json:"diagnostics"`
	Members     []Member       `json:"members,omitempty"`
	AdviceByID  map[string]any `json:"adviceById,omitempty"`
}

type Visibility struct {
	UnitID string
	Tree   string
	Files  map[string]bool
}

type Decoded struct {
	Report  *DiagnosticReport
	Refused string
	// Bytes is the normalized report's reference-compatible CAS content.
	// It is nil for refusals; consumers must store it instead of marshaling Report.
	Bytes []byte
}

// Decode returns either a validated report or a refusal for every JSON shape.
// It checks all strings and object keys, including otherwise ignored fields.
func Decode(data []byte, visibility Visibility) Decoded {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return Decoded{Refused: "report.json is not JSON"}
	}
	if reason := checkStrings(value, visibility); reason != "" {
		return Decoded{Refused: reason}
	}
	r, reason := decodeReport(value, visibility)
	if reason != "" {
		return Decoded{Refused: reason}
	}
	canonical, err := canonicalReport(data)
	if err != nil {
		return Decoded{Refused: "report.json is not JSON"}
	}
	return Decoded{Report: r, Bytes: canonical}
}

func checkStrings(value any, visibility Visibility) string {
	pending := []any{value}
	for len(pending) > 0 {
		v := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch x := v.(type) {
		case string:
			if strings.Contains(x, visibility.Tree) {
				return "report string contains the Tree"
			}
			for _, prefix := range []string{"/work/", "/upstream/", "/scratch/", "/out/", "/tmp/"} {
				if strings.Contains(x, prefix) {
					return "report string contains the sandbox path " + prefix
				}
			}
		case []any:
			pending = append(pending, x...)
		case map[string]any:
			for key, child := range x {
				pending = append(pending, key, child)
			}
		}
	}
	return ""
}

func decodeReport(value any, visibility Visibility) (*DiagnosticReport, string) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, "report is not an object"
	}
	id, ok := m["unitId"].(string)
	if !ok || id != visibility.UnitID {
		return nil, "unitId is not this Unit"
	}
	list, ok := m["diagnostics"].([]any)
	if !ok {
		return nil, "diagnostics is not an array"
	}
	r := &DiagnosticReport{UnitID: id, Diagnostics: make([]Diagnostic, 0, len(list))}
	for i, entry := range list {
		d, reason := decodeDiagnostic(entry, visibility)
		if reason != "" {
			return nil, fmt.Sprintf("diagnostics[%d].%s", i, reason)
		}
		r.Diagnostics = append(r.Diagnostics, d)
	}
	if value, exists := m["members"]; exists {
		members, ok := value.([]any)
		if !ok {
			return nil, "members is not an array"
		}
		r.Members = make([]Member, 0, len(members))
		for i, value := range members {
			member, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Sprintf("members[%d] is not an object", i)
			}
			name, nameOK := member["name"].(string)
			status, statusOK := member["status"].(string)
			if !nameOK {
				return nil, fmt.Sprintf("members[%d].name is not a string", i)
			}
			if !statusOK || !oneOf(status, "pass", "fail", "skip") {
				return nil, fmt.Sprintf("members[%d].status is not one of pass, fail, skip", i)
			}
			r.Members = append(r.Members, Member{Name: name, Status: status})
		}
	}
	if value, exists := m["adviceById"]; exists {
		advice, ok := value.(map[string]any)
		if !ok {
			return nil, "adviceById is not an object"
		}
		r.AdviceByID = advice
	}
	return r, ""
}

func decodeDiagnostic(value any, visibility Visibility) (Diagnostic, string) {
	m, ok := value.(map[string]any)
	if !ok {
		return Diagnostic{}, "is not an object"
	}
	var d Diagnostic
	var valid bool
	if d.ErrorKey, valid = m["errorKey"].(string); !valid {
		return d, "errorKey is not a string"
	}
	if d.Message, valid = m["message"].(string); !valid {
		return d, "message is not a string"
	}
	if d.Severity, valid = m["severity"].(string); !valid || !oneOf(d.Severity, "error", "warning", "suggestion", "message") {
		return d, "severity is not one of error, warning, suggestion, message"
	}
	for _, field := range []string{"code", "file"} {
		if value, exists := m[field]; exists {
			s, ok := value.(string)
			if !ok {
				return d, field + " is not a string"
			}
			if field == "code" {
				d.Code = &s
			} else {
				if !visibility.Files[s] {
					return d, "file is not a tracked repository path in the Unit's inputs"
				}
				d.File = &s
			}
		}
	}
	if value, exists := m["range"]; exists {
		r, ok := value.(map[string]any)
		if !ok {
			return d, "range is not an object"
		}
		start, reason := decodePosition(r["start"])
		if reason != "" {
			return d, "range.start." + reason
		}
		d.Range = &Range{Start: start}
		if value, exists := r["end"]; exists {
			end, reason := decodePosition(value)
			if reason != "" {
				return d, "range.end." + reason
			}
			d.Range.End = &end
		}
	}
	return d, ""
}

func decodePosition(value any) (Position, string) {
	m, ok := value.(map[string]any)
	if !ok {
		return Position{}, "is not an object"
	}
	p := Position{}
	for _, field := range []string{"line", "column"} {
		n, ok := m[field].(float64)
		if !ok || n < 1 || n > 9007199254740991 || math.Trunc(n) != n {
			return p, field + " is not a positive integer"
		}
		if field == "line" {
			p.Line = int64(n)
		} else {
			p.Column = int64(n)
		}
	}
	return p, ""
}

func oneOf(value string, allowed ...string) bool {
	for _, s := range allowed {
		if value == s {
			return true
		}
	}
	return false
}

type Unit struct {
	Verifier string
	UnitID   string
	Report   DiagnosticReport
}

type Occurrence struct {
	Key      string `json:"key"`
	Verifier string `json:"verifier"`
	UnitID   string `json:"unitId"`
	Diagnostic
	Anchor string `json:"anchor"`
}

type MemberStatus struct {
	Verifier string `json:"verifier"`
	UnitID   string `json:"unitId"`
	Member
}

// Occurrences derives identity from source line bytes, in reference ordering.
func Occurrences(units []Unit, files map[string][]byte) []Occurrence {
	ordered := append([]Unit(nil), units...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return jsLess(ordered[i].Verifier+"\x00"+ordered[i].UnitID, ordered[j].Verifier+"\x00"+ordered[j].UnitID)
	})
	ordinals := map[string]int{}
	result := make([]Occurrence, 0)
	for _, unit := range ordered {
		for _, d := range unit.Report.Diagnostics {
			anchor := unit.UnitID
			var file any
			if d.File != nil {
				file = *d.File
				line := int64(0)
				if d.Range != nil {
					line = d.Range.Start.Line
				}
				hash := lineDigest(files[*d.File], line)
				slot := jsonString([]any{unit.Verifier, *d.File, d.ErrorKey, hash})
				anchor = fmt.Sprintf("%s:%d", hash, ordinals[slot])
				ordinals[slot]++
			}
			key := digest([]byte(jsonString([]any{unit.Verifier, d.ErrorKey, file, anchor})))
			result = append(result, Occurrence{Key: key, Verifier: unit.Verifier, UnitID: unit.UnitID, Diagnostic: d, Anchor: anchor})
		}
	}
	return result
}

func Members(units []Unit) []MemberStatus {
	result := make([]MemberStatus, 0)
	for _, unit := range units {
		for _, member := range unit.Report.Members {
			result = append(result, MemberStatus{Verifier: unit.Verifier, UnitID: unit.UnitID, Member: member})
		}
	}
	return result
}

func lineDigest(data []byte, line int64) string {
	if line < 1 {
		return digest(nil)
	}
	start := 0
	for current := int64(1); current < line; current++ {
		n := bytes.IndexByte(data[start:], '\n')
		if n < 0 {
			return digest(nil)
		}
		start += n + 1
	}
	end := bytes.IndexByte(data[start:], '\n')
	if end < 0 {
		return digest(data[start:])
	}
	return digest(data[start : start+end])
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func jsonString(value []any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(value)
	s := strings.TrimSuffix(b.String(), "\n")
	var result strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if strings.HasPrefix(s[i:], `\u2028`) {
				result.WriteRune('\u2028')
				i += 5
				continue
			}
			if strings.HasPrefix(s[i:], `\u2029`) {
				result.WriteRune('\u2029')
				i += 5
				continue
			}
			result.WriteString(s[i : i+2])
			i++
			continue
		}
		result.WriteByte(s[i])
	}
	return result.String()
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
