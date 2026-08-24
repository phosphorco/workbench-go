package acceptance_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

const (
	releasePackageURI     = "package://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0"
	releasePackageZIP     = "https://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0.zip"
	v020ReleasePackageURI = "package://github.com/phosphorco/workbench-go/releases/download/0.2.0/workbench@0.2.0"
	v020ReleasePackageZIP = "https://github.com/phosphorco/workbench-go/releases/download/0.2.0/workbench@0.2.0.zip"
)

func TestReleasePackageCandidate(t *testing.T) {
	projectRoot := filepath.Clean("..")
	outputRoot := t.TempDir()
	packageReleaseCandidate(t, projectRoot, outputRoot)

	metadataPath := filepath.Join(outputRoot, "workbench@0.2.0")
	archivePath := metadataPath + ".zip"
	metadata := readReleaseMetadata(t, metadataPath)
	if metadata.Name != "workbench" {
		t.Errorf("metadata name = %q, want workbench", metadata.Name)
	}
	if metadata.PackageURI != v020ReleasePackageURI {
		t.Errorf("metadata packageUri = %q, want %q", metadata.PackageURI, v020ReleasePackageURI)
	}
	if metadata.Version != "0.2.0" {
		t.Errorf("metadata version = %q, want 0.2.0", metadata.Version)
	}
	if metadata.PackageZIPURL != v020ReleasePackageZIP {
		t.Errorf("metadata packageZipUrl = %q, want %q", metadata.PackageZIPURL, v020ReleasePackageZIP)
	}

	metadataChecksum := verifyReleaseChecksum(t, metadataPath)
	if metadataChecksum == "" {
		t.Error("metadata checksum is empty")
	}
	archiveChecksum := verifyReleaseChecksum(t, archivePath)
	if metadata.PackageZIPChecksums.SHA256 != archiveChecksum {
		t.Errorf("metadata ZIP checksum = %q, want %q", metadata.PackageZIPChecksums.SHA256, archiveChecksum)
	}

	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open package archive: %v", err)
	}
	t.Cleanup(func() { _ = archive.Close() })

	entries := make([]string, 0, len(archive.File))
	for _, file := range archive.File {
		entries = append(entries, file.Name)
	}
	sort.Strings(entries)
	wantEntries := []string{
		"AgentInstructions.pkl",
		"PackageScopeRepository.pkl",
		"Repository.pkl",
		"WorkbenchCommitPlan.pkl",
		"WorkbenchSubject.pkl",
		"WorkbenchWorldSnapshot.pkl",
		"pkl/AgentInstructions.pkl",
		"pkl/PackageScopeRepository.pkl",
		"pkl/Repository.pkl",
		"pkl/WorkbenchCommitPlan.pkl",
		"pkl/WorkbenchSubject.pkl",
		"pkl/WorkbenchWorldSnapshot.pkl",
	}
	if !slices.Equal(entries, wantEntries) {
		t.Fatalf("package archive entries = %v, want exactly %v", entries, wantEntries)
	}

	assertPackagedContractSemantics(t, archivePath)

	secondOutputRoot := t.TempDir()
	packageReleaseCandidate(t, projectRoot, secondOutputRoot)
	for _, name := range []string{
		"workbench@0.2.0",
		"workbench@0.2.0.sha256",
		"workbench@0.2.0.zip",
		"workbench@0.2.0.zip.sha256",
	} {
		first, err := os.ReadFile(filepath.Join(outputRoot, name))
		if err != nil {
			t.Fatalf("read first %q candidate: %v", name, err)
		}
		second, err := os.ReadFile(filepath.Join(secondOutputRoot, name))
		if err != nil {
			t.Fatalf("read second %q candidate: %v", name, err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("release artifact %q differs across identical package runs", name)
		}
	}
}

func packageReleaseCandidate(t *testing.T, projectRoot, outputRoot string) {
	t.Helper()
	command := exec.Command(
		"pkl", "project", "package",
		"--skip-publish-check",
		"--output-path", outputRoot,
		projectRoot,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("package release candidate: %v\n%s", err, output)
	}
}

type releaseMetadata struct {
	Name                string `json:"name"`
	PackageURI          string `json:"packageUri"`
	Version             string `json:"version"`
	PackageZIPURL       string `json:"packageZipUrl"`
	PackageZIPChecksums struct {
		SHA256 string `json:"sha256"`
	} `json:"packageZipChecksums"`
}

func readReleaseMetadata(t *testing.T, path string) releaseMetadata {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release metadata: %v", err)
	}
	var metadata releaseMetadata
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		t.Fatalf("decode release metadata: %v", err)
	}
	return metadata
}

func verifyReleaseChecksum(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release artifact %q: %v", filepath.Base(path), err)
	}
	digest := sha256.Sum256(contents)
	want := hex.EncodeToString(digest[:])
	checksum, err := os.ReadFile(path + ".sha256")
	if err != nil {
		t.Fatalf("read release checksum %q: %v", filepath.Base(path)+".sha256", err)
	}
	if string(checksum) != want {
		t.Fatalf("release checksum for %q = %q, want %q", filepath.Base(path), checksum, want)
	}
	return want
}

func assertPackagedContractSemantics(t *testing.T, archivePath string) {
	t.Helper()
	modulePath := filepath.ToSlash(archivePath)
	tests := []struct {
		name        string
		source      string
		wantSuccess bool
	}{
		{
			name: "valid subject",
			source: `amends "modulepath:/WorkbenchSubject.pkl"

workLine {
  branch = "cole/2026-08-workbench-fixture"
  baseBranch = "main"
}
entrypoints {
  "https://github.com/phosphorco/workbench-fixture-entry"
}
`,
			wantSuccess: true,
		},
		{
			name: "invalid subject branch",
			source: `amends "modulepath:/WorkbenchSubject.pkl"

workLine {
  branch = ""
  baseBranch = "main"
}
entrypoints {
  "https://github.com/phosphorco/workbench-fixture-entry"
}
`,
		},
		{
			name: "valid repository",
			source: `amends "modulepath:/PackageScopeRepository.pkl"

scope = "@workbench-entry"
includes {
	  ["phosphorco/workbench-fixture-library"] {}
}
`,
			wantSuccess: true,
		},
		{
			name: "valid repository shape",
			source: `amends "modulepath:/Repository.pkl"

includes {
  ["phosphorco/workbench-fixture-library"] {}
}
`,
			wantSuccess: true,
		},
		{
			name: "repository shape rejects authored identity",
			source: `amends "modulepath:/Repository.pkl"

identity = "hand-authored"
`,
		},
		{
			name: "valid agent instructions",
			source: `amends "modulepath:/AgentInstructions.pkl"

prose = "Keep Git-owned source intact."
subject {
  workLine {
    branch = "workbench/proof-0.2.0"
    baseBranch = "main"
  }
  entrypoints { "https://github.com/phosphorco/workbench-fixture-entry" }
}
resources {
  new {
    identity = "@workbench-entry"
    github = "phosphorco/workbench-fixture-entry"
    shape = new PackageScopeShape { scope = "@workbench-entry" }
    canonicalPath = "pkg/@workbench-entry"
    branch = "workbench/proof-0.2.0"
    health = "healthy"
  }
}
generatedPaths { "AGENTS.md" }
handOwnedPaths { "AGENTS.pkl" }
`,
			wantSuccess: true,
		},
		{
			name: "valid commit plan",
			source: `amends "modulepath:/WorkbenchCommitPlan.pkl"

changeId = "fixture-change"
summary = "Change both fixture repositories."
commits {
  ["@workbench-entry"] {
    title = "feat: consume library"
    description = "Use the shared value."
    filePaths { "src/index.ts" }
  }
}
`,
			wantSuccess: true,
		},
		{
			name: "valid world snapshot",
			source: `amends "modulepath:/WorkbenchWorldSnapshot.pkl"

resources {
  ["@workbench-entry"] {
    shape = new PackageScopeShape { scope = "@workbench-entry" }
    github = "phosphorco/workbench-fixture-entry"
    canonicalPath = "pkg/@workbench-entry"
    commit = "0123456789abcdef0123456789abcdef01234567"
  }
}
`,
			wantSuccess: true,
		},
		{
			name: "invalid repository scope",
			source: `amends "modulepath:/PackageScopeRepository.pkl"

scope = "@Workbench-Entry"
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module := filepath.Join(t.TempDir(), "consumer.pkl")
			if err := os.WriteFile(module, []byte(test.source), 0o600); err != nil {
				t.Fatalf("write contract consumer: %v", err)
			}
			command := exec.Command("pkl", "eval", "--module-path", modulePath, "--format", "json", module)
			output, err := command.CombinedOutput()
			if test.wantSuccess && err != nil {
				t.Fatalf("evaluate packaged contract: %v\n%s", err, output)
			}
			if !test.wantSuccess && err == nil {
				t.Fatalf("invalid contract unexpectedly evaluated:\n%s", output)
			}
		})
	}
}
