//go:build unix

package evaluate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func configureContextCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func contextOpenRooted(root *os.Root, relative string) (*os.File, error) {
	return root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func openContextMetadata(filename string) (*os.File, error) {
	file, err := os.OpenFile(filename, os.O_RDONLY|syscall.O_NONBLOCK, 0)
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

func killContextCommand(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func lockEvaluatorLease(ctx context.Context, filename string) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return nil, fmt.Errorf("create context evaluator lease directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("context evaluator lease is not a regular file")
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			released := false
			return func() error {
				if released {
					return nil
				}
				released = true
				unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				closeErr := file.Close()
				if unlockErr != nil {
					return unlockErr
				}
				return closeErr
			}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			_ = file.Close()
			return nil, err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func runContextWorker(spec ContextWorkerSpec) error {
	if spec.PklExecutable == "" || !filepath.IsAbs(spec.PklExecutable) {
		return fmt.Errorf("context worker requires an absolute Pkl executable")
	}
	limit := spec.MaxProcessDataBytes
	if limit == 0 {
		limit = contextDefaultDataBytes
	}
	if err := setContextDataLimit(limit); err != nil {
		return err
	}
	return syscall.Exec(spec.PklExecutable, []string{spec.PklExecutable, "server"}, os.Environ())
}
