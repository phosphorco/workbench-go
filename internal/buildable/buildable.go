// Package buildable validates and runs repository-owned compiled artifacts.
package buildable

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
)

const configurationPath = ".workbench/buildables.json"

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Refusal is a buildable that cannot lawfully run and its actionable remedy.
type Refusal struct {
	Buildable string
	Candidate string
	Reason    string
	Remedy    string
}

func (refusal *Refusal) Error() string {
	subject := fmt.Sprintf("buildable %q", refusal.Buildable)
	if refusal.Candidate != "" {
		subject += fmt.Sprintf(" candidate %q", refusal.Candidate)
	}
	return fmt.Sprintf("%s refused: %s\nNext action: %s", subject, refusal.Reason, refusal.Remedy)
}

type configuration struct {
	Buildables map[string]Buildable `json:"buildables"`
}

// Buildable is the generated JSON projection of one workbench.pkl declaration.
type Buildable struct {
	InputDetection InputDetection      `json:"inputDetection"`
	BuildCommand   BuildCommand        `json:"buildCommand"`
	Manifest       ManifestContract    `json:"manifest"`
	Candidates     []Candidate         `json:"candidates"`
	Platforms      map[string]Platform `json:"platforms"`
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
	SchemaVersion        int      `json:"schemaVersion"`
	Kind                 string   `json:"kind"`
	ContractID           string   `json:"contractId"`
	SourceRepository     string   `json:"sourceRepository"`
	SourceChannel        string   `json:"sourceChannel"`
	RequiredCapabilities []string `json:"requiredCapabilities"`
}

type Candidate struct {
	Root          string `json:"root"`
	InvalidRemedy string `json:"invalidRemedy"`
}

type Platform struct {
	OS         []string `json:"os"`
	Arch       []string `json:"arch"`
	Path       string   `json:"path"`
	Executable bool     `json:"executable"`
}

type artifactManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	ContractID    string `json:"contractId"`
	Source        struct {
		Repository     string `json:"repository"`
		Revision       string `json:"revision"`
		Channel        string `json:"channel"`
		Version        string `json:"version"`
		NestedRevision string `json:"nestedRevision"`
	} `json:"source"`
	ProducerInputs struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"producerInputs"`
	Capabilities []string         `json:"capabilities"`
	Outputs      []artifactOutput `json:"outputs"`
}

type artifactOutput struct {
	Platform   string `json:"platform"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}

// Resolve returns the first present, valid candidate in declared order.
func Resolve(ctx context.Context, repositoryRoot, name, hostOS, hostArch string) (string, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	config, err := loadConfiguration(root)
	if err != nil {
		return "", err
	}
	buildable, exists := config.Buildables[name]
	if !exists {
		return "", fmt.Errorf("buildable %q is not declared", name)
	}
	return ResolveDeclared(ctx, root, name, buildable, hostOS, hostArch)
}

// ResolveDeclared is the configuration-independent seam used by setup and
// contract-parity proofs before the generated projection is installed.
func ResolveDeclared(ctx context.Context, repositoryRoot, name string, buildable Buildable, hostOS, hostArch string) (string, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	if err := buildable.validate(); err != nil {
		return "", fmt.Errorf("buildable %q configuration: %w", name, err)
	}
	platformName, platform, err := buildable.platform(hostOS, hostArch)
	if err != nil {
		return "", &Refusal{Buildable: name, Reason: err.Error(), Remedy: "Run this buildable on one of its declared host platforms."}
	}
	for _, candidate := range buildable.Candidates {
		present, err := candidatePresent(root, candidate, buildable.Platforms)
		if err != nil {
			return "", fmt.Errorf("observe buildable %q candidate %q: %w", name, candidate.Root, err)
		}
		if !present {
			continue
		}
		path, err := verifyCandidate(ctx, root, buildable, candidate, platformName, platform)
		if err != nil {
			return "", &Refusal{Buildable: name, Candidate: candidate.Root, Reason: err.Error(), Remedy: candidate.InvalidRemedy}
		}
		return path, nil
	}
	return "", &Refusal{
		Buildable: name,
		Reason:    "no declared artifact candidate is present",
		Remedy:    fmt.Sprintf("Run '%s' to create the preferred local candidate.", buildable.BuildCommand.String()),
	}
}

// Run validates and replaces Workbench with the selected artifact process.
func Run(ctx context.Context, repositoryRoot, name string, arguments []string) error {
	path, err := Resolve(ctx, repositoryRoot, name, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	argv := append([]string{path}, arguments...)
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec buildable %q at %q: %w", name, path, err)
	}
	return nil
}

func loadConfiguration(root string) (configuration, error) {
	path := filepath.Join(root, filepath.FromSlash(configurationPath))
	file, err := os.Open(path)
	if err != nil {
		return configuration{}, fmt.Errorf("read generated buildable configuration %q: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReader(file))
	decoder.DisallowUnknownFields()
	var value configuration
	if err := decoder.Decode(&value); err != nil {
		return configuration{}, fmt.Errorf("decode generated buildable configuration %q: %w", path, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return configuration{}, fmt.Errorf("decode generated buildable configuration %q: %w", path, err)
	}
	if len(value.Buildables) == 0 {
		return configuration{}, errors.New("generated buildable configuration declares no buildables")
	}
	return value, nil
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

func (buildable Buildable) validate() error {
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
	if strings.TrimSpace(buildable.BuildCommand.Executable) == "" {
		return errors.New("build command executable is empty")
	}
	if buildable.Manifest.SchemaVersion <= 0 || buildable.Manifest.Kind == "" || buildable.Manifest.ContractID == "" || buildable.Manifest.SourceRepository == "" || buildable.Manifest.SourceChannel == "" {
		return errors.New("manifest contract identity is incomplete")
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
		if _, duplicate := seenCandidates[candidate.Root]; duplicate {
			return fmt.Errorf("candidate root %q is duplicated", candidate.Root)
		}
		seenCandidates[candidate.Root] = struct{}{}
	}
	if len(buildable.Platforms) == 0 {
		return errors.New("platform outputs are empty")
	}
	for name, platform := range buildable.Platforms {
		if name == "" || len(platform.OS) == 0 || len(platform.Arch) == 0 {
			return fmt.Errorf("platform %q has incomplete host aliases", name)
		}
		if err := validateRelativePath(platform.Path); err != nil {
			return fmt.Errorf("platform %q output path: %w", name, err)
		}
		if !platform.Executable {
			return fmt.Errorf("platform %q output is not executable", name)
		}
	}
	return nil
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

func candidatePresent(root string, candidate Candidate, platforms map[string]Platform) (bool, error) {
	paths := []string{"manifest.json"}
	for _, platform := range platforms {
		paths = append(paths, platform.Path)
	}
	for _, path := range paths {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate.Root), filepath.FromSlash(path)))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func verifyCandidate(ctx context.Context, root string, buildable Buildable, candidate Candidate, selectedName string, selected Platform) (string, error) {
	candidateRoot, err := existingPathWithin(root, candidate.Root)
	if err != nil {
		return "", fmt.Errorf("candidate root is not confined to the repository: %w", err)
	}
	manifestPath, err := existingPathWithin(candidateRoot, "manifest.json")
	if err != nil {
		return "", fmt.Errorf("manifest is missing or escapes the candidate root: %w", err)
	}
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return "", err
	}
	if err := validateManifest(buildable, manifest); err != nil {
		return "", err
	}
	currentDigest, err := gitHeadTreeDigest(ctx, root, buildable.InputDetection.Paths)
	if err != nil {
		return "", err
	}
	if manifest.ProducerInputs.Digest != currentDigest {
		return "", fmt.Errorf("stale artifact: producer input digest is %s, current inputs require %s", manifest.ProducerInputs.Digest, currentDigest)
	}
	outputs := make(map[string]artifactOutput, len(manifest.Outputs))
	for _, output := range manifest.Outputs {
		outputs[output.Platform] = output
		platform := buildable.Platforms[output.Platform]
		artifactPath, err := existingPathWithin(candidateRoot, platform.Path)
		if err != nil {
			return "", fmt.Errorf("output %s is missing or escapes the candidate root: %w", output.Platform, err)
		}
		if err := verifyOutput(artifactPath, output); err != nil {
			return "", fmt.Errorf("output %s: %w", output.Platform, err)
		}
	}
	output := outputs[selectedName]
	return filepath.Join(candidateRoot, filepath.FromSlash(output.Path)), nil
}

func readManifest(path string) (artifactManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return artifactManifest{}, fmt.Errorf("read manifest %q: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReader(file))
	var manifest artifactManifest
	if err := decoder.Decode(&manifest); err != nil {
		return artifactManifest{}, fmt.Errorf("decode manifest %q: %w", path, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return artifactManifest{}, fmt.Errorf("decode manifest %q: %w", path, err)
	}
	return manifest, nil
}

func validateManifest(buildable Buildable, manifest artifactManifest) error {
	contract := buildable.Manifest
	if manifest.SchemaVersion != contract.SchemaVersion || manifest.Kind != contract.Kind || manifest.ContractID != contract.ContractID {
		return fmt.Errorf("manifest contract identity is %d/%s/%s, want %d/%s/%s", manifest.SchemaVersion, manifest.Kind, manifest.ContractID, contract.SchemaVersion, contract.Kind, contract.ContractID)
	}
	if manifest.Source.Repository != contract.SourceRepository || manifest.Source.Channel != contract.SourceChannel || manifest.Source.Revision == "" || manifest.Source.Version == "" || manifest.Source.NestedRevision == "" {
		return errors.New("manifest source identity is incomplete or non-canonical")
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
	if len(manifest.Outputs) != len(buildable.Platforms) {
		return fmt.Errorf("manifest has %d outputs, want %d", len(manifest.Outputs), len(buildable.Platforms))
	}
	seen := make(map[string]struct{}, len(manifest.Outputs))
	for _, output := range manifest.Outputs {
		platform, exists := buildable.Platforms[output.Platform]
		if !exists {
			return fmt.Errorf("manifest has unexpected platform %q", output.Platform)
		}
		if _, duplicate := seen[output.Platform]; duplicate {
			return fmt.Errorf("manifest repeats platform %q", output.Platform)
		}
		seen[output.Platform] = struct{}{}
		if filepath.ToSlash(filepath.Clean(output.Path)) != filepath.ToSlash(filepath.Clean(platform.Path)) {
			return fmt.Errorf("manifest output path for %s is %q, want %q", output.Platform, output.Path, platform.Path)
		}
		if !sha256Pattern.MatchString(output.SHA256) || output.Size <= 0 || output.Executable != platform.Executable {
			return fmt.Errorf("manifest output facts for %s are invalid", output.Platform)
		}
	}
	return nil
}

func gitHeadTreeDigest(ctx context.Context, root string, paths []string) (string, error) {
	arguments := []string{"ls-tree", "-r", "--full-tree", "HEAD", "--"}
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
		return err
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
		return fmt.Errorf("inspect mode: %w", err)
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
