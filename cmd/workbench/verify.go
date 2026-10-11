package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	workbenchruntime "github.com/phosphorco/workbench-go/internal/runtime"
	"github.com/phosphorco/workbench-go/internal/verify/declaration"
	"github.com/phosphorco/workbench-go/internal/verify/engine"
	"github.com/phosphorco/workbench-go/internal/version"
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
	pkl, err := verifyPkl()
	if err != nil {
		return verifyCommandFailure{fmt.Errorf("verify: %w", err)}
	}
	code, err := engine.Main(ctx, arguments, engine.IO{WorkingDirectory: workingDirectory, Pkl: &pkl, Env: env, Stdout: output, Stderr: diagnostics})
	if err != nil {
		return verifyCommandFailure{fmt.Errorf("verify: %w", err)}
	}
	if code != 0 {
		return reportedError{err: verifyExit(code)}
	}
	return nil
}

// verifyPkl grants verification the same Pkl the other commands use: the
// bundled runtime beside a released executable, PATH in development.
func verifyPkl() (declaration.Pkl, error) {
	if version.IsDevelopment() {
		return declaration.AmbientPkl(), nil
	}
	toolchain, err := workbenchruntime.Installed()
	if err != nil {
		return declaration.Pkl{}, err
	}
	return declaration.NewPkl(toolchain.PklPath())
}
