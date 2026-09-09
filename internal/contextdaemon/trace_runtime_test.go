package contextdaemon

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/phosphorco/workbench-go/internal/contexttrace"
)

func traceTestOptions() contexttrace.Options {
	return contexttrace.Options{
		DiskCap:             2_000_000,
		MaxBlockBytes:       32 * 1024,
		MaxBlockRecords:     64,
		MaxBlocks:           16,
		MaxDirectoryEntries: 32,
		MaxRecordBytes:      32 * 1024,
		MaxInputBytes:       64 * 1024,
		MaxStringBytes:      1024,
		MaxInputs:           16,
		MaxReasons:          8,
		MaxParameters:       16,
		MaxQueryRecords:     32,
		MaxQueryBytes:       64 * 1024,
		MaxScanBytes:        64 * 1024,
	}
}

func openTraceTestStore(t *testing.T) (*contexttrace.Store, string) {
	t.Helper()
	directory := t.TempDir()
	store, err := contexttrace.Open(directory, traceTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	return store, directory
}

func startTraceTestWorker(t *testing.T, trace *traceRuntime) {
	t.Helper()
	stop := make(chan struct{})
	go trace.run(stop)
	t.Cleanup(func() {
		close(stop)
		trace.wait()
	})
}

func waitForTraceMailbox(t *testing.T, trace *traceRuntime, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if queued, _, _ := trace.snapshot(); int(queued) == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("trace mailbox did not reach %d entries", want)
}

func oversizedTraceRecord(size int) contexttrace.Record {
	return contexttrace.Record{Kind: contexttrace.KindContribution, Content: make([]byte, size)}
}

func traceDropMarkerCount(t *testing.T, record contexttrace.Record) uint64 {
	t.Helper()
	for _, input := range record.Inputs {
		if input.Key == "trace.queue-drop.records" && input.Value.Kind == contexttrace.ValueUint64 {
			return input.Value.Uint64
		}
	}
	return 0
}

func TestCloneTraceRecordOwnsNestedValuesAndNormalizesInactiveFields(t *testing.T) {
	backing := strings.Repeat("x", 1<<20)
	key := backing[100:120]
	valueText := backing[200:220]
	provider := backing[300:320]
	rule := backing[400:420]
	parameterKey := backing[500:520]
	inactive := backing[600:700]

	record := contexttrace.Record{
		Kind:        contexttrace.KindObservation,
		Scope:       backing[700:720],
		Audience:    backing[800:820],
		Turn:        backing[900:920],
		Profile:     backing[1000:1020],
		Source:      backing[1100:1120],
		Contributor: backing[1200:1220],
		Content:     []byte("caller-owned"),
		Inputs: []contexttrace.Input{
			{Code: 7, Key: key, Value: contexttrace.StringValue(valueText)},
			{Code: 8, Key: key, Value: contexttrace.Value{Kind: contexttrace.ValueInt64, Int64: 7, String: inactive, Bool: true, Uint64: 9}},
		},
		Reasons: []contexttrace.Reason{{
			Code:     9,
			Provider: provider,
			Rule:     rule,
			Params:   []contexttrace.Parameter{{Key: parameterKey, Value: contexttrace.BoolValue(true)}},
		}},
	}

	clone := cloneTraceRecord(record)
	record.Content[0] = 'X'
	if string(clone.Content) != "caller-owned" {
		t.Fatalf("clone retained caller content: %q", clone.Content)
	}
	if clone.Inputs[0].Value.String != valueText || clone.Reasons[0].Provider != provider || clone.Reasons[0].Rule != rule || clone.Reasons[0].Params[0].Key != parameterKey {
		t.Fatalf("nested values were not copied: %+v", clone)
	}
	if clone.Inputs[1].Value != contexttrace.Int64Value(7) {
		t.Fatalf("inactive tagged value fields survived: %+v", clone.Inputs[1].Value)
	}
	if unsafe.StringData(clone.Inputs[0].Key) == unsafe.StringData(key) || unsafe.StringData(clone.Inputs[0].Value.String) == unsafe.StringData(valueText) || unsafe.StringData(clone.Reasons[0].Provider) == unsafe.StringData(provider) {
		t.Fatal("clone still points into a large caller string backing array")
	}
}

func TestTraceAdmissionBoundsMetadataBeforeCopying(t *testing.T) {
	trace := newTraceRuntime(nil, nil, 4, 512)
	trace.append(contexttrace.Record{Kind: contexttrace.KindObservation, Content: []byte("small")})
	trace.append(contexttrace.Record{
		Kind:   contexttrace.KindObservation,
		Inputs: []contexttrace.Input{{Key: strings.Repeat("metadata", 1000), Value: contexttrace.Int64Value(1)}},
	})

	queued, queueBytes, dropped := trace.snapshot()
	if queued != 1 || queueBytes > trace.queueCap || dropped != 1 {
		t.Fatalf("metadata admission exceeded bounds: queued=%d bytes=%d dropped=%d cap=%d", queued, queueBytes, dropped, trace.queueCap)
	}
	if trace.pendingDrop.records != 1 || len(trace.mailbox) != 1 {
		t.Fatalf("rejected metadata was not represented as one aggregate gap: pending=%+v mailbox=%d", trace.pendingDrop, len(trace.mailbox))
	}
	entry := (<-trace.mailbox).entry
	if entry == nil || len(entry.record.Inputs) != 0 {
		t.Fatalf("unexpected retained record after oversized metadata rejection: %+v", entry)
	}
}

func TestTraceSamplePreservesUTF8OffsetsAndOmissions(t *testing.T) {
	store, _ := openTraceTestStore(t)
	trace := newTraceRuntime(store, nil, 4, 64*1024)
	startTraceTestWorker(t, trace)
	content := []byte(strings.Repeat("界", 5001))
	trace.append(contexttrace.Record{Kind: contexttrace.KindContribution, Content: content})

	result, err := trace.query(context.Background(), contexttrace.Query{Limit: 4, MaxBytes: 64 * 1024, MaxScanBytes: 64 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("got %d records, want one: %+v", len(result.Records), result)
	}
	sample := result.Records[0].Sample
	if sample.OriginalBytes != uint64(len(content)) || sample.OmittedBytes == 0 {
		t.Fatalf("sample did not disclose omitted bytes: %+v", sample)
	}
	var sampled uint64
	for _, excerpt := range sample.Excerpts {
		if !utf8.ValidString(excerpt.Text) || excerpt.Bytes != uint64(len(excerpt.Text)) || excerpt.Offset+excerpt.Bytes > sample.OriginalBytes {
			t.Fatalf("invalid UTF-8 sample excerpt: %+v", excerpt)
		}
		sampled += excerpt.Bytes
	}
	if sampled > contexttrace.MaxContributionSampleBytes || sample.OmittedBytes != sample.OriginalBytes-sampled {
		t.Fatalf("sample bound/omission accounting is wrong: %+v sampled=%d", sample, sampled)
	}
}

func TestTraceFIFOAggregatesDropsAndClearResetsGeneration(t *testing.T) {
	store, _ := openTraceTestStore(t)
	first := contexttrace.Record{Kind: contexttrace.KindObservation, Scope: "fifo", Content: []byte("first")}
	last := contexttrace.Record{Kind: contexttrace.KindObservation, Scope: "fifo", Content: []byte("last")}
	queueCap := traceEntryBytes(first) + traceEntryBytes(last) + traceDropSummaryBytes
	trace := newTraceRuntime(store, nil, 4, queueCap)
	trace.append(first)
	trace.append(oversizedTraceRecord(int(queueCap) + 1))
	trace.append(last)
	startTraceTestWorker(t, trace)

	query := contexttrace.Query{Limit: 8, MaxBytes: 64 * 1024, MaxScanBytes: 64 * 1024}
	result, err := trace.query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 3 || string(result.Records[0].Sample.Excerpts[0].Text) != "first" || string(result.Records[2].Sample.Excerpts[0].Text) != "last" || traceDropMarkerCount(t, result.Records[1]) != 1 {
		t.Fatalf("drop marker did not preserve FIFO or typed count: %+v", result)
	}
	if result.Records[1].ID == 1 || result.Records[1].Inputs[0].Value.Kind != contexttrace.ValueUint64 {
		t.Fatalf("aggregate count was confused with generated record identity: %+v", result.Records[1])
	}
	_, _, dropped := trace.snapshot()
	if dropped != 1 {
		t.Fatalf("operational dropped count=%d, want 1", dropped)
	}

	oldGeneration := result.Generation
	cleared, err := trace.clear(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Generation == oldGeneration {
		t.Fatalf("clear did not start a new trace generation: %+v", cleared)
	}
	empty, err := trace.query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Generation != cleared.Generation || len(empty.Records) != 0 || empty.Retained.Count != 0 {
		t.Fatalf("clear left trace evidence or stale generation: %+v", empty)
	}

	trace.append(oversizedTraceRecord(int(queueCap) + 2))
	afterClear, err := trace.query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterClear.Records) != 1 || afterClear.Generation != cleared.Generation || traceDropMarkerCount(t, afterClear.Records[0]) != 1 {
		t.Fatalf("post-clear loss was not reported in the new generation: %+v", afterClear)
	}
	_, _, dropped = trace.snapshot()
	if dropped != 2 {
		t.Fatalf("cumulative operational dropped count=%d, want 2", dropped)
	}
}

func TestTraceCallCapturesDropSummaryAtAdmission(t *testing.T) {
	store, _ := openTraceTestStore(t)
	trace := newTraceRuntime(store, nil, 4, 512)
	trace.append(oversizedTraceRecord(2048))

	type callResult struct {
		value any
		err   error
	}
	completed := make(chan callResult, 1)
	go func() {
		value, err := trace.call(context.Background(), func(store *contexttrace.Store) (any, error) {
			return store.Query(contexttrace.Query{Limit: 4, MaxBytes: 64 * 1024, MaxScanBytes: 64 * 1024})
		})
		completed <- callResult{value: value, err: err}
	}()
	waitForTraceMailbox(t, trace, 1)
	trace.append(oversizedTraceRecord(2049))
	message := <-trace.mailbox
	if message.call == nil || message.call.droppedBefore.records != 1 {
		t.Fatalf("call did not capture the preceding drop summary: %+v", message)
	}
	trace.dispatch(message)
	result := <-completed
	if result.err != nil {
		t.Fatal(result.err)
	}
	queryResult := result.value.(contexttrace.QueryResult)
	if len(queryResult.Records) != 1 || traceDropMarkerCount(t, queryResult.Records[0]) != 1 {
		t.Fatalf("call used future loss instead of its admission-time summary: %+v", queryResult)
	}
	if trace.pendingDrop.records != 1 {
		t.Fatalf("later drop was consumed by an earlier call: %+v", trace.pendingDrop)
	}
	trace.stopAndWait()
	trace.wait()
}

func TestTraceCallTimeoutOnFullMailboxReleasesMutex(t *testing.T) {
	store, _ := openTraceTestStore(t)
	trace := newTraceRuntime(store, nil, 1, 4096)
	trace.append(contexttrace.Record{Kind: contexttrace.KindObservation, Content: []byte("queued")})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := trace.call(ctx, func(store *contexttrace.Store) (any, error) { return store.Stats(), nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("saturated call returned %v, want deadline", err)
	}
	snapshotDone := make(chan struct{})
	go func() {
		trace.snapshot()
		close(snapshotDone)
	}()
	select {
	case <-snapshotDone:
	case <-time.After(time.Second):
		t.Fatal("trace mutex remained held after saturated call timeout")
	}
	trace.stopAndWait()
	trace.wait()
}

func TestTraceShutdownFlushesLossOnlyAggregate(t *testing.T) {
	store, directory := openTraceTestStore(t)
	trace := newTraceRuntime(store, nil, 4, 512)
	trace.append(oversizedTraceRecord(2048))
	trace.stopAndWait()
	trace.wait()

	reopened, err := contexttrace.Open(directory, traceTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Query(contexttrace.Query{Limit: 4, MaxBytes: 64 * 1024, MaxScanBytes: 64 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || traceDropMarkerCount(t, result.Records[0]) != 1 {
		t.Fatalf("loss-only shutdown aggregate was lost: %+v", result)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
