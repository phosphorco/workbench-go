package contextdaemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextconfig"
	"github.com/phosphorco/workbench-go/internal/contextengine"
	"github.com/phosphorco/workbench-go/internal/contextprovider"
	"github.com/phosphorco/workbench-go/internal/contexttrace"
)

var (
	ErrRuntimeClosed  = errors.New("contextdaemon: runtime is closed")
	ErrRuntimeStarted = errors.New("contextdaemon: runtime is already started")
	ErrRuntimeBounds  = errors.New("contextdaemon: runtime bound is exhausted")
	ErrRuntimeInput   = errors.New("contextdaemon: invalid input")
)

const (
	defaultHookDeadline      = 2 * time.Second
	defaultWholeHookDeadline = 2 * time.Second
	defaultTraceQueueItems   = 256
	defaultTraceQueueBytes   = 8 * 1024 * 1024
	defaultRequestSlots      = 64
	defaultConnections       = 64
	defaultProviderProcesses = 4
	defaultWireBytes         = 4 * 1024 * 1024
	defaultStartupTimeout    = 2 * time.Second
	defaultDialTimeout       = 250 * time.Millisecond
	defaultIdleTTL           = 5 * time.Minute
	defaultSessionStates     = 512
	maxReasonCount           = 64
	maxReasonText            = 4096
)

// Runtime is the sole owner of engine partitions, provider processes, epoch
// state, and the one trace worker for a daemon generation. Provider and trace
// I/O is deliberately outside mu and partitionState.mu.
type Runtime struct {
	mu      sync.Mutex
	startMu sync.Mutex
	// admissionMu serializes the small engine mutation plus global-bound
	// accounting transaction. Provider and filesystem calls happen before it.
	admissionMu sync.Mutex
	options     RuntimeOptions
	paths       Paths

	started bool
	closing bool
	closed  bool
	stop    chan struct{}

	workerWG  sync.WaitGroup
	requestWG sync.WaitGroup
	active    uint32
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error

	requestSlots chan struct{}
	connections  chan struct{}

	generation contextapi.RuntimeGeneration
	nextEpoch  contextapi.EpochID

	sessions   map[sessionKey]epochState
	partitions map[partitionKey]*partitionState
	providers  map[providerKey]*providerState
	idleTTL    time.Duration

	trace *traceRuntime
}

// Client owns only one daemon connection. Closing it never terminates the
// shared daemon; startup process ownership transfers after socket readiness.
type Client struct {
	mu      sync.Mutex
	paths   Paths
	dial    time.Duration
	maxWire int
	conn    net.Conn
	closed  bool
}

type sessionKey struct {
	harness  contextapi.Harness
	audience contextapi.AudienceID
}

type epochState struct {
	epoch    contextapi.EpochID
	lastSeen time.Time
}

type partitionKey struct {
	harness      contextapi.Harness
	scope        contextapi.ScopeID
	root         string
	audience     contextapi.AudienceID
	epoch        contextapi.EpochID
	configDigest contextapi.ConfigDigest
}

type partitionState struct {
	mu sync.Mutex

	key          partitionKey
	scope        contextapi.ScopeIdentity
	audience     contextapi.Audience
	configDigest contextapi.ConfigDigest
	engine       *contextengine.Engine
	limits       contextapi.EngineLimits
	lastUsedAt   time.Time
	idleTTL      time.Duration
	host         contextapi.HostSnapshot
	task         contextapi.TaskRef
	profile      contextapi.ProfileSnapshot

	// This is freshness/origin metadata only. The engine owns all pending,
	// live-offer, and receipt truth. Entries are pruned from PendingSources,
	// retaining bounded original recruitment observations even before an offer.
	sourceEvidence map[contextapi.OfferItemIdentity]freshnessEvidence
}

type freshnessEvidence struct {
	item     contextapi.OfferItemIdentity
	source   contextapi.SourceRef
	provider contextapi.ProviderConfig
	request  contextapi.ContributionRequest
	lastSeen time.Time
}

type providerKey struct {
	scope        contextapi.ScopeID
	root         string
	configDigest contextapi.ConfigDigest
	provider     contextapi.ProviderID
}

type providerState struct {
	config    contextapi.ProviderConfig
	resource  contextapi.ProviderResource
	client    *contextprovider.Client
	starting  bool
	closing   bool
	ready     chan struct{}
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
	err       error
	users     uint32
	lastUsed  time.Time
	idleTTL   time.Duration
}

func NewRuntime(options RuntimeOptions) (*Runtime, error) {
	paths, err := normalizePaths(options.Paths)
	if err != nil {
		return nil, err
	}
	if options.HookDeadline <= 0 {
		options.HookDeadline = defaultHookDeadline
	}
	if options.WholeHookDeadline <= 0 {
		options.WholeHookDeadline = defaultWholeHookDeadline
	}
	if options.HookDeadline > options.WholeHookDeadline {
		options.HookDeadline = options.WholeHookDeadline
	}
	if options.TraceQueueItems == 0 {
		options.TraceQueueItems = defaultTraceQueueItems
	}
	if options.TraceQueueBytes == 0 {
		options.TraceQueueBytes = defaultTraceQueueBytes
	}
	return &Runtime{
		options:      options,
		paths:        paths,
		generation:   newGeneration(),
		closeDone:    make(chan struct{}),
		sessions:     make(map[sessionKey]epochState),
		partitions:   make(map[partitionKey]*partitionState),
		providers:    make(map[providerKey]*providerState),
		idleTTL:      defaultIdleTTL,
		requestSlots: make(chan struct{}, defaultRequestSlots),
		connections:  make(chan struct{}, defaultConnections),
	}, nil
}

// hookContext enforces the per-operation hook budget for direct runtime
// callers. Transport supplies WholeHookDeadline as the outer request budget;
// the shorter hook budget still bounds composition and provider work inside
// Observe/Confirm.
func (runtime *Runtime) hookContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, runtime.options.HookDeadline)
}

// Start initializes the local trace worker. Cache failure is diagnostic only:
// delivery remains usable and status exposes the unavailable explanation.
func (runtime *Runtime) Start(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil start context", ErrRuntimeInput)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.startMu.Lock()
	defer runtime.startMu.Unlock()
	runtime.mu.Lock()
	if runtime.closed || runtime.closing {
		runtime.mu.Unlock()
		return ErrRuntimeClosed
	}
	if runtime.started {
		runtime.mu.Unlock()
		return nil
	}
	paths := runtime.paths
	options := runtime.options
	runtime.mu.Unlock()

	store, openErr := contexttrace.Open(paths.CacheDir, traceOptions(options))
	trace := newTraceRuntime(store, openErr, options.TraceQueueItems, options.TraceQueueBytes)
	idleTTL := homeIdleTTL(options)
	runtime.mu.Lock()
	if runtime.closed || runtime.closing {
		runtime.mu.Unlock()
		trace.stopAndWait()
		return ErrRuntimeClosed
	}
	runtime.stop = make(chan struct{})
	runtime.trace = trace
	runtime.idleTTL = idleTTL
	runtime.started = true
	runtime.workerWG.Add(1)
	stop := runtime.stop
	runtime.mu.Unlock()
	go func() {
		defer runtime.workerWG.Done()
		trace.run(stop)
	}()
	return nil
}

// Close has one join owner. No request may be admitted after closing is set;
// the owner waits admitted requests before stopping providers and trace.
func (runtime *Runtime) Close() error {
	runtime.closeOnce.Do(func() {
		runtime.closeErr = runtime.close()
		close(runtime.closeDone)
	})
	<-runtime.closeDone
	return runtime.closeErr
}

func (runtime *Runtime) close() error {
	runtime.startMu.Lock()
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		runtime.startMu.Unlock()
		return nil
	}
	runtime.closing = true
	runtime.mu.Unlock()
	runtime.startMu.Unlock()

	// beginRequest performs Add while holding runtime.mu, so this Wait cannot
	// race an Add after the closing fence.
	runtime.requestWG.Wait()
	runtime.mu.Lock()
	stop, trace := runtime.stop, runtime.trace
	providers := make([]*providerState, 0, len(runtime.providers))
	for _, state := range runtime.providers {
		if state.client != nil {
			state.closing = true
			providers = append(providers, state)
		}
	}
	runtime.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if trace != nil {
		trace.wait()
	}
	runtime.workerWG.Wait()

	var closeErr error
	for _, state := range providers {
		if err := runtime.closeProviderState(state); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("close provider %q: %w", state.config.ID, err)
		}
	}
	runtime.mu.Lock()
	runtime.closed = true
	runtime.mu.Unlock()
	return closeErr
}

func (runtime *Runtime) Observe(ctx context.Context, input ObserveInput) (ObserveResult, error) {
	if ctx == nil {
		return ObserveResult{}, fmt.Errorf("%w: nil observe context", ErrRuntimeInput)
	}
	ctx, cancel := runtime.hookContext(ctx)
	defer cancel()
	directory := input.WorkingDirectory
	if directory == "" {
		directory = input.Host.WorkingDirectory
	}
	if directory == "" {
		directory = input.Observation.Scope.CanonicalRoot
	}
	// Local activation is normally performed by the hook before Ensure. Keep
	// this preflight as a safety fence: an inactive caller must not start a
	// daemon merely to report that it has no work to do.
	_, initialActivation, err := runtime.loadCurrentActivation(directory, time.Now().UTC())
	if err != nil {
		return ObserveResult{}, err
	}
	if initialActivation.State != contextapi.ActivationEnabled {
		runtime.mu.Lock()
		started := runtime.started && !runtime.closing && !runtime.closed
		runtime.mu.Unlock()
		if !started {
			return ObserveResult{Activation: initialActivation, Decision: contextapi.DeliveryDecision{
				State: contextapi.DeliveryWithdrawn, Reasons: cloneReasons(initialActivation.Reasons),
			}}, nil
		}
	}
	if err := runtime.beginRequest(ctx); err != nil {
		return ObserveResult{}, err
	}
	defer runtime.endRequest()
	now := time.Now().UTC()
	_, activation, err := runtime.loadCurrentActivation(directory, now)
	if err != nil {
		return ObserveResult{}, err
	}
	if activation.State != contextapi.ActivationEnabled {
		// Inactive observations may only revoke the exact audience/scope named
		// by the normalized hook. They never carry an empty global scope.
		runtime.withdrawInactive(input.Host.Harness, input.Observation, now, activation.Reasons)
		return ObserveResult{Activation: activation, Decision: contextapi.DeliveryDecision{
			State: contextapi.DeliveryWithdrawn, Reasons: cloneReasons(activation.Reasons),
		}}, nil
	}
	if err := validateObservationInput(input, directory, activation.Scope); err != nil {
		return ObserveResult{}, err
	}
	audience, epochReasons, previous, err := runtime.assignAudience(input, now)
	if err != nil {
		return ObserveResult{}, err
	}
	if previous.Epoch != 0 && previous.Epoch != audience.Epoch {
		runtime.revokeSessionPartitions(input.Host.Harness, audience.ID, previous.Epoch, now, epochReasons)
	}
	runtime.revokeChangedConfigPartitions(input.Host.Harness, activation.Scope, audience.ID, activation.Effective.ConfigDigest, now)
	profile, profileReasons := runtime.composeProfile(ctx, activation, input.Host, directory, audience, now)
	contributions, evidence, contributionReasons := runtime.collectContributions(ctx, activation, input, audience, profile, now)
	state, err := runtime.partitionFor(input.Host.Harness, activation, audience, input.Host, profile, now)
	if err != nil {
		return ObserveResult{}, err
	}
	planInput := contextapi.DeliveryPlanInput{
		Now: now, Active: true, Scope: activation.Scope, ConfigDigest: activation.Effective.ConfigDigest,
		Audience: audience, Generation: runtime.generationValue(), Profile: profile,
		Contributions: contributions, Opportunity: input.Opportunity, Transition: input.Transition,
	}
	decision, planReasons := runtime.planPartition(ctx, state, planInput, evidence, activation, now)
	engineReasons := cloneReasons(decision.Reasons)
	decision.Reasons = append(cloneReasons(epochReasons), profileReasons...)
	decision.Reasons = append(decision.Reasons, contributionReasons...)
	decision.Reasons = append(decision.Reasons, planReasons...)
	decision.Reasons = append(decision.Reasons, engineReasons...)
	decision.Reasons = dedupeReasons(decision.Reasons, maxReasonCount)
	runtime.traceDecision(state, input.Observation, profile, decision, contributions, now)
	return ObserveResult{Activation: activation, Decision: decision, Generation: runtime.generationValue(), Audience: audience}, nil
}

func (runtime *Runtime) Confirm(ctx context.Context, input ConfirmInput) contextapi.ConfirmationResult {
	if ctx == nil {
		return confirmationFailure(ErrRuntimeInput, "nil confirmation context")
	}
	if input.WorkingDirectory == "" || input.Identity.ID == 0 {
		return confirmationFailure(ErrRuntimeInput, "confirmation requires working directory and offer identity")
	}
	ctx, cancel := runtime.hookContext(ctx)
	defer cancel()
	// input.At is historical adapter evidence only. It can never extend the
	// live-offer/profile deadlines.
	now := time.Now().UTC()
	_, activation, err := runtime.loadCurrentActivation(input.WorkingDirectory, now)
	if err != nil {
		return confirmationFailure(err, "reload current activation")
	}
	if activation.State != contextapi.ActivationEnabled {
		result := runtime.withdrawInactiveConfirmation(input.WorkingDirectory, input.Identity, now, activation.Reasons)
		return result
	}
	if err := runtime.beginRequest(ctx); err != nil {
		return confirmationFailure(err, "runtime is unavailable")
	}
	defer runtime.endRequest()
	// The preflight above protects the cold inactive path. Reload after
	// admission so a declaration withdrawn while startup was in progress is
	// handled as inactive without touching an offer from the old scope.
	now = time.Now().UTC()
	_, activation, err = runtime.loadCurrentActivation(input.WorkingDirectory, now)
	if err != nil {
		return confirmationFailure(err, "reload current activation")
	}
	if activation.State != contextapi.ActivationEnabled {
		return runtime.withdrawInactiveConfirmation(input.WorkingDirectory, input.Identity, now, activation.Reasons)
	}
	state, ambiguous := runtime.findOfferPartition(input.WorkingDirectory, input.Identity, activation)
	if state == nil {
		if ambiguous {
			return contextapi.ConfirmationResult{State: contextapi.DeliveryWithdrawn, Reasons: []contextapi.Reason{runtimeReason(contextapi.ReasonOfferMismatch, now, "confirmation partition is ambiguous across harnesses; no state was confirmed")}}
		}
		return confirmationFailure(contextengine.ErrInvalidPartition, "offer partition is no longer resident")
	}
	state.mu.Lock()
	host := cloneHost(state.host)
	audience := state.audience
	state.mu.Unlock()
	host.WorkingDirectory = filepath.Clean(input.WorkingDirectory)
	profile, profileReasons := runtime.composeProfile(ctx, activation, host, input.WorkingDirectory, audience, now)
	if fresh, staleItem, reason := runtime.validateOfferItems(ctx, state, input.Identity.Items, nil, activation, profile, now); !fresh {
		withdrawalReasons := []contextapi.Reason{reason}
		if staleItem.Source.ID != "" {
			runtime.admissionMu.Lock()
			if runtime.isResident(state) {
				state.mu.Lock()
				withdrawal := state.engine.Withdraw(contextapi.DeliveryWithdrawal{Now: now, Scope: state.scope, Audience: state.audience, Generation: input.Identity.Generation, ConfigDigest: state.configDigest, Item: staleItem})
				delete(state.sourceEvidence, staleItem)
				state.mu.Unlock()
				withdrawalReasons = append(withdrawalReasons, withdrawal.Reasons...)
			}
			runtime.admissionMu.Unlock()
		}
		result := contextapi.ConfirmationResult{State: contextapi.DeliveryWithdrawn, Reasons: append(profileReasons, withdrawalReasons...)}
		runtime.traceConfirmation(state, input.Identity, input.Handoff, result, profile, now)
		return result
	}
	confirmation := contextapi.OfferConfirmation{
		Identity: cloneOfferIdentity(input.Identity), Handoff: cloneHandoff(input.Handoff), ConfirmedAt: now,
		Active: true, Scope: activation.Scope, Audience: input.Identity.Audience,
		ConfigDigest: activation.Effective.ConfigDigest, ProfileRevision: profile.Revision, ProfileValidUntil: profile.ValidUntil,
	}
	runtime.admissionMu.Lock()
	if !runtime.isResident(state) {
		runtime.admissionMu.Unlock()
		return contextapi.ConfirmationResult{State: contextapi.DeliveryWithdrawn, Reasons: []contextapi.Reason{runtimeReason(contextapi.ReasonWithdrawn, now, "offer partition was withdrawn before confirmation")}}
	}
	admission := runtime.globalAdmissionFor(state, nil, nil, activation.Effective.Runtime)
	state.mu.Lock()
	result := state.engine.Confirm(confirmation, admission)
	state.profile = cloneProfileSnapshot(profile)
	state.lastUsedAt = now
	if result.State == contextapi.DeliveryConfirmed {
		runtime.pruneEvidenceLocked(state)
	}
	state.mu.Unlock()
	runtime.admissionMu.Unlock()
	result.Reasons = append(profileReasons, result.Reasons...)
	runtime.traceConfirmation(state, input.Identity, input.Handoff, result, profile, now)
	return result
}

func (runtime *Runtime) Status(ctx context.Context, request StatusRequest) (Status, error) {
	if ctx == nil {
		return Status{}, fmt.Errorf("%w: nil status context", ErrRuntimeInput)
	}
	if err := runtime.beginRequest(ctx); err != nil {
		return Status{}, err
	}
	defer runtime.endRequest()
	now := time.Now().UTC()
	runtime.evictIdle(now)
	status := Status{Generation: runtime.generationValue(), SocketPath: runtime.paths.SocketPath, Running: runtime.running()}
	runtime.mu.Lock()
	status.ProviderProcesses = uint32(len(runtime.providers))
	if runtime.trace != nil {
		status.TraceQueueItems, status.TraceQueueBytes, status.TraceDropped = runtime.trace.snapshot()
	}
	states := make([]*partitionState, 0, len(runtime.partitions))
	for _, state := range runtime.partitions {
		states = append(states, state)
	}
	trace := runtime.trace
	runtime.mu.Unlock()
	if request.WorkingDirectory != "" {
		_, activation, err := runtime.loadCurrentActivation(request.WorkingDirectory, now)
		if err != nil {
			return Status{}, err
		}
		status.Reasons = cloneReasons(activation.Reasons)
	}
	seenScopes := make(map[contextapi.ScopeID]struct{}, len(states))
	seenAudiences := make(map[contextapi.AudienceID]struct{}, len(states))
	for _, state := range states {
		state.mu.Lock()
		if statusRequestMatches(request, state) {
			status.Partitions = append(status.Partitions, PartitionStatus{Scope: state.scope, Audience: state.audience, Generation: runtime.generationValue(), ConfigDigest: state.configDigest, Engine: state.engine.Stats(), Profile: cloneProfileSnapshot(state.profile), LastUsedAt: state.lastUsedAt})
			seenScopes[state.scope.ID] = struct{}{}
			seenAudiences[state.audience.ID] = struct{}{}
		}
		state.mu.Unlock()
	}
	status.ActiveScopes = uint32(len(seenScopes))
	status.ActiveAudiences = uint32(len(seenAudiences))
	sort.Slice(status.Partitions, func(i, j int) bool {
		if status.Partitions[i].Scope.ID != status.Partitions[j].Scope.ID {
			return status.Partitions[i].Scope.ID < status.Partitions[j].Scope.ID
		}
		if status.Partitions[i].Audience.ID != status.Partitions[j].Audience.ID {
			return status.Partitions[i].Audience.ID < status.Partitions[j].Audience.ID
		}
		return status.Partitions[i].Audience.Epoch < status.Partitions[j].Audience.Epoch
	})
	if trace != nil {
		traceStats, err := trace.stats(ctx)
		if err != nil {
			if errors.Is(err, errTraceUnavailable) {
				status.Reasons = append(status.Reasons, runtimeReason(contextapi.ReasonEvidenceUnavailable, now, "context explanation cache is unavailable"))
			} else {
				return Status{}, err
			}
		} else {
			status.Trace = traceStats
		}
	} else {
		status.Reasons = append(status.Reasons, runtimeReason(contextapi.ReasonEvidenceUnavailable, now, "context explanation cache is not initialized"))
	}
	return status, nil
}

func (runtime *Runtime) Query(ctx context.Context, query contexttrace.Query) (contexttrace.QueryResult, error) {
	release, err := runtime.inspectionLease(ctx)
	if err != nil {
		return contexttrace.QueryResult{}, err
	}
	defer release()
	return runtime.trace.query(ctx, query)
}

func (runtime *Runtime) Inspect(ctx context.Context, request InspectRequest) (contexttrace.QueryResult, error) {
	release, err := runtime.inspectionLease(ctx)
	if err != nil {
		return contexttrace.QueryResult{}, err
	}
	defer release()
	switch request.Kind {
	case InspectContribution:
		if request.ContributionID == 0 {
			return contexttrace.QueryResult{}, fmt.Errorf("%w: contribution id is required", ErrRuntimeInput)
		}
		return runtime.trace.inspectContribution(ctx, request.ContributionID, request.Query)
	case InspectTurn:
		if request.Turn == "" {
			return contexttrace.QueryResult{}, fmt.Errorf("%w: turn is required", ErrRuntimeInput)
		}
		return runtime.trace.inspectTurn(ctx, request.Turn, request.Query)
	case InspectProfile:
		if request.Profile == "" {
			return contexttrace.QueryResult{}, fmt.Errorf("%w: profile is required", ErrRuntimeInput)
		}
		return runtime.trace.inspectProfile(ctx, request.Profile, request.Query)
	default:
		return contexttrace.QueryResult{}, fmt.Errorf("%w: unknown inspection kind %q", ErrRuntimeInput, request.Kind)
	}
}

func (runtime *Runtime) Clear(ctx context.Context) (contexttrace.ClearResult, error) {
	release, err := runtime.inspectionLease(ctx)
	if err != nil {
		return contexttrace.ClearResult{}, err
	}
	defer release()
	return runtime.trace.clear(ctx)
}

func (runtime *Runtime) inspectionLease(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil inspection context", ErrRuntimeInput)
	}
	if err := runtime.beginRequest(ctx); err != nil {
		return nil, err
	}
	runtime.mu.Lock()
	trace := runtime.trace
	runtime.mu.Unlock()
	if trace == nil || trace.store == nil {
		runtime.endRequest()
		return nil, fmt.Errorf("%w: context explanation cache is unavailable", ErrRuntimeInput)
	}
	return runtime.endRequest, nil
}

func (runtime *Runtime) beginRequest(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.mu.Lock()
	if runtime.closed || runtime.closing {
		runtime.mu.Unlock()
		return ErrRuntimeClosed
	}
	started := runtime.started
	runtime.mu.Unlock()
	if !started {
		if err := runtime.Start(ctx); err != nil {
			return err
		}
	}
	runtime.mu.Lock()
	if runtime.closed || runtime.closing || !runtime.started {
		runtime.mu.Unlock()
		return ErrRuntimeClosed
	}
	runtime.requestWG.Add(1)
	runtime.active++
	slots := runtime.requestSlots
	runtime.mu.Unlock()
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		runtime.mu.Lock()
		runtime.active--
		runtime.mu.Unlock()
		runtime.requestWG.Done()
		return ctx.Err()
	}
}

func (runtime *Runtime) endRequest() {
	<-runtime.requestSlots
	runtime.mu.Lock()
	runtime.active--
	runtime.mu.Unlock()
	runtime.requestWG.Done()
}

func (runtime *Runtime) loadCurrentActivation(directory string, now time.Time) (contextapi.ActivationInput, contextapi.ActivationResult, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return contextapi.ActivationInput{}, contextapi.ActivationResult{State: contextapi.ActivationInvalid}, fmt.Errorf("%w: working directory must be absolute", ErrRuntimeInput)
	}
	loaded, err := contextconfig.Load(contextconfig.LoadOptions{WorkingDirectory: directory, HomeConfigPath: runtime.paths.HomeConfigPath, Now: now, Limits: runtime.options.LoadLimits})
	if err != nil {
		return contextapi.ActivationInput{}, contextapi.ActivationResult{}, fmt.Errorf("load current context activation: %w", err)
	}
	return loaded.Input, loaded.Result, nil
}

func validateObservationInput(input ObserveInput, directory string, scope contextapi.ScopeIdentity) error {
	if input.Host.Harness != contextapi.HarnessClaudeCode && input.Host.Harness != contextapi.HarnessCodex {
		return fmt.Errorf("%w: supported harness is required", ErrRuntimeInput)
	}
	if input.Observation.Audience.ID == "" {
		return fmt.Errorf("%w: audience identity is required", ErrRuntimeInput)
	}
	if input.Host.WorkingDirectory != "" && filepath.Clean(input.Host.WorkingDirectory) != filepath.Clean(directory) {
		return fmt.Errorf("%w: host working directory differs from request", ErrRuntimeInput)
	}
	if input.Observation.Scope.CanonicalRoot != "" && input.Observation.Scope.CanonicalRoot != scope.CanonicalRoot {
		return fmt.Errorf("%w: observation scope differs from current activation", ErrRuntimeInput)
	}
	return nil
}

func (runtime *Runtime) assignAudience(input ObserveInput, now time.Time) (contextapi.Audience, []contextapi.Reason, contextapi.Audience, error) {
	observed := input.Observation.Audience
	if observed.ID == "" {
		return contextapi.Audience{}, nil, contextapi.Audience{}, fmt.Errorf("%w: audience id is required", ErrRuntimeInput)
	}
	key := sessionKey{harness: input.Host.Harness, audience: observed.ID}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	previous := contextapi.Audience{}
	state, tracked := runtime.sessions[key]
	if tracked {
		previous = contextapi.Audience{ID: observed.ID, Epoch: state.epoch, Continuity: contextapi.ContinuityKnown}
	}
	fresh := !tracked || input.Transition.Kind != "" || observed.Continuity == contextapi.ContinuityReset
	reasons := make([]contextapi.Reason, 0, 2)
	if tracked && observed.Epoch != 0 && observed.Epoch != state.epoch && input.Transition.Kind == "" {
		fresh = true
		reasons = append(reasons, runtimeReason(contextapi.ReasonEpochMismatch, now, "adapter epoch disagrees with runtime-owned tracked epoch"))
	}
	if fresh {
		epoch, err := runtime.allocateEpochLocked()
		if err != nil {
			return contextapi.Audience{}, nil, previous, err
		}
		if len(runtime.sessions) >= defaultSessionStates && !tracked {
			runtime.pruneSessionsLocked()
		}
		runtime.sessions[key] = epochState{epoch: epoch, lastSeen: now}
		if input.Transition.Kind != "" {
			reasons = append(reasons, runtimeReason(contextapi.ReasonWithdrawn, now, "explicit audience transition allocated a fresh epoch"))
		} else if !tracked {
			reasons = append(reasons, runtimeReason(contextapi.ReasonRuntimeRestarted, now, "first-seen or runtime-lost continuity allocated a conservative fresh epoch"))
		} else if observed.Continuity == contextapi.ContinuityReset {
			reasons = append(reasons, runtimeReason(contextapi.ReasonWithdrawn, now, "adapter reported an audience reset"))
		}
		// Once the runtime assigns an epoch, continuity is a runtime fact. Keep
		// the canonical audience stable across the lifecycle evidence values
		// supplied by ordinary hooks (which are intentionally unknown). This is
		// also part of the profile identity: reset evidence must not make the
		// post-reset ordinary hook compose a different profile solely because
		// the adapter changed Continuity from reset to unknown.
		return contextapi.Audience{ID: observed.ID, Epoch: epoch, Continuity: contextapi.ContinuityKnown}, reasons, previous, nil
	}
	state.lastSeen = now
	runtime.sessions[key] = state
	return contextapi.Audience{ID: observed.ID, Epoch: state.epoch, Continuity: contextapi.ContinuityKnown}, reasons, previous, nil
}

func (runtime *Runtime) pruneSessionsLocked() {
	if len(runtime.sessions) < defaultSessionStates {
		return
	}
	var oldest sessionKey
	var oldestAt time.Time
	for key, state := range runtime.sessions {
		if oldestAt.IsZero() || state.lastSeen.Before(oldestAt) {
			oldest, oldestAt = key, state.lastSeen
		}
	}
	delete(runtime.sessions, oldest)
}

func (runtime *Runtime) allocateEpochLocked() (contextapi.EpochID, error) {
	if runtime.nextEpoch == ^contextapi.EpochID(0) {
		return 0, fmt.Errorf("%w: epoch id space is exhausted", ErrRuntimeBounds)
	}
	runtime.nextEpoch++
	return runtime.nextEpoch, nil
}

func (runtime *Runtime) generationValue() contextapi.RuntimeGeneration {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.generation
}

func (runtime *Runtime) running() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.started && !runtime.closing && !runtime.closed
}

func (runtime *Runtime) partitionFor(harness contextapi.Harness, activation contextapi.ActivationResult, audience contextapi.Audience, host contextapi.HostSnapshot, profile contextapi.ProfileSnapshot, now time.Time) (*partitionState, error) {
	key := partitionKey{harness: harness, scope: activation.Scope.ID, root: activation.Scope.CanonicalRoot, audience: audience.ID, epoch: audience.Epoch, configDigest: activation.Effective.ConfigDigest}
	runtime.mu.Lock()
	if state := runtime.partitions[key]; state != nil {
		runtime.mu.Unlock()
		state.mu.Lock()
		// Continuity is evidence on an adapter observation, not part of the
		// immutable partition key. assignAudience canonicalizes the runtime
		// snapshot to ContinuityKnown, so the audience identity remains a
		// read-only partition value and cannot race trace/status readers.
		state.lastUsedAt, state.host, state.task, state.profile = now, cloneHost(host), host.Task, cloneProfileSnapshot(profile)
		state.mu.Unlock()
		return state, nil
	}
	if !runtime.partitionCapacityLocked(activation.Effective.Runtime, activation.Scope.ID, activation.Scope.CanonicalRoot, audience.ID) {
		runtime.mu.Unlock()
		return nil, fmt.Errorf("%w: active scope/audience bound is full", ErrRuntimeBounds)
	}
	limits := activation.Effective.Delivery
	partition := contextapi.DeliveryPartition{Scope: activation.Scope, Audience: audience, Generation: runtime.generation, ConfigDigest: activation.Effective.ConfigDigest}
	engine, err := contextengine.NewEngine(partition, limits)
	if err != nil {
		runtime.mu.Unlock()
		return nil, fmt.Errorf("construct delivery partition: %w", err)
	}
	state := &partitionState{key: key, scope: activation.Scope, audience: audience, configDigest: activation.Effective.ConfigDigest, engine: engine, limits: limits, lastUsedAt: now, idleTTL: runtimeIdleTTL(activation.Effective.Runtime), host: cloneHost(host), task: host.Task, profile: cloneProfileSnapshot(profile), sourceEvidence: make(map[contextapi.OfferItemIdentity]freshnessEvidence)}
	runtime.partitions[key] = state
	runtime.mu.Unlock()
	return state, nil
}

func (runtime *Runtime) partitionCapacityLocked(limits contextapi.RuntimeLimits, scopeID contextapi.ScopeID, root string, audienceID contextapi.AudienceID) bool {
	maxScopes, maxAudiences := limits.MaxActiveScopes, limits.MaxActiveAudiences
	if maxScopes == 0 {
		maxScopes = 64
	}
	if maxAudiences == 0 {
		maxAudiences = 256
	}
	scopes := make(map[string]struct{}, len(runtime.partitions))
	audiences := make(map[string]struct{}, len(runtime.partitions))
	for _, state := range runtime.partitions {
		scopes[string(state.scope.ID)+"\x00"+state.scope.CanonicalRoot] = struct{}{}
		audiences[string(state.audience.ID)] = struct{}{}
	}
	_, hasScope := scopes[string(scopeID)+"\x00"+root]
	_, hasAudience := audiences[string(audienceID)]
	return (hasScope || uint32(len(scopes)) < maxScopes) && (hasAudience || uint32(len(audiences)) < maxAudiences)
}

func runtimeIdleTTL(limits contextapi.RuntimeLimits) time.Duration {
	if limits.IdleTTLMs == 0 {
		return defaultIdleTTL
	}
	return time.Duration(limits.IdleTTLMs) * time.Millisecond
}

// planPartition first asks the engine to select using its retained queue, then
// validates only the selected item sources outside state locks. A stale item is
// withdrawn with the exact engine identity and filtered from this incremental
// call so the old contribution cannot be re-admitted in the same Plan.
func (runtime *Runtime) planPartition(ctx context.Context, state *partitionState, input contextapi.DeliveryPlanInput, current map[contextapi.OfferItemIdentity]freshnessEvidence, activation contextapi.ActivationResult, now time.Time) (contextapi.DeliveryDecision, []contextapi.Reason) {
	blocked := make(map[contextapi.OfferItemIdentity]struct{})
	reasons := make([]contextapi.Reason, 0, 4)
	for attempt := 0; attempt < 128; attempt++ {
		input.Contributions = filterContributions(input.Contributions, blocked)
		runtime.admissionMu.Lock()
		if !runtime.isResident(state) {
			runtime.admissionMu.Unlock()
			return contextapi.DeliveryDecision{State: contextapi.DeliveryWithdrawn}, append(reasons, runtimeReason(contextapi.ReasonWithdrawn, now, "delivery partition was withdrawn before planning"))
		}
		admission := runtime.globalAdmissionFor(state, input.Contributions, current, activation.Effective.Runtime)
		state.mu.Lock()
		decision := state.engine.Plan(input, admission)
		state.lastUsedAt = now
		state.profile = cloneProfileSnapshot(input.Profile)
		// The engine is authoritative for pending membership. Refresh the
		// separate recruitment evidence before releasing the admission fence so
		// another partition cannot observe new engine state without its charge.
		runtime.refreshSourceEvidenceLocked(state, current, now)
		state.mu.Unlock()
		runtime.admissionMu.Unlock()
		if decision.Offer.Identity.ID == 0 {
			return decision, reasons
		}
		fresh, staleItem, reason := runtime.validateOfferItems(ctx, state, decision.Offer.Identity.Items, current, activation, input.Profile, now)
		if fresh {
			return decision, reasons
		}
		reasons = append(reasons, reason)
		if staleItem.Source.ID != "" {
			if _, already := blocked[staleItem]; !already {
				blocked[staleItem] = struct{}{}
				runtime.admissionMu.Lock()
				state.mu.Lock()
				result := state.engine.Withdraw(contextapi.DeliveryWithdrawal{Now: now, Scope: state.scope, Audience: state.audience, Generation: input.Generation, ConfigDigest: state.configDigest, Item: staleItem})
				delete(state.sourceEvidence, staleItem)
				state.mu.Unlock()
				runtime.admissionMu.Unlock()
				reasons = append(reasons, result.Reasons...)
			}
		}
	}
	return contextapi.DeliveryDecision{State: contextapi.DeliveryDeferred}, append(reasons, runtimeReason(contextapi.ReasonEvidenceUnavailable, now, "freshness validation retry bound was reached"))
}

func (runtime *Runtime) globalAdmissionFor(state *partitionState, input []contextapi.Contribution, current map[contextapi.OfferItemIdentity]freshnessEvidence, limits contextapi.RuntimeLimits) contextapi.GlobalAdmission {
	maxItems, maxBytes, maxOffers, maxReceipts := limits.MaxPendingItems, limits.MaxPendingMemoryBytes, limits.MaxOutstandingOffers, limits.MaxReceipts
	if maxItems == 0 {
		maxItems = 256
	}
	if maxBytes == 0 {
		maxBytes = 4 * 1024 * 1024
	}
	if maxOffers == 0 {
		maxOffers = 128
	}
	if maxReceipts == 0 {
		maxReceipts = 1024
	}
	state.mu.Lock()
	known := make(map[contextapi.OfferItemIdentity]struct{}, len(state.sourceEvidence))
	for identity := range state.sourceEvidence {
		known[identity] = struct{}{}
	}
	targetEvidence := evidenceBytes(state.sourceEvidence)
	state.mu.Unlock()
	newEvidence := uint64(0)
	for _, contribution := range input {
		identity := offerItemIdentity(contribution)
		if _, ok := known[identity]; !ok {
			if value, exists := current[identity]; exists {
				newEvidence += evidenceSize(value)
			}
		}
	}
	runtime.mu.Lock()
	states := make([]*partitionState, 0, len(runtime.partitions))
	for _, other := range runtime.partitions {
		states = append(states, other)
	}
	runtime.mu.Unlock()
	items, retained, offers, receipts := uint32(0), uint64(0), uint32(0), uint32(0)
	for _, other := range states {
		if other == state {
			continue
		}
		other.mu.Lock()
		stats := other.engine.Stats()
		items += stats.PendingItems
		retained += stats.RetainedBytes + evidenceBytes(other.sourceEvidence)
		offers += stats.LiveOffers
		receipts += stats.Receipts
		other.mu.Unlock()
	}
	metadata := targetEvidence + newEvidence
	if retained > maxBytes {
		retained = maxBytes
	}
	if metadata > maxBytes-retained {
		metadata = maxBytes - retained
	}
	return contextapi.GlobalAdmission{
		MaxPendingItems:       residualUint32(maxItems, items),
		MaxPendingMemoryBytes: maxBytes - retained - metadata,
		MaxOutstandingOffers:  residualUint32(maxOffers, offers),
		MaxReceipts:           residualUint32(maxReceipts, receipts),
	}
}

func (runtime *Runtime) isResident(state *partitionState) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.partitions[state.key] == state
}

func evidenceBytes(values map[contextapi.OfferItemIdentity]freshnessEvidence) uint64 {
	var total uint64
	for _, value := range values {
		total += evidenceSize(value)
	}
	return total
}

func evidenceSize(value freshnessEvidence) uint64 {
	// The request is the bounded original recruitment evidence, not a body
	// cache. JSON gives one compact traversal of every nested string, fact,
	// selector, provider setting, profile value, and identity we retain. Charge
	// a conservative multiplier for Go object/array overhead so the runtime's
	// reservation cannot understate its own metadata.
	encoded, err := json.Marshal(struct {
		Item     contextapi.OfferItemIdentity
		Source   contextapi.SourceRef
		Provider contextapi.ProviderConfig
		Request  contextapi.ContributionRequest
		LastSeen time.Time
	}{
		Item: value.item, Source: value.source, Provider: value.provider,
		Request: value.request, LastSeen: value.lastSeen,
	})
	if err != nil {
		return ^uint64(0)
	}
	return uint64(512) + uint64(len(encoded))*2
}

func residualUint32(limit, used uint32) uint32 {
	if used >= limit {
		return 0
	}
	return limit - used
}

func (runtime *Runtime) refreshSourceEvidenceLocked(state *partitionState, current map[contextapi.OfferItemIdentity]freshnessEvidence, now time.Time) {
	pending := state.engine.PendingSources()
	keep := make(map[contextapi.OfferItemIdentity]freshnessEvidence, len(pending))
	for _, source := range pending {
		if evidence, ok := current[source.Identity]; ok {
			evidence.lastSeen = now
			keep[source.Identity] = cloneFreshnessEvidence(evidence)
			continue
		}
		if evidence, ok := state.sourceEvidence[source.Identity]; ok {
			evidence.lastSeen = now
			keep[source.Identity] = evidence
		}
	}
	state.sourceEvidence = keep
}

func (runtime *Runtime) validateOfferItems(ctx context.Context, state *partitionState, items []contextapi.OfferItemIdentity, currentEvidence map[contextapi.OfferItemIdentity]freshnessEvidence, activation contextapi.ActivationResult, profile contextapi.ProfileSnapshot, now time.Time) (bool, contextapi.OfferItemIdentity, contextapi.Reason) {
	state.mu.Lock()
	evidence := make([]freshnessEvidence, 0, len(items))
	for _, item := range items {
		if value, ok := state.sourceEvidence[item]; ok {
			evidence = append(evidence, cloneFreshnessEvidence(value))
		} else {
			state.mu.Unlock()
			return false, item, runtimeReason(contextapi.ReasonEvidenceUnavailable, now, "pending source recruitment evidence is no longer resident")
		}
	}
	state.mu.Unlock()
	for _, value := range evidence {
		// Exact items collected during this Observe already carry current
		// provider/source evidence. Confirm and later Observes still revalidate
		// delayed work because they do not provide this request-local evidence.
		if _, collected := currentEvidence[value.item]; collected {
			continue
		}
		request := cloneContributionRequest(value.request)
		request.Scope, request.Audience, request.Profile, request.ConfigDigest = activation.Scope, state.audience, cloneProfileSnapshot(profile), activation.Effective.ConfigDigest
		request.Observation.Scope, request.Observation.Audience, request.Observation.At = activation.Scope, state.audience, now
		if value.provider.Kind == contextapi.ProviderKindBuiltin {
			fresh, err := contextprovider.Revalidate(ctx, activation.Scope.CanonicalRoot, value.source, value.item.SourceRevision, builtinLimits(value.provider.Limits, activation.Effective.Runtime))
			if err != nil {
				return false, value.item, runtimeReasonForProvider(contextapi.ReasonEvidenceUnavailable, value.provider.ID, now, "builtin source freshness could not be established: "+err.Error())
			}
			if !fresh {
				return false, value.item, runtimeReasonForProvider(contextapi.ReasonSourceChanged, value.provider.ID, now, "builtin source revision changed or was deleted before delivery")
			}
			continue
		}
		client, release, err := runtime.acquireProvider(ctx, value.provider, activation.Scope, activation.Effective.ConfigDigest, now, activation.Effective.Runtime)
		if err != nil {
			return false, value.item, runtimeReasonForProvider(contextapi.ReasonEvidenceUnavailable, value.provider.ID, now, "external source freshness unavailable: "+err.Error())
		}
		response, callErr := client.Contribute(ctx, request)
		release()
		if callErr != nil {
			return false, value.item, runtimeReasonForProvider(contextapi.ReasonEvidenceUnavailable, value.provider.ID, now, "external source freshness unavailable: "+callErr.Error())
		}
		found := false
		for _, contribution := range response.Contributions {
			if offerItemIdentity(contribution) == value.item {
				found = true
				break
			}
		}
		if !found {
			return false, value.item, runtimeReasonForProvider(contextapi.ReasonSourceChanged, value.provider.ID, now, "external provider no longer returns the exact recruited source revision/content")
		}
	}
	return true, contextapi.OfferItemIdentity{}, contextapi.Reason{}
}

func (runtime *Runtime) collectContributions(ctx context.Context, activation contextapi.ActivationResult, input ObserveInput, audience contextapi.Audience, profile contextapi.ProfileSnapshot, now time.Time) ([]contextapi.Contribution, map[contextapi.OfferItemIdentity]freshnessEvidence, []contextapi.Reason) {
	configs := activation.Effective.Providers
	if len(configs) == 0 {
		return nil, nil, nil
	}
	type result struct {
		index         int
		config        contextapi.ProviderConfig
		request       contextapi.ContributionRequest
		contributions []contextapi.Contribution
		reasons       []contextapi.Reason
	}
	results := make(chan result, len(configs))
	var wait sync.WaitGroup
	for index, config := range configs {
		index, config := index, cloneProviderConfig(config)
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := contextapi.ContributionRequest{RequestID: contextapi.RequestID(now.UnixNano() + int64(index)), Scope: activation.Scope, Audience: audience, Task: input.Host.Task, Observation: cloneObservation(input.Observation), Profile: cloneProfileSnapshot(profile), ConfigDigest: activation.Effective.ConfigDigest, Limits: contextapi.ContributionLimits{MaxContributions: activation.Effective.Runtime.MaxContributions, MaxBodyBytes: activation.Effective.Runtime.MaxContributionBodyBytes}}
			request.Observation.Scope, request.Observation.Audience, request.Observation.At = activation.Scope, audience, now
			if config.Kind == contextapi.ProviderKindBuiltin {
				response := contextprovider.ContributeBuiltin(ctx, request, activation.Scope.CanonicalRoot, builtinLimits(config.Limits, activation.Effective.Runtime))
				results <- result{index: index, config: config, request: request, contributions: response.Contributions, reasons: response.Reasons}
				return
			}
			client, release, err := runtime.acquireProvider(ctx, config, activation.Scope, activation.Effective.ConfigDigest, now, activation.Effective.Runtime)
			if err != nil {
				results <- result{index: index, config: config, request: request, reasons: []contextapi.Reason{runtimeReasonForAcquire(config.ID, now, err)}}
				return
			}
			response, callErr := client.Contribute(ctx, request)
			release()
			if callErr != nil {
				results <- result{index: index, config: config, request: request, reasons: []contextapi.Reason{runtimeReasonForProvider(contextapi.ReasonProviderFailed, config.ID, now, callErr.Error())}}
				return
			}
			results <- result{index: index, config: config, request: request, contributions: response.Contributions, reasons: response.Reasons}
		}()
	}
	wait.Wait()
	close(results)
	ordered := make([]result, len(configs))
	for item := range results {
		ordered[item.index] = item
	}
	var contributions []contextapi.Contribution
	evidence := make(map[contextapi.OfferItemIdentity]freshnessEvidence)
	var reasons []contextapi.Reason
	for _, item := range ordered {
		for _, contribution := range item.contributions {
			contribution = cloneContribution(contribution)
			contributions = append(contributions, contribution)
			identity := offerItemIdentity(contribution)
			evidence[identity] = freshnessEvidence{item: identity, source: cloneSourceRef(contribution.Source), provider: cloneProviderConfig(item.config), request: cloneContributionRequest(item.request), lastSeen: now}
		}
		reasons = append(reasons, cloneReasons(item.reasons)...)
	}
	return contributions, evidence, reasons
}

func (runtime *Runtime) composeProfile(ctx context.Context, activation contextapi.ActivationResult, host contextapi.HostSnapshot, directory string, audience contextapi.Audience, now time.Time) (contextapi.ProfileSnapshot, []contextapi.Reason) {
	facts, reasons := runtime.profileFacts(ctx, activation, host, directory, audience, now)
	composition := contextengine.Compose(contextapi.ProfileCompositionInput{Scope: activation.Scope, WorkingDirectory: directory, Audience: audience, Task: host.Task, Explicit: activation.Effective.Profile, HostFacts: cloneFacts(host.Facts), ProviderFacts: facts, ConfigDigest: activation.Effective.ConfigDigest, Now: now})
	return composition.Snapshot, append(reasons, composition.Reasons...)
}

func (runtime *Runtime) profileFacts(ctx context.Context, activation contextapi.ActivationResult, host contextapi.HostSnapshot, directory string, audience contextapi.Audience, now time.Time) ([]contextapi.ProfileFact, []contextapi.Reason) {
	configs := selectedProviderConfigs(activation.Effective.Providers, activation.Effective.ProfileProviders)
	if len(configs) == 0 {
		return nil, nil
	}
	type result struct {
		index   int
		facts   []contextapi.ProfileFact
		reasons []contextapi.Reason
	}
	results := make(chan result, len(configs))
	var wait sync.WaitGroup
	for index, config := range configs {
		index, config := index, cloneProviderConfig(config)
		wait.Add(1)
		go func() {
			defer wait.Done()
			if config.Kind == contextapi.ProviderKindBuiltin {
				results <- result{index: index, reasons: []contextapi.Reason{runtimeReasonForProvider(contextapi.ReasonProviderNotSelected, config.ID, now, "builtin provider has no profile capability")}}
				return
			}
			client, release, err := runtime.acquireProvider(ctx, config, activation.Scope, activation.Effective.ConfigDigest, now, activation.Effective.Runtime)
			if err != nil {
				results <- result{index: index, reasons: []contextapi.Reason{runtimeReasonForAcquire(config.ID, now, err)}}
				return
			}
			response, callErr := client.Profile(ctx, contextapi.ProfileRequest{RequestID: contextapi.RequestID(now.UnixNano() + int64(index)), Scope: activation.Scope, Audience: audience, Task: host.Task, Host: cloneHost(host), Explicit: activation.Effective.Profile, ConfigDigest: activation.Effective.ConfigDigest, Now: now, Limits: contextapi.ProfileLimits{MaxFacts: activation.Effective.Runtime.MaxProfileFacts}})
			release()
			if callErr != nil {
				results <- result{index: index, reasons: []contextapi.Reason{runtimeReasonForProvider(contextapi.ReasonProviderFailed, config.ID, now, callErr.Error())}}
				return
			}
			results <- result{index: index, facts: response.Facts, reasons: response.Reasons}
		}()
	}
	wait.Wait()
	close(results)
	ordered := make([]result, len(configs))
	for item := range results {
		ordered[item.index] = item
	}
	var facts []contextapi.ProfileFact
	var reasons []contextapi.Reason
	for _, item := range ordered {
		facts = append(facts, cloneProfileFacts(item.facts)...)
		reasons = append(reasons, cloneReasons(item.reasons)...)
	}
	return facts, reasons
}

func (runtime *Runtime) acquireProvider(ctx context.Context, config contextapi.ProviderConfig, scope contextapi.ScopeIdentity, digest contextapi.ConfigDigest, now time.Time, limits contextapi.RuntimeLimits) (*contextprovider.Client, func(), error) {
	key := providerKey{scope: scope.ID, root: scope.CanonicalRoot, configDigest: digest, provider: config.ID}
	resource := contextapi.ProviderResource{Provider: config.ID, Scope: scope, ConfigDigest: digest}
	maxProcesses := limits.MaxProviderProcesses
	if maxProcesses == 0 {
		maxProcesses = defaultProviderProcesses
	}
	for {
		runtime.mu.Lock()
		state := runtime.providers[key]
		if state == nil {
			if uint32(len(runtime.providers)) >= maxProcesses {
				runtime.mu.Unlock()
				return nil, func() {}, fmt.Errorf("%w: provider process bound is full", ErrRuntimeBounds)
			}
			state = &providerState{
				config:    cloneProviderConfig(config),
				resource:  resource,
				starting:  true,
				ready:     make(chan struct{}),
				closeDone: make(chan struct{}),
				lastUsed:  now,
				idleTTL:   runtimeIdleTTL(limits),
			}
			runtime.providers[key] = state
			runtime.mu.Unlock()
			client, err := contextprovider.Start(ctx, config, resource, contextprovider.Options{MaxResponseBytes: config.Limits.MaxResponseBytes, MaxStdoutBytes: config.Limits.MaxResponseBytes})
			runtime.mu.Lock()
			current := runtime.providers[key] == state
			state.starting, state.client, state.err = false, client, err
			if err != nil && current {
				delete(runtime.providers, key)
			}
			close(state.ready)
			runtime.mu.Unlock()
			if err != nil {
				return nil, func() {}, fmt.Errorf("start provider %q: %w", config.ID, err)
			}
			runtime.mu.Lock()
			if current && !runtime.closing {
				state.users++
				state.lastUsed = now
				runtime.mu.Unlock()
				return client, runtime.providerRelease(key, state), nil
			}
			if current {
				state.closing = true
			}
			runtime.mu.Unlock()
			if current {
				_ = runtime.closeProviderState(state)
			} else {
				closeCtx, cancel := context.WithTimeout(context.Background(), defaultHookDeadline)
				_ = client.Close(closeCtx)
				cancel()
			}
			return nil, func() {}, ErrRuntimeClosed
		}
		if state.starting {
			ready := state.ready
			runtime.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return nil, func() {}, ctx.Err()
			}
		}
		if state.closing {
			runtime.mu.Unlock()
			return nil, func() {}, fmt.Errorf("%w: provider process is closing", ErrRuntimeBounds)
		}
		if state.client == nil {
			err := state.err
			runtime.mu.Unlock()
			if err == nil {
				err = contextprovider.ErrProviderClosed
			}
			return nil, func() {}, err
		}
		state.users++
		state.lastUsed = now
		client := state.client
		runtime.mu.Unlock()
		return client, runtime.providerRelease(key, state), nil
	}
}

// closeProviderState is the sole owner of a successfully started provider's
// shutdown. The provider remains in runtime.providers, and therefore remains
// charged against MaxProviderProcesses and visible to Status, until Close has
// returned. No runtime mutex is held while the provider performs I/O.
func (runtime *Runtime) closeProviderState(state *providerState) error {
	state.closeOnce.Do(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), defaultWholeHookDeadline)
		state.closeErr = state.client.Close(closeCtx)
		cancel()
		close(state.closeDone)
	})
	<-state.closeDone
	runtime.mu.Lock()
	for key, current := range runtime.providers {
		if current == state {
			delete(runtime.providers, key)
			break
		}
	}
	runtime.mu.Unlock()
	return state.closeErr
}

func (runtime *Runtime) providerRelease(key providerKey, state *providerState) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			runtime.mu.Lock()
			if runtime.providers[key] == state && state.users > 0 {
				state.users--
				state.lastUsed = time.Now().UTC()
			}
			runtime.mu.Unlock()
		})
	}
}

func (runtime *Runtime) revokeSessionPartitions(harness contextapi.Harness, audience contextapi.AudienceID, epoch contextapi.EpochID, now time.Time, reasons []contextapi.Reason) {
	runtime.revokePartitions(func(key partitionKey) bool {
		return key.harness == harness && key.audience == audience && key.epoch == epoch
	}, now, reasons)
}

func (runtime *Runtime) revokeChangedConfigPartitions(harness contextapi.Harness, scope contextapi.ScopeIdentity, audience contextapi.AudienceID, digest contextapi.ConfigDigest, now time.Time) {
	runtime.revokePartitions(func(key partitionKey) bool {
		return key.harness == harness && key.scope == scope.ID && key.root == scope.CanonicalRoot && key.audience == audience && key.configDigest != digest
	}, now, []contextapi.Reason{runtimeReason(contextapi.ReasonWithdrawn, now, "configuration digest changed; old partition withdrawn")})
}

func (runtime *Runtime) withdrawInactive(harness contextapi.Harness, observation contextapi.Observation, now time.Time, reasons []contextapi.Reason) {
	if observation.Audience.ID == "" || observation.Scope.ID == "" {
		return
	}
	runtime.revokePartitions(func(key partitionKey) bool {
		return key.harness == harness && key.scope == observation.Scope.ID && key.root == observation.Scope.CanonicalRoot && key.audience == observation.Audience.ID
	}, now, reasons)
}

// findOfferPartition resolves a partition-local OfferID from the bounded
// resident partition set. ConfirmInput intentionally does not carry harness
// identity, so equal candidates across harnesses are rejected as ambiguous;
// map iteration can never choose a partition by accident. Engine.Confirm then
// remains the authority for exact live-offer identity and expiry.
func (runtime *Runtime) findOfferPartition(directory string, identity contextapi.OfferIdentity, activation contextapi.ActivationResult) (*partitionState, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if identity.Generation == 0 || identity.Generation != runtime.generation {
		return nil, false
	}
	var candidate *partitionState
	longest := -1
	ambiguous := false
	for key, state := range runtime.partitions {
		if key.scope != activation.Scope.ID || key.root != activation.Scope.CanonicalRoot || key.configDigest != activation.Effective.ConfigDigest || key.audience != identity.Audience.ID || key.epoch != identity.Audience.Epoch || !pathWithin(directory, key.root) {
			continue
		}
		if len(key.root) > longest {
			candidate, longest, ambiguous = state, len(key.root), false
			continue
		}
		if len(key.root) == longest {
			ambiguous = true
		}
	}
	if ambiguous {
		return nil, true
	}
	return candidate, false
}

func (runtime *Runtime) findInactiveOfferPartition(directory string, identity contextapi.OfferIdentity) (*partitionState, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if identity.Generation == 0 || identity.Generation != runtime.generation {
		return nil, false
	}
	var candidate *partitionState
	longest := -1
	ambiguous := false
	for key, state := range runtime.partitions {
		if key.audience != identity.Audience.ID || key.epoch != identity.Audience.Epoch || !pathWithin(directory, key.root) {
			continue
		}
		if len(key.root) > longest {
			candidate, longest, ambiguous = state, len(key.root), false
			continue
		}
		if len(key.root) == longest {
			ambiguous = true
		}
	}
	if ambiguous {
		return nil, true
	}
	return candidate, false
}

// This inactive path intentionally has no request lease: it only withdraws
// lock-fenced in-memory state and never starts providers or touches the trace
// worker, so it remains safe alongside Close.
func (runtime *Runtime) withdrawInactiveConfirmation(directory string, identity contextapi.OfferIdentity, now time.Time, reasons []contextapi.Reason) contextapi.ConfirmationResult {
	state, ambiguous := runtime.findInactiveOfferPartition(directory, identity)
	if state == nil {
		if ambiguous {
			reasons = append(reasons, runtimeReason(contextapi.ReasonOfferMismatch, now, "confirmation partition is ambiguous across harnesses; no state was withdrawn"))
		}
		return contextapi.ConfirmationResult{State: contextapi.DeliveryWithdrawn, Reasons: cloneReasons(reasons)}
	}
	withdrawalReasons := cloneReasons(reasons)
	runtime.admissionMu.Lock()
	if runtime.isResident(state) {
		state.mu.Lock()
		for _, item := range identity.Items {
			withdrawal := state.engine.Withdraw(contextapi.DeliveryWithdrawal{Now: now, Scope: state.scope, Audience: state.audience, Generation: identity.Generation, ConfigDigest: state.configDigest, Item: item})
			delete(state.sourceEvidence, item)
			withdrawalReasons = append(withdrawalReasons, withdrawal.Reasons...)
		}
		state.mu.Unlock()
	}
	runtime.admissionMu.Unlock()
	return contextapi.ConfirmationResult{State: contextapi.DeliveryWithdrawn, Reasons: dedupeReasons(withdrawalReasons, maxReasonCount)}
}

func pathWithin(directory, root string) bool {
	if directory == "" || root == "" {
		return false
	}
	directory, root = filepath.Clean(directory), filepath.Clean(root)
	return directory == root || strings.HasPrefix(directory, root+string(filepath.Separator))
}

func (runtime *Runtime) revokePartitions(match func(partitionKey) bool, now time.Time, reasons []contextapi.Reason) {
	runtime.admissionMu.Lock()
	runtime.mu.Lock()
	removed := make([]*partitionState, 0)
	for key, state := range runtime.partitions {
		if match(key) {
			delete(runtime.partitions, key)
			removed = append(removed, state)
		}
	}
	runtime.mu.Unlock()
	runtime.admissionMu.Unlock()
	for _, state := range removed {
		state.mu.Lock()
		profile, scope, audience := cloneProfileSnapshot(state.profile), state.scope, state.audience
		state.sourceEvidence = make(map[contextapi.OfferItemIdentity]freshnessEvidence)
		state.mu.Unlock()
		runtime.traceDecision(state, contextapi.Observation{At: now, Scope: scope, Audience: audience}, profile, contextapi.DeliveryDecision{State: contextapi.DeliveryWithdrawn, Reasons: cloneReasons(reasons)}, nil, now)
	}
}

func (runtime *Runtime) evictIdle(now time.Time) {
	runtime.mu.Lock()
	states := make([]struct {
		key   partitionKey
		state *partitionState
	}, 0, len(runtime.partitions))
	for key, state := range runtime.partitions {
		states = append(states, struct {
			key   partitionKey
			state *partitionState
		}{key, state})
	}
	active := runtime.active
	providers := make([]struct {
		key   providerKey
		state *providerState
	}, 0, len(runtime.providers))
	for key, state := range runtime.providers {
		providers = append(providers, struct {
			key   providerKey
			state *providerState
		}{key, state})
	}
	runtime.mu.Unlock()
	expired := make([]*partitionState, 0)
	for _, entry := range states {
		if active != 0 {
			break
		}
		runtime.admissionMu.Lock()
		entry.state.mu.Lock()
		last, ttl := entry.state.lastUsedAt, entry.state.idleTTL
		entry.state.mu.Unlock()
		if ttl <= 0 || last.IsZero() || now.Before(last) || now.Sub(last) < ttl {
			runtime.admissionMu.Unlock()
			continue
		}
		runtime.mu.Lock()
		if runtime.active == 0 && runtime.partitions[entry.key] == entry.state {
			delete(runtime.partitions, entry.key)
			expired = append(expired, entry.state)
		}
		runtime.mu.Unlock()
		runtime.admissionMu.Unlock()
	}
	for _, state := range expired {
		state.mu.Lock()
		stats := state.engine.Stats()
		profile, scope, audience := cloneProfileSnapshot(state.profile), state.scope, state.audience
		state.sourceEvidence = make(map[contextapi.OfferItemIdentity]freshnessEvidence)
		state.mu.Unlock()
		reason := runtimeReason(contextapi.ReasonWithdrawn, now, fmt.Sprintf("idle TTL expired; resident partition evicted (pending=%d liveOffers=%d receipts=%d)", stats.PendingItems, stats.LiveOffers, stats.Receipts))
		reason.Params = []contextapi.ReasonParameter{
			{Key: "idle.pendingItems", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: int64(stats.PendingItems)}},
			{Key: "idle.liveOffers", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: int64(stats.LiveOffers)}},
			{Key: "idle.receipts", Value: contextapi.ReasonValue{Kind: contextapi.ReasonValueNumber, Number: int64(stats.Receipts)}},
		}
		runtime.traceDecision(state, contextapi.Observation{At: now, Scope: scope, Audience: audience}, profile, contextapi.DeliveryDecision{State: contextapi.DeliveryWithdrawn, Reasons: []contextapi.Reason{reason}}, nil, now)
	}
	for _, entry := range providers {
		runtime.mu.Lock()
		current := runtime.providers[entry.key] == entry.state
		eligible := current && !entry.state.starting && !entry.state.closing && entry.state.client != nil && entry.state.users == 0 && !entry.state.lastUsed.IsZero() && !now.Before(entry.state.lastUsed) && now.Sub(entry.state.lastUsed) >= entry.state.idleTTL
		if eligible {
			entry.state.closing = true
		}
		runtime.mu.Unlock()
		if eligible {
			_ = runtime.closeProviderState(entry.state)
		}
	}
}

func (runtime *Runtime) profileAndIdle(now time.Time) (bool, time.Duration) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active != 0 || len(runtime.partitions) != 0 || len(runtime.providers) != 0 {
		return false, 0
	}
	if runtime.idleTTL <= 0 {
		return true, defaultIdleTTL
	}
	return true, runtime.idleTTL
}

func statusRequestMatches(request StatusRequest, state *partitionState) bool {
	if request.WorkingDirectory != "" {
		clean := filepath.Clean(request.WorkingDirectory)
		if clean != state.scope.CanonicalRoot && !strings.HasPrefix(clean, state.scope.CanonicalRoot+string(filepath.Separator)) {
			return false
		}
	}
	if request.Scope.ID != "" && (request.Scope.ID != state.scope.ID || (request.Scope.CanonicalRoot != "" && request.Scope.CanonicalRoot != state.scope.CanonicalRoot)) {
		return false
	}
	if request.Audience.ID != "" && (request.Audience.ID != state.audience.ID || (request.Audience.Epoch != 0 && request.Audience.Epoch != state.audience.Epoch)) {
		return false
	}
	return true
}

func selectedProviderConfigs(configs []contextapi.ProviderConfig, ids []contextapi.ProviderID) []contextapi.ProviderConfig {
	if len(ids) == 0 {
		return nil
	}
	wanted := make(map[contextapi.ProviderID]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	result := make([]contextapi.ProviderConfig, 0, len(ids))
	for _, config := range configs {
		if _, ok := wanted[config.ID]; ok {
			result = append(result, cloneProviderConfig(config))
		}
	}
	return result
}

func filterContributions(input []contextapi.Contribution, blocked map[contextapi.OfferItemIdentity]struct{}) []contextapi.Contribution {
	if len(blocked) == 0 {
		return input
	}
	result := make([]contextapi.Contribution, 0, len(input))
	for _, contribution := range input {
		if _, ok := blocked[offerItemIdentity(contribution)]; !ok {
			result = append(result, contribution)
		}
	}
	return result
}

func offerItemIdentity(contribution contextapi.Contribution) contextapi.OfferItemIdentity {
	return contextapi.OfferItemIdentity{Slot: contribution.Slot, Source: contribution.Source.Identity, SourceRevision: contribution.SourceRevision, Content: contribution.Content, Mode: contextapi.DeliveryFullBody}
}

func builtinLimits(provider contextapi.ProviderLimits, runtime contextapi.RuntimeLimits) contextprovider.BuiltinLimits {
	maxContributions, maxBody := provider.MaxContributions, provider.MaxBodyBytes
	if runtime.MaxContributions > 0 && (maxContributions == 0 || runtime.MaxContributions < maxContributions) {
		maxContributions = runtime.MaxContributions
	}
	if runtime.MaxContributionBodyBytes > 0 && (maxBody == 0 || runtime.MaxContributionBodyBytes < maxBody) {
		maxBody = runtime.MaxContributionBodyBytes
	}
	return contextprovider.BuiltinLimits{MaxContributions: maxContributions, MaxBodyBytes: maxBody, MaxOutputBytes: provider.MaxResponseBytes, MaxReasonBytes: provider.MaxResponseBytes}
}

func dedupeReasons(input []contextapi.Reason, max int) []contextapi.Reason {
	seen := make(map[string]struct{}, len(input))
	result := make([]contextapi.Reason, 0, minInt(len(input), max))
	for _, reason := range input {
		key := string(reason.Code) + "\x00" + reason.Summary + "\x00" + string(reason.Provider)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, reason)
		if len(result) == max {
			break
		}
	}
	return result
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (runtime *Runtime) pruneEvidenceLocked(state *partitionState) {
	pending := state.engine.PendingSources()
	keep := make(map[contextapi.OfferItemIdentity]freshnessEvidence, len(pending))
	for _, item := range pending {
		if value, ok := state.sourceEvidence[item.Identity]; ok {
			keep[item.Identity] = value
		}
	}
	state.sourceEvidence = keep
}

func cloneFreshnessEvidence(input freshnessEvidence) freshnessEvidence {
	input.item = cloneOfferItemIdentity(input.item)
	input.source = cloneSourceRef(input.source)
	input.provider = cloneProviderConfig(input.provider)
	input.request = cloneContributionRequest(input.request)
	return input
}

func cloneContributionRequest(input contextapi.ContributionRequest) contextapi.ContributionRequest {
	result := input
	result.RequestID = input.RequestID
	result.Scope = cloneScopeIdentity(input.Scope)
	result.Audience = cloneAudience(input.Audience)
	result.Task = cloneTaskRef(input.Task)
	result.Observation = cloneObservation(input.Observation)
	result.Profile = cloneProfileSnapshot(input.Profile)
	result.ConfigDigest = contextapi.ConfigDigest(strings.Clone(string(input.ConfigDigest)))
	result.Limits = input.Limits
	return result
}

func cloneContribution(input contextapi.Contribution) contextapi.Contribution {
	result := input
	result.Slot = cloneContributionSlot(input.Slot)
	result.Source = cloneSourceRef(input.Source)
	result.SourceRevision = cloneSourceRevision(input.SourceRevision)
	result.SourceRevision.Source = cloneSourceIdentity(result.Source.Identity)
	result.Body = strings.Clone(input.Body)
	result.Recruitment.Kind = contextapi.SourceRecruitmentKind(strings.Clone(string(input.Recruitment.Kind)))
	result.Recruitment.Resource = strings.Clone(input.Recruitment.Resource)
	result.Content.ID = contextapi.ContentID(strings.Clone(string(input.Content.ID)))
	result.Contributor = contextapi.ProviderID(strings.Clone(string(input.Contributor)))
	result.ConfigDigest = contextapi.ConfigDigest(strings.Clone(string(input.ConfigDigest)))
	result.ProfileRevision = contextapi.ProfileRevision(strings.Clone(string(input.ProfileRevision)))
	result.Reasons = cloneReasons(input.Reasons)
	return result
}

func cloneProviderConfig(input contextapi.ProviderConfig) contextapi.ProviderConfig {
	result := input
	result.ID = contextapi.ProviderID(strings.Clone(string(input.ID)))
	result.Kind = contextapi.ProviderKind(strings.Clone(string(input.Kind)))
	result.Executable = strings.Clone(input.Executable)
	result.Arguments = make([]string, len(input.Arguments))
	for i, argument := range input.Arguments {
		result.Arguments[i] = strings.Clone(argument)
	}
	result.Capabilities = make([]contextapi.ProviderCapability, len(input.Capabilities))
	for i, capability := range input.Capabilities {
		result.Capabilities[i] = contextapi.ProviderCapability(strings.Clone(string(capability)))
	}
	result.Settings = append([]byte(nil), input.Settings...)
	return result
}

func cloneHost(input contextapi.HostSnapshot) contextapi.HostSnapshot {
	result := input
	result.Harness = contextapi.Harness(strings.Clone(string(input.Harness)))
	result.WorkingDirectory, result.RepositoryRoot = strings.Clone(input.WorkingDirectory), strings.Clone(input.RepositoryRoot)
	result.Turn = cloneTurnRef(input.Turn)
	result.Task = cloneTaskRef(input.Task)
	result.Facts = cloneFacts(input.Facts)
	result.Capabilities.Harness = contextapi.Harness(strings.Clone(string(input.Capabilities.Harness)))
	result.Capabilities.AudienceIdentity = contextapi.IdentityAvailability(strings.Clone(string(input.Capabilities.AudienceIdentity)))
	result.Capabilities.EpochIdentity = contextapi.IdentityAvailability(strings.Clone(string(input.Capabilities.EpochIdentity)))
	result.Capabilities.TurnIdentity = contextapi.IdentityAvailability(strings.Clone(string(input.Capabilities.TurnIdentity)))
	result.Capabilities.BudgetUnit = strings.Clone(input.Capabilities.BudgetUnit)
	result.Capabilities.Observations = make([]contextapi.ObservationKind, len(input.Capabilities.Observations))
	for i, observation := range input.Capabilities.Observations {
		result.Capabilities.Observations[i] = contextapi.ObservationKind(strings.Clone(string(observation)))
	}
	result.Capabilities.DeliverySurfaces = make([]contextapi.DeliverySurface, len(input.Capabilities.DeliverySurfaces))
	for i, surface := range input.Capabilities.DeliverySurfaces {
		result.Capabilities.DeliverySurfaces[i] = contextapi.DeliverySurface(strings.Clone(string(surface)))
	}
	return result
}

func cloneFacts(input []contextapi.Fact) []contextapi.Fact {
	if input == nil {
		return nil
	}
	result := make([]contextapi.Fact, len(input))
	for i, fact := range input {
		result[i] = cloneFact(fact)
	}
	return result
}

func cloneObservation(input contextapi.Observation) contextapi.Observation {
	result := input
	result.CausalID = contextapi.CausalID(strings.Clone(string(input.CausalID)))
	result.Audience = cloneAudience(input.Audience)
	result.Scope = cloneScopeIdentity(input.Scope)
	result.Invocation.ID = contextapi.InvocationID(strings.Clone(string(input.Invocation.ID)))
	result.Invocation.HookEvent = strings.Clone(input.Invocation.HookEvent)
	result.Turn = cloneTurnRef(input.Turn)
	result.Trigger = contextapi.ObservationTrigger(strings.Clone(string(input.Trigger)))
	result.Resources = make([]contextapi.ObservedResource, len(input.Resources))
	for i, resource := range input.Resources {
		result.Resources[i] = resource
		result.Resources[i].Path = strings.Clone(resource.Path)
		result.Resources[i].Kind = contextapi.ResourceKind(strings.Clone(string(resource.Kind)))
		result.Resources[i].Operation = contextapi.ResourceOperation(strings.Clone(string(resource.Operation)))
		result.Resources[i].Outcome = contextapi.ResourceOutcome(strings.Clone(string(resource.Outcome)))
		result.Resources[i].Confidence = contextapi.EvidenceConfidence(strings.Clone(string(resource.Confidence)))
	}
	result.Selectors = make([]contextapi.ObservedSelector, len(input.Selectors))
	for i, selector := range input.Selectors {
		result.Selectors[i] = selector
		result.Selectors[i].Raw = strings.Clone(selector.Raw)
		result.Selectors[i].ObservedAs = contextapi.SelectorObservedAs(strings.Clone(string(selector.ObservedAs)))
		result.Selectors[i].Confidence = contextapi.EvidenceConfidence(strings.Clone(string(selector.Confidence)))
		result.Selectors[i].Interpretations = make([]contextapi.SelectorInterpretation, len(selector.Interpretations))
		for j, interpretation := range selector.Interpretations {
			result.Selectors[i].Interpretations[j] = contextapi.SelectorInterpretation(strings.Clone(string(interpretation)))
		}
	}
	result.Facts = cloneFacts(input.Facts)
	return result
}

func cloneProfileSnapshot(input contextapi.ProfileSnapshot) contextapi.ProfileSnapshot {
	result := input
	result.Revision = contextapi.ProfileRevision(strings.Clone(string(input.Revision)))
	result.Audience = cloneAudience(input.Audience)
	result.Facts = cloneProfileFacts(input.Facts)
	return result
}

func cloneProfileFacts(input []contextapi.ProfileFact) []contextapi.ProfileFact {
	if input == nil {
		return nil
	}
	result := make([]contextapi.ProfileFact, len(input))
	for i, fact := range input {
		result[i] = fact
		result[i].Key = strings.Clone(fact.Key)
		result[i].Value = cloneFactValue(fact.Value)
		result[i].AppliesTo.Directory = strings.Clone(fact.AppliesTo.Directory)
		result[i].AppliesTo.Audience = cloneAudience(fact.AppliesTo.Audience)
		result[i].AppliesTo.Task = cloneTaskRef(fact.AppliesTo.Task)
		result[i].Validity.Policy = contextapi.ValidityPolicy(strings.Clone(string(fact.Validity.Policy)))
		result[i].Validity.Rule = strings.Clone(fact.Validity.Rule)
		result[i].Validity.Inputs = cloneReasonParameters(fact.Validity.Inputs)
		result[i].Provenance.Origin = contextapi.FactOrigin(strings.Clone(string(fact.Provenance.Origin)))
		result[i].Provenance.Provider = contextapi.ProviderID(strings.Clone(string(fact.Provenance.Provider)))
		result[i].Provenance.Rule = strings.Clone(fact.Provenance.Rule)
		result[i].Provenance.SourceRevision = cloneSourceRevision(fact.Provenance.SourceRevision)
	}
	return result
}

func cloneReasons(input []contextapi.Reason) []contextapi.Reason {
	if input == nil {
		return nil
	}
	result := make([]contextapi.Reason, len(input))
	for i, reason := range input {
		result[i] = reason
		result[i].Code = contextapi.ReasonCode(strings.Clone(string(reason.Code)))
		result[i].Origin = contextapi.ReasonOrigin(strings.Clone(string(reason.Origin)))
		result[i].Summary, result[i].Rule = strings.Clone(reason.Summary), strings.Clone(reason.Rule)
		result[i].Provider = contextapi.ProviderID(strings.Clone(string(reason.Provider)))
		result[i].Params = cloneReasonParameters(reason.Params)
		result[i].Evidence = append([]contextapi.EvidenceID(nil), reason.Evidence...)
	}
	return result
}

func cloneOfferIdentity(input contextapi.OfferIdentity) contextapi.OfferIdentity {
	result := input
	result.Generation = input.Generation
	result.Audience = cloneAudience(input.Audience)
	result.OpportunityID = strings.Clone(input.OpportunityID)
	result.Surface = contextapi.DeliverySurface(strings.Clone(string(input.Surface)))
	result.Items = make([]contextapi.OfferItemIdentity, len(input.Items))
	for i, item := range input.Items {
		result.Items[i] = cloneOfferItemIdentity(item)
	}
	result.Body.ID = contextapi.ContentID(strings.Clone(string(input.Body.ID)))
	return result
}

func cloneHandoff(input contextapi.HandoffOutcome) contextapi.HandoffOutcome {
	result := input
	result.State = contextapi.HandoffState(strings.Clone(string(input.State)))
	result.EmittedContent.ID = contextapi.ContentID(strings.Clone(string(input.EmittedContent.ID)))
	result.Reasons = cloneReasons(input.Reasons)
	return result
}

func cloneSourceRef(input contextapi.SourceRef) contextapi.SourceRef {
	result := input
	result.Identity = cloneSourceIdentity(input.Identity)
	result.Kind = contextapi.SourceKind(strings.Clone(string(input.Kind)))
	result.Path, result.Label = strings.Clone(input.Path), strings.Clone(input.Label)
	return result
}

func cloneAudience(input contextapi.Audience) contextapi.Audience {
	return contextapi.Audience{
		ID:         contextapi.AudienceID(strings.Clone(string(input.ID))),
		Epoch:      input.Epoch,
		Continuity: contextapi.Continuity(strings.Clone(string(input.Continuity))),
	}
}

func cloneScopeIdentity(input contextapi.ScopeIdentity) contextapi.ScopeIdentity {
	return contextapi.ScopeIdentity{
		ID:            contextapi.ScopeID(strings.Clone(string(input.ID))),
		Authority:     contextapi.ScopeAuthority(strings.Clone(string(input.Authority))),
		CanonicalRoot: strings.Clone(input.CanonicalRoot),
		ConfigDigest:  contextapi.ConfigDigest(strings.Clone(string(input.ConfigDigest))),
	}
}

func cloneTaskRef(input contextapi.TaskRef) contextapi.TaskRef {
	return contextapi.TaskRef{ID: contextapi.TaskID(strings.Clone(string(input.ID))), Kind: strings.Clone(input.Kind)}
}

func cloneTurnRef(input contextapi.TurnRef) contextapi.TurnRef {
	return contextapi.TurnRef{ID: contextapi.TurnID(strings.Clone(string(input.ID))), State: contextapi.TurnState(strings.Clone(string(input.State)))}
}

func cloneFactValue(input contextapi.FactValue) contextapi.FactValue {
	input.Kind = contextapi.FactValueKind(strings.Clone(string(input.Kind)))
	input.Text = strings.Clone(input.Text)
	return input
}

func cloneFact(input contextapi.Fact) contextapi.Fact {
	input.Key = strings.Clone(input.Key)
	input.Value = cloneFactValue(input.Value)
	input.Origin = contextapi.FactOrigin(strings.Clone(string(input.Origin)))
	input.Provider = contextapi.ProviderID(strings.Clone(string(input.Provider)))
	return input
}

func cloneReasonValue(input contextapi.ReasonValue) contextapi.ReasonValue {
	input.Kind = contextapi.ReasonValueKind(strings.Clone(string(input.Kind)))
	input.Text = strings.Clone(input.Text)
	return input
}

func cloneReasonParameter(input contextapi.ReasonParameter) contextapi.ReasonParameter {
	return contextapi.ReasonParameter{Key: strings.Clone(input.Key), Value: cloneReasonValue(input.Value)}
}

func cloneReasonParameters(input []contextapi.ReasonParameter) []contextapi.ReasonParameter {
	if input == nil {
		return nil
	}
	result := make([]contextapi.ReasonParameter, len(input))
	for i, parameter := range input {
		result[i] = cloneReasonParameter(parameter)
	}
	return result
}

func cloneSourceIdentity(input contextapi.SourceIdentity) contextapi.SourceIdentity {
	return contextapi.SourceIdentity{Provider: contextapi.ProviderID(strings.Clone(string(input.Provider))), ID: contextapi.SourceID(strings.Clone(string(input.ID)))}
}

func cloneSourceRevision(input contextapi.SourceRevision) contextapi.SourceRevision {
	return contextapi.SourceRevision{Source: cloneSourceIdentity(input.Source), Revision: contextapi.SourceRevisionID(strings.Clone(string(input.Revision)))}
}

func cloneContributionSlot(input contextapi.ContributionSlot) contextapi.ContributionSlot {
	return contextapi.ContributionSlot{Provider: contextapi.ProviderID(strings.Clone(string(input.Provider))), Key: contextapi.SlotKey(strings.Clone(string(input.Key)))}
}

func cloneOfferItemIdentity(input contextapi.OfferItemIdentity) contextapi.OfferItemIdentity {
	return contextapi.OfferItemIdentity{
		Slot:           cloneContributionSlot(input.Slot),
		Source:         cloneSourceIdentity(input.Source),
		SourceRevision: cloneSourceRevision(input.SourceRevision),
		Content:        contextapi.ContentIdentity{ID: contextapi.ContentID(strings.Clone(string(input.Content.ID))), UTF8Bytes: input.Content.UTF8Bytes},
		Mode:           contextapi.DeliveryMode(strings.Clone(string(input.Mode))),
	}
}

func runtimeReason(code contextapi.ReasonCode, at time.Time, summary string) contextapi.Reason {
	return contextapi.Reason{Code: code, Origin: contextapi.ReasonRuntime, Summary: truncateReason(summary), At: at}
}

func runtimeReasonForProvider(code contextapi.ReasonCode, provider contextapi.ProviderID, at time.Time, summary string) contextapi.Reason {
	reason := runtimeReason(code, at, summary)
	reason.Provider, reason.Origin = provider, contextapi.ReasonProvider
	return reason
}

func runtimeReasonForAcquire(provider contextapi.ProviderID, at time.Time, err error) contextapi.Reason {
	code := contextapi.ReasonProviderFailed
	if errors.Is(err, ErrRuntimeBounds) {
		// A closing or globally saturated provider slot is a transient runtime
		// admission condition, not evidence that the provider itself failed.
		code = contextapi.ReasonEvidenceUnavailable
	}
	return runtimeReasonForProvider(code, provider, at, err.Error())
}

func truncateReason(value string) string {
	if len(value) <= maxReasonText {
		return value
	}
	return value[:maxReasonText]
}

func confirmationFailure(err error, summary string) contextapi.ConfirmationResult {
	code := contextapi.ReasonOutputUnknown
	if errors.Is(err, ErrRuntimeInput) {
		code = contextapi.ReasonInvalidInput
	}
	return contextapi.ConfirmationResult{State: contextapi.DeliveryRejected, Reasons: []contextapi.Reason{{Code: code, Origin: contextapi.ReasonRuntime, Summary: truncateReason(summary + ": " + err.Error()), At: time.Now().UTC()}}}
}

func newGeneration() contextapi.RuntimeGeneration {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		value := binary.BigEndian.Uint64(bytes[:])
		if value != 0 {
			return contextapi.RuntimeGeneration(value)
		}
	}
	return contextapi.RuntimeGeneration(time.Now().UnixNano())
}

func normalizePaths(input Paths) (Paths, error) {
	if input.RuntimeDir == "" && input.SocketPath != "" {
		input.RuntimeDir = filepath.Dir(input.SocketPath)
	}
	if input.RuntimeDir == "" {
		return Paths{}, fmt.Errorf("%w: runtime directory is required", ErrRuntimeInput)
	}
	input.RuntimeDir = filepath.Clean(input.RuntimeDir)
	if !filepath.IsAbs(input.RuntimeDir) {
		return Paths{}, fmt.Errorf("%w: runtime directory must be absolute", ErrRuntimeInput)
	}
	if input.SocketPath == "" {
		input.SocketPath = filepath.Join(input.RuntimeDir, "context.sock")
	}
	if input.LockPath == "" {
		input.LockPath = filepath.Join(input.RuntimeDir, "context.start.lock")
	}
	if input.ServerLockPath == "" {
		input.ServerLockPath = filepath.Join(input.RuntimeDir, "context.server.lock")
	}
	if input.CacheDir == "" {
		input.CacheDir = filepath.Join(input.RuntimeDir, "cache")
	}
	for name, path := range map[string]string{"socket": input.SocketPath, "startup lock": input.LockPath, "server lock": input.ServerLockPath, "cache": input.CacheDir} {
		if !filepath.IsAbs(path) {
			return Paths{}, fmt.Errorf("%w: %s path must be absolute", ErrRuntimeInput, name)
		}
	}
	if input.HomeConfigPath != "" && !filepath.IsAbs(input.HomeConfigPath) {
		return Paths{}, fmt.Errorf("%w: home configuration path must be absolute", ErrRuntimeInput)
	}
	return input, nil
}

func traceOptions(options RuntimeOptions) contexttrace.Options {
	result := options.TraceOptions
	if options.Paths.HomeConfigPath == "" {
		return result
	}
	// Loading the explicit home file from its own directory is enough to
	// obtain machine-wide cache limits without guessing a cwd. Failure is
	// handled by the runtime's unavailable-cache diagnostic.
	loaded, err := contextconfig.Load(contextconfig.LoadOptions{WorkingDirectory: filepath.Dir(options.Paths.HomeConfigPath), HomeConfigPath: options.Paths.HomeConfigPath, Now: time.Now().UTC(), Limits: options.LoadLimits})
	if err == nil {
		cache := loaded.Input.Config.Home.Cache
		if cache.DiskCapBytes > 0 {
			result.DiskCap = int64(cache.DiskCapBytes)
		}
		if cache.MaxRecordBytes > 0 {
			result.MaxRecordBytes = int(cache.MaxRecordBytes)
		}
		if cache.MaxInputBytes > 0 {
			result.MaxInputBytes = int(cache.MaxInputBytes)
		}
		if cache.MaxStringBytes > 0 {
			result.MaxStringBytes = int(cache.MaxStringBytes)
		}
		if cache.MaxReasons > 0 {
			result.MaxReasons = int(cache.MaxReasons)
		}
		if cache.MaxReasonParameters > 0 {
			result.MaxParameters = int(cache.MaxReasonParameters)
		}
		if cache.MaxQueryRecords > 0 {
			result.MaxQueryRecords = int(cache.MaxQueryRecords)
		}
		if cache.MaxQueryBytes > 0 {
			result.MaxQueryBytes = int(cache.MaxQueryBytes)
		}
		if cache.MaxScanBytes > 0 {
			result.MaxScanBytes = int(cache.MaxScanBytes)
		}
		if cache.MaxBlockBytes > 0 {
			result.MaxBlockBytes = int(cache.MaxBlockBytes)
		}
		if cache.MaxBlockRecords > 0 {
			result.MaxBlockRecords = int(cache.MaxBlockRecords)
		}
		if cache.MaxBlocks > 0 {
			result.MaxBlocks = int(cache.MaxBlocks)
		}
	}
	return result
}

func homeIdleTTL(options RuntimeOptions) time.Duration {
	if options.Paths.HomeConfigPath == "" {
		return defaultIdleTTL
	}
	loaded, err := contextconfig.Load(contextconfig.LoadOptions{WorkingDirectory: filepath.Dir(options.Paths.HomeConfigPath), HomeConfigPath: options.Paths.HomeConfigPath, Now: time.Now().UTC(), Limits: options.LoadLimits})
	if err == nil && loaded.Input.Config.Home.Runtime.IdleTTLMs > 0 {
		return time.Duration(loaded.Input.Config.Home.Runtime.IdleTTLMs) * time.Millisecond
	}
	return defaultIdleTTL
}

func stableContributionID(item contextapi.OfferItemIdentity) uint64 {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%v|%v|%v|%v|%v", item.Slot, item.Source, item.SourceRevision, item.Content, item.Mode)))
	value := binary.BigEndian.Uint64(hash[:8])
	if value == 0 {
		return 1
	}
	return value
}

func traceKind(state contextapi.DeliveryState) contexttrace.EventKind {
	switch state {
	case contextapi.DeliveryQueued:
		return contexttrace.KindQueued
	case contextapi.DeliveryOffered:
		return contexttrace.KindOffered
	case contextapi.DeliveryConfirmed:
		return contexttrace.KindConfirmed
	case contextapi.DeliverySuppressed:
		return contexttrace.KindSuppressed
	case contextapi.DeliveryDeferred:
		return contexttrace.KindDeferred
	case contextapi.DeliveryRejected:
		return contexttrace.KindRejected
	case contextapi.DeliveryFailed:
		return contexttrace.KindFailed
	case contextapi.DeliveryWithdrawn:
		return contexttrace.KindWithdrawn
	default:
		return contexttrace.KindObservation
	}
}

func traceOutcome(state contextapi.DeliveryState) contexttrace.OutcomeCode {
	switch state {
	case contextapi.DeliveryQueued:
		return contexttrace.OutcomeQueued
	case contextapi.DeliveryOffered:
		return contexttrace.OutcomeOffered
	case contextapi.DeliveryConfirmed:
		return contexttrace.OutcomeConfirmed
	case contextapi.DeliverySuppressed:
		return contexttrace.OutcomeSuppressed
	case contextapi.DeliveryDeferred:
		return contexttrace.OutcomeDeferred
	case contextapi.DeliveryRejected:
		return contexttrace.OutcomeRejected
	case contextapi.DeliveryFailed:
		return contexttrace.OutcomeFailed
	case contextapi.DeliveryWithdrawn:
		return contexttrace.OutcomeWithdrawn
	default:
		return contexttrace.OutcomeNone
	}
}

func (runtime *Runtime) traceDecision(state *partitionState, observation contextapi.Observation, profile contextapi.ProfileSnapshot, decision contextapi.DeliveryDecision, contributions []contextapi.Contribution, now time.Time) {
	if runtime.trace == nil {
		return
	}
	at := observation.At
	if at.IsZero() {
		at = now
	}
	base := contexttrace.Record{Kind: contexttrace.KindObservation, At: at, Scope: string(state.scope.ID), Audience: string(state.audience.ID), Epoch: uint64(state.audience.Epoch), Profile: string(profile.Revision), Inputs: traceInputs(observation, profile)}
	if observation.Turn.State == contextapi.TurnKnown {
		base.Turn = string(observation.Turn.ID)
	}
	runtime.trace.append(base)
	contributionID := uint64(0)
	for _, contribution := range contributions {
		id := stableContributionID(offerItemIdentity(contribution))
		if contributionID == 0 {
			contributionID = id
		}
		runtime.trace.append(contexttrace.Record{Kind: contexttrace.KindContribution, At: at, Scope: base.Scope, Audience: base.Audience, Epoch: base.Epoch, Turn: base.Turn, Profile: base.Profile, Source: string(contribution.Source.Identity.ID), Contributor: string(contribution.Contributor), ContributionID: id, Content: boundedTraceContent(contribution.Body), Reasons: traceReasons(contribution.Reasons)})
	}
	if len(decision.Offer.Identity.Items) > 0 {
		// One record per selected item keeps every coalesced contributor
		// inspectable by the same stable ContributionID.
		for _, item := range decision.Offer.Identity.Items {
			record := contexttrace.Record{Kind: traceKind(decision.State), At: at, Scope: base.Scope, Audience: base.Audience, Epoch: base.Epoch, Turn: base.Turn, Profile: base.Profile, Source: string(item.Source.ID), ContributionID: stableContributionID(item), Outcome: traceOutcome(decision.State), Inputs: appendTraceInputs(base.Inputs, traceItemInputs(item)), Reasons: traceReasons(decision.Reasons)}
			if decision.Offer.Body != "" {
				record.Content = boundedTraceContent(decision.Offer.Body)
			}
			runtime.trace.append(record)
		}
		return
	}
	runtime.trace.append(contexttrace.Record{Kind: traceKind(decision.State), At: at, Scope: base.Scope, Audience: base.Audience, Epoch: base.Epoch, Turn: base.Turn, Profile: base.Profile, ContributionID: contributionID, Outcome: traceOutcome(decision.State), Inputs: base.Inputs, Reasons: traceReasons(decision.Reasons)})
}

func (runtime *Runtime) traceConfirmation(state *partitionState, identity contextapi.OfferIdentity, handoff contextapi.HandoffOutcome, result contextapi.ConfirmationResult, profile contextapi.ProfileSnapshot, now time.Time) {
	if runtime.trace == nil {
		return
	}
	inputs := traceContentIdentityInputs(handoff.EmittedContent)
	if len(identity.Items) == 0 {
		runtime.trace.append(contexttrace.Record{Kind: traceKind(result.State), At: now, Scope: string(state.scope.ID), Audience: string(state.audience.ID), Epoch: uint64(state.audience.Epoch), Profile: string(profile.Revision), Inputs: inputs, Outcome: traceOutcome(result.State), Reasons: traceReasons(result.Reasons)})
		return
	}
	for _, item := range identity.Items {
		runtime.trace.append(contexttrace.Record{Kind: traceKind(result.State), At: now, Scope: string(state.scope.ID), Audience: string(state.audience.ID), Epoch: uint64(state.audience.Epoch), Profile: string(profile.Revision), Source: string(item.Source.ID), ContributionID: stableContributionID(item), Inputs: appendTraceInputs(inputs, traceItemInputs(item)), Outcome: traceOutcome(result.State), Reasons: traceReasons(result.Reasons)})
	}
}

func boundedTraceContent(value string) []byte {
	return []byte(value)
}

func traceInputs(observation contextapi.Observation, profile contextapi.ProfileSnapshot) []contexttrace.Input {
	result := make([]contexttrace.Input, 0, 16+len(observation.Resources)*3+len(observation.Selectors)*2+len(profile.Facts)*3)
	result = append(result,
		traceStringInput("observation.causalId", string(observation.CausalID)),
		traceUintInput("observation.id", uint64(observation.ID)),
		traceStringInput("observation.invocationId", string(observation.Invocation.ID)),
		traceStringInput("observation.hookEvent", observation.Invocation.HookEvent),
		traceStringInput("observation.trigger", string(observation.Trigger)),
		traceStringInput("observation.turn", string(observation.Turn.ID)),
	)
	for _, resource := range observation.Resources {
		result = append(result,
			traceStringInput("resource.path", resource.Path), traceStringInput("resource.kind", string(resource.Kind)),
			traceStringInput("resource.operation", string(resource.Operation)), traceStringInput("resource.outcome", string(resource.Outcome)),
			traceStringInput("resource.confidence", string(resource.Confidence)))
	}
	for _, selector := range observation.Selectors {
		result = append(result, traceStringInput("selector.raw", selector.Raw), traceStringInput("selector.observedAs", string(selector.ObservedAs)), traceStringInput("selector.confidence", string(selector.Confidence)))
		for _, interpretation := range selector.Interpretations {
			result = append(result, traceStringInput("selector.interpretation", string(interpretation)))
		}
	}
	for _, fact := range observation.Facts {
		result = append(result, traceStringInput("observation.fact.key", fact.Key), traceFactValue("observation.fact.value", fact.Value), traceStringInput("observation.fact.origin", string(fact.Origin)))
	}
	for _, fact := range profile.Facts {
		result = append(result, traceStringInput("profile.fact.key", fact.Key), traceFactValue("profile.fact.value", fact.Value), traceStringInput("profile.fact.provider", string(fact.Provenance.Provider)))
	}
	return result
}

func appendTraceInputs(left, right []contexttrace.Input) []contexttrace.Input {
	result := make([]contexttrace.Input, 0, len(left)+len(right))
	result = append(result, left...)
	result = append(result, right...)
	return result
}

func traceItemInputs(item contextapi.OfferItemIdentity) []contexttrace.Input {
	return []contexttrace.Input{
		traceStringInput("item.slot.provider", string(item.Slot.Provider)), traceStringInput("item.slot.key", string(item.Slot.Key)),
		traceStringInput("item.source.provider", string(item.Source.Provider)), traceStringInput("item.source.id", string(item.Source.ID)),
		traceStringInput("item.source.revision", string(item.SourceRevision.Revision)), traceStringInput("item.content.id", string(item.Content.ID)),
		traceUintInput("item.content.bytes", item.Content.UTF8Bytes), traceStringInput("item.mode", string(item.Mode)),
	}
}

func traceContentIdentityInputs(identity contextapi.ContentIdentity) []contexttrace.Input {
	return []contexttrace.Input{traceStringInput("handoff.content.id", string(identity.ID)), traceUintInput("handoff.content.bytes", identity.UTF8Bytes)}
}

func traceStringInput(key, value string) contexttrace.Input {
	return contexttrace.Input{Code: stableCode(key), Key: key, Value: contexttrace.StringValue(value)}
}
func traceUintInput(key string, value uint64) contexttrace.Input {
	return contexttrace.Input{Code: stableCode(key), Key: key, Value: contexttrace.Uint64Value(value)}
}

func traceFactValue(key string, value contextapi.FactValue) contexttrace.Input {
	switch value.Kind {
	case contextapi.FactNumber:
		return contexttrace.Input{Code: stableCode(key), Key: key, Value: contexttrace.Int64Value(value.Number)}
	case contextapi.FactBoolean:
		return contexttrace.Input{Code: stableCode(key), Key: key, Value: contexttrace.BoolValue(value.Boolean)}
	default:
		return contexttrace.Input{Code: stableCode(key), Key: key, Value: contexttrace.StringValue(value.Text)}
	}
}

func traceReasons(reasons []contextapi.Reason) []contexttrace.Reason {
	if len(reasons) > maxReasonCount {
		reasons = reasons[:maxReasonCount]
	}
	result := make([]contexttrace.Reason, 0, len(reasons))
	for _, reason := range reasons {
		// contexttrace stores a compact numeric code, so retain the original
		// reversible vocabulary and provenance in its bounded Rule field. A
		// hash alone would make old explanations collision-prone and opaque.
		item := contexttrace.Reason{Code: stableCode(string(reason.Code)), Provider: string(reason.Provider), Rule: reason.Rule}
		item.Params = append(item.Params,
			contexttrace.Parameter{Key: "_reason.code", Value: contexttrace.StringValue(string(reason.Code))},
			contexttrace.Parameter{Key: "_reason.origin", Value: contexttrace.StringValue(string(reason.Origin))},
			contexttrace.Parameter{Key: "_reason.summary", Value: contexttrace.StringValue(reason.Summary)},
			contexttrace.Parameter{Key: "_reason.at.unixNano", Value: contexttrace.Int64Value(traceReasonUnixNano(reason.At))},
		)
		for _, evidence := range reason.Evidence {
			item.Params = append(item.Params, contexttrace.Parameter{Key: "_reason.evidence", Value: contexttrace.Uint64Value(uint64(evidence))})
		}
		for _, parameter := range reason.Params {
			item.Params = append(item.Params, contexttrace.Parameter{Key: parameter.Key, Value: traceValue(parameter.Value)})
		}
		result = append(result, item)
	}
	return result
}

func traceReasonUnixNano(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixNano()
}

func traceValue(value contextapi.ReasonValue) contexttrace.Value {
	switch value.Kind {
	case contextapi.ReasonValueText:
		return contexttrace.StringValue(value.Text)
	case contextapi.ReasonValueNumber:
		return contexttrace.Int64Value(value.Number)
	case contextapi.ReasonValueDuration:
		return contexttrace.Int64Value(value.DurationNanos)
	case contextapi.ReasonValueBoolean:
		return contexttrace.BoolValue(value.Boolean)
	case contextapi.ReasonValueBytes:
		return contexttrace.Uint64Value(value.Bytes)
	default:
		return contexttrace.StringValue("invalid")
	}
}

func stableCode(value string) uint16 {
	hash := sha256.Sum256([]byte(value))
	return uint16(hash[0])<<8 | uint16(hash[1])
}
