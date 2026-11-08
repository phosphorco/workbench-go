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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/buildable"
)

// Decode the future opt-in through the existing public projection shape: the
// current decoder ignores it, so the first positive is a behavioral RED rather
// than a compilation failure on an as-yet nonexistent Go field.
func hostLocalFixture(t *testing.T) (string, buildable.Buildable) {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("fixture executes linux amd64")
	}
	_, root := fixtureRepository(t)
	declaration := helloDeclaration()
	declaration.BuildCommand = buildable.BuildCommand{Executable: "true"}
	policy := "observed-v1"
	declaration.BuildInputBinding = &policy
	declaration.Platforms["macos-arm64"] = buildable.Platform{OS: []string{"darwin"}, Arch: []string{"arm64"}, Path: "bin/mac", Executable: true}
	raw, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	obj["localExecution"] = json.RawMessage(`"host-observed-v1"`)
	raw, err = json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &declaration); err != nil {
		t.Fatal(err)
	}
	writeUnsealedCandidate(t, root, ".local-build/hello", "host")
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	return root, declaration
}

func hostResolve(t *testing.T, root string, declaration buildable.Buildable) buildable.Resolution {
	t.Helper()
	got, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64")
	if err != nil {
		t.Fatalf("host-local resolution: %v", err)
	}
	return got
}

func TestHostLocalObservedExecutionAndCoverage(t *testing.T) {
	root, declaration := hostLocalFixture(t)
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err == nil {
		t.Fatal("full seal accepted missing macOS output")
	}
	result := hostResolve(t, root, declaration)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["localExecution"] != "host-observed-v1" {
		t.Fatalf("host-local coverage absent: %s", raw)
	}
	output, err := exec.Command(result.Path).Output()
	if err != nil || string(output) != "host\n" {
		t.Fatalf("real selected output = %q, %v", output, err)
	}
	if result.Candidate != "local" || result.Platform != "linux-x86_64" || !containsString(result.Capabilities, "greeting-v1") {
		t.Fatalf("resolution = %#v", result)
	}
}

func TestHostLocalRejectsDescriptorFIFOWithoutBlocking(t *testing.T) {
	if root := os.Getenv("WORKBENCH_FIFO_TEST_ROOT"); root != "" {
		var declaration buildable.Buildable
		if err := json.Unmarshal([]byte(os.Getenv("WORKBENCH_FIFO_TEST_DECLARATION")), &declaration); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "fifo-admission-started")
		_, err := buildable.ResolveDeclaredPlatform(context.Background(), root, "hello", declaration, "linux-x86_64")
		var refusal *buildable.Refusal
		if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid || !strings.Contains(refusal.Reason, "regular file") {
			t.Fatalf("FIFO refusal = %v", err)
		}
		return
	}
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo unavailable")
	}
	root, declaration := hostLocalFixture(t)
	hostResolve(t, root, declaration)
	descriptor := filepath.Join(root, ".local-build/hello", buildable.SourceDescriptorFilename)
	if err := os.Remove(descriptor); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(mkfifo, descriptor).CombinedOutput(); err != nil {
		t.Fatalf("create FIFO: %s: %v", output, err)
	}
	info, err := os.Lstat(descriptor)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("descriptor is not a FIFO: %v, %v", info, err)
	}
	encoded, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestHostLocalRejectsDescriptorFIFOWithoutBlocking$")
	command.Env = append(os.Environ(), "WORKBENCH_FIFO_TEST_ROOT="+root, "WORKBENCH_FIFO_TEST_DECLARATION="+string(encoded))
	output, err := command.CombinedOutput()
	if !strings.Contains(string(output), "fifo-admission-started") {
		t.Fatalf("child did not reach FIFO admission: %q, %v", output, err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("FIFO admission blocked until child timeout: %q", output)
	}
	if err != nil {
		t.Fatalf("FIFO admission did not refuse correctly: %q, %v", output, err)
	}
}

func TestOutputFIFORefusesWithoutBlocking(t *testing.T) {
	if root := os.Getenv("WORKBENCH_OUTPUT_FIFO_ROOT"); root != "" {
		var declaration buildable.Buildable
		if err := json.Unmarshal([]byte(os.Getenv("WORKBENCH_OUTPUT_FIFO_DECLARATION")), &declaration); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "output-fifo-admission-started")
		var err error
		if os.Getenv("WORKBENCH_OUTPUT_FIFO_MODE") != "legacy" {
			_, err = buildable.ResolveDeclaredPlatform(context.Background(), root, "hello", declaration, "linux-x86_64")
		} else {
			err = buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello")
		}
		var refusal *buildable.Refusal
		if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid || !strings.Contains(refusal.Reason, "regular file") {
			t.Fatalf("output FIFO refusal = %v", err)
		}
		return
	}
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo unavailable")
	}
	for _, mode := range []string{"host", "legacy", "sealed"} {
		t.Run(mode, func(t *testing.T) {
			var root string
			var declaration buildable.Buildable
			if mode == "host" {
				root, declaration = hostLocalFixture(t)
				hostResolve(t, root, declaration)
			} else {
				_, root = fixtureRepository(t)
				declaration = helloDeclaration()
				declaration.BuildCommand = buildable.BuildCommand{Executable: "true"}
				writeUnsealedCandidate(t, root, ".local-build/hello", "legacy")
				if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
					t.Fatalf("valid legacy build: %v", err)
				}
				if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err != nil {
					t.Fatalf("valid legacy seal: %v", err)
				}
			}
			candidate := filepath.Join(root, ".local-build/hello")
			protected := []string{filepath.Join(root, "producer.txt"), filepath.Join(candidate, buildable.SourceDescriptorFilename)}
			if mode == "host" {
				digest := sha256.Sum256([]byte("linux-x86_64"))
				protected = append(protected, filepath.Join(candidate, ".workbench-build-inputs", hex.EncodeToString(digest[:])+".json"))
			} else {
				protected = append(protected, filepath.Join(candidate, "manifest.json"))
			}
			before := make(map[string][32]byte, len(protected))
			for _, path := range protected {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = sha256.Sum256(data)
			}
			outputPath := filepath.Join(candidate, "bin/hello")
			if err := os.Remove(outputPath); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(mkfifo, outputPath).CombinedOutput(); err != nil {
				t.Fatalf("create output FIFO: %s: %v", output, err)
			}
			info, err := os.Lstat(outputPath)
			if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("output is not a FIFO: %v, %v", info, err)
			}
			encoded, err := json.Marshal(declaration)
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestOutputFIFORefusesWithoutBlocking$")
			command.Env = append(os.Environ(), "WORKBENCH_OUTPUT_FIFO_ROOT="+root, "WORKBENCH_OUTPUT_FIFO_DECLARATION="+string(encoded), "WORKBENCH_OUTPUT_FIFO_MODE="+mode)
			output, err := command.CombinedOutput()
			if !strings.Contains(string(output), "output-fifo-admission-started") {
				t.Fatalf("child did not reach output admission: %q, %v", output, err)
			}
			for _, path := range protected {
				data, readErr := os.ReadFile(path)
				if readErr != nil || sha256.Sum256(data) != before[path] {
					t.Fatalf("protected premise changed %q: %v", path, readErr)
				}
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("output FIFO admission blocked until child timeout: %q", output)
			}
			if err != nil {
				t.Fatalf("output FIFO did not refuse correctly: %q, %v", output, err)
			}
		})
	}
}

func TestDescriptorSymlinkCompatibility(t *testing.T) {
	t.Run("legacy confined descriptor remains valid", func(t *testing.T) {
		_, root := fixtureRepository(t)
		declaration := helloDeclaration()
		declaration.BuildCommand = buildable.BuildCommand{Executable: "true"}
		writeUnsealedCandidate(t, root, ".local-build/hello", "legacy")
		candidate := filepath.Join(root, ".local-build/hello")
		descriptor := filepath.Join(candidate, buildable.SourceDescriptorFilename)
		if err := os.Rename(descriptor, filepath.Join(candidate, "source.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("source.json", descriptor); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
			t.Fatalf("legacy symlink build: %v", err)
		}
		if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err != nil {
			t.Fatalf("legacy symlink seal: %v", err)
		}
	})
	t.Run("host-local descriptor symlink refuses", func(t *testing.T) {
		root, declaration := hostLocalFixture(t)
		hostResolve(t, root, declaration)
		candidate := filepath.Join(root, ".local-build/hello")
		descriptor := filepath.Join(candidate, buildable.SourceDescriptorFilename)
		if err := os.Rename(descriptor, filepath.Join(candidate, "source.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("source.json", descriptor); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		_, err := buildable.ResolveDeclaredPlatform(context.Background(), root, "hello", declaration, "linux-x86_64")
		var refusal *buildable.Refusal
		if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid || !strings.Contains(refusal.Reason, "regular file") {
			t.Fatalf("host-local symlink refusal = %v", err)
		}
	})
}

func TestHostLocalRefusesAlterations(t *testing.T) {
	for _, change := range []string{"input", "output", "capability", "descriptor-changed", "wrong-identity", "missing-descriptor-digest", "missing-binding", "malformed-binding", "symlink-binding"} {
		t.Run(change, func(t *testing.T) {
			root, declaration := hostLocalFixture(t)
			hostResolve(t, root, declaration)
			candidate := filepath.Join(root, ".local-build/hello")
			digest := sha256.Sum256([]byte("linux-x86_64"))
			binding := filepath.Join(candidate, ".workbench-build-inputs", hex.EncodeToString(digest[:])+".json")
			switch change {
			case "input":
				write(t, filepath.Join(root, "producer.txt"), "changed\n", 0o644)
			case "output":
				write(t, filepath.Join(candidate, "bin/hello"), "#!/bin/sh\necho wrong\n", 0o755)
			case "capability":
				write(t, filepath.Join(candidate, buildable.SourceDescriptorFilename), `{"source":{},"capabilities":[]}`+"\n", 0o644)
			case "descriptor-changed":
				descriptorPath := filepath.Join(candidate, buildable.SourceDescriptorFilename)
				data, err := os.ReadFile(descriptorPath)
				if err != nil {
					t.Fatal(err)
				}
				var descriptor map[string]any
				if err := json.Unmarshal(data, &descriptor); err != nil {
					t.Fatal(err)
				}
				descriptor["source"].(map[string]any)["version"] = "different-but-valid"
				changed, err := json.Marshal(descriptor)
				if err != nil {
					t.Fatal(err)
				}
				write(t, descriptorPath, string(changed), 0o644)
			case "missing-descriptor-digest":
				data, err := os.ReadFile(binding)
				if err != nil {
					t.Fatal(err)
				}
				var receipt map[string]any
				if err := json.Unmarshal(data, &receipt); err != nil {
					t.Fatal(err)
				}
				delete(receipt, "sourceDigest")
				changed, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				write(t, binding, string(changed), 0o644)
			case "wrong-identity":
				data, err := os.ReadFile(binding)
				if err != nil {
					t.Fatal(err)
				}
				var receipt map[string]any
				if err := json.Unmarshal(data, &receipt); err != nil {
					t.Fatal(err)
				}
				receipt["declarationIdentity"] = strings.Repeat("0", 64)
				changed, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				write(t, binding, string(changed), 0o644)
			case "missing-binding":
				if err := os.Remove(binding); err != nil {
					t.Fatal(err)
				}
			case "malformed-binding":
				write(t, binding, "{bad\n", 0o644)
			case "symlink-binding":
				if err := os.Remove(binding); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "producer.txt"), binding); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err == nil {
				t.Fatalf("accepted %s", change)
			}
		})
	}
}

func TestHostLocalFailClosedThenExplicitQuarantineAndDetachedCI(t *testing.T) {
	root, declaration := hostLocalFixture(t)
	write(t, filepath.Join(root, ".local-build/hello/bin/mac"), "#!/bin/sh\necho mac\n", 0o755)
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "macos-arm64"); err != nil {
		t.Fatal(err)
	}
	if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".ci-build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, ".local-build/hello"), filepath.Join(root, ".ci-build/hello")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "checkout", "--detach", "HEAD")
	write(t, filepath.Join(root, ".local-build/hello/invalid"), "preserve me", 0o644)
	if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err == nil {
		t.Fatal("invalid local silently fell through")
	}
	quarantine := filepath.Join(root, "quarantined-hello")
	if err := os.Rename(filepath.Join(root, ".local-build/hello"), quarantine); err != nil {
		t.Fatal(err)
	}
	if got := hostResolve(t, root, declaration); got.Candidate != "committed" {
		t.Fatalf("candidate after quarantine = %q", got.Candidate)
	}
	if got, err := os.ReadFile(filepath.Join(quarantine, "invalid")); err != nil || !strings.Contains(string(got), "preserve me") {
		t.Fatalf("quarantine contents %q %v", got, err)
	}
	if err := os.Rename(quarantine, filepath.Join(root, ".local-build/hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err == nil {
		t.Fatal("restored invalid local silently fell through")
	}
}

func TestHostLocalFailedRebuildAndInvalidManifestRefuse(t *testing.T) {
	root, declaration := hostLocalFixture(t)
	hostResolve(t, root, declaration)
	declaration.BuildCommand = buildable.BuildCommand{Executable: "false"}
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err == nil {
		t.Fatal("failed rebuild succeeded")
	}
	declaration.BuildCommand = buildable.BuildCommand{Executable: "true"}
	if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err == nil {
		t.Fatal("failed rebuild reused old binding")
	}
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	hostResolve(t, root, declaration)
	write(t, filepath.Join(root, ".local-build/hello/manifest.json"), "{bad\n", 0o644)
	if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err == nil {
		t.Fatal("invalid full manifest was bypassed")
	}
}

func TestHostLocalAcceptsStableDirtyAndRejectsChangedUntrackedInput(t *testing.T) {
	root, declaration := hostLocalFixture(t)
	declaration.InputDetection.Paths = []string{"producer.txt", "producer-inputs"}
	write(t, filepath.Join(root, "producer.txt"), "dirty\n", 0o644)
	write(t, filepath.Join(root, "producer-inputs/new.txt"), "first\n", 0o644)
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	hostResolve(t, root, declaration)
	write(t, filepath.Join(root, "producer-inputs/new.txt"), "second\n", 0o644)
	if _, err := buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "linux", "amd64"); err == nil {
		t.Fatal("accepted changed untracked input")
	}
}

func TestHostLocalRequiresObservedBuildBinding(t *testing.T) {
	_, declaration := hostLocalFixture(t)
	raw, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "buildInputBinding")
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var unbound buildable.Buildable
	if err := json.Unmarshal(raw, &unbound); err != nil {
		t.Fatal(err)
	}
	if err := unbound.Validate(); err == nil {
		t.Fatal("host-local mode accepted without observed binding")
	}
}

func TestHostLocalProjectionKeepsLegacyDigestAndRefusesMismatchedScope(t *testing.T) {
	workbench, root := fixtureRepository(t)
	legacy, err := os.ReadFile(filepath.Join(workbench, buildable.ProjectionPath))
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err := json.Unmarshal(legacy, &old); err != nil {
		t.Fatal(err)
	}
	if old["schemaDigest"] != "cf600469e3207b765366bf64d028a9056b3c9b1c8c9268fe23a6efc2e87c26ac" {
		t.Fatalf("legacy projection digest changed: %s", legacy)
	}
	_, declaration := hostLocalFixture(t)
	projectFixture(t, workbench, root, declaration)
	path := filepath.Join(workbench, buildable.ProjectionPath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var projection map[string]any
	if err := json.Unmarshal(data, &projection); err != nil {
		t.Fatal(err)
	}
	if projection["schemaDigest"] == old["schemaDigest"] {
		t.Fatal("host-local scope retained legacy schema digest")
	}
	projection["schemaDigest"] = old["schemaDigest"]
	forged, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(forged), 0o644)
	if _, err := buildable.Resolve(context.Background(), workbench, "hello", "linux", "amd64"); err == nil {
		t.Fatal("legacy projection accepted host-local scope")
	}
}

// This fixture is a synthetic foreign-platform record; it does not execute macOS code.
func TestHostLocalWrongHostRejectsValidForeignFragment(t *testing.T) {
	root, declaration := hostLocalFixture(t)
	candidate := filepath.Join(root, ".local-build/hello")
	foreignOutput := filepath.Join(candidate, "bin/mac")
	write(t, foreignOutput, "#!/bin/sh\necho mac-fixture\n", 0o755)
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "macos-arm64"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(candidate, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected full manifest: %v", err)
	}
	digest := sha256.Sum256([]byte("macos-arm64"))
	record := filepath.Join(candidate, ".workbench-build-inputs", hex.EncodeToString(digest[:])+".json")
	beforeRecord, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	beforeOutput, err := os.ReadFile(foreignOutput)
	if err != nil {
		t.Fatal(err)
	}
	var binding struct {
		Platform            string `json:"platform"`
		DeclarationIdentity string `json:"declarationIdentity"`
		ProducerDigest      string `json:"producerDigest"`
		SourceDigest        string `json:"sourceDigest"`
		Outputs             []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(beforeRecord, &binding); err != nil {
		t.Fatal(err)
	}
	outputHash := sha256.Sum256(beforeOutput)
	if binding.Platform != "macos-arm64" || binding.DeclarationIdentity == "" || binding.ProducerDigest == "" || binding.SourceDigest == "" || len(binding.Outputs) != 1 || binding.Outputs[0].Path != "bin/mac" || binding.Outputs[0].SHA256 != hex.EncodeToString(outputHash[:]) {
		t.Fatalf("foreign fixture is not a valid observed fragment: %s", beforeRecord)
	}
	_, err = buildable.ResolveDeclared(context.Background(), root, "hello", declaration, "darwin", "arm64")
	if err == nil || !strings.Contains(err.Error(), "only valid for the current host platform") {
		t.Fatalf("foreign host refusal = %v", err)
	}
	afterRecord, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	afterOutput, err := os.ReadFile(foreignOutput)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRecord) != string(beforeRecord) || string(afterOutput) != string(beforeOutput) {
		t.Fatal("foreign host refusal changed fixture bytes")
	}
}

func TestHostLocalExplicitSecondHostPlatform(t *testing.T) {
	root, declaration := hostLocalFixture(t)
	alias := declaration.Platforms["linux-x86_64"]
	alias.Path = "bin/alias"
	declaration.Platforms["z-linux-alias"] = alias
	declaration.BuildCommand = buildable.BuildCommand{Executable: "sh", Arguments: []string{"-c", `printf '#!/bin/sh\necho host\n' > "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/bin/alias"; chmod +x "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/bin/alias"`}}
	if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "z-linux-alias"); err != nil {
		t.Fatalf("build selected host alias: %v", err)
	}
	result, err := buildable.ResolveDeclaredPlatform(context.Background(), root, "hello", declaration, "z-linux-alias")
	if err != nil {
		t.Fatalf("resolve selected host alias: %v", err)
	}
	if result.Platform != "z-linux-alias" || result.Path == "" || result.LocalExecution != "host-observed-v1" {
		t.Fatalf("selected host alias result = %#v", result)
	}
	if output, err := exec.Command(result.Path).Output(); err != nil || string(output) != "host\n" {
		t.Fatalf("selected host alias output = %q, %v", output, err)
	}
	_, err = buildable.ResolveDeclaredPlatform(context.Background(), root, "hello", declaration, "macos-arm64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid {
		t.Fatalf("foreign platform refusal = %v", err)
	}
}

func TestSealedHostLocalManifestMetadataMatchesBoundDescriptor(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("fixture executes linux amd64")
	}
	for _, mode := range []string{"host", "observed", "unbound"} {
		for _, field := range []string{"source", "capability"} {
			t.Run(mode+"/"+field, func(t *testing.T) {
				_, root := fixtureRepository(t)
				declaration := helloDeclaration()
				declaration.BuildCommand = buildable.BuildCommand{Executable: "true"}
				if mode != "unbound" {
					binding := "observed-v1"
					declaration.BuildInputBinding = &binding
				}
				if mode == "host" {
					local := "host-observed-v1"
					declaration.LocalExecution = &local
				}
				writeUnsealedCandidate(t, root, ".local-build/hello", "sealed")
				if err := buildable.BuildDeclared(context.Background(), root, "hello", declaration, "linux-x86_64"); err != nil {
					t.Fatalf("build: %v", err)
				}
				if err := buildable.SealDeclared(context.Background(), root, "hello", declaration, ".local-build/hello"); err != nil {
					t.Fatalf("seal: %v", err)
				}
				before, err := buildable.ResolveDeclaredPlatform(context.Background(), root, "hello", declaration, "linux-x86_64")
				if err != nil || before.Source["version"] != "1.0.0" || !containsString(before.Capabilities, "greeting-v1") {
					t.Fatalf("valid sealed control = %#v, %v", before, err)
				}
				candidate := filepath.Join(root, ".local-build/hello")
				protected := []string{filepath.Join(root, "producer.txt"), filepath.Join(candidate, "bin/hello"), filepath.Join(candidate, buildable.SourceDescriptorFilename)}
				if mode != "unbound" {
					digest := sha256.Sum256([]byte("linux-x86_64"))
					protected = append(protected, filepath.Join(candidate, ".workbench-build-inputs", hex.EncodeToString(digest[:])+".json"))
				}
				unchanged := make(map[string]string)
				for _, path := range protected {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					unchanged[path] = string(data)
				}
				manifestPath := filepath.Join(candidate, "manifest.json")
				data, err := os.ReadFile(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				var manifest map[string]any
				if err := json.Unmarshal(data, &manifest); err != nil {
					t.Fatal(err)
				}
				if field == "source" {
					manifest["source"].(map[string]any)["version"] = "2.0.0"
				} else {
					manifest["capabilities"] = append(manifest["capabilities"].([]any), "optional-extra")
				}
				data, err = json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				write(t, manifestPath, string(data)+"\n", 0o644)
				for path, want := range unchanged {
					got, err := os.ReadFile(path)
					if err != nil || string(got) != want {
						t.Fatalf("protected input changed: %s: %v", path, err)
					}
				}
				got, err := buildable.ResolveDeclaredPlatform(context.Background(), root, "hello", declaration, "linux-x86_64")
				if mode == "host" {
					var refusal *buildable.Refusal
					if !errors.As(err, &refusal) || refusal.Code != buildable.RefusalCandidateInvalid {
						t.Fatalf("altered sealed %s accepted: result=%#v error=%v", field, got, err)
					}
				} else if err != nil {
					t.Fatalf("legacy %s metadata should remain admitted: %v", mode, err)
				} else if field == "source" && got.Source["version"] != "2.0.0" || field == "capability" && !containsString(got.Capabilities, "optional-extra") {
					t.Fatalf("legacy result did not retain changed metadata: %#v", got)
				}
			})
		}
	}
}
