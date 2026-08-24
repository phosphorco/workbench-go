// Package distribution constructs deterministic, Mise-discoverable Workbench
// release archives from explicitly designated inputs.
package distribution

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

type Platform struct {
	OS   string
	Arch string
}

// ArchiveInputs designates every byte allowed into a Workbench archive.
type ArchiveInputs struct {
	Version             string
	Revision            string
	WorkbenchBinary     string
	PklBinary           string
	BunBinary           string
	RuntimeLock         string
	WorkbenchLicense    string
	PklLicense          string
	PklNotice           string
	PklThirdPartyNotice string
	BunLicense          string
	GoLicense           string
	GoPatents           string
	PklGoLicense        string
	PklGoNotice         string
	MsgpackLicense      string
	TagparserLicense    string
}

func AssetName(version string, platform Platform) (string, error) {
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("invalid Workbench release %q", version)
	}
	osName, ok := map[string]string{"darwin": "macos", "linux": "linux"}[platform.OS]
	if !ok {
		return "", fmt.Errorf("unsupported Workbench operating system %q", platform.OS)
	}
	archName, ok := map[string]string{"amd64": "x64", "arm64": "arm64"}[platform.Arch]
	if !ok {
		return "", fmt.Errorf("unsupported Workbench architecture %q", platform.Arch)
	}
	return fmt.Sprintf("workbench-%s-%s-%s.tar.gz", version, osName, archName), nil
}

// WriteArchive writes a byte-deterministic archive with a closed installation
// layout. Every source is read and validated before the output path is opened.
func WriteArchive(output string, inputs ArchiveInputs) error {
	if !versionPattern.MatchString(inputs.Version) {
		return fmt.Errorf("invalid Workbench release %q", inputs.Version)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(inputs.Revision) {
		return fmt.Errorf("invalid Workbench revision %q", inputs.Revision)
	}
	buildMetadata, err := json.Marshal(struct {
		Version  string `json:"version"`
		Revision string `json:"revision"`
	}{Version: inputs.Version, Revision: inputs.Revision})
	if err != nil {
		return fmt.Errorf("encode Workbench build metadata: %w", err)
	}
	buildMetadata = append(buildMetadata, '\n')
	root := "workbench-" + inputs.Version
	entries := []archiveEntry{
		directory(root + "/"),
		directory(root + "/bin/"),
		file(root+"/bin/workbench", inputs.WorkbenchBinary, 0o755),
		directory(root + "/libexec/"),
		directory(root + "/libexec/workbench/"),
		file(root+"/libexec/workbench/bun", inputs.BunBinary, 0o755),
		file(root+"/libexec/workbench/pkl", inputs.PklBinary, 0o755),
		directory(root + "/share/"),
		directory(root + "/share/licenses/"),
		directory(root + "/share/licenses/bun/"),
		file(root+"/share/licenses/bun/LICENSE.md", inputs.BunLicense, 0o644),
		directory(root + "/share/licenses/go/"),
		file(root+"/share/licenses/go/LICENSE", inputs.GoLicense, 0o644),
		file(root+"/share/licenses/go/PATENTS", inputs.GoPatents, 0o644),
		directory(root + "/share/licenses/msgpack/"),
		file(root+"/share/licenses/msgpack/LICENSE", inputs.MsgpackLicense, 0o644),
		directory(root + "/share/licenses/pkl/"),
		file(root+"/share/licenses/pkl/LICENSE.txt", inputs.PklLicense, 0o644),
		file(root+"/share/licenses/pkl/NOTICE.txt", inputs.PklNotice, 0o644),
		file(root+"/share/licenses/pkl/THIRD-PARTY-NOTICES.txt", inputs.PklThirdPartyNotice, 0o644),
		directory(root + "/share/licenses/pkl-go/"),
		file(root+"/share/licenses/pkl-go/LICENSE.txt", inputs.PklGoLicense, 0o644),
		file(root+"/share/licenses/pkl-go/NOTICE.txt", inputs.PklGoNotice, 0o644),
		directory(root + "/share/licenses/tagparser/"),
		file(root+"/share/licenses/tagparser/LICENSE", inputs.TagparserLicense, 0o644),
		directory(root + "/share/licenses/workbench/"),
		file(root+"/share/licenses/workbench/LICENSE", inputs.WorkbenchLicense, 0o644),
		directory(root + "/share/workbench/"),
		contentsFile(root+"/share/workbench/build.json", buildMetadata, 0o644),
		file(root+"/share/workbench/runtime-lock.json", inputs.RuntimeLock, 0o644),
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].name < entries[right].name })
	for index := range entries {
		if err := entries[index].load(); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("create archive output directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".workbench-archive-*")
	if err != nil {
		return fmt.Errorf("create archive candidate: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := writeCompressedArchive(temporary, entries); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close archive candidate: %w", err)
	}
	if err := os.Rename(temporaryPath, output); err != nil {
		return fmt.Errorf("publish archive candidate: %w", err)
	}
	return nil
}

type archiveEntry struct {
	name       string
	sourcePath string
	mode       int64
	directory  bool
	contents   []byte
}

func directory(name string) archiveEntry {
	return archiveEntry{name: name, mode: 0o755, directory: true}
}

func file(name, sourcePath string, mode int64) archiveEntry {
	return archiveEntry{name: name, sourcePath: sourcePath, mode: mode}
}

func contentsFile(name string, contents []byte, mode int64) archiveEntry {
	return archiveEntry{name: name, mode: mode, contents: contents}
}

func (entry *archiveEntry) load() error {
	if entry.directory {
		return nil
	}
	if len(entry.contents) > 0 {
		return nil
	}
	if entry.sourcePath == "" {
		return fmt.Errorf("archive input for %q is not designated", entry.name)
	}
	contents, err := os.ReadFile(entry.sourcePath)
	if err != nil {
		return fmt.Errorf("read archive input for %q: %w", entry.name, err)
	}
	if len(contents) == 0 {
		return fmt.Errorf("archive input for %q is empty", entry.name)
	}
	entry.contents = contents
	return nil
}

func writeCompressedArchive(output io.Writer, entries []archiveEntry) error {
	compressed, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("start compressed archive: %w", err)
	}
	compressed.Header.ModTime = time.Unix(0, 0).UTC()
	compressed.Header.OS = 255
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		typeflag := byte(tar.TypeReg)
		if entry.directory {
			typeflag = tar.TypeDir
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     entry.mode,
			Size:     int64(len(entry.contents)),
			ModTime:  time.Unix(0, 0).UTC(),
			Typeflag: typeflag,
			Format:   tar.FormatUSTAR,
		}
		if err := archive.WriteHeader(header); err != nil {
			return fmt.Errorf("write archive header %q: %w", entry.name, err)
		}
		if len(entry.contents) > 0 {
			if _, err := archive.Write(entry.contents); err != nil {
				return fmt.Errorf("write archive contents %q: %w", entry.name, err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("close tar archive: %w", err)
	}
	if err := compressed.Close(); err != nil {
		return fmt.Errorf("close compressed archive: %w", err)
	}
	return nil
}
