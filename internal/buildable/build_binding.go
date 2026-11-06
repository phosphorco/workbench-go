package buildable

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const bindingDirectory = ".workbench-build-inputs"

type platformBuildBinding struct {
	Platform            string           `json:"platform"`
	DeclarationIdentity string           `json:"declarationIdentity"`
	ProducerDigest      string           `json:"producerDigest"`
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
