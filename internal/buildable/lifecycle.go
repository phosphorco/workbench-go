package buildable

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// SourceDescriptorFilename is the strict producer-authored source and
// capability descriptor carried identically by every platform candidate.
const SourceDescriptorFilename = ".workbench-buildable-source.json"

type sourceDescriptor struct {
	Source       map[string]string `json:"source"`
	Capabilities []string          `json:"capabilities"`
}

// Build invokes the repository-owned build command for one declared platform,
// then validates the platform output and shared source descriptor.
func Build(ctx context.Context, workbenchRoot, name, platformName string) error {
	ownerRoot, declaration, err := loadProjectedDeclaration(workbenchRoot, name)
	if err != nil {
		return err
	}
	return BuildDeclared(ctx, ownerRoot, name, declaration, platformName)
}

// BuildDeclared is the cold lifecycle seam for a declaration evaluated from
// the caller's current workbench.pkl. Hot invocation deliberately uses Build's
// setup-owned projection instead.
func BuildDeclared(ctx context.Context, ownerRoot, name string, declaration Buildable, platformName string) error {
	if err := declaration.ValidateForName(name); err != nil {
		return err
	}
	platform, exists := declaration.Platforms[platformName]
	if !exists {
		return &Refusal{
			Code: RefusalUnsupportedPlatform, Buildable: name,
			Reason: fmt.Sprintf("platform %q is not declared", platformName),
			Remedy: "Choose one of the platform names declared by the owning workbench.pkl.",
		}
	}
	command := exec.CommandContext(ctx, declaration.BuildCommand.Executable, declaration.BuildCommand.Arguments...)
	command.Dir = ownerRoot
	command.Env = append(os.Environ(),
		"WORKBENCH_BUILDABLE_NAME="+name,
		"WORKBENCH_BUILDABLE_PLATFORM="+platformName,
		"WORKBENCH_BUILDABLE_CANDIDATE_ROOT="+declaration.Candidates[0].Root,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build %q with %q: %w: %s", name, declaration.BuildCommand.String(), err, strings.TrimSpace(string(output)))
	}
	candidateRoot, err := existingPathWithin(ownerRoot, declaration.Candidates[0].Root)
	if err != nil {
		return &Refusal{
			Code: RefusalCandidateInvalid, Buildable: name, Candidate: declaration.Candidates[0].Root,
			Reason: fmt.Sprintf("built candidate root is missing or escapes its owner: %v", err),
			Remedy: declaration.Candidates[0].InvalidRemedy,
		}
	}
	if _, err := readSourceDescriptor(candidateRoot, declaration); err != nil {
		return &Refusal{
			Code: RefusalCandidateInvalid, Buildable: name, Candidate: declaration.Candidates[0].Root,
			Reason: err.Error(), Remedy: declaration.Candidates[0].InvalidRemedy,
		}
	}
	outputPath, err := existingPathWithin(candidateRoot, platform.Path)
	if err != nil {
		return &Refusal{
			Code: RefusalCandidateInvalid, Buildable: name, Candidate: declaration.Candidates[0].Root,
			Reason: fmt.Sprintf("platform %q output is missing or escapes the candidate: %v", platformName, err),
			Remedy: declaration.Candidates[0].InvalidRemedy,
		}
	}
	info, err := os.Stat(outputPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || platform.Executable && info.Mode().Perm()&0o111 == 0 {
		return &Refusal{
			Code: RefusalCandidateInvalid, Buildable: name, Candidate: declaration.Candidates[0].Root,
			Reason: fmt.Sprintf("platform %q output is not a non-empty executable regular file", platformName),
			Remedy: declaration.Candidates[0].InvalidRemedy,
		}
	}
	return nil
}

// Seal records producer identity and every declared output fact in manifest.json.
func Seal(ctx context.Context, workbenchRoot, name, candidateRoot string) error {
	ownerRoot, declaration, candidate, absoluteCandidate, err := declaredCandidate(workbenchRoot, name, candidateRoot)
	if err != nil {
		return err
	}
	return sealDeclared(ctx, ownerRoot, name, declaration, candidate, absoluteCandidate)
}

// SealDeclared seals one candidate from a directly evaluated declaration.
func SealDeclared(ctx context.Context, ownerRoot, name string, declaration Buildable, candidateRoot string) error {
	candidate, absoluteCandidate, err := declaredCandidateIn(ownerRoot, name, declaration, candidateRoot)
	if err != nil {
		return err
	}
	return sealDeclared(ctx, ownerRoot, name, declaration, candidate, absoluteCandidate)
}

func sealDeclared(ctx context.Context, ownerRoot, name string, declaration Buildable, candidate Candidate, absoluteCandidate string) error {
	descriptor, err := readSourceDescriptor(absoluteCandidate, declaration)
	if err != nil {
		return &Refusal{Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Root, Reason: err.Error(), Remedy: candidate.InvalidRemedy}
	}
	producerDigest, err := candidateInputDigest(ctx, ownerRoot, candidate, declaration.InputDetection.Paths)
	if err != nil {
		return err
	}
	manifest := artifactManifest{
		SchemaVersion: declaration.Manifest.SchemaVersion,
		Kind:          declaration.Manifest.Kind,
		ContractID:    declaration.Manifest.ContractID,
		Source:        descriptor.Source,
		Capabilities:  append([]string(nil), descriptor.Capabilities...),
	}
	manifest.ProducerInputs.Algorithm = "sha256"
	manifest.ProducerInputs.Digest = producerDigest
	platformNames := make([]string, 0, len(declaration.Platforms))
	for platformName := range declaration.Platforms {
		platformNames = append(platformNames, platformName)
	}
	sort.Strings(platformNames)
	for _, platformName := range platformNames {
		platform := declaration.Platforms[platformName]
		path, err := existingPathWithin(absoluteCandidate, platform.Path)
		if err != nil {
			return &Refusal{
				Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Root,
				Reason: fmt.Sprintf("platform %q output is missing or escapes the candidate: %v", platformName, err),
				Remedy: candidate.InvalidRemedy,
			}
		}
		output, err := inspectOutput(path, platformName, platform)
		if err != nil {
			return &Refusal{Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Root, Reason: err.Error(), Remedy: candidate.InvalidRemedy}
		}
		manifest.Outputs = append(manifest.Outputs, output)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode buildable %q manifest: %w", name, err)
	}
	if err := writeAtomic(filepath.Join(absoluteCandidate, "manifest.json"), append(encoded, '\n')); err != nil {
		return fmt.Errorf("seal buildable %q manifest: %w", name, err)
	}
	return nil
}

// Verify validates an exact declared candidate and may run its explicit
// repository-owned composition proof.
func Verify(ctx context.Context, workbenchRoot, name, candidateRoot string, runDeclaredVerification bool) error {
	ownerRoot, declaration, candidate, _, err := declaredCandidate(workbenchRoot, name, candidateRoot)
	if err != nil {
		return err
	}
	return verifyDeclared(ctx, ownerRoot, name, declaration, candidate, runDeclaredVerification)
}

// VerifyDeclared verifies one candidate from a directly evaluated declaration.
func VerifyDeclared(ctx context.Context, ownerRoot, name string, declaration Buildable, candidateRoot string, runDeclaredVerification bool) error {
	candidate, _, err := declaredCandidateIn(ownerRoot, name, declaration, candidateRoot)
	if err != nil {
		return err
	}
	return verifyDeclared(ctx, ownerRoot, name, declaration, candidate, runDeclaredVerification)
}

func verifyDeclared(ctx context.Context, ownerRoot, name string, declaration Buildable, candidate Candidate, runDeclaredVerification bool) error {
	if err := verifyExactCandidate(ctx, ownerRoot, name, declaration, candidate); err != nil {
		return err
	}
	if !runDeclaredVerification {
		return nil
	}
	if declaration.VerificationCommand == nil {
		return &Refusal{
			Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Root,
			Reason: "declared verification was requested but verificationCommand is absent",
			Remedy: "Declare a repository-owned verificationCommand or omit --run-declared-verification.",
		}
	}
	command := exec.CommandContext(ctx, declaration.VerificationCommand.Executable, declaration.VerificationCommand.Arguments...)
	command.Dir = ownerRoot
	command.Env = append(os.Environ(),
		"WORKBENCH_BUILDABLE_NAME="+name,
		"WORKBENCH_BUILDABLE_CANDIDATE_ROOT="+candidate.Root,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("verify %q with %q: %w: %s", name, declaration.VerificationCommand.String(), err, strings.TrimSpace(string(output)))
	}
	return nil
}

// CheckFresh proves the sealed candidate was built from builtFrom and that
// declared inputs are unchanged at against.
func CheckFresh(ctx context.Context, workbenchRoot, name, candidateRoot, builtFrom, against string) error {
	ownerRoot, declaration, candidate, absoluteCandidate, err := declaredCandidate(workbenchRoot, name, candidateRoot)
	if err != nil {
		return err
	}
	return checkFreshDeclared(ctx, ownerRoot, name, declaration, candidate, absoluteCandidate, builtFrom, against)
}

// CheckFreshDeclared checks one directly evaluated declaration.
func CheckFreshDeclared(ctx context.Context, ownerRoot, name string, declaration Buildable, candidateRoot, builtFrom, against string) error {
	candidate, absoluteCandidate, err := declaredCandidateIn(ownerRoot, name, declaration, candidateRoot)
	if err != nil {
		return err
	}
	return checkFreshDeclared(ctx, ownerRoot, name, declaration, candidate, absoluteCandidate, builtFrom, against)
}

func checkFreshDeclared(ctx context.Context, ownerRoot, name string, declaration Buildable, candidate Candidate, absoluteCandidate, builtFrom, against string) error {
	if err := validateGitRevision(builtFrom); err != nil {
		return fmt.Errorf("built-from revision: %w", err)
	}
	if err := validateGitRevision(against); err != nil {
		return fmt.Errorf("against revision: %w", err)
	}
	manifest, err := readManifest(filepath.Join(absoluteCandidate, "manifest.json"))
	if err != nil {
		return &Refusal{Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Root, Reason: err.Error(), Remedy: candidate.InvalidRemedy}
	}
	if err := validateManifest(declaration, manifest); err != nil {
		return &Refusal{Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Root, Reason: err.Error(), Remedy: candidate.InvalidRemedy}
	}
	builtDigest, err := gitTreeDigestAtRevision(ctx, ownerRoot, builtFrom, declaration.InputDetection.Paths)
	if err != nil {
		return err
	}
	if manifest.ProducerInputs.Digest != builtDigest {
		return &Refusal{
			Code: RefusalStaleProducerInputs, Buildable: name, Candidate: candidate.Root,
			Reason: fmt.Sprintf("candidate producer digest is %s, built-from %s requires %s", manifest.ProducerInputs.Digest, builtFrom, builtDigest),
			Remedy: candidate.InvalidRemedy,
		}
	}
	againstDigest, err := gitTreeDigestAtRevision(ctx, ownerRoot, against, declaration.InputDetection.Paths)
	if err != nil {
		return err
	}
	if builtDigest != againstDigest {
		return &Refusal{
			Code: RefusalStaleProducerInputs, Buildable: name, Candidate: candidate.Root,
			Reason: fmt.Sprintf("declared producer inputs changed: %s requires %s, %s requires %s", builtFrom, builtDigest, against, againstDigest),
			Remedy: candidate.InvalidRemedy,
		}
	}
	return nil
}

// Promote atomically replaces the declared committed candidate with a
// byte-identical copy of the verified local candidate.
func Promote(ctx context.Context, workbenchRoot, name, candidateRoot, committedRoot string) error {
	ownerRoot, declaration, candidate, absoluteCandidate, err := declaredCandidate(workbenchRoot, name, candidateRoot)
	if err != nil {
		return err
	}
	return promoteDeclared(ctx, ownerRoot, name, declaration, candidate, absoluteCandidate, committedRoot)
}

// PromoteDeclared promotes one directly evaluated declaration.
func PromoteDeclared(ctx context.Context, ownerRoot, name string, declaration Buildable, candidateRoot, committedRoot string) error {
	candidate, absoluteCandidate, err := declaredCandidateIn(ownerRoot, name, declaration, candidateRoot)
	if err != nil {
		return err
	}
	return promoteDeclared(ctx, ownerRoot, name, declaration, candidate, absoluteCandidate, committedRoot)
}

func promoteDeclared(ctx context.Context, ownerRoot, name string, declaration Buildable, candidate Candidate, absoluteCandidate, committedRoot string) error {
	if candidate.Root != declaration.Candidates[0].Root {
		return fmt.Errorf("promotion source %q is not the declared local candidate %q", candidate.Root, declaration.Candidates[0].Root)
	}
	if filepath.ToSlash(filepath.Clean(committedRoot)) != declaration.Candidates[1].Root {
		return fmt.Errorf("promotion target %q is not the declared committed candidate %q", committedRoot, declaration.Candidates[1].Root)
	}
	if err := verifyExactCandidate(ctx, ownerRoot, name, declaration, candidate); err != nil {
		return err
	}
	sourceDigest, err := candidateTreeDigest(absoluteCandidate)
	if err != nil {
		return fmt.Errorf("digest promotion source: %w", err)
	}
	target := filepath.Join(ownerRoot, filepath.FromSlash(declaration.Candidates[1].Root))
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create promotion parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".workbench-promote-*")
	if err != nil {
		return fmt.Errorf("create promotion staging: %w", err)
	}
	defer os.RemoveAll(staging)
	stagedCandidate := filepath.Join(staging, "candidate")
	if err := copyCandidateTree(absoluteCandidate, stagedCandidate); err != nil {
		return fmt.Errorf("copy promotion candidate: %w", err)
	}
	stagedDigest, err := candidateTreeDigest(stagedCandidate)
	if err != nil {
		return fmt.Errorf("digest staged promotion candidate: %w", err)
	}
	if stagedDigest != sourceDigest {
		return fmt.Errorf("staged candidate digest %s differs from source %s", stagedDigest, sourceDigest)
	}
	backup, hadTarget, err := moveExistingAside(target)
	if err != nil {
		return err
	}
	restore := func(cause error) error {
		_ = os.RemoveAll(target)
		if hadTarget {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				return errors.Join(cause, fmt.Errorf("restore previous committed candidate: %w", restoreErr))
			}
		}
		return cause
	}
	if err := os.Rename(stagedCandidate, target); err != nil {
		return restore(fmt.Errorf("install promoted candidate: %w", err))
	}
	targetDigest, err := candidateTreeDigest(target)
	if err != nil {
		return restore(fmt.Errorf("digest promoted candidate: %w", err))
	}
	if targetDigest != sourceDigest {
		return restore(fmt.Errorf("promoted candidate digest %s differs from source %s", targetDigest, sourceDigest))
	}
	if hadTarget {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove replaced committed candidate backup: %w", err)
		}
	}
	return nil
}

func declaredCandidate(workbenchRoot, name, candidateRoot string) (string, Buildable, Candidate, string, error) {
	ownerRoot, declaration, err := loadProjectedDeclaration(workbenchRoot, name)
	if err != nil {
		return "", Buildable{}, Candidate{}, "", err
	}
	candidate, absolute, err := declaredCandidateIn(ownerRoot, name, declaration, candidateRoot)
	return ownerRoot, declaration, candidate, absolute, err
}

func declaredCandidateIn(ownerRoot, name string, declaration Buildable, candidateRoot string) (Candidate, string, error) {
	if err := declaration.ValidateForName(name); err != nil {
		return Candidate{}, "", err
	}
	normalized := filepath.ToSlash(filepath.Clean(candidateRoot))
	for _, candidate := range declaration.Candidates {
		if normalized != candidate.Root {
			continue
		}
		absolute, err := existingPathWithin(ownerRoot, candidate.Root)
		if err != nil {
			return Candidate{}, "", &Refusal{
				Code: RefusalCandidateInvalid, Buildable: name, Candidate: candidate.Root,
				Reason: fmt.Sprintf("candidate is missing or escapes its owner: %v", err), Remedy: candidate.InvalidRemedy,
			}
		}
		return candidate, absolute, nil
	}
	return Candidate{}, "", fmt.Errorf("candidate root %q is not declared for buildable %q", candidateRoot, name)
}

func readSourceDescriptor(candidateRoot string, declaration Buildable) (sourceDescriptor, error) {
	path, err := existingPathWithin(candidateRoot, SourceDescriptorFilename)
	if err != nil {
		return sourceDescriptor{}, fmt.Errorf("source descriptor %q is missing or escapes the candidate: %w", SourceDescriptorFilename, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return sourceDescriptor{}, fmt.Errorf("read source descriptor: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReader(file))
	decoder.DisallowUnknownFields()
	var descriptor sourceDescriptor
	if err := decoder.Decode(&descriptor); err != nil {
		return sourceDescriptor{}, fmt.Errorf("decode source descriptor: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return sourceDescriptor{}, fmt.Errorf("decode source descriptor: %w", err)
	}
	if descriptor.Source == nil {
		return sourceDescriptor{}, errors.New("source descriptor omits source")
	}
	for field, expected := range declaration.Manifest.ExpectedSource {
		if descriptor.Source[field] != expected {
			return sourceDescriptor{}, fmt.Errorf("source descriptor field %q is %q, want %q", field, descriptor.Source[field], expected)
		}
	}
	for _, field := range declaration.Manifest.RequiredSourceFields {
		if descriptor.Source[field] == "" {
			return sourceDescriptor{}, fmt.Errorf("source descriptor field %q is required", field)
		}
	}
	if duplicates(descriptor.Capabilities) {
		return sourceDescriptor{}, errors.New("source descriptor capabilities are empty or duplicated")
	}
	for _, required := range declaration.Manifest.RequiredCapabilities {
		if !contains(descriptor.Capabilities, required) {
			return sourceDescriptor{}, fmt.Errorf("source descriptor lacks required capability %q", required)
		}
	}
	return descriptor, nil
}

func inspectOutput(path, platformName string, platform Platform) (artifactOutput, error) {
	file, err := os.Open(path)
	if err != nil {
		return artifactOutput{}, fmt.Errorf("read platform %q output: %w", platformName, err)
	}
	digest := sha256.New()
	size, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil {
		return artifactOutput{}, fmt.Errorf("hash platform %q output: %w", platformName, copyErr)
	}
	if closeErr != nil {
		return artifactOutput{}, fmt.Errorf("close platform %q output: %w", platformName, closeErr)
	}
	info, err := os.Stat(path)
	if err != nil {
		return artifactOutput{}, fmt.Errorf("inspect platform %q output: %w", platformName, err)
	}
	if !info.Mode().IsRegular() || size <= 0 || platform.Executable && info.Mode().Perm()&0o111 == 0 {
		return artifactOutput{}, fmt.Errorf("platform %q output is not a non-empty executable regular file", platformName)
	}
	return artifactOutput{
		Platform: platformName, Path: platform.Path,
		SHA256: hex.EncodeToString(digest.Sum(nil)), Size: size, Executable: platform.Executable,
	}, nil
}

func verifyExactCandidate(ctx context.Context, ownerRoot, name string, declaration Buildable, candidate Candidate) error {
	platformNames := make([]string, 0, len(declaration.Platforms))
	for platformName := range declaration.Platforms {
		platformNames = append(platformNames, platformName)
	}
	sort.Strings(platformNames)
	selectedName := platformNames[0]
	_, code, err := verifyCandidate(ctx, ownerRoot, declaration, candidate, selectedName, declaration.Platforms[selectedName])
	if err != nil {
		return &Refusal{Code: code, Buildable: name, Candidate: candidate.Root, Reason: err.Error(), Remedy: candidate.InvalidRemedy}
	}
	return nil
}

func validateGitRevision(revision string) error {
	if strings.TrimSpace(revision) == "" || strings.HasPrefix(revision, "-") || strings.ContainsRune(revision, '\x00') {
		return fmt.Errorf("revision %q is invalid", revision)
	}
	return nil
}

func gitTreeDigestAtRevision(ctx context.Context, root, revision string, paths []string) (string, error) {
	arguments := []string{"-c", "core.quotePath=false", "ls-tree", "-r", "--full-tree", revision, "--"}
	arguments = append(arguments, paths...)
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("fingerprint producer inputs at %q with git: %w", revision, err)
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return "", fmt.Errorf("producer input inventory at %q is empty", revision)
	}
	digest := sha256.Sum256(output)
	return hex.EncodeToString(digest[:]), nil
}

func writeAtomic(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".workbench-buildable-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func candidateTreeDigest(root string) (string, error) {
	digest := sha256.New()
	if err := walkCandidate(root, func(relative string, entry fs.DirEntry, path string) error {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return writeTreeFact(digest, relative, info.Mode(), nil)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("candidate path %q is not a regular file or directory", relative)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		if err := writeTreeFact(digest, relative, info.Mode(), file); err != nil {
			file.Close()
			return err
		}
		return file.Close()
	}); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeTreeFact(digest hash.Hash, relative string, mode fs.FileMode, contents io.Reader) error {
	if _, err := io.WriteString(digest, filepath.ToSlash(relative)+"\x00"+mode.String()+"\x00"); err != nil {
		return err
	}
	if contents != nil {
		if _, err := io.Copy(digest, contents); err != nil {
			return err
		}
	}
	_, err := digest.Write([]byte{0})
	return err
}

func walkCandidate(root string, visit func(relative string, entry fs.DirEntry, path string) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		return visit(relative, entry, path)
	})
}

func copyCandidateTree(source, target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	return walkCandidate(source, func(relative string, entry fs.DirEntry, path string) error {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("candidate path %q is not a regular file or directory", relative)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputErr := input.Close()
		outputErr := output.Close()
		return errors.Join(copyErr, inputErr, outputErr)
	})
}

func moveExistingAside(target string) (string, bool, error) {
	_, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("observe committed candidate: %w", err)
	}
	placeholder, err := os.CreateTemp(filepath.Dir(target), ".workbench-replaced-*")
	if err != nil {
		return "", false, fmt.Errorf("reserve committed candidate backup: %w", err)
	}
	backup := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		return "", false, err
	}
	if err := os.Remove(backup); err != nil {
		return "", false, err
	}
	if err := os.Rename(target, backup); err != nil {
		return "", false, fmt.Errorf("move existing committed candidate aside: %w", err)
	}
	return backup, true, nil
}
