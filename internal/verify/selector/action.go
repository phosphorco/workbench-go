package selector

import (
	"slices"
	"strings"
)

type Input struct {
	Path   string
	Digest string
	Mode   string
}

type Upstream struct {
	Name   string
	Digest string
}

type RunSpec struct {
	VerifierID  string
	Version     string
	Command     []string
	ImageDigest string
	Program     []Input
	Upstream    []Upstream
}

// Action contains the select Action's content-key inputs. Host staging paths,
// Scratch, network execution state, and whole-Tree identity are absent.
// The engine owns hashing, nondeterministic Tree salting, and cache publication.
type Action struct {
	VerifierID  string
	Version     string
	Role        string
	UnitID      string
	Inputs      []Input
	Upstream    []Upstream
	Program     []Input
	Command     []string
	ImageDigest string
}

// SelectAction exposes every whole-Tree input, program input and upstream digest
// used by Own. whole must describe the complete tracked Tree, including file modes.
func SelectAction(spec RunSpec, whole []Input) Action {
	inputs, program := sortedInputs(whole), sortedInputs(spec.Program)
	upstream := append([]Upstream{}, spec.Upstream...)
	slices.SortFunc(upstream, func(a, b Upstream) int { return strings.Compare(a.Name, b.Name) })
	return Action{spec.VerifierID, spec.Version, "select", "*", inputs, upstream, program, append([]string{}, spec.Command...), spec.ImageDigest}
}

func sortedInputs(inputs []Input) []Input {
	result := append([]Input{}, inputs...)
	slices.SortFunc(result, func(a, b Input) int { return strings.Compare(a.Path, b.Path) })
	return result
}
