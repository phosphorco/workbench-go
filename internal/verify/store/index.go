package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

type Verifier struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}
type Attestation struct {
	ActionKey string            `json:"actionKey"`
	Verifier  Verifier          `json:"verifier"`
	UnitID    string            `json:"unitId"`
	Exit      int               `json:"exit"`
	Outputs   map[string]string `json:"outputs"`
}
type IndexRecord struct {
	Attestation Attestation `json:"attestation"`
	ComputedIn  string      `json:"computedIn"`
	Stdout      string      `json:"stdout"`
	Stderr      string      `json:"stderr"`
}
type Publication struct {
	Record  IndexRecord
	Won     bool
	Witness string
}
type WitnessRecord struct {
	Kind      string            `json:"kind"`
	ActionKey string            `json:"actionKey"`
	UnitID    string            `json:"unitId"`
	Attested  map[string]string `json:"attested,omitempty"`
	Later     map[string]string `json:"later,omitempty"`
	Scratch   string            `json:"scratch,omitempty"`
	Warm      map[string]string `json:"warm,omitempty"`
	Cold      any               `json:"cold,omitempty"`
	Exit      *int              `json:"exit,omitempty"`
	Stdout    string            `json:"stdout,omitempty"`
	Stderr    string            `json:"stderr,omitempty"`
	Reason    string            `json:"reason,omitempty"`
}

func component(s string) error {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\\x00") {
		return fmt.Errorf("invalid store component %q", s)
	}
	return nil
}
func indexPath(home, key, tree string, deterministic bool) (string, error) {
	if err := component(key); err != nil {
		return "", err
	}
	return filepath.Join(home, "ac", IndexKey(key, tree, deterministic)+".json"), nil
}
func Lookup(home, key, tree string, deterministic bool) (*IndexRecord, error) {
	p, err := indexPath(home, key, tree, deterministic)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r IndexRecord
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
func pretty(v any) ([]byte, error) {
	b, err := JSONBytes(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err = json.Indent(&out, b, "", "  ")
	return out.Bytes(), err
}
func witnessDir(home string, v Verifier) (string, error) {
	if err := component(v.ID); err != nil {
		return "", err
	}
	if err := component(v.Version); err != nil {
		return "", err
	}
	return filepath.Join(home, "witness", v.ID, v.Version), nil
}

// WriteWitness names immutable evidence by its complete JSON bytes.
func WriteWitness(home string, v Verifier, w WitnessRecord) (string, error) {
	dir, err := witnessDir(home, v)
	if err != nil {
		return "", err
	}
	b, err := pretty(struct {
		Verifier string `json:"verifier"`
		Version  string `json:"version"`
		WitnessRecord
	}{v.ID, v.Version, w})
	if err != nil {
		return "", err
	}
	d := SHA256(b)
	_, err = CreateExclusively(filepath.Join(dir, d+".json"), b)
	return d, err
}
func Witnessed(home string, v Verifier) (bool, error) {
	dir, err := witnessDir(home, v)
	if err != nil {
		return false, err
	}
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return len(files) > 0, err
}

var ErrFailureNotAttested = errors.New("nonzero exit cannot be attested")

// Publish returns the permanently stored record, including when this caller loses publication.
func Publish(home, tree string, deterministic bool, record IndexRecord) (Publication, error) {
	a := record.Attestation
	if a.Exit != 0 {
		d, err := WriteWitness(home, a.Verifier, WitnessRecord{Kind: "tool-failure", ActionKey: a.ActionKey, UnitID: a.UnitID, Exit: &a.Exit, Stdout: record.Stdout, Stderr: record.Stderr})
		if err != nil {
			return Publication{}, err
		}
		return Publication{Witness: d}, ErrFailureNotAttested
	}
	p, err := indexPath(home, a.ActionKey, tree, deterministic)
	if err != nil {
		return Publication{}, err
	}
	b, err := pretty(record)
	if err != nil {
		return Publication{}, err
	}
	won, err := CreateExclusively(p, b)
	if err != nil {
		return Publication{}, err
	}
	if won {
		return Publication{Record: record, Won: true}, nil
	}
	stored, err := Lookup(home, a.ActionKey, tree, deterministic)
	if err != nil {
		return Publication{}, err
	}
	if stored == nil {
		return Publication{}, fmt.Errorf("action index record vanished: %s", p)
	}
	result := Publication{Record: *stored}
	if !maps.Equal(stored.Attestation.Outputs, a.Outputs) {
		result.Witness, err = WriteWitness(home, a.Verifier, WitnessRecord{Kind: "nondeterminism", ActionKey: a.ActionKey, UnitID: a.UnitID, Attested: stored.Attestation.Outputs, Later: a.Outputs})
	}
	return result, err
}
