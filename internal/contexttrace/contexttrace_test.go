package contexttrace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/phosphorco/workbench-go/internal/contextcache"
)

func testOptions() Options {
	return Options{
		MaxBlockBytes:   20_000,
		MaxBlockRecords: 2,
		MaxBlocks:       128,
		MaxRecordBytes:  20_000,
		MaxInputBytes:   2_000_000,
		MaxStringBytes:  256,
		MaxInputs:       8,
		MaxReasons:      8,
		MaxParameters:   16,
		MaxQueryRecords: 32,
		MaxQueryBytes:   100_000,
		MaxScanBytes:    100_000,
	}
}

func openTestStore(t *testing.T, options Options) (*Store, string) {
	t.Helper()
	return openTestStoreWithCap(t, options, 32_000)
}

func openTestStoreWithCap(t *testing.T, options Options, capBytes int64) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := openTestStoreAt(t, root, options, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, filepath.Join(root, "trace")
}

func openTestStoreAt(t *testing.T, root string, options Options, capBytes int64) (*Store, error) {
	t.Helper()
	pool, err := newTestPool(t, root, options)
	if err != nil {
		return nil, err
	}
	return Open(context.Background(), options, pool, contextcache.Policy{
		CapBytes: capBytes,
		Validate: func(context.Context) error { return nil },
	})
}

func newTestPool(t *testing.T, root string, options Options) (*contextcache.Pool, error) {
	t.Helper()
	return contextcache.Open(root, contextcache.Options{
		MaxScanEntries:     options.MaxDirectoryEntries + 16,
		MaxScanDepth:       8,
		MaxSnapshotBytes:   1 << 20,
		MaxSnapshotEntries: 16,
		LockWait:           2 * time.Second,
		LockPoll:           time.Millisecond,
	})
}

func TestSampleIsBoundedValidAndDistributedForMultibyteContent(t *testing.T) {
	store, _ := openTestStore(t, testOptions())
	content := []byte(strings.Repeat("前缀-重要资格-", 2_000))
	if len(content) <= MaxContributionSampleBytes {
		t.Fatalf("test content unexpectedly short: %d", len(content))
	}
	if _, err := store.Append(Record{Kind: KindContribution, Content: content}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Query(context.Background(), Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("got %d records", len(result.Records))
	}
	sampleValue := result.Records[0].Sample
	if sampleValue.OriginalBytes != uint64(len(content)) || sampleValue.OmittedBytes == 0 {
		t.Fatalf("sample size accounting is wrong: %+v", sampleValue)
	}
	var sampled uint64
	for _, excerpt := range sampleValue.Excerpts {
		if !utf8.ValidString(excerpt.Text) || excerpt.Bytes != uint64(len(excerpt.Text)) {
			t.Fatalf("invalid excerpt: %+v", excerpt)
		}
		if excerpt.Offset+excerpt.Bytes > sampleValue.OriginalBytes {
			t.Fatalf("excerpt is outside source: %+v", excerpt)
		}
		sampled += excerpt.Bytes
	}
	if sampled > MaxContributionSampleBytes || sampled+sampleValue.OmittedBytes != sampleValue.OriginalBytes {
		t.Fatalf("sample exceeds/accounting mismatch: sampled=%d sample=%+v", sampled, sampleValue)
	}
	if len(sampleValue.Excerpts) < 3 || sampleValue.Excerpts[0].Offset != 0 || sampleValue.Excerpts[1].OmittedBefore == 0 {
		t.Fatalf("sample did not retain head, distributed middles, and tail: %+v", sampleValue)
	}
}

func TestAppendRetainsTypedReasonsAndCopiesCallerStorage(t *testing.T) {
	store, _ := openTestStore(t, testOptions())
	content := []byte("original contribution")
	inputs := []Input{{Code: 7, Key: "attempt", Value: Int64Value(-3)}}
	reasons := []Reason{{Code: 11, Provider: "provider-a", Rule: "rule-before", Params: []Parameter{{Key: "enabled", Value: BoolValue(true)}, {Key: "score", Value: Uint64Value(42)}}}}
	if _, err := store.Append(Record{Kind: KindContribution, Scope: "project-a", Audience: "audience-a", Epoch: 9, Turn: "turn-a", Profile: "profile-a", Source: "source-a", Contributor: "provider-a", Content: content, Inputs: inputs, Reasons: reasons}); err != nil {
		t.Fatal(err)
	}
	content[0] = 'X'
	inputs[0].Key = "mutated"
	inputs[0].Value.Int64 = 99
	reasons[0].Rule = "mutated-rule"
	reasons[0].Params[0].Value.Bool = false
	result, err := store.Query(context.Background(), Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("got %d records", len(result.Records))
	}
	record := result.Records[0]
	if record.Generation != result.Generation {
		t.Fatalf("record generation was not retained: record=%d result=%d", record.Generation, result.Generation)
	}
	if want := sha256.Sum256([]byte("original contribution")); record.ContentDigest != want {
		t.Fatalf("content fingerprint was not retained: got %x want %x", record.ContentDigest, want)
	}
	if record.Sample.Excerpts[0].Text != "original contribution" || record.Inputs[0].Key != "attempt" || record.Inputs[0].Value != Int64Value(-3) || record.Reasons[0].Rule != "rule-before" || record.Reasons[0].Params[0].Value != BoolValue(true) || record.Reasons[0].Params[1].Value != Uint64Value(42) {
		t.Fatalf("caller mutation leaked into trace: %+v", record)
	}
	// Mutating the result must not mutate the stored record or a later page.
	record.Sample.Excerpts[0].Text = "changed result"
	record.Inputs[0].Key = "changed result"
	record.Reasons[0].Params[0].Value.Bool = false
	again, err := store.Query(context.Background(), Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if again.Records[0].Sample.Excerpts[0].Text != "original contribution" || again.Records[0].Inputs[0].Key != "attempt" || !again.Records[0].Reasons[0].Params[0].Value.Bool {
		t.Fatal("query result aliases retained storage")
	}
}

func TestQueryFiltersPaginationAndPartialScan(t *testing.T) {
	options := testOptions()
	options.MaxBlockRecords = 4
	options.MaxScanBytes = 4_096
	store, directory := openTestStore(t, options)
	for index := 0; index < 5; index++ {
		if _, err := store.Append(Record{Kind: KindQueued, Scope: "scope-a", Audience: "audience-a", Epoch: 10, Turn: "turn-a", Profile: "profile-a", Content: []byte("row-" + string(rune('a'+index)))}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Query(context.Background(), Query{Scope: "scope-a", Turn: "turn-a", Profile: "profile-a", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) != 2 || !first.HasMore || first.NextAfter == 0 {
		t.Fatalf("unexpected first page: %+v", first)
	}
	if first.NextCursor.RecordID != first.Records[len(first.Records)-1].ID {
		t.Fatalf("cursor skipped an unreturned in-block match: %+v", first.NextCursor)
	}
	second, err := store.Query(context.Background(), Query{Scope: "scope-a", Turn: "turn-a", Profile: "profile-a", After: first.NextAfter, Cursor: first.NextCursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Records) != 3 || second.Records[0].ID <= first.NextAfter {
		t.Fatalf("unexpected second page: %+v", second)
	}
	partialPage, err := store.Query(context.Background(), Query{Scope: "scope-a", Limit: 10, MaxBytes: 400})
	if err != nil || len(partialPage.Records) != 1 || !partialPage.PartialPage || !partialPage.HasMore || partialPage.NextCursor.RecordID != partialPage.Records[0].ID {
		t.Fatalf("result byte bound did not preserve resumable progress: err=%v result=%+v", err, partialPage)
	}
	inspected, err := store.InspectTurn(context.Background(), "turn-a", Query{Scope: "scope-a", Profile: "profile-a", Limit: 1})
	if err != nil || len(inspected.Records) != 1 {
		t.Fatalf("named turn inspection failed: err=%v result=%+v", err, inspected)
	}
	files, err := filepath.Glob(filepath.Join(directory, blockNamePrefix+"*"+blockNameSuffix))
	if err != nil || len(files) < 2 {
		t.Fatalf("expected several blocks: %v %d", err, len(files))
	}
	firstInfo, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	partial, err := store.Query(context.Background(), Query{Scope: "scope-a", Limit: 32, MaxScanBytes: int(firstInfo.Size())})
	if err != nil {
		t.Fatal(err)
	}
	if !partial.PartialScan || partial.NextCursor.BlockSequence == 0 {
		t.Fatalf("scan budget did not produce an explicit continuation: %+v", partial)
	}
	resumed, err := store.Query(context.Background(), Query{Scope: "scope-a", Limit: 32, Cursor: partial.NextCursor, MaxScanBytes: options.MaxScanBytes})
	if err != nil || len(resumed.Records) == 0 || resumed.Records[0].ID <= partial.NextCursor.RecordID {
		t.Fatalf("cross-block scan continuation did not progress: err=%v result=%+v", err, resumed)
	}
	tooSmall, err := store.Query(context.Background(), Query{Scope: "scope-a", Limit: 1, MaxScanBytes: int(firstInfo.Size()) - 1})
	var scanErr ScanBudgetError
	if err == nil || !errors.As(err, &scanErr) || !errors.Is(err, ErrScanBudgetTooSmall) || scanErr.RequiredBytes != firstInfo.Size() {
		t.Fatalf("undersized first segment did not return its minimum: err=%v typed=%+v", err, tooSmall)
	}
}

func TestStatsUsesBoundedAuthoritativeMetadata(t *testing.T) {
	options := testOptions()
	options.MaxBlockRecords = 4
	capBytes := int64(32_000)
	store, _ := openTestStore(t, options)
	initial := store.Stats()
	if initial.Generation == 0 || initial.DiskCap != capBytes || initial.BlockCount != 0 || initial.PendingRecords != 0 || initial.KnownLoss {
		t.Fatalf("unexpected initial stats: %+v", initial)
	}
	if _, err := store.Append(Record{Kind: KindObservation, Content: []byte("pending")}); err != nil {
		t.Fatal(err)
	}
	pending := store.Stats()
	if pending.PendingRecords != 1 || pending.PendingBytes <= 0 || pending.BlockCount != 0 || pending.DiskBytes > pending.DiskCap {
		t.Fatalf("pending metadata stats are wrong: %+v", pending)
	}
	for index := 0; index < 3; index++ {
		if _, err := store.Append(Record{Kind: KindObservation, Content: []byte("flushed")}); err != nil {
			t.Fatal(err)
		}
	}
	flushed := store.Stats()
	if flushed.PendingRecords != 0 || flushed.PendingBytes != 0 || flushed.BlockCount != 1 || flushed.DiskBytes <= 0 || flushed.KnownLoss {
		t.Fatalf("flushed metadata stats are wrong: %+v", flushed)
	}
}

func TestRotationClearReopenAndGeneration(t *testing.T) {
	options := testOptions()
	capBytes := int64(4_000)
	options.MaxBlockRecords = 1
	options.MaxBlockBytes = 2_048
	store, directory := openTestStoreWithCap(t, options, capBytes)
	firstGeneration := store.Generation()
	for index := 0; index < 30; index++ {
		if _, err := store.Append(Record{Kind: KindObservation, Scope: "rotating", Content: []byte("small")}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Query(context.Background(), Query{Scope: "rotating", Limit: 32})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) == 0 || len(result.Records) >= 30 || !hasGap(result.Gaps, GapRotated) {
		t.Fatalf("rotation was not explicit/bounded: records=%d gaps=%+v", len(result.Records), result.Gaps)
	}
	if usage := directoryUsage(t, directory); usage > capBytes {
		t.Fatalf("disk cap exceeded: %d > %d", usage, capBytes)
	}
	cleared, err := store.Clear(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cleared.PreviousGeneration != firstGeneration || cleared.Generation == firstGeneration || cleared.RemovedRecords == 0 {
		t.Fatalf("unexpected clear result: %+v", cleared)
	}
	empty, err := store.Query(context.Background(), Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Records) != 0 || empty.Generation != cleared.Generation {
		t.Fatalf("clear retained trace records: %+v", empty)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openTestStoreAt(t, filepath.Dir(directory), options, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Generation() != cleared.Generation {
		t.Fatalf("generation did not survive reopen: got %d want %d", reopened.Generation(), cleared.Generation)
	}
}

func TestFirstRotationAccountsForStateAndIncomingBlock(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "trace")
	options := testOptions()
	options.MaxBlockRecords = 1
	store, err := openTestStoreAt(t, root, options, 32_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Record{Kind: KindObservation, Content: []byte("same-size")}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, blockNamePrefix+"*"+blockNameSuffix))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one measured block: err=%v files=%d", err, len(files))
	}
	blockInfo, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	blockBytes := blockInfo.Size()
	capBytes := 2 * blockBytes
	reopened, err := openTestStoreAt(t, root, options, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Append(Record{Kind: KindObservation, Content: []byte("same-size")}); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Stats().DiskBytes; got > capBytes {
		t.Fatalf("two measured blocks already exceed exact cap: %d > %d", got, capBytes)
	}
	if _, err := reopened.Append(Record{Kind: KindObservation, Content: []byte("same-size")}); !errors.Is(err, ErrDiskCap) {
		t.Fatalf("third append exceeded exact cap instead of being rejected: %v", err)
	}
	if got := reopened.Stats().DiskBytes; got > capBytes || directoryUsage(t, directory) > capBytes {
		t.Fatalf("first-rotation accounting exceeded exact cap: stats=%d dir=%d cap=%d", got, directoryUsage(t, directory), capBytes)
	}
}

func TestLiveFlushEnforcesMaxBlocksAndReportsRotation(t *testing.T) {
	options := testOptions()
	options.MaxBlockRecords = 1
	options.MaxBlocks = 2
	options.MaxDirectoryEntries = 8
	store, directory := openTestStore(t, options)
	for index := 0; index < 12; index++ {
		if _, err := store.Append(Record{Kind: KindQueued, Scope: "bounded-blocks", Content: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Query(context.Background(), Query{Scope: "bounded-blocks", Limit: 32})
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, blockNamePrefix+"*"+blockNameSuffix))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) > options.MaxBlocks || len(result.Records) > options.MaxBlocks || !hasGap(result.Gaps, GapRotated) {
		t.Fatalf("live max-block bound failed: files=%d records=%d gaps=%+v", len(files), len(result.Records), result.Gaps)
	}
	if usage := directoryUsage(t, directory); usage > 32_000 {
		t.Fatalf("live block rotation exceeded disk cap: %d > %d", usage, 32_000)
	}
}

func TestOpenReclaimsOwnedTempsAndTrimsExistingOverCap(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "trace")
	options := testOptions()
	options.MaxBlockRecords = 1
	store, err := openTestStoreAt(t, root, options, 8_000)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 8; index++ {
		if _, err := store.Append(Record{Kind: KindObservation, Content: []byte("startup")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, stateName+".tmp"), make([]byte, 100_000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, blockFilename(999)+".tmp"), make([]byte, 100_000), 0o600); err != nil {
		t.Fatal(err)
	}
	capBytes := int64(1_500)
	reopened, err := openTestStoreAt(t, root, options, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := os.Stat(filepath.Join(directory, stateName+".tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state temp was not reclaimed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, blockFilename(999)+".tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("block temp was not reclaimed: %v", err)
	}
	if usage := directoryUsage(t, directory); usage > capBytes {
		t.Fatalf("startup did not trim existing cache: %d > %d", usage, capBytes)
	}
	result, err := reopened.Query(context.Background(), Query{Limit: 32})
	if err != nil {
		t.Fatal(err)
	}
	if !hasGap(result.Gaps, GapRotated) {
		t.Fatalf("startup trim did not disclose lost history: %+v", result.Gaps)
	}
}

func TestZeroLengthCorruptBlocksConsumeScanBudgetAndGapBound(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "trace")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.MaxScanBytes = 256
	options.MaxDirectoryEntries = 512
	for index := 1; index <= 100; index++ {
		if err := os.WriteFile(filepath.Join(directory, blockFilename(uint64(index))), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := openTestStoreAt(t, root, options, 32_000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	result, err := store.Query(context.Background(), Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !result.PartialScan || len(result.Gaps) == 0 || result.NextCursor.BlockSequence == 0 {
		t.Fatalf("zero-length corruption was not bounded: %+v", result)
	}
	if len(result.Gaps) > maxResultGaps+1 {
		t.Fatalf("gap result grew without bound: %d", len(result.Gaps))
	}
}

func TestCorruptTailIsReportedWithoutBlockingOlderRecords(t *testing.T) {
	options := testOptions()
	options.MaxBlockRecords = 1
	store, directory := openTestStore(t, options)
	for _, content := range []string{"older", "tail"} {
		if _, err := store.Append(Record{Kind: KindConfirmed, Content: []byte(content)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, blockNamePrefix+"*"+blockNameSuffix))
	if err != nil || len(files) != 2 {
		t.Fatalf("expected two blocks: %v %d", err, len(files))
	}
	slices.Sort(files)
	contents, err := os.ReadFile(files[len(files)-1])
	if err != nil {
		t.Fatal(err)
	}
	contents[len(contents)-1] ^= 0x80
	if err := os.WriteFile(files[len(files)-1], contents, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := store.Query(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || result.Records[0].Sample.Excerpts[0].Text != "older" || !hasGap(result.Gaps, GapCorrupt) {
		t.Fatalf("corrupt tail was not explicit: %+v", result)
	}
}

func TestBoundsRejectBeforeRetainingAndCloseIsSafe(t *testing.T) {
	options := testOptions()
	options.MaxStringBytes = 4
	options.MaxInputBytes = 4
	store, _ := openTestStore(t, options)
	dropped, err := store.Append(Record{Kind: KindContribution, Scope: "too-long"})
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("got %v, want record bound error", err)
	}
	if dropped.Dropped.Kind != GapDropped {
		t.Fatalf("rejected admission did not return an explicit gap: %+v", dropped)
	}
	if _, err := store.Append(Record{Kind: KindContribution, Content: []byte("too long")}); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("got %v, want input bound error", err)
	}
	trace, err := store.Query(context.Background(), Query{Limit: 1})
	if err != nil || !hasGap(trace.Gaps, GapDropped) {
		t.Fatalf("rejected admissions were not inspectable as gaps: err=%v result=%+v", err, trace)
	}
	if _, err := store.Query(context.Background(), Query{Limit: options.MaxQueryRecords + 1}); !errors.Is(err, ErrQueryBounds) {
		t.Fatalf("got %v, want query bound error", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Record{Kind: KindFailed}); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v, want closed error", err)
	}
}

func TestInactiveTaggedValueDoesNotRetainItsLargeString(t *testing.T) {
	store, _ := openTestStore(t, testOptions())
	huge := strings.Repeat("not-active", 300_000)
	if _, err := store.Append(Record{Kind: KindObservation, Inputs: []Input{{Key: "number", Value: Value{Kind: ValueInt64, Int64: 1, String: huge}}}}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Query(context.Background(), Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	value := result.Records[0].Inputs[0].Value
	if value != Int64Value(1) {
		t.Fatalf("inactive tagged fields survived normalization: %+v", value)
	}
}

func TestConcurrentAppendQueryAndClearAreRaceSafe(t *testing.T) {
	options := testOptions()
	options.MaxBlockRecords = 4
	store, _ := openTestStore(t, options)
	var group sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for index := 0; index < 50; index++ {
				_, _ = store.Append(Record{Kind: KindObservation, Scope: "race", Turn: "turn", Content: []byte("bounded")})
				_, _ = store.Query(context.Background(), Query{Scope: "race", Limit: 2, MaxScanBytes: options.MaxScanBytes})
			}
		}(worker)
	}
	group.Wait()
	if _, err := store.Query(context.Background(), Query{Scope: "race", Limit: 1}); err != nil {
		t.Fatal(err)
	}
}

func hasGap(gaps []Gap, kind GapKind) bool {
	for _, gap := range gaps {
		if gap.Kind == kind {
			return true
		}
	}
	return false
}

func directoryUsage(t *testing.T, directory string) int64 {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

func TestRecordTimeIsCopiedIntoUTC(t *testing.T) {
	store, _ := openTestStore(t, testOptions())
	location := time.FixedZone("test", -5*60*60)
	when := time.Date(2026, 9, 8, 12, 0, 0, 0, location)
	if _, err := store.Append(Record{Kind: KindObservation, At: when}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Query(context.Background(), Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Records[0].At.Equal(when) || result.Records[0].At.Location() != time.UTC {
		t.Fatalf("timestamp was not normalized: %v", result.Records[0].At)
	}
}

func testPolicy(capBytes int64) contextcache.Policy {
	return contextcache.Policy{CapBytes: capBytes, Validate: func(context.Context) error { return nil }}
}

func poolUsage(t *testing.T, pool *contextcache.Pool, policy contextcache.Policy) contextcache.Usage {
	t.Helper()
	lease, err := pool.Begin(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	usage := lease.Usage()
	if err := lease.Finish(); err != nil {
		t.Fatal(err)
	}
	return usage
}

func snapshotKey(t *testing.T, name string) contextcache.SnapshotKey {
	t.Helper()
	digest := sha256.Sum256([]byte(name))
	key, err := contextcache.NewSnapshotKey(hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSharedPoolSerializesSnapshotAndTraceAdmission(t *testing.T) {
	root := t.TempDir()
	options := testOptions()
	options.MaxBlockRecords = 1
	pool, err := newTestPool(t, root, options)
	if err != nil {
		t.Fatal(err)
	}
	policy := testPolicy(20_000)
	store, err := Open(context.Background(), options, pool, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	key := snapshotKey(t, "shared-capacity")
	payload := bytes.Repeat([]byte("s"), 12_000)
	traceBody := bytes.Repeat([]byte("t"), 9_500)
	errs := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		errs <- pool.SnapshotPublish(context.Background(), policy, key, payload)
	}()
	go func() {
		defer group.Done()
		_, appendErr := store.Append(Record{Kind: KindObservation, Content: traceBody})
		errs <- appendErr
	}()
	group.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if err != nil && !errors.Is(err, ErrDiskCap) && !errors.Is(err, contextcache.ErrCapacity) {
			t.Fatalf("shared admission returned unexpected error: %v", err)
		}
	}
	if successes == 2 {
		t.Fatal("independent snapshot and trace writes both succeeded beyond the shared cap")
	}
	usage := poolUsage(t, pool, policy)
	if usage.Bytes > policy.CapBytes {
		t.Fatalf("shared pool was overspent: usage=%d cap=%d", usage.Bytes, policy.CapBytes)
	}
}

func TestReducedPolicyClosesTraceGrowth(t *testing.T) {
	root := t.TempDir()
	options := testOptions()
	options.MaxBlockRecords = 1
	pool, err := newTestPool(t, root, options)
	if err != nil {
		t.Fatal(err)
	}
	initialPolicy := testPolicy(20_000)
	store, err := Open(context.Background(), options, pool, initialPolicy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Append(Record{Kind: KindObservation, Content: bytes.Repeat([]byte("r"), 4_000)}); err != nil {
		t.Fatal(err)
	}
	reduced := testPolicy(64)
	if err := store.UpdatePolicy(context.Background(), reduced); err != nil {
		t.Fatal(err)
	}
	if got := store.Stats().DiskCap; got != reduced.CapBytes {
		t.Fatalf("reduced policy was not installed: got=%d want=%d", got, reduced.CapBytes)
	}
	if _, err := store.Append(Record{Kind: KindObservation, Content: []byte("must-not-grow")}); !errors.Is(err, ErrDiskCap) {
		t.Fatalf("append under reduced policy returned %v, want ErrDiskCap", err)
	}
	if usage := poolUsage(t, pool, reduced); usage.Bytes > initialPolicy.CapBytes {
		t.Fatalf("reduced-policy cleanup exceeded prior cap: %d", usage.Bytes)
	}
}

func TestUpdatePolicyRejectsStaleCallbackWithoutReplacement(t *testing.T) {
	root := t.TempDir()
	options := testOptions()
	pool, err := newTestPool(t, root, options)
	if err != nil {
		t.Fatal(err)
	}
	initial := testPolicy(20_000)
	store, err := Open(context.Background(), options, pool, initial)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stale := errors.New("captured home changed")
	if err := store.UpdatePolicy(context.Background(), contextcache.Policy{
		CapBytes: 64,
		Validate: func(context.Context) error { return stale },
	}); !errors.Is(err, stale) {
		t.Fatalf("stale policy returned %v, want callback error", err)
	}
	if got := store.Stats().DiskCap; got != initial.CapBytes {
		t.Fatalf("stale policy replaced current cap: got=%d want=%d", got, initial.CapBytes)
	}
}

func TestClearPreservesSnapshotNamespace(t *testing.T) {
	root := t.TempDir()
	options := testOptions()
	options.MaxBlockRecords = 1
	pool, err := newTestPool(t, root, options)
	if err != nil {
		t.Fatal(err)
	}
	policy := testPolicy(20_000)
	store, err := Open(context.Background(), options, pool, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := snapshotKey(t, "clear-preservation")
	payload := []byte("snapshot survives trace clear")
	if err := pool.SnapshotPublish(context.Background(), policy, key, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Record{Kind: KindObservation, Content: []byte("trace-only")}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, hit, err := pool.SnapshotRead(context.Background(), key)
	if err != nil || !hit || !bytes.Equal(got, payload) {
		t.Fatalf("trace clear damaged snapshot: hit=%t err=%v payload=%q", hit, err, got)
	}
	traceFiles, err := filepath.Glob(filepath.Join(root, "trace", blockNamePrefix+"*"+blockNameSuffix))
	if err != nil || len(traceFiles) != 0 {
		t.Fatalf("trace blocks survived clear: err=%v files=%v", err, traceFiles)
	}
	if usage := poolUsage(t, pool, policy); usage.Bytes > policy.CapBytes {
		t.Fatalf("clear left pool over cap: %d > %d", usage.Bytes, policy.CapBytes)
	}
}

func TestTraceMutationHonorsSharedLockTimeout(t *testing.T) {
	root := t.TempDir()
	options := testOptions()
	options.MaxBlockRecords = 1
	pool, err := contextcache.Open(root, contextcache.Options{
		MaxScanEntries:     options.MaxDirectoryEntries + 16,
		MaxScanDepth:       8,
		MaxSnapshotBytes:   1 << 20,
		MaxSnapshotEntries: 16,
		LockWait:           25 * time.Millisecond,
		LockPoll:           time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := testPolicy(20_000)
	store, err := Open(context.Background(), options, pool, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hold, err := pool.Begin(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	_, appendErr := store.Append(Record{Kind: KindObservation, Content: []byte("lock timeout")})
	if err := hold.Finish(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(appendErr, contextcache.ErrLockTimeout) {
		t.Fatalf("blocked trace mutation returned %v, want shared lock timeout", appendErr)
	}
}

func TestQueryHonorsCallerContextWhileSharedLockIsHeld(t *testing.T) {
	root := t.TempDir()
	options := testOptions()
	options.MaxBlockRecords = 1
	pool, err := contextcache.Open(root, contextcache.Options{
		MaxScanEntries:     options.MaxDirectoryEntries + 16,
		MaxScanDepth:       8,
		MaxSnapshotBytes:   1 << 20,
		MaxSnapshotEntries: 16,
		LockWait:           2 * time.Second,
		LockPoll:           time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := testPolicy(20_000)
	store, err := Open(context.Background(), options, pool, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hold, err := pool.Begin(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, queryErr := store.Query(ctx, Query{Limit: 1})
	if !errors.Is(queryErr, context.DeadlineExceeded) {
		t.Fatalf("contextual query returned %v, want context deadline", queryErr)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("contextual query exceeded caller bound: %s", elapsed)
	}
	if err := hold.Finish(); err != nil {
		t.Fatal(err)
	}
}
