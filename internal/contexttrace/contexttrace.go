// Package contexttrace stores disposable, bounded evidence about context
// decisions. It deliberately has no dependency on the context runtime or on
// a delivery implementation.
package contexttrace

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/phosphorco/workbench-go/internal/contextcache"
)

const (
	// MaxContributionSampleBytes is a product bound, not an option. A caller
	// cannot configure the trace to retain a complete large contribution.
	MaxContributionSampleBytes = 10_000

	defaultBlockBytes          = 256 * 1024
	defaultBlockRecords        = 64
	defaultMaxBlocks           = 32 * 1024
	defaultMaxDirectoryEntries = defaultMaxBlocks + 256
	defaultRecordBytes         = 64 * 1024
	defaultInputBytes          = 64 * 1024 * 1024
	defaultStringBytes         = 4 * 1024
	defaultInputs              = 64
	defaultReasons             = 64
	defaultParameters          = 256
	defaultQueryRecords        = 100
	defaultQueryBytes          = 1024 * 1024
	defaultScanBytes           = 4 * 1024 * 1024

	stateFileSize   = 32
	blockHeaderSize = 64
	blockNamePrefix = "block-"
	blockNameSuffix = ".gob"
	stateName       = "state.bin"
	traceDirName    = "trace"
)

var (
	ErrClosed             = errors.New("contexttrace: store is closed")
	ErrInvalidOptions     = errors.New("contexttrace: invalid options")
	ErrInvalidRecord      = errors.New("contexttrace: invalid record")
	ErrInvalidUTF8        = errors.New("contexttrace: contribution is not valid UTF-8")
	ErrInputTooLarge      = errors.New("contexttrace: input exceeds its bound")
	ErrRecordTooLarge     = errors.New("contexttrace: record exceeds its bound")
	ErrQueryBounds        = errors.New("contexttrace: query exceeds its bound")
	ErrDiskCap            = errors.New("contexttrace: disk cap would be exceeded")
	ErrScanBudgetTooSmall = errors.New("contexttrace: scan budget is smaller than the next segment")
)

// ScanBudgetError is returned when a query cannot inspect even its first
// eligible block within MaxScanBytes. The caller must retry with at least
// RequiredBytes; returning an empty page here would make a cursor repeat
// forever while falsely looking like absent evidence.
type ScanBudgetError struct {
	RequiredBytes int64
	ProvidedBytes int64
}

func (err ScanBudgetError) Error() string {
	return fmt.Sprintf("%v: need at least %d bytes, provided %d", ErrScanBudgetTooSmall, err.RequiredBytes, err.ProvidedBytes)
}

func (err ScanBudgetError) Is(target error) bool { return target == ErrScanBudgetTooSmall }

// EventKind identifies the owner or stage that produced a trace record.
// Unknown non-zero values are retained so a newer runtime can be inspected by
// an older cache reader.
type EventKind uint16

const (
	KindObservation EventKind = iota + 1
	KindProfile
	KindContribution
	KindQueued
	KindOffered
	KindConfirmed
	KindSuppressed
	KindDeferred
	KindRejected
	KindFailed
	KindWithdrawn
)

// OutcomeCode is the outcome attached to a record. It is numeric on disk so
// common outcome names do not consume a dictionary entry.
type OutcomeCode uint16

const (
	OutcomeNone OutcomeCode = iota
	OutcomeQueued
	OutcomeOffered
	OutcomeConfirmed
	OutcomeSuppressed
	OutcomeDeferred
	OutcomeRejected
	OutcomeFailed
	OutcomeWithdrawn
)

// ValueKind preserves the original scalar type of a why-input or reason
// parameter. Numbers and booleans are not stringified into ambiguous text.
type ValueKind uint8

const (
	ValueInvalid ValueKind = iota
	ValueString
	ValueInt64
	ValueUint64
	ValueBool
)

// Value is a small tagged scalar suitable for bounded trace provenance.
type Value struct {
	Kind   ValueKind
	String string
	Int64  int64
	Uint64 uint64
	Bool   bool
}

func StringValue(value string) Value { return Value{Kind: ValueString, String: value} }
func Int64Value(value int64) Value   { return Value{Kind: ValueInt64, Int64: value} }
func Uint64Value(value uint64) Value { return Value{Kind: ValueUint64, Uint64: value} }
func BoolValue(value bool) Value     { return Value{Kind: ValueBool, Bool: value} }

// Input is a bounded, retained fact used by a selection decision. Code is
// intentionally numeric; Key and Value preserve the actual bounded input
// rather than only a hash or a path to mutable configuration.
type Input struct {
	Code  uint16
	Key   string
	Value Value
}

// Parameter is a bounded value supplied with a provider or runtime reason.
type Parameter struct {
	Key   string
	Value Value
}

// Reason preserves provenance for why a decision was made. The provider and
// rule are historical values, not references to whatever configuration exists
// when the record is inspected later.
type Reason struct {
	Code     uint16
	Provider string
	Rule     string
	Params   []Parameter
}

// Record is the caller-facing trace input and inspected output. Content is
// read only by Append and is never retained whole; Query results expose the
// bounded Sample instead. IDs and Generation are assigned by Store and are
// ignored when supplied to Append.
type Record struct {
	ID             uint64
	Generation     uint64
	Kind           EventKind
	At             time.Time
	Scope          string
	Audience       string
	Epoch          uint64
	Turn           string
	Profile        string
	Source         string
	Contributor    string
	ContributionID uint64
	// ContentDigest fingerprints the complete caller input without retaining
	// that input. It is computed by Append; a supplied value is ignored.
	ContentDigest [32]byte
	Content       []byte
	Inputs        []Input
	Reasons       []Reason
	Outcome       OutcomeCode
	Sample        Sample
}

// Excerpt is one valid UTF-8 portion of a contribution. Offset and Bytes are
// byte units, as are the omission counts. OmittedBefore/OmittedAfter are
// context around this excerpt; OmittedBytes on Sample is the authoritative
// non-retained total.
type Excerpt struct {
	Offset        uint64
	Bytes         uint64
	OmittedBefore uint64
	OmittedAfter  uint64
	Text          string
}

// Sample is bounded evidence, never delivery content. Its excerpt text is
// independently allocated and therefore cannot keep a large caller string or
// byte backing array alive.
type Sample struct {
	OriginalBytes uint64
	OmittedBytes  uint64
	Excerpts      []Excerpt
}

// GapKind says why a range of evidence is unavailable.
type GapKind uint8

const (
	GapRotated GapKind = iota + 1
	GapDropped
	GapCorrupt
	GapUnreadable
)

// Gap is explicit missing evidence. A zero range means the missing thing was
// an auxiliary block or state file whose record range was unknowable.
type Gap struct {
	Kind          GapKind
	Generation    uint64
	FromRecordID  uint64
	ToRecordID    uint64
	BlockSequence uint64
	Detail        string
}

// RetainedRange describes the valid records found while inspecting the
// current generation. It is not a claim that records outside the range never
// existed.
type RetainedRange struct {
	Generation    uint64
	FirstRecordID uint64
	LastRecordID  uint64
	Count         uint64
}

// Cursor resumes a bounded scan after the indicated block record. It is
// separate from After, which filters returned record IDs.
type Cursor struct {
	BlockSequence uint64
	RecordID      uint64
}

// Options bounds both input work and retained storage. Zero values select the
// documented defaults. A lower disk cap retains less history; it never raises
// the sample or query bounds.
type Options struct {
	MaxBlockBytes       int
	MaxBlockRecords     int
	MaxBlocks           int
	MaxDirectoryEntries int
	MaxRecordBytes      int
	MaxInputBytes       int
	MaxStringBytes      int
	MaxInputs           int
	MaxReasons          int
	MaxParameters       int
	MaxQueryRecords     int
	MaxQueryBytes       int
	MaxScanBytes        int
}

func defaultOptions(options Options) Options {
	if options.MaxBlockBytes == 0 {
		options.MaxBlockBytes = defaultBlockBytes
	}
	if options.MaxBlockRecords == 0 {
		options.MaxBlockRecords = defaultBlockRecords
	}
	if options.MaxBlocks == 0 {
		options.MaxBlocks = defaultMaxBlocks
	}
	if options.MaxDirectoryEntries == 0 {
		options.MaxDirectoryEntries = defaultMaxDirectoryEntries
	}
	if options.MaxRecordBytes == 0 {
		options.MaxRecordBytes = defaultRecordBytes
	}
	if options.MaxInputBytes == 0 {
		options.MaxInputBytes = defaultInputBytes
	}
	if options.MaxStringBytes == 0 {
		options.MaxStringBytes = defaultStringBytes
	}
	if options.MaxInputs == 0 {
		options.MaxInputs = defaultInputs
	}
	if options.MaxReasons == 0 {
		options.MaxReasons = defaultReasons
	}
	if options.MaxParameters == 0 {
		options.MaxParameters = defaultParameters
	}
	if options.MaxQueryRecords == 0 {
		options.MaxQueryRecords = defaultQueryRecords
	}
	if options.MaxQueryBytes == 0 {
		options.MaxQueryBytes = defaultQueryBytes
	}
	if options.MaxScanBytes == 0 {
		options.MaxScanBytes = defaultScanBytes
	}
	return options
}

func (options Options) validate() error {
	if options.MaxBlockBytes < blockHeaderSize+64 {
		return fmt.Errorf("%w: block byte bound is too small", ErrInvalidOptions)
	}
	if uint64(options.MaxBlockRecords) > uint64(^uint32(0)) {
		return fmt.Errorf("%w: block record bound is too large", ErrInvalidOptions)
	}
	if options.MaxBlockRecords < 1 || options.MaxBlocks < 1 || options.MaxDirectoryEntries < options.MaxBlocks || options.MaxRecordBytes < 1 || options.MaxInputBytes < 1 || options.MaxStringBytes < 1 || options.MaxInputs < 1 || options.MaxReasons < 1 || options.MaxParameters < 1 || options.MaxQueryRecords < 1 || options.MaxQueryBytes < queryMetadataBytes || options.MaxScanBytes < 1 {
		return fmt.Errorf("%w: bounds must be positive", ErrInvalidOptions)
	}
	return nil
}

// Query selects a bounded page. After is an exclusive numeric record cursor;
// callers can request another page using NextAfter. Cursor bounds the amount
// of retained history inspected and MaxScanBytes prevents a query from
// holding the store lock while walking an unbounded cache.
type Query struct {
	After          uint64
	Limit          int
	MaxBytes       int
	MaxScanBytes   int
	Cursor         Cursor
	Generation     uint64
	Kind           EventKind
	ContributionID uint64
	Scope          string
	Audience       string
	Epoch          uint64
	Turn           string
	Profile        string
	Source         string
	Contributor    string
}

// QueryResult is a bounded immutable snapshot. Gaps are returned even when a
// corrupt or rotated block prevented a complete history scan.
type QueryResult struct {
	Generation  uint64
	Records     []Record
	Retained    RetainedRange
	Gaps        []Gap
	NextAfter   uint64
	NextCursor  Cursor
	HasMore     bool
	PartialScan bool
	PartialPage bool
}

// AppendResult distinguishes acceptance into the bounded in-memory batch from
// a block already written to disk. Flush or Query establishes the latter.
type AppendResult struct {
	Accepted       bool
	Persisted      bool
	Generation     uint64
	RecordID       uint64
	ContributionID uint64
	Dropped        Gap
}

// ClearResult reports the trace-only reset. Delivery state owned by another
// package is not reachable from this type and cannot be cleared accidentally.
type ClearResult struct {
	PreviousGeneration uint64
	Generation         uint64
	RemovedRecords     uint64
}

// Stats is a bounded status snapshot. DiskBytes includes the state file and
// every other regular auxiliary in the private cache directory, while
// PendingBytes is the encoded size of the in-memory batch if flushed now.
// KnownLoss means inspection has at least one retained gap (rotation,
// rejected admission, or unreadable/corrupt cache evidence).
type Stats struct {
	Generation     uint64
	DiskCap        int64
	DiskBytes      int64
	BlockCount     int
	PendingRecords int
	PendingBytes   int64
	KnownLoss      bool
}

// Store is a synchronous owner of one private cache directory. It starts no
// goroutines and never retains caller-owned mutable storage. The mutex is
// process-local; callers must not open the same directory from multiple Store
// writers concurrently.
type Store struct {
	mu sync.Mutex

	options  Options
	capacity *contextcache.Pool
	policy   contextcache.Policy
	closed   bool

	generation     uint64
	nextRecordID   uint64
	rotatedThrough uint64
	stateValid     bool
	stateDirty     bool

	nextBlockSequence uint64
	blocks            []blockFile
	pending           []preparedRecord
	pendingBytes      int64
	openGaps          []Gap
	dropGaps          []Gap
	usage             int64
}

type blockFile struct {
	sequence uint64
	path     string
	size     int64
}

type preparedRecord struct {
	ID             uint64
	Generation     uint64
	Kind           EventKind
	At             time.Time
	Scope          string
	Audience       string
	Epoch          uint64
	Turn           string
	Profile        string
	Source         string
	Contributor    string
	ContributionID uint64
	ContentDigest  [32]byte
	Inputs         []Input
	Reasons        []Reason
	Outcome        OutcomeCode
	Sample         Sample
}

// Open creates or opens the private disposable trace namespace derived from
// pool.Root()/trace. Existing malformed blocks are retained for inspection as
// gaps instead of making the runtime fail to start. Pool is the sole
// cross-process capacity and writer lease; all startup cleanup,
// reconciliation, trimming, and state writes occur while the Store mutex is
// logically held and that lease is active.
func Open(ctx context.Context, options Options, pool *contextcache.Pool, policy contextcache.Policy) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if pool == nil {
		return nil, fmt.Errorf("%w: shared context capacity pool is required", ErrInvalidOptions)
	}
	if policy.Validate == nil || policy.CapBytes < 0 {
		return nil, fmt.Errorf("%w: shared context capacity policy is invalid", ErrInvalidOptions)
	}
	options = defaultOptions(options)
	if err := options.validate(); err != nil {
		return nil, err
	}

	store := &Store{
		options:           options,
		capacity:          pool,
		policy:            policy,
		nextRecordID:      1,
		nextBlockSequence: 1,
		generation:        newGeneration(0),
	}
	store.mu.Lock()
	lease, err := store.beginMutationLocked(ctx)
	if err != nil {
		store.mu.Unlock()
		return nil, err
	}
	loadErr := func() error {
		info, statErr := lease.Root().Lstat(traceDirName)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := lease.EnsureFits(contextcache.Admission{MetadataEntries: 1}); err != nil {
				return store.mapCapacityError(err)
			}
			if err := lease.Root().Mkdir(traceDirName, 0o700); err != nil {
				return fmt.Errorf("open context trace directory: %w", err)
			}
			info, statErr = lease.Root().Lstat(traceDirName)
		}
		if statErr != nil {
			return fmt.Errorf("stat context trace directory: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: trace directory is not a directory", contextcache.ErrSymlink)
		}
		if err := lease.Root().Chmod(traceDirName, 0o700); err != nil {
			return fmt.Errorf("private context trace directory: %w", err)
		}
		return store.loadLocked(lease)
	}()
	finishErr := lease.Finish()
	store.mu.Unlock()
	if err := errors.Join(loadErr, finishErr); err != nil {
		return nil, err
	}
	return store, nil
}

// UpdatePolicy validates a newly evaluated home policy while holding the
// Store mutex and shared pool lease, then installs it for subsequent writes.
// Validation is deliberately performed by contextcache.Policy.Validate; the
// Store never reimplements declaration freshness. A reduced policy may report
// ErrCapacity for already-retained bytes; that still is a valid policy update
// and closes future admissions until trace evicts its own blocks.
func (store *Store) UpdatePolicy(ctx context.Context, policy contextcache.Policy) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return ErrClosed
	}
	if policy.Validate == nil || policy.CapBytes < 0 {
		return fmt.Errorf("%w: shared context capacity policy is invalid", ErrInvalidOptions)
	}
	lease, err := store.capacity.Begin(ctx, policy)
	if err != nil {
		return err
	}
	finishErr := lease.Finish()
	if finishErr != nil && !errors.Is(finishErr, contextcache.ErrCapacity) {
		return finishErr
	}
	store.policy = policy
	return nil
}

// Generation returns the current disposable cache generation. It remains
// stable after Close so callers can label a final status result.
func (store *Store) Generation() uint64 {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.generation
}

// Stats returns status from bounded authoritative metadata without reading or
// decoding any retained block. It is safe to call after Close.
func (store *Store) Stats() Stats {
	store.mu.Lock()
	defer store.mu.Unlock()
	return Stats{
		Generation:     store.generation,
		DiskCap:        store.policy.CapBytes,
		DiskBytes:      store.usage,
		BlockCount:     len(store.blocks),
		PendingRecords: len(store.pending),
		PendingBytes:   store.pendingBytes,
		KnownLoss:      store.rotatedThrough > 0 || len(store.openGaps) > 0 || len(store.dropGaps) > 0,
	}
}

// Append accepts one bounded event. Content is sampled immediately, so later
// caller mutation cannot affect the pending batch. The batch is bounded and
// flushed when its record or encoded byte limit is reached.
func (store *Store) Append(record Record) (AppendResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return AppendResult{}, ErrClosed
	}

	id := store.nextRecordID
	prepared, err := prepareRecord(record, id, store.generation, store.options)
	if err != nil {
		return store.rejectAppendLocked(err)
	}
	pendingBytesBefore := store.pendingBytes
	pendingBytesAfter := int64(0)
	if len(store.pending) > 0 {
		candidate := make([]preparedRecord, 0, len(store.pending)+1)
		candidate = append(candidate, store.pending...)
		candidate = append(candidate, prepared)
		encoded, encodeErr := encodeBlock(candidate, store.generation, store.nextBlockSequence)
		if encodeErr != nil {
			return store.rejectAppendLocked(encodeErr)
		}
		if len(candidate) > store.options.MaxBlockRecords || len(encoded) > store.options.MaxBlockBytes {
			if err := store.flushLocked(); err != nil {
				return store.rejectAppendLocked(err)
			}
		} else {
			pendingBytesAfter = int64(len(encoded))
		}
	}

	if len(store.pending) == 0 {
		encoded, encodeErr := encodeBlock([]preparedRecord{prepared}, store.generation, store.nextBlockSequence)
		if encodeErr != nil {
			return store.rejectAppendLocked(encodeErr)
		}
		if len(encoded) > store.options.MaxBlockBytes {
			return store.rejectAppendLocked(fmt.Errorf("%w: encoded block is %d bytes, limit is %d", ErrRecordTooLarge, len(encoded), store.options.MaxBlockBytes))
		}
		pendingBytesAfter = int64(len(encoded))
	}

	store.pending = append(store.pending, prepared)
	store.pendingBytes = pendingBytesAfter
	store.nextRecordID++
	result := AppendResult{Accepted: true, Generation: store.generation, RecordID: id, ContributionID: prepared.ContributionID}
	if len(store.pending) >= store.options.MaxBlockRecords {
		if err := store.flushLocked(); err != nil {
			store.pending = store.pending[:len(store.pending)-1]
			store.pendingBytes = pendingBytesBefore
			store.nextRecordID--
			return store.rejectAppendLocked(err)
		}
		result.Persisted = true
	}
	return result, nil
}

func (store *Store) rejectAppendLocked(err error) (AppendResult, error) {
	gap := Gap{Kind: GapDropped, Generation: store.generation, Detail: "record admission rejected: " + boundedDetail(err)}
	store.dropGaps = appendBoundedGap(store.dropGaps, gap)
	return AppendResult{Generation: store.generation, Dropped: gap}, err
}

// Flush writes the bounded pending batch. It is synchronous and has no
// durability promise beyond the successful return from the filesystem write.
func (store *Store) Flush() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return ErrClosed
	}
	return store.flushLocked()
}

// Query returns one bounded page. It flushes the pending batch first so an
// accepted record is inspectable immediately. Corruption and rotation are
// represented in Gaps and do not become a successful empty answer.
func (store *Store) Query(ctx context.Context, query Query) (result QueryResult, returnErr error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return QueryResult{}, ErrClosed
	}
	if err := store.validateQuery(query); err != nil {
		return QueryResult{}, err
	}
	query = normalizeQuery(query, store.options)
	lease, err := store.beginMutationLocked(ctx)
	if err != nil {
		return QueryResult{}, store.mapCapacityError(err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, store.mapCapacityError(lease.Finish()))
	}()
	flushErr := store.flushWithLeaseLocked(lease, nil)
	result = QueryResult{Generation: store.generation, NextAfter: query.After, NextCursor: query.Cursor}
	if query.Generation != 0 && query.Generation != store.generation {
		result.Gaps = append(result.Gaps, Gap{Kind: GapUnreadable, Generation: query.Generation, Detail: "generation is no longer retained"})
		return result, nil
	}

	budget := resultBudget{maxBytes: query.MaxBytes, usedBytes: queryMetadataBytes}
	for _, gap := range store.openGaps {
		budget.addGap(&result, gap)
	}
	for _, gap := range store.dropGaps {
		budget.addGap(&result, gap)
	}
	if store.rotatedThrough > 0 {
		budget.addGap(&result, Gap{Kind: GapRotated, Generation: store.generation, FromRecordID: 1, ToRecordID: store.rotatedThrough, Detail: "older blocks rotated out"})
	}

	matched := 0
	var previousSequence uint64
	var previousRecord uint64
	var sawBlock bool
	lastScanned := query.Cursor
	stop := false
	scannedBytes := int64(0)
	for _, file := range store.blocks {
		if query.Cursor.BlockSequence > 0 && file.sequence < query.Cursor.BlockSequence {
			continue
		}
		if query.Cursor.BlockSequence == file.sequence && query.Cursor.RecordID == ^uint64(0) {
			continue
		}
		if sawBlock && file.sequence > previousSequence+1 {
			budget.addGap(&result, Gap{Kind: GapDropped, Generation: store.generation, BlockSequence: previousSequence + 1, Detail: "block sequence is missing"})
		}
		previousSequence = file.sequence
		sawBlock = true
		charge := file.size
		if charge < blockHeaderSize {
			charge = blockHeaderSize
		}
		if charge > int64(query.MaxScanBytes) {
			if scannedBytes == 0 {
				return QueryResult{}, ScanBudgetError{RequiredBytes: charge, ProvidedBytes: int64(query.MaxScanBytes)}
			}
			result.PartialScan = true
			result.NextCursor = lastScanned
			budget.addGap(&result, Gap{Kind: GapUnreadable, Generation: store.generation, BlockSequence: file.sequence, Detail: "scan byte budget exhausted; more evidence is unknown"})
			break
		}
		if scannedBytes > int64(query.MaxScanBytes)-charge {
			result.PartialScan = true
			result.NextCursor = lastScanned
			budget.addGap(&result, Gap{Kind: GapUnreadable, Generation: store.generation, BlockSequence: file.sequence, Detail: "scan byte budget exhausted; more evidence is unknown"})
			break
		}
		scannedBytes += charge

		block, header, err := readBlock(lease.Root(), file.path, store.options)
		if err != nil {
			gap := Gap{Kind: GapCorrupt, Generation: store.generation, BlockSequence: file.sequence, Detail: boundedDetail(err)}
			if header.Generation != 0 {
				gap.Generation = header.Generation
				gap.FromRecordID = header.FirstRecordID
				gap.ToRecordID = header.LastRecordID
				lastScanned = Cursor{BlockSequence: file.sequence, RecordID: header.LastRecordID}
			} else {
				lastScanned = Cursor{BlockSequence: file.sequence, RecordID: ^uint64(0)}
			}
			budget.addGap(&result, gap)
			result.NextCursor = lastScanned
			continue
		}
		if header.Sequence != file.sequence {
			budget.addGap(&result, Gap{Kind: GapCorrupt, Generation: header.Generation, BlockSequence: file.sequence, FromRecordID: header.FirstRecordID, ToRecordID: header.LastRecordID, Detail: "block filename and header sequence differ"})
			lastScanned = Cursor{BlockSequence: file.sequence, RecordID: header.LastRecordID}
			result.NextCursor = lastScanned
			continue
		}
		if header.Generation != store.generation {
			budget.addGap(&result, Gap{Kind: GapCorrupt, Generation: header.Generation, BlockSequence: file.sequence, FromRecordID: header.FirstRecordID, ToRecordID: header.LastRecordID, Detail: "block belongs to another cache generation"})
			lastScanned = Cursor{BlockSequence: file.sequence, RecordID: header.LastRecordID}
			result.NextCursor = lastScanned
			continue
		}
		if previousRecord > 0 && header.FirstRecordID > previousRecord+1 {
			budget.addGap(&result, Gap{Kind: GapDropped, Generation: store.generation, FromRecordID: previousRecord + 1, ToRecordID: header.FirstRecordID - 1, Detail: "record range is absent between retained blocks"})
		}
		if result.Retained.FirstRecordID == 0 || header.FirstRecordID < result.Retained.FirstRecordID {
			if result.Retained.FirstRecordID == 0 && header.FirstRecordID > 1 && store.rotatedThrough == 0 {
				budget.addGap(&result, Gap{Kind: GapDropped, Generation: store.generation, FromRecordID: 1, ToRecordID: header.FirstRecordID - 1, Detail: "record IDs before the retained range have no block evidence"})
			}
			result.Retained.FirstRecordID = header.FirstRecordID
		}
		if header.LastRecordID > result.Retained.LastRecordID {
			result.Retained.LastRecordID = header.LastRecordID
		}
		result.Retained.Count += uint64(len(block.Records))
		previousRecord = header.LastRecordID

		for _, diskRecord := range block.Records {
			if query.Cursor.BlockSequence == file.sequence && diskRecord.ID <= query.Cursor.RecordID {
				continue
			}
			before := lastScanned
			prepared, decodeErr := decodeRecord(diskRecord, block.Dictionary, header.Generation)
			if decodeErr != nil {
				budget.addGap(&result, Gap{Kind: GapCorrupt, Generation: store.generation, BlockSequence: file.sequence, FromRecordID: diskRecord.ID, ToRecordID: diskRecord.ID, Detail: boundedDetail(decodeErr)})
				lastScanned = Cursor{BlockSequence: file.sequence, RecordID: diskRecord.ID}
				continue
			}
			if prepared.ID <= query.After || !matches(prepared, query) {
				lastScanned = Cursor{BlockSequence: file.sequence, RecordID: prepared.ID}
				continue
			}
			if matched >= query.Limit {
				result.HasMore = true
				result.NextCursor = before
				stop = true
				break
			}
			public := publicRecord(prepared)
			if !budget.addRecord(&result, public, publicRecordBytes(public)) {
				if len(result.Records) == 0 {
					return QueryResult{}, fmt.Errorf("%w: page needs at least %d bytes", ErrQueryBounds, publicRecordBytes(public))
				}
				result.HasMore = true
				result.PartialPage = true
				result.NextCursor = before
				stop = true
				break
			}
			matched++
			result.NextAfter = prepared.ID
			lastScanned = Cursor{BlockSequence: file.sequence, RecordID: prepared.ID}
		}
		if stop {
			break
		}
		lastScanned = Cursor{BlockSequence: file.sequence, RecordID: header.LastRecordID}
		result.NextCursor = lastScanned
	}
	if flushErr != nil {
		budget.addGap(&result, Gap{Kind: GapUnreadable, Generation: store.generation, Detail: "pending batch could not be written: " + boundedDetail(flushErr)})
		for _, prepared := range store.pending {
			if stop {
				break
			}
			if prepared.ID <= query.After || !matches(prepared, query) {
				lastScanned = Cursor{BlockSequence: store.nextBlockSequence, RecordID: prepared.ID}
				continue
			}
			before := lastScanned
			if matched >= query.Limit {
				result.HasMore = true
				result.NextCursor = before
				break
			}
			public := publicRecord(prepared)
			if !budget.addRecord(&result, public, publicRecordBytes(public)) {
				if len(result.Records) == 0 {
					return QueryResult{}, fmt.Errorf("%w: page needs at least %d bytes", ErrQueryBounds, publicRecordBytes(public))
				}
				result.HasMore = true
				result.PartialPage = true
				result.NextCursor = before
				break
			}
			matched++
			result.NextAfter = prepared.ID
			lastScanned = Cursor{BlockSequence: store.nextBlockSequence, RecordID: prepared.ID}
		}
	}
	result.NextCursor = lastScanned
	result.Retained.Generation = store.generation
	if len(store.pending) > 0 {
		if result.Retained.FirstRecordID == 0 || store.pending[0].ID < result.Retained.FirstRecordID {
			result.Retained.FirstRecordID = store.pending[0].ID
		}
		if store.pending[len(store.pending)-1].ID > result.Retained.LastRecordID {
			result.Retained.LastRecordID = store.pending[len(store.pending)-1].ID
		}
		result.Retained.Count += uint64(len(store.pending))
	}
	return result, nil
}

const (
	queryMetadataBytes = 128
	maxResultGaps      = 256
)

type resultBudget struct {
	maxBytes   int
	usedBytes  int
	gapSummary bool
}

func (budget *resultBudget) addRecord(result *QueryResult, record Record, size int) bool {
	if size < 0 || budget.usedBytes > budget.maxBytes-size {
		return false
	}
	budget.usedBytes += size
	result.Records = append(result.Records, record)
	return true
}

func (budget *resultBudget) addGap(result *QueryResult, gap Gap) {
	gapSize := 64 + len(gap.Detail)
	if len(result.Gaps) < maxResultGaps && gapSize >= 0 && budget.usedBytes <= budget.maxBytes-gapSize {
		budget.usedBytes += gapSize
		result.Gaps = append(result.Gaps, Gap{Kind: gap.Kind, Generation: gap.Generation, FromRecordID: gap.FromRecordID, ToRecordID: gap.ToRecordID, BlockSequence: gap.BlockSequence, Detail: strings.Clone(gap.Detail)})
		return
	}
	result.PartialScan = true
	if budget.gapSummary {
		return
	}
	summary := Gap{Kind: GapUnreadable, Generation: gap.Generation, Detail: "additional evidence gaps omitted by the bounded result"}
	summarySize := 64 + len(summary.Detail)
	if len(result.Gaps) < maxResultGaps && budget.usedBytes <= budget.maxBytes-summarySize {
		budget.usedBytes += summarySize
		result.Gaps = append(result.Gaps, summary)
	}
	budget.gapSummary = true
}

// InspectContribution is a named bounded inspection surface for all events
// associated with one contribution.
func (store *Store) InspectContribution(ctx context.Context, id uint64, query Query) (QueryResult, error) {
	query.ContributionID = id
	return store.Query(ctx, query)
}

// InspectTurn is a named bounded inspection surface for one observed turn.
func (store *Store) InspectTurn(ctx context.Context, turn string, query Query) (QueryResult, error) {
	query.Turn = turn
	return store.Query(ctx, query)
}

// InspectProfile is a named bounded inspection surface for one profile value.
func (store *Store) InspectProfile(ctx context.Context, profile string, query Query) (QueryResult, error) {
	query.Profile = profile
	return store.Query(ctx, query)
}

// Clear removes only trace files and starts a new numeric generation. The
// cache directory itself remains available for future records.
func (store *Store) Clear(ctx context.Context) (ClearResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return ClearResult{}, ErrClosed
	}
	lease, err := store.beginMutationLocked(ctx)
	if err != nil {
		return ClearResult{}, store.mapCapacityError(err)
	}
	removed := uint64(len(store.pending))
	previous := store.generation
	operationErr := func() error {
		for _, file := range store.blocks {
			if header, _, err := readHeader(lease.Root(), file.path); err == nil && header.Generation == store.generation {
				removed += uint64(header.RecordCount)
			}
			if err := lease.Root().Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("clear context trace block: %w", err)
			}
			store.usage -= file.size
		}
		if _, err := lease.Refresh(); err != nil {
			return err
		}
		store.generation = newGeneration(previous)
		store.nextRecordID = 1
		store.nextBlockSequence = 1
		store.rotatedThrough = 0
		store.blocks = nil
		store.pending = nil
		store.pendingBytes = 0
		store.openGaps = nil
		store.dropGaps = nil
		store.stateValid = false
		if err := store.writeStateLocked(lease); err != nil {
			return err
		}
		store.stateValid = true
		return nil
	}()
	finishErr := lease.Finish()
	if err := errors.Join(operationErr, store.mapCapacityError(finishErr)); err != nil {
		return ClearResult{PreviousGeneration: previous, Generation: store.generation, RemovedRecords: removed}, err
	}
	return ClearResult{PreviousGeneration: previous, Generation: store.generation, RemovedRecords: removed}, nil
}

// Close flushes the pending batch and joins the store's synchronous lifetime.
// It is idempotent after a successful close; no goroutine is left behind.
func (store *Store) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	err := store.flushLocked()
	if store.stateDirty {
		lease, beginErr := store.beginMutationLocked(nil)
		if beginErr != nil {
			err = errors.Join(err, store.mapCapacityError(beginErr))
		} else {
			stateErr := store.writeStateLocked(lease)
			finishErr := lease.Finish()
			err = errors.Join(err, stateErr, store.mapCapacityError(finishErr))
		}
	}
	store.closed = true
	return err
}

func (store *Store) validateQuery(query Query) error {
	limit := query.Limit
	if limit == 0 {
		limit = store.options.MaxQueryRecords
	}
	maxBytes := query.MaxBytes
	if maxBytes == 0 {
		maxBytes = store.options.MaxQueryBytes
	}
	maxScanBytes := query.MaxScanBytes
	if maxScanBytes == 0 {
		maxScanBytes = store.options.MaxScanBytes
	}
	if limit < 1 || limit > store.options.MaxQueryRecords || maxBytes < queryMetadataBytes || maxBytes > store.options.MaxQueryBytes || maxScanBytes < 1 || maxScanBytes > store.options.MaxScanBytes {
		return fmt.Errorf("%w: limit %d/%d, bytes %d/%d, scan %d/%d", ErrQueryBounds, limit, store.options.MaxQueryRecords, maxBytes, store.options.MaxQueryBytes, maxScanBytes, store.options.MaxScanBytes)
	}
	for name, value := range map[string]string{
		"scope": query.Scope, "audience": query.Audience,
		"turn": query.Turn, "profile": query.Profile, "source": query.Source,
		"contributor": query.Contributor,
	} {
		if len(value) > store.options.MaxStringBytes {
			return fmt.Errorf("%w: %s filter is too long", ErrQueryBounds, name)
		}
	}
	return nil
}

func normalizeQuery(query Query, options Options) Query {
	if query.Limit == 0 {
		query.Limit = options.MaxQueryRecords
	}
	if query.MaxBytes == 0 {
		query.MaxBytes = options.MaxQueryBytes
	}
	if query.MaxScanBytes == 0 {
		query.MaxScanBytes = options.MaxScanBytes
	}
	return query
}

func (store *Store) loadLocked(lease *contextcache.Mutation) error {
	if err := store.cleanupTemps(lease.Root()); err != nil {
		return err
	}
	stateFound, stateValid, err := store.loadState(lease.Root())
	if err != nil {
		return err
	}
	generationKnown := stateValid
	if stateFound && !stateValid {
		store.openGaps = append(store.openGaps, Gap{Kind: GapCorrupt, Generation: store.generation, Detail: "state: cache state could not be decoded"})
	}

	directory, err := lease.Root().Open(traceDirName)
	if err != nil {
		return fmt.Errorf("scan context trace blocks: %w", err)
	}
	defer directory.Close()
	entriesSeen := 0
	for {
		names, readErr := directory.Readdirnames(128)
		for _, name := range names {
			entriesSeen++
			if entriesSeen > store.options.MaxDirectoryEntries {
				return fmt.Errorf("context trace directory has more than %d bounded entries", store.options.MaxDirectoryEntries)
			}
			sequence, ok := parseBlockName(name)
			if !ok {
				continue
			}
			path := filepath.Join(traceDirName, name)
			info, infoErr := lease.Root().Lstat(path)
			if infoErr != nil {
				store.openGaps = appendBoundedGap(store.openGaps, Gap{Kind: GapUnreadable, Generation: store.generation, BlockSequence: sequence, Detail: boundedDetail(infoErr)})
				continue
			}
			if !info.Mode().IsRegular() {
				store.openGaps = appendBoundedGap(store.openGaps, Gap{Kind: GapUnreadable, Generation: store.generation, BlockSequence: sequence, Detail: "block is not a regular file"})
				continue
			}
			if sequence >= store.nextBlockSequence {
				store.nextBlockSequence = sequence + 1
			}
			header, _, headerErr := readHeader(lease.Root(), path)
			if headerErr == nil && !generationKnown {
				store.generation = header.Generation
				generationKnown = true
			}
			if headerErr == nil && header.Generation == store.generation && header.LastRecordID >= store.nextRecordID {
				store.nextRecordID = header.LastRecordID + 1
			}
			if err := store.addLoadedBlock(lease, blockFile{sequence: sequence, path: path, size: info.Size()}, header, headerErr); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("scan context trace blocks: %w", readErr)
		}
	}
	if store.generation == 0 {
		store.generation = newGeneration(0)
	}
	if store.nextRecordID == 0 {
		store.nextRecordID = 1
	}
	slices.SortFunc(store.blocks, func(left, right blockFile) int {
		if left.sequence < right.sequence {
			return -1
		}
		if left.sequence > right.sequence {
			return 1
		}
		return 0
	})
	if err := store.reconcileUsage(lease.Root()); err != nil {
		return err
	}
	if err := store.trimLoadedCache(lease); err != nil {
		return err
	}
	if store.stateDirty {
		if err := store.writeStateLocked(lease); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) loadState(root *os.Root) (found, valid bool, err error) {
	path := filepath.Join(traceDirName, stateName)
	contents, readErr := readBounded(root, path, stateFileSize)
	if errors.Is(readErr, os.ErrNotExist) {
		return false, false, nil
	}
	if readErr != nil {
		return true, false, nil
	}
	state, stateErr := decodeState(contents)
	if stateErr != nil {
		return true, false, nil
	}
	store.generation = state.Generation
	store.rotatedThrough = state.RotatedThrough
	store.stateValid = true
	return true, true, nil
}

func (store *Store) cleanupTemps(root *os.Root) error {
	directory, err := root.Open(traceDirName)
	if err != nil {
		return fmt.Errorf("scan context trace temporary files: %w", err)
	}
	defer directory.Close()
	entriesSeen := 0
	for {
		names, readErr := directory.Readdirnames(128)
		for _, name := range names {
			entriesSeen++
			if entriesSeen > store.options.MaxDirectoryEntries {
				return fmt.Errorf("context trace directory has more than %d bounded entries", store.options.MaxDirectoryEntries)
			}
			if !isOwnedTemporary(name) {
				continue
			}
			if err := root.Remove(filepath.Join(traceDirName, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove stale context trace temporary %q: %w", name, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("scan context trace temporary files: %w", readErr)
		}
	}
}

func isOwnedTemporary(name string) bool {
	return name == stateName+".tmp" || (strings.HasPrefix(name, blockNamePrefix) && strings.HasSuffix(name, blockNameSuffix+".tmp"))
}

func readBounded(root *os.Root, path string, maximum int) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maximum {
		return contents, errors.New("file exceeds bounded read")
	}
	return contents, nil
}

func (store *Store) reconcileUsage(root *os.Root) error {
	directory, err := root.Open(traceDirName)
	if err != nil {
		return fmt.Errorf("reconcile context trace disk usage: %w", err)
	}
	defer directory.Close()
	var usage int64
	entriesSeen := 0
	for {
		names, readErr := directory.Readdirnames(128)
		for _, name := range names {
			entriesSeen++
			if entriesSeen > store.options.MaxDirectoryEntries {
				return fmt.Errorf("context trace directory has more than %d bounded entries", store.options.MaxDirectoryEntries)
			}
			info, infoErr := root.Lstat(filepath.Join(traceDirName, name))
			if infoErr != nil {
				return fmt.Errorf("stat context trace auxiliary %q: %w", name, infoErr)
			}
			if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			if info.Size() > 0 && usage > (1<<63-1)-info.Size() {
				return errors.New("context trace disk usage overflows int64")
			}
			usage += info.Size()
		}
		if errors.Is(readErr, io.EOF) {
			store.usage = usage
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("reconcile context trace disk usage: %w", readErr)
		}
	}
}

func (store *Store) trimLoadedCache(lease *contextcache.Mutation) error {
	trimmed := false
	for {
		usage, err := lease.Refresh()
		if err != nil {
			return err
		}
		if usage.Bytes <= store.policy.CapBytes || len(store.blocks) == 0 {
			break
		}
		oldestIndex := 0
		for index := 1; index < len(store.blocks); index++ {
			if store.blocks[index].sequence < store.blocks[oldestIndex].sequence {
				oldestIndex = index
			}
		}
		oldest := store.blocks[oldestIndex]
		store.noteRemovedBlock(lease.Root(), oldest)
		if err := lease.Root().Remove(oldest.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("trim context trace cache: %w", err)
		}
		store.usage -= oldest.size
		store.blocks = append(store.blocks[:oldestIndex], store.blocks[oldestIndex+1:]...)
		trimmed = true
	}
	usage, err := lease.Refresh()
	if err != nil {
		return err
	}
	if usage.Bytes > store.policy.CapBytes {
		return ErrDiskCap
	}
	if trimmed {
		store.stateDirty = true
	}
	return nil
}

func (store *Store) addLoadedBlock(lease *contextcache.Mutation, file blockFile, header blockHeader, headerErr error) error {
	if len(store.blocks) < store.options.MaxBlocks {
		store.blocks = append(store.blocks, file)
		return nil
	}
	oldestIndex := 0
	for index := 1; index < len(store.blocks); index++ {
		if store.blocks[index].sequence < store.blocks[oldestIndex].sequence {
			oldestIndex = index
		}
	}
	oldest := store.blocks[oldestIndex]
	if file.sequence <= oldest.sequence {
		store.noteRemovedBlockHeader(file.sequence, header, headerErr)
		if err := lease.Root().Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("trim old context trace block: %w", err)
		}
		if _, err := lease.Refresh(); err != nil {
			return err
		}
		return nil
	}
	store.noteRemovedBlock(lease.Root(), oldest)
	if err := lease.Root().Remove(oldest.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("trim old context trace block: %w", err)
	}
	if _, err := lease.Refresh(); err != nil {
		return err
	}
	store.blocks[oldestIndex] = file
	return nil
}

func (store *Store) noteRemovedBlock(root *os.Root, file blockFile) {
	header, _, err := readHeader(root, file.path)
	store.noteRemovedBlockHeader(file.sequence, header, err)
}

func (store *Store) noteRemovedBlockHeader(sequence uint64, header blockHeader, err error) {
	if err != nil {
		gap := Gap{Kind: GapCorrupt, Generation: store.generation, BlockSequence: sequence, Detail: boundedDetail(err)}
		if header.Generation != 0 {
			gap.Generation = header.Generation
			gap.FromRecordID = header.FirstRecordID
			gap.ToRecordID = header.LastRecordID
		}
		store.openGaps = appendBoundedGap(store.openGaps, gap)
		return
	}
	if header.Sequence != sequence {
		store.openGaps = appendBoundedGap(store.openGaps, Gap{Kind: GapCorrupt, Generation: header.Generation, BlockSequence: sequence, FromRecordID: header.FirstRecordID, ToRecordID: header.LastRecordID, Detail: "block filename and header sequence differ"})
		return
	}
	store.noteRotation(header)
}

func (store *Store) noteRotation(header blockHeader) {
	if header.Generation == store.generation && header.LastRecordID > store.rotatedThrough {
		store.rotatedThrough = header.LastRecordID
		store.stateDirty = true
	}
}

func (store *Store) flushLocked() error {
	if len(store.pending) == 0 {
		return nil
	}
	lease, err := store.beginMutationLocked(nil)
	if err != nil {
		return store.mapCapacityError(err)
	}
	operationErr := store.flushWithLeaseLocked(lease, nil)
	finishErr := lease.Finish()
	return errors.Join(operationErr, store.mapCapacityError(finishErr))
}

func (store *Store) flushWithLeaseLocked(lease *contextcache.Mutation, encoded []byte) error {
	if len(store.pending) == 0 {
		return nil
	}
	if len(store.pending) > store.options.MaxBlockRecords {
		return fmt.Errorf("%w: pending record count exceeds block bound", ErrRecordTooLarge)
	}
	var err error
	if len(encoded) == 0 {
		encoded, err = encodeBlock(store.pending, store.generation, store.nextBlockSequence)
		if err != nil {
			return err
		}
	}
	if len(encoded) > store.options.MaxBlockBytes {
		return fmt.Errorf("%w: pending block is %d bytes, limit is %d", ErrRecordTooLarge, len(encoded), store.options.MaxBlockBytes)
	}
	sequence := store.nextBlockSequence
	path := filepath.Join(traceDirName, blockFilename(sequence))
	temporary := path + ".tmp"
	if err := lease.Root().Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale context trace block temporary: %w", err)
	}
	if _, err := lease.Refresh(); err != nil {
		return err
	}
	if err := store.makeRoomLocked(lease, int64(len(encoded))); err != nil {
		return err
	}
	file, err := lease.Root().OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create context trace block: %w", err)
	}
	writeErr := func() error {
		written, err := file.Write(encoded)
		if err != nil {
			return err
		}
		if written != len(encoded) {
			return io.ErrShortWrite
		}
		if err := file.Sync(); err != nil {
			return err
		}
		return file.Close()
	}()
	if writeErr != nil {
		_ = file.Close()
		_ = lease.Root().Remove(temporary)
		return fmt.Errorf("write context trace block: %w", writeErr)
	}
	if err := lease.Root().Rename(temporary, path); err != nil {
		_ = lease.Root().Remove(temporary)
		return fmt.Errorf("install context trace block: %w", err)
	}
	store.blocks = append(store.blocks, blockFile{sequence: sequence, path: path, size: int64(len(encoded))})
	store.usage += int64(len(encoded))
	store.nextBlockSequence++
	store.pending = nil
	store.pendingBytes = 0
	return nil
}

func (store *Store) makeRoomLocked(lease *contextcache.Mutation, additional int64) error {
	if additional < 0 {
		return ErrDiskCap
	}
	rotated := false
	for {
		admissionErr := lease.EnsureFits(contextcache.Admission{
			TempBytes:   additional,
			TempEntries: 1,
		})
		if len(store.blocks) < store.options.MaxBlocks && admissionErr == nil {
			break
		}
		if len(store.blocks) == 0 {
			if admissionErr != nil {
				return store.mapCapacityError(admissionErr)
			}
			return ErrDiskCap
		}
		oldestIndex := 0
		for index := 1; index < len(store.blocks); index++ {
			if store.blocks[index].sequence < store.blocks[oldestIndex].sequence {
				oldestIndex = index
			}
		}
		oldest := store.blocks[oldestIndex]
		store.noteRemovedBlock(lease.Root(), oldest)
		rotated = true
		if removeErr := lease.Root().Remove(oldest.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("rotate context trace block: %w", removeErr)
		}
		store.usage -= oldest.size
		store.blocks = append(store.blocks[:oldestIndex], store.blocks[oldestIndex+1:]...)
		if _, err := lease.Refresh(); err != nil {
			return err
		}
	}
	if rotated {
		// Rotation changes the retained range, so the state replacement and the
		// incoming block must fit together while both temporary files exist.
		state := encodeState(stateDisk{Generation: store.generation, RotatedThrough: store.rotatedThrough})
		if err := lease.EnsureFits(contextcache.Admission{
			TempBytes:   int64(len(state)) + additional,
			TempEntries: 2,
		}); err != nil {
			return store.mapCapacityError(err)
		}
		if err := store.writeStateLocked(lease); err != nil {
			return err
		}
		store.stateValid = true
	}
	if err := lease.EnsureFits(contextcache.Admission{TempBytes: additional, TempEntries: 1}); err != nil {
		return store.mapCapacityError(err)
	}
	return nil
}

func (store *Store) stateSizeLocked(root *os.Root) (int64, error) {
	info, err := root.Lstat(filepath.Join(traceDirName, stateName))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat context trace state: %w", err)
	}
	return info.Size(), nil
}

func (store *Store) writeStateLocked(lease *contextcache.Mutation) error {
	state := encodeState(stateDisk{Generation: store.generation, RotatedThrough: store.rotatedThrough})
	path := filepath.Join(traceDirName, stateName)
	oldSize, err := store.stateSizeLocked(lease.Root())
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := lease.Root().Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale context trace state temporary: %w", err)
	}
	if _, err := lease.Refresh(); err != nil {
		return err
	}
	if err := lease.EnsureFits(contextcache.Admission{TempBytes: int64(len(state)), TempEntries: 1}); err != nil {
		return store.mapCapacityError(err)
	}
	file, err := lease.Root().OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create context trace state: %w", err)
	}
	writeErr := func() error {
		written, err := file.Write(state)
		if err != nil {
			return err
		}
		if written != len(state) {
			return io.ErrShortWrite
		}
		if err := file.Sync(); err != nil {
			return err
		}
		return file.Close()
	}()
	if writeErr != nil {
		_ = file.Close()
		_ = lease.Root().Remove(temporary)
		return fmt.Errorf("write context trace state: %w", writeErr)
	}
	if err := lease.Root().Rename(temporary, path); err != nil {
		_ = lease.Root().Remove(temporary)
		return fmt.Errorf("install context trace state: %w", err)
	}
	store.usage += int64(len(state)) - oldSize
	store.stateValid = true
	store.stateDirty = false
	return nil
}

func (store *Store) beginMutationLocked(ctx context.Context) (*contextcache.Mutation, error) {
	if ctx == nil {
		return store.capacity.BeginDefault(store.policy)
	}
	return store.capacity.Begin(ctx, store.policy)
}

func (store *Store) mapCapacityError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, contextcache.ErrCapacity) {
		return fmt.Errorf("%w: %v", ErrDiskCap, err)
	}
	return err
}

type stateDisk struct {
	Generation     uint64
	RotatedThrough uint64
}

func encodeState(state stateDisk) []byte {
	contents := make([]byte, stateFileSize)
	copy(contents[:8], []byte("WCTXST01"))
	binary.LittleEndian.PutUint16(contents[8:10], 1)
	binary.LittleEndian.PutUint64(contents[12:20], state.Generation)
	binary.LittleEndian.PutUint64(contents[20:28], state.RotatedThrough)
	return contents
}

func decodeState(contents []byte) (stateDisk, error) {
	if len(contents) != stateFileSize || string(contents[:8]) != "WCTXST01" || binary.LittleEndian.Uint16(contents[8:10]) != 1 {
		return stateDisk{}, errors.New("invalid state header")
	}
	state := stateDisk{Generation: binary.LittleEndian.Uint64(contents[12:20]), RotatedThrough: binary.LittleEndian.Uint64(contents[20:28])}
	if state.Generation == 0 {
		return stateDisk{}, errors.New("state generation is zero")
	}
	return state, nil
}

type blockHeader struct {
	Generation    uint64
	Sequence      uint64
	FirstRecordID uint64
	LastRecordID  uint64
	RecordCount   uint32
	PayloadBytes  uint64
	Checksum      uint32
}

func encodeHeader(header blockHeader) []byte {
	contents := make([]byte, blockHeaderSize)
	copy(contents[:8], []byte("WCTXBL01"))
	binary.LittleEndian.PutUint16(contents[8:10], 1)
	binary.LittleEndian.PutUint64(contents[12:20], header.Generation)
	binary.LittleEndian.PutUint64(contents[20:28], header.Sequence)
	binary.LittleEndian.PutUint64(contents[28:36], header.FirstRecordID)
	binary.LittleEndian.PutUint64(contents[36:44], header.LastRecordID)
	binary.LittleEndian.PutUint32(contents[44:48], header.RecordCount)
	binary.LittleEndian.PutUint64(contents[48:56], header.PayloadBytes)
	binary.LittleEndian.PutUint32(contents[56:60], header.Checksum)
	return contents
}

func decodeHeader(contents []byte) (blockHeader, error) {
	if len(contents) < blockHeaderSize {
		return blockHeader{}, io.ErrUnexpectedEOF
	}
	if string(contents[:8]) != "WCTXBL01" || binary.LittleEndian.Uint16(contents[8:10]) != 1 {
		return blockHeader{}, errors.New("invalid block header")
	}
	header := blockHeader{
		Generation:    binary.LittleEndian.Uint64(contents[12:20]),
		Sequence:      binary.LittleEndian.Uint64(contents[20:28]),
		FirstRecordID: binary.LittleEndian.Uint64(contents[28:36]),
		LastRecordID:  binary.LittleEndian.Uint64(contents[36:44]),
		RecordCount:   binary.LittleEndian.Uint32(contents[44:48]),
		PayloadBytes:  binary.LittleEndian.Uint64(contents[48:56]),
		Checksum:      binary.LittleEndian.Uint32(contents[56:60]),
	}
	if header.Generation == 0 || header.Sequence == 0 || header.FirstRecordID == 0 || header.LastRecordID < header.FirstRecordID || header.RecordCount == 0 || header.PayloadBytes == 0 {
		return blockHeader{}, errors.New("invalid block bounds")
	}
	return header, nil
}

func readHeader(root *os.Root, path string) (blockHeader, []byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return blockHeader{}, nil, err
	}
	defer file.Close()
	contents := make([]byte, blockHeaderSize)
	if _, err := io.ReadFull(file, contents); err != nil {
		return blockHeader{}, nil, err
	}
	header, err := decodeHeader(contents)
	return header, contents, err
}

type diskBlock struct {
	Dictionary []string
	Records    []diskRecord
}

type diskRecord struct {
	ID             uint64
	ContributionID uint64
	Timestamp      int64
	Kind           EventKind
	Outcome        OutcomeCode
	Scope          uint32
	Audience       uint32
	Epoch          uint64
	Turn           uint32
	Profile        uint32
	Source         uint32
	Contributor    uint32
	ContentDigest  [32]byte
	Inputs         []diskInput
	Reasons        []diskReason
	Sample         diskSample
}

type diskValue struct {
	Kind   ValueKind
	String uint32
	Int64  int64
	Uint64 uint64
	Bool   bool
}

type diskInput struct {
	Code  uint16
	Key   uint32
	Value diskValue
}

type diskParameter struct {
	Key   uint32
	Value diskValue
}

type diskReason struct {
	Code     uint16
	Provider uint32
	Rule     uint32
	Params   []diskParameter
}

type diskSample struct {
	OriginalBytes uint64
	OmittedBytes  uint64
	Excerpts      []diskExcerpt
}

type diskExcerpt struct {
	Offset        uint64
	Bytes         uint64
	OmittedBefore uint64
	OmittedAfter  uint64
	Text          uint32
}

type stringDictionary struct {
	values []string
	index  map[string]uint32
}

func newStringDictionary() stringDictionary {
	return stringDictionary{values: []string{""}, index: map[string]uint32{"": 0}}
}

func (dictionary *stringDictionary) id(value string) uint32 {
	if value == "" {
		return 0
	}
	if id, ok := dictionary.index[value]; ok {
		return id
	}
	id := uint32(len(dictionary.values))
	dictionary.values = append(dictionary.values, strings.Clone(value))
	dictionary.index[value] = id
	return id
}

func encodeBlock(records []preparedRecord, generation, sequence uint64) ([]byte, error) {
	if len(records) == 0 {
		return nil, errors.New("encode empty context trace block")
	}
	dictionary := newStringDictionary()
	diskRecords := make([]diskRecord, 0, len(records))
	for _, record := range records {
		diskRecords = append(diskRecords, encodeRecord(record, &dictionary))
	}
	var payload bytes.Buffer
	encoder := gob.NewEncoder(&payload)
	if err := encoder.Encode(diskBlock{Dictionary: dictionary.values, Records: diskRecords}); err != nil {
		return nil, fmt.Errorf("encode context trace gob block: %w", err)
	}
	payloadBytes := payload.Bytes()
	header := blockHeader{Generation: generation, Sequence: sequence, FirstRecordID: records[0].ID, LastRecordID: records[len(records)-1].ID, RecordCount: uint32(len(records)), PayloadBytes: uint64(len(payloadBytes)), Checksum: crc32.ChecksumIEEE(payloadBytes)}
	contents := make([]byte, 0, blockHeaderSize+len(payloadBytes))
	contents = append(contents, encodeHeader(header)...)
	contents = append(contents, payloadBytes...)
	return contents, nil
}

func readBlock(root *os.Root, path string, options Options) (diskBlock, blockHeader, error) {
	file, err := root.Open(path)
	if err != nil {
		return diskBlock{}, blockHeader{}, err
	}
	defer file.Close()
	headerBytes := make([]byte, blockHeaderSize)
	if _, err := io.ReadFull(file, headerBytes); err != nil {
		return diskBlock{}, blockHeader{}, err
	}
	header, err := decodeHeader(headerBytes)
	if err != nil {
		return diskBlock{}, blockHeader{}, err
	}
	if header.PayloadBytes > uint64(options.MaxBlockBytes-blockHeaderSize) {
		return diskBlock{}, header, errors.New("block payload exceeds configured bound")
	}
	if uint64(header.RecordCount) > uint64(options.MaxBlockRecords) {
		return diskBlock{}, header, errors.New("block record count exceeds configured bound")
	}
	payload := make([]byte, int(header.PayloadBytes))
	if _, err := io.ReadFull(file, payload); err != nil {
		return diskBlock{}, header, err
	}
	var trailing [1]byte
	if count, trailingErr := file.Read(trailing[:]); trailingErr != io.EOF || count != 0 {
		return diskBlock{}, header, errors.New("block has trailing bytes")
	}
	if crc32.ChecksumIEEE(payload) != header.Checksum {
		return diskBlock{}, header, errors.New("block checksum mismatch")
	}
	var block diskBlock
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&block); err != nil {
		return diskBlock{}, header, fmt.Errorf("decode context trace gob block: %w", err)
	}
	if err := validateDiskBlock(block, header, options); err != nil {
		return diskBlock{}, header, err
	}
	if len(block.Records) == 0 || len(block.Records) != int(header.RecordCount) || len(block.Dictionary) == 0 || block.Dictionary[0] != "" {
		return diskBlock{}, header, errors.New("block record or dictionary count mismatch")
	}
	return block, header, nil
}

func validateDiskBlock(block diskBlock, header blockHeader, options Options) error {
	if len(block.Dictionary) == 0 || block.Dictionary[0] != "" {
		return errors.New("block dictionary has no empty entry")
	}
	if len(block.Dictionary) > options.MaxBlockBytes {
		return errors.New("block dictionary count exceeds bound")
	}
	for _, value := range block.Dictionary {
		if !utf8.ValidString(value) || len(value) > maxInt(options.MaxStringBytes, MaxContributionSampleBytes) {
			return errors.New("block dictionary contains invalid or oversized UTF-8")
		}
	}
	if len(block.Records) == 0 || len(block.Records) > options.MaxBlockRecords || len(block.Records) != int(header.RecordCount) {
		return errors.New("block record count exceeds bound")
	}
	var previousID uint64
	for _, record := range block.Records {
		if record.ID < header.FirstRecordID || record.ID > header.LastRecordID || (previousID != 0 && record.ID <= previousID) {
			return errors.New("block record IDs are outside header bounds")
		}
		previousID = record.ID
		if err := validateDiskRecord(record, block.Dictionary, options); err != nil {
			return err
		}
	}
	return nil
}

func validateDiskRecord(record diskRecord, dictionary []string, options Options) error {
	if record.ID == 0 || record.Kind == 0 {
		return errors.New("decoded record has invalid identity or kind")
	}
	if err := validateDictionaryID(record.Scope, dictionary, options.MaxStringBytes); err != nil {
		return err
	}
	if err := validateDictionaryID(record.Audience, dictionary, options.MaxStringBytes); err != nil {
		return err
	}
	if err := validateDictionaryID(record.Turn, dictionary, options.MaxStringBytes); err != nil {
		return err
	}
	if err := validateDictionaryID(record.Profile, dictionary, options.MaxStringBytes); err != nil {
		return err
	}
	if err := validateDictionaryID(record.Source, dictionary, options.MaxStringBytes); err != nil {
		return err
	}
	if err := validateDictionaryID(record.Contributor, dictionary, options.MaxStringBytes); err != nil {
		return err
	}
	if len(record.Inputs) > options.MaxInputs || len(record.Reasons) > options.MaxReasons {
		return errors.New("nested input/reason count exceeds bound")
	}
	parameterCount := 0
	for _, input := range record.Inputs {
		if err := validateDictionaryID(input.Key, dictionary, options.MaxStringBytes); err != nil {
			return err
		}
		if err := validateDiskValue(input.Value, dictionary, options); err != nil {
			return err
		}
	}
	for _, reason := range record.Reasons {
		if err := validateDictionaryID(reason.Provider, dictionary, options.MaxStringBytes); err != nil {
			return err
		}
		if err := validateDictionaryID(reason.Rule, dictionary, options.MaxStringBytes); err != nil {
			return err
		}
		parameterCount += len(reason.Params)
		if parameterCount > options.MaxParameters {
			return errors.New("nested parameter count exceeds bound")
		}
		for _, parameter := range reason.Params {
			if err := validateDictionaryID(parameter.Key, dictionary, options.MaxStringBytes); err != nil {
				return err
			}
			if err := validateDiskValue(parameter.Value, dictionary, options); err != nil {
				return err
			}
		}
	}
	if record.Sample.OriginalBytes > uint64(options.MaxInputBytes) || record.Sample.OmittedBytes > record.Sample.OriginalBytes || len(record.Sample.Excerpts) > 64 {
		return errors.New("sample bounds are invalid")
	}
	var previousEnd uint64
	var sampled uint64
	for index, excerpt := range record.Sample.Excerpts {
		if err := validateDictionaryID(excerpt.Text, dictionary, MaxContributionSampleBytes); err != nil {
			return err
		}
		textBytes := uint64(len(dictionary[excerpt.Text]))
		if !utf8.ValidString(dictionary[excerpt.Text]) || excerpt.Bytes != textBytes || excerpt.Offset < previousEnd || excerpt.Offset > record.Sample.OriginalBytes || excerpt.Bytes > record.Sample.OriginalBytes-excerpt.Offset || excerpt.OmittedBefore != excerpt.Offset-previousEnd {
			return errors.New("sample excerpt bounds are invalid")
		}
		if index+1 < len(record.Sample.Excerpts) && excerpt.OmittedAfter != 0 {
			return errors.New("non-final sample excerpt has an after-omission qualifier")
		}
		if index+1 == len(record.Sample.Excerpts) && excerpt.OmittedAfter != record.Sample.OriginalBytes-(excerpt.Offset+excerpt.Bytes) {
			return errors.New("final sample excerpt has an invalid after-omission qualifier")
		}
		if excerpt.Bytes > MaxContributionSampleBytes || sampled > MaxContributionSampleBytes-excerpt.Bytes {
			return errors.New("sample excerpt bytes exceed bound")
		}
		sampled += excerpt.Bytes
		previousEnd = excerpt.Offset + excerpt.Bytes
	}
	if sampled > MaxContributionSampleBytes || sampled > record.Sample.OriginalBytes || record.Sample.OmittedBytes != record.Sample.OriginalBytes-sampled {
		return errors.New("sample omission accounting is invalid")
	}
	retainedBytes := 64 + sha256.Size + 24 + len(dictionary[record.Scope]) + len(dictionary[record.Audience]) + len(dictionary[record.Turn]) + len(dictionary[record.Profile]) + len(dictionary[record.Source]) + len(dictionary[record.Contributor])
	for _, input := range record.Inputs {
		retainedBytes += 8 + len(dictionary[input.Key]) + diskValueBytes(input.Value, dictionary)
	}
	for _, reason := range record.Reasons {
		retainedBytes += 8 + len(dictionary[reason.Provider]) + len(dictionary[reason.Rule])
		for _, parameter := range reason.Params {
			retainedBytes += 8 + len(dictionary[parameter.Key]) + diskValueBytes(parameter.Value, dictionary)
		}
	}
	for _, excerpt := range record.Sample.Excerpts {
		retainedBytes += 32 + len(dictionary[excerpt.Text])
	}
	if retainedBytes > options.MaxRecordBytes {
		return errors.New("decoded record exceeds retained byte bound")
	}
	return nil
}

func validateDiskValue(value diskValue, dictionary []string, options Options) error {
	switch value.Kind {
	case ValueString:
		if value.Int64 != 0 || value.Uint64 != 0 || value.Bool {
			return errors.New("string value has inactive scalar fields")
		}
		return validateDictionaryID(value.String, dictionary, options.MaxStringBytes)
	case ValueInt64:
		if value.String != 0 || value.Uint64 != 0 || value.Bool {
			return errors.New("int64 value has inactive scalar fields")
		}
	case ValueUint64:
		if value.String != 0 || value.Int64 != 0 || value.Bool {
			return errors.New("uint64 value has inactive scalar fields")
		}
	case ValueBool:
		if value.String != 0 || value.Int64 != 0 || value.Uint64 != 0 {
			return errors.New("bool value has inactive scalar fields")
		}
	default:
		return errors.New("value kind is invalid")
	}
	return nil
}

func validateDictionaryID(id uint32, dictionary []string, maximum int) error {
	if uint64(id) >= uint64(len(dictionary)) {
		return errors.New("string dictionary reference is invalid")
	}
	if len(dictionary[id]) > maximum {
		return errors.New("string dictionary value exceeds bound")
	}
	return nil
}

func diskValueBytes(value diskValue, dictionary []string) int {
	if value.Kind == ValueString && value.String < uint32(len(dictionary)) {
		return len(dictionary[value.String])
	}
	return 8
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func encodeRecord(record preparedRecord, dictionary *stringDictionary) diskRecord {
	disk := diskRecord{
		ID:             record.ID,
		ContributionID: record.ContributionID,
		ContentDigest:  record.ContentDigest,
		Timestamp:      record.At.UnixNano(),
		Kind:           record.Kind,
		Outcome:        record.Outcome,
		Scope:          dictionary.id(record.Scope),
		Audience:       dictionary.id(record.Audience),
		Epoch:          record.Epoch,
		Turn:           dictionary.id(record.Turn),
		Profile:        dictionary.id(record.Profile),
		Source:         dictionary.id(record.Source),
		Contributor:    dictionary.id(record.Contributor),
		Sample:         diskSample{OriginalBytes: record.Sample.OriginalBytes, OmittedBytes: record.Sample.OmittedBytes},
	}
	for _, input := range record.Inputs {
		disk.Inputs = append(disk.Inputs, diskInput{Code: input.Code, Key: dictionary.id(input.Key), Value: encodeValue(input.Value, dictionary)})
	}
	for _, reason := range record.Reasons {
		diskReasonValue := diskReason{Code: reason.Code, Provider: dictionary.id(reason.Provider), Rule: dictionary.id(reason.Rule)}
		for _, parameter := range reason.Params {
			diskReasonValue.Params = append(diskReasonValue.Params, diskParameter{Key: dictionary.id(parameter.Key), Value: encodeValue(parameter.Value, dictionary)})
		}
		disk.Reasons = append(disk.Reasons, diskReasonValue)
	}
	for _, excerpt := range record.Sample.Excerpts {
		disk.Sample.Excerpts = append(disk.Sample.Excerpts, diskExcerpt{Offset: excerpt.Offset, Bytes: excerpt.Bytes, OmittedBefore: excerpt.OmittedBefore, OmittedAfter: excerpt.OmittedAfter, Text: dictionary.id(excerpt.Text)})
	}
	return disk
}

func encodeValue(value Value, dictionary *stringDictionary) diskValue {
	encoded := diskValue{Kind: value.Kind, Int64: value.Int64, Uint64: value.Uint64, Bool: value.Bool}
	if value.Kind == ValueString {
		encoded.String = dictionary.id(value.String)
	}
	return encoded
}

func decodeValue(value diskValue, dictionary []string) (Value, error) {
	if value.Kind != ValueString && value.Kind != ValueInt64 && value.Kind != ValueUint64 && value.Kind != ValueBool {
		return Value{}, errors.New("value kind is invalid")
	}
	decoded := Value{Kind: value.Kind, Int64: value.Int64, Uint64: value.Uint64, Bool: value.Bool}
	if value.Kind == ValueString {
		if value.String >= uint32(len(dictionary)) {
			return Value{}, errors.New("value string reference is invalid")
		}
		decoded.String = strings.Clone(dictionary[value.String])
	}
	return cloneValue(decoded), nil
}

func decodeRecord(record diskRecord, dictionary []string, generation uint64) (preparedRecord, error) {
	lookup := func(id uint32) (string, error) {
		if uint64(id) >= uint64(len(dictionary)) {
			return "", errors.New("string dictionary reference is invalid")
		}
		return strings.Clone(dictionary[id]), nil
	}
	value := preparedRecord{ID: record.ID, Generation: generation, ContributionID: record.ContributionID, ContentDigest: record.ContentDigest, At: time.Unix(0, record.Timestamp).UTC(), Kind: record.Kind, Outcome: record.Outcome, Epoch: record.Epoch}
	var err error
	if value.Scope, err = lookup(record.Scope); err != nil {
		return preparedRecord{}, err
	}
	if value.Audience, err = lookup(record.Audience); err != nil {
		return preparedRecord{}, err
	}
	if value.Turn, err = lookup(record.Turn); err != nil {
		return preparedRecord{}, err
	}
	if value.Profile, err = lookup(record.Profile); err != nil {
		return preparedRecord{}, err
	}
	if value.Source, err = lookup(record.Source); err != nil {
		return preparedRecord{}, err
	}
	if value.Contributor, err = lookup(record.Contributor); err != nil {
		return preparedRecord{}, err
	}
	for _, input := range record.Inputs {
		key, keyErr := lookup(input.Key)
		if keyErr != nil {
			return preparedRecord{}, keyErr
		}
		inputValue, valueErr := decodeValue(input.Value, dictionary)
		if valueErr != nil {
			return preparedRecord{}, valueErr
		}
		value.Inputs = append(value.Inputs, Input{Code: input.Code, Key: key, Value: inputValue})
	}
	for _, reason := range record.Reasons {
		provider, providerErr := lookup(reason.Provider)
		if providerErr != nil {
			return preparedRecord{}, providerErr
		}
		rule, ruleErr := lookup(reason.Rule)
		if ruleErr != nil {
			return preparedRecord{}, ruleErr
		}
		decodedReason := Reason{Code: reason.Code, Provider: provider, Rule: rule}
		for _, parameter := range reason.Params {
			key, keyErr := lookup(parameter.Key)
			if keyErr != nil {
				return preparedRecord{}, keyErr
			}
			parameterValue, valueErr := decodeValue(parameter.Value, dictionary)
			if valueErr != nil {
				return preparedRecord{}, valueErr
			}
			decodedReason.Params = append(decodedReason.Params, Parameter{Key: key, Value: parameterValue})
		}
		value.Reasons = append(value.Reasons, decodedReason)
	}
	value.Sample = Sample{OriginalBytes: record.Sample.OriginalBytes, OmittedBytes: record.Sample.OmittedBytes}
	for _, excerpt := range record.Sample.Excerpts {
		text, textErr := lookup(excerpt.Text)
		if textErr != nil {
			return preparedRecord{}, textErr
		}
		value.Sample.Excerpts = append(value.Sample.Excerpts, Excerpt{Offset: excerpt.Offset, Bytes: excerpt.Bytes, OmittedBefore: excerpt.OmittedBefore, OmittedAfter: excerpt.OmittedAfter, Text: text})
	}
	return value, nil
}

func prepareRecord(record Record, id, generation uint64, options Options) (preparedRecord, error) {
	if record.Kind == 0 {
		return preparedRecord{}, fmt.Errorf("%w: event kind is zero", ErrInvalidRecord)
	}
	if len(record.Content) > options.MaxInputBytes {
		return preparedRecord{}, fmt.Errorf("%w: content is %d bytes, limit is %d", ErrInputTooLarge, len(record.Content), options.MaxInputBytes)
	}
	if len(record.Inputs) > options.MaxInputs || len(record.Reasons) > options.MaxReasons {
		return preparedRecord{}, fmt.Errorf("%w: input/reason count exceeds bound", ErrRecordTooLarge)
	}
	parameterCount := 0
	structuredBytes := 64
	for _, input := range record.Inputs {
		if err := validateValue(input.Value, options.MaxStringBytes); err != nil {
			return preparedRecord{}, fmt.Errorf("%w: input value: %v", ErrRecordTooLarge, err)
		}
		if len(input.Key) > options.MaxStringBytes {
			return preparedRecord{}, fmt.Errorf("%w: input key exceeds bound", ErrRecordTooLarge)
		}
		structuredBytes += 8 + len(input.Key) + valueBytes(input.Value)
	}
	for _, reason := range record.Reasons {
		if len(reason.Provider) > options.MaxStringBytes || len(reason.Rule) > options.MaxStringBytes {
			return preparedRecord{}, fmt.Errorf("%w: reason string exceeds bound", ErrRecordTooLarge)
		}
		parameterCount += len(reason.Params)
		if parameterCount > options.MaxParameters {
			return preparedRecord{}, fmt.Errorf("%w: parameter count exceeds bound", ErrRecordTooLarge)
		}
		structuredBytes += 8 + len(reason.Provider) + len(reason.Rule)
		for _, parameter := range reason.Params {
			if len(parameter.Key) > options.MaxStringBytes {
				return preparedRecord{}, fmt.Errorf("%w: reason parameter key exceeds bound", ErrRecordTooLarge)
			}
			if err := validateValue(parameter.Value, options.MaxStringBytes); err != nil {
				return preparedRecord{}, fmt.Errorf("%w: reason parameter: %v", ErrRecordTooLarge, err)
			}
			structuredBytes += 8 + len(parameter.Key) + valueBytes(parameter.Value)
		}
	}
	for name, value := range map[string]string{
		"scope": record.Scope, "audience": record.Audience, "turn": record.Turn,
		"profile": record.Profile, "source": record.Source, "contributor": record.Contributor,
	} {
		if len(value) > options.MaxStringBytes {
			return preparedRecord{}, fmt.Errorf("%w: %s exceeds string bound", ErrRecordTooLarge, name)
		}
		structuredBytes += len(value)
	}
	if structuredBytes > options.MaxRecordBytes {
		return preparedRecord{}, fmt.Errorf("%w: structured input is %d bytes, limit is %d", ErrRecordTooLarge, structuredBytes, options.MaxRecordBytes)
	}
	sampleValue, err := sample(record.Content)
	if err != nil {
		return preparedRecord{}, err
	}
	retainedBytes := structuredBytes + sampleBytes(sampleValue) + sha256.Size
	if retainedBytes > options.MaxRecordBytes {
		return preparedRecord{}, fmt.Errorf("%w: retained record is %d bytes, limit is %d", ErrRecordTooLarge, retainedBytes, options.MaxRecordBytes)
	}
	prepared := preparedRecord{ID: id, Generation: generation, Kind: record.Kind, At: record.At.UTC(), Scope: strings.Clone(record.Scope), Audience: strings.Clone(record.Audience), Epoch: record.Epoch, Turn: strings.Clone(record.Turn), Profile: strings.Clone(record.Profile), Source: strings.Clone(record.Source), Contributor: strings.Clone(record.Contributor), ContributionID: record.ContributionID, ContentDigest: sha256.Sum256(record.Content), Outcome: record.Outcome, Sample: sampleValue}
	if prepared.At.IsZero() {
		prepared.At = time.Now().UTC()
	}
	if record.Kind == KindContribution && prepared.ContributionID == 0 {
		prepared.ContributionID = id
	}
	prepared.Inputs = cloneInputs(record.Inputs)
	prepared.Reasons = cloneReasons(record.Reasons)
	return prepared, nil
}

func validateValue(value Value, maxStringBytes int) error {
	switch value.Kind {
	case ValueString:
		if len(value.String) > maxStringBytes {
			return errors.New("string exceeds bound")
		}
	case ValueInt64, ValueUint64, ValueBool:
	default:
		return errors.New("kind is invalid")
	}
	return nil
}

func valueBytes(value Value) int {
	if value.Kind == ValueString {
		return len(value.String)
	}
	return 8
}

func sampleBytes(sampleValue Sample) int {
	size := 24
	for _, excerpt := range sampleValue.Excerpts {
		size += 32 + len(excerpt.Text)
	}
	return size
}

func sample(contents []byte) (Sample, error) {
	if !utf8.Valid(contents) {
		return Sample{}, ErrInvalidUTF8
	}
	original := uint64(len(contents))
	if len(contents) <= MaxContributionSampleBytes {
		return Sample{OriginalBytes: original, Excerpts: []Excerpt{{Offset: 0, Bytes: original, Text: cloneBytesAsString(contents)}}}, nil
	}
	const excerptBytes = MaxContributionSampleBytes / 5
	type interval struct{ start, end int }
	intervals := []interval{{0, excerptBytes}}
	for _, center := range []int{len(contents) / 4, len(contents) / 2, (len(contents) / 4) * 3} {
		intervals = append(intervals, interval{center - excerptBytes/2, center + excerptBytes/2})
	}
	intervals = append(intervals, interval{len(contents) - excerptBytes, len(contents)})
	valid := make([]interval, 0, len(intervals))
	for _, current := range intervals {
		if current.start < 0 {
			current.start = 0
		}
		if current.end > len(contents) {
			current.end = len(contents)
		}
		for current.start < current.end && !utf8Start(contents[current.start]) {
			current.start++
		}
		for current.end > current.start && current.end < len(contents) && !utf8Start(contents[current.end]) {
			current.end--
		}
		if current.start < current.end {
			valid = append(valid, current)
		}
	}
	slices.SortFunc(valid, func(left, right interval) int {
		if left.start != right.start {
			return left.start - right.start
		}
		return left.end - right.end
	})
	merged := make([]interval, 0, len(valid))
	for _, current := range valid {
		if len(merged) > 0 && current.start <= merged[len(merged)-1].end {
			if current.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = current.end
			}
			continue
		}
		merged = append(merged, current)
	}
	result := Sample{OriginalBytes: original}
	var previousEnd int
	var sampled uint64
	for _, current := range merged {
		text := cloneBytesAsString(contents[current.start:current.end])
		result.Excerpts = append(result.Excerpts, Excerpt{Offset: uint64(current.start), Bytes: uint64(current.end - current.start), OmittedBefore: uint64(current.start - previousEnd), Text: text})
		previousEnd = current.end
		sampled += uint64(current.end - current.start)
	}
	if len(result.Excerpts) > 0 {
		result.Excerpts[len(result.Excerpts)-1].OmittedAfter = uint64(len(contents) - previousEnd)
	}
	result.OmittedBytes = original - sampled
	return result, nil
}

func utf8Start(value byte) bool { return value&0xc0 != 0x80 }

func cloneBytesAsString(contents []byte) string {
	if len(contents) == 0 {
		return ""
	}
	copyOfContents := make([]byte, len(contents))
	copy(copyOfContents, contents)
	return string(copyOfContents)
}

func matches(record preparedRecord, query Query) bool {
	return (query.Kind == 0 || record.Kind == query.Kind) &&
		(query.ContributionID == 0 || record.ContributionID == query.ContributionID) &&
		(query.Scope == "" || record.Scope == query.Scope) &&
		(query.Audience == "" || record.Audience == query.Audience) &&
		(query.Epoch == 0 || record.Epoch == query.Epoch) &&
		(query.Turn == "" || record.Turn == query.Turn) &&
		(query.Profile == "" || record.Profile == query.Profile) &&
		(query.Source == "" || record.Source == query.Source) &&
		(query.Contributor == "" || record.Contributor == query.Contributor)
}

func publicRecord(record preparedRecord) Record {
	return Record{ID: record.ID, Generation: record.Generation, Kind: record.Kind, At: record.At.UTC(), Scope: strings.Clone(record.Scope), Audience: strings.Clone(record.Audience), Epoch: record.Epoch, Turn: strings.Clone(record.Turn), Profile: strings.Clone(record.Profile), Source: strings.Clone(record.Source), Contributor: strings.Clone(record.Contributor), ContributionID: record.ContributionID, ContentDigest: record.ContentDigest, Inputs: cloneInputs(record.Inputs), Reasons: cloneReasons(record.Reasons), Outcome: record.Outcome, Sample: cloneSample(record.Sample)}
}

func cloneValue(value Value) Value {
	switch value.Kind {
	case ValueString:
		return Value{Kind: ValueString, String: strings.Clone(value.String)}
	case ValueInt64:
		return Value{Kind: ValueInt64, Int64: value.Int64}
	case ValueUint64:
		return Value{Kind: ValueUint64, Uint64: value.Uint64}
	case ValueBool:
		return Value{Kind: ValueBool, Bool: value.Bool}
	default:
		return Value{}
	}
}

func cloneInputs(inputs []Input) []Input {
	if len(inputs) == 0 {
		return nil
	}
	cloned := make([]Input, len(inputs))
	for index, input := range inputs {
		cloned[index] = Input{Code: input.Code, Key: strings.Clone(input.Key), Value: cloneValue(input.Value)}
	}
	return cloned
}

func cloneReasons(reasons []Reason) []Reason {
	if len(reasons) == 0 {
		return nil
	}
	cloned := make([]Reason, len(reasons))
	for index, reason := range reasons {
		cloned[index] = Reason{Code: reason.Code, Provider: strings.Clone(reason.Provider), Rule: strings.Clone(reason.Rule)}
		if len(reason.Params) > 0 {
			cloned[index].Params = make([]Parameter, len(reason.Params))
			for parameterIndex, parameter := range reason.Params {
				cloned[index].Params[parameterIndex] = Parameter{Key: strings.Clone(parameter.Key), Value: cloneValue(parameter.Value)}
			}
		}
	}
	return cloned
}

func cloneSample(sampleValue Sample) Sample {
	cloned := Sample{OriginalBytes: sampleValue.OriginalBytes, OmittedBytes: sampleValue.OmittedBytes}
	if len(sampleValue.Excerpts) > 0 {
		cloned.Excerpts = make([]Excerpt, len(sampleValue.Excerpts))
		for index, excerpt := range sampleValue.Excerpts {
			cloned.Excerpts[index] = Excerpt{Offset: excerpt.Offset, Bytes: excerpt.Bytes, OmittedBefore: excerpt.OmittedBefore, OmittedAfter: excerpt.OmittedAfter, Text: strings.Clone(excerpt.Text)}
		}
	}
	return cloned
}

func publicRecordBytes(record Record) int {
	size := 128 + sha256.Size + len(record.Scope) + len(record.Audience) + len(record.Turn) + len(record.Profile) + len(record.Source) + len(record.Contributor)
	for _, input := range record.Inputs {
		size += 8 + len(input.Key) + valueBytes(input.Value)
	}
	for _, reason := range record.Reasons {
		size += 8 + len(reason.Provider) + len(reason.Rule)
		for _, parameter := range reason.Params {
			size += 8 + len(parameter.Key) + valueBytes(parameter.Value)
		}
	}
	for _, excerpt := range record.Sample.Excerpts {
		size += 32 + len(excerpt.Text)
	}
	return size
}

func appendBoundedGap(gaps []Gap, gap Gap) []Gap {
	const maxOpenGaps = 256
	if len(gaps) >= maxOpenGaps {
		return gaps
	}
	return append(gaps, gap)
}

func boundedDetail(err error) string {
	const maxDetailBytes = 512
	if err == nil {
		return ""
	}
	detail := err.Error()
	if len(detail) > maxDetailBytes {
		detail = detail[:maxDetailBytes]
		for !utf8.ValidString(detail) {
			detail = detail[:len(detail)-1]
		}
	}
	return strings.Clone(detail)
}

func parseBlockName(name string) (uint64, bool) {
	if !strings.HasPrefix(name, blockNamePrefix) || !strings.HasSuffix(name, blockNameSuffix) {
		return 0, false
	}
	digits := strings.TrimSuffix(strings.TrimPrefix(name, blockNamePrefix), blockNameSuffix)
	if digits == "" {
		return 0, false
	}
	sequence, err := strconv.ParseUint(digits, 10, 64)
	return sequence, err == nil && sequence > 0
}

func blockFilename(sequence uint64) string {
	return fmt.Sprintf("%s%020d%s", blockNamePrefix, sequence, blockNameSuffix)
}

func newGeneration(previous uint64) uint64 {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		generation := binary.LittleEndian.Uint64(bytes[:])
		if generation != 0 && generation != previous {
			return generation
		}
	}
	generation := uint64(time.Now().UnixNano())
	if generation == 0 || generation == previous {
		generation++
	}
	return generation
}
