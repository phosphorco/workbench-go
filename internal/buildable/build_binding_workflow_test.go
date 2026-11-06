package buildable_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/buildable"
	"gopkg.in/yaml.v3"
)

func assemblyPrefix(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/workbench-buildable-promotion.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string } `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, step := range workflow.Jobs["assemble-and-verify"].Steps {
		if step.Name != "Assemble, seal, and verify the exact candidate" {
			continue
		}
		boundary := strings.Index(step.Run, "\nmise exec \"github:phosphorco/workbench-go@${WORKBENCH_VERSION}\" --")
		if boundary < 0 {
			t.Fatal("assembly-to-seal boundary missing")
		}
		return step.Run[:boundary]
	}
	t.Fatal("assembly step missing")
	return ""
}

func TestBuildBindingWorkflowAssemblyPrefix(t *testing.T) {
	prefix := assemblyPrefix(t)
	for _, tc := range []struct {
		name                       string
		mismatch, collision, mixed bool
	}{
		{name: "matching"},
		{name: "source descriptor disagreement", mismatch: true},
		{name: "colliding payload", collision: true},
		{name: "different build inputs", mixed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, declaration := bindingFixture(t)
			second, _ := bindingFixture(t)
			declaration.Platforms["macos-arm64"] = buildable.Platform{OS: []string{"darwin"}, Arch: []string{"arm64"}, Path: "bin/mac", Executable: true}
			write(t, filepath.Join(second, ".local-build/hello/bin/mac"), "#!/bin/sh\nexit 0\n", 0o755)
			if err := buildable.BuildDeclared(context.Background(), first, "hello", declaration, "linux-x86_64"); err != nil {
				t.Fatal(err)
			}
			if tc.mixed {
				write(t, filepath.Join(second, "producer.txt"), "other source\n", 0o644)
			}
			if err := buildable.BuildDeclared(context.Background(), second, "hello", declaration, "macos-arm64"); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(second, ".local-build/hello/bin/hello")); err != nil {
				t.Fatal(err)
			}
			if tc.mismatch {
				write(t, filepath.Join(second, ".local-build/hello/.workbench-buildable-source.json"), "different source\n", 0o644)
			}
			if tc.collision {
				for _, root := range []string{first, second} {
					write(t, filepath.Join(root, ".local-build/hello/bin/collision"), "same bytes\n", 0o644)
				}
			}
			_, assembled := fixtureRepository(t)
			if err := os.Mkdir(filepath.Join(assembled, "artifacts"), 0o755); err != nil {
				t.Fatal(err)
			}
			for index, root := range []string{first, second} {
				archive := filepath.Join(assembled, "artifacts", []string{"linux.tar", "mac.tar"}[index])
				cmd := exec.Command("tar", "-cf", archive, "-C", root, ".local-build/hello")
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("archive: %v\n%s", err, output)
				}
			}
			cmd := exec.Command("bash", "-c", prefix)
			cmd.Dir = assembled
			cmd.Env = append(os.Environ(), "BUILDABLE=hello", "WORKBENCH_VERSION=0.10.0", "RUNNER_TEMP="+t.TempDir())
			output, err := cmd.CombinedOutput()
			if tc.mismatch || tc.collision {
				if err == nil {
					t.Fatalf("assembly accepted %s", tc.name)
				}
				want := "disagree on canonical source bytes"
				if tc.collision {
					want = "payloads collide"
				}
				if !strings.Contains(string(output), want) {
					t.Fatalf("assembly refusal: %v\n%s", err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("assembly prefix: %v\n%s", err, output)
			}
			err = buildable.SealDeclared(context.Background(), assembled, "hello", declaration, ".local-build/hello")
			if tc.mixed {
				if err == nil {
					t.Fatal("sealed assembled platforms from different producer inputs")
				}
				return
			}
			if err != nil {
				t.Fatalf("seal matching assembled candidate: %v", err)
			}
			if _, err := buildable.ResolveDeclared(context.Background(), assembled, "hello", declaration, "linux", "amd64"); err != nil {
				t.Fatalf("resolve assembled candidate: %v", err)
			}
		})
	}
}
