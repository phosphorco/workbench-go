package buildable_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/buildable"
)

func TestResolvePrefersTheFirstValidCandidate(t *testing.T) {
	repository := fixtureRepository(t)
	writeCandidate(t, repository, ".ci-build/hello", "committed")
	writeCandidate(t, repository, ".local-build/hello", "local")

	resolved, err := buildable.Resolve(context.Background(), repository, "hello", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repository, ".local-build/hello/bin/hello")
	if resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}
}

func TestResolveRefusesAnInvalidPreferredCandidateWithoutFallingThrough(t *testing.T) {
	repository := fixtureRepository(t)
	writeCandidate(t, repository, ".ci-build/hello", "committed")
	writeCandidate(t, repository, ".local-build/hello", "local")
	write(t, filepath.Join(repository, ".local-build/hello/bin/hello"), "tampered\n", 0o755)

	_, err := buildable.Resolve(context.Background(), repository, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Resolve error = %T %v, want typed refusal", err, err)
	}
	if refusal.Candidate != ".local-build/hello" || !strings.Contains(refusal.Reason, "hash mismatch") {
		t.Fatalf("refusal = %#v", refusal)
	}
	if !strings.Contains(refusal.Remedy, "mise run hello:build-local") {
		t.Fatalf("remedy = %q", refusal.Remedy)
	}
	if strings.Contains(err.Error(), ".ci-build/hello/bin/hello") {
		t.Fatalf("refusal fell through to committed candidate: %v", err)
	}
}

func TestResolveRefusesAStaleManifestWithTheRecordedAndCurrentDigests(t *testing.T) {
	repository := fixtureRepository(t)
	writeCandidate(t, repository, ".ci-build/hello", "committed")
	write(t, filepath.Join(repository, "producer.txt"), "changed\n", 0o644)
	git(t, repository, "add", "producer.txt")
	git(t, repository, "commit", "-m", "change producer")

	_, err := buildable.Resolve(context.Background(), repository, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Resolve error = %T %v, want typed refusal", err, err)
	}
	if !strings.Contains(refusal.Reason, "stale") || !strings.Contains(refusal.Reason, "current inputs require") {
		t.Fatalf("refusal reason = %q", refusal.Reason)
	}
	if refusal.Candidate != ".ci-build/hello" {
		t.Fatalf("refusal candidate = %q", refusal.Candidate)
	}
}

func TestResolveRefusesAbsentCandidatesWithoutInvokingTheBuildCommand(t *testing.T) {
	repository := fixtureRepository(t)

	_, err := buildable.Resolve(context.Background(), repository, "hello", "linux", "amd64")
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Resolve error = %T %v, want typed refusal", err, err)
	}
	if refusal.Candidate != "" || !strings.Contains(refusal.Remedy, "mise run hello:build-local") {
		t.Fatalf("refusal = %#v", refusal)
	}
	if _, statErr := os.Stat(filepath.Join(repository, ".local-build")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Resolve acquired build authority: %v", statErr)
	}
}

func fixtureRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	write(t, filepath.Join(repository, "producer.txt"), "producer\n", 0o644)
	writeConfiguration(t, repository)
	git(t, repository, "init", "-q", "-b", "main")
	git(t, repository, "config", "user.name", "Buildable Test")
	git(t, repository, "config", "user.email", "buildable@example.test")
	git(t, repository, "add", "producer.txt")
	git(t, repository, "commit", "-qm", "producer")
	return repository
}

func writeConfiguration(t *testing.T, repository string) {
	t.Helper()
	configuration := map[string]any{
		"buildables": map[string]any{
			"hello": map[string]any{
				"inputDetection": map[string]any{"strategy": "gitHeadTree", "paths": []string{"producer.txt"}},
				"buildCommand":   map[string]any{"executable": "mise", "arguments": []string{"run", "hello:build-local"}},
				"manifest": map[string]any{
					"schemaVersion": 2, "kind": "hello-artifact-manifest", "contractId": "hello-v2",
					"expectedSource":       map[string]string{"repository": "https://example.test/hello", "channel": "latest"},
					"requiredSourceFields": []string{"revision", "version", "nestedRevision"},
					"requiredCapabilities": []string{"greeting-v1"},
				},
				"candidates": []map[string]any{
					{"root": ".local-build/hello", "invalidRemedy": "Run 'mise run hello:build-local' to rebuild it or remove the local candidate."},
					{"root": ".ci-build/hello", "invalidRemedy": "Restore the committed candidate or run the authorized CI promotion."},
				},
				"platforms": map[string]any{
					"linux-x86_64": map[string]any{"os": []string{"linux"}, "arch": []string{"amd64"}, "path": "bin/hello", "executable": true},
				},
			},
		},
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repository, ".workbench/buildables.json"), string(encoded)+"\n", 0o644)
}

func writeCandidate(t *testing.T, repository, root, label string) {
	t.Helper()
	contents := "#!/bin/sh\necho " + label + "\n"
	write(t, filepath.Join(repository, root, "bin/hello"), contents, 0o755)
	digest := sha256.Sum256([]byte(contents))
	manifest := map[string]any{
		"schemaVersion": 2,
		"kind":          "hello-artifact-manifest",
		"contractId":    "hello-v2",
		"source":        map[string]any{"repository": "https://example.test/hello", "revision": "fixture", "channel": "latest", "version": "1.0.0", "nestedRevision": "fixture"},
		"producerInputs": map[string]any{
			"algorithm": "sha256", "digest": producerDigest(t, repository),
		},
		"capabilities": []string{"greeting-v1"},
		"outputs": []map[string]any{{
			"platform": "linux-x86_64", "path": "bin/hello", "sha256": hex.EncodeToString(digest[:]),
			"size": len(contents), "executable": true,
		}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repository, root, "manifest.json"), string(encoded)+"\n", 0o644)
}

func producerDigest(t *testing.T, repository string) string {
	t.Helper()
	command := exec.Command("git", "ls-tree", "-r", "--full-tree", "HEAD", "--", "producer.txt")
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(output)
	return hex.EncodeToString(digest[:])
}

func write(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, repository string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}
