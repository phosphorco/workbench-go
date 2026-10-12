package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/report"
)

func encode(v any, pretty bool) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if pretty {
		e.SetIndent("", "  ")
	}
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	data := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	var out bytes.Buffer
	for i := 0; i < len(data); i++ {
		if data[i] == '\\' && i+1 < len(data) {
			if bytes.HasPrefix(data[i:], []byte(`\u2028`)) {
				out.WriteRune('\u2028')
				i += 5
				continue
			}
			if bytes.HasPrefix(data[i:], []byte(`\u2029`)) {
				out.WriteRune('\u2029')
				i += 5
				continue
			}
			out.Write(data[i : i+2])
			i++
			continue
		}
		out.WriteByte(data[i])
	}
	return out.Bytes(), nil
}

func LastSeenFile(o Options) string {
	var b strings.Builder
	for _, c := range []byte(o.Session) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.!~*'()", rune(c)) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return filepath.Join(o.Home, "sessions", b.String(), "last-seen.json")
}
func ReadLastSeen(path string) (LastSeen, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return LastSeen{Occurrences: []SeenEntry{}}, nil
	}
	if err != nil {
		return LastSeen{}, err
	}
	var s LastSeen
	err = json.Unmarshal(data, &s)
	return s, err
}

// WriteLastSeen atomically replaces the file after syncing the new contents.
func WriteLastSeen(path string, s LastSeen) error {
	data, err := encode(s, true)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".last-seen-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

type Presentation struct {
	Options     Options
	Observation observe.Observation
	Occurrences []report.Occurrence
	Members     []report.MemberStatus
	Focus       Focus
	Gate        *GateStatus
	Blocking    []string
}

// ObservationJSONOutput exposes the full Observation for explicit inspection.
func ObservationJSONOutput(o observe.Observation, occurrences []report.Occurrence, members []report.MemberStatus, d Delta, f Focus) (string, error) {
	wire := struct {
		Observation struct {
			Tree        string                `json:"tree"`
			Verifiers   []observe.Verifier    `json:"verifiers"`
			Results     []observe.Result      `json:"results"`
			Unknown     []observe.Unknown     `json:"unknown"`
			Occurrences []report.Occurrence   `json:"occurrences"`
			Members     []report.MemberStatus `json:"members"`
			Complete    bool                  `json:"complete"`
		} `json:"observation"`
		Delta Delta `json:"delta"`
		Focus Focus `json:"focus"`
	}{Delta: d, Focus: normalizedFocus(f)}
	wire.Observation.Tree = o.Tree
	wire.Observation.Complete = o.Complete
	wire.Observation.Verifiers = append([]observe.Verifier{}, o.Verifiers...)
	wire.Observation.Results = append([]observe.Result{}, o.Results...)
	wire.Observation.Unknown = append([]observe.Unknown{}, o.Unknown...)
	wire.Observation.Occurrences = append([]report.Occurrence{}, occurrences...)
	wire.Observation.Members = append([]report.MemberStatus{}, members...)
	data, err := encode(wire, true)
	return string(data) + "\n", err
}

// JSONOutput renders the same grouped fold as text without engine mechanics.
func JSONOutput(o observe.Observation, occurrences []report.Occurrence, members []report.MemberStatus, d Delta, f Focus) (string, error) {
	data, err := encode(d.Output, true)
	return string(data) + "\n", err
}

// Present writes the Delta before committing attention state. --all is a read;
// --reset explicitly clears state even when combined with --all.
func Present(p Presentation, output io.Writer) (int, error) {
	path := LastSeenFile(p.Options)
	if p.Options.Reset {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 2, err
		}
	}
	last, err := ReadLastSeen(path)
	if err != nil {
		return 2, err
	}

	d, next := Fold(FoldInput{p.Observation, p.Occurrences, p.Members, last, p.Focus, p.Gate})
	blocking := p.Blocking
	if blocking == nil {
		blocking = []string{"error"}
	}
	exit := ExitCode(p.Observation, p.Occurrences, p.Focus, p.Gate, blocking)
	text := Render(d, RenderOptions{p.Options.All, exit})
	if p.Options.JSON {
		var err error
		if p.Options.ObservationJSON {
			text, err = ObservationJSONOutput(p.Observation, p.Occurrences, p.Members, d, p.Focus)
		} else {
			text, err = JSONOutput(p.Observation, p.Occurrences, p.Members, d, p.Focus)
		}
		if err != nil {
			return 2, err
		}
	}
	n, err := io.WriteString(output, text)
	if err != nil {
		return 2, err
	}
	if n != len(text) {
		return 2, io.ErrShortWrite
	}
	if !p.Options.All {
		if err := WriteLastSeen(path, next); err != nil {
			return 2, err
		}
	}
	return exit, nil
}
