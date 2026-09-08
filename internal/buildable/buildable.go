// Package buildable validates and runs repository-owned compiled artifacts.
package buildable

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
)

const (
	// ProjectionPath is the setup-owned buildable projection.
	ProjectionPath = ".workbench/buildables.json"
	// ProjectionSchemaDigest binds the projection to the released Pkl schema
	// interpreted by this binary. A schema edit must update this digest.
	ProjectionSchemaDigest  = "59f87f6155ed72e5bec0f9226330e84b0419f4df1c35f9baa85b0af2a925399d"
	projectionSchemaVersion = 1
)

var (
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	namePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// RefusalCode is a stable machine-readable reason a declared buildable cannot run.
type RefusalCode string

const (
	RefusalProjectionInvalid   RefusalCode = "projectionInvalid"
	RefusalProjectionStale     RefusalCode = "projectionStale"
	RefusalBuildableUndeclared RefusalCode = "buildableUndeclared"
	RefusalUnsupportedPlatform RefusalCode = "unsupportedPlatform"
	RefusalCandidateInvalid    RefusalCode = "candidateInvalid"
	RefusalDirtyProducerInputs RefusalCode = "dirtyProducerInputs"
	RefusalStaleProducerInputs RefusalCode = "staleProducerInputs"
	RefusalCandidatesAbsent    RefusalCode = "candidatesAbsent"
	RefusalNotExecutable       RefusalCode = "notExecutable"
)

// Refusal is a buildable that cannot lawfully run and its actionable remedy.
type Refusal struct {
	Code      RefusalCode `json:"code"`
	Buildable string      `json:"buildable"`
	Candidate string      `json:"candidate,omitempty"`
	Reason    string      `json:"reason"`
	Remedy    string      `json:"remedy"`
}

func (refusal *Refusal) Error() string {
	subject := fmt.Sprintf("buildable %q", refusal.Buildable)
	if refusal.Candidate != "" {
		subject += fmt.Sprintf(" candidate %q", refusal.Candidate)
	}
	return fmt.Sprintf("%s refused: %s\nNext action: %s", subject, refusal.Reason, refusal.Remedy)
}

// Resolution is one completely validated candidate and the complete output
// set for one declared platform. Candidate is an opaque declaration identity;
// candidate roots and manifest details are deliberately absent from this
// value.
type Resolution struct {
	SchemaVersion int               `json:"schemaVersion"`
	Buildable     string            `json:"buildable"`
	Candidate     string            `json:"candidate"`
	Platform      string            `json:"platform"`
	Outputs       []ResolvedOutput  `json:"outputs"`
	Capabilities  []string          `json:"capabilities"`
	Source        map[string]string `json:"source"`

	// Path is retained for source compatibility with pre-seam Go callers. It
	// is not serialized and is populated only for the single executable output.
	Path string `json:"-"`
}

// ResolvedOutput is a verified output handle.
// path is invocation-scoped and non-comparable; do not compare paths, compare digest and size; do not persist paths, persist destination.
// Path is already resolved to the bytes selected by Workbench; consumers must
// not reconstruct it from roots.
type ResolvedOutput struct {
	Path        string `json:"path"`
	Destination string `json:"destination"`
	Kind        string `json:"kind"`
	Digest      string `json:"digest"`
	Size        int64  `json:"size"`
	Executable  bool   `json:"executable"`
}

// CheckReport is the stable JSON result of the no-exec check command.
type CheckReport struct {
	SchemaVersion int      `json:"schemaVersion"`
	Status        string   `json:"status"`
	Buildable     string   `json:"buildable"`
	Candidate     string   `json:"candidate,omitempty"`
	Path          string   `json:"path,omitempty"`
	Refusal       *Refusal `json:"refusal,omitempty"`
}

// Projection is the strict setup-owned execution contract.
type Projection struct {
	SchemaVersion int                           `json:"schemaVersion"`
	SchemaDigest  string                        `json:"schemaDigest"`
	Buildables    map[string]ProjectedBuildable `json:"buildables"`
}

// ProjectedBuildable binds a declaration to its owning checkout and source.
type ProjectedBuildable struct {
	Owner       OwnerReference `json:"owner"`
	Declaration Buildable      `json:"declaration"`
}

// OwnerReference is the least authority needed to find and revalidate an owner.
type OwnerReference struct {
	Identity            string `json:"identity"`
	RepositoryPath      string `json:"repositoryPath"`
	SourceDigest        string `json:"sourceDigest,omitempty"`
	DeclarationIdentity string `json:"declarationIdentity"`
}

// ProjectionOwner is setup's pure input for one participating repository.
type ProjectionOwner struct {
	Identity       string
	RepositoryPath string
	Source         []byte
	Buildables     map[string]Buildable
}

// Buildable is the generated JSON projection of one workbench.pkl declaration.
type Buildable struct {
	InputDetection      InputDetection      `json:"inputDetection"`
	BuildCommand        BuildCommand        `json:"buildCommand"`
	VerificationCommand *BuildCommand       `json:"verificationCommand,omitempty"`
	Manifest            ManifestContract    `json:"manifest"`
	Candidates          []Candidate         `json:"candidates"`
	Platforms           map[string]Platform `json:"platforms"`
}

type InputDetection struct {
	Strategy string   `json:"strategy"`
	Paths    []string `json:"paths"`
}

type BuildCommand struct {
	Executable string   `json:"executable"`
	Arguments  []string `json:"arguments"`
}

type ManifestContract struct {
	SchemaVersion        int               `json:"schemaVersion"`
	Kind                 string            `json:"kind"`
	ContractID           string            `json:"contractId"`
	ExpectedSource       map[string]string `json:"expectedSource"`
	RequiredSourceFields []string          `json:"requiredSourceFields"`
	RequiredCapabilities []string          `json:"requiredCapabilities"`
}

type Candidate struct {
	Root          string `json:"root"`
	Identity      string `json:"identity,omitempty"`
	InputStrategy string `json:"inputStrategy"`
	InvalidRemedy string `json:"invalidRemedy"`
}

type Platform struct {
	OS   []string `json:"os"`
	Arch []string `json:"arch"`
	// Path and Executable are the 0.6 single-output shape. They remain
	// accepted so existing declarations continue to parse and run.
	Path       string   `json:"path,omitempty"`
	Executable bool     `json:"executable,omitempty"`
	Outputs    []Output `json:"outputs,omitempty"`
}

// Output is one candidate-relative generated file. Destination is relative
// to the explicit materialization destination, never to a candidate root.
type Output struct {
	Path        string `json:"path"`
	Destination string `json:"destination"`
	Kind        string `json:"kind"`
	Executable  bool   `json:"executable"`
}

type artifactManifest struct {
	SchemaVersion       int               `json:"schemaVersion"`
	Kind                string            `json:"kind"`
	ContractID          string            `json:"contractId"`
	DeclarationIdentity string            `json:"declarationIdentity,omitempty"`
	Source              map[string]string `json:"source"`
	ProducerInputs      struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"producerInputs"`
	Capabilities []string         `json:"capabilities"`
	Outputs      []artifactOutput `json:"outputs"`
}

type artifactOutput struct {
	Platform    string `json:"platform"`
	Path        string `json:"path"`
	Destination string `json:"destination,omitempty"`
	Kind        string `json:"kind,omitempty"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Executable  bool   `json:"executable"`
}

type candidateVerification struct {
	Root     string
	Manifest artifactManifest
	Outputs  map[string][]artifactOutput
}

// EncodeProjection validates and deterministically encodes all owner declarations.
func EncodeProjection(owners []ProjectionOwner) ([]byte, error) {
	projection := Projection{
		SchemaVersion: projectionSchemaVersion,
		SchemaDigest:  ProjectionSchemaDigest,
		Buildables:    make(map[string]ProjectedBuildable),
	}
	for _, owner := range owners {
		if strings.TrimSpace(owner.Identity) == "" {
			return nil, errors.New("buildable owner identity is empty")
		}
		if err := validateRelativePath(owner.RepositoryPath); err != nil {
			return nil, fmt.Errorf("buildable owner %q repository path: %w", owner.Identity, err)
		}
		if len(owner.Source) == 0 {
			return nil, fmt.Errorf("buildable owner %q workbench.pkl source is empty", owner.Identity)
		}
		sourceDigest := sha256.Sum256(owner.Source)
		for name, declaration := range owner.Buildables {
			if err := ValidateName(name); err != nil {
				return nil, fmt.Errorf("buildable owner %q: %w", owner.Identity, err)
			}
			if err := declaration.ValidateForName(name); err != nil {
				return nil, fmt.Errorf("buildable %q owned by %q: %w", name, owner.Identity, err)
			}
			if existing, duplicate := projection.Buildables[name]; duplicate {
				return nil, fmt.Errorf(
					"buildable %q is declared by both %q at %q and %q at %q",
					name,
					existing.Owner.Identity,
					existing.Owner.RepositoryPath,
					owner.Identity,
					owner.RepositoryPath,
				)
			}
			projection.Buildables[name] = ProjectedBuildable{
				Owner: OwnerReference{
					Identity:            owner.Identity,
					RepositoryPath:      owner.RepositoryPath,
					SourceDigest:        hex.EncodeToString(sourceDigest[:]),
					DeclarationIdentity: mustDeclarationIdentity(name, declaration),
				},
				Declaration: declaration,
			}
		}
	}
	encoded, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode buildable projection: %w", err)
	}
	return append(encoded, '\n'), nil
}

// ValidateName checks the authored buildable-name grammar shared with Pkl.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("buildable name %q is invalid", name)
	}
	return nil
}

// DeclarationIdentity returns the semantic identity of one buildable
// declaration. It intentionally excludes the raw workbench.pkl bytes so an
// unrelated declaration in the same file cannot stale this buildable.
func DeclarationIdentity(name string, declaration Buildable) (string, error) {
	if err := declaration.ValidateForName(name); err != nil {
		return "", err
	}
	normalized := normalizeDeclaration(name, declaration)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode buildable %q declaration identity: %w", name, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type declarationIdentityInput struct {
	Name        string    `json:"name"`
	Declaration Buildable `json:"declaration"`
}

func normalizeDeclaration(name string, declaration Buildable) declarationIdentityInput {
	normalized := declaration
	normalized.InputDetection.Paths = normalizePaths(declaration.InputDetection.Paths)
	normalized.Manifest.ExpectedSource = cloneStringMap(declaration.Manifest.ExpectedSource)
	normalized.Manifest.RequiredSourceFields = sortedStrings(declaration.Manifest.RequiredSourceFields)
	normalized.Manifest.RequiredCapabilities = sortedStrings(declaration.Manifest.RequiredCapabilities)
	normalized.Candidates = append([]Candidate(nil), declaration.Candidates...)
	for index := range normalized.Candidates {
		normalized.Candidates[index].Root = normalizePath(normalized.Candidates[index].Root)
	}
	normalized.Platforms = make(map[string]Platform, len(declaration.Platforms))
	for name, platform := range declaration.Platforms {
		platform.OS = sortedStrings(platform.OS)
		platform.Arch = sortedStrings(platform.Arch)
		platform.Path = normalizePath(platform.Path)
		platform.Outputs = append([]Output(nil), platform.Outputs...)
		for index := range platform.Outputs {
			platform.Outputs[index].Path = normalizePath(platform.Outputs[index].Path)
			platform.Outputs[index].Destination = normalizePath(platform.Outputs[index].Destination)
		}
		sort.Slice(platform.Outputs, func(left, right int) bool {
			if platform.Outputs[left].Path != platform.Outputs[right].Path {
				return platform.Outputs[left].Path < platform.Outputs[right].Path
			}
			return platform.Outputs[left].Destination < platform.Outputs[right].Destination
		})
		normalized.Platforms[name] = platform
	}
	return declarationIdentityInput{Name: name, Declaration: normalized}
}

func normalizePaths(paths []string) []string {
	normalized := make([]string, len(paths))
	for index, path := range paths {
		normalized[index] = normalizePath(path)
	}
	slicesSortStrings(normalized)
	return normalized
}

func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	slicesSortStrings(result)
	return result
}

func slicesSortStrings(values []string) {
	sort.Strings(values)
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// Resolve returns the first present, valid candidate in declared order.
func Resolve(ctx context.Context, workbenchRoot, name, hostOS, hostArch string) (Resolution, error) {
	ownerRoot, declaration, err := loadProjectedDeclaration(workbenchRoot, name)
	if err != nil {
		return Resolution{}, err
	}
	platformName, _, err := declaration.platform(hostOS, hostArch)
	if err != nil {
		return Resolution{}, &Refusal{
			Code: RefusalUnsupportedPlatform, Buildable: name, Reason: err.Error(),
			Remedy: "Run this buildable on one of its declared host platforms.",
		}
	}
	return resolveDeclaredPlatform(ctx, ownerRoot, name, declaration, platformName)
}

// ResolvePlatform resolves one exact declared platform from the generated
// projection. It is the hot-path counterpart to ResolveDeclaredPlatform.
func ResolvePlatform(ctx context.Context, workbenchRoot, name, platformName string) (Resolution, error) {
	ownerRoot, declaration, err := loadProjectedDeclaration(workbenchRoot, name)
	if err != nil {
		return Resolution{}, err
	}
	return resolveDeclaredPlatform(ctx, ownerRoot, name, declaration, platformName)
}

func loadProjectedDeclaration(workbenchRoot, name string) (string, Buildable, error) {
	// This loader is deliberately projection-only. Callers that have evaluated
	// the current workbench.pkl must run ValidateProjectedDeclaration first;
	// this package cannot evaluate Pkl and must never compare a projection's
	// declaration identity with the same projected declaration.
	root, err := filepath.Abs(workbenchRoot)
	if err != nil {
		return "", Buildable{}, fmt.Errorf("resolve workbench root: %w", err)
	}
	projection, err := loadProjection(root)
	if err != nil {
		return "", Buildable{}, &Refusal{
			Code: RefusalProjectionInvalid, Buildable: name, Reason: err.Error(),
			Remedy: "Run 'workbench setup' to regenerate the buildable projection.",
		}
	}
	projected, exists := projection.Buildables[name]
	if !exists {
		return "", Buildable{}, &Refusal{
			Code: RefusalBuildableUndeclared, Buildable: name,
			Reason: "no participating repository declares this buildable",
			Remedy: "Declare the buildable in an owning workbench.pkl and run 'workbench setup'.",
		}
	}
	ownerRoot, err := existingPathWithin(root, projected.Owner.RepositoryPath)
	if err != nil {
		return "", Buildable{}, &Refusal{
			Code: RefusalProjectionStale, Buildable: name,
			Reason: fmt.Sprintf("projected owner %q at %q is unavailable: %v", projected.Owner.Identity, projected.Owner.RepositoryPath, err),
			Remedy: "Run 'workbench setup' to reconcile the owning checkout and regenerate the projection.",
		}
	}
	source, err := os.ReadFile(filepath.Join(ownerRoot, "workbench.pkl"))
	if err != nil {
		return "", Buildable{}, &Refusal{
			Code: RefusalProjectionStale, Buildable: name,
			Reason: fmt.Sprintf("read projected owner %q workbench.pkl: %v", projected.Owner.Identity, err),
			Remedy: "Run 'workbench setup' to reconcile the owning checkout and regenerate the projection.",
		}
	}
	sourceDigest := sha256.Sum256(source)
	currentSourceDigest := hex.EncodeToString(sourceDigest[:])
	if projected.Owner.DeclarationIdentity == "" && currentSourceDigest != projected.Owner.SourceDigest {
		return "", Buildable{}, &Refusal{
			Code: RefusalProjectionStale, Buildable: name,
			Reason: fmt.Sprintf("generated declaration digest is %s, current %s", projected.Owner.SourceDigest, currentSourceDigest),
			Remedy: "Run 'workbench setup' to regenerate the buildable projection from the current workbench.pkl.",
		}
	}
	return ownerRoot, projected.Declaration, nil
}

// ValidateProjectedDeclaration compares a current, semantically evaluated
// declaration with the optional setup projection. A missing projection is the
// cold state and succeeds; a present projection is only an optimization when
// its per-buildable identity still matches the current declaration.
func ValidateProjectedDeclaration(workbenchRoot, name string, declaration Buildable) error {
	root, err := filepath.Abs(workbenchRoot)
	if err != nil {
		return fmt.Errorf("resolve workbench root: %w", err)
	}
	projectionPath := filepath.Join(root, filepath.FromSlash(ProjectionPath))
	if _, err := os.Lstat(projectionPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return &Refusal{
			Code: RefusalProjectionInvalid, Buildable: name,
			Reason: "observe generated buildable projection: " + err.Error(),
			Remedy: "Run 'workbench setup' to regenerate the buildable projection.",
		}
	}
	projection, err := loadProjection(root)
	if err != nil {
		return &Refusal{
			Code: RefusalProjectionInvalid, Buildable: name, Reason: err.Error(),
			Remedy: "Run 'workbench setup' to regenerate the buildable projection.",
		}
	}
	projected, exists := projection.Buildables[name]
	if !exists {
		return &Refusal{
			Code: RefusalProjectionStale, Buildable: name,
			Reason: "generated projection does not contain the current buildable declaration",
			Remedy: "Run 'workbench setup' to regenerate the buildable projection.",
		}
	}
	identity, err := DeclarationIdentity(name, declaration)
	if err != nil {
		return &Refusal{
			Code: RefusalProjectionStale, Buildable: name, Reason: err.Error(),
			Remedy: "Run 'workbench setup' to regenerate the buildable projection.",
		}
	}
	if projected.Owner.DeclarationIdentity == "" {
		ownerRoot, ownerErr := existingPathWithin(root, projected.Owner.RepositoryPath)
		if ownerErr != nil {
			return &Refusal{
				Code: RefusalProjectionStale, Buildable: name,
				Reason: fmt.Sprintf("projected owner %q is unavailable: %v", projected.Owner.Identity, ownerErr),
				Remedy: "Run 'workbench setup' to reconcile the owning checkout and regenerate the projection.",
			}
		}
		source, sourceErr := os.ReadFile(filepath.Join(ownerRoot, "workbench.pkl"))
		if sourceErr != nil {
			return &Refusal{
				Code: RefusalProjectionStale, Buildable: name,
				Reason: fmt.Sprintf("read projected owner %q workbench.pkl: %v", projected.Owner.Identity, sourceErr),
				Remedy: "Run 'workbench setup' to reconcile the owning checkout and regenerate the projection.",
			}
		}
		sourceDigest := sha256.Sum256(source)
		currentSourceDigest := hex.EncodeToString(sourceDigest[:])
		if currentSourceDigest != projected.Owner.SourceDigest {
			return &Refusal{
				Code: RefusalProjectionStale, Buildable: name,
				Reason: fmt.Sprintf("generated declaration digest is %s, current %s", projected.Owner.SourceDigest, currentSourceDigest),
				Remedy: "Run 'workbench setup' to regenerate the buildable projection from the current workbench.pkl.",
			}
		}
		return nil
	}
	if projected.Owner.DeclarationIdentity != identity {
		return &Refusal{
			Code: RefusalProjectionStale, Buildable: name,
			Reason: fmt.Sprintf("generated declaration identity is %s, current %s", projected.Owner.DeclarationIdentity, identity),
			Remedy: "Run 'workbench setup' to regenerate the buildable projection from the current workbench.pkl.",
		}
	}
	return nil
}

// ResolveDeclared is the projection-independent seam for producer contract proofs.
func ResolveDeclared(ctx context.Context, repositoryRoot, name string, declaration Buildable, hostOS, hostArch string) (Resolution, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve repository root: %w", err)
	}
	return resolveDeclared(ctx, root, name, declaration, hostOS, hostArch)
}

func resolveDeclared(ctx context.Context, root, name string, declaration Buildable, hostOS, hostArch string) (Resolution, error) {
	platformName, _, err := declaration.platform(hostOS, hostArch)
	if err != nil {
		return Resolution{}, &Refusal{
			Code: RefusalUnsupportedPlatform, Buildable: name, Reason: err.Error(),
			Remedy: "Run this buildable on one of its declared host platforms.",
		}
	}
	return resolveDeclaredPlatform(ctx, root, name, declaration, platformName)
}

// ResolveDeclaredPlatform resolves one exact platform from a directly
// evaluated declaration. This is the projection-independent cold seam.
func ResolveDeclaredPlatform(ctx context.Context, repositoryRoot, name string, declaration Buildable, platformName string) (Resolution, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve repository root: %w", err)
	}
	return resolveDeclaredPlatform(ctx, root, name, declaration, platformName)
}

func resolveDeclaredPlatform(ctx context.Context, root, name string, declaration Buildable, platformName string) (Resolution, error) {
	if err := ValidateName(name); err != nil {
		return Resolution{}, err
	}
	if err := declaration.ValidateForName(name); err != nil {
		return Resolution{}, fmt.Errorf("buildable %q configuration: %w", name, err)
	}
	platform, exists := declaration.Platforms[platformName]
	if !exists {
		return Resolution{}, &Refusal{
			Code: RefusalUnsupportedPlatform, Buildable: name,
			Reason: fmt.Sprintf("platform %q is not declared", platformName),
			Remedy: "Choose one of the platform names declared by the owning workbench.pkl.",
		}
	}
	candidate, verified, err := resolveCandidate(ctx, root, name, declaration, platformName)
	if err != nil {
		return Resolution{}, err
	}
	resolution := Resolution{
		SchemaVersion: 1, Buildable: name, Candidate: candidate.Identity, Platform: platformName,
		Capabilities: append([]string(nil), verified.Manifest.Capabilities...),
		Source:       cloneStringMap(verified.Manifest.Source),
		Outputs:      make([]ResolvedOutput, 0, len(verified.Outputs[platformName])),
	}
	for _, output := range verified.Outputs[platformName] {
		resolvedPath, pathErr := existingPathWithin(verified.Root, output.Path)
		if pathErr != nil {
			return Resolution{}, &Refusal{
				Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Identity,
				Reason: fmt.Sprintf("selected output %q is missing or escapes the candidate", output.Path),
				Remedy: candidate.InvalidRemedy,
			}
		}
		kind, destination := output.Kind, output.Destination
		if kind == "" || destination == "" {
			if declared, declaredExists := declaredOutput(platform, output.Path); declaredExists {
				if kind == "" {
					kind = declared.Kind
				}
				if destination == "" && len(platform.Outputs) > 0 {
					destination = declared.Destination
				}
			}
		}
		resolution.Outputs = append(resolution.Outputs, ResolvedOutput{
			Path: resolvedPath, Destination: destination, Kind: kind,
			Digest: output.SHA256, Size: output.Size, Executable: output.Executable,
		})
	}
	if output, exists := executableOutput(platform); exists {
		resolution.Path, err = existingPathWithin(verified.Root, output.Path)
		if err != nil {
			return Resolution{}, &Refusal{
				Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Identity,
				Reason: fmt.Sprintf("selected executable output %q is missing or escapes the candidate", output.Path),
				Remedy: candidate.InvalidRemedy,
			}
		}
	}
	return resolution, nil
}

func resolveCandidate(ctx context.Context, root, name string, declaration Buildable, selectedPlatform string) (Candidate, candidateVerification, error) {
	for index, candidate := range declaration.Candidates {
		candidate.Identity = candidateIdentity(candidate, index)
		present, err := candidatePresent(root, candidate, declaration.Platforms)
		if err != nil {
			return Candidate{}, candidateVerification{}, fmt.Errorf("observe buildable %q candidate %q: %w", name, candidate.Identity, err)
		}
		if !present {
			continue
		}
		verified, code, err := verifyCandidate(ctx, root, name, declaration, candidate)
		if err != nil {
			return Candidate{}, candidateVerification{}, &Refusal{Code: code, Buildable: name, Candidate: candidate.Identity, Reason: err.Error(), Remedy: candidate.InvalidRemedy}
		}
		if _, exists := verified.Outputs[selectedPlatform]; !exists {
			return Candidate{}, candidateVerification{}, &Refusal{
				Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Identity,
				Reason: fmt.Sprintf("candidate has no validated outputs for platform %q", selectedPlatform), Remedy: candidate.InvalidRemedy,
			}
		}
		return candidate, verified, nil
	}
	return Candidate{}, candidateVerification{}, &Refusal{
		Code: RefusalCandidatesAbsent, Buildable: name,
		Reason: "no declared artifact candidate is present",
		Remedy: fmt.Sprintf("Run '%s' to create the preferred local candidate.", declaration.BuildCommand.String()),
	}
}

// Check validates without executing or mutating a candidate.
func Check(ctx context.Context, workbenchRoot, name, hostOS, hostArch string) (CheckReport, error) {
	resolution, err := Resolve(ctx, workbenchRoot, name, hostOS, hostArch)
	if err == nil {
		return CheckReport{
			SchemaVersion: 1, Status: "valid", Buildable: name,
			Candidate: resolution.Candidate, Path: resolution.Path,
		}, nil
	}
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return CheckReport{SchemaVersion: 1, Status: "refused", Buildable: name, Refusal: refusal}, err
	}
	return CheckReport{}, err
}

// CheckCurrent is the CLI composition for the current host platform.
func CheckCurrent(ctx context.Context, workbenchRoot, name string) (CheckReport, error) {
	return Check(ctx, workbenchRoot, name, runtime.GOOS, runtime.GOARCH)
}

// Run validates and replaces Workbench with the selected artifact process.
func Run(ctx context.Context, workbenchRoot, name string, arguments []string) error {
	resolution, err := Resolve(ctx, workbenchRoot, name, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	return RunResolved(resolution, name, arguments)
}

// RunResolved executes the executable output selected by a verified
// resolution. It performs no candidate selection or artifact verification.
func RunResolved(resolution Resolution, name string, arguments []string) error {
	path := resolution.Path
	if path == "" {
		for _, output := range resolution.Outputs {
			if output.Executable {
				path = output.Path
				break
			}
		}
	}
	if path == "" {
		return &Refusal{
			Code: RefusalNotExecutable, Buildable: name, Candidate: resolution.Candidate,
			Reason: fmt.Sprintf("platform %q declares no executable output; run is only for executable buildables", resolution.Platform),
			Remedy: "Use 'workbench buildable materialize' with an explicit destination for this module.",
		}
	}
	argv := append([]string{path}, arguments...)
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec buildable %q at %q: %w", name, path, err)
	}
	return nil
}

func loadProjection(root string) (Projection, error) {
	path := filepath.Join(root, filepath.FromSlash(ProjectionPath))
	file, err := os.Open(path)
	if err != nil {
		return Projection{}, fmt.Errorf("read generated buildable projection %q: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReader(file))
	decoder.DisallowUnknownFields()
	var projection Projection
	if err := decoder.Decode(&projection); err != nil {
		return Projection{}, fmt.Errorf("decode generated buildable projection %q: %w", path, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Projection{}, fmt.Errorf("decode generated buildable projection %q: %w", path, err)
	}
	if projection.SchemaVersion != projectionSchemaVersion || projection.SchemaDigest != ProjectionSchemaDigest {
		return Projection{}, fmt.Errorf(
			"projection schema is version %d digest %q, want version %d digest %q",
			projection.SchemaVersion,
			projection.SchemaDigest,
			projectionSchemaVersion,
			ProjectionSchemaDigest,
		)
	}
	if projection.Buildables == nil {
		return Projection{}, errors.New("generated buildable projection omits buildables")
	}
	for name, projected := range projection.Buildables {
		if err := ValidateName(name); err != nil {
			return Projection{}, err
		}
		if strings.TrimSpace(projected.Owner.Identity) == "" || !sha256Pattern.MatchString(projected.Owner.SourceDigest) {
			return Projection{}, fmt.Errorf("buildable %q owner reference is incomplete", name)
		}
		if projected.Owner.DeclarationIdentity != "" && !sha256Pattern.MatchString(projected.Owner.DeclarationIdentity) {
			return Projection{}, fmt.Errorf("buildable %q declaration identity is incomplete", name)
		}
		if err := validateRelativePath(projected.Owner.RepositoryPath); err != nil {
			return Projection{}, fmt.Errorf("buildable %q owner repository path: %w", name, err)
		}
		if err := projected.Declaration.ValidateForName(name); err != nil {
			return Projection{}, fmt.Errorf("buildable %q declaration: %w", name, err)
		}
	}
	return projection, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are present")
		}
		return err
	}
	return nil
}

// Validate proves the declaration's closed configuration invariants.
func (buildable Buildable) Validate() error {
	if buildable.InputDetection.Strategy != "gitHeadTree" {
		return fmt.Errorf("unsupported input detection strategy %q", buildable.InputDetection.Strategy)
	}
	if len(buildable.InputDetection.Paths) == 0 {
		return errors.New("input detection paths are empty")
	}
	for _, path := range buildable.InputDetection.Paths {
		if err := validateRelativePath(path); err != nil {
			return fmt.Errorf("input path %q: %w", path, err)
		}
	}
	if duplicates(buildable.InputDetection.Paths) {
		return errors.New("input detection paths contain duplicates")
	}
	if err := buildable.BuildCommand.validate("build command"); err != nil {
		return err
	}
	if buildable.VerificationCommand != nil {
		if err := buildable.VerificationCommand.validate("verification command"); err != nil {
			return err
		}
	}
	if buildable.Manifest.SchemaVersion <= 0 || buildable.Manifest.Kind == "" || buildable.Manifest.ContractID == "" {
		return errors.New("manifest contract identity is incomplete")
	}
	for field, value := range buildable.Manifest.ExpectedSource {
		if field == "" || value == "" {
			return errors.New("expected manifest source contains an empty field or value")
		}
	}
	if duplicates(buildable.Manifest.RequiredSourceFields) {
		return errors.New("required manifest source fields contain duplicates")
	}
	if duplicates(buildable.Manifest.RequiredCapabilities) {
		return errors.New("required manifest capabilities contain duplicates")
	}
	if len(buildable.Candidates) == 0 {
		return errors.New("candidate preference is empty")
	}
	seenCandidates := make(map[string]struct{}, len(buildable.Candidates))
	for _, candidate := range buildable.Candidates {
		if err := validateRelativePath(candidate.Root); err != nil {
			return fmt.Errorf("candidate root %q: %w", candidate.Root, err)
		}
		if strings.TrimSpace(candidate.InvalidRemedy) == "" {
			return fmt.Errorf("candidate %q invalid remedy is empty", candidate.Root)
		}
		if candidate.Identity != "" && !namePattern.MatchString(candidate.Identity) {
			return fmt.Errorf("candidate %q identity is invalid", candidate.Root)
		}
		if candidate.InputStrategy != "gitWorktree" && candidate.InputStrategy != "gitHeadTree" {
			return fmt.Errorf("candidate %q has unsupported input strategy %q", candidate.Root, candidate.InputStrategy)
		}
		if _, duplicate := seenCandidates[candidate.Root]; duplicate {
			return fmt.Errorf("candidate root %q is duplicated", candidate.Root)
		}
		seenCandidates[candidate.Root] = struct{}{}
	}
	if len(buildable.Platforms) == 0 {
		return errors.New("platform outputs are empty")
	}
	seenOutputPaths := make(map[string]string, len(buildable.Platforms))
	for name, platform := range buildable.Platforms {
		if name == "" || len(platform.OS) == 0 || len(platform.Arch) == 0 {
			return fmt.Errorf("platform %q has incomplete host aliases", name)
		}
		if duplicates(platform.OS) || duplicates(platform.Arch) {
			return fmt.Errorf("platform %q host aliases are empty or duplicated", name)
		}
		outputs, err := platform.outputs()
		if err != nil {
			return fmt.Errorf("platform %q: %w", name, err)
		}
		seenDestinations := make(map[string]string, len(outputs))
		executableOutputs := 0
		for _, output := range outputs {
			if err := validateRelativePath(output.Path); err != nil {
				return fmt.Errorf("platform %q output path: %w", name, err)
			}
			normalizedPath := strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(output.Path))))
			if previous, duplicate := seenOutputPaths[normalizedPath]; duplicate {
				return fmt.Errorf("platforms %q and %q share normalized output path %q", previous, name, normalizedPath)
			}
			seenOutputPaths[normalizedPath] = name
			if output.Kind == "" {
				return fmt.Errorf("platform %q output %q kind is empty", name, output.Path)
			}
			if output.Executable {
				executableOutputs++
			}
			if len(platform.Outputs) > 0 {
				if err := validateRelativePath(output.Destination); err != nil {
					return fmt.Errorf("platform %q output %q destination: %w", name, output.Path, err)
				}
				normalizedDestination := strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(output.Destination))))
				if previous, duplicate := seenDestinations[normalizedDestination]; duplicate {
					return fmt.Errorf("platform %q outputs %q and %q share normalized destination %q", name, previous, output.Path, normalizedDestination)
				}
				seenDestinations[normalizedDestination] = output.Path
			}
		}
		if executableOutputs > 1 {
			return fmt.Errorf("platform %q declares %d executable outputs; run requires exactly one", name, executableOutputs)
		}
		for _, left := range outputs {
			for _, right := range outputs {
				if left.Path != right.Path && pathPrefix(left.Path, right.Path) {
					return fmt.Errorf("platform %q outputs have colliding paths %q and %q", name, left.Path, right.Path)
				}
				if len(platform.Outputs) > 0 && left.Destination != right.Destination && pathPrefix(left.Destination, right.Destination) {
					return fmt.Errorf("platform %q outputs have colliding destinations %q and %q", name, left.Destination, right.Destination)
				}
			}
		}
	}
	return nil
}

func (platform Platform) outputs() ([]Output, error) {
	if len(platform.Outputs) == 0 {
		if platform.Path == "" {
			return nil, errors.New("output set is empty")
		}
		return []Output{{Path: platform.Path, Kind: "executable", Executable: platform.Executable}}, nil
	}
	if platform.Path != "" {
		return nil, errors.New("legacy path cannot be combined with an output set")
	}
	return platform.Outputs, nil
}

func pathPrefix(parent, child string) bool {
	parent = filepath.ToSlash(filepath.Clean(filepath.FromSlash(parent)))
	child = filepath.ToSlash(filepath.Clean(filepath.FromSlash(child)))
	return strings.HasPrefix(child, parent+"/")
}

func executableOutput(platform Platform) (Output, bool) {
	outputs, err := platform.outputs()
	if err != nil {
		return Output{}, false
	}
	for _, output := range outputs {
		if output.Executable {
			return output, true
		}
	}
	return Output{}, false
}

func declaredOutput(platform Platform, path string) (Output, bool) {
	outputs, err := platform.outputs()
	if err != nil {
		return Output{}, false
	}
	for _, output := range outputs {
		if filepath.ToSlash(filepath.Clean(output.Path)) == filepath.ToSlash(filepath.Clean(path)) {
			return output, true
		}
	}
	return Output{}, false
}

// ValidateForName adds the fixed local-then-CI candidate topology.
func (buildable Buildable) ValidateForName(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := buildable.Validate(); err != nil {
		return err
	}
	want := []string{".local-build/" + name, ".ci-build/" + name}
	wantStrategies := []string{"gitWorktree", "gitHeadTree"}
	if len(buildable.Candidates) != len(want) {
		return fmt.Errorf("candidate preference has %d entries, want local then CI", len(buildable.Candidates))
	}
	for index, root := range want {
		if filepath.ToSlash(filepath.Clean(buildable.Candidates[index].Root)) != root {
			return fmt.Errorf("candidate %d root is %q, want %q", index, buildable.Candidates[index].Root, root)
		}
		if buildable.Candidates[index].InputStrategy != wantStrategies[index] {
			return fmt.Errorf("candidate %d input strategy is %q, want %q", index, buildable.Candidates[index].InputStrategy, wantStrategies[index])
		}
	}
	return nil
}

func candidateIdentity(candidate Candidate, index int) string {
	if candidate.Identity != "" {
		return candidate.Identity
	}
	// The 0.6 declaration has no authored identity field. This compatibility
	// mapping stays inside Workbench; the mapping is never exposed as a root.
	switch index {
	case 0:
		return "local"
	case 1:
		return "committed"
	default:
		return fmt.Sprintf("candidate-%d", index+1)
	}
}

func mustDeclarationIdentity(name string, declaration Buildable) string {
	identity, err := DeclarationIdentity(name, declaration)
	if err != nil {
		panic(err)
	}
	return identity
}

func (buildable Buildable) platform(hostOS, hostArch string) (string, Platform, error) {
	names := make([]string, 0, len(buildable.Platforms))
	for name := range buildable.Platforms {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		platform := buildable.Platforms[name]
		if contains(platform.OS, hostOS) && contains(platform.Arch, hostArch) {
			return name, platform, nil
		}
	}
	return "", Platform{}, fmt.Errorf("host platform %s/%s is unsupported", hostOS, hostArch)
}

func candidatePresent(root string, candidate Candidate, _ map[string]Platform) (bool, error) {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(candidate.Root)))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, errors.New("observe declared candidate path")
}

func verifyCandidate(ctx context.Context, root, name string, buildable Buildable, candidate Candidate) (candidateVerification, RefusalCode, error) {
	candidateRoot, err := existingPathWithin(root, candidate.Root)
	if err != nil {
		return candidateVerification{}, RefusalCandidateInvalid, errors.New("candidate root is not confined to the repository")
	}
	manifestPath, err := existingPathWithin(candidateRoot, "manifest.json")
	if err != nil {
		return candidateVerification{}, RefusalCandidateInvalid, errors.New("manifest is missing or escapes the candidate root")
	}
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return candidateVerification{}, RefusalCandidateInvalid, err
	}
	if err := validateManifest(name, buildable, manifest); err != nil {
		return candidateVerification{}, RefusalCandidateInvalid, err
	}
	currentDigest, err := candidateInputDigest(ctx, root, candidate, buildable.InputDetection.Paths)
	if err != nil {
		return candidateVerification{}, RefusalCandidateInvalid, err
	}
	if manifest.ProducerInputs.Digest != currentDigest {
		return candidateVerification{}, RefusalStaleProducerInputs, fmt.Errorf("stale artifact: producer input digest is %s, current inputs require %s", manifest.ProducerInputs.Digest, currentDigest)
	}
	outputs := make(map[string][]artifactOutput, len(buildable.Platforms))
	for _, output := range manifest.Outputs {
		artifactPath, err := existingPathWithin(candidateRoot, output.Path)
		if err != nil {
			return candidateVerification{}, RefusalCandidateInvalid, fmt.Errorf("output %s is missing or escapes the candidate root", output.Path)
		}
		if err := verifyOutput(artifactPath, output); err != nil {
			return candidateVerification{}, RefusalCandidateInvalid, fmt.Errorf("output %s: %w", output.Path, err)
		}
		outputs[output.Platform] = append(outputs[output.Platform], output)
	}
	return candidateVerification{Root: candidateRoot, Manifest: manifest, Outputs: outputs}, "", nil
}

func readManifest(path string) (artifactManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return artifactManifest{}, fmt.Errorf("read candidate manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReader(file))
	decoder.DisallowUnknownFields()
	var manifest artifactManifest
	if err := decoder.Decode(&manifest); err != nil {
		return artifactManifest{}, fmt.Errorf("decode candidate manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return artifactManifest{}, fmt.Errorf("decode candidate manifest: %w", err)
	}
	return manifest, nil
}

func validateManifest(name string, buildable Buildable, manifest artifactManifest) error {
	contract := buildable.Manifest
	if manifest.SchemaVersion != contract.SchemaVersion || manifest.Kind != contract.Kind || manifest.ContractID != contract.ContractID {
		return fmt.Errorf("manifest contract identity is %d/%s/%s, want %d/%s/%s", manifest.SchemaVersion, manifest.Kind, manifest.ContractID, contract.SchemaVersion, contract.Kind, contract.ContractID)
	}
	if name != "" {
		identity, err := DeclarationIdentity(name, buildable)
		if err != nil {
			return err
		}
		if manifest.DeclarationIdentity != "" && manifest.DeclarationIdentity != identity {
			return fmt.Errorf("manifest declaration identity is %q, want %q", manifest.DeclarationIdentity, identity)
		}
	}
	for field, expected := range contract.ExpectedSource {
		if manifest.Source[field] != expected {
			return fmt.Errorf("manifest source field %q is %q, want %q", field, manifest.Source[field], expected)
		}
	}
	for _, field := range contract.RequiredSourceFields {
		if manifest.Source[field] == "" {
			return fmt.Errorf("manifest source field %q is required", field)
		}
	}
	if manifest.ProducerInputs.Algorithm != "sha256" || !sha256Pattern.MatchString(manifest.ProducerInputs.Digest) {
		return errors.New("producer input digest is not lowercase SHA-256")
	}
	if duplicates(manifest.Capabilities) {
		return errors.New("manifest capabilities contain duplicates")
	}
	for _, required := range contract.RequiredCapabilities {
		if !contains(manifest.Capabilities, required) {
			return fmt.Errorf("manifest lacks required capability %q", required)
		}
	}
	wantedOutputs := 0
	for platformName, platform := range buildable.Platforms {
		outputs, err := platform.outputs()
		if err != nil {
			return fmt.Errorf("platform %q: %w", platformName, err)
		}
		wantedOutputs += len(outputs)
	}
	if len(manifest.Outputs) != wantedOutputs {
		return fmt.Errorf("manifest has %d outputs, want %d", len(manifest.Outputs), wantedOutputs)
	}
	seen := make(map[string]struct{}, len(manifest.Outputs))
	for _, output := range manifest.Outputs {
		platform, exists := buildable.Platforms[output.Platform]
		if !exists {
			return fmt.Errorf("manifest has unexpected platform %q", output.Platform)
		}
		declaredOutputs, err := platform.outputs()
		if err != nil {
			return fmt.Errorf("platform %q: %w", output.Platform, err)
		}
		key := outputKey(output.Platform, output.Path)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("manifest repeats output %s", key)
		}
		seen[key] = struct{}{}
		var declared *Output
		for index := range declaredOutputs {
			if filepath.ToSlash(filepath.Clean(declaredOutputs[index].Path)) == filepath.ToSlash(filepath.Clean(output.Path)) {
				declared = &declaredOutputs[index]
				break
			}
		}
		if declared == nil {
			return fmt.Errorf("manifest output path for %s is %q not declared", output.Platform, output.Path)
		}
		kind := output.Kind
		if len(platform.Outputs) == 0 && kind == "" {
			kind = declared.Kind
		}
		if !sha256Pattern.MatchString(output.SHA256) || output.Size <= 0 || output.Executable != declared.Executable || kind != declared.Kind {
			return fmt.Errorf("manifest output facts for %s are invalid", output.Platform)
		}
		if len(platform.Outputs) > 0 && filepath.ToSlash(filepath.Clean(output.Destination)) != filepath.ToSlash(filepath.Clean(declared.Destination)) {
			return fmt.Errorf("manifest output destination for %s is %q, want %q", output.Platform, output.Destination, declared.Destination)
		}
	}
	for platformName, platform := range buildable.Platforms {
		outputs, _ := platform.outputs()
		for _, declared := range outputs {
			if _, exists := seen[outputKey(platformName, declared.Path)]; !exists {
				return fmt.Errorf("manifest omits output %s", outputKey(platformName, declared.Path))
			}
		}
	}
	return nil
}

func outputKey(platform, path string) string {
	return platform + "\x00" + filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
}

func gitWorktreeStatus(ctx context.Context, root string, paths []string) ([]byte, error) {
	arguments := []string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--"}
	arguments = append(arguments, paths...)
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect producer worktree inputs with git: %w", err)
	}
	return output, nil
}

func candidateInputDigest(ctx context.Context, root string, candidate Candidate, paths []string) (string, error) {
	paths = meaningfulProducerInputPaths(paths)
	if len(paths) == 0 {
		return "", errors.New("meaningful producer input inventory is empty")
	}
	switch candidate.InputStrategy {
	case "gitHeadTree":
		return gitHeadTreeDigest(ctx, root, paths)
	case "gitWorktree":
		return gitWorktreeDigest(ctx, root, paths)
	default:
		return "", fmt.Errorf("unsupported candidate input strategy %q", candidate.InputStrategy)
	}
}

func meaningfulProducerInputPaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		normalized := normalizePath(path)
		// The declaration's semantic identity represents workbench.pkl. Its
		// raw bytes must never also participate in producer freshness.
		if normalized == "workbench.pkl" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}

func gitWorktreeDigest(ctx context.Context, root string, paths []string) (string, error) {
	headDigest, err := gitHeadTreeDigest(ctx, root, paths)
	if err != nil {
		return "", err
	}
	status, err := gitWorktreeStatus(ctx, root, paths)
	if err != nil {
		return "", err
	}
	if len(status) == 0 {
		return headDigest, nil
	}
	current, err := worktreeContentDigest(ctx, root, paths)
	if err != nil {
		return "", err
	}
	after, err := worktreeContentDigest(ctx, root, paths)
	if err != nil {
		return "", err
	}
	if after != current {
		return "", errors.New("producer worktree inputs changed while computing their digest")
	}
	return current, nil
}

type worktreeInventoryRecord struct {
	mode, objectType, objectID, path string
}

func worktreeContentDigest(ctx context.Context, root string, paths []string) (string, error) {
	head, err := headTreeInventory(ctx, root, paths)
	if err != nil {
		return "", err
	}
	records := make(map[string]worktreeInventoryRecord)
	ordered := append([]string(nil), paths...)
	sort.Strings(ordered)
	for _, relative := range ordered {
		if err := validateRelativePath(relative); err != nil {
			return "", fmt.Errorf("producer worktree path %q: %w", relative, err)
		}
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		if _, err := os.Lstat(absolute); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("inspect producer worktree path %q: %w", relative, err)
		}
		if err := filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path != absolute && entry.Name() == ".git" {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			factPath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relativeFact := filepath.ToSlash(factPath)
			if err := validateRelativePath(relativeFact); err != nil {
				return fmt.Errorf("producer worktree path %q: %w", relativeFact, err)
			}
			if entry.IsDir() {
				if tracked, exists := head[relativeFact]; exists && tracked.mode == "160000" {
					record, err := currentGitlinkRecord(ctx, path, relativeFact)
					if err != nil {
						return err
					}
					records[relativeFact] = record
					return filepath.SkipDir
				}
				return nil
			}
			if gitlink, exists := enclosingGitlink(head, relativeFact); exists {
				record, err := worktreePathRecord(ctx, root, relativeFact, path, entry)
				if err != nil {
					return err
				}
				matches, err := matchesSubmoduleHEAD(ctx, root, gitlink.path, record)
				if err != nil {
					return err
				}
				if !matches {
					records[relativeFact] = record
				}
				return nil
			}
			if _, tracked := head[relativeFact]; !tracked {
				ignored, err := gitIgnoredWithoutIndex(ctx, root, relativeFact)
				if err != nil {
					return err
				}
				if ignored {
					return nil
				}
			}
			record, err := worktreePathRecord(ctx, root, relativeFact, path, entry)
			if err != nil {
				return err
			}
			records[relativeFact] = record
			return nil
		}); err != nil {
			return "", fmt.Errorf("fingerprint producer worktree path %q: %w", relative, err)
		}
	}
	pathsInInventory := make([]string, 0, len(records))
	for path := range records {
		pathsInInventory = append(pathsInInventory, path)
	}
	sort.Strings(pathsInInventory)
	digest := sha256.New()
	for _, path := range pathsInInventory {
		record := records[path]
		if _, err := fmt.Fprintf(digest, "%s %s %s\t%s\n", record.mode, record.objectType, record.objectID, record.path); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func enclosingGitlink(head map[string]worktreeInventoryRecord, relative string) (worktreeInventoryRecord, bool) {
	for candidate := filepath.ToSlash(filepath.Dir(relative)); candidate != "."; candidate = filepath.ToSlash(filepath.Dir(candidate)) {
		if record, exists := head[candidate]; exists && record.mode == "160000" {
			return record, true
		}
	}
	return worktreeInventoryRecord{}, false
}

func matchesSubmoduleHEAD(ctx context.Context, root, gitlinkPath string, actual worktreeInventoryRecord) (bool, error) {
	nested := strings.TrimPrefix(actual.path, gitlinkPath+"/")
	arguments := []string{"-c", "core.quotePath=false", "ls-tree", "-r", "-z", "--full-tree", "HEAD", "--", nested}
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = filepath.Join(root, filepath.FromSlash(gitlinkPath))
	encoded, err := command.Output()
	if err != nil {
		return false, fmt.Errorf("inventory producer submodule path %q: %w", actual.path, err)
	}
	for _, item := range bytes.Split(encoded, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		metadata, path, found := bytes.Cut(item, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !found || len(fields) != 3 {
			return false, fmt.Errorf("git produced invalid submodule tree record %q", item)
		}
		if string(path) != nested {
			continue
		}
		return string(fields[0]) == actual.mode && string(fields[1]) == actual.objectType && string(fields[2]) == actual.objectID, nil
	}
	return false, nil
}

func headTreeInventory(ctx context.Context, root string, paths []string) (map[string]worktreeInventoryRecord, error) {
	arguments := []string{"-c", "core.quotePath=false", "ls-tree", "-r", "-z", "--full-tree", "HEAD", "--"}
	arguments = append(arguments, paths...)
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = root
	encoded, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("inventory producer inputs with git: %w", err)
	}
	result := make(map[string]worktreeInventoryRecord)
	for _, item := range bytes.Split(encoded, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		metadata, path, found := bytes.Cut(item, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !found || len(fields) != 3 || len(path) == 0 {
			return nil, fmt.Errorf("git produced invalid tree record %q", item)
		}
		record := worktreeInventoryRecord{mode: string(fields[0]), objectType: string(fields[1]), objectID: string(fields[2]), path: string(path)}
		result[record.path] = record
	}
	return result, nil
}

func worktreePathRecord(ctx context.Context, root, relative, path string, entry fs.DirEntry) (worktreeInventoryRecord, error) {
	info, err := entry.Info()
	if err != nil {
		return worktreeInventoryRecord{}, err
	}
	mode := "100644"
	var contents []byte
	switch {
	case info.Mode().IsRegular():
		if info.Mode().Perm()&0o111 != 0 {
			mode = "100755"
		}
		contents, err = os.ReadFile(path)
	case info.Mode()&os.ModeSymlink != 0:
		mode = "120000"
		var target string
		target, err = os.Readlink(path)
		contents = []byte(target)
	default:
		return worktreeInventoryRecord{}, fmt.Errorf("producer worktree path %q has unsupported mode %s", relative, info.Mode())
	}
	if err != nil {
		return worktreeInventoryRecord{}, fmt.Errorf("read producer worktree path %q: %w", relative, err)
	}
	command := exec.CommandContext(ctx, "git", "hash-object", "--stdin")
	command.Dir = root
	command.Stdin = bytes.NewReader(contents)
	encoded, err := command.Output()
	if err != nil {
		return worktreeInventoryRecord{}, fmt.Errorf("hash producer worktree path %q with git: %w", relative, err)
	}
	objectID := strings.TrimSpace(string(encoded))
	if objectID == "" {
		return worktreeInventoryRecord{}, fmt.Errorf("hash producer worktree path %q: empty object id", relative)
	}
	return worktreeInventoryRecord{mode: mode, objectType: "blob", objectID: objectID, path: relative}, nil
}

func currentGitlinkRecord(ctx context.Context, root, relative string) (worktreeInventoryRecord, error) {
	command := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	command.Dir = root
	encoded, err := command.Output()
	if err != nil {
		return worktreeInventoryRecord{}, fmt.Errorf("read producer submodule %q HEAD: %w", relative, err)
	}
	return worktreeInventoryRecord{mode: "160000", objectType: "commit", objectID: strings.TrimSpace(string(encoded)), path: relative}, nil
}

func gitIgnoredWithoutIndex(ctx context.Context, root, relative string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "check-ignore", "--quiet", "--no-index", "--", relative)
	command.Dir = root
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check producer worktree ignore policy for %q: %w", relative, err)
}

func gitHeadTreeDigest(ctx context.Context, root string, paths []string) (string, error) {
	arguments := []string{"-c", "core.quotePath=false", "ls-tree", "-r", "--full-tree", "HEAD", "--"}
	arguments = append(arguments, paths...)
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("fingerprint producer inputs with git: %w", err)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return "", errors.New("producer input inventory is empty")
	}
	digest := sha256.Sum256(output)
	return hex.EncodeToString(digest[:]), nil
}

func verifyOutput(path string, output artifactOutput) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("read output bytes")
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("hash bytes: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close after hashing: %w", closeErr)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if digest != output.SHA256 {
		return fmt.Errorf("hash mismatch: expected %s, found %s", output.SHA256, digest)
	}
	if size != output.Size {
		return fmt.Errorf("size mismatch: expected %d, found %d", output.Size, size)
	}
	info, err := os.Stat(path)
	if err != nil {
		return errors.New("inspect output mode")
	}
	if output.Executable && info.Mode().Perm()&0o111 == 0 {
		return errors.New("executable mode is absent")
	}
	return nil
}

func existingPathWithin(root, relative string) (string, error) {
	if err := validateRelativePath(relative); err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	relation, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil {
		return "", err
	}
	if relation == ".." || strings.HasPrefix(relation, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside %q", relative, root)
	}
	return resolved, nil
}

func validateRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return errors.New("path must be non-empty and relative")
	}
	if strings.ContainsRune(path, '\\') {
		return errors.New("path contains a non-portable backslash")
	}
	for _, character := range path {
		if character <= 0x1f || character == 0x7f {
			return errors.New("path contains an ASCII control character")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("path escapes its root")
	}
	return nil
}

func (command BuildCommand) String() string {
	parts := append([]string{command.Executable}, command.Arguments...)
	return strings.Join(parts, " ")
}

func (command BuildCommand) validate(subject string) error {
	if strings.TrimSpace(command.Executable) == "" {
		return fmt.Errorf("%s executable is empty", subject)
	}
	for _, argument := range command.Arguments {
		if argument == "" {
			return fmt.Errorf("%s contains an empty argument", subject)
		}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func duplicates(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return true
		}
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
