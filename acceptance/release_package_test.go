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
	"strings"
	"testing"
)

const (
	releasePackageURI        = "package://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0"
	releasePackageZIP        = "https://github.com/phosphorco/workbench-go/releases/download/0.1.0/workbench@0.1.0.zip"
	v020ReleasePackageURI    = "package://github.com/phosphorco/workbench-go/releases/download/0.2.0/workbench@0.2.0"
	v020ReleasePackageZIP    = "https://github.com/phosphorco/workbench-go/releases/download/0.2.0/workbench@0.2.0.zip"
	currentReleasePackageURI = "package://github.com/phosphorco/workbench-go/releases/download/0.6.2/workbench@0.6.1"
	currentReleasePackageZIP = "https://github.com/phosphorco/workbench-go/releases/download/0.6.2/workbench@0.6.1.zip"
)

func TestCurrentContractURIMatchesThePublishedReleaseAsset(t *testing.T) {
	read := func(path string) string {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(contents)
	}
	project := read(filepath.Join("..", "PklProject"))
	for _, marker := range []string{
		`local releaseCoordinate = "0.6.2"`,
		`baseUri = "package://github.com/phosphorco/workbench-go/releases/download/\(releaseCoordinate)/workbench"`,
		`version = "0.6.1"`,
		`"https://github.com/phosphorco/workbench-go/releases/download/\(releaseCoordinate)/workbench@\(version).zip"`,
	} {
		if !strings.Contains(project, marker) {
			t.Fatalf("PklProject lacks current independent release marker %q", marker)
		}
	}
	if strings.Contains(project, "releases/download/0.6.1/workbench@0.6.1") {
		t.Fatal("PklProject points at nonexistent release coordinate 0.6.1 for contract package 0.6.1")
	}

	workflow := read(filepath.Join("..", ".github", "workflows", "release.yml"))
	for _, marker := range []string{
		"WORKBENCH_VERSION: 0.6.2",
		"WORKBENCH_CONTRACT_VERSION: 0.6.1",
		"mise exec -- pkl project package --skip-publish-check --output-path contracts .",
		"contracts/workbench@0.6.1.zip",
		"gh release create \"${{ github.ref_name }}\" release-assets/*",
	} {
		if !strings.Contains(workflow, marker) {
			t.Fatalf("release workflow lacks current contract publication marker %q", marker)
		}
	}
	if currentReleasePackageURI != "package://github.com/phosphorco/workbench-go/releases/download/0.6.2/workbench@0.6.1" {
		t.Fatalf("current contract URI = %q, want the 0.6.2 release asset", currentReleasePackageURI)
	}
	acceptance := read(filepath.Join("..", ".github", "workflows", "release-acceptance.yml"))
	if !strings.Contains(acceptance, currentReleasePackageURI+"#/Repository.pkl") {
		t.Fatal("release acceptance does not amend the package asset produced under release 0.6.2")
	}
	if strings.Contains(acceptance, "releases/download/0.6.1/workbench@0.6.1") {
		t.Fatal("release acceptance points at nonexistent release coordinate 0.6.1 for contract package 0.6.1")
	}
}

func TestReleasePackageCandidate(t *testing.T) {
	projectRoot := filepath.Clean("..")
	outputRoot := t.TempDir()
	packageReleaseCandidate(t, projectRoot, outputRoot)

	metadataPath := filepath.Join(outputRoot, "workbench@0.6.1")
	archivePath := metadataPath + ".zip"
	metadata := readReleaseMetadata(t, metadataPath)
	if metadata.Name != "workbench" {
		t.Errorf("metadata name = %q, want workbench", metadata.Name)
	}
	if metadata.PackageURI != currentReleasePackageURI {
		t.Errorf("metadata packageUri = %q, want %q", metadata.PackageURI, currentReleasePackageURI)
	}
	if metadata.Version != "0.6.1" {
		t.Errorf("metadata version = %q, want 0.6.1", metadata.Version)
	}
	if metadata.PackageZIPURL != currentReleasePackageZIP {
		t.Errorf("metadata packageZipUrl = %q, want %q", metadata.PackageZIPURL, currentReleasePackageZIP)
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
		"WorkbenchSnapshot.pkl",
		"WorkbenchSubject.pkl",
		"pkl/AgentInstructions.pkl",
		"pkl/PackageScopeRepository.pkl",
		"pkl/Repository.pkl",
		"pkl/WorkbenchCommitPlan.pkl",
		"pkl/WorkbenchSnapshot.pkl",
		"pkl/WorkbenchSubject.pkl",
	}
	if !slices.Equal(entries, wantEntries) {
		t.Fatalf("package archive entries = %v, want exactly %v", entries, wantEntries)
	}

	assertPackagedContractSemantics(t, archivePath)

	secondOutputRoot := t.TempDir()
	packageReleaseCandidate(t, projectRoot, secondOutputRoot)
	for _, name := range []string{
		"workbench@0.6.1",
		"workbench@0.6.1.sha256",
		"workbench@0.6.1.zip",
		"workbench@0.6.1.zip.sha256",
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
packages {
  ["@workbench-entry/app"] {}
}
`,
			wantSuccess: true,
		},
		{
			name: "package scope rejects another scope",
			source: `amends "modulepath:/PackageScopeRepository.pkl"

scope = "@workbench-entry"
packages {
  ["@other/app"] {}
}
`,
		},
		{
			name: "package scope rejects nested package leaf",
			source: `amends "modulepath:/PackageScopeRepository.pkl"

scope = "@workbench-entry"
packages {
  ["@workbench-entry/apps/web"] {}
}
`,
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
			name: "package scope buildable contract",
			source: `amends "modulepath:/PackageScopeRepository.pkl"

scope = "@workbench-entry"
buildables {
  ["tsgo"] = new Buildable {
    inputDetection = new GitHeadTreeInputDetection { paths { "scripts/tsgo-artifact-contract.mts" } }
    buildCommand = new BuildCommand { executable = "mise"; arguments { "run"; "tsgo:build-local" } }
    manifest = new ManifestContract { schemaVersion = 2; kind = "tsgo-artifact-manifest"; contractId = "tsgo-v2" }
    candidates {
      new BuildableCandidate { root = ".local-build/tsgo"; inputStrategy = "gitWorktree"; invalidRemedy = "Rebuild it." }
      new BuildableCandidate { root = ".ci-build/tsgo"; inputStrategy = "gitHeadTree"; invalidRemedy = "Restore it." }
    }
    platforms {
      ["linux-x86_64"] = new BuildablePlatformOutput { os { "linux" }; arch { "amd64" }; path = "linux-x86_64/tsgo" }
    }
  }
}
`,
			wantSuccess: true,
		},
		{
			name: "repository buildable contract",
			source: `amends "modulepath:/Repository.pkl"

buildables {
  ["tool"] = new Buildable {
    inputDetection = new GitHeadTreeInputDetection { paths { "tools" } }
    buildCommand = new BuildCommand { executable = "mise" }
    manifest = new ManifestContract { schemaVersion = 1; kind = "tool-manifest"; contractId = "tool-v1" }
    candidates {
      new BuildableCandidate { root = ".local-build/tool"; inputStrategy = "gitWorktree"; invalidRemedy = "Rebuild it." }
      new BuildableCandidate { root = ".ci-build/tool"; inputStrategy = "gitHeadTree"; invalidRemedy = "Restore it." }
    }
    platforms {
      ["macos-arm64"] = new BuildablePlatformOutput { os { "darwin" }; arch { "arm64" }; path = "bin/tool" }
    }
  }
}
`,
			wantSuccess: true,
		},
		{
			name: "buildable contract rejects nonportable paths",
			source: `amends "modulepath:/Repository.pkl"

buildables {
  ["tool"] = new Buildable {
    inputDetection = new GitHeadTreeInputDetection { paths { "producer\\input" } }
    buildCommand = new BuildCommand { executable = "mise" }
    manifest = new ManifestContract { schemaVersion = 1; kind = "tool-manifest"; contractId = "tool-v1" }
    candidates {
      new BuildableCandidate { root = ".local-build/tool"; inputStrategy = "gitWorktree"; invalidRemedy = "Rebuild it." }
      new BuildableCandidate { root = ".ci-build/tool"; inputStrategy = "gitHeadTree"; invalidRemedy = "Restore it." }
    }
    platforms {
      ["linux-x86_64"] = new BuildablePlatformOutput { os { "linux" }; arch { "amd64" }; path = "bin\ncontrol" }
    }
  }
}
`,
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
    branch = "workbench/proof-0.4.0"
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
    branch = "workbench/proof-0.4.0"
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
    filePaths { "app/src/index.ts" }
  }
}
`,
			wantSuccess: true,
		},
		{
			name: "valid Workbench Snapshot",
			source: `amends "modulepath:/WorkbenchSnapshot.pkl"

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
