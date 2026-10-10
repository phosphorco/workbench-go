package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf16"
)

type Mode string

const (
	Regular    Mode = "100644"
	Executable Mode = "100755"
	Symlink    Mode = "120000"
)

type DirectoryEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Mode   Mode   `json:"mode"`
}
type Upstream struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}
type Action struct {
	Verifier    string           `json:"verifier"`
	Version     string           `json:"version"`
	Role        string           `json:"role"`
	UnitID      string           `json:"unitId"`
	Inputs      []DirectoryEntry `json:"inputs"`
	Upstream    []Upstream       `json:"upstream"`
	Program     []DirectoryEntry `json:"program"`
	Command     []string         `json:"command"`
	ImageDigest string           `json:"imageDigest"`
}

func SHA256(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func JSONBytes(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	out := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	result := make([]byte, 0, len(out))
	for i := 0; i < len(out); i++ {
		if out[i] == '\\' && i+1 < len(out) {
			if i+5 < len(out) && (string(out[i:i+6]) == `\u2028` || string(out[i:i+6]) == `\u2029`) {
				if out[i+5] == '8' {
					result = append(result, []byte("\u2028")...)
				} else {
					result = append(result, []byte("\u2029")...)
				}
				i += 5
				continue
			}
			result = append(result, out[i], out[i+1])
			i++
			continue
		}
		result = append(result, out[i])
	}
	return result, nil
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
func tuples(entries []DirectoryEntry) [][3]string {
	out := make([][3]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, [3]string{e.Path, e.Digest, string(e.Mode)})
	}
	return out
}

// ActionKey matches the reference's JSON array order and JavaScript tuple sorting.
func ActionKey(a Action) string {
	ins, prog := tuples(a.Inputs), tuples(a.Program)
	for _, list := range [][][3]string{ins, prog} {
		sort.SliceStable(list, func(i, j int) bool { return jsLess(strings.Join(list[i][:], ","), strings.Join(list[j][:], ",")) })
	}
	up := make([][2]string, 0, len(a.Upstream))
	for _, u := range a.Upstream {
		up = append(up, [2]string{u.Name, u.Digest})
	}
	sort.SliceStable(up, func(i, j int) bool { return jsLess(strings.Join(up[i][:], ","), strings.Join(up[j][:], ",")) })
	cmd := a.Command
	if cmd == nil {
		cmd = []string{}
	}
	b, _ := JSONBytes([]any{a.Verifier, a.Version, a.Role, a.UnitID, ins, up, prog, cmd, a.ImageDigest})
	return SHA256(b)
}
func IndexKey(key, tree string, deterministic bool) string {
	if deterministic {
		return key
	}
	return SHA256([]byte(key + "\x00" + tree))
}
