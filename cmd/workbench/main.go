package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phosphorco/workbench-go/internal/setup"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getwd, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "workbench: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, workingDirectory func() (string, error), output io.Writer) error {
	if len(arguments) != 1 || arguments[0] != "setup" {
		return fmt.Errorf("usage: workbench setup")
	}
	root, err := workingDirectory()
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}
	result, err := setup.Run(ctx, root)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if _, err := fmt.Fprintf(output, "Workbench reconciled %d repositories; %d generated paths changed.\n", len(result.World.Resources), len(result.ChangedPaths)); err != nil {
		return fmt.Errorf("report setup result: %w", err)
	}
	return nil
}
