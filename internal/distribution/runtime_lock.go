package distribution

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type RuntimeLock struct {
	SchemaVersion     int                       `json:"schemaVersion"`
	WorkbenchVersion  string                    `json:"workbenchVersion"`
	Runtimes          map[string]RuntimeSpec    `json:"runtimes"`
	BuildDependencies map[string]DependencySpec `json:"buildDependencies"`
}

type DependencySpec struct {
	Version        string            `json:"version"`
	SourceRevision string            `json:"sourceRevision"`
	Licenses       []LicenseArtifact `json:"licenses"`
}

type RuntimeSpec struct {
	Version        string                     `json:"version"`
	SourceRevision string                     `json:"sourceRevision"`
	Artifacts      map[string]RuntimeArtifact `json:"artifacts"`
	Licenses       []LicenseArtifact          `json:"licenses"`
}

type RuntimeArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Format string `json:"format"`
	Member string `json:"member,omitempty"`
}

type LicenseArtifact struct {
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	ArchivePath string `json:"archivePath"`
}

func LoadRuntimeLock(path string) (RuntimeLock, error) {
	file, err := os.Open(path)
	if err != nil {
		return RuntimeLock{}, fmt.Errorf("open runtime lock: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var lock RuntimeLock
	if err := decoder.Decode(&lock); err != nil {
		return RuntimeLock{}, fmt.Errorf("decode runtime lock: %w", err)
	}
	if err := lock.validate(); err != nil {
		return RuntimeLock{}, err
	}
	return lock, nil
}

func (lock RuntimeLock) validate() error {
	if lock.SchemaVersion != 1 {
		return fmt.Errorf("runtime lock schemaVersion = %d, want 1", lock.SchemaVersion)
	}
	if !versionPattern.MatchString(lock.WorkbenchVersion) {
		return fmt.Errorf("runtime lock Workbench version %q is invalid", lock.WorkbenchVersion)
	}
	platforms := []string{"linux-arm64", "linux-x64", "macos-arm64", "macos-x64"}
	for _, runtimeName := range []string{"bun", "pkl"} {
		runtimeSpec, ok := lock.Runtimes[runtimeName]
		if !ok {
			return fmt.Errorf("runtime lock does not designate %s", runtimeName)
		}
		if !versionPattern.MatchString(runtimeSpec.Version) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(runtimeSpec.SourceRevision) {
			return fmt.Errorf("runtime %s has invalid version or source revision", runtimeName)
		}
		gotPlatforms := make([]string, 0, len(runtimeSpec.Artifacts))
		for platform, artifact := range runtimeSpec.Artifacts {
			gotPlatforms = append(gotPlatforms, platform)
			if err := validateArtifact(runtimeName+" "+platform, artifact.URL, artifact.SHA256); err != nil {
				return err
			}
			if artifact.Format != "executable" && artifact.Format != "zip" {
				return fmt.Errorf("runtime %s %s has unsupported format %q", runtimeName, platform, artifact.Format)
			}
			if (artifact.Format == "zip") != (artifact.Member != "") {
				return fmt.Errorf("runtime %s %s has incoherent archive member", runtimeName, platform)
			}
		}
		slices.Sort(gotPlatforms)
		if !slices.Equal(gotPlatforms, platforms) {
			return fmt.Errorf("runtime %s platforms = %v, want %v", runtimeName, gotPlatforms, platforms)
		}
		if len(runtimeSpec.Licenses) == 0 {
			return fmt.Errorf("runtime %s has no license inventory", runtimeName)
		}
		for _, license := range runtimeSpec.Licenses {
			if err := validateArtifact(runtimeName+" license "+license.ArchivePath, license.URL, license.SHA256); err != nil {
				return err
			}
			if license.ArchivePath == "" {
				return fmt.Errorf("runtime %s has an empty license archive path", runtimeName)
			}
		}
	}
	if len(lock.Runtimes) != 2 {
		return fmt.Errorf("runtime lock contains undeclared runtimes")
	}
	expectedDependencies := map[string]string{
		"go": "1.26.6", "msgpack": "5.4.1", "pkl-go": "0.14.0", "tagparser": "2.0.0", "yaml": "3.0.1",
	}
	expectedLicensePaths := map[string][]string{
		"go":        {"go/LICENSE", "go/PATENTS"},
		"msgpack":   {"msgpack/LICENSE"},
		"pkl-go":    {"pkl-go/LICENSE.txt", "pkl-go/NOTICE.txt"},
		"tagparser": {"tagparser/LICENSE"},
		"yaml":      {"yaml/LICENSE"},
	}
	if len(lock.BuildDependencies) != len(expectedDependencies) {
		return fmt.Errorf("runtime lock build dependency inventory is not closed")
	}
	for name, expectedVersion := range expectedDependencies {
		dependency, ok := lock.BuildDependencies[name]
		if !ok {
			return fmt.Errorf("runtime lock does not designate build dependency %s", name)
		}
		if dependency.Version != expectedVersion || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(dependency.SourceRevision) {
			return fmt.Errorf("build dependency %s has invalid version or source revision", name)
		}
		licensePaths := make([]string, 0, len(dependency.Licenses))
		for _, license := range dependency.Licenses {
			if err := validateArtifact(name+" license "+license.ArchivePath, license.URL, license.SHA256); err != nil {
				return err
			}
			if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(license.ArchivePath) {
				return fmt.Errorf("build dependency %s has unsafe license archive path %q", name, license.ArchivePath)
			}
			licensePaths = append(licensePaths, license.ArchivePath)
		}
		slices.Sort(licensePaths)
		if !slices.Equal(licensePaths, expectedLicensePaths[name]) {
			return fmt.Errorf("build dependency %s license inventory = %v, want %v", name, licensePaths, expectedLicensePaths[name])
		}
	}
	return nil
}

func validateArtifact(name, url, digest string) error {
	if len(url) < len("https://") || url[:len("https://")] != "https://" {
		return fmt.Errorf("%s URL %q is not HTTPS", name, url)
	}
	if !sha256Pattern.MatchString(digest) {
		return fmt.Errorf("%s SHA-256 %q is invalid", name, digest)
	}
	return nil
}
