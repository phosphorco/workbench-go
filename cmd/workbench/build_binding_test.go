package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/evaluate"
)

func TestColdObservedBindingCLI(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("fixture declares Linux amd64")
	}
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	pkl, err = filepath.Abs(pkl)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	declaration := `amends "workbench-contract:/0.10.0/Repository.pkl"
buildables {
  ["hello"] = new Buildable {
    buildInputBinding = "observed-v1"
    inputDetection = new GitHeadTreeInputDetection { paths { "producer.txt"; "producer.sh" } }
    buildCommand = new BuildCommand { executable = "sh"; arguments { "producer.sh" } }
    manifest = new ManifestContract { schemaVersion = 1; kind = "hello-manifest"; contractId = "hello-v1" }
    candidates {
      new BuildableCandidate { root = ".local-build/hello"; inputStrategy = "gitWorktree"; invalidRemedy = "Rebuild it." }
      new BuildableCandidate { root = ".ci-build/hello"; inputStrategy = "gitHeadTree"; invalidRemedy = "Restore it." }
    }
    platforms {
      ["linux-x86_64"] = new BuildablePlatformOutput { os { "linux" }; arch { "amd64" }; path = "bin/hello" }
    }
  }
}
`
	producer := `#!/bin/sh
set -eu
value=$(cat producer.txt)
mkdir -p "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/bin"
printf '#!/bin/sh\nprintf "%%s\n" "%s"\n' "$value" > "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/bin/hello"
chmod +x "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/bin/hello"
printf '%s\n' '{"source":{},"capabilities":[]}' > "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/.workbench-buildable-source.json"
`
	writeHandlerFile(t, filepath.Join(root, "workbench.pkl"), []byte(declaration), 0o644)
	writeHandlerFile(t, filepath.Join(root, "producer.txt"), []byte("A\n"), 0o644)
	writeHandlerFile(t, filepath.Join(root, "producer.sh"), []byte(producer), 0o644)
	gitOutput(t, root, "init", "-q", "-b", "main")
	gitOutput(t, root, "config", "user.name", "Workbench Test")
	gitOutput(t, root, "config", "user.email", "workbench@example.invalid")
	gitOutput(t, root, "add", "workbench.pkl", "producer.txt", "producer.sh")
	gitOutput(t, root, "commit", "-qm", "fixture")
	application := applicationsForEnvironment(func() (commandEnvironment, error) { return commandEnvironment{evaluator: evaluator}, nil })
	invoke := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), args, func() (string, error) { return root, nil }, &out, io.Discard, application)
		return out.String(), err
	}
	build := []string{"buildable", "build", "--name", "hello", "--platform", "linux-x86_64"}
	seal := []string{"buildable", "seal", "--name", "hello", "--candidate-root", ".local-build/hello"}
	resolve := []string{"buildable", "resolve", "--name", "hello", "--platform", "linux-x86_64", "--format", "json"}
	if _, err := invoke(build...); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(seal...); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(resolve...); err != nil {
		t.Fatal(err)
	}
	runChild := func() (string, error) {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "-test.run=^TestColdObservedBindingRunChild$")
		cmd.Env = append(os.Environ(), "WORKBENCH_BINDING_CHILD_ROOT="+root, "WORKBENCH_BINDING_CHILD_PKL="+pkl)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	if output, err := runChild(); err != nil || output != "A\n" {
		t.Fatalf("cold run = %q, %v; want A", output, err)
	}
	writeHandlerFile(t, filepath.Join(root, "producer.txt"), []byte("B\n"), 0o644)
	if _, err := invoke(seal...); err == nil || !strings.Contains(err.Error(), "different producer inputs") {
		t.Fatalf("build A / seal B = %v", err)
	}
	if output, err := runChild(); err == nil || strings.Contains(output, "A\n") {
		t.Fatalf("run accepted changed source: %q %v", output, err)
	}
	writeHandlerFile(t, filepath.Join(root, "producer.txt"), []byte("A\n"), 0o644)
	outputPath := filepath.Join(root, ".local-build/hello/bin/hello")
	outputBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	writeHandlerFile(t, outputPath, []byte("#!/bin/sh\nprintf 'mutated\\n'\n"), 0o755)
	if _, err := invoke(resolve...); err == nil {
		t.Fatal("resolved changed output")
	}
	writeHandlerFile(t, outputPath, outputBytes, 0o755)
	digest := sha256.Sum256([]byte("linux-x86_64"))
	record := filepath.Join(root, ".local-build/hello/.workbench-build-inputs", hex.EncodeToString(digest[:])+".json")
	recordBytes, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	writeHandlerFile(t, record, []byte("{broken\n"), 0o644)
	if output, err := runChild(); err == nil || strings.Contains(output, "A\n") {
		t.Fatalf("run accepted broken receipt: %q %v", output, err)
	}
	writeHandlerFile(t, record, recordBytes, 0o644)
	if output, err := runChild(); err != nil || output != "A\n" {
		t.Fatalf("restored cold run = %q, %v; want A", output, err)
	}
}

func TestColdObservedBindingRunChild(t *testing.T) {
	root := os.Getenv("WORKBENCH_BINDING_CHILD_ROOT")
	if root == "" {
		return
	}
	evaluator, err := evaluate.NewEvaluator(os.Getenv("WORKBENCH_BINDING_CHILD_PKL"))
	if err != nil {
		t.Fatal(err)
	}
	application := applicationsForEnvironment(func() (commandEnvironment, error) { return commandEnvironment{evaluator: evaluator}, nil })
	if err := runWith(context.Background(), []string{"run", "hello", "--"}, func() (string, error) { return root, nil }, os.Stdout, os.Stderr, application); err != nil {
		t.Fatal(err)
	}
}

func TestColdHostLocalExecutionCLI(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("fixture declares Linux amd64")
	}
	pkl, err := exec.LookPath("pkl")
	if err != nil {
		t.Skip("pkl unavailable")
	}
	pkl, err = filepath.Abs(pkl)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	declaration := `amends "workbench-contract:/0.11.0/Repository.pkl"
buildables {
  ["hello"] = new Buildable {
    buildInputBinding = "observed-v1"
    localExecution = "host-observed-v1"
    inputDetection = new GitHeadTreeInputDetection { paths { "producer.txt"; "producer.sh" } }
    buildCommand = new BuildCommand { executable = "sh"; arguments { "producer.sh" } }
    manifest = new ManifestContract { schemaVersion = 1; kind = "hello-manifest"; contractId = "hello-v1" }
    candidates {
      new BuildableCandidate { root = ".local-build/hello"; inputStrategy = "gitWorktree"; invalidRemedy = "Rebuild it." }
      new BuildableCandidate { root = ".ci-build/hello"; inputStrategy = "gitHeadTree"; invalidRemedy = "Restore it." }
    }
    platforms {
      ["linux-x86_64"] = new BuildablePlatformOutput { os { "linux" }; arch { "amd64" }; path = "bin/hello" }
      ["macos-arm64"] = new BuildablePlatformOutput { os { "darwin" }; arch { "arm64" }; path = "bin/mac" }
    }
  }
}
`
	producer := `#!/bin/sh
set -eu
value=$(cat producer.txt)
mkdir -p "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/bin"
printf '#!/bin/sh\nprintf "%%s\n" "%s"\n' "$value" > "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/bin/hello"
chmod +x "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/bin/hello"
printf '%s\n' '{"source":{},"capabilities":[]}' > "$WORKBENCH_BUILDABLE_CANDIDATE_ROOT/.workbench-buildable-source.json"
`
	writeHandlerFile(t, filepath.Join(root, "workbench.pkl"), []byte(declaration), 0o644)
	writeHandlerFile(t, filepath.Join(root, "producer.txt"), []byte("A\n"), 0o644)
	writeHandlerFile(t, filepath.Join(root, "producer.sh"), []byte(producer), 0o644)
	gitOutput(t, root, "init", "-q", "-b", "main")
	gitOutput(t, root, "config", "user.name", "Workbench Test")
	gitOutput(t, root, "config", "user.email", "workbench@example.invalid")
	gitOutput(t, root, "add", "workbench.pkl", "producer.txt", "producer.sh")
	gitOutput(t, root, "commit", "-qm", "fixture")
	application := applicationsForEnvironment(func() (commandEnvironment, error) { return commandEnvironment{evaluator: evaluator}, nil })
	invoke := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runWith(context.Background(), args, func() (string, error) { return root, nil }, &out, io.Discard, application)
		return out.String(), err
	}
	if _, err := invoke("buildable", "build", "--name", "hello", "--platform", "linux-x86_64"); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke("buildable", "seal", "--name", "hello", "--candidate-root", ".local-build/hello"); err == nil {
		t.Fatal("full seal accepted missing macOS output")
	}
	output, err := invoke("buildable", "resolve", "--name", "hello", "--platform", "linux-x86_64", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result["localExecution"] != "host-observed-v1" {
		t.Fatalf("scope absent: %s", output)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestColdObservedBindingRunChild$")
	cmd.Env = append(os.Environ(), "WORKBENCH_BINDING_CHILD_ROOT="+root, "WORKBENCH_BINDING_CHILD_PKL="+pkl)
	value, err := cmd.CombinedOutput()
	if err != nil || string(value) != "A\n" {
		t.Fatalf("run = %q, %v", value, err)
	}
}
