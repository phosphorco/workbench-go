package selector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/phosphorco/workbench-go/internal/verify/sandbox"
)

type Unit struct {
	UnitID  string   `json:"unitId"`
	Display string   `json:"display"`
	Inputs  []string `json:"inputs"`
}

// WholeTree returns one Unit over the sorted tracked paths, without aliasing them.
func WholeTree(tracked []string, verifierID string) []Unit {
	return []Unit{{UnitID: verifierID, Display: verifierID, Inputs: sortedPaths(tracked)}}
}

// Paths selects only tracked paths matching at least one pattern.
func Paths(tracked []string, verifierID string, patterns []string) ([]Unit, error) {
	for _, pattern := range patterns {
		if !doublestar.ValidatePattern(pattern) {
			return nil, fmt.Errorf("invalid glob %q", pattern)
		}
	}
	inputs := []string{}
	for _, p := range sortedPaths(tracked) {
		for _, pattern := range patterns {
			matched, err := doublestar.Match(pattern, p)
			if err != nil {
				return nil, err
			}
			if matched {
				inputs = append(inputs, p)
				break
			}
		}
	}
	return []Unit{{UnitID: verifierID, Display: verifierID, Inputs: inputs}}, nil
}

func sortedPaths(paths []string) []string {
	result := append([]string{}, paths...)
	slices.Sort(result)
	return slices.Compact(result)
}

// Own runs the select role against a caller-staged whole Tree. The caller owns
// the fresh output directory and the staging lifetime. Scratch is never granted.
// Process evidence is returned even on refusal; selection JSON comes from stdout.
func Own(ctx context.Context, tracked []string, invocation sandbox.Invocation) ([]Unit, sandbox.Outcome, error) {
	invocation.Role, invocation.UnitID, invocation.ScratchDir = "select", "*", ""
	outcome, err := sandbox.Execute(ctx, invocation)
	if err != nil {
		return nil, outcome, err
	}
	units, err := Decode(outcome.Stdout, tracked)
	return units, outcome, err
}

// Decode totally validates selection JSON, refusing duplicate identities,
// duplicate object keys, escaping paths, and inputs outside the tracked list.
// Input duplicates are removed and each closure is sorted; Unit order is retained.
func Decode(data []byte, tracked []string) ([]Unit, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("select stdout is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := jsonValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("malformed select stdout: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("select stdout has trailing data")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("select stdout must be an object")
	}
	rawUnits, ok := object["units"].([]any)
	if !ok {
		return nil, fmt.Errorf("select stdout lacks units[]")
	}
	allowed := make(map[string]bool, len(tracked))
	for _, p := range tracked {
		allowed[p] = true
	}
	seen := map[string]bool{}
	units := make([]Unit, 0, len(rawUnits))
	for _, raw := range rawUnits {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("select unit must be an object")
		}
		id, ok := object["unitId"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("select unit lacks nonempty unitId")
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate unitId %q", id)
		}
		seen[id] = true
		if rawDisplay, present := object["display"]; present {
			_, ok = rawDisplay.(string)
			if !ok {
				return nil, fmt.Errorf("unit %q display must be a string", id)
			}
		}
		rawInputs, ok := object["inputs"].([]any)
		if !ok {
			return nil, fmt.Errorf("unit %q lacks inputs[]", id)
		}
		inputs := make([]string, 0, len(rawInputs))
		for _, rawInput := range rawInputs {
			p, ok := rawInput.(string)
			if !ok {
				return nil, fmt.Errorf("unit %q input must be a string", id)
			}
			if p == "." || !fs.ValidPath(p) || strings.ContainsAny(p, "\\\x00") {
				return nil, fmt.Errorf("unit %q input escapes repository: %q", id, p)
			}
			if !allowed[p] {
				return nil, fmt.Errorf("select named untracked input %q", p)
			}
			inputs = append(inputs, p)
		}
		units = append(units, Unit{UnitID: id, Display: id, Inputs: sortedPaths(inputs)})
	}
	return units, nil
}

func jsonValue(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for d.More() {
			keyToken, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("invalid object key")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate object key %q", key)
			}
			value, err := jsonValue(d)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := []any{}
		for d.More() {
			value, err := jsonValue(d)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter")
	}
}
