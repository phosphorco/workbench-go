package contextapi

import (
	"bytes"
	"testing"
)

func TestEvaluationInputValidatesAuthorityAndSchema(t *testing.T) {
	valid := EvaluationInput{
		Origin:      DeclarationOrigin{Path: "/workspace/workbench-context.pkl", Root: "/workspace", Authority: ScopeAuthorityProject},
		SchemaURI:   "workbench:context",
		SourceBytes: []byte("amends \"workbench:context\""),
		Bounds:      EvaluationBounds{MaxInputBytes: 64, MaxOutputBytes: 64, MaxDiagnosticsBytes: 64, MaxImports: 4, DeadlineMs: 100},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]EvaluationInput{
		"relative origin":  {Origin: DeclarationOrigin{Path: "workbench-context.pkl", Root: "/workspace", Authority: ScopeAuthorityProject}, SchemaURI: "workbench:context", SourceBytes: []byte("source"), Bounds: valid.Bounds},
		"outside root":     {Origin: DeclarationOrigin{Path: "/other/workbench-context.pkl", Root: "/workspace", Authority: ScopeAuthorityProject}, SchemaURI: "workbench:context", SourceBytes: valid.SourceBytes, Bounds: valid.Bounds},
		"wrong schema":     {Origin: valid.Origin, SchemaURI: "workbench:context-home", SourceBytes: valid.SourceBytes, Bounds: valid.Bounds},
		"empty source":     {Origin: valid.Origin, SchemaURI: valid.SchemaURI, Bounds: valid.Bounds},
		"zero bounds":      {Origin: valid.Origin, SchemaURI: valid.SchemaURI, SourceBytes: valid.SourceBytes},
		"oversized source": {Origin: valid.Origin, SchemaURI: valid.SchemaURI, SourceBytes: []byte("this source is too long"), Bounds: EvaluationBounds{MaxInputBytes: 1, MaxOutputBytes: 64, MaxDiagnosticsBytes: 64, MaxImports: 4, DeadlineMs: 100}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := input.Validate(); err == nil {
				t.Fatal("invalid evaluation input was accepted")
			}
		})
	}
}

func TestEvaluatedDeclarationValidatesClosedKindAndOrigin(t *testing.T) {
	valid := EvaluatedDeclaration{
		Origin:    DeclarationOrigin{Path: "/workspace/workbench-context.pkl", Root: "/workspace", Authority: ScopeAuthorityProject},
		Kind:      DeclarationKindProject,
		Revision:  "sha256:revision",
		Evaluator: EvaluatorIdentity{Name: "pkl", Version: "0.32.1", Digest: "sha256:evaluator"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]EvaluatedDeclaration{
		"kind authority mismatch": {Origin: DeclarationOrigin{Path: "/workspace/home.pkl", Root: "/workspace", Authority: ScopeAuthorityHome}, Kind: DeclarationKindProject, Revision: valid.Revision, Evaluator: valid.Evaluator},
		"unknown kind":            {Origin: valid.Origin, Kind: DeclarationKind("other"), Revision: valid.Revision, Evaluator: valid.Evaluator},
		"missing identity":        {Origin: valid.Origin, Kind: valid.Kind, Revision: valid.Revision},
		"project carries home":    {Origin: valid.Origin, Kind: valid.Kind, Project: valid.Project, Home: HomeDeclaration{Directories: []HomeDirectorySelection{{Root: "/workspace"}}}, Revision: valid.Revision, Evaluator: valid.Evaluator},
		"home carries project":    {Origin: DeclarationOrigin{Path: "/home/.workbench-context.pkl", Root: "/home", Authority: ScopeAuthorityHome}, Kind: DeclarationKindHome, Project: ProjectDeclaration{Enabled: true}, Revision: valid.Revision, Evaluator: valid.Evaluator},
	} {
		t.Run(name, func(t *testing.T) {
			if err := input.Validate(); err == nil {
				t.Fatal("invalid evaluated declaration was accepted")
			}
		})
	}
}

func TestEvaluationOwnershipClonesSourceAndDependencyBytes(t *testing.T) {
	input := EvaluationInput{
		Origin:      DeclarationOrigin{Path: "/workspace/context.pkl", Root: "/workspace", Authority: ScopeAuthorityProject},
		SchemaURI:   "workbench:context",
		SourceBytes: []byte("source"),
	}
	clone := CloneEvaluationInput(input)
	input.SourceBytes[0] = 'X'
	if !bytes.Equal(clone.SourceBytes, []byte("source")) {
		t.Fatalf("source bytes were retained by alias: %q", clone.SourceBytes)
	}

	manifest := DependencyManifest{Captures: []DependencyCapture{{
		Designation:             "workbench:local",
		ImportingOrigin:         ModuleOrigin{Known: true, Path: "/workspace/context.pkl"},
		ResolvedCanonicalTarget: "/workspace/local.pkl",
		Bytes:                   []byte("local"),
		Digest:                  "sha256:local",
	}}}
	manifestClone := CloneDependencyManifest(manifest)
	manifest.Captures[0].Bytes[0] = 'X'
	if !bytes.Equal(manifestClone.Captures[0].Bytes, []byte("local")) || manifestClone.Captures[0].ImportingOrigin.Path != "/workspace/context.pkl" {
		t.Fatalf("dependency capture was retained by alias: %#v", manifestClone.Captures[0])
	}
}
