package acceptance_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	publicReleaseAssetBaseURL = "https://github.com/phosphorco/workbench-go/releases/download/0.1.0/"
	wantReleaseMetadataSHA256 = "7dfb598cd2826de21063b937caf4bf1cf76efb8cbeaa0c6ab27d2274060b2774"
	wantReleaseArchiveSHA256  = "546e40a1565681e6819edf89f9696a3fb5657d750296e9c98e3ec0108c87aa0d"
)

func TestPublishedWorkbenchContractsAreImmutableAndAccessible(t *testing.T) {
	assetNames := []string{
		"workbench@0.1.0",
		"workbench@0.1.0.sha256",
		"workbench@0.1.0.zip",
		"workbench@0.1.0.zip.sha256",
	}
	downloadRoot := t.TempDir()
	downloaded := make(map[string][]byte, len(assetNames))
	for _, name := range assetNames {
		contents := downloadPublicReleaseAsset(t, publicReleaseAssetBaseURL+name)
		downloaded[name] = contents
		if err := os.WriteFile(filepath.Join(downloadRoot, name), contents, 0o600); err != nil {
			t.Fatalf("write downloaded release asset %q: %v", name, err)
		}
	}

	metadataDigest := assertPublicReleaseChecksum(
		t,
		"workbench@0.1.0",
		downloaded["workbench@0.1.0"],
		downloaded["workbench@0.1.0.sha256"],
	)
	if metadataDigest != wantReleaseMetadataSHA256 {
		t.Fatalf("published metadata digest = %q, want accepted candidate %q", metadataDigest, wantReleaseMetadataSHA256)
	}
	archiveDigest := assertPublicReleaseChecksum(
		t,
		"workbench@0.1.0.zip",
		downloaded["workbench@0.1.0.zip"],
		downloaded["workbench@0.1.0.zip.sha256"],
	)
	if archiveDigest != wantReleaseArchiveSHA256 {
		t.Fatalf("published archive digest = %q, want accepted candidate %q", archiveDigest, wantReleaseArchiveSHA256)
	}

	var metadata releaseMetadata
	if err := json.Unmarshal(downloaded["workbench@0.1.0"], &metadata); err != nil {
		t.Fatalf("decode published release metadata: %v", err)
	}
	if metadata.Name != "workbench" {
		t.Errorf("published metadata name = %q, want workbench", metadata.Name)
	}
	if metadata.PackageURI != releasePackageURI {
		t.Errorf("published metadata packageUri = %q, want %q", metadata.PackageURI, releasePackageURI)
	}
	if metadata.Version != "0.1.0" {
		t.Errorf("published metadata version = %q, want 0.1.0", metadata.Version)
	}
	if metadata.PackageZIPURL != releasePackageZIP {
		t.Errorf("published metadata packageZipUrl = %q, want %q", metadata.PackageZIPURL, releasePackageZIP)
	}
	if metadata.PackageZIPChecksums.SHA256 != archiveDigest {
		t.Errorf("published metadata ZIP checksum = %q, want %q", metadata.PackageZIPChecksums.SHA256, archiveDigest)
	}

	archivePath := filepath.Join(downloadRoot, "workbench@0.1.0.zip")
	assertPublishedArchiveRoots(t, downloaded["workbench@0.1.0.zip"])
	assertPackagedContractSemantics(t, archivePath)
	verifyImmutableReleaseAndAssets(t, downloadRoot, assetNames)
}

func downloadPublicReleaseAsset(t *testing.T, url string) []byte {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("construct anonymous release request for %q: %v", url, err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("download anonymous release asset %q: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		t.Fatalf("anonymous release asset %q returned %s, want 200 OK", url, response.Status)
	}
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read anonymous release asset %q: %v", url, err)
	}
	if len(contents) == 0 {
		t.Fatalf("anonymous release asset %q is empty", url)
	}
	return contents
}

func assertPublicReleaseChecksum(t *testing.T, name string, contents, sidecar []byte) string {
	t.Helper()
	want := strings.TrimSpace(string(sidecar))
	decoded, err := hex.DecodeString(want)
	if err != nil || len(decoded) != sha256.Size {
		t.Fatalf("published checksum for %q is not one SHA-256 digest: %q", name, sidecar)
	}
	digest := sha256.Sum256(contents)
	got := hex.EncodeToString(digest[:])
	if got != want {
		t.Fatalf("published checksum for %q = %q, want digest of anonymous bytes %q", name, want, got)
	}
	return got
}

func assertPublishedArchiveRoots(t *testing.T, archiveBytes []byte) {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatalf("open published package archive: %v", err)
	}
	entries := make([]string, 0, len(archive.File))
	for _, file := range archive.File {
		entries = append(entries, file.Name)
	}
	sort.Strings(entries)
	want := []string{
		"PackageScopeRepository.pkl",
		"WorkbenchSubject.pkl",
		"pkl/PackageScopeRepository.pkl",
		"pkl/WorkbenchSubject.pkl",
	}
	if !slices.Equal(entries, want) {
		t.Fatalf("published package archive entries = %v, want exactly %v", entries, want)
	}
}

func verifyImmutableReleaseAndAssets(t *testing.T, downloadRoot string, assetNames []string) {
	t.Helper()
	// Public artifact transport is proven anonymously by downloadPublicReleaseAsset.
	// GitHub's cryptographic attestation API is a separate authenticated observation;
	// the explicit repository and detached working directory prevent ambient Git state
	// from selecting what is verified.
	release := exec.Command("gh", "release", "verify", "0.1.0", "-R", "phosphorco/workbench-go")
	release.Dir = t.TempDir()
	if output, err := release.CombinedOutput(); err != nil {
		t.Fatalf("cryptographically verify immutable release: %v\n%s", err, output)
	}
	for _, name := range assetNames {
		path := filepath.Join(downloadRoot, name)
		asset := exec.Command(
			"gh", "release", "verify-asset", "0.1.0", path,
			"-R", "phosphorco/workbench-go",
		)
		asset.Dir = t.TempDir()
		if output, err := asset.CombinedOutput(); err != nil {
			t.Fatalf("cryptographically verify downloaded release asset %q: %v\n%s", name, err, output)
		}
	}
}
