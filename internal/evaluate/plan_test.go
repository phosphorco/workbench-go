package evaluate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/evaluate"
)

func planEvaluator(t *testing.T) evaluate.Evaluator {
	t.Helper()
	executable, err := exec.LookPath("pkl")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := evaluate.NewEvaluator(executable)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func writePlanModule(t *testing.T, filename, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPlanEvaluationComposesOnlyRootedModules(t *testing.T) {
	runtime := planEvaluator(t)
	root := t.TempDir()
	fragment := filepath.Join(root, "fragments", "title.pkl")
	writePlanModule(t, fragment, `title = "Rooted composition"`)
	source := `amends "workbench:plan"
import "../fragments/title.pkl" as fragment
meta { title = fragment.title; goal = "Use explicit local fragments." }
nodes { new Guard { id = "proof"; owner = "testing"; oracle = module.mechanical("true") } }
`
	filename := filepath.Join(root, "plans", "test.plan.pkl")
	writePlanModule(t, filename, source)
	definition, err := runtime.EvaluatePlan(context.Background(), filename, root)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Meta.Title != "Rooted composition" || len(definition.Nodes) != 1 {
		t.Fatalf("definition: %#v", definition)
	}

	outside := filepath.Join(t.TempDir(), "outside.pkl")
	writePlanModule(t, outside, `title = "Outside authority"`)
	if err := os.Remove(fragment); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, fragment); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.EvaluatePlan(context.Background(), filename, root); err == nil {
		t.Fatal("import followed a symlink outside the root")
	}
	if _, err := runtime.EvaluatePlan(context.Background(), outside, root); err == nil {
		t.Fatal("accepted an entrypoint outside the root")
	}
}

func TestPlanEvaluationDeniesAmbientReadsAndImports(t *testing.T) {
	runtime := planEvaluator(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.pkl")
	writePlanModule(t, outside, `title = "Outside authority"`)
	for _, test := range []struct{ name, imports, title string }{
		{"environment", "", `read("env:HOME")`},
		{"file resource", "", fmt.Sprintf(`read(%q).text`, "file:"+outside)},
		{"network resource", "", `read("https://example.com/secret").text`},
		{"file module", fmt.Sprintf("import %q", "file:"+outside), "outside.title"},
		{"network module", `import "https://example.com/secret.pkl"`, "secret.title"},
		{"traversal", `import "../outside.pkl"`, "outside.title"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := "amends \"workbench:plan\"\n" + test.imports + "\nmeta { title = " + test.title + "; goal = \"Deny ambient authority.\" }\nnodes {}\n"
			filename := filepath.Join(root, "test.plan.pkl")
			writePlanModule(t, filename, source)
			if _, err := runtime.EvaluatePlan(context.Background(), filename, root); err == nil {
				t.Fatal("ambient access succeeded")
			}
		})
	}
}

func TestPlanEvaluationPreservesCancellation(t *testing.T) {
	runtime := planEvaluator(t)
	root := t.TempDir()
	filename := filepath.Join(root, "test.plan.pkl")
	writePlanModule(t, filename, "amends \"workbench:plan\"\nmeta { title = \"Cancel\"; goal = \"Cancel\" }\nnodes {}")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.EvaluatePlan(ctx, filename, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity lost: %v", err)
	}
}

func TestPlanEvaluationsHaveIndependentLifetimes(t *testing.T) {
	runtime := planEvaluator(t)
	root := t.TempDir()
	filename := filepath.Join(root, "test.plan.pkl")
	writePlanModule(t, filename, "amends \"workbench:plan\"\nmeta { title = \"Independent\"; goal = \"Independent\" }\nnodes {}")
	var wait sync.WaitGroup
	for range 4 {
		wait.Go(func() {
			definition, err := runtime.EvaluatePlan(context.Background(), filename, root)
			if err != nil {
				t.Error(err)
				return
			}
			if definition.Meta.Title != "Independent" {
				t.Errorf("wrong definition: %#v", definition)
			}
		})
	}
	wait.Wait()
}

func TestPlanEvaluationDeadlineStopsEvaluation(t *testing.T) {
	runtime := planEvaluator(t)
	root := t.TempDir()
	filename := filepath.Join(root, "test.plan.pkl")
	writePlanModule(t, filename, `amends "workbench:plan"
local function fibonacci(n: Int): Int = if (n < 2) n else fibonacci(n - 1) + fibonacci(n - 2)
meta { title = fibonacci(45).toString(); goal = "An interrupted evaluation is not a plan." }
nodes {}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := runtime.EvaluatePlan(ctx, filename, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline identity lost: %v", err)
	}
}
