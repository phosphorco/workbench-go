package declaration

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type offlinePackages struct {
	uris  []string
	files map[string][]byte
}

type lockedPackage struct {
	Type      string `json:"type"`
	URI       string `json:"uri"`
	Checksums struct {
		SHA256 string `json:"sha256"`
	} `json:"checksums"`
}

type packageMetadata struct {
	PackageURI   string `json:"packageUri"`
	ZipURL       string `json:"packageZipUrl"`
	ZipChecksums struct {
		SHA256 string `json:"sha256"`
	} `json:"packageZipChecksums"`
	Dependencies map[string]struct {
		URI       string `json:"uri"`
		Checksums struct {
			SHA256 string `json:"sha256"`
		} `json:"checksums"`
	} `json:"dependencies"`
}

// PackageCachePaths names Pkl's package-2 cache files under VERIFY_HOME/pkl/cache.
// Provisioning is separate; evaluation never downloads or modifies cached packages.
func PackageCachePaths(verifyHome, uri string) (metadata, archive string, err error) {
	u, err := url.Parse(uri)
	if err != nil || u == nil || u.Scheme != "package" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || !validPath(strings.TrimPrefix(u.Path, "/")) || !strings.Contains(path.Base(u.Path), "@") {
		return "", "", fmt.Errorf("invalid pinned package URI %q", uri)
	}
	if strings.ContainsAny(u.Host, "\\:") {
		return "", "", fmt.Errorf("invalid package host %q", u.Host)
	}
	root := filepath.Join(verifyHome, "pkl", "cache", "package-2", u.Host, filepath.FromSlash(strings.TrimPrefix(u.Path, "/")))
	base := path.Base(u.Path)
	return filepath.Join(root, base+".json"), filepath.Join(root, base+".zip"), nil
}

func loadPackages(data []byte, home string) (offlinePackages, error) {
	result := offlinePackages{files: map[string][]byte{}}
	if len(data) == 0 {
		return result, nil
	}
	var lock struct {
		SchemaVersion        int                      `json:"schemaVersion"`
		ResolvedDependencies map[string]lockedPackage `json:"resolvedDependencies"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return result, fmt.Errorf("committed PklProject.deps.json: %w", err)
	}
	if lock.SchemaVersion != 1 || lock.ResolvedDependencies == nil {
		return result, fmt.Errorf("unsupported committed PklProject.deps.json")
	}
	byURI := map[string]lockedPackage{}
	for _, dep := range lock.ResolvedDependencies {
		dep.URI = strings.Replace(dep.URI, "projectpackage:", "package:", 1)
		if dep.Type != "remote" {
			return result, fmt.Errorf("offline lock requires remote pinned packages, got %q", dep.Type)
		}
		if len(dep.Checksums.SHA256) != 64 {
			return result, fmt.Errorf("package %s has no pinned SHA256", dep.URI)
		}
		if previous, ok := byURI[dep.URI]; ok && previous.Checksums != dep.Checksums {
			return result, fmt.Errorf("conflicting pins for %s", dep.URI)
		}
		byURI[dep.URI] = dep
	}
	for uri, dep := range byURI {
		if home == "" {
			return result, fmt.Errorf("VERIFY_HOME is required for offline packages")
		}
		metadataPath, archivePath, err := PackageCachePaths(home, uri)
		if err != nil {
			return result, err
		}
		metadata, err := os.ReadFile(metadataPath)
		if err != nil {
			return result, fmt.Errorf("offline package %s metadata absent: %w", uri, err)
		}
		if sha(metadata) != dep.Checksums.SHA256 {
			return result, fmt.Errorf("offline package %s metadata checksum mismatch", uri)
		}
		var value packageMetadata
		if err := json.Unmarshal(metadata, &value); err != nil {
			return result, fmt.Errorf("package %s metadata: %w", uri, err)
		}
		if value.PackageURI != uri {
			return result, fmt.Errorf("package metadata URI mismatch: %s", uri)
		}
		for _, upstream := range value.Dependencies {
			pin, ok := byURI[upstream.URI]
			if !ok || upstream.Checksums.SHA256 != pin.Checksums.SHA256 {
				return result, fmt.Errorf("package %s has an unpinned transitive dependency %s", uri, upstream.URI)
			}
		}
		archive, err := os.ReadFile(archivePath)
		if err != nil {
			return result, fmt.Errorf("offline package %s archive absent: %w", uri, err)
		}
		if sha(archive) != value.ZipChecksums.SHA256 {
			return result, fmt.Errorf("offline package %s archive checksum mismatch", uri)
		}
		result.files[metadataPath] = metadata
		result.files[archivePath] = archive
		result.uris = append(result.uris, uri)
	}
	sort.Strings(result.uris)
	return result, nil
}

func (p offlinePackages) stage(home string) (string, func(), error) {
	if len(p.uris) == 0 {
		return "", func() {}, nil
	}
	parent := filepath.Join(home, "pkl")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", nil, err
	}
	root, err := os.MkdirTemp(parent, "evaluation-packages-")
	if err != nil {
		return "", nil, err
	}
	remove := func() { _ = os.RemoveAll(root) }
	for original, content := range p.files {
		relative, err := filepath.Rel(filepath.Join(home, "pkl", "cache"), original)
		if err != nil {
			remove()
			return "", nil, err
		}
		target := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			remove()
			return "", nil, err
		}
		if err := os.WriteFile(target, content, 0600); err != nil {
			remove()
			return "", nil, err
		}
	}
	return root, remove, nil
}
