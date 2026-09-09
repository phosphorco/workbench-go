package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquirePinnedPklSelectsLockArtifactAndExportsAbsolutePath(t *testing.T) {
	root := t.TempDir()
	payload := []byte("pinned pkl executable fixture\n")
	payloadPath := filepath.Join(root, "fixture-pkl")
	if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	lock := `{"runtimes":{"pkl":{"artifacts":{"linux-x64":{"url":"https://fixture.invalid/pkl","sha256":"` + hex.EncodeToString(digest[:]) + `"}}}}}`
	lockPath := filepath.Join(root, "runtime-lock.json")
	if err := os.WriteFile(lockPath, []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCurl := filepath.Join(fakeBin, "curl")
	if err := os.WriteFile(fakeCurl, []byte(`#!/usr/bin/env bash
set -euo pipefail
output=""
while (($#)); do
  if [[ "$1" == "--output" ]]; then
    output="$2"
    shift 2
  else
    shift
  fi
done
cp "$PKL_FIXTURE" "$output"
`), 0o755); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(root, "nested", "pkl")
	envPath := filepath.Join(root, "github-env")
	command := exec.Command("bash", filepath.Join(".", "acquire-pkl.sh"),
		"--lock", lockPath,
		"--platform", "linux-x64",
		"--output", outputPath,
		"--env-file", envPath,
	)
	command.Env = append(os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PKL_FIXTURE="+payloadPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("acquire pinned Pkl: %v\n%s", err, output)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("acquired Pkl = %q, want %q", got, payload)
	}
	if info, err := os.Stat(outputPath); err != nil {
		t.Fatal(err)
	} else if info.Mode()&0o111 == 0 {
		t.Fatalf("acquired Pkl mode = %o, want executable", info.Mode())
	}
	env, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := filepath.Abs(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "PKL_EXECUTABLE=" + wantPath + "\n"; string(env) != want {
		t.Fatalf("workflow environment = %q, want %q", env, want)
	}
	if strings.Contains(string(env), "../") {
		t.Fatalf("workflow environment exported a non-absolute path: %q", env)
	}
}
