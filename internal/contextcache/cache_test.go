package contextcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIndependentWritersBaselineCanOverspend(t *testing.T) {
	root := t.TempDir()
	capBytes := int64(16)
	start := make(chan struct{})
	var group sync.WaitGroup
	for _, name := range []string{"trace/block", "snapshots/snapshot"} {
		name := name
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			path := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Errorf("mkdir: %v", err)
				return
			}
			if err := os.WriteFile(path, make([]byte, 12), 0600); err != nil {
				t.Errorf("write: %v", err)
			}
		}()
	}
	close(start)
	group.Wait()
	if got := regularBytes(t, root); got <= capBytes {
		t.Fatalf("baseline usage = %d, want independent writers to exceed %d", got, capBytes)
	}
}

func TestConstructionAndMissingReadAreReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	p := mustPool(t, root, Options{})
	key := testKey(1)
	if data, ok, err := p.SnapshotRead(context.Background(), key); err != nil || ok || data != nil {
		t.Fatalf("missing read = (%q, %t, %v)", data, ok, err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("missing read created root: %v", err)
	}
}

func TestSnapshotRejectsWhenTraceLeavesNoRoom(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "trace", "block"), 15)
	p := mustPool(t, root, Options{})
	if err := p.SnapshotPublish(context.Background(), testPolicy(15+snapshotHeaderSize+6-1), testKey(2), make([]byte, 6)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("publish error = %v, want capacity", err)
	}
	if got := regularBytes(t, filepath.Join(root, "trace")); got != 15 {
		t.Fatalf("trace was changed by snapshot eviction: %d", got)
	}
	if _, err := os.Stat(filepath.Join(root, snapshotDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot directory after rejected publish = %v", err)
	}
}

func TestSnapshotReplacementChargesOldDestinationAtPeak(t *testing.T) {
	root := t.TempDir()
	key := testKey(3)
	old := filepath.Join(root, snapshotDir, string(key)+snapshotSuffix)
	writeFile(t, filepath.Join(root, "trace", "block"), 4)
	writeFile(t, old, 8)
	p := mustPool(t, root, Options{})
	if err := p.SnapshotPublish(context.Background(), testPolicy(4+8+snapshotHeaderSize+9-1), key, make([]byte, 9)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("replacement error = %v, want peak capacity", err)
	}
	if info, err := os.Stat(old); err != nil || info.Size() != 8 {
		t.Fatalf("old destination = (%v), want preserved 8-byte file", err)
	}
}

func TestSnapshotEvictsOnlyItsOwnFiles(t *testing.T) {
	root := t.TempDir()
	oldKey := testKey(4)
	newKey := testKey(5)
	writeFile(t, filepath.Join(root, "trace", "block"), 15)
	old := filepath.Join(root, snapshotDir, string(oldKey)+snapshotSuffix)
	writeFile(t, old, 4)
	oldTime := time.Unix(1, 0)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	p := mustPool(t, root, Options{})
	if err := p.SnapshotPublish(context.Background(), testPolicy(15+4+snapshotHeaderSize+5-1), newKey, make([]byte, 5)); err != nil {
		t.Fatalf("publish after own eviction: %v", err)
	}
	if got := regularBytes(t, filepath.Join(root, "trace")); got != 15 {
		t.Fatalf("trace usage = %d, want 15", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old snapshot remains: %v", err)
	}
	if data, ok, err := p.SnapshotRead(context.Background(), newKey); err != nil || !ok || len(data) != 5 {
		t.Fatalf("new snapshot = (%d, %t, %v)", len(data), ok, err)
	}
}

func TestReducedCapClosesNewGrowth(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "trace", "block"), 12)
	p := mustPool(t, root, Options{})
	mutation, err := p.Begin(context.Background(), testPolicy(8))
	if err != nil {
		t.Fatal(err)
	}
	if err := mutation.EnsureFits(Admission{}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("reduced-cap admission = %v, want capacity", err)
	}
	if err := mutation.Finish(); !errors.Is(err, ErrCapacity) {
		t.Fatalf("finish reduced-cap no-op = %v, want capacity", err)
	}
}

func TestPolicyValidatorRejectsStaleAndAbsentCreation(t *testing.T) {
	root := t.TempDir()
	declaration := filepath.Join(root, "home.pkl")
	first := []byte("alpha")
	writeBytes(t, declaration, first)
	valid := true
	policy := Policy{CapBytes: 100, Validate: func(context.Context) error {
		if !valid {
			return ErrStalePolicy
		}
		return nil
	}}
	p := mustPool(t, filepath.Join(root, "pool"), Options{})
	mutation, err := p.Begin(context.Background(), policy)
	if err != nil {
		t.Fatalf("initial manifest: %v", err)
	}
	if err := mutation.Finish(); err != nil {
		t.Fatal(err)
	}
	writeBytes(t, declaration, []byte("omega"))
	valid = false
	if _, err := p.Begin(context.Background(), policy); !errors.Is(err, ErrStalePolicy) {
		t.Fatalf("same-length edit = %v, want stale policy", err)
	}

	absent := filepath.Join(root, "new-home.pkl")
	absentExists := false
	absentPolicy := Policy{CapBytes: 100, Validate: func(context.Context) error {
		if absentExists {
			return ErrStalePolicy
		}
		return nil
	}}
	mutation, err = p.Begin(context.Background(), absentPolicy)
	if err != nil {
		t.Fatalf("initial absent manifest: %v", err)
	}
	if err := mutation.Finish(); err != nil {
		t.Fatal(err)
	}
	writeBytes(t, absent, []byte("created"))
	absentExists = true
	if _, err := p.Begin(context.Background(), absentPolicy); !errors.Is(err, ErrStalePolicy) {
		t.Fatalf("created absent path = %v, want stale policy", err)
	}
}

func TestAbandonedOwnedTempsReclaimedButUnknownTempsRemain(t *testing.T) {
	root := t.TempDir()
	ownedKey := testKey(9)
	writeBytes(t, filepath.Join(root, snapshotDir, tempPrefix+string(ownedKey)+"-123"+tempSuffix), []byte("old"))
	unknown := filepath.Join(root, snapshotDir, "other.tmp")
	writeBytes(t, unknown, []byte("keep"))
	traceTemp := filepath.Join(root, "trace", tempPrefix+string(ownedKey)+"-124"+tempSuffix)
	writeBytes(t, traceTemp, []byte("trace"))
	p := mustPool(t, root, Options{})
	mutation, err := p.Begin(context.Background(), testPolicy(100))
	if err != nil {
		t.Fatal(err)
	}
	if got := mutation.Usage().Bytes; got != int64(len("keep")+len("trace")) {
		t.Fatalf("recovered usage = %d, want unknown and trace temp", got)
	}
	if err := mutation.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, snapshotDir, tempPrefix+string(ownedKey)+"-123"+tempSuffix)); !os.IsNotExist(err) {
		t.Fatalf("owned abandoned temp remains: %v", err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("unknown temp was removed: %v", err)
	}
	if _, err := os.Stat(traceTemp); err != nil {
		t.Fatalf("trace temp was removed: %v", err)
	}
}

func TestSymlinkIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pool uses flock and no-follow semantics on Linux")
	}
	root := t.TempDir()
	key := testKey(6)
	target := filepath.Join(root, "outside")
	writeBytes(t, target, []byte("outside"))
	link := filepath.Join(root, snapshotDir, string(key)+snapshotSuffix)
	if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	p := mustPool(t, root, Options{})
	if _, _, err := p.SnapshotRead(context.Background(), key); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink read = %v, want symlink error", err)
	}
	if _, err := p.Begin(context.Background(), testPolicy(100)); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink scan = %v, want symlink error", err)
	}
}

func TestScanBoundIsEnforced(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "one"), 1)
	writeFile(t, filepath.Join(root, "two"), 1)
	p := mustPool(t, root, Options{MaxScanEntries: 2})
	if _, err := p.Begin(context.Background(), testPolicy(100)); !errors.Is(err, ErrScanLimit) {
		t.Fatalf("scan bound = %v, want scan limit", err)
	}
}

func TestPublicationReservesScanEntryHeadroom(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "trace", "block"), 1)
	p := mustPool(t, root, Options{MaxScanEntries: 4})
	if err := p.SnapshotPublish(context.Background(), testPolicy(100), testKey(7), []byte("x")); !errors.Is(err, ErrCapacity) {
		t.Fatalf("entry-headroom publish = %v, want capacity", err)
	}
}

func TestSnapshotCountBoundIsEnforcedAndSameKeyReplaces(t *testing.T) {
	root := t.TempDir()
	first := testKey(10)
	second := testKey(11)
	p := mustPool(t, root, Options{MaxSnapshotEntries: 1})
	if err := p.SnapshotPublish(context.Background(), testPolicy(1000), first, []byte("first")); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if err := p.SnapshotPublish(context.Background(), testPolicy(1000), second, []byte("second")); err != nil {
		t.Fatalf("second snapshot with own eviction: %v", err)
	}
	if _, ok, err := p.SnapshotRead(context.Background(), first); err != nil || ok {
		t.Fatalf("evicted first snapshot = (%t, %v)", ok, err)
	}
	if err := p.SnapshotPublish(context.Background(), testPolicy(1000), second, []byte("replacement")); err != nil {
		t.Fatalf("same-key replacement: %v", err)
	}
	if data, ok, err := p.SnapshotRead(context.Background(), second); err != nil || !ok || string(data) != "replacement" {
		t.Fatalf("replacement read = (%q, %t, %v)", data, ok, err)
	}
}

func TestFinishHonorsCancellationAndReleasesLock(t *testing.T) {
	root := t.TempDir()
	p := mustPool(t, root, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	mutation, err := p.Begin(ctx, testPolicy(100))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := mutation.Finish(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled finish = %v, want cancellation", err)
	}
	if next, err := p.Begin(context.Background(), testPolicy(100)); err != nil {
		t.Fatalf("lock after cancelled finish: %v", err)
	} else if err := next.Finish(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotIntegrityBitFlipIsMiss(t *testing.T) {
	root := t.TempDir()
	key := testKey(8)
	p := mustPool(t, root, Options{})
	if err := p.SnapshotPublish(context.Background(), testPolicy(100), key, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, snapshotDir, string(key)+snapshotSuffix)
	data := readBytes(t, path)
	data[len(data)-1] ^= 1
	writeBytes(t, path, data)
	if got, ok, err := p.SnapshotRead(context.Background(), key); err != nil || ok || got != nil {
		t.Fatalf("bit-flipped snapshot = (%q, %t, %v), want miss", got, ok, err)
	}
}

func TestLockCancellationWithinProcess(t *testing.T) {
	root := t.TempDir()
	p := mustPool(t, root, Options{LockWait: time.Second, LockPoll: time.Millisecond})
	held, err := p.Begin(context.Background(), testPolicy(100))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Finish()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Begin(ctx, testPolicy(100)); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("lock cancellation = %v, want timeout", err)
	}
}

func TestBeginDefaultMutationContextLivesUntilFinish(t *testing.T) {
	root := t.TempDir()
	p := mustPool(t, root, Options{LockWait: 250 * time.Millisecond, LockPoll: time.Millisecond})
	mutation, err := p.BeginDefault(testPolicy(100))
	if err != nil {
		t.Fatal(err)
	}
	if err := mutation.EnsureFits(Admission{}); err != nil {
		t.Fatalf("default mutation admission = %v", err)
	}
	if err := mutation.Finish(); err != nil {
		t.Fatalf("default mutation finish = %v", err)
	}
}

func TestMultiprocessLockWaitAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pool uses flock")
	}
	root := t.TempDir()
	p := mustPool(t, root, Options{LockWait: 2 * time.Second, LockPoll: time.Millisecond})
	held, err := p.Begin(context.Background(), testPolicy(100))
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(root, "helper-result")
	cmd := helperCommand(t, root, result, "wait")
	if err := cmd.Start(); err != nil {
		held.Finish()
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if err := held.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(readBytes(t, result))); got != "acquired" {
		t.Fatalf("helper wait result = %q", got)
	}

	held, err = p.Begin(context.Background(), testPolicy(100))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(result); err != nil {
		t.Fatal(err)
	}
	cmd = helperCommand(t, root, result, "cancel")
	if err := cmd.Start(); err != nil {
		held.Finish()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		held.Finish()
		t.Fatal(err)
	}
	if err := held.Finish(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(readBytes(t, result))); got != "timeout" {
		t.Fatalf("helper cancel result = %q", got)
	}
}

func TestPoolHelperProcess(t *testing.T) {
	if os.Getenv("CONTEXTCACHE_HELPER") != "1" {
		return
	}
	root := os.Getenv("CONTEXTCACHE_ROOT")
	result := os.Getenv("CONTEXTCACHE_RESULT")
	mode := os.Getenv("CONTEXTCACHE_MODE")
	p, err := Open(root, Options{LockWait: 2 * time.Second, LockPoll: time.Millisecond})
	if err != nil {
		writeBytes(t, result, []byte("open-error"))
		return
	}
	ctx := context.Background()
	if mode == "cancel" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
	}
	mutation, err := p.Begin(ctx, testPolicy(100))
	if err != nil {
		if errors.Is(err, ErrLockTimeout) {
			writeBytes(t, result, []byte("timeout"))
			return
		}
		writeBytes(t, result, []byte("error:"+err.Error()))
		return
	}
	_ = mutation.Finish()
	writeBytes(t, result, []byte("acquired"))
}

func mustPool(t *testing.T, root string, options Options) *Pool {
	t.Helper()
	p, err := Open(root, options)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testPolicy(capBytes int64) Policy {
	return Policy{CapBytes: capBytes, Validate: func(context.Context) error { return nil }}
}

func testKey(value byte) SnapshotKey {
	digest := make([]byte, sha256.Size)
	digest[0] = value
	return SnapshotKey(hex.EncodeToString(digest))
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	writeBytes(t, path, make([]byte, size))
}

func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func regularBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func helperCommand(t *testing.T, root, result, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestPoolHelperProcess", "--")
	cmd.Env = append(os.Environ(),
		"CONTEXTCACHE_HELPER=1",
		"CONTEXTCACHE_ROOT="+root,
		"CONTEXTCACHE_RESULT="+result,
		"CONTEXTCACHE_MODE="+mode,
	)
	return cmd
}
