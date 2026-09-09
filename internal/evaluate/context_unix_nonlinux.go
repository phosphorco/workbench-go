//go:build unix && !linux && !darwin

package evaluate

import "fmt"

func setContextDataLimit(limit uint64) error {
	return fmt.Errorf("context worker data limit is unsupported on this platform")
}
