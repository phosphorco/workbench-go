//go:build darwin

package evaluate

import (
	"fmt"
	"syscall"
)

// setContextDataLimit is a best-effort Darwin worker limit. RLIMIT_DATA only
// covers the process data segment; it is not a complete native heap or RSS
// bound. The worker still relies on finite input/output/deadline limits and
// joined process cleanup to bound work and lifetime.
func setContextDataLimit(limit uint64) error {
	if limit == 0 || limit > uint64(^uint64(0)>>1) {
		return fmt.Errorf("context worker data limit is invalid")
	}
	resource := &syscall.Rlimit{Cur: limit, Max: limit}
	if err := syscall.Setrlimit(syscall.RLIMIT_DATA, resource); err != nil {
		// Darwin may reject RLIMIT_DATA with EINVAL for a native runtime
		// configuration. Treat that as unavailable assistance only; finite
		// I/O/deadline limits and joined cleanup still bound work and lifetime.
		if err == syscall.EINVAL {
			return nil
		}
		return fmt.Errorf("set context worker data limit: %w", err)
	}
	return nil
}
