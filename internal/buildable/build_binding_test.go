package buildable_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/buildable"
)

func TestHistoricalDeclarationIdentityRemainsStableAndBoundModeChangesIt(t *testing.T) {
	// This is the declaration consumed by the installed Workbench 0.8.1
	// build/seal probe; its manifest recorded the identity below.
	const historic = `{"inputDetection":{"strategy":"gitHeadTree","paths":["source.txt","producer.py"]},"buildCommand":{"executable":"python3","arguments":["producer.py"]},"manifest":{"schemaVersion":1,"kind":"probe-manifest","contractId":"probe-v1","expectedSource":{"repository":"scratch"},"requiredSourceFields":["repository"],"requiredCapabilities":["probe-v1"]},"candidates":[{"root":".local-build/probe","inputStrategy":"gitWorktree","invalidRemedy":"rebuild"},{"root":".ci-build/probe","inputStrategy":"gitHeadTree","invalidRemedy":"republish"}],"platforms":{"linux-x86_64":{"os":["linux"],"arch":["amd64"],"executable":true,"outputs":[{"path":"probe","destination":"probe","kind":"executable","executable":true}]}}}`
	var declaration buildable.Buildable
	if err := json.Unmarshal([]byte(historic), &declaration); err != nil {
		t.Fatal(err)
	}
	identity, err := buildable.DeclarationIdentity("probe", declaration)
	if err != nil {
		t.Fatal(err)
	}
	const recorded = "f6aa2540d50283e0264a21555990fc0ff23f6accb2e0cdc5a08eaa2021aea0f7"
	if identity != recorded {
		t.Fatalf("historical declaration identity = %s, want %s", identity, recorded)
	}
	policy := "observed-v1"
	declaration.BuildInputBinding = &policy
	boundIdentity, err := buildable.DeclarationIdentity("probe", declaration)
	if err != nil {
		t.Fatal(err)
	}
	if boundIdentity == identity {
		t.Fatal("bound mode retained the unbound declaration identity")
	}
}

func TestBoundModeRefusesAnUnboundHistoricalCandidate(t *testing.T) {
	_, root := fixtureRepository(t)
	declaration := helloDeclaration()
	writeCandidate(t, root, ".local-build/hello", "legacy")
	if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err != nil {
		t.Fatalf("legacy candidate: %v", err)
	}
	policy := "observed-v1"
	declaration.BuildInputBinding = &policy
	if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err == nil {
		t.Fatal("bound mode accepted an unbound manifest")
	}
}

func copyBindingFixture(t *testing.T, from, to string) {
	t.Helper()
	contents, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	write(t, to, string(contents), 0o755)
}

func bindingFixture(t *testing.T) (string, buildable.Buildable) {
	t.Helper()
	_, repository := fixtureRepository(t)
	declaration := helloDeclaration()
	declaration.BuildCommand = buildable.BuildCommand{Executable: "true"}
	policy := "observed-v1"
	declaration.BuildInputBinding = &policy
	writeUnsealedCandidate(t, repository, ".local-build/hello", "built")
	return repository, declaration
}

func TestUnboundDirectSealRemainsSupported(t *testing.T) {
	_, root := fixtureRepository(t)
	declaration := helloDeclaration()
	writeUnsealedCandidate(t, root, ".local-build/hello", "adopted")
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err != nil {
		t.Fatalf("unbound direct seal: %v", err)
	}
}

func TestRequiredBindingIsCheckedAtSealAndResolve(t *testing.T) {
	root, declaration := bindingFixture(t)
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err == nil {
		t.Fatal("sealed without a build binding")
	}
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("linux-x86_64"))
	record := filepath.Join(root, ".local-build/hello/.workbench-build-inputs", hex.EncodeToString(digest[:])+".json")
	write(t, record, "{bad json", 0o644)
	if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err == nil {
		t.Fatal("resolved malformed binding")
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "producer.txt"), record); err != nil {
		t.Fatal(err)
	}
	if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err == nil {
		t.Fatal("resolved symlinked binding")
	}
}

func TestBuildBindingRejectsSourceChangeBeforeSeal(t *testing.T) {
	root, declaration := bindingFixture(t)
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "producer.txt"), "changed after build\n", 0o644)
	err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello")
	if err == nil || !strings.Contains(err.Error(), "different producer inputs") {
		t.Fatalf("Seal = %v; want stale refusal", err)
	}
}

func TestBuildBindingAllowsStableDirtyInputsAndRejectsChangedUntracked(t *testing.T) {
	root, declaration := bindingFixture(t)
	declaration.InputDetection.Paths = []string{"producer.txt", "producer-inputs"}
	write(t, filepath.Join(root, "producer.txt"), "dirty\n", 0o644)
	write(t, filepath.Join(root, "producer-inputs/new.txt"), "first\n", 0o644)
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "producer-inputs/new.txt"), "second\n", 0o644)
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err == nil {
		t.Fatal("accepted changed untracked input")
	}
}

func TestBuildBindingRejectsChangeDuringBuild(t *testing.T) {
	root, declaration := bindingFixture(t)
	declaration.BuildCommand = buildable.BuildCommand{Executable: "sh", Arguments: []string{"-c", "printf changed > producer.txt"}}
	err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64")
	if err == nil || !strings.Contains(err.Error(), "changed during build") {
		t.Fatalf("Build = %v; want stale refusal", err)
	}
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err == nil {
		t.Fatal("sealed a failed build")
	}
}

func TestFailedRebuildInvalidatesEarlierPlatformBinding(t *testing.T) {
	root, declaration := bindingFixture(t)
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	declaration.BuildCommand = buildable.BuildCommand{Executable: "false"}
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err == nil {
		t.Fatal("expected build failure")
	}
	declaration.BuildCommand = buildable.BuildCommand{Executable: "true"}
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err == nil {
		t.Fatal("sealed using an earlier successful build record")
	}
}

func TestBuildBindingRejectsMixedPlatformInputsAndChangedOutput(t *testing.T) {
	root, declaration := bindingFixture(t)
	declaration.Platforms["macos-arm64"] = buildable.Platform{OS: []string{"darwin"}, Arch: []string{"arm64"}, Path: "bin/mac", Executable: true}
	write(t, filepath.Join(root, ".local-build/hello/bin/mac"), "#!/bin/sh\nexit 0\n", 0o755)
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "producer.txt"), "different platform source\n", 0o644)
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "macos-arm64"); err != nil {
		t.Fatal(err)
	}
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err == nil {
		t.Fatal("sealed mixed platform inputs")
	}
	write(t, filepath.Join(root, "producer.txt"), "producer\n", 0o644)
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "macos-arm64"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".local-build/hello/bin/mac"), "#!/bin/sh\nexit 1\n", 0o755)
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err == nil {
		t.Fatal("sealed changed output")
	}
	if err := os.Remove(filepath.Join(root, ".local-build/hello/bin/mac")); err != nil {
		t.Fatal(err)
	}
}

func TestBuildBindingSurvivesAssemblyOnlyForMatchingPlatforms(t *testing.T) {
	first, declaration := bindingFixture(t)
	second, _ := bindingFixture(t)
	declaration.Platforms["macos-arm64"] = buildable.Platform{OS: []string{"darwin"}, Arch: []string{"arm64"}, Path: "bin/mac", Executable: true}
	write(t, filepath.Join(second, ".local-build/hello/bin/mac"), "#!/bin/sh\nexit 0\n", 0o755)
	if err := buildable.BuildDeclared(context.Background(), first, "hello", declaration, "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	if err := buildable.BuildDeclared(context.Background(), second, "hello", declaration, "macos-arm64"); err != nil {
		t.Fatal(err)
	}
	platformDigest := sha256.Sum256([]byte("macos-arm64"))
	record := filepath.Join(".local-build/hello/.workbench-build-inputs", hex.EncodeToString(platformDigest[:])+".json")
	copyBindingFixture(t, filepath.Join(second, record), filepath.Join(first, record))
	copyBindingFixture(t, filepath.Join(second, ".local-build/hello/bin/mac"), filepath.Join(first, ".local-build/hello/bin/mac"))
	if err := buildable.SealDeclared(context.Background(), first, "hello", declaration, ".local-build/hello"); err != nil {
		t.Fatalf("matching assembled builds: %v", err)
	}
	write(t, filepath.Join(second, "producer.txt"), "other source\n", 0o644)
	if err := buildable.BuildDeclared(context.Background(), second, "hello", declaration, "macos-arm64"); err != nil {
		t.Fatal(err)
	}
	copyBindingFixture(t, filepath.Join(second, record), filepath.Join(first, record))
	if err := buildable.SealDeclared(context.Background(), first, "hello", declaration, ".local-build/hello"); err == nil {
		t.Fatal("sealed assembled platforms from different sources")
	}
}
