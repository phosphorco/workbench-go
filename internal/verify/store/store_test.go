package store_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/phosphorco/workbench-go/internal/verify/store"
)

func must[T any](t *testing.T, v T, err error) T {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func blob(t *testing.T, home, text string) string {
	t.Helper()
	v, e := store.Put(home, []byte(text))
	return must(t, v, e)
}
func record(output, tree string) store.IndexRecord {
	return store.IndexRecord{Attestation: store.Attestation{ActionKey: store.SHA256([]byte("key")), Verifier: store.Verifier{ID: "v", Version: "one"}, UnitID: "unit", Outputs: map[string]string{"artifact": output}}, ComputedIn: tree, Stdout: "stdout", Stderr: "stderr"}
}
func TestKeyGoldenParity(t *testing.T) {
	b, e := os.ReadFile("testdata/keys.json")
	b = must(t, b, e)
	var cases []struct {
		Name      string
		Action    store.Action
		Key       string
		Directory string
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := store.ActionKey(c.Action); got != c.Key {
				t.Fatalf("key %s want %s", got, c.Key)
			}
			d, e := store.PutDirectory(t.TempDir(), c.Action.Inputs)
			if must(t, d, e) != c.Directory {
				t.Fatal("directory digest differs")
			}
			a := c.Action
			for i, j := 0, len(a.Inputs)-1; i < j; i, j = i+1, j-1 {
				a.Inputs[i], a.Inputs[j] = a.Inputs[j], a.Inputs[i]
			}
			if store.ActionKey(a) != c.Key {
				t.Fatal("order changes key")
			}
		})
	}
}
func TestFirstWins(t *testing.T) {
	home := t.TempDir()
	first := record("first", "tree1")
	p, e := store.Publish(home, "tree1", true, first)
	if !must(t, p, e).Won {
		t.Fatal("first lost")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			p, e := store.Publish(home, "tree2", true, record("later", "tree2"))
			if e != nil {
				t.Error(e)
				return
			}
			if p.Won || !reflect.DeepEqual(p.Record, first) || p.Witness == "" {
				t.Error("loser did not return stored record and witness")
			}
		})
	}
	wg.Wait()
	files, e := os.ReadDir(filepath.Join(home, "witness", "v", "one"))
	if len(must(t, files, e)) != 1 {
		t.Fatal("duplicate evidence not deduplicated")
	}
	p, e = store.Publish(home, "another", true, record("first", "another"))
	if must(t, p, e).Witness != "" {
		t.Fatal("same outputs witnessed")
	}
}
func TestFirstWinsConcurrent(t *testing.T) {
	home := t.TempDir()
	const n = 32
	results := make(chan store.Publication, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Go(func() {
			p, e := store.Publish(home, "tree", true, record(strings.Repeat("x", i+1), "tree"))
			if e != nil {
				t.Error(e)
				return
			}
			results <- p
		})
	}
	wg.Wait()
	close(results)
	wins := 0
	var stored *store.IndexRecord
	for p := range results {
		if p.Won {
			wins++
		}
		if stored == nil {
			s := p.Record
			stored = &s
		} else if !reflect.DeepEqual(*stored, p.Record) {
			t.Fatal("callers received differing records")
		}
	}
	if wins != 1 {
		t.Fatalf("%d winners", wins)
	}
}
func TestWitnessImmutable(t *testing.T) {
	home := t.TempDir()
	v := store.Verifier{ID: "v", Version: "one"}
	w := store.WitnessRecord{Kind: "scratch-audit", ActionKey: "key", UnitID: "unit", Scratch: "state", Warm: map[string]string{"artifact": "warm"}, Cold: map[string]string{"artifact": "cold"}}
	d, e := store.WriteWitness(home, v, w)
	d = must(t, d, e)
	p := filepath.Join(home, "witness", "v", "one", d+".json")
	b, e := os.ReadFile(p)
	b = must(t, b, e)
	if store.SHA256(b) != d {
		t.Fatal("witness filename is not content digest")
	}
	s, e := os.Stat(p)
	s = must(t, s, e)
	d2, e := store.WriteWitness(home, v, w)
	if must(t, d2, e) != d {
		t.Fatal("identical evidence changed digest")
	}
	s2, e := os.Stat(p)
	if !os.SameFile(s, must(t, s2, e)) {
		t.Fatal("witness overwritten")
	}
	w.Cold = map[string]string{"artifact": "other"}
	d2, e = store.WriteWitness(home, v, w)
	if must(t, d2, e) == d {
		t.Fatal("distinct evidence has same identity")
	}
	b2, e := os.ReadFile(p)
	if string(must(t, b2, e)) != string(b) {
		t.Fatal("earlier evidence changed")
	}
}
func TestFailureNotAttested(t *testing.T) {
	home := t.TempDir()
	r := record("failure", "tree")
	r.Attestation.Exit = 7
	p, e := store.Publish(home, "tree", true, r)
	if !errors.Is(e, store.ErrFailureNotAttested) || p.Witness == "" {
		t.Fatalf("failure: %+v %v", p, e)
	}
	found, e := store.Lookup(home, r.Attestation.ActionKey, "tree", true)
	if must(t, found, e) != nil {
		t.Fatal("failed action indexed")
	}
	r.Attestation.Exit = 0
	p, e = store.Publish(home, "tree", true, r)
	if !must(t, p, e).Won {
		t.Fatal("failure prevented retry")
	}
	r.Attestation.Exit = 9
	_, e = store.Publish(home, "tree", true, r)
	if !errors.Is(e, store.ErrFailureNotAttested) {
		t.Fatal(e)
	}
	found, e = store.Lookup(home, r.Attestation.ActionKey, "tree", true)
	if must(t, found, e).Attestation.Exit != 0 {
		t.Fatal("failure replaced success")
	}
}
func TestScratchVersion(t *testing.T) {
	home := t.TempDir()
	v := store.Verifier{ID: "v", Version: "one"}
	entries := []store.DirectoryEntry{{Path: "cache/h", Digest: blob(t, home, "state"), Mode: store.Regular}}
	for _, unit := range []string{"a/b 雪", "other"} {
		if e := store.SaveScratch(home, v, unit, entries); e != nil {
			t.Fatal(e)
		}
	}
	p, got, e := store.RestoreScratch(home, v, "a/b 雪")
	if e != nil || p == nil || !reflect.DeepEqual(got, entries) {
		t.Fatalf("restore %+v %+v %v", p, got, e)
	}
	v2 := store.Verifier{ID: "v", Version: "two"}
	p, _, e = store.RestoreScratch(home, v2, "a/b 雪")
	if e != nil || p != nil {
		t.Fatal("restored different version")
	}
	if e = store.SaveScratch(home, v2, "different", entries); e != nil {
		t.Fatal(e)
	}
	if e = store.DropScratch(home, v); e != nil {
		t.Fatal(e)
	}
	p, _, e = store.RestoreScratch(home, v, "other")
	if e != nil || p != nil {
		t.Fatal("old version retained")
	}
	p, _, e = store.RestoreScratch(home, v2, "different")
	if e != nil || p == nil {
		t.Fatal("other version removed")
	}
	_, e = store.WriteWitness(home, v2, store.WitnessRecord{Kind: "scratch-audit", ActionKey: "key", UnitID: "different"})
	if e != nil {
		t.Fatal(e)
	}
	p, _, e = store.RestoreScratch(home, v2, "different")
	if e != nil || p != nil {
		t.Fatal("witnessed scratch restored")
	}
}
func TestNondeterministicTree(t *testing.T) {
	home := t.TempDir()
	r := record("one", "t1")
	p, e := store.Publish(home, "t1", false, r)
	if !must(t, p, e).Won {
		t.Fatal("lost")
	}
	f, e := store.Lookup(home, r.Attestation.ActionKey, "t2", false)
	if must(t, f, e) != nil {
		t.Fatal("carried across tree")
	}
	r = record("two", "t2")
	p, e = store.Publish(home, "t2", false, r)
	if !must(t, p, e).Won || p.Witness != "" {
		t.Fatal("different tree collided")
	}
}
func TestCollectMaterialize(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	if e := os.WriteFile(filepath.Join(root, "tool"), []byte("tool"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink("/etc/passwd", filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	b, _, e := store.ReadRegularFile(filepath.Join(root, "link"))
	if !errors.Is(e, store.ErrRefused) || b != nil {
		t.Fatal("read followed symlink")
	}
	entries, e := store.Collect(home, root, nil)
	entries = must(t, entries, e)
	if len(entries) != 2 || entries[0].Mode != store.Symlink || entries[1].Mode != store.Executable {
		t.Fatalf("%+v", entries)
	}
	b, e = store.Read(home, entries[0].Digest)
	if string(must(t, b, e)) != "/etc/passwd" {
		t.Fatal("symlink target text not stored")
	}
	dest := t.TempDir()
	if e = store.Materialize(home, entries, dest); e != nil {
		t.Fatal(e)
	}
	target, e := os.Readlink(filepath.Join(dest, "link"))
	if must(t, target, e) != "/etc/passwd" {
		t.Fatal("symlink changed")
	}
	if e = os.WriteFile(filepath.Join(dest, "tool"), []byte("changed"), 0644); e != nil {
		t.Fatal(e)
	}
	b, e = store.Read(home, entries[1].Digest)
	if string(must(t, b, e)) != "tool" {
		t.Fatal("materialization changed CAS")
	}
	if e = store.Materialize(home, []store.DirectoryEntry{{Path: "../escape", Digest: entries[1].Digest, Mode: store.Regular}}, dest); e == nil {
		t.Fatal("path escaped")
	}
}

func TestActionKeyCoversRunFacts(t *testing.T) {
	base := store.Action{Verifier: "v", Version: "version", Role: "run", UnitID: "u", Inputs: []store.DirectoryEntry{{Path: "a", Digest: "blob", Mode: store.Regular}}, Upstream: []store.Upstream{{Name: "dependency", Digest: "artifact"}}, Program: []store.DirectoryEntry{{Path: "run", Digest: "program", Mode: store.Executable}}, Command: []string{"tool", "arg"}, ImageDigest: "image"}
	key := store.ActionKey(base)
	edits := []struct {
		name   string
		change func(*store.Action)
	}{
		{"verifier", func(a *store.Action) { a.Verifier = "other" }},
		{"version", func(a *store.Action) { a.Version = "other" }},
		{"role", func(a *store.Action) { a.Role = "select" }},
		{"unit", func(a *store.Action) { a.UnitID = "other" }},
		{"input path", func(a *store.Action) { a.Inputs[0].Path = "other" }},
		{"input bytes", func(a *store.Action) { a.Inputs[0].Digest = "other" }},
		{"input mode", func(a *store.Action) { a.Inputs[0].Mode = store.Symlink }},
		{"upstream name", func(a *store.Action) { a.Upstream[0].Name = "other" }},
		{"upstream bytes", func(a *store.Action) { a.Upstream[0].Digest = "other" }},
		{"program path", func(a *store.Action) { a.Program[0].Path = "other" }},
		{"program bytes", func(a *store.Action) { a.Program[0].Digest = "other" }},
		{"program mode", func(a *store.Action) { a.Program[0].Mode = store.Regular }},
		{"command", func(a *store.Action) { a.Command[0] = "other" }},
		{"image", func(a *store.Action) { a.ImageDigest = "other" }},
	}
	for _, edit := range edits {
		t.Run(edit.name, func(t *testing.T) {
			a := base
			a.Inputs = append([]store.DirectoryEntry{}, base.Inputs...)
			a.Program = append([]store.DirectoryEntry{}, base.Program...)
			a.Upstream = append([]store.Upstream{}, base.Upstream...)
			a.Command = append([]string{}, base.Command...)
			edit.change(&a)
			if store.ActionKey(a) == key {
				t.Fatal("fact absent from key")
			}
		})
	}
}

func TestMaterializeRefusesSymlinkEscape(t *testing.T) {
	home, target, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if e := os.Symlink(outside, filepath.Join(target, "escape")); e != nil {
		t.Fatal(e)
	}
	entry := store.DirectoryEntry{Path: "escape/file", Digest: blob(t, home, "secret"), Mode: store.Regular}
	if e := store.Materialize(home, []store.DirectoryEntry{entry}, target); e == nil {
		t.Fatal("materialization followed escaping directory symlink")
	}
	if _, e := os.Stat(filepath.Join(outside, "file")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("wrote outside target")
	}
}
