package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestRunWritesNamedArchiveAndExactChecksum(t *testing.T) {
	root := t.TempDir()
	file := func(name string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	outputDirectory := filepath.Join(root, "out")
	arguments := []string{
		"--version", "0.6.0",
		"--revision", "0123456789abcdef0123456789abcdef01234567",
		"--goos", "linux", "--goarch", "amd64", "--output", outputDirectory,
		"--workbench", file("workbench"), "--pkl", file("pkl"), "--bun", file("bun"),
		"--runtime-lock", "runtime-lock.json",
		"--workbench-license", file("workbench-license"),
		"--pkl-license", file("pkl-license"), "--pkl-notice", file("pkl-notice"),
		"--pkl-third-party-notice", file("pkl-third-party"), "--bun-license", file("bun-license"),
		"--go-license", file("go-license"), "--go-patents", file("go-patents"),
		"--pkl-go-license", file("pkl-go-license"), "--pkl-go-notice", file("pkl-go-notice"),
		"--msgpack-license", file("msgpack-license"), "--tagparser-license", file("tagparser-license"),
		"--yaml-license", file("yaml-license"),
	}
	var output bytes.Buffer
	if err := run(arguments, &output); err != nil {
		t.Fatalf("run(): %v", err)
	}
	archive := filepath.Join(outputDirectory, "workbench-0.6.0-linux-x64.tar.gz")
	if output.String() != archive+"\n" {
		t.Fatalf("output = %q, want archive path", output.String())
	}
	contents, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	wantChecksum := hex.EncodeToString(digest[:]) + "\n"
	checksum, err := os.ReadFile(archive + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	if string(checksum) != wantChecksum {
		t.Fatalf("checksum = %q, want %q", checksum, wantChecksum)
	}
}

func TestRunRequiresThePinnedRuntimeLock(t *testing.T) {
	root := t.TempDir()
	file := func(name string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	arguments := []string{
		"--version", "0.4.1",
		"--revision", "0123456789abcdef0123456789abcdef01234567",
		"--goos", "linux", "--goarch", "amd64", "--output", filepath.Join(root, "out"),
		"--workbench", file("workbench"), "--pkl", file("pkl"), "--bun", file("bun"),
		"--runtime-lock", "runtime-lock.json",
		"--workbench-license", file("workbench-license"),
		"--pkl-license", file("pkl-license"), "--pkl-notice", file("pkl-notice"),
		"--pkl-third-party-notice", file("pkl-third-party"), "--bun-license", file("bun-license"),
		"--go-license", file("go-license"), "--go-patents", file("go-patents"),
		"--pkl-go-license", file("pkl-go-license"), "--pkl-go-notice", file("pkl-go-notice"),
		"--msgpack-license", file("msgpack-license"), "--tagparser-license", file("tagparser-license"),
		"--yaml-license", file("yaml-license"),
	}
	if err := run(arguments, &bytes.Buffer{}); err == nil {
		t.Fatal("run succeeded when release and runtime-lock versions diverged")
	}
}
