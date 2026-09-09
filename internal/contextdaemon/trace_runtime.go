package contextdaemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/phosphorco/workbench-go/internal/contexttrace"
)

var errTraceUnavailable = errors.New("contextdaemon: explanation cache is unavailable")

// traceRuntime is the only owner of a contexttrace.Store. Hook code performs
// only bounded, non-blocking enqueue; query/inspect/clear are synchronous
// calls routed through the same worker so no second process-local Store owner
// can race disk operations.
type traceRuntime struct {
	mu         sync.Mutex
	store      *contexttrace.Store
	openErr    error
	mailbox    chan traceMessage
	queueCap   uint64
	queueBytes uint64
	// dropped is a process-lifetime operational count. Clear resets the Store
	// trace generation but does not erase this status counter.
	dropped     uint64
	pendingDrop traceDropSummary
	space       chan struct{}
	done        chan struct{}
	doneOnce    sync.Once
	storeOnce   sync.Once
	started     bool
	stopped     bool
}

type traceEntry struct {
	record        contexttrace.Record
	bytes         uint64
	droppedBefore traceDropSummary
}

// traceDropSummary is deliberately a fixed-size aggregate. It records queue
// admission loss without turning a burst of rejected records into a second
// unbounded trace stream.
type traceDropSummary struct {
	records uint64
	bytes   uint64
	lastAt  time.Time
}

const traceDropSummaryBytes uint64 = 40

type traceMessage struct {
	entry *traceEntry
	call  *traceCall
}

type traceCall struct {
	fn            func(*contexttrace.Store) (any, error)
	result        chan traceCallResult
	droppedBefore traceDropSummary
}

type traceCallResult struct {
	value any
	err   error
}

func newTraceRuntime(store *contexttrace.Store, openErr error, maxItems uint32, maxBytes uint64) *traceRuntime {
	if maxItems == 0 {
		maxItems = defaultTraceQueueItems
	}
	if maxBytes == 0 {
		maxBytes = defaultTraceQueueBytes
	}
	return &traceRuntime{
		store: store, openErr: openErr,
		mailbox: make(chan traceMessage, maxItems), queueCap: maxBytes,
		space: make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
}

func (trace *traceRuntime) run(stop <-chan struct{}) {
	trace.mu.Lock()
	trace.started = true
	if trace.stopped {
		trace.mu.Unlock()
		trace.finish()
		return
	}
	trace.mu.Unlock()
	for {
		select {
		case message := <-trace.mailbox:
			trace.dispatch(message)
		case <-stop:
			trace.mu.Lock()
			trace.stopped = true
			trace.mu.Unlock()
			trace.drain()
			trace.finish()
			return
		}
	}
}

func (trace *traceRuntime) write(entry traceEntry) {
	trace.mu.Lock()
	store := trace.store
	if trace.queueBytes >= entry.bytes {
		trace.queueBytes -= entry.bytes
	} else {
		trace.queueBytes = 0
	}
	trace.mu.Unlock()
	if store == nil {
		trace.mu.Lock()
		trace.dropped++
		trace.mu.Unlock()
		return
	}
	trace.writeDropMarker(store, entry.droppedBefore)
	if _, err := store.Append(entry.record); err != nil {
		trace.mu.Lock()
		trace.dropped++
		trace.mu.Unlock()
	}
}

func (trace *traceRuntime) drain() {
	for {
		trace.mu.Lock()
		queueEmpty := len(trace.mailbox) == 0
		trace.mu.Unlock()
		if queueEmpty {
			return
		}
		select {
		case message := <-trace.mailbox:
			trace.dispatch(message)
		}
	}
}

func (trace *traceRuntime) dispatch(message traceMessage) {
	trace.signalSpace()
	if message.entry != nil {
		trace.write(*message.entry)
		return
	}
	if message.call != nil {
		trace.execute(*message.call)
	}
}

func (trace *traceRuntime) execute(call traceCall) {
	trace.mu.Lock()
	store := trace.store
	openErr := trace.openErr
	trace.mu.Unlock()
	if store == nil {
		call.result <- traceCallResult{err: errors.Join(errTraceUnavailable, openErr)}
		return
	}
	trace.writeDropMarker(store, call.droppedBefore)
	value, err := call.fn(store)
	call.result <- traceCallResult{value: value, err: err}
}

func (trace *traceRuntime) finish() {
	trace.mu.Lock()
	trace.stopped = true
	store := trace.store
	pending := trace.pendingDrop
	trace.pendingDrop = traceDropSummary{}
	trace.mu.Unlock()
	trace.writeDropMarker(store, pending)
	trace.storeOnce.Do(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	trace.doneOnce.Do(func() { close(trace.done) })
}

func (trace *traceRuntime) stopAndWait() {
	trace.mu.Lock()
	if trace.stopped {
		trace.mu.Unlock()
		return
	}
	trace.stopped = true
	store := trace.store
	started := trace.started
	pending := trace.pendingDrop
	if !started {
		trace.pendingDrop = traceDropSummary{}
	}
	trace.mu.Unlock()
	if !started {
		trace.writeDropMarker(store, pending)
		trace.storeOnce.Do(func() {
			if store != nil {
				_ = store.Close()
			}
		})
		trace.doneOnce.Do(func() { close(trace.done) })
	}
}

func (trace *traceRuntime) wait() { <-trace.done }

func (trace *traceRuntime) append(record contexttrace.Record) {
	// Measure borrowed input before copying it. A rejected oversized body must
	// not transiently become a large queue allocation.
	entryBytes := traceEntryBytes(record)
	trace.mu.Lock()
	markerBytes := uint64(0)
	if trace.pendingDrop.records != 0 {
		markerBytes = traceDropSummaryBytes
	}
	if entryBytes > ^uint64(0)-markerBytes {
		entryBytes = ^uint64(0)
	} else {
		entryBytes += markerBytes
	}
	if trace.stopped {
		if trace.dropped != ^uint64(0) {
			trace.dropped++
		}
		trace.mu.Unlock()
		return
	}
	if len(trace.mailbox) >= cap(trace.mailbox) || entryBytes > trace.queueCap || trace.queueBytes > trace.queueCap-entryBytes {
		trace.noteDropLocked(record, traceEntryBytes(record))
		trace.mu.Unlock()
		return
	}
	entry := traceEntry{bytes: entryBytes, droppedBefore: trace.pendingDrop}
	entry.record = cloneTraceRecord(record)
	trace.pendingDrop = traceDropSummary{}
	trace.mailbox <- traceMessage{entry: &entry}
	trace.queueBytes += entry.bytes
	trace.mu.Unlock()
}

func (trace *traceRuntime) call(ctx context.Context, fn func(*contexttrace.Store) (any, error)) (any, error) {
	if ctx == nil {
		return nil, errTraceUnavailable
	}
	result := make(chan traceCallResult, 1)
	for {
		trace.mu.Lock()
		if trace.stopped || trace.store == nil {
			err := errors.Join(errTraceUnavailable, trace.openErr)
			trace.mu.Unlock()
			return nil, err
		}
		call := &traceCall{fn: fn, result: result, droppedBefore: trace.pendingDrop}
		message := traceMessage{call: call}
		select {
		case trace.mailbox <- message:
			trace.pendingDrop = traceDropSummary{}
			trace.mu.Unlock()
			goto sent
		default:
			space, done := trace.space, trace.done
			trace.mu.Unlock()
			select {
			case <-space:
				continue
			case <-done:
				return nil, errTraceUnavailable
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

sent:
	select {
	case value := <-result:
		return value.value, value.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (trace *traceRuntime) snapshot() (uint32, uint64, uint64) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return uint32(len(trace.mailbox)), trace.queueBytes, trace.dropped
}

func (trace *traceRuntime) stats(ctx context.Context) (contexttrace.Stats, error) {
	value, err := trace.call(ctx, func(store *contexttrace.Store) (any, error) { return store.Stats(), nil })
	if err != nil {
		return contexttrace.Stats{}, err
	}
	return value.(contexttrace.Stats), nil
}

func (trace *traceRuntime) query(ctx context.Context, query contexttrace.Query) (contexttrace.QueryResult, error) {
	value, err := trace.call(ctx, func(store *contexttrace.Store) (any, error) { return store.Query(query) })
	if err != nil {
		return contexttrace.QueryResult{}, err
	}
	return value.(contexttrace.QueryResult), nil
}

func (trace *traceRuntime) inspectContribution(ctx context.Context, id uint64, query contexttrace.Query) (contexttrace.QueryResult, error) {
	value, err := trace.call(ctx, func(store *contexttrace.Store) (any, error) { return store.InspectContribution(id, query) })
	if err != nil {
		return contexttrace.QueryResult{}, err
	}
	return value.(contexttrace.QueryResult), nil
}

func (trace *traceRuntime) inspectTurn(ctx context.Context, turn string, query contexttrace.Query) (contexttrace.QueryResult, error) {
	value, err := trace.call(ctx, func(store *contexttrace.Store) (any, error) { return store.InspectTurn(turn, query) })
	if err != nil {
		return contexttrace.QueryResult{}, err
	}
	return value.(contexttrace.QueryResult), nil
}

func (trace *traceRuntime) inspectProfile(ctx context.Context, profile string, query contexttrace.Query) (contexttrace.QueryResult, error) {
	value, err := trace.call(ctx, func(store *contexttrace.Store) (any, error) { return store.InspectProfile(profile, query) })
	if err != nil {
		return contexttrace.QueryResult{}, err
	}
	return value.(contexttrace.QueryResult), nil
}

func (trace *traceRuntime) clear(ctx context.Context) (contexttrace.ClearResult, error) {
	value, err := trace.call(ctx, func(store *contexttrace.Store) (any, error) { return store.Clear() })
	if err != nil {
		return contexttrace.ClearResult{}, err
	}
	return value.(contexttrace.ClearResult), nil
}

func (trace *traceRuntime) signalSpace() {
	select {
	case trace.space <- struct{}{}:
	default:
	}
}

func traceEntryBytes(record contexttrace.Record) uint64 {
	value := uint64(256)
	for _, text := range []string{record.Scope, record.Audience, record.Turn, record.Profile, record.Source, record.Contributor} {
		value = addTraceBytes(value, uint64(len(text)))
	}
	value = addTraceBytes(value, uint64(len(record.Content)))
	for _, input := range record.Inputs {
		value = addTraceBytes(value, 24+uint64(len(input.Key))+traceValueBytes(input.Value))
	}
	for _, reason := range record.Reasons {
		value = addTraceBytes(value, 32+uint64(len(reason.Provider))+uint64(len(reason.Rule)))
		for _, parameter := range reason.Params {
			value = addTraceBytes(value, 24+uint64(len(parameter.Key))+traceValueBytes(parameter.Value))
		}
	}
	return value
}

func addTraceBytes(total, addition uint64) uint64 {
	if ^uint64(0)-total < addition {
		return ^uint64(0)
	}
	return total + addition
}

func traceValueBytes(value contexttrace.Value) uint64 {
	return 16 + uint64(len(value.String))
}

func cloneTraceRecord(input contexttrace.Record) contexttrace.Record {
	result := input
	// Store.Append performs the product sampler and computes the complete
	// input digest. The queue itself is bounded by traceEntryBytes, so passing
	// the complete admitted body here does not create an unbounded retention
	// path or destroy the sampler's tail/omission metadata.
	result.Scope = strings.Clone(input.Scope)
	result.Audience = strings.Clone(input.Audience)
	result.Turn = strings.Clone(input.Turn)
	result.Profile = strings.Clone(input.Profile)
	result.Source = strings.Clone(input.Source)
	result.Contributor = strings.Clone(input.Contributor)
	result.Content = append([]byte(nil), input.Content...)
	result.Inputs = make([]contexttrace.Input, len(input.Inputs))
	for i, item := range input.Inputs {
		result.Inputs[i] = contexttrace.Input{Code: item.Code, Key: strings.Clone(item.Key), Value: cloneTraceValue(item.Value)}
	}
	result.Reasons = make([]contexttrace.Reason, len(input.Reasons))
	for i, reason := range input.Reasons {
		result.Reasons[i] = contexttrace.Reason{Code: reason.Code, Provider: strings.Clone(reason.Provider), Rule: strings.Clone(reason.Rule), Params: make([]contexttrace.Parameter, len(reason.Params))}
		for j, parameter := range reason.Params {
			result.Reasons[i].Params[j] = contexttrace.Parameter{Key: strings.Clone(parameter.Key), Value: cloneTraceValue(parameter.Value)}
		}
	}
	return result
}

func cloneTraceValue(input contexttrace.Value) contexttrace.Value {
	switch input.Kind {
	case contexttrace.ValueString:
		return contexttrace.StringValue(strings.Clone(input.String))
	case contexttrace.ValueInt64:
		return contexttrace.Int64Value(input.Int64)
	case contexttrace.ValueUint64:
		return contexttrace.Uint64Value(input.Uint64)
	case contexttrace.ValueBool:
		return contexttrace.BoolValue(input.Bool)
	default:
		return contexttrace.Value{}
	}
}

func (trace *traceRuntime) noteDropLocked(record contexttrace.Record, bytes uint64) {
	if trace.dropped != ^uint64(0) {
		trace.dropped++
	}
	if trace.pendingDrop.records != ^uint64(0) {
		trace.pendingDrop.records++
	}
	trace.pendingDrop.bytes = addTraceBytes(trace.pendingDrop.bytes, bytes)
	at := record.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	trace.pendingDrop.lastAt = at
}

func (trace *traceRuntime) writeDropMarker(store *contexttrace.Store, summary traceDropSummary) {
	if store == nil || summary.records == 0 {
		return
	}
	record := contexttrace.Record{
		Kind: contexttrace.KindObservation,
		At:   summary.lastAt,
		Inputs: []contexttrace.Input{
			{Code: stableCode("trace.queue-drop.records"), Key: "trace.queue-drop.records", Value: contexttrace.Uint64Value(summary.records)},
		},
		Reasons: []contexttrace.Reason{{Code: stableCode("trace.queue-drop"), Rule: "trace queue admission", Params: []contexttrace.Parameter{{Key: "trace.queue-drop.bytes", Value: contexttrace.Uint64Value(summary.bytes)}}}},
	}
	_, _ = store.Append(record)
}
