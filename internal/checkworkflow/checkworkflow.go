// Package checkworkflow composes Workbench setup with the generated root
// code-health scripts.
package checkworkflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/phosphorco/workbench-go/internal/setup"
)

// Step identifies one generated root code-health script.
type Step string

const (
	StepTypecheck Step = "typecheck"
	StepTest      Step = "test"
)

// StepFailure preserves which code-health step failed and its process cause.
// A StepFailure means setup completed successfully before code health failed.
type StepFailure struct {
	Step   Step
	Output string
	cause  error
}

func (failure *StepFailure) Error() string {
	if failure.Output == "" {
		return fmt.Sprintf("%s failed: %v", failure.Step, failure.cause)
	}
	return fmt.Sprintf("%s failed: %v: %s", failure.Step, failure.cause, failure.Output)
}

func (failure *StepFailure) Unwrap() error {
	return failure.cause
}

// Run reconciles the Workbench, then runs its generated typecheck and test
// scripts with the exact Bun executable supplied by the composition root.
// The returned setup result remains meaningful when err is a StepFailure.
func Run(
	ctx context.Context,
	root string,
	bun string,
	setupApplication func(context.Context, string) (setup.Result, error),
) (setup.Result, error) {
	result, err := setupApplication(ctx, root)
	if err != nil {
		return setup.Result{}, fmt.Errorf("setup: %w", err)
	}
	for _, step := range []Step{StepTypecheck, StepTest} {
		command := exec.CommandContext(ctx, bun, "run", string(step))
		command.Dir = root
		command.Env = os.Environ()
		output, commandErr := command.CombinedOutput()
		if commandErr != nil {
			return result, &StepFailure{
				Step:   step,
				Output: strings.TrimSpace(string(output)),
				cause:  commandErr,
			}
		}
	}
	return result, nil
}
