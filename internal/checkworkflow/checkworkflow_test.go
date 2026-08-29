package checkworkflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/checkworkflow"
	"github.com/phosphorco/workbench-go/internal/setup"
)

func TestRunComposesSetupTypecheckAndTestInOrder(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "commands.log")
	bun := writeBun(t, root, `#!/bin/sh
printf '%s\n' "$*" >> "$WORKBENCH_CHECK_LOG"
`)
	t.Setenv("WORKBENCH_CHECK_LOG", log)

	wantSetup := setup.Result{ChangedPaths: []string{"package.json"}}
	setupCalled := false
	result, err := checkworkflow.Run(context.Background(), root, bun, func(_ context.Context, gotRoot string) (setup.Result, error) {
		setupCalled = true
		if gotRoot != root {
			t.Fatalf("setup root = %q, want %q", gotRoot, root)
		}
		return wantSetup, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !setupCalled {
		t.Fatal("setup was not called")
	}
	if !reflect.DeepEqual(result, wantSetup) {
		t.Fatalf("setup result = %#v, want %#v", result, wantSetup)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "run typecheck\nrun test\n"; got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
}

func TestRunStopsAtSetupFailureBeforeCodeHealth(t *testing.T) {
	root := t.TempDir()
	bun := writeBun(t, root, "#!/bin/sh\nexit 99\n")
	want := errors.New("checkout refused")
	_, err := checkworkflow.Run(context.Background(), root, bun, func(context.Context, string) (setup.Result, error) {
		return setup.Result{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("check error = %v, want setup cause", err)
	}
	var failure *checkworkflow.StepFailure
	if errors.As(err, &failure) {
		t.Fatalf("setup error was classified as code health: %v", err)
	}
}

func TestRunReturnsSetupSuccessWithTypedStepFailure(t *testing.T) {
	for _, test := range []struct {
		name         string
		failedStep   checkworkflow.Step
		wantCommands string
	}{
		{name: "typecheck", failedStep: checkworkflow.StepTypecheck, wantCommands: "run typecheck\n"},
		{name: "test", failedStep: checkworkflow.StepTest, wantCommands: "run typecheck\nrun test\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			log := filepath.Join(root, "commands.log")
			bun := writeBun(t, root, `#!/bin/sh
printf '%s\n' "$*" >> "$WORKBENCH_CHECK_LOG"
if [ "$2" = "$WORKBENCH_FAIL_STEP" ]; then
  printf 'diagnostic for %s\n' "$2"
  exit 17
fi
`)
			t.Setenv("WORKBENCH_CHECK_LOG", log)
			t.Setenv("WORKBENCH_FAIL_STEP", string(test.failedStep))
			wantSetup := setup.Result{ChangedPaths: []string{"tsconfig.json"}}
			result, err := checkworkflow.Run(context.Background(), root, bun, func(context.Context, string) (setup.Result, error) {
				return wantSetup, nil
			})
			if !reflect.DeepEqual(result, wantSetup) {
				t.Fatalf("setup result = %#v, want %#v", result, wantSetup)
			}
			var failure *checkworkflow.StepFailure
			if !errors.As(err, &failure) {
				t.Fatalf("check error = %T %v, want StepFailure", err, err)
			}
			if failure.Step != test.failedStep {
				t.Fatalf("failure step = %q, want %q", failure.Step, test.failedStep)
			}
			if !strings.Contains(failure.Error(), "diagnostic for "+string(test.failedStep)) {
				t.Fatalf("failure = %q, want command diagnostic", failure.Error())
			}
			contents, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if got := string(contents); got != test.wantCommands {
				t.Fatalf("commands = %q, want %q", got, test.wantCommands)
			}
		})
	}
}

func writeBun(t *testing.T, root, contents string) string {
	t.Helper()
	path := filepath.Join(root, "bun")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
