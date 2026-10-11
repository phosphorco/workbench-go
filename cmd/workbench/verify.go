package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phosphorco/workbench-go/internal/verify/engine"
)

type verifyCommandFailure struct{ error }

func (e verifyCommandFailure) ExitCode() int { return 2 }

type verifyExit int

func (e verifyExit) Error() string { return fmt.Sprintf("verification exited with status %d", e) }
func (e verifyExit) ExitCode() int { return int(e) }

func runVerifyCommand(ctx context.Context, arguments []string, workingDirectory func() (string, error), output, diagnostics io.Writer) error {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	code, err := engine.Main(ctx, arguments, engine.IO{WorkingDirectory: workingDirectory, Env: env, Stdout: output, Stderr: diagnostics})
	if err != nil {
		return verifyCommandFailure{fmt.Errorf("verify: %w", err)}
	}
	if code != 0 {
		return reportedError{err: verifyExit(code)}
	}
	return nil
}
