package contextapi

import (
	"fmt"
	"path/filepath"
	"strings"
)

// EvaluationBounds are finite required policy values. Process-memory control is
// deliberately absent until the evaluator/probe lane proves a concrete
// enforcement mechanism.
type EvaluationBounds struct {
	MaxInputBytes       uint64 `json:"maxInputBytes"`
	MaxOutputBytes      uint64 `json:"maxOutputBytes"`
	MaxDiagnosticsBytes uint64 `json:"maxDiagnosticsBytes"`
	MaxImports          uint32 `json:"maxImports"`
	DeadlineMs          uint64 `json:"deadlineMs"`
}

// DeclarationOrigin is the loader-owned authority boundary for one evaluated
// declaration. Root is the only permitted local import boundary.
type DeclarationOrigin struct {
	Path      string         `json:"path"`
	Authority ScopeAuthority `json:"authority"`
	Root      string         `json:"root"`
}

// EvaluationInput is the exact value given to the evaluator. SourceBytes are
// the bytes read for the entry module, not bytes reread or hashed later. The
// caller does not supply evaluator identity; the implementation returns it.
type EvaluationInput struct {
	Origin      DeclarationOrigin `json:"origin"`
	SchemaURI   string            `json:"schemaURI"`
	SourceBytes []byte            `json:"sourceBytes"`
	Bounds      EvaluationBounds  `json:"bounds"`
}

// ModuleOrigin records the importing origin only when the reader API exposes
// it. Current pkl-go readers set Known false instead of inventing source edges.
type ModuleOrigin struct {
	Known bool   `json:"known"`
	Path  string `json:"path"`
}

// DependencyCapture is captured at the reader boundary. Designation is the
// resolved module URI requested by the reader, not an authored import literal.
type DependencyCapture struct {
	Designation             string       `json:"designation"`
	ImportingOrigin         ModuleOrigin `json:"importingOrigin"`
	ResolvedCanonicalTarget string       `json:"resolvedCanonicalTarget"`
	Bytes                   []byte       `json:"bytes"`
	Digest                  ContentID    `json:"digest"`
}

type EvaluationDiagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type DependencyManifest struct {
	Captures    []DependencyCapture    `json:"captures"`
	Diagnostics []EvaluationDiagnostic `json:"diagnostics"`
}

type EvaluatorIdentity struct {
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Digest  ContentID `json:"digest"`
}

type DeclarationKind string

const (
	DeclarationKindProject DeclarationKind = "project"
	DeclarationKindHome    DeclarationKind = "home"
)

// EvaluatedDeclaration is a closed evaluated result. Kind selects the
// canonical Project or Home value; both fields are values, never optional JSON
// pointers. Revision covers only this declaration's source/import closure plus
// schema/evaluator identity. The loader computes the combined effective digest
// after applying home policy; it is not placed into a project revision.
type EvaluatedDeclaration struct {
	Origin       DeclarationOrigin  `json:"origin"`
	Kind         DeclarationKind    `json:"kind"`
	Project      ProjectDeclaration `json:"project"`
	Home         HomeDeclaration    `json:"home"`
	Dependencies DependencyManifest `json:"dependencies"`
	Revision     ConfigDigest       `json:"revision"`
	Evaluator    EvaluatorIdentity  `json:"evaluator"`
}

func (input EvaluationInput) Validate() error {
	if !filepath.IsAbs(input.Origin.Path) || !filepath.IsAbs(input.Origin.Root) {
		return fmt.Errorf("evaluation origin path and root must be absolute")
	}
	relative, err := filepath.Rel(filepath.Clean(input.Origin.Root), filepath.Clean(input.Origin.Path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("evaluation origin path %q is outside root %q", input.Origin.Path, input.Origin.Root)
	}
	if input.Origin.Authority != ScopeAuthorityProject && input.Origin.Authority != ScopeAuthorityHome {
		return fmt.Errorf("invalid evaluation authority %q", input.Origin.Authority)
	}
	wantSchema := "workbench:context"
	if input.Origin.Authority == ScopeAuthorityHome {
		wantSchema = "workbench:context-home"
	}
	if input.SchemaURI != wantSchema {
		return fmt.Errorf("schema %q does not match %s authority", input.SchemaURI, input.Origin.Authority)
	}
	if input.Bounds.MaxInputBytes == 0 || input.Bounds.MaxOutputBytes == 0 || input.Bounds.MaxDiagnosticsBytes == 0 || input.Bounds.MaxImports == 0 || input.Bounds.DeadlineMs == 0 {
		return fmt.Errorf("evaluation bounds must be finite and nonzero")
	}
	if len(input.SourceBytes) == 0 || uint64(len(input.SourceBytes)) > input.Bounds.MaxInputBytes {
		return fmt.Errorf("evaluation source exceeds its input bound")
	}
	return nil
}

func (value EvaluatedDeclaration) Validate() error {
	if !filepath.IsAbs(value.Origin.Path) || !filepath.IsAbs(value.Origin.Root) {
		return fmt.Errorf("evaluated origin path and root must be absolute")
	}
	switch value.Kind {
	case DeclarationKindProject:
		if value.Origin.Authority != ScopeAuthorityProject {
			return fmt.Errorf("project evaluation has %s authority", value.Origin.Authority)
		}
		if homeDeclarationNonEmpty(value.Home) {
			return fmt.Errorf("project evaluation carries a nonempty home value")
		}
	case DeclarationKindHome:
		if value.Origin.Authority != ScopeAuthorityHome {
			return fmt.Errorf("home evaluation has %s authority", value.Origin.Authority)
		}
		if projectDeclarationNonEmpty(value.Project) {
			return fmt.Errorf("home evaluation carries a nonempty project value")
		}
	default:
		return fmt.Errorf("invalid evaluated declaration kind %q", value.Kind)
	}
	if value.Revision == "" {
		return fmt.Errorf("evaluated declaration revision is empty")
	}
	if value.Evaluator.Name == "" || value.Evaluator.Version == "" || value.Evaluator.Digest == "" {
		return fmt.Errorf("evaluated declaration evaluator identity is incomplete")
	}
	return nil
}

func projectDeclarationNonEmpty(value ProjectDeclaration) bool {
	return value.Enabled || value.Scope != "" || len(value.Contributors) > 0 || value.Profile.Role != "" || value.Profile.GuidanceSet != "" || len(value.Profile.Preferences) > 0
}

func homeDeclarationNonEmpty(value HomeDeclaration) bool {
	return len(value.Exclusions) > 0 || len(value.Directories) > 0 || value.Limits.ProviderPolicy.Mode != "" || len(value.Limits.ProviderPolicy.Allowed) > 0 || value.Limits.Defaults.Selection.Role != "" || value.Limits.Defaults.Selection.GuidanceSet != "" || len(value.Limits.Defaults.Selection.Preferences) > 0 || value.Limits.Delivery != (EngineLimits{}) || value.Limits.Runtime != (RuntimeLimits{}) || value.Limits.Cache != (CacheLimits{})
}

func CloneEvaluationInput(input EvaluationInput) EvaluationInput {
	input.Origin.Path = strings.Clone(input.Origin.Path)
	input.Origin.Root = strings.Clone(input.Origin.Root)
	input.SchemaURI = strings.Clone(input.SchemaURI)
	input.SourceBytes = cloneBytesValue(input.SourceBytes)
	return input
}

func CloneDependencyManifest(input DependencyManifest) DependencyManifest {
	result := DependencyManifest{
		Captures:    make([]DependencyCapture, len(input.Captures)),
		Diagnostics: make([]EvaluationDiagnostic, len(input.Diagnostics)),
	}
	for index, capture := range input.Captures {
		result.Captures[index] = DependencyCapture{
			Designation:             strings.Clone(capture.Designation),
			ImportingOrigin:         ModuleOrigin{Known: capture.ImportingOrigin.Known, Path: strings.Clone(capture.ImportingOrigin.Path)},
			ResolvedCanonicalTarget: strings.Clone(capture.ResolvedCanonicalTarget),
			Bytes:                   cloneBytesValue(capture.Bytes),
			Digest:                  ContentID(strings.Clone(string(capture.Digest))),
		}
	}
	for index, diagnostic := range input.Diagnostics {
		result.Diagnostics[index] = EvaluationDiagnostic{Severity: strings.Clone(diagnostic.Severity), Message: strings.Clone(diagnostic.Message)}
	}
	return result
}

func cloneBytesValue(input []byte) []byte {
	result := make([]byte, len(input))
	copy(result, input)
	return result
}
