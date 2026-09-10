package acceptance_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	publicReleaseAssetBaseURL  = "https://github.com/phosphorco/workbench-go/releases/download/0.1.0/"
	wantReleaseMetadataSHA256  = "7dfb598cd2826de21063b937caf4bf1cf76efb8cbeaa0c6ab27d2274060b2774"
	wantReleaseArchiveSHA256   = "546e40a1565681e6819edf89f9696a3fb5657d750296e9c98e3ec0108c87aa0d"
	publicReleaseRepository    = "phosphorco/workbench-go"
	publicReleaseTag           = "0.1.0"
	publicReleaseTargetURI     = "pkg:github/phosphorco/workbench-go@0.1.0"
	publicReleaseTargetSHA1    = "7a5d98e19faca6a2db49bff3e892b2f806504502"
	releaseAttestationAttempts = 2
	releaseAttestationTimeout  = 20 * time.Second
	releaseAttestationMaxBytes = 64 << 10
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
	assertV010PackagedContractSemantics(t, archivePath)
	verifyImmutableReleaseAndAssets(t, downloadRoot, assetNames)
}

func assertV010PackagedContractSemantics(t *testing.T, archivePath string) {
	t.Helper()
	modulePath := filepath.ToSlash(archivePath)
	consumers := []struct {
		name   string
		source string
	}{
		{
			name: "subject",
			source: `amends "modulepath:/WorkbenchSubject.pkl"

workLine {
  branch = "workbench/proof-0.1.0"
  baseBranch = "main"
}
entrypoints { "https://github.com/phosphorco/workbench-fixture-entry" }
`,
		},
		{
			name: "package scope repository",
			source: `amends "modulepath:/PackageScopeRepository.pkl"

scope = "@workbench-entry"
includes {
  ["@workbench-library"] {
    github = "phosphorco/workbench-fixture-library"
  }
}
`,
		},
	}
	for _, consumer := range consumers {
		t.Run(consumer.name, func(t *testing.T) {
			module := filepath.Join(t.TempDir(), "consumer.pkl")
			if err := os.WriteFile(module, []byte(consumer.source), 0o600); err != nil {
				t.Fatalf("write 0.1 contract consumer: %v", err)
			}
			command := exec.Command("pkl", "eval", "--module-path", modulePath, "--format", "json", module)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("evaluate immutable 0.1 contract: %v\n%s", err, output)
			}
		})
	}
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
	// Fetch one cryptographically verified attestation snapshot. The explicit
	// repository and detached working directory prevent ambient Git state from
	// selecting what is verified; local subject matching below replaces four
	// independently retried API lookups without weakening attestation checks.
	snapshot, diagnostics, err := fetchReleaseAttestationSnapshot(t.Context())
	if err != nil {
		t.Fatalf("cryptographically verify immutable release after %d bounded attempts: %v\n%s", releaseAttestationAttempts, err, strings.Join(diagnostics, "\n"))
	}
	for _, diagnostic := range diagnostics {
		t.Logf("release attestation retrieval diagnostic: %s", diagnostic)
	}
	attested, err := validateReleaseAttestationSnapshot(snapshot, assetNames)
	if err != nil {
		t.Fatalf("cryptographically verify immutable release snapshot: %v", err)
	}
	for name, wantDigest := range attested {
		path := filepath.Join(downloadRoot, name)
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read downloaded release asset %q for attestation subject match: %v", name, err)
		}
		digest := sha256.Sum256(contents)
		gotDigest := hex.EncodeToString(digest[:])
		if gotDigest != wantDigest {
			t.Fatalf("downloaded release asset %q digest = %q, want verified attestation subject %q", name, gotDigest, wantDigest)
		}
	}
}

type releaseAttestationSnapshot struct {
	VerificationResult struct {
		Statement struct {
			Predicate struct {
				Repository string `json:"repository"`
				Tag        string `json:"tag"`
			} `json:"predicate"`
			Subject []struct {
				URI    string            `json:"uri"`
				Name   *string           `json:"name"`
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
		} `json:"statement"`
	} `json:"verificationResult"`
}

func expectedReleaseAttestationAssets() map[string]string {
	return map[string]string{
		"workbench@0.1.0":            wantReleaseMetadataSHA256,
		"workbench@0.1.0.sha256":     "928d796beb6ee3d313edfb2fa233019c28c7ccd90788c69a8a1909a9c417c9fd",
		"workbench@0.1.0.zip":        wantReleaseArchiveSHA256,
		"workbench@0.1.0.zip.sha256": "c85f879c1d2400824d3e2e3a825239687b694a6cd88b324f2f563b67eec501b4",
	}
}

func validateReleaseAttestationSnapshot(snapshot []byte, assetNames []string) (map[string]string, error) {
	wantAssets := expectedReleaseAttestationAssets()
	if len(assetNames) != len(wantAssets) {
		return nil, fmt.Errorf("asset input count = %d, want exactly %d", len(assetNames), len(wantAssets))
	}
	inputNames := make(map[string]struct{}, len(assetNames))
	for _, name := range assetNames {
		if _, duplicate := inputNames[name]; duplicate {
			return nil, fmt.Errorf("asset input %q is duplicated", name)
		}
		if _, known := wantAssets[name]; !known {
			return nil, fmt.Errorf("asset input %q is not an expected 0.1.0 asset", name)
		}
		inputNames[name] = struct{}{}
	}
	if len(inputNames) != len(wantAssets) {
		return nil, fmt.Errorf("asset input set is incomplete")
	}

	var attestation releaseAttestationSnapshot
	if err := json.Unmarshal(snapshot, &attestation); err != nil {
		return nil, fmt.Errorf("decode verified attestation JSON: %w", err)
	}
	predicate := attestation.VerificationResult.Statement.Predicate
	if predicate.Repository != publicReleaseRepository {
		return nil, fmt.Errorf("attestation repository = %q, want %q", predicate.Repository, publicReleaseRepository)
	}
	if predicate.Tag != publicReleaseTag {
		return nil, fmt.Errorf("attestation tag = %q, want %q", predicate.Tag, publicReleaseTag)
	}
	if len(attestation.VerificationResult.Statement.Subject) != len(wantAssets)+1 {
		return nil, fmt.Errorf("attestation subject count = %d, want exactly %d assets plus target", len(attestation.VerificationResult.Statement.Subject), len(wantAssets)+1)
	}

	gotAssets := make(map[string]string, len(wantAssets))
	targetCount := 0
	for _, subject := range attestation.VerificationResult.Statement.Subject {
		if subject.Name == nil {
			targetCount++
			if subject.URI != publicReleaseTargetURI {
				return nil, fmt.Errorf("attestation target URI = %q, want %q", subject.URI, publicReleaseTargetURI)
			}
			if len(subject.Digest) != 1 || subject.Digest["sha1"] != publicReleaseTargetSHA1 {
				return nil, fmt.Errorf("attestation target subject does not match %s", publicReleaseTargetSHA1)
			}
			continue
		}
		name := *subject.Name
		wantDigest, known := wantAssets[name]
		if !known {
			return nil, fmt.Errorf("attestation contains unexpected asset subject %q", name)
		}
		if _, duplicate := gotAssets[name]; duplicate {
			return nil, fmt.Errorf("attestation contains duplicate asset subject %q", name)
		}
		if len(subject.Digest) != 1 || subject.Digest["sha256"] != wantDigest {
			return nil, fmt.Errorf("attestation digest for %q does not match pinned digest", name)
		}
		gotAssets[name] = subject.Digest["sha256"]
	}
	if targetCount != 1 {
		return nil, fmt.Errorf("attestation target subject count = %d, want exactly 1", targetCount)
	}
	if len(gotAssets) != len(wantAssets) {
		return nil, fmt.Errorf("attestation asset subject count = %d, want exactly %d", len(gotAssets), len(wantAssets))
	}
	return gotAssets, nil
}

type boundedReleaseOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (output *boundedReleaseOutput) Write(p []byte) (int, error) {
	remaining := output.limit - output.Len()
	if remaining <= 0 {
		output.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = output.Buffer.Write(p[:remaining])
		output.truncated = true
		return len(p), nil
	}
	return output.Buffer.Write(p)
}

func runReleaseAttestationCommand(parent context.Context) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(parent, releaseAttestationTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "gh", "release", "verify", publicReleaseTag, "-R", publicReleaseRepository, "--format", "json")
	command.Dir = os.TempDir()
	command.WaitDelay = 2 * time.Second
	stdout := &boundedReleaseOutput{limit: releaseAttestationMaxBytes}
	stderr := &boundedReleaseOutput{limit: releaseAttestationMaxBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("gh release verify deadline: %w", ctx.Err())
	}
	diagnostic := fmt.Sprintf("stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	if stdout.truncated || stderr.truncated {
		err = fmt.Errorf("gh release verify output exceeded %d bytes per stream", releaseAttestationMaxBytes)
	}
	if err != nil {
		return nil, diagnostic, err
	}
	return stdout.Bytes(), diagnostic, nil
}

func fetchReleaseAttestationSnapshot(parent context.Context) ([]byte, []string, error) {
	diagnostics := make([]string, 0, releaseAttestationAttempts)
	for attempt := 1; attempt <= releaseAttestationAttempts; attempt++ {
		snapshot, diagnostic, err := runReleaseAttestationCommand(parent)
		if err == nil {
			if len(diagnostics) > 0 {
				diagnostics = append(diagnostics, fmt.Sprintf("attempt %d succeeded", attempt))
			}
			return snapshot, diagnostics, nil
		}
		diagnostics = append(diagnostics, fmt.Sprintf("attempt %d failed: %v\n%s", attempt, err, diagnostic))
	}
	return nil, diagnostics, fmt.Errorf("gh release verify failed after %d attempts", releaseAttestationAttempts)
}

func TestReleaseAttestationSnapshotRejectsInvalidSubjects(t *testing.T) {
	assetNames := []string{
		"workbench@0.1.0",
		"workbench@0.1.0.sha256",
		"workbench@0.1.0.zip",
		"workbench@0.1.0.zip.sha256",
	}
	cases := []struct {
		name   string
		mutate func([]map[string]any) []map[string]any
	}{
		{
			name: "wrong target",
			mutate: func(subjects []map[string]any) []map[string]any {
				subjects[0]["digest"].(map[string]any)["sha1"] = "0000000000000000000000000000000000000000"
				return subjects
			},
		},
		{
			name: "wrong target URI",
			mutate: func(subjects []map[string]any) []map[string]any {
				subjects[0]["uri"] = "pkg:github/attacker/workbench-go@0.1.0"
				return subjects
			},
		},
		{
			name: "missing asset",
			mutate: func(subjects []map[string]any) []map[string]any {
				return subjects[:len(subjects)-1]
			},
		},
		{
			name: "duplicate asset",
			mutate: func(subjects []map[string]any) []map[string]any {
				return append(subjects, subjects[1])
			},
		},
		{
			name: "extra asset",
			mutate: func(subjects []map[string]any) []map[string]any {
				return append(subjects, map[string]any{
					"name":   "workbench@0.1.0.extra",
					"digest": map[string]any{"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				})
			},
		},
		{
			name: "extra digest algorithm",
			mutate: func(subjects []map[string]any) []map[string]any {
				subjects[1]["digest"].(map[string]any)["sha1"] = "0000000000000000000000000000000000000000"
				return subjects
			},
		},
		{
			name: "wrong asset digest",
			mutate: func(subjects []map[string]any) []map[string]any {
				subjects[1]["digest"].(map[string]any)["sha256"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				return subjects
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			subjects := test.mutate(releaseAttestationFixtureSubjects())
			snapshot := marshalReleaseAttestationFixture(t, subjects)
			if _, err := validateReleaseAttestationSnapshot(snapshot, assetNames); err == nil {
				t.Fatalf("validateReleaseAttestationSnapshot accepted %s", test.name)
			}
		})
	}
}

func TestReleaseAttestationSnapshotAcceptsExactSubjects(t *testing.T) {
	assetNames := []string{
		"workbench@0.1.0",
		"workbench@0.1.0.sha256",
		"workbench@0.1.0.zip",
		"workbench@0.1.0.zip.sha256",
	}
	snapshot := marshalReleaseAttestationFixture(t, releaseAttestationFixtureSubjects())
	if got, err := validateReleaseAttestationSnapshot(snapshot, assetNames); err != nil {
		t.Fatalf("validateReleaseAttestationSnapshot rejected exact fixture: %v", err)
	} else if len(got) != len(assetNames) {
		t.Fatalf("validated subject count = %d, want %d", len(got), len(assetNames))
	}
}

func TestReleaseAttestationSnapshotRejectsWrongIdentity(t *testing.T) {
	assetNames := []string{
		"workbench@0.1.0",
		"workbench@0.1.0.sha256",
		"workbench@0.1.0.zip",
		"workbench@0.1.0.zip.sha256",
	}
	for _, test := range []struct {
		name       string
		repository string
		tag        string
	}{
		{name: "repository", repository: "attacker/workbench-go", tag: publicReleaseTag},
		{name: "tag", repository: publicReleaseRepository, tag: "0.1.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := marshalReleaseAttestationFixtureIdentity(t, releaseAttestationFixtureSubjects(), test.repository, test.tag)
			if _, err := validateReleaseAttestationSnapshot(snapshot, assetNames); err == nil {
				t.Fatalf("validateReleaseAttestationSnapshot accepted wrong %s", test.name)
			}
		})
	}
}

func releaseAttestationFixtureSubjects() []map[string]any {
	return []map[string]any{
		{
			"uri":    "pkg:github/phosphorco/workbench-go@0.1.0",
			"name":   nil,
			"digest": map[string]any{"sha1": "7a5d98e19faca6a2db49bff3e892b2f806504502"},
		},
		{
			"name":   "workbench@0.1.0",
			"digest": map[string]any{"sha256": "7dfb598cd2826de21063b937caf4bf1cf76efb8cbeaa0c6ab27d2274060b2774"},
		},
		{
			"name":   "workbench@0.1.0.sha256",
			"digest": map[string]any{"sha256": "928d796beb6ee3d313edfb2fa233019c28c7ccd90788c69a8a1909a9c417c9fd"},
		},
		{
			"name":   "workbench@0.1.0.zip",
			"digest": map[string]any{"sha256": "546e40a1565681e6819edf89f9696a3fb5657d750296e9c98e3ec0108c87aa0d"},
		},
		{
			"name":   "workbench@0.1.0.zip.sha256",
			"digest": map[string]any{"sha256": "c85f879c1d2400824d3e2e3a825239687b694a6cd88b324f2f563b67eec501b4"},
		},
	}
}

func marshalReleaseAttestationFixture(t *testing.T, subjects []map[string]any) []byte {
	return marshalReleaseAttestationFixtureIdentity(t, subjects, publicReleaseRepository, publicReleaseTag)
}

func marshalReleaseAttestationFixtureIdentity(t *testing.T, subjects []map[string]any, repository, tag string) []byte {
	t.Helper()
	fixture := map[string]any{
		"verificationResult": map[string]any{
			"statement": map[string]any{
				"predicate": map[string]any{
					"repository": repository,
					"tag":        tag,
				},
				"subject": subjects,
			},
		},
	}
	snapshot, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
