package pkl

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
)

//go:embed Repository.pkl
var RepositorySource string

//go:embed Verifier.pkl
var VerifierSource string

//go:embed Focus.pkl
var FocusSource string

// VerifyRepository carries verification policy from an evaluated Repository.pkl.
// Existing Workbench declarations remain opaque to the verification consumer.
type VerifyRepository struct {
	Includes   map[string]json.RawMessage `json:"includes"`
	Packages   map[string]json.RawMessage `json:"packages"`
	Buildables map[string]json.RawMessage `json:"buildables"`
	Verifiers  map[string]Verifier        `json:"verifiers"`
	Gates      map[string]Gate            `json:"gates"`
	Focuses    map[string]Focus           `json:"focuses"`
}

// Verifier matches the evaluated Pkl property order. Timeout is in seconds.
type Verifier struct {
	ID            string            `json:"id"`
	Image         string            `json:"image"`
	Program       []string          `json:"program"`
	Command       []string          `json:"command"`
	Selector      Selector          `json:"selector"`
	DisplayNoun   string            `json:"displayNoun"`
	Produces      string            `json:"produces"`
	Outputs       []string          `json:"outputs"`
	Members       bool              `json:"members"`
	Requires      map[string]string `json:"requires"`
	Toolchain     Toolchain         `json:"toolchain"`
	Deterministic bool              `json:"deterministic"`
	Network       string            `json:"network"`
	Timeout       float64           `json:"timeout"`
}

type Selector struct {
	Kind     string   `json:"kind"`
	Patterns []string `json:"patterns,omitempty"`
}

type Toolchain struct {
	Kind      string   `json:"kind"`
	Retained  []string `json:"retained,omitempty"`
	AuditRate *float64 `json:"auditRate,omitempty"`
}

type Gate struct {
	Requires []string `json:"requires"`
	Blocking []string `json:"blocking"`
}

type Focus struct {
	Files      []string `json:"files"`
	Codes      []string `json:"codes"`
	Verifiers  []string `json:"verifiers"`
	Units      []string `json:"units"`
	Members    []string `json:"members"`
	Severities []string `json:"severities"`
}

// DecodeVerifyRepository refuses unknown verification properties and trailing JSON.
// Pkl evaluation establishes schema constraints; existing Workbench sections are
// retained as JSON for their owning consumers to decode.
func DecodeVerifyRepository(data []byte) (VerifyRepository, error) {
	var value VerifyRepository
	if err := decodeVerifyJSON(data, &value); err != nil {
		return VerifyRepository{}, fmt.Errorf("decode verify repository: %w", err)
	}
	return value, nil
}

// DecodeVerifier refuses unknown properties, including fields from other union arms.
func DecodeVerifier(data []byte) (Verifier, error) {
	var value Verifier
	if err := decodeVerifyJSON(data, &value); err != nil {
		return Verifier{}, fmt.Errorf("decode verifier: %w", err)
	}
	return value, nil
}

func decodeVerifyJSON(data []byte, target any) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("expected an object, got null")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func (s *Selector) UnmarshalJSON(data []byte) error {
	type plain Selector
	var value plain
	if err := decodeVerifyJSON(data, &value); err != nil {
		return err
	}
	switch value.Kind {
	case "WholeTree", "Own":
		var arm struct {
			Kind string `json:"kind"`
		}
		if err := decodeVerifyJSON(data, &arm); err != nil {
			return err
		}
	case "Paths":
		if len(value.Patterns) == 0 {
			return fmt.Errorf("Paths requires patterns")
		}
	default:
		return fmt.Errorf("unknown selector kind %q", value.Kind)
	}
	*s = Selector(value)
	return nil
}

func (t *Toolchain) UnmarshalJSON(data []byte) error {
	type plain Toolchain
	var value plain
	if err := decodeVerifyJSON(data, &value); err != nil {
		return err
	}
	switch value.Kind {
	case "strong":
		var arm struct {
			Kind string `json:"kind"`
		}
		if err := decodeVerifyJSON(data, &arm); err != nil {
			return err
		}
	case "weak":
		if len(value.Retained) == 0 || value.AuditRate == nil || *value.AuditRate < 0 || *value.AuditRate > 1 {
			return fmt.Errorf("weak toolchain requires retained paths and auditRate in [0, 1]")
		}
	default:
		return fmt.Errorf("unknown toolchain kind %q", value.Kind)
	}
	*t = Toolchain(value)
	return nil
}
