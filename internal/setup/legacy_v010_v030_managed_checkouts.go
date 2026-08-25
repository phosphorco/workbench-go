package setup

import "path/filepath"

// This adapter recognizes the exact receipt identity emitted by released
// Workbench 0.1–0.3. Current setup never emits this identity.
func observeHistoricalV010V030Receipt(stateDirectory string) (*observedReceipt, error) {
	return observeReceipt(filepath.Join(stateDirectory, "world.json"), "released 0.1–0.3 managed-checkout receipt")
}
