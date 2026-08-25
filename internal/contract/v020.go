package contract

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	changeIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	commitPattern      = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	hunkIDPattern      = regexp.MustCompile(`^[0-9a-fA-F]{4,64}(?::[0-9]+(?:-[0-9]+)?(?:,[0-9]+(?:-[0-9]+)?)*)?$`)
	packageLeafPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

type ResourceShapeKind string

const (
	PackageScopeShape ResourceShapeKind = "packageScope"
	RepositoryShape   ResourceShapeKind = "repository"
)

// ResourceShape is the closed, released set of identity and placement laws.
// Scope participates only in the PackageScope arm.
type ResourceShape struct {
	Kind  ResourceShapeKind `json:"kind"`
	Scope string            `json:"scope,omitempty"`
}

// Declaration is a released-line repository-owned resource declaration. Its
// GitHub designation is deliberately supplied by the checkout transport,
// rather than repeated as repository-authored identity. Version-specific
// decoders establish the laws carried by Packages.
type Declaration struct {
	Shape    ResourceShape
	Includes map[string]ResourceInclude `json:"includes"`
	Packages map[string]PackagePolicy   `json:"packages"`
}

type ResourceInclude struct {
	Skills *SkillPolicy `json:"skills,omitempty"`
}

func DecodePackageScopeDeclaration(encoded []byte) (Declaration, error) {
	var value struct {
		Scope    string                     `json:"scope"`
		Includes map[string]ResourceInclude `json:"includes"`
		Packages map[string]PackagePolicy   `json:"packages"`
	}
	if err := decodeStrict(encoded, &value); err != nil {
		return Declaration{}, fmt.Errorf("decode 0.2 package-scope declaration: %w", err)
	}
	declaration := Declaration{
		Shape:    ResourceShape{Kind: PackageScopeShape, Scope: value.Scope},
		Includes: value.Includes,
		Packages: value.Packages,
	}
	if err := declaration.Validate(); err != nil {
		return Declaration{}, err
	}
	return declaration, nil
}

// DecodePackageScopeDeclarationV030 applies the 0.3 PackageScope ontology.
// The 0.2 decoder remains separate because its released contract permitted
// package names without deriving a canonical child directory from the scope.
func DecodePackageScopeDeclarationV030(encoded []byte) (Declaration, error) {
	declaration, err := DecodePackageScopeDeclaration(encoded)
	if err != nil {
		return Declaration{}, fmt.Errorf("decode 0.3 package-scope declaration: %w", err)
	}
	for name := range declaration.Packages {
		if _, err := packageScopePackageLeaf(declaration.Shape.Scope, name); err != nil {
			return Declaration{}, err
		}
	}
	return declaration, nil
}

func DecodeRepositoryDeclaration(encoded []byte) (Declaration, error) {
	var value struct {
		Includes map[string]ResourceInclude `json:"includes"`
		Packages map[string]PackagePolicy   `json:"packages"`
	}
	if err := decodeStrict(encoded, &value); err != nil {
		return Declaration{}, fmt.Errorf("decode 0.2 repository declaration: %w", err)
	}
	declaration := Declaration{
		Shape:    ResourceShape{Kind: RepositoryShape},
		Includes: value.Includes,
		Packages: value.Packages,
	}
	if err := declaration.Validate(); err != nil {
		return Declaration{}, err
	}
	return declaration, nil
}

func (declaration Declaration) Validate() error {
	if err := declaration.Shape.Validate(); err != nil {
		return fmt.Errorf("repository shape: %w", err)
	}
	for designation := range declaration.Includes {
		if _, err := NormalizeGitHubRepository(designation); err != nil {
			return fmt.Errorf("include designation %q: %w", designation, err)
		}
	}
	return nil
}

func (shape ResourceShape) Validate() error {
	switch shape.Kind {
	case PackageScopeShape:
		if !packageScopePattern.MatchString(shape.Scope) {
			return fmt.Errorf("scope %q is not a package scope", shape.Scope)
		}
	case RepositoryShape:
		if shape.Scope != "" {
			return fmt.Errorf("Repository shape cannot carry package scope %q", shape.Scope)
		}
	default:
		return fmt.Errorf("unknown resource shape %q", shape.Kind)
	}
	return nil
}

func (declaration Declaration) Identity(github string) (string, error) {
	if err := declaration.Shape.Validate(); err != nil {
		return "", err
	}
	if declaration.Shape.Kind == PackageScopeShape {
		return declaration.Shape.Scope, nil
	}
	return NormalizeGitHubRepository(github)
}

func (declaration Declaration) CanonicalPath(github string) (string, error) {
	identity, err := declaration.Identity(github)
	if err != nil {
		return "", err
	}
	if declaration.Shape.Kind == PackageScopeShape {
		return "pkg/" + identity, nil
	}
	_, name, _ := strings.Cut(identity, "/")
	return "repos/" + name, nil
}

// PackageDirectory derives the canonical child directory of one declared 0.3
// PackageScope package. Repository-shaped resources have independent package
// placement semantics and deliberately do not participate in this operation.
func (declaration Declaration) PackageDirectory(name string) (string, error) {
	if declaration.Shape.Kind != PackageScopeShape {
		return "", fmt.Errorf("Repository shape does not derive PackageScope package directories")
	}
	if _, declared := declaration.Packages[name]; !declared {
		return "", fmt.Errorf("package identity %q is not declared", name)
	}
	return packageScopePackageLeaf(declaration.Shape.Scope, name)
}

func packageScopePackageLeaf(scope string, name string) (string, error) {
	prefix := scope + "/"
	leaf := strings.TrimPrefix(name, prefix)
	if leaf == name || len(name) > 214 || !packageLeafPattern.MatchString(leaf) {
		return "", fmt.Errorf("package identity %q must be exactly %s/<leaf> with one safe package leaf", name, scope)
	}
	return leaf, nil
}

func NormalizeGitHubRepository(designation string) (string, error) {
	if !githubNamePattern.MatchString(designation) {
		return "", fmt.Errorf("GitHub repository %q is invalid", designation)
	}
	owner, name, ok := strings.Cut(designation, "/")
	if !ok || strings.HasSuffix(strings.ToLower(name), ".git") {
		return "", fmt.Errorf("GitHub repository %q is invalid", designation)
	}
	return strings.ToLower(owner + "/" + name), nil
}

type AgentInstructions struct {
	Prose          string          `json:"prose"`
	Subject        AgentSubject    `json:"subject"`
	Resources      []AgentResource `json:"resources"`
	GeneratedPaths []string        `json:"generatedPaths"`
	HandOwnedPaths []string        `json:"handOwnedPaths"`
}

type AgentSubject struct {
	WorkLine    WorkLine `json:"workLine"`
	Entrypoints []string `json:"entrypoints"`
}

type AgentResource struct {
	Identity      string        `json:"identity"`
	GitHub        string        `json:"github"`
	Shape         ResourceShape `json:"shape"`
	CanonicalPath string        `json:"canonicalPath"`
	Branch        string        `json:"branch"`
	Health        string        `json:"health"`
}

func DecodeAgentInstructions(encoded []byte) (AgentInstructions, error) {
	var value AgentInstructions
	if err := decodeStrict(encoded, &value); err != nil {
		return AgentInstructions{}, fmt.Errorf("decode AgentInstructions: %w", err)
	}
	if err := value.Validate(); err != nil {
		return AgentInstructions{}, err
	}
	return value, nil
}

func (instructions AgentInstructions) Validate() error {
	if strings.TrimSpace(instructions.Prose) == "" {
		return fmt.Errorf("AgentInstructions prose is empty")
	}
	if err := (Subject{WorkLine: instructions.Subject.WorkLine, Entrypoints: instructions.Subject.Entrypoints}).Validate(); err != nil {
		return fmt.Errorf("AgentInstructions subject: %w", err)
	}
	identities := make(map[string]struct{}, len(instructions.Resources))
	for index, resource := range instructions.Resources {
		declaration := Declaration{Shape: resource.Shape}
		identity, err := declaration.Identity(resource.GitHub)
		if err != nil {
			return fmt.Errorf("AgentInstructions resource %d: %w", index, err)
		}
		canonicalPath, err := declaration.CanonicalPath(resource.GitHub)
		if err != nil {
			return fmt.Errorf("AgentInstructions resource %d: %w", index, err)
		}
		if resource.Identity != identity || resource.CanonicalPath != canonicalPath {
			return fmt.Errorf("AgentInstructions resource %d identity or canonical path disagrees with its shape", index)
		}
		if strings.TrimSpace(resource.Branch) == "" {
			return fmt.Errorf("AgentInstructions resource %q branch is empty", identity)
		}
		switch resource.Health {
		case "healthy", "missingCheckout", "wrongBranch":
		default:
			return fmt.Errorf("AgentInstructions resource %q has unknown health %q", identity, resource.Health)
		}
		if _, exists := identities[identity]; exists {
			return fmt.Errorf("AgentInstructions resource identity %q is duplicated", identity)
		}
		identities[identity] = struct{}{}
	}
	for _, paths := range [][]string{instructions.GeneratedPaths, instructions.HandOwnedPaths} {
		for _, value := range paths {
			if err := validateRelativePath(value); err != nil {
				return fmt.Errorf("AgentInstructions path %q: %w", value, err)
			}
		}
	}
	return nil
}

type WorkbenchCommitPlan struct {
	ChangeID string                     `json:"changeId"`
	Summary  string                     `json:"summary"`
	Commits  map[string]CommitSelection `json:"commits"`
}

type CommitSelection struct {
	Title                 string   `json:"title"`
	Description           string   `json:"description"`
	FilePaths             []string `json:"filePaths"`
	HunkIDs               []string `json:"hunkIds"`
	UnrelatedDeletedPaths []string `json:"unrelatedDeletedPaths"`
}

func DecodeWorkbenchCommitPlan(encoded []byte) (WorkbenchCommitPlan, error) {
	var value WorkbenchCommitPlan
	if err := decodeStrict(encoded, &value); err != nil {
		return WorkbenchCommitPlan{}, fmt.Errorf("decode WorkbenchCommitPlan: %w", err)
	}
	if err := value.Validate(); err != nil {
		return WorkbenchCommitPlan{}, err
	}
	return value, nil
}

func (plan WorkbenchCommitPlan) Validate() error {
	if !changeIDPattern.MatchString(plan.ChangeID) {
		return fmt.Errorf("WorkbenchCommitPlan changeId %q is invalid", plan.ChangeID)
	}
	if strings.TrimSpace(plan.Summary) == "" {
		return fmt.Errorf("WorkbenchCommitPlan summary is empty")
	}
	if len(plan.Commits) == 0 {
		return fmt.Errorf("WorkbenchCommitPlan commits is empty")
	}
	for identity, selection := range plan.Commits {
		if !validResourceIdentity(identity) {
			return fmt.Errorf("commit resource identity %q is invalid", identity)
		}
		if err := selection.Validate(); err != nil {
			return fmt.Errorf("commit %q: %w", identity, err)
		}
	}
	return nil
}

func (selection CommitSelection) Validate() error {
	if strings.TrimSpace(selection.Title) == "" || strings.ContainsAny(selection.Title, "\r\n") {
		return fmt.Errorf("title must be one non-empty line")
	}
	if strings.TrimSpace(selection.Description) == "" {
		return fmt.Errorf("description is empty")
	}
	if len(selection.FilePaths) == 0 && len(selection.HunkIDs) == 0 {
		return fmt.Errorf("exact selection needs at least one file path or hunk id")
	}
	if err := validateUniquePaths(selection.FilePaths, "filePaths"); err != nil {
		return err
	}
	if err := validateUniquePaths(selection.UnrelatedDeletedPaths, "unrelatedDeletedPaths"); err != nil {
		return err
	}
	seenHunks := make(map[string]struct{}, len(selection.HunkIDs))
	for _, id := range selection.HunkIDs {
		if !hunkIDPattern.MatchString(id) {
			return fmt.Errorf("hunk id %q is invalid", id)
		}
		if _, exists := seenHunks[id]; exists {
			return fmt.Errorf("hunk id %q is duplicated", id)
		}
		seenHunks[id] = struct{}{}
	}
	selected := make(map[string]struct{}, len(selection.FilePaths))
	for _, selectedPath := range selection.FilePaths {
		selected[selectedPath] = struct{}{}
	}
	for _, deletedPath := range selection.UnrelatedDeletedPaths {
		if _, exists := selected[deletedPath]; exists {
			return fmt.Errorf("path %q is both selected and an unrelated deletion", deletedPath)
		}
	}
	return nil
}

// WorkbenchSnapshot freezes the exact participating-repository revisions of an
// assembled Workbench. Subject branch policy is deliberately not represented.
type WorkbenchSnapshot struct {
	Resources map[string]SnapshotResource `json:"resources"`
}

type SnapshotResource struct {
	Shape         ResourceShape `json:"shape"`
	GitHub        string        `json:"github"`
	CanonicalPath string        `json:"canonicalPath"`
	Commit        string        `json:"commit"`
}

func DecodeWorkbenchSnapshot(encoded []byte) (WorkbenchSnapshot, error) {
	var value WorkbenchSnapshot
	if err := decodeStrict(encoded, &value); err != nil {
		return WorkbenchSnapshot{}, fmt.Errorf("decode WorkbenchSnapshot: %w", err)
	}
	if err := value.Validate(); err != nil {
		return WorkbenchSnapshot{}, err
	}
	return value, nil
}

func (snapshot WorkbenchSnapshot) Validate() error {
	if len(snapshot.Resources) == 0 {
		return fmt.Errorf("WorkbenchSnapshot resources is empty")
	}
	for identity, resource := range snapshot.Resources {
		declaration := Declaration{Shape: resource.Shape}
		derivedIdentity, err := declaration.Identity(resource.GitHub)
		if err != nil {
			return fmt.Errorf("snapshot resource %q: %w", identity, err)
		}
		canonicalPath, err := declaration.CanonicalPath(resource.GitHub)
		if err != nil {
			return fmt.Errorf("snapshot resource %q: %w", identity, err)
		}
		if identity != derivedIdentity {
			return fmt.Errorf("snapshot identity %q disagrees with shape-derived identity %q", identity, derivedIdentity)
		}
		if resource.CanonicalPath != canonicalPath {
			return fmt.Errorf("snapshot resource %q canonical path %q disagrees with %q", identity, resource.CanonicalPath, canonicalPath)
		}
		if !commitPattern.MatchString(resource.Commit) {
			return fmt.Errorf("snapshot resource %q commit %q is not an exact Git object id", identity, resource.Commit)
		}
	}
	return nil
}

func validResourceIdentity(identity string) bool {
	if packageScopePattern.MatchString(identity) {
		return true
	}
	normalized, err := NormalizeGitHubRepository(identity)
	return err == nil && normalized == identity
}

func validateUniquePaths(values []string, field string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateRelativePath(value); err != nil {
			return fmt.Errorf("%s entry %q: %w", field, value, err)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s entry %q is duplicated", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateRelativePath(value string) error {
	if value == "" || value == "." || strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("must be a non-empty relative path")
	}
	if len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\') {
		return fmt.Errorf("must be a relative path")
	}
	for _, component := range strings.FieldsFunc(value, func(character rune) bool { return character == '/' || character == '\\' }) {
		if component == ".." {
			return fmt.Errorf("must not contain ..")
		}
	}
	if path.Clean(strings.ReplaceAll(value, "\\", "/")) == "." {
		return fmt.Errorf("must name a path")
	}
	return nil
}
