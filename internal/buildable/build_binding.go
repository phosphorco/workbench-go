package buildable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const bindingDirectory = ".workbench-build-inputs"

type platformBuildBinding struct {
	Platform            string           `json:"platform"`
	DeclarationIdentity string           `json:"declarationIdentity"`
	ProducerDigest      string           `json:"producerDigest"`
	SourceDigest        string           `json:"sourceDigest,omitempty"`
	Outputs             []artifactOutput `json:"outputs"`
}

func bindingFilename(platform string) string {
	digest := sha256.Sum256([]byte(platform))
	return hex.EncodeToString(digest[:]) + ".json"
}

func bindingDir(candidateRoot string, create bool) (string, error) {
	dir := filepath.Join(candidateRoot, bindingDirectory)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.Mkdir(dir, 0o755); err != nil {
			return "", err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("build input binding directory is not a real directory")
	}
	return dir, nil
}

func clearPlatformBinding(ownerRoot, candidateRoot, platform string) error {
	root, err := existingPathWithin(ownerRoot, candidateRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dir, err := bindingDir(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, bindingFilename(platform)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func writePlatformBinding(root string, binding platformBuildBinding) error {
	dir, err := bindingDir(root, true)
	if err != nil {
		return err
	}
	contents, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, bindingFilename(binding.Platform)), append(contents, '\n'))
}

func readPlatformBinding(root, platform string) (platformBuildBinding, error) {
	var binding platformBuildBinding
	dir, err := bindingDir(root, false)
	if err != nil {
		return binding, err
	}
	file := filepath.Join(dir, bindingFilename(platform))
	info, err := os.Lstat(file)
	if err != nil {
		return binding, err
	}
	if !info.Mode().IsRegular() {
		return binding, errors.New("build input binding is not a regular file")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return binding, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return binding, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return binding, err
	}
	if binding.Platform != platform {
		return binding, fmt.Errorf("build input binding names platform %q, want %q", binding.Platform, platform)
	}
	return binding, nil
}

func validatePlatformBindings(root, name string, declaration Buildable, digest string, outputs []artifactOutput) error {
	var sourceDigest string
	if declaration.LocalExecution != nil {
		var err error
		sourceDigest, err = sourceDescriptorDigest(root)
		if err != nil {
			return err
		}
	}
	byKey := make(map[string]artifactOutput, len(outputs))
	for _, output := range outputs {
		byKey[outputKey(output.Platform, output.Path)] = output
	}
	for platform, plan := range declaration.Platforms {
		binding, err := readPlatformBinding(root, platform)
		if err != nil {
			return fmt.Errorf("platform %q build binding unavailable: %w", platform, err)
		}
		if binding.DeclarationIdentity != mustDeclarationIdentity(name, declaration) || binding.ProducerDigest != digest {
			return fmt.Errorf("platform %q was built from different producer inputs", platform)
		}
		if declaration.LocalExecution != nil && binding.SourceDigest != sourceDigest {
			return fmt.Errorf("platform %q source descriptor differs from build", platform)
		}
		if declaration.LocalExecution == nil && binding.SourceDigest != "" {
			return fmt.Errorf("platform %q has an unexpected source descriptor binding", platform)
		}
		declared, err := plan.outputs()
		if err != nil {
			return err
		}
		if len(binding.Outputs) != len(declared) {
			return fmt.Errorf("platform %q binding output count differs", platform)
		}
		for _, output := range binding.Outputs {
			if manifestOutput, ok := byKey[outputKey(platform, output.Path)]; !ok || manifestOutput != output {
				return fmt.Errorf("platform %q bound output differs from manifest", platform)
			}
			delete(byKey, outputKey(platform, output.Path))
		}
	}
	if len(byKey) != 0 {
		return errors.New("build bindings omit manifest outputs")
	}
	return nil
}

func sourceDescriptorDigest(root string) (string, error) {
	path := filepath.Join(root, SourceDescriptorFilename)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect source descriptor: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("source descriptor is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read source descriptor: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func verifyHostLocal(ctx context.Context, root, name string, declaration Buildable, candidate Candidate, platformName string) (candidateVerification, RefusalCode, error) {
	invalid := func(err error) (candidateVerification, RefusalCode, error) {
		return candidateVerification{}, RefusalCandidateInvalid, err
	}
	host, platform, err := declaration.platform(runtime.GOOS, runtime.GOARCH)
	if err != nil || host != platformName {
		return invalid(errors.New("host-local candidate is only valid for the current host platform"))
	}
	candidateRoot, err := existingPathWithin(root, candidate.Root)
	if err != nil {
		return invalid(errors.New("candidate root is not confined to the repository"))
	}
	descriptor, err := readSourceDescriptor(candidateRoot, declaration)
	if err != nil {
		return invalid(err)
	}
	descriptorDigest, err := sourceDescriptorDigest(candidateRoot)
	if err != nil {
		return invalid(err)
	}
	binding, err := readPlatformBinding(candidateRoot, platformName)
	if err != nil {
		return invalid(fmt.Errorf("platform %q build binding unavailable: %w", platformName, err))
	}
	currentDigest, err := candidateInputDigest(ctx, root, candidate, declaration.InputDetection.Paths)
	if err != nil {
		return invalid(err)
	}
	if binding.DeclarationIdentity != mustDeclarationIdentity(name, declaration) || binding.ProducerDigest != currentDigest {
		return candidateVerification{}, RefusalStaleProducerInputs, errors.New("host-local build binding has different producer inputs or declaration")
	}
	if binding.SourceDigest == "" || binding.SourceDigest != descriptorDigest {
		return invalid(errors.New("host-local source descriptor differs from build"))
	}
	declared, err := platform.outputs()
	if err != nil {
		return invalid(err)
	}
	if len(declared) != len(binding.Outputs) {
		return invalid(errors.New("host-local binding output count differs"))
	}
	byPath := make(map[string]artifactOutput, len(binding.Outputs))
	for _, output := range binding.Outputs {
		key := outputKey(output.Platform, output.Path)
		if _, exists := byPath[key]; exists {
			return invalid(errors.New("host-local binding repeats output"))
		}
		byPath[key] = output
	}
	outputs := make([]artifactOutput, 0, len(declared))
	for _, output := range declared {
		key := outputKey(platformName, output.Path)
		recorded, exists := byPath[key]
		if !exists {
			return invalid(fmt.Errorf("host-local binding omits output %q", output.Path))
		}
		path, err := existingPathWithin(candidateRoot, output.Path)
		if err != nil {
			return invalid(fmt.Errorf("host-local output %q is missing or escapes candidate: %w", output.Path, err))
		}
		actual, err := inspectOutput(path, platformName, output)
		if err != nil {
			return invalid(err)
		}
		if actual != recorded {
			return invalid(fmt.Errorf("host-local output %q differs from build", output.Path))
		}
		outputs = append(outputs, actual)
	}
	return candidateVerification{Root: candidateRoot, Manifest: artifactManifest{Source: descriptor.Source, Capabilities: descriptor.Capabilities}, Outputs: map[string][]artifactOutput{platformName: outputs}, LocalExecution: "host-observed-v1"}, "", nil
}
