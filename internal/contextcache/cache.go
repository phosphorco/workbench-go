// Package contextcache owns the disposable context snapshot pool.
//
// The pool is deliberately independent of contexttrace.  A caller admits a
// disk mutation with Begin, performs its bounded write while the returned
// lease is held, and calls Finish.  The lease is the reservation; no journal
// or persistent reservation record is used.
package contextcache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	lockName       = ".capacity.lock"
	snapshotDir    = "snapshots"
	tempPrefix     = ".workbench-contextcache-"
	tempSuffix     = ".tmp"
	snapshotSuffix = ".snap"

	defaultMaxScanEntries     = 4096
	defaultMaxSnapshotBytes   = 8 << 20
	defaultLockWait           = 250 * time.Millisecond
	defaultLockPoll           = 2 * time.Millisecond
	defaultMaxSnapshotEntries = 4096
	snapshotHeaderSize        = 8 + 8 + sha256.Size
)

var snapshotMagic = [8]byte{'W', 'B', 'C', 'A', 'C', 'H', 'E', 1}

var (
	ErrInvalidPolicy = errors.New("contextcache: invalid policy")
	ErrStalePolicy   = errors.New("contextcache: stale policy")
	ErrCapacity      = errors.New("contextcache: capacity exceeded")
	ErrLockTimeout   = errors.New("contextcache: lock wait exceeded")
	ErrScanLimit     = errors.New("contextcache: scan limit exceeded")
	ErrSymlink       = errors.New("contextcache: symlink is not allowed")
	ErrSnapshotKey   = errors.New("contextcache: invalid snapshot key")
)

// Options bounds every operation performed by a Pool. Zero values select the
// finite defaults. Construction never creates Root or any child path.
type Options struct {
	MaxScanEntries     int
	MaxScanDepth       int
	MaxSnapshotBytes   int64
	MaxSnapshotEntries int
	LockWait           time.Duration
	LockPoll           time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxScanEntries == 0 {
		o.MaxScanEntries = defaultMaxScanEntries
	}
	if o.MaxScanDepth == 0 {
		o.MaxScanDepth = 32
	}
	if o.MaxSnapshotBytes == 0 {
		o.MaxSnapshotBytes = defaultMaxSnapshotBytes
	}
	if o.MaxSnapshotEntries == 0 {
		o.MaxSnapshotEntries = defaultMaxSnapshotEntries
	}
	if o.LockWait == 0 {
		o.LockWait = defaultLockWait
	}
	if o.LockPoll == 0 {
		o.LockPoll = defaultLockPoll
	}
	return o
}

func (o Options) validate() error {
	if o.MaxScanEntries < 1 || o.MaxScanDepth < 1 ||
		o.MaxSnapshotBytes < 1 || o.MaxSnapshotEntries < 1 || o.LockWait <= 0 || o.LockPoll <= 0 {
		return fmt.Errorf("%w: all bounds must be positive", ErrInvalidPolicy)
	}
	if o.MaxSnapshotBytes > int64(^uint(0)>>1)-snapshotHeaderSize {
		return fmt.Errorf("%w: snapshot bound overflows envelope", ErrInvalidPolicy)
	}
	return nil
}

// Policy is supplied by the authoritative loader for every mutation. CapBytes
type Policy struct {
	CapBytes int64
	// Validate is owned by the declaration loader. It runs after the pool lock
	// is acquired and must only perform bounded file-only freshness checks on
	// the immutable captured home/dependency closure. It must not evaluate Pkl,
	// call providers, recurse into this pool, or start workers. A changed
	// declaration should return the loader's typed stale-policy error so the
	// caller can release and reload outside the lock.
	Validate func(context.Context) error
}

// Usage is the authoritative regular-file usage observed while a mutation is
// held. The lock file is included; it is normally zero length, but any bytes
// written to it are charged like every other regular file.
type Usage struct {
	Bytes   int64
	Entries int
	Files   int
}

// Admission describes the peak resources that a caller will add while its
// mutation is held. Entries include temporary files and metadata directories;
// this prevents a bounded scan from being stranded by a successful write.
type Admission struct {
	TempBytes       int64
	TempEntries     int
	MetadataBytes   int64
	MetadataEntries int
}

// SnapshotKey is an opaque SHA-256 digest rendered as 64 lowercase hex bytes.
// It cannot select an arbitrary filesystem path.
type SnapshotKey string

// NewSnapshotKey validates a digest-derived snapshot key.
func NewSnapshotKey(digest string) (SnapshotKey, error) {
	if len(digest) != sha256.Size*2 || digest != strings.ToLower(digest) {
		return "", ErrSnapshotKey
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return "", ErrSnapshotKey
	}
	return SnapshotKey(digest), nil
}

// Pool is a concrete shared-capacity owner. It has no contexttrace dependency
// and performs no filesystem writes until Begin or SnapshotPublish.
type Pool struct {
	root string
	opts Options
}

// Open constructs a pool without creating Root, its lock, or snapshot paths.
func Open(root string, options Options) (*Pool, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: root must be absolute", ErrInvalidPolicy)
	}
	options = options.withDefaults()
	if err := options.validate(); err != nil {
		return nil, err
	}
	return &Pool{root: filepath.Clean(root), opts: options}, nil
}

// Root returns the confined absolute directory owned by this Pool. Owners
// that keep a private namespace below the pool must derive it from this value
// rather than accepting an independent path that could escape accounting.
func (p *Pool) Root() string { return p.root }

// Begin obtains the private cross-process lock, revalidates policy freshness,
// recovers only owned abandoned temporary files, and scans current usage. The
// caller owns the returned mutation until Finish. A stale policy releases the
// lock so the caller can reload outside the pool.
func (p *Pool) Begin(ctx context.Context, policy Policy) (*Mutation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := p.validatePolicyShape(policy); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ensureRoot(p.root); err != nil {
		return nil, err
	}
	file, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	release := func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}
	committed := false
	defer func() {
		if !committed {
			release()
			_ = root.Close()
		}
	}()
	if policy.Validate != nil {
		if err := policy.Validate(ctx); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.recoverTemps(ctx, root); err != nil {
		return nil, err
	}
	usage, err := p.scan(ctx, root)
	if err != nil {
		return nil, err
	}
	mutation := &Mutation{pool: p, lock: file, root: root, policy: policy, usage: usage, ctx: ctx}
	committed = true
	return mutation, nil
}

// BeginDefault is the bounded entry point for an owner whose API has no
// context. It still has the pool's finite LockWait bound.
func (p *Pool) BeginDefault(policy Policy) (*Mutation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.opts.LockWait)
	mutation, err := p.Begin(ctx, policy)
	if err != nil {
		cancel()
		return nil, err
	}
	mutation.cancel = cancel
	return mutation, nil
}

// Mutation is an owned lock and bounded mutation reservation. Disk writes must
// occur before Finish, and the caller must clean any caller-owned temp on an
// operation error before finishing.
type Mutation struct {
	pool   *Pool
	lock   *os.File
	root   *os.Root
	policy Policy
	usage  Usage
	ctx    context.Context
	cancel context.CancelFunc
	done   bool
}

// Usage returns the scan performed at Begin or the latest Refresh.
func (l *Mutation) Usage() Usage { return l.usage }

// Root returns the mutation-scoped capability for the exact filesystem root
// that was scanned at Begin. Callers may use it only until Finish; it is not a
// second root or an ownership transfer.
func (l *Mutation) Root() *os.Root { return l.root }

// Refresh rescans authoritative regular-file lengths while retaining the
// lease. It is useful after an owner evicts only its own files.
func (l *Mutation) Refresh() (Usage, error) {
	if err := l.checkOpen(); err != nil {
		return Usage{}, err
	}
	usage, err := l.pool.scan(l.ctx, l.root)
	if err != nil {
		return Usage{}, err
	}
	l.usage = usage
	return usage, nil
}

// EnsureFits admits the requested peak temporary and metadata bytes and
// entries against the current filesystem scan. Existing destination bytes and
// entries remain charged until an atomic replacement removes them.
func (l *Mutation) EnsureFits(admission Admission) error {
	if err := l.checkOpen(); err != nil {
		return err
	}
	if admission.TempBytes < 0 || admission.TempEntries < 0 || admission.MetadataBytes < 0 || admission.MetadataEntries < 0 {
		return fmt.Errorf("%w: negative mutation bounds", ErrInvalidPolicy)
	}
	usage, err := l.Refresh()
	if err != nil {
		return err
	}
	if exceeds(usage, admission, l.policy.CapBytes, l.pool.opts.MaxScanEntries) {
		return &CapacityError{CapBytes: l.policy.CapBytes, UsageBytes: usage.Bytes, UsageEntries: usage.Entries, PeakTempBytes: admission.TempBytes, PeakTempEntries: admission.TempEntries, MetadataBytes: admission.MetadataBytes, MetadataEntries: admission.MetadataEntries, MaxEntries: l.pool.opts.MaxScanEntries}
	}
	return nil
}

// Finish performs the final authoritative scan and releases the lock. A
// caller that wrote beyond its admission receives ErrCapacity, but the pool
// still releases the lock and never leaves a persistent reservation.
func (l *Mutation) Finish() (result error) {
	if err := l.checkOpen(); err != nil {
		return err
	}
	defer func() {
		unlockErr := syscall.Flock(int(l.lock.Fd()), syscall.LOCK_UN)
		closeErr := l.lock.Close()
		rootErr := l.root.Close()
		if l.cancel != nil {
			l.cancel()
			l.cancel = nil
		}
		result = errors.Join(result, unlockErr, closeErr, rootErr)
	}()
	usage, scanErr := l.pool.scan(l.ctx, l.root)
	l.usage = usage
	l.done = true
	if scanErr != nil {
		return scanErr
	}
	if usage.Bytes > l.policy.CapBytes {
		return &CapacityError{CapBytes: l.policy.CapBytes, UsageBytes: usage.Bytes, UsageEntries: usage.Entries, MaxEntries: l.pool.opts.MaxScanEntries}
	}
	return nil
}

// SnapshotPublish atomically replaces one opaque snapshot while holding the
// same pool lock used by other writers. It stores a bounded integrity envelope
// (magic, payload length, SHA-256, payload), evicts only older snapshot files,
// never trace or unknown files, and leaves the old destination in usage until
// the replacement rename.
func (p *Pool) SnapshotPublish(ctx context.Context, policy Policy, key SnapshotKey, encoded []byte) error {
	if err := p.validatePolicyShape(policy); err != nil {
		return err
	}
	if err := validateSnapshotKey(key); err != nil {
		return err
	}
	if int64(len(encoded)) > p.opts.MaxSnapshotBytes {
		return &CapacityError{CapBytes: policy.CapBytes, PeakTempBytes: int64(snapshotHeaderSize) + int64(len(encoded)), Detail: "snapshot exceeds per-snapshot bound"}
	}
	wire := snapshotEnvelope(encoded)
	lease, err := p.Begin(ctx, policy)
	if err != nil {
		return err
	}
	finished := false
	finishLease := func(operationErr error) error {
		if finished {
			return operationErr
		}
		finishErr := lease.Finish()
		finished = true
		return errors.Join(operationErr, finishErr)
	}
	defer func() {
		if !finished {
			_ = finishLease(nil)
		}
	}()

	root, err := os.OpenRoot(p.root)
	if err != nil {
		return finishLease(err)
	}
	defer root.Close()
	if err := p.makeSnapshotRoom(lease, root, key, int64(len(wire))); err != nil {
		return finishLease(err)
	}
	if err := root.MkdirAll(snapshotDir, 0700); err != nil {
		return finishLease(err)
	}
	if info, err := root.Lstat(snapshotDir); err != nil {
		return finishLease(err)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return finishLease(fmt.Errorf("%w: snapshot directory", ErrSymlink))
	}
	temp, tempRel, err := p.createTemp(root, string(key))
	if err != nil {
		return finishLease(err)
	}
	cleanup := true
	finishTemp := func(operationErr error) error {
		var cleanupErr error
		if cleanup {
			cleanupErr = root.Remove(tempRel)
			if os.IsNotExist(cleanupErr) {
				cleanupErr = nil
			}
			if cleanupErr == nil {
				cleanup = false
			}
		}
		finishErr := lease.Finish()
		finished = true
		return errors.Join(operationErr, cleanupErr, finishErr)
	}
	defer func() {
		if !finished {
			_ = finishTemp(nil)
		}
	}()
	if err := writeContext(ctx, temp, wire); err != nil {
		_ = temp.Close()
		return finishTemp(err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return finishTemp(err)
	}
	if err := temp.Close(); err != nil {
		return finishTemp(err)
	}
	targetRel := filepath.Join(snapshotDir, string(key)+snapshotSuffix)
	if info, statErr := root.Lstat(targetRel); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return finishTemp(fmt.Errorf("%w: snapshot target", ErrSymlink))
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return finishTemp(statErr)
	}
	if err := root.Rename(tempRel, targetRel); err != nil {
		return finishTemp(err)
	}
	cleanup = false
	return finishTemp(nil)
}

// SnapshotRead returns a disposable snapshot or (nil, false, nil) for an
// absent, corrupt, incompatible, or oversized cache entry. It opens through
// os.Root so an ancestor cannot escape the pool, then bounds the actual open
// file's size before allocating or decoding it. It creates no paths.
func (p *Pool) SnapshotRead(ctx context.Context, key SnapshotKey) ([]byte, bool, error) {
	if err := validateSnapshotKey(key); err != nil {
		return nil, false, err
	}
	if err := ctxOrBackground(ctx).Err(); err != nil {
		return nil, false, err
	}
	rootInfo, err := os.Lstat(p.root)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%w: pool root", ErrSymlink)
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	dirInfo, err := root.Lstat(snapshotDir)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%w: snapshot directory", ErrSymlink)
	}
	if !dirInfo.IsDir() {
		return nil, false, fmt.Errorf("%w: snapshot directory is not a directory", ErrInvalidPolicy)
	}
	rel := filepath.Join(snapshotDir, string(key)+snapshotSuffix)
	if info, err := root.Lstat(rel); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%w: snapshot", ErrSymlink)
	} else if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%w: snapshot is not regular", ErrInvalidPolicy)
	}
	file, err := root.Open(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("%w: snapshot is not regular", ErrInvalidPolicy)
		}
		return nil, false, err
	}
	maxWire := int64(snapshotHeaderSize) + p.opts.MaxSnapshotBytes
	if info.Size() < snapshotHeaderSize || info.Size() > maxWire {
		return nil, false, nil
	}
	data := make([]byte, int(info.Size()))
	if err := readContext(ctxOrBackground(ctx), file, data); err != nil {
		return nil, false, err
	}
	if !validSnapshotEnvelope(data, p.opts.MaxSnapshotBytes) {
		return nil, false, nil
	}
	payloadSize := int(binary.LittleEndian.Uint64(data[len(snapshotMagic):snapshotHeaderSize]))
	return data[snapshotHeaderSize : snapshotHeaderSize+payloadSize], true, nil
}

// CapacityError reports the admission terms without exposing filesystem
// paths or a mutable accounting object.
type CapacityError struct {
	CapBytes        int64
	UsageBytes      int64
	UsageEntries    int
	PeakTempBytes   int64
	PeakTempEntries int
	MetadataBytes   int64
	MetadataEntries int
	MaxEntries      int
	Detail          string
}

func (e *CapacityError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%v: %s (cap=%d usage=%d/%d temp=%d/%d metadata=%d/%d)", ErrCapacity, e.Detail, e.CapBytes, e.UsageBytes, e.UsageEntries, e.PeakTempBytes, e.PeakTempEntries, e.MetadataBytes, e.MetadataEntries)
	}
	return fmt.Sprintf("%v (cap=%d usage=%d/%d temp=%d/%d metadata=%d/%d max-entries=%d)", ErrCapacity, e.CapBytes, e.UsageBytes, e.UsageEntries, e.PeakTempBytes, e.PeakTempEntries, e.MetadataBytes, e.MetadataEntries, e.MaxEntries)
}

func (e *CapacityError) Unwrap() error { return ErrCapacity }

func (p *Pool) validatePolicyShape(policy Policy) error {
	if policy.CapBytes < 0 || policy.Validate == nil {
		return fmt.Errorf("%w: nonnegative capacity and nonnil validator are required", ErrInvalidPolicy)
	}
	return nil
}

func (p *Pool) acquire(ctx context.Context) (*os.File, error) {
	lockPath := filepath.Join(p.root, lockName)
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: capacity lock", ErrSymlink)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("%w: create lock handle", ErrInvalidPolicy)
	}
	deadline := time.Now().Add(p.opts.LockWait)
	for {
		if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return file, nil
		} else if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = file.Close()
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, errors.Join(ErrLockTimeout, err)
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, ErrLockTimeout
		}
		timer := time.NewTimer(p.opts.LockPoll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, errors.Join(ErrLockTimeout, ctx.Err())
		case <-timer.C:
		}
	}
}

func ensureRoot(root string) error {
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: pool root", ErrSymlink)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: pool root is not a directory", ErrInvalidPolicy)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: pool root", ErrInvalidPolicy)
	}
	return nil
}

func (p *Pool) recoverTemps(ctx context.Context, root *os.Root) error {
	info, err := root.Lstat(snapshotDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: snapshot directory", ErrSymlink)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: snapshot directory is not a directory", ErrInvalidPolicy)
	}
	dir, err := root.Open(snapshotDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	seen := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := p.recoveryEntryLimit() - seen
		if remaining <= 0 {
			probe, readErr := dir.ReadDir(1)
			if len(probe) != 0 {
				return fmt.Errorf("%w: recovery entry bound exceeded", ErrScanLimit)
			}
			if readErr != nil && readErr != io.EOF {
				return readErr
			}
			return nil
		}
		entries, readErr := dir.ReadDir(remaining + 1)
		if len(entries) > remaining {
			return fmt.Errorf("%w: recovery entry bound exceeded", ErrScanLimit)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			seen++
			if !ownedSnapshotTemp(entry.Name()) {
				continue
			}
			rel := filepath.Join(snapshotDir, entry.Name())
			entryInfo, statErr := root.Lstat(rel)
			if os.IsNotExist(statErr) {
				continue
			}
			if statErr != nil {
				return statErr
			}
			if entryInfo.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: abandoned snapshot temp", ErrSymlink)
			}
			if entryInfo.IsDir() || !entryInfo.Mode().IsRegular() {
				return fmt.Errorf("%w: abandoned snapshot temp is not regular", ErrInvalidPolicy)
			}
			if err := root.Remove(rel); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if len(entries) == 0 {
			return nil
		}
	}
}

func (p *Pool) recoveryEntryLimit() int {
	return p.opts.MaxScanEntries
}

func ownedSnapshotTemp(name string) bool {
	if !strings.HasPrefix(name, tempPrefix) || !strings.HasSuffix(name, tempSuffix) {
		return false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, tempPrefix), tempSuffix)
	separator := strings.LastIndexByte(value, '-')
	if separator <= 0 || separator == len(value)-1 {
		return false
	}
	if _, err := NewSnapshotKey(value[:separator]); err != nil {
		return false
	}
	sequence, err := strconv.ParseInt(value[separator+1:], 10, 64)
	return err == nil && sequence >= 0
}

func (p *Pool) scan(ctx context.Context, root *os.Root) (Usage, error) {
	usage := Usage{}
	err := p.walk(ctx, root, ".", func(path string, info os.FileInfo) error {
		usage.Entries++
		if info.IsDir() {
			return nil
		}
		if info.Mode().IsRegular() {
			if info.Size() < 0 || usage.Bytes > (int64(^uint64(0)>>1))-info.Size() {
				return fmt.Errorf("%w: byte total overflow", ErrScanLimit)
			}
			usage.Bytes += info.Size()
			usage.Files++
		}
		return nil
	})
	return usage, err
}

// walk invokes fn for each non-root entry and rejects symlinks without ever
// following them. ReadDir is chunked and the shared entry/depth bounds apply
// to recovery, accounting, and every future pool-owned walk.
func (p *Pool) walk(ctx context.Context, root *os.Root, start string, fn func(string, os.FileInfo) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := root.Lstat(start)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, start)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: pool root is not a directory", ErrInvalidPolicy)
	}
	entriesSeen := 0
	var visit func(string, int) error
	visit = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > p.opts.MaxScanDepth {
			return fmt.Errorf("%w: depth bound exceeded", ErrScanLimit)
		}
		file, err := root.Open(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer file.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			remaining := p.opts.MaxScanEntries - entriesSeen
			if remaining <= 0 {
				probe, readErr := file.ReadDir(1)
				if len(probe) != 0 {
					return fmt.Errorf("%w: entry bound exceeded", ErrScanLimit)
				}
				if readErr != nil && readErr != io.EOF {
					return readErr
				}
				return nil
			}
			entries, readErr := file.ReadDir(remaining + 1)
			if len(entries) > remaining {
				return fmt.Errorf("%w: entry bound exceeded", ErrScanLimit)
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				entriesSeen++
				path := filepath.Join(dir, entry.Name())
				entryInfo, statErr := root.Lstat(path)
				if statErr != nil {
					if os.IsNotExist(statErr) {
						continue
					}
					return statErr
				}
				if entryInfo.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("%w: %s", ErrSymlink, path)
				}
				if err := fn(path, entryInfo); err != nil {
					return err
				}
				if entryInfo.IsDir() {
					if err := visit(path, depth+1); err != nil {
						return err
					}
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			if len(entries) == 0 {
				return nil
			}
		}
	}
	return visit(start, 0)
}

type snapshotCandidate struct {
	key  SnapshotKey
	rel  string
	when time.Time
}

func (p *Pool) makeSnapshotRoom(lease *Mutation, root *os.Root, key SnapshotKey, needed int64) error {
	for {
		admission, err := snapshotAdmission(root, needed)
		if err != nil {
			return err
		}
		candidates, snapshotCount, hasKey, err := p.snapshotCandidates(lease.ctx, root, key)
		if err != nil {
			return err
		}
		fitsErr := lease.EnsureFits(admission)
		countFull := snapshotCount > p.opts.MaxSnapshotEntries || (!hasKey && snapshotCount >= p.opts.MaxSnapshotEntries)
		if fitsErr == nil && !countFull {
			return nil
		} else if fitsErr != nil && !errors.Is(fitsErr, ErrCapacity) {
			return fitsErr
		}
		if len(candidates) == 0 {
			if fitsErr != nil {
				return fitsErr
			}
			return &CapacityError{CapBytes: lease.policy.CapBytes, UsageBytes: lease.usage.Bytes, UsageEntries: lease.usage.Entries, PeakTempBytes: needed, PeakTempEntries: admission.TempEntries, MaxEntries: lease.pool.opts.MaxScanEntries, Detail: "snapshot count bound reached"}
		}
		candidate := candidates[0]
		expectedRel := filepath.Join(snapshotDir, string(candidate.key)+snapshotSuffix)
		if candidate.rel != expectedRel {
			return fmt.Errorf("%w: snapshot candidate path", ErrInvalidPolicy)
		}
		info, err := root.Lstat(candidate.rel)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: snapshot candidate", ErrSymlink)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: snapshot candidate is not regular", ErrInvalidPolicy)
		}
		if err := root.Remove(candidate.rel); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
	}
}

func snapshotAdmission(root *os.Root, bytes int64) (Admission, error) {
	entries := 1 // the protocol-owned temporary file
	info, err := root.Lstat(snapshotDir)
	if os.IsNotExist(err) {
		entries++ // MkdirAll creates the snapshots directory before the temp
	} else if err != nil {
		return Admission{}, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return Admission{}, fmt.Errorf("%w: snapshot directory", ErrSymlink)
	} else if !info.IsDir() {
		return Admission{}, fmt.Errorf("%w: snapshot directory is not a directory", ErrInvalidPolicy)
	}
	return Admission{TempBytes: bytes, TempEntries: entries}, nil
}

func (p *Pool) snapshotCandidates(ctx context.Context, root *os.Root, exclude SnapshotKey) ([]snapshotCandidate, int, bool, error) {
	info, err := root.Lstat(snapshotDir)
	if os.IsNotExist(err) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, false, fmt.Errorf("%w: snapshot directory", ErrSymlink)
	}
	if !info.IsDir() {
		return nil, 0, false, fmt.Errorf("%w: snapshot directory is not a directory", ErrInvalidPolicy)
	}
	dir, err := root.Open(snapshotDir)
	if err != nil {
		return nil, 0, false, err
	}
	defer dir.Close()
	result := make([]snapshotCandidate, 0, p.opts.MaxSnapshotEntries)
	seen := 0
	snapshotCount := 0
	hasKey := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, false, err
		}
		remaining := p.opts.MaxScanEntries - seen
		if remaining <= 0 {
			probe, readErr := dir.ReadDir(1)
			if len(probe) != 0 {
				return nil, 0, false, fmt.Errorf("%w: snapshot entry bound exceeded", ErrScanLimit)
			}
			if readErr != nil && readErr != io.EOF {
				return nil, 0, false, readErr
			}
			return sortSnapshotCandidates(result), snapshotCount, hasKey, nil
		}
		entries, readErr := dir.ReadDir(remaining + 1)
		if len(entries) > remaining {
			return nil, 0, false, fmt.Errorf("%w: snapshot entry bound exceeded", ErrScanLimit)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, 0, false, err
			}
			seen++
			if !strings.HasSuffix(entry.Name(), snapshotSuffix) {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), snapshotSuffix)
			candidate, err := NewSnapshotKey(name)
			if err != nil || candidate == exclude {
				if err == nil && candidate == exclude {
					snapshotCount++
					hasKey = true
				}
				continue
			}
			snapshotCount++
			rel := filepath.Join(snapshotDir, entry.Name())
			entryInfo, statErr := root.Lstat(rel)
			if statErr != nil {
				if os.IsNotExist(statErr) {
					continue
				}
				return nil, 0, false, statErr
			}
			if entryInfo.Mode()&os.ModeSymlink != 0 {
				return nil, 0, false, fmt.Errorf("%w: snapshot candidate", ErrSymlink)
			}
			if !entryInfo.Mode().IsRegular() {
				return nil, 0, false, fmt.Errorf("%w: snapshot candidate", ErrInvalidPolicy)
			}
			result = append(result, snapshotCandidate{key: candidate, rel: rel, when: entryInfo.ModTime()})
		}
		if readErr == io.EOF {
			return sortSnapshotCandidates(result), snapshotCount, hasKey, nil
		}
		if readErr != nil {
			return nil, 0, false, readErr
		}
		if len(entries) == 0 {
			return sortSnapshotCandidates(result), snapshotCount, hasKey, nil
		}
	}
}

func sortSnapshotCandidates(candidates []snapshotCandidate) []snapshotCandidate {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].when.Equal(candidates[j].when) {
			return candidates[i].rel < candidates[j].rel
		}
		return candidates[i].when.Before(candidates[j].when)
	})
	return candidates
}

func (p *Pool) createTemp(root *os.Root, key string) (*os.File, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		name := fmt.Sprintf("%s%s-%d%s", tempPrefix, key, time.Now().UnixNano()+int64(attempt), tempSuffix)
		rel := filepath.Join(snapshotDir, name)
		file, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_CLOEXEC, 0600)
		if err == nil {
			return file, rel, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("%w: temporary name exhausted", ErrInvalidPolicy)
}

func snapshotEnvelope(payload []byte) []byte {
	result := make([]byte, snapshotHeaderSize+len(payload))
	copy(result, snapshotMagic[:])
	binary.LittleEndian.PutUint64(result[len(snapshotMagic):len(snapshotMagic)+8], uint64(len(payload)))
	digest := sha256.Sum256(payload)
	copy(result[len(snapshotMagic)+8:snapshotHeaderSize], digest[:])
	copy(result[snapshotHeaderSize:], payload)
	return result
}

func validSnapshotEnvelope(data []byte, maxPayload int64) bool {
	if len(data) < snapshotHeaderSize || !bytesEqual(data[:len(snapshotMagic)], snapshotMagic[:]) {
		return false
	}
	payloadSize := binary.LittleEndian.Uint64(data[len(snapshotMagic) : len(snapshotMagic)+8])
	if payloadSize > uint64(maxPayload) || payloadSize > uint64(len(data)-snapshotHeaderSize) {
		return false
	}
	if int64(snapshotHeaderSize)+int64(payloadSize) != int64(len(data)) {
		return false
	}
	digest := sha256.Sum256(data[snapshotHeaderSize:])
	return bytesEqual(data[len(snapshotMagic)+8:snapshotHeaderSize], digest[:])
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func writeContext(ctx context.Context, file *os.File, data []byte) error {
	ctx = ctxOrBackground(ctx)
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := data
		if len(chunk) > 32*1024 {
			chunk = chunk[:32*1024]
		}
		n, err := file.Write(chunk)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readContext(ctx context.Context, file *os.File, data []byte) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := file.Read(data)
		if n > 0 {
			data = data[n:]
		}
		if err == io.EOF && len(data) == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func validateSnapshotKey(key SnapshotKey) error {
	_, err := NewSnapshotKey(string(key))
	if err != nil {
		return err
	}
	return nil
}

func exceeds(usage Usage, admission Admission, cap int64, maxEntries int) bool {
	if usage.Bytes < 0 || admission.TempBytes < 0 || admission.MetadataBytes < 0 || cap < 0 || usage.Entries < 0 || admission.TempEntries < 0 || admission.MetadataEntries < 0 || maxEntries < 0 {
		return true
	}
	if usage.Bytes > cap || admission.TempBytes > cap-usage.Bytes {
		return true
	}
	if admission.MetadataBytes > cap-usage.Bytes-admission.TempBytes {
		return true
	}
	if admission.TempEntries > maxEntries-usage.Entries {
		return true
	}
	return admission.MetadataEntries > maxEntries-usage.Entries-admission.TempEntries
}

func (l *Mutation) checkOpen() error {
	if l == nil || l.done || l.lock == nil {
		return fmt.Errorf("%w: mutation is closed", ErrInvalidPolicy)
	}
	return nil
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
