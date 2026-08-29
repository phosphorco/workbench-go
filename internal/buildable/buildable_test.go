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
	if resolved.Path != want || resolved.Candidate != ".local-build/hello" {
		t.Fatalf("resolved = %#v, want path %q", resolved, want)
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
	if refusal.Candidate != ".local-build/hello" || !strings.Contains(refusal.Reason, "hash mismatch") {
		t.Fatalf("refusal = %#v", refusal)
	}
	if !strings.Contains(refusal.Remedy, "mise run hello:build-local") {
		t.Fatalf("remedy = %q", refusal.Remedy)
	}
	if strings.Contains(err.Error(), ".ci-build/hello/bin/hello") {
		t.Fatalf("refusal fell through to committed candidate: %v", err)
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
			if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid || refusal.Candidate != ".local-build/hello" {
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
	if refusal.Candidate != ".ci-build/hello" {
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
	resolution, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Candidate != ".local-build/hello" {
		t.Fatalf("resolution = %#v", resolution)
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

func TestProjectionBindingRefusesChangedWorkbenchSource(t *testing.T) {
	workbench, repository := fixtureRepository(t)
	writeCandidate(t, repository, ".ci-build/hello", "committed")
	write(t, filepath.Join(repository, "workbench.pkl"), "changed declaration\n", 0o644)

	_, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalProjectionStale {
		t.Fatalf("Resolve error = %T %v, want projection-stale refusal", err, err)
	}
	if !strings.Contains(refusal.Remedy, "workbench setup") {
		t.Fatalf("remedy = %q", refusal.Remedy)
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
		"schemaVersion": 2,
		"kind":          "hello-artifact-manifest",
		"contractId":    "hello-v2",
		"source":        map[string]any{"repository": "https://example.test/hello", "revision": "fixture", "channel": "latest", "version": "1.0.0", "nestedRevision": "fixture"},
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
