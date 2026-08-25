package setup

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNewManagedCheckoutReceiptNeverReplacesUnprovenState(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, ".workbench")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, managedCheckoutReceiptName)
	original := []byte("foreign state\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDurableReceipt(path, []byte(`{"version":1,"resources":[]}`), nil); err == nil {
		t.Fatal("unproven current state was replaced")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after, original) {
		t.Fatalf("current state changed: %q", after)
	}
}
