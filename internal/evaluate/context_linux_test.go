//go:build linux

package evaluate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestContextIdentityRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-lock.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	started := time.Now()
	_, err := loadContextIdentity(context.Background(), path)
	if err == nil {
		t.Fatal("FIFO runtime lock was accepted")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("FIFO runtime lock open blocked: %s", elapsed)
	}
}

func TestContextRootedReadStableControl(t *testing.T) {
	rootPath := t.TempDir()
	modulePath := filepath.Join(rootPath, "rules.pkl")
	want := []byte("module test.Rules\nvalue: String = \"stable\"\n")
	if err := os.WriteFile(modulePath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	target, got, err := readContextRooted(root, rootPath, "rules.pkl", uint64(len(want)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("stable rooted read = %q, want %q", got, want)
	}
	if target == "" {
		t.Fatal("stable rooted read omitted canonical target")
	}
}

func TestContextRootedReadRejectsSameSizeInPlaceMutation(t *testing.T) {
	rootPath := t.TempDir()
	modulePath := filepath.Join(rootPath, "rules.pkl")
	first := []byte("module test.Rules\nvalue: String = \"alpha\"\n")
	second := []byte("module test.Rules\nvalue: String = \"bravo\"\n")
	if len(first) != len(second) {
		t.Fatalf("test fixture sizes differ: %d != %d", len(first), len(second))
	}
	if err := os.WriteFile(modulePath, first, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, _, err = readContextRootedWithHook(root, rootPath, "rules.pkl", uint64(len(first)), func() error {
		return os.WriteFile(modulePath, second, 0o600)
	})
	if err == nil {
		t.Fatal("same-size in-place mutation was accepted")
	}
}

func TestContextRootedReadRejectsSameSizeReplacement(t *testing.T) {
	rootPath := t.TempDir()
	modulePath := filepath.Join(rootPath, "rules.pkl")
	replacementPath := filepath.Join(rootPath, "replacement.pkl")
	first := []byte("module test.Rules\nvalue: String = \"alpha\"\n")
	second := []byte("module test.Rules\nvalue: String = \"bravo\"\n")
	if len(first) != len(second) {
		t.Fatalf("test fixture sizes differ: %d != %d", len(first), len(second))
	}
	if err := os.WriteFile(modulePath, first, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacementPath, second, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, _, err = readContextRootedWithHook(root, rootPath, "rules.pkl", uint64(len(first)), func() error {
		return os.Rename(replacementPath, modulePath)
	})
	if err == nil {
		t.Fatal("same-size replacement was accepted")
	}
}
