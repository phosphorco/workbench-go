package buildable_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/buildable"
)

func TestResolvePrefersTheFirstValidCandidate(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeCandidate(t, repository, ".ci-build/hello", "committed")
	writeCandidate(t, repository, ".local-build/hello", "local")

	resolved, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repository, ".local-build/hello/bin/hello")
	if resolved.Path != want || resolved.Candidate != "local" {
		t.Fatalf("resolved = %#v, want path %q", resolved, want)
	}
}

func TestResolveDeclaredPlatformReturnsCompleteOpaqueVerifiedResolution(t *testing.T) {
	_, repository := fixtureRepository(t)
	writeCandidate(t, repository, ".local-build/hello", "local")

	resolution, err := buildable.ResolveDeclaredPlatform(context.Background(), repository, "hello", helloDeclaration(), "linux-x86_64")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.SchemaVersion != 1 || resolution.Buildable != "hello" || resolution.Candidate != "local" || resolution.Platform != "linux-x86_64" {
		t.Fatalf("resolution identity = %#v", resolution)
	}
	if resolution.Candidate == ".local-build/hello" || resolution.Candidate == ".ci-build/hello" {
		t.Fatalf("resolution leaked candidate root: %#v", resolution)
	}
	if len(resolution.Outputs) != 1 {
		t.Fatalf("resolution outputs = %#v, want complete one-output set", resolution.Outputs)
	}
	output := resolution.Outputs[0]
	if output.Path != filepath.Join(repository, ".local-build/hello/bin/hello") || output.Kind != "executable" || !output.Executable || output.Size <= 0 || len(output.Digest) != 64 {
		t.Fatalf("resolved output = %#v", output)
	}
	if resolution.Source["repository"] != "https://example.test/hello" || !containsString(resolution.Capabilities, "greeting-v1") {
		t.Fatalf("producer facts = source %#v capabilities %#v", resolution.Source, resolution.Capabilities)
	}
	encoded, err := json.Marshal(resolution)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"candidate":".local-build/hello"`) {
		t.Fatalf("resolution JSON leaked candidate root: %s", encoded)
	}
	if strings.Contains(string(encoded), `"sha256"`) {
		t.Fatalf("public resolution boundary leaked sealed-manifest sha256: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"digest":"`) || !strings.Contains(string(encoded), `"outputs"`) {
		t.Fatalf("resolution JSON = %s", encoded)
	}
}

func TestResolveRefusesAnInvalidPreferredCandidateWithoutFallingThrough(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeCandidate(t, repository, ".ci-build/hello", "committed")
	writeCandidate(t, repository, ".local-build/hello", "local")
	write(t, filepath.Join(repository, ".local-build/hello/bin/hello"), "tampered\n", 0o755)

	_, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Resolve error = %T %v, want typed refusal", err, err)
	}
	if refusal.Candidate != "local" || !strings.Contains(refusal.Reason, "hash mismatch") {
		t.Fatalf("refusal = %#v", refusal)
	}
	if !strings.Contains(refusal.Remedy, "mise run hello:build-local") {
		t.Fatalf("remedy = %q", refusal.Remedy)
	}
	if strings.Contains(err.Error(), ".ci-build/hello/bin/hello") {
		t.Fatalf("refusal fell through to committed candidate: %v", err)
	}
}

func TestResolveRefusalKeepsCandidateIdentityOpaque(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	write(t, filepath.Join(repository, ".local-build/hello/manifest.json"), "not json\n", 0o644)

	_, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Candidate != "local" {
		t.Fatalf("Resolve error = %T %v, want opaque local refusal", err, err)
	}
	if strings.Contains(err.Error(), ".local-build/hello") || strings.Contains(err.Error(), repository) {
		t.Fatalf("candidate root leaked through public refusal: %v", err)
	}
}

func TestResolveTreatsAnyCandidateRootAsPresent(t *testing.T) {
	for _, test := range []struct {
		name       string
		descriptor bool
	}{
		{name: "empty root"},
		{name: "descriptor only", descriptor: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workbench, repository := fixtureRepository(t)
			writeCandidate(t, repository, ".ci-build/hello", "committed")
			localRoot := filepath.Join(repository, ".local-build/hello")
			if err := os.MkdirAll(localRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if test.descriptor {
				write(t, filepath.Join(localRoot, buildable.SourceDescriptorFilename), `{"source":{},"capabilities":[]}`+"\n", 0o644)
			}

			_, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
			var refusal *buildable.Refusal
			if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid || refusal.Candidate != "local" {
				t.Fatalf("Resolve error = %T %v, want invalid local candidate", err, err)
			}
			if !strings.Contains(refusal.Reason, "manifest is missing") {
				t.Fatalf("refusal reason = %q", refusal.Reason)
			}
		})
	}
}

func TestResolveRefusesAStaleManifestWithTheRecordedAndCurrentDigests(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeCandidate(t, repository, ".ci-build/hello", "committed")
	write(t, filepath.Join(repository, "producer.txt"), "changed\n", 0o644)
	git(t, repository, "add", "producer.txt")
	git(t, repository, "commit", "-m", "change producer")

	_, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Resolve error = %T %v, want typed refusal", err, err)
	}
	if !strings.Contains(refusal.Reason, "stale") || !strings.Contains(refusal.Reason, "current inputs require") {
		t.Fatalf("refusal reason = %q", refusal.Reason)
	}
	if refusal.Candidate != "committed" {
		t.Fatalf("refusal candidate = %q", refusal.Candidate)
	}
}

func TestResolveRefusesAbsentCandidatesWithoutInvokingTheBuildCommand(t *testing.T) {
	workbench, repository := fixtureRepository(t)

	_, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Resolve error = %T %v, want typed refusal", err, err)
	}
	if refusal.Candidate != "" || !strings.Contains(refusal.Remedy, "mise run hello:build-local") {
		t.Fatalf("refusal = %#v", refusal)
	}
	if _, statErr := os.Stat(filepath.Join(repository, ".local-build")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Resolve acquired build authority: %v", statErr)
	}
}

func TestSealMakesADirtyLocalBuildUsableWithoutBlessingItAsCommitted(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	write(t, filepath.Join(repository, "producer.txt"), "dirty producer\n", 0o644)
	writeUnsealedCandidate(t, repository, ".local-build/hello", "dirty local")

	if err := buildable.Seal(context.Background(), workbench, "hello", ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	sealedManifest, err := os.ReadFile(filepath.Join(repository, ".local-build/hello/manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sealedManifest), `"declarationIdentity"`) {
		t.Fatalf("sealed manifest omitted declaration identity: %s", sealedManifest)
	}
	var sealed struct {
		Outputs []struct {
			SHA256 string `json:"sha256"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(sealedManifest, &sealed); err != nil {
		t.Fatalf("decode sealed manifest: %v", err)
	}
	if len(sealed.Outputs) != 1 || len(sealed.Outputs[0].SHA256) != 64 {
		t.Fatalf("sealed manifest omitted internal outputs[].sha256 evidence: %s", sealedManifest)
	}
	resolution, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Candidate != "local" {
		t.Fatalf("resolution = %#v", resolution)
	}
	publicResolution, err := json.Marshal(resolution)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicResolution), `"sha256"`) {
		t.Fatalf("public resolution boundary leaked sealed-manifest sha256; sealed evidence must remain Workbench-internal: %s", publicResolution)
	}
	head := gitOutput(t, repository, "rev-parse", "HEAD")
	checkErr := buildable.CheckFresh(context.Background(), workbench, "hello", ".local-build/hello", head, head)
	var refusal *buildable.Refusal
	if !errors.As(checkErr, &refusal) || refusal.Code != buildable.RefusalStaleProducerInputs {
		t.Fatalf("dirty-built candidate passed promotion freshness: %T %v", checkErr, checkErr)
	}
}

func TestDirtyWorktreeDigestIgnoresStagedVersusUnstagedIndexState(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	write(t, filepath.Join(repository, "producer.txt"), "same worktree bytes\n", 0o644)
	git(t, repository, "add", "producer.txt")
	writeUnsealedCandidate(t, repository, ".local-build/hello", "local")
	if err := buildable.Seal(context.Background(), workbench, "hello", ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "reset", "-q", "HEAD", "--", "producer.txt")
	if _, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64"); err != nil {
		t.Fatalf("same worktree bytes changed digest after index reset: %v", err)
	}
}

func TestDirtyIndexWithHEADWorktreeBytesMatchesCleanGitTreeDigest(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	write(t, filepath.Join(repository, "producer.txt"), "different staged bytes\n", 0o644)
	git(t, repository, "add", "producer.txt")
	write(t, filepath.Join(repository, "producer.txt"), "producer\n", 0o644)
	if status := gitOutput(t, repository, "status", "--porcelain=v1", "--", "producer.txt"); !strings.HasPrefix(status, "MM ") {
		t.Fatalf("precondition status = %q, want staged and unstaged divergence", status)
	}
	writeUnsealedCandidate(t, repository, ".local-build/hello", "local")
	if err := buildable.Seal(context.Background(), workbench, "hello", ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "reset", "-q", "HEAD", "--", "producer.txt")
	if status := gitOutput(t, repository, "status", "--porcelain=v1", "--", "producer.txt"); status != "" {
		t.Fatalf("postcondition status = %q, want clean", status)
	}
	if _, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64"); err != nil {
		t.Fatalf("HEAD-identical filesystem changed digest when index became clean: %v", err)
	}
}

func TestProjectionBindingIgnoresUnrelatedWorkbenchSource(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeCandidate(t, repository, ".ci-build/hello", "committed")
	write(t, filepath.Join(repository, "workbench.pkl"), "changed declaration\n", 0o644)

	if _, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64"); err != nil {
		t.Fatalf("Resolve rejected an unrelated workbench.pkl edit: %v", err)
	}
}

func TestDeclarationIdentityIsolatedPerBuildable(t *testing.T) {
	first := helloDeclaration()
	second := helloDeclaration()
	first.Candidates[0].Root = ".local-build/first"
	first.Candidates[1].Root = ".ci-build/first"
	second.Candidates[0].Root = ".local-build/second"
	second.Candidates[1].Root = ".ci-build/second"
	firstID, err := buildable.DeclarationIdentity("first", first)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := buildable.DeclarationIdentity("second", second)
	if err != nil {
		t.Fatal(err)
	}
	first.Manifest.ContractID = "first-changed"
	changedFirstID, err := buildable.DeclarationIdentity("first", first)
	if err != nil {
		t.Fatal(err)
	}
	unchangedSecondID, err := buildable.DeclarationIdentity("second", second)
	if err != nil {
		t.Fatal(err)
	}
	if changedFirstID == firstID {
		t.Fatal("target declaration identity did not change")
	}
	if unchangedSecondID != secondID {
		t.Fatal("unrelated buildable declaration identity changed")
	}
}

func TestCheckFreshComposesIntegrityVerificationBeforeFreshness(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeUnsealedCandidate(t, repository, ".local-build/hello", "local")
	if err := buildable.Seal(context.Background(), workbench, "hello", ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	head := gitOutput(t, repository, "rev-parse", "HEAD")
	write(t, filepath.Join(repository, ".local-build/hello/bin/hello"), "corrupted\n", 0o755)
	err := buildable.CheckFresh(context.Background(), workbench, "hello", ".local-build/hello", head, head)
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid {
		t.Fatalf("CheckFresh error = %T %v, want integrity refusal", err, err)
	}
	if !strings.Contains(refusal.Reason, "hash mismatch") {
		t.Fatalf("CheckFresh refusal = %v", err)
	}
}

func TestOriginManifestWithoutDeclarationIdentityPassesVerifyAndCheckFreshAgainstOriginMain(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeCandidate(t, repository, ".local-build/hello", "local")
	manifestPath := filepath.Join(repository, ".local-build/hello/manifest.json")
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		t.Fatal(err)
	}
	delete(manifest, "declarationIdentity")
	legacy, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, manifestPath, string(legacy), 0o644)
	if err := buildable.Verify(context.Background(), workbench, "hello", ".local-build/hello", false); err != nil {
		t.Fatalf("origin-shaped manifest verify = %v", err)
	}
	head := gitOutput(t, repository, "rev-parse", "HEAD")
	git(t, repository, "update-ref", "refs/remotes/origin/main", head)
	if err := buildable.CheckFresh(context.Background(), workbench, "hello", ".local-build/hello", head, "origin/main"); err != nil {
		t.Fatalf("origin-shaped manifest check-fresh = %v", err)
	}
}

func TestCheckFreshIgnoresAnUnrelatedWorkbenchEdit(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeUnsealedCandidate(t, repository, ".local-build/hello", "local")
	if err := buildable.Seal(context.Background(), workbench, "hello", ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	head := gitOutput(t, repository, "rev-parse", "HEAD")
	write(t, filepath.Join(repository, "workbench.pkl"), "unrelated semantic source edit\n", 0o644)
	if err := buildable.CheckFresh(context.Background(), workbench, "hello", ".local-build/hello", head, head); err != nil {
		t.Fatalf("CheckFresh rejected unrelated workbench edit: %v", err)
	}
}

func TestProjectedDeclarationIdentityRefusesRelevantDeclarationChange(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	declaration := helloDeclaration()
	projectFixture(t, workbench, repository, declaration)
	current := declaration
	current.BuildCommand.Arguments = []string{"changed-declaration"}

	err := buildable.ValidateProjectedDeclaration(workbench, "hello", current)
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalProjectionStale {
		t.Fatalf("projected declaration validation = %T %v, want stale projection refusal", err, err)
	}
	if !strings.Contains(refusal.Reason, "declaration identity") {
		t.Fatalf("refusal = %#v", refusal)
	}
}

func TestLegacyProjectionWithoutDeclarationIdentityUsesSourceDigestFallback(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeCandidate(t, repository, ".local-build/hello", "local")
	projectionPath := filepath.Join(workbench, buildable.ProjectionPath)
	encoded, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	var projection map[string]any
	if err := json.Unmarshal(encoded, &projection); err != nil {
		t.Fatal(err)
	}
	buildables, ok := projection["buildables"].(map[string]any)
	if !ok {
		t.Fatalf("projection buildables = %#v", projection["buildables"])
	}
	entry, ok := buildables["hello"].(map[string]any)
	if !ok {
		t.Fatalf("projection hello entry = %#v", buildables["hello"])
	}
	owner, ok := entry["owner"].(map[string]any)
	if !ok {
		t.Fatalf("projection owner = %#v", entry["owner"])
	}
	delete(owner, "declarationIdentity")
	legacy, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	write(t, projectionPath, string(legacy), 0o644)
	if _, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64"); err != nil {
		t.Fatalf("legacy projection refused while source was unchanged: %v", err)
	}
	write(t, filepath.Join(repository, "workbench.pkl"), "changed workbench source\n", 0o644)
	_, err = buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalProjectionStale {
		t.Fatalf("legacy projection change error = %T %v, want stale projection refusal", err, err)
	}
}

func TestEncodeProjectionRefusesDuplicateNamesWithBothOwners(t *testing.T) {
	declaration := helloDeclaration()
	_, err := buildable.EncodeProjection([]buildable.ProjectionOwner{
		{Identity: "example/one", RepositoryPath: "repos/one", Source: []byte("one"), Buildables: map[string]buildable.Buildable{"hello": declaration}},
		{Identity: "@example", RepositoryPath: "pkg/@example", Source: []byte("two"), Buildables: map[string]buildable.Buildable{"hello": declaration}},
	})
	if err == nil || !strings.Contains(err.Error(), `"example/one" at "repos/one"`) || !strings.Contains(err.Error(), `"@example" at "pkg/@example"`) {
		t.Fatalf("duplicate projection error = %v", err)
	}
}

func TestValidateForNameRefusesNormalizedPlatformPathCollisions(t *testing.T) {
	declaration := helloDeclaration()
	declaration.Platforms["macos-arm64"] = buildable.Platform{OS: []string{"darwin"}, Arch: []string{"arm64"}, Path: "BIN/../bin/HELLO", Executable: true}
	if err := declaration.ValidateForName("hello"); err == nil || !strings.Contains(err.Error(), "share normalized output path") {
		t.Fatalf("ValidateForName error = %v", err)
	}
}

func TestValidateForNameRefusesNonPortableRelativePaths(t *testing.T) {
	tests := map[string]string{"backslash": `producer\\input`, "delete": "producer\x7finput"}
	for character := byte(0); character <= 0x1f; character++ {
		tests[fmt.Sprintf("control-%02x", character)] = "producer" + string(character) + "input"
	}
	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			declaration := helloDeclaration()
			declaration.InputDetection.Paths = []string{path}
			if err := declaration.ValidateForName("hello"); err == nil {
				t.Fatalf("ValidateForName accepted path %q", path)
			}
		})
	}
}

func TestLifecycleSealsVerifiesChecksFreshAndPromotesUnchanged(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeUnsealedCandidate(t, repository, ".local-build/hello", "local")
	if err := buildable.Seal(context.Background(), workbench, "hello", ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	if err := buildable.Verify(context.Background(), workbench, "hello", ".local-build/hello", false); err != nil {
		t.Fatal(err)
	}
	head := gitOutput(t, repository, "rev-parse", "HEAD")
	if err := buildable.CheckFresh(context.Background(), workbench, "hello", ".local-build/hello", head, head); err != nil {
		t.Fatal(err)
	}
	if err := buildable.Promote(context.Background(), workbench, "hello", ".local-build/hello", ".ci-build/hello"); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"bin/hello", buildable.SourceDescriptorFilename, "manifest.json"} {
		local, err := os.ReadFile(filepath.Join(repository, ".local-build/hello", relative))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(filepath.Join(repository, ".ci-build/hello", relative))
		if err != nil {
			t.Fatal(err)
		}
		if string(local) != string(committed) {
			t.Fatalf("promoted %s changed bytes", relative)
		}
	}
}

func TestBuildRunsTheDeclaredCommandThenValidatesTheRequestedOutput(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	declaration := helloDeclaration()
	declaration.BuildCommand = buildable.BuildCommand{Executable: "true"}
	projectFixture(t, workbench, repository, declaration)
	writeUnsealedCandidate(t, repository, ".local-build/hello", "local")
	if err := buildable.Build(context.Background(), workbench, "hello", "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repository, ".local-build/hello/bin/hello")); err != nil {
		t.Fatal(err)
	}
	if err := buildable.Build(context.Background(), workbench, "hello", "linux-x86_64"); err == nil {
		t.Fatal("Build accepted a missing requested platform output")
	}
}

func TestSealRequiresTheExactStrictSourceDescriptor(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeUnsealedCandidate(t, repository, ".local-build/hello", "local")
	wrong := filepath.Join(repository, ".local-build/hello", "buildable-source.json")
	if err := os.Rename(filepath.Join(repository, ".local-build/hello", buildable.SourceDescriptorFilename), wrong); err != nil {
		t.Fatal(err)
	}
	if err := buildable.Seal(context.Background(), workbench, "hello", ".local-build/hello"); err == nil || !strings.Contains(err.Error(), buildable.SourceDescriptorFilename) {
		t.Fatalf("Seal with legacy descriptor name error = %v", err)
	}
	if err := os.Rename(wrong, filepath.Join(repository, ".local-build/hello", buildable.SourceDescriptorFilename)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repository, ".local-build/hello", buildable.SourceDescriptorFilename)
	write(t, path, `{"source":{"repository":"https://example.test/hello","channel":"latest","revision":"fixture","version":"1.0.0","nestedRevision":"fixture"},"capabilities":["greeting-v1"],"artifact":"hidden"}`+"\n", 0o644)
	if err := buildable.Seal(context.Background(), workbench, "hello", ".local-build/hello"); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Seal accepted a non-strict source descriptor: %v", err)
	}
}

func TestGitWorktreeStrategyIncludesUntrackedProducerInputs(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	declaration := helloDeclaration()
	declaration.InputDetection.Paths = []string{"producer.txt", "producer-inputs"}
	projectFixture(t, workbench, repository, declaration)
	write(t, filepath.Join(repository, "producer-inputs", "untracked.txt"), "first\n", 0o644)
	writeUnsealedCandidate(t, repository, ".local-build/hello", "local")
	if err := buildable.Seal(context.Background(), workbench, "hello", ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repository, "producer-inputs", "untracked.txt"), "second\n", 0o644)
	_, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalStaleProducerInputs {
		t.Fatalf("changed untracked producer input error = %T %v", err, err)
	}
}

func TestCheckReportsARefusalWithoutMutation(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	before := gitOutput(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
	report, err := buildable.Check(context.Background(), workbench, "hello", "linux", "amd64")
	if err == nil || report.Status != "refused" || report.Refusal == nil || report.Refusal.Code != buildable.RefusalCandidatesAbsent {
		t.Fatalf("Check = %#v, %v", report, err)
	}
	after := gitOutput(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
	if after != before {
		t.Fatalf("check mutated repository: before %q after %q", before, after)
	}
}

func fixtureRepository(t *testing.T) (string, string) {
	t.Helper()
	workbench := t.TempDir()
	repository := filepath.Join(workbench, "repos", "hello")
	write(t, filepath.Join(repository, "producer.txt"), "producer\n", 0o644)
	source := []byte("fixture workbench.pkl\n")
	write(t, filepath.Join(repository, "workbench.pkl"), string(source), 0o644)
	git(t, repository, "init", "-q", "-b", "main")
	git(t, repository, "config", "user.name", "Buildable Test")
	git(t, repository, "config", "user.email", "buildable@example.test")
	git(t, repository, "add", "producer.txt")
	git(t, repository, "commit", "-qm", "producer")
	encoded, err := buildable.EncodeProjection([]buildable.ProjectionOwner{{
		Identity: "example/hello", RepositoryPath: "repos/hello", Source: source,
		Buildables: map[string]buildable.Buildable{"hello": helloDeclaration()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, buildable.ProjectionPath), string(encoded), 0o644)
	return workbench, repository
}

func projectFixture(t *testing.T, workbench, repository string, declaration buildable.Buildable) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(repository, "workbench.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := buildable.EncodeProjection([]buildable.ProjectionOwner{{
		Identity: "example/hello", RepositoryPath: "repos/hello", Source: source,
		Buildables: map[string]buildable.Buildable{"hello": declaration},
	}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(workbench, buildable.ProjectionPath), string(encoded), 0o644)
}

func helloDeclaration() buildable.Buildable {
	return buildable.Buildable{
		InputDetection: buildable.InputDetection{Strategy: "gitHeadTree", Paths: []string{"producer.txt"}},
		BuildCommand:   buildable.BuildCommand{Executable: "mise", Arguments: []string{"run", "hello:build-local"}},
		Manifest: buildable.ManifestContract{
			SchemaVersion: 2, Kind: "hello-artifact-manifest", ContractID: "hello-v2",
			ExpectedSource:       map[string]string{"repository": "https://example.test/hello", "channel": "latest"},
			RequiredSourceFields: []string{"revision", "version", "nestedRevision"},
			RequiredCapabilities: []string{"greeting-v1"},
		},
		Candidates: []buildable.Candidate{
			{Root: ".local-build/hello", InputStrategy: "gitWorktree", InvalidRemedy: "Run 'mise run hello:build-local' to rebuild it or remove the local candidate."},
			{Root: ".ci-build/hello", InputStrategy: "gitHeadTree", InvalidRemedy: "Restore the committed candidate or run the authorized CI promotion."},
		},
		Platforms: map[string]buildable.Platform{
			"linux-x86_64": {OS: []string{"linux"}, Arch: []string{"amd64"}, Path: "bin/hello", Executable: true},
		},
	}
}

func writeCandidate(t *testing.T, repository, root, label string) {
	t.Helper()
	contents := "#!/bin/sh\necho " + label + "\n"
	write(t, filepath.Join(repository, root, "bin/hello"), contents, 0o755)
	digest := sha256.Sum256([]byte(contents))
	manifest := map[string]any{
		"schemaVersion":       2,
		"kind":                "hello-artifact-manifest",
		"contractId":          "hello-v2",
		"declarationIdentity": mustDeclarationIdentity(t, "hello", helloDeclaration()),
		"source":              map[string]any{"repository": "https://example.test/hello", "revision": "fixture", "channel": "latest", "version": "1.0.0", "nestedRevision": "fixture"},
		"producerInputs": map[string]any{
			"algorithm": "sha256", "digest": producerDigest(t, repository),
		},
		"capabilities": []string{"greeting-v1"},
		"outputs": []map[string]any{{
			"platform": "linux-x86_64", "path": "bin/hello", "sha256": hex.EncodeToString(digest[:]),
			"size": len(contents), "executable": true,
		}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repository, root, "manifest.json"), string(encoded)+"\n", 0o644)
}

func writeUnsealedCandidate(t *testing.T, repository, root, label string) {
	t.Helper()
	write(t, filepath.Join(repository, root, "bin/hello"), "#!/bin/sh\necho "+label+"\n", 0o755)
	descriptor := map[string]any{
		"source": map[string]string{
			"repository": "https://example.test/hello", "channel": "latest",
			"revision": "fixture", "version": "1.0.0", "nestedRevision": "fixture",
		},
		"capabilities": []string{"greeting-v1"},
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repository, root, buildable.SourceDescriptorFilename), string(encoded)+"\n", 0o644)
}

func producerDigest(t *testing.T, repository string) string {
	t.Helper()
	command := exec.Command("git", "ls-tree", "-r", "--full-tree", "HEAD", "--", "producer.txt")
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(output)
	return hex.EncodeToString(digest[:])
}

func write(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func mustDeclarationIdentity(t *testing.T, name string, declaration buildable.Buildable) string {
	t.Helper()
	identity, err := buildable.DeclarationIdentity(name, declaration)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func git(t *testing.T, repository string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutput(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
