//go:build linux

package evaluate

import (
	"fmt"
	"syscall"
)

func setContextDataLimit(limit uint64) error {
	if limit == 0 || limit > uint64(^uint64(0)>>1) {
		return fmt.Errorf("context worker data limit is invalid")
	}
	resource := &syscall.Rlimit{Cur: limit, Max: limit}
	if err := syscall.Setrlimit(syscall.RLIMIT_DATA, resource); err != nil {
		return fmt.Errorf("set context worker data limit: %w", err)
	}
	return nil
}
