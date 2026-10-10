package tree_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/verify/tree"
)

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func write(t *testing.T, root, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), content, mode); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "--object-format=sha1")
	write(t, root, "plain.txt", []byte("base\n"), 0644)
	write(t, root, "nested/binary", []byte{0, 255, 10, 13, 0}, 0644)
	write(t, root, "run", []byte("#!/bin/sh\necho hi\n"), 0755)
	if err := os.Symlink("plain.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "tab\tline\nfile", []byte("odd path\n"), 0644)
	write(t, root, "deleted", []byte("gone\n"), 0644)
	git(t, root, "add", ".")
	return root
}
func compute(t *testing.T, root, home string) tree.Tree {
	t.Helper()
	result, err := tree.Compute(context.Background(), root, home)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func edits(t *testing.T, root string) {
	git(t, root, "update-index", "--assume-unchanged", "plain.txt")
	write(t, root, "plain.txt", []byte("edited\n"), 0644)
	if err := os.Remove(filepath.Join(root, "deleted")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "untracked", []byte("excluded\n"), 0644)
}
func TestAssumeUnchangedStillChanges(t *testing.T) {
	root := fixture(t)
	home := t.TempDir()
	before := compute(t, root, home)
	git(t, root, "update-index", "--assume-unchanged", "plain.txt")
	write(t, root, "plain.txt", []byte("changed\n"), 0644)
	after := compute(t, root, home)
	if before.Hash == after.Hash {
		t.Fatal("edited assume-unchanged file did not change tree")
	}
	content, err := tree.Bytes(context.Background(), after, []string{"plain.txt"})
	if err != nil || string(content["plain.txt"]) != "changed\n" {
		t.Fatalf("snapshot bytes: %q, %v", content, err)
	}
}
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		result[rel] = info.Mode().String()
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[rel] += string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestDotGitUntouched(t *testing.T) {
	root := fixture(t)
	edits(t, root)
	before := snapshot(t, filepath.Join(root, ".git"))
	home := t.TempDir()
	result := compute(t, root, home)
	if _, err := tree.ReadBlobs(context.Background(), result, []string{result.Entries[0].OID}); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, filepath.Join(root, ".git"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("checkout .git changed")
	}
	paths, err := os.ReadDir(filepath.Join(home, "tmp"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("temporary indexes not cleaned: %v, %v", paths, err)
	}
	if !strings.HasPrefix(result.Objects, home+string(os.PathSeparator)) {
		t.Fatal("objects outside home")
	}
}
func TestEqualsWriteTree(t *testing.T) {
	root := fixture(t)
	write(t, root, "plain.txt", []byte("edited\n"), 0644)
	if err := os.Remove(filepath.Join(root, "deleted")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "plain.txt"), 0755); err != nil {
		t.Fatal(err)
	}
	actual := compute(t, root, t.TempDir())
	git(t, root, "add", "--update")
	expected := git(t, root, "write-tree")
	if actual.Hash != expected {
		t.Fatalf("got %s want %s", actual.Hash, expected)
	}
}
func TestUntrackedExcluded(t *testing.T) {
	root := fixture(t)
	home := t.TempDir()
	before := compute(t, root, home)
	write(t, root, "new/untracked", []byte("new\n"), 0644)
	after := compute(t, root, home)
	if after.Hash != before.Hash {
		t.Fatal("untracked file entered tree")
	}
	if _, err := tree.Bytes(context.Background(), after, []string{"new/untracked"}); err == nil {
		t.Fatal("untracked read accepted")
	}
}
func TestSkipWorktreeAbsentLeavesTree(t *testing.T) {
	root := fixture(t)
	before := compute(t, root, t.TempDir())
	git(t, root, "update-index", "--skip-worktree", "plain.txt")
	if err := os.Remove(filepath.Join(root, "plain.txt")); err != nil {
		t.Fatal(err)
	}
	after := compute(t, root, t.TempDir())
	if before.Hash == after.Hash {
		t.Fatal("absent skip-worktree file retained")
	}
	for _, entry := range after.Entries {
		if entry.Path == "plain.txt" {
			t.Fatal("absent path listed")
		}
	}
}
func TestTypeScriptParity(t *testing.T) {
	root := fixture(t)
	edits(t, root)
	actual := compute(t, root, t.TempDir())
	var expected struct {
		Hash    string       `json:"hash"`
		Entries []tree.Entry `json:"entries"`
	}
	data, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if actual.Hash != expected.Hash || !reflect.DeepEqual(actual.Entries, expected.Entries) {
		t.Fatalf("Go tree differs from Bun reference: got %+v, want %+v", actual, expected)
	}
}
func TestBlobReadsSurviveCheckoutEdits(t *testing.T) {
	root := fixture(t)
	snapshot := compute(t, root, t.TempDir())
	write(t, root, "plain.txt", []byte("later\n"), 0644)
	contents, err := tree.Bytes(context.Background(), snapshot, []string{"plain.txt", "nested/binary", "link"})
	if err != nil {
		t.Fatal(err)
	}
	if string(contents["plain.txt"]) != "base\n" || string(contents["link"]) != "plain.txt" || !reflect.DeepEqual(contents["nested/binary"], []byte{0, 255, 10, 13, 0}) {
		t.Fatalf("wrong snapshot bytes: %v", contents)
	}
	if _, err := tree.ReadBlobs(context.Background(), snapshot, []string{strings.Repeat("0", 40)}); err == nil {
		t.Fatal("missing oid accepted")
	}
}
func TestGitlinksRetainedButNotBlobEntries(t *testing.T) {
	root := fixture(t)
	git(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixture")
	oid := git(t, root, "rev-parse", "HEAD")
	git(t, root, "update-index", "--add", "--cacheinfo", "160000,"+oid+",module")
	expected := git(t, root, "write-tree")
	snapshot := compute(t, root, t.TempDir())
	if snapshot.Hash != expected {
		t.Fatalf("gitlink tree %s want %s", snapshot.Hash, expected)
	}
	for _, entry := range snapshot.Entries {
		if entry.Path == "module" {
			t.Fatal("gitlink listed as blob")
		}
	}
}

func TestLinkedWorktreeUsesCheckoutAlternates(t *testing.T) {
	root := fixture(t)
	git(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixture")
	oid := git(t, root, "rev-parse", "HEAD:plain.txt")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "--detach", linked)
	before := snapshot(t, filepath.Join(root, ".git"))
	pointer, err := os.ReadFile(filepath.Join(linked, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	result := compute(t, linked, t.TempDir())
	blobs, err := tree.ReadBlobs(context.Background(), result, []string{oid})
	if err != nil || string(blobs[oid]) != "base\n" {
		t.Fatalf("alternate blob read: %q, %v", blobs, err)
	}
	afterPointer, err := os.ReadFile(filepath.Join(linked, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if string(pointer) != string(afterPointer) || !reflect.DeepEqual(before, snapshot(t, filepath.Join(root, ".git"))) {
		t.Fatal("worktree Git metadata changed")
	}
}
