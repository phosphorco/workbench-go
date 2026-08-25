// Package v020v030snapshot isolates the immutable snapshot contract identity
// released by Workbench 0.2.0 and 0.3.0. Current writers must not use it.
package v020v030snapshot

import (
	"fmt"

	"github.com/phosphorco/workbench-go/internal/contract"
)

const Filename = "WorkbenchWorldSnapshot.pkl"

// ContractURI returns an exact released identity, never a current contract.
func ContractURI(version string) (string, error) {
	switch version {
	case "0.2.0", "0.3.0":
		return fmt.Sprintf(
			"package://github.com/phosphorco/workbench-go/releases/download/%s/workbench@%s#/%s",
			version,
			version,
			Filename,
		), nil
	default:
		return "", fmt.Errorf("release %s has no compatible historical snapshot contract", version)
	}
}

// Decode preserves the released JSON meaning while normalizing it into the
// current in-memory snapshot value. It does not authorize current emission.
func Decode(encoded []byte) (contract.WorkbenchSnapshot, error) {
	return contract.DecodeWorkbenchSnapshot(encoded)
}
