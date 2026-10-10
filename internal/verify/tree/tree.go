package tree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Entry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	OID  string `json:"oid"`
}

// Tree identifies a snapshot whose objects remain available under Home.
type Tree struct {
	Hash       string
	Root       string
	Home       string
	Entries    []Entry
	Objects    string
	Alternates string
}

func git(ctx context.Context, root string, env []string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return out, nil
}

func environment() []string {
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR":
		default:
			env = append(env, value)
		}
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0")
}

func objectEnv(t Tree) []string {
	return append(environment(), "GIT_OBJECT_DIRECTORY="+t.Objects, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+strconv.Quote(t.Alternates))
}

func RepositoryRoot(ctx context.Context, cwd string) (string, error) {
	out, err := git(ctx, cwd, environment(), nil, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(string(out)), err
}

// Compute rehashes tracked working files using a fresh index, writing only under home.
// Tracked files absent from disk, including skip-worktree paths, are omitted.
func Compute(ctx context.Context, root, home string) (Tree, error) {
	root, err := RepositoryRoot(ctx, root)
	if err != nil {
		return Tree{}, err
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return Tree{}, err
	}
	env := environment()
	alternate, err := git(ctx, root, env, nil, "rev-parse", "--git-path", "objects")
	if err != nil {
		return Tree{}, err
	}
	alt := strings.TrimSpace(string(alternate))
	if !filepath.IsAbs(alt) {
		alt = filepath.Join(root, alt)
	}
	t := Tree{Root: root, Home: home, Objects: filepath.Join(home, "git", "objects"), Alternates: alt, Entries: []Entry{}}
	if err := os.MkdirAll(t.Objects, 0700); err != nil {
		return Tree{}, err
	}
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0700); err != nil {
		return Tree{}, err
	}
	dir, err := os.MkdirTemp(tmp, "tree-")
	if err != nil {
		return Tree{}, err
	}
	defer os.RemoveAll(dir)
	staging := append(objectEnv(t), "GIT_INDEX_FILE="+filepath.Join(dir, "index"))
	listed, err := git(ctx, root, env, nil, "ls-files", "-z", "--stage")
	if err != nil {
		return Tree{}, err
	}
	var files, links bytes.Buffer
	seen := map[string]bool{}
	for _, record := range bytes.Split(listed, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, path, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 {
			return Tree{}, fmt.Errorf("invalid index entry %q", record)
		}
		if fields[0] == "160000" {
			fmt.Fprintf(&links, "%s %s\t%s\x00", fields[0], fields[1], path)
		} else if !seen[path] {
			seen[path] = true
			files.WriteString(path)
			files.WriteByte(0)
		}
	}
	if _, err = git(ctx, root, staging, links.Bytes(), "update-index", "-z", "--index-info"); err != nil {
		return Tree{}, err
	}
	if _, err = git(ctx, root, staging, files.Bytes(), "update-index", "--add", "--remove", "-z", "--stdin"); err != nil {
		return Tree{}, err
	}
	hash, err := git(ctx, root, staging, nil, "write-tree")
	if err != nil {
		return Tree{}, err
	}
	t.Hash = strings.TrimSpace(string(hash))
	listing, err := git(ctx, root, objectEnv(t), nil, "ls-tree", "-r", "-z", "--full-tree", t.Hash)
	if err != nil {
		return Tree{}, err
	}
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, path, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 {
			return Tree{}, fmt.Errorf("invalid tree entry %q", record)
		}
		if fields[1] == "blob" {
			t.Entries = append(t.Entries, Entry{Path: path, Mode: fields[0], OID: fields[2]})
		}
	}
	return t, nil
}

// ReadBlobs reads snapshot objects by oid, independent of subsequent checkout edits.
func ReadBlobs(ctx context.Context, t Tree, oids []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(oids))
	if len(oids) == 0 {
		return result, nil
	}
	for _, oid := range oids {
		if len(oid) != 40 && len(oid) != 64 {
			return nil, fmt.Errorf("invalid blob oid %q", oid)
		}
		for _, c := range oid {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return nil, fmt.Errorf("invalid blob oid %q", oid)
			}
		}
	}
	out, err := git(ctx, t.Root, objectEnv(t), []byte(strings.Join(oids, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	for _, requested := range oids {
		newline := bytes.IndexByte(out, '\n')
		if newline < 0 {
			return nil, fmt.Errorf("missing object response for %s", requested)
		}
		fields := strings.Fields(string(out[:newline]))
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("object %s is not an available blob", requested)
		}
		size, err := strconv.Atoi(fields[2])
		out = out[newline+1:]
		if err != nil || size < 0 || size >= len(out) || out[size] != '\n' {
			return nil, fmt.Errorf("invalid blob response for %s", requested)
		}
		result[requested] = bytes.Clone(out[:size])
		out = out[size+1:]
	}
	return result, nil
}

func Bytes(ctx context.Context, t Tree, paths []string) (map[string][]byte, error) {
	entries := make(map[string]string, len(t.Entries))
	for _, entry := range t.Entries {
		entries[entry.Path] = entry.OID
	}
	oids := []string{}
	seen := map[string]bool{}
	for _, path := range paths {
		oid, ok := entries[path]
		if !ok {
			return nil, fmt.Errorf("%s is not tracked in tree %s", path, t.Hash)
		}
		if !seen[oid] {
			oids = append(oids, oid)
			seen[oid] = true
		}
	}
	blobs, err := ReadBlobs(ctx, t, oids)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(paths))
	for _, path := range paths {
		result[path] = blobs[entries[path]]
	}
	return result, nil
}
