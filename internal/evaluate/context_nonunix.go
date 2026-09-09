//go:build !unix

package evaluate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func configureContextCommand(command *exec.Cmd) {}

func killContextCommand(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}

func lockEvaluatorLease(ctx context.Context, filename string) (func() error, error) {
	return nil, fmt.Errorf("context evaluator lease is unsupported on this platform")
}

func contextOpenRooted(root *os.Root, relative string) (*os.File, error) {
	return root.Open(relative)
}

func openContextMetadata(filename string) (*os.File, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("context runtime lock is not a regular file")
	}
	return file, nil
}

func runContextWorker(spec ContextWorkerSpec) error {
	return fmt.Errorf("context worker is unsupported on this platform")
}
