// Package contextprovider owns the two provider implementations used by the
// context runtime: a bounded JSON-RPC subprocess client and the builtin
// ai-context contributor.
//
// Client is deliberately one process and one serialized request stream. The
// runtime, rather than this package, owns client reuse, request admission, and
// eviction.
package contextprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

var (
	ErrInvalidProviderConfig = errors.New("contextprovider: invalid provider configuration")
	ErrProviderClosed        = errors.New("contextprovider: provider is closed")
	ErrRequestTooLarge       = errors.New("contextprovider: request exceeds its bound")
	ErrResponseTooLarge      = errors.New("contextprovider: response exceeds its bound")
	ErrUnsupportedCapability = errors.New("contextprovider: capability is not available")
	ErrProviderRejected      = errors.New("contextprovider: provider rejected initialization")
)

// ProtocolError identifies a malformed or mismatched JSON-RPC response. It
// intentionally does not include stderr: provider stderr may contain secrets
// and is only counted in Stats.
type ProtocolError struct {
	Operation string
	Message   string
}

func (e *ProtocolError) Error() string {
	if e.Operation == "" {
		return "contextprovider: protocol error: " + e.Message
	}
	return "contextprovider: " + e.Operation + ": protocol error: " + e.Message
}

// RemoteError is a JSON-RPC error returned by the provider.
type RemoteError struct {
	Operation string
	Code      int
	Message   string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("contextprovider: %s: provider error %d: %s", e.Operation, e.Code, e.Message)
}

// Options bounds transport buffers. A zero field uses a conservative local
// transport default; the runtime still supplies provider semantic limits in
// contextapi.ProviderLimits and each request.
type Options struct {
	MaxRequestBytes  uint64
	MaxStdoutBytes   uint64
	MaxStderrBytes   uint64
	MaxStderrRead    uint64
	MaxResponseBytes uint64
}

// Stats is a point-in-time transport snapshot. StderrBytes counts bytes
// drained from the child, while stderr is never retained.
type Stats struct {
	Requests        uint64
	Responses       uint64
	BytesSent       uint64
	StdoutBytes     uint64
	StderrBytes     uint64
	StderrTruncated bool
	ProcessExited   bool
	Closed          bool
}

const (
	defaultRequestBytes  = 1 << 20
	defaultResponseBytes = 1 << 20
	defaultStderrBytes   = 64 << 10
	defaultStderrRead    = 32 << 10
	maxTransportBytes    = 64 << 20
	shutdownDeadline     = 2 * time.Second
)

type clientState uint8

const (
	clientRunning clientState = iota + 1
	clientClosing
	clientClosed
)

// Client owns one executable, its process group, its stdio pipes, and the
// goroutine that reaps it. Profile and contribution calls are serialized over
// the child stream, but may be called safely from concurrent runtime goroutines.
type Client struct {
	config   contextapi.ProviderConfig
	resource contextapi.ProviderResource
	limits   Options

	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdoutRead    *os.File
	stderrRead    *os.File
	stdoutResults chan stdoutResult
	stdoutStop    chan struct{}
	stdoutDone    chan struct{}

	processCtx    context.Context
	processCancel context.CancelFunc
	stopOnce      sync.Once
	terminated    chan struct{}

	gate       chan struct{}
	closed     chan struct{}
	closeDone  chan struct{}
	operations sync.WaitGroup

	stateMu       sync.Mutex
	state         clientState
	nextID        contextapi.RequestID
	capabilities  []contextapi.ProviderCapability
	waitErr       error
	processExited bool
	stats         Stats

	stderrDone chan struct{}
	waitDone   chan struct{}
}

type stdoutResult struct {
	line []byte
	err  error
}

// Start starts config.Executable, initializes it, and returns an initialized
// client. The start context bounds initialization only; it does not become the
// lifetime context of the provider process.
func Start(
	ctx context.Context,
	config contextapi.ProviderConfig,
	resource contextapi.ProviderResource,
	options Options,
) (*Client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil start context", ErrInvalidProviderConfig)
	}
	if err := validateProviderConfig(config, resource); err != nil {
		return nil, err
	}
	resource = normalizeResource(config, resource)
	limits, err := normalizeOptions(config, options)
	if err != nil {
		return nil, err
	}

	command := exec.Command(config.Executable, config.Arguments...)
	command.Dir = resource.Scope.CanonicalRoot
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("contextprovider: open stdin: %w", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("contextprovider: open stdout: %w", err)
	}
	command.Stdout = stdoutWrite
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		return nil, fmt.Errorf("contextprovider: open stderr: %w", err)
	}
	command.Stderr = stderrWrite
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		_ = stderrRead.Close()
		_ = stderrWrite.Close()
		return nil, fmt.Errorf("contextprovider: start provider %q: %w", config.ID, err)
	}
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()

	processCtx, processCancel := context.WithCancel(context.Background())
	client := &Client{
		config:        cloneProviderConfig(config),
		resource:      resource,
		limits:        limits,
		cmd:           command,
		stdin:         stdin,
		stdoutRead:    stdoutRead,
		stderrRead:    stderrRead,
		stdoutResults: make(chan stdoutResult, 1),
		stdoutStop:    make(chan struct{}),
		stdoutDone:    make(chan struct{}),
		processCtx:    processCtx,
		processCancel: processCancel,
		terminated:    make(chan struct{}),
		gate:          make(chan struct{}, 1),
		closed:        make(chan struct{}),
		closeDone:     make(chan struct{}),
		stderrDone:    make(chan struct{}),
		waitDone:      make(chan struct{}),
		state:         clientRunning,
		nextID:        1,
	}
	client.gate <- struct{}{}
	go client.readStdout()
	go client.drainStderr(stderrRead)
	go client.waitForProcess()

	initialize := contextapi.ProviderInitializeRequest{
		ProtocolVersion:       1,
		Resource:              resource,
		Settings:              cloneRawMessage(config.Settings),
		SupportedCapabilities: cloneCapabilities(config.Capabilities),
		Limits:                config.Limits,
	}
	var initializeResponse contextapi.ProviderInitializeResponse
	initCtx, cancel := operationContext(ctx, limitsForDeadline(config.Limits, limits))
	err = client.roundTrip(initCtx, contextapi.RPCMethodInitialize, initialize, &initializeResponse)
	cancel()
	if err != nil {
		_ = client.Close(context.Background())
		return nil, fmt.Errorf("contextprovider: initialize provider %q: %w", config.ID, err)
	}
	if !initializeResponse.Accepted {
		_ = client.Close(context.Background())
		return nil, fmt.Errorf("contextprovider: initialize provider %q: %w", config.ID, ErrProviderRejected)
	}
	client.stateMu.Lock()
	client.capabilities = cloneCapabilities(initializeResponse.Capabilities)
	client.stateMu.Unlock()
	return client, nil
}

// Profile asks an initialized profile-capable provider for facts. The request
// scope and config digest are runtime-owned and must agree with Start's
// resource; a provider cannot change audience identity in its response.
func (c *Client) Profile(ctx context.Context, request contextapi.ProfileRequest) (contextapi.ProfileResponse, error) {
	if ctx == nil {
		return contextapi.ProfileResponse{}, errors.New("contextprovider: nil profile context")
	}
	if err := c.validateRequestScope(request.Scope, request.ConfigDigest); err != nil {
		return contextapi.ProfileResponse{}, err
	}
	if !c.hasCapability(contextapi.ProviderCapabilityProfile) {
		return contextapi.ProfileResponse{}, ErrUnsupportedCapability
	}
	request = normalizeProfileRequest(request, c.resource)
	wire := contextapi.ProviderProfileRequest{Resource: c.resource, Input: request}
	var output contextapi.ProviderProfileResponse
	requestCtx, cancel := operationContext(ctx, limitsForDeadline(c.config.Limits, c.limits))
	err := c.roundTrip(requestCtx, contextapi.RPCMethodProfile, wire, &output)
	cancel()
	if err != nil {
		return contextapi.ProfileResponse{}, err
	}
	return normalizeProfileResponse(output.Output, request, c.resource.Provider, minUint32NonZero(c.config.Limits.MaxFacts, request.Limits.MaxFacts))
}

// Contribute asks the provider for complete contribution bodies. Returned
// bodies are never reconstructed from trace samples; malformed individual
// items are rejected with a scoped reason while valid peer items remain.
func (c *Client) Contribute(ctx context.Context, request contextapi.ContributionRequest) (contextapi.ContributionResponse, error) {
	if ctx == nil {
		return contextapi.ContributionResponse{}, errors.New("contextprovider: nil contribute context")
	}
	if err := c.validateRequestScope(request.Scope, request.ConfigDigest); err != nil {
		return contextapi.ContributionResponse{}, err
	}
	if !c.hasCapability(contextapi.ProviderCapabilityContribute) {
		return contextapi.ContributionResponse{}, ErrUnsupportedCapability
	}
	request = normalizeContributionRequest(request, c.resource)
	wire := contextapi.ProviderContributeRequest{Resource: c.resource, Input: request}
	var output contextapi.ProviderContributeResponse
	requestCtx, cancel := operationContext(ctx, limitsForDeadline(c.config.Limits, c.limits))
	err := c.roundTrip(requestCtx, contextapi.RPCMethodContribute, wire, &output)
	cancel()
	if err != nil {
		return contextapi.ContributionResponse{}, err
	}
	response, err := normalizeContributionResponse(output.Output, request, c.resource, c.config.Limits)
	return response, err
}

// Close stops the provider, makes a best-effort shutdown RPC when no request
// is in flight, kills the complete process group, waits for cmd.Wait, and joins
// the stderr reader and every in-flight request. Cancellation requests the
// kill; it does not allow cleanup to be skipped.
func (c *Client) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.stateMu.Lock()
	if c.state == clientClosed {
		c.stateMu.Unlock()
		return nil
	}
	if c.state == clientClosing {
		done := c.closeDone
		c.stateMu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	c.state = clientClosing
	close(c.closed)
	c.stateMu.Unlock()

	var shutdownErr error
	select {
	case <-c.gate:
		select {
		case <-c.terminated:
			// A canceled request already terminated this stream.
		case <-c.waitDone:
			// The provider exited after its final response; shutdown is moot.
		default:
			shutdown := contextapi.ProviderShutdownRequest{RequestID: c.allocateID()}
			var shutdownResponse contextapi.ProviderShutdownResponse
			requestCtx, cancel := context.WithTimeout(ctx, shutdownDeadline)
			shutdownErr = c.roundTripLocked(requestCtx, contextapi.RPCMethodShutdown, shutdown, &shutdownResponse)
			cancel()
			if shutdownErr == nil && shutdownResponse.RequestID != shutdown.RequestID {
				c.terminate()
				shutdownErr = &ProtocolError{Operation: contextapi.RPCMethodShutdown, Message: "shutdown result id does not match request"}
			}
		}
		c.gate <- struct{}{}
	default:
		// A request owns the stream. Killing now unblocks both pipe directions;
		// Close then joins that request before returning.
	}
	c.terminate()
	c.operations.Wait()
	<-c.stdoutDone
	<-c.waitDone
	<-c.stderrDone
	_ = c.stdin.Close()

	c.stateMu.Lock()
	c.state = clientClosed
	processErr := c.waitErr
	c.stats.Closed = true
	c.stats.ProcessExited = c.processExited
	close(c.closeDone)
	c.stateMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A provider may have exited normally after delivering the final response;
	// in that case the best-effort shutdown write can only report a closed pipe.
	// Cleanup has already joined that process, so this is not a close failure.
	if shutdownErr != nil && !processErrExited(c) && !errors.Is(shutdownErr, io.EOF) && !errors.Is(shutdownErr, context.DeadlineExceeded) {
		return shutdownErr
	}
	if processErr != nil && !isKilledProcessError(processErr) {
		return fmt.Errorf("contextprovider: provider process: %w", processErr)
	}
	return nil
}

// Stats returns a copy of the current transport counters.
func (c *Client) Stats() Stats {
	c.stateMu.Lock()
	stats := c.stats
	stats.ProcessExited = c.processExited
	stats.Closed = c.state == clientClosed
	c.stateMu.Unlock()
	return stats
}

func (c *Client) roundTrip(ctx context.Context, method string, params any, result any) error {
	if err := c.beginOperation(ctx); err != nil {
		return err
	}
	defer c.operations.Done()
	select {
	case <-c.gate:
		defer func() { c.gate <- struct{}{} }()
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return ErrProviderClosed
	}
	return c.roundTripLocked(ctx, method, params, result)
}

func (c *Client) roundTripLocked(ctx context.Context, method string, params any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.allocateID()
	request := contextapi.JSONRPCRequest{JSONRPC: "2.0", ID: id, Method: method}
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("contextprovider: encode %s params: %w", method, err)
	}
	request.Params = encodedParams
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("contextprovider: encode %s request: %w", method, err)
	}
	if uint64(len(encoded)+1) > c.limits.MaxRequestBytes {
		return ErrRequestTooLarge
	}
	encoded = append(encoded, '\n')

	watchStop := make(chan struct{})
	watchDone := make(chan struct{})
	var complete atomic.Bool
	requestCompleted := false
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			if !complete.Load() {
				c.terminate()
			}
		case <-watchStop:
			if ctx.Err() != nil && !complete.Load() {
				c.terminate()
			}
		}
	}()
	defer func() {
		if !requestCompleted && ctx.Err() != nil {
			c.terminate()
		}
		complete.Store(true)
		close(watchStop)
		<-watchDone
	}()

	if err := writeAll(c.stdin, encoded); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("contextprovider: write %s request: %w", method, err)
	}
	c.stateMu.Lock()
	c.stats.Requests++
	c.stats.BytesSent += uint64(len(encoded))
	c.stateMu.Unlock()

	var line []byte
	select {
	case childResult, ok := <-c.stdoutResults:
		if !ok {
			return io.EOF
		}
		line, err = childResult.line, childResult.err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.stdoutDone:
		return io.EOF
	}
	if err != nil {
		if errors.Is(err, ErrResponseTooLarge) {
			c.terminate()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("contextprovider: read %s response: %w", method, err)
	}
	c.stateMu.Lock()
	c.stats.Responses++
	c.stats.StdoutBytes += uint64(len(line))
	c.stateMu.Unlock()
	var response contextapi.JSONRPCResponse
	if err := json.Unmarshal(line, &response); err != nil {
		c.terminate()
		return &ProtocolError{Operation: method, Message: "response is not one JSON object: " + err.Error()}
	}
	if response.JSONRPC != "2.0" {
		c.terminate()
		return &ProtocolError{Operation: method, Message: "response jsonrpc must be 2.0"}
	}
	if response.ID != id {
		c.terminate()
		return &ProtocolError{Operation: method, Message: fmt.Sprintf("response id %d does not match request id %d", response.ID, id)}
	}
	if response.Error != nil && response.Result != nil {
		c.terminate()
		return &ProtocolError{Operation: method, Message: "response contains both result and error"}
	}
	if response.Error != nil {
		return &RemoteError{Operation: method, Code: response.Error.Code, Message: response.Error.Message}
	}
	if response.Result == nil {
		c.terminate()
		return &ProtocolError{Operation: method, Message: "successful response has no result"}
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		c.terminate()
		return &ProtocolError{Operation: method, Message: "result does not match method schema: " + err.Error()}
	}
	if err := ctx.Err(); err != nil {
		c.terminate()
		return err
	}
	requestCompleted = true
	return nil
}

func processErrExited(c *Client) bool {
	c.stateMu.Lock()
	exited := c.processExited
	c.stateMu.Unlock()
	return exited
}

func (c *Client) beginOperation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.state != clientRunning {
		return ErrProviderClosed
	}
	c.operations.Add(1)
	return nil
}

func (c *Client) allocateID() contextapi.RequestID {
	c.stateMu.Lock()
	id := c.nextID
	c.nextID++
	c.stateMu.Unlock()
	return id
}

func (c *Client) hasCapability(capability contextapi.ProviderCapability) bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return containsCapability(c.capabilities, capability)
}

func (c *Client) validateRequestScope(scope contextapi.ScopeIdentity, digest contextapi.ConfigDigest) error {
	if !isZeroScope(scope) && !scopeIdentityEqual(scope, c.resource.Scope) {
		return fmt.Errorf("%w: request scope does not match provider resource", ErrInvalidProviderConfig)
	}
	if digest != "" && c.resource.ConfigDigest != "" && digest != c.resource.ConfigDigest {
		return fmt.Errorf("%w: request config digest does not match provider resource", ErrInvalidProviderConfig)
	}
	return nil
}

func (c *Client) terminate() {
	c.stopOnce.Do(func() {
		c.processCancel()
		close(c.stdoutStop)
		close(c.terminated)
		// Closing the parent read/write ends is required in addition to killing
		// the process group: blocked reads and a blocked request write must wake
		// even if a descendant inherited a descriptor or the kernel has not yet
		// delivered SIGKILL to the group leader.
		now := time.Now()
		_ = c.stdoutRead.SetReadDeadline(now)
		_ = c.stderrRead.SetReadDeadline(now)
		if deadlineWriter, ok := c.stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = deadlineWriter.SetWriteDeadline(now)
		}
		_ = c.stdin.Close()
		_ = c.stdoutRead.Close()
		_ = c.stderrRead.Close()
		if c.cmd.Process != nil {
			// Setpgid makes the negative pid address the complete provider group,
			// including grandchildren that inherited stdio.
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		}
	})
}

func (c *Client) waitForProcess() {
	err := c.cmd.Wait()
	c.stateMu.Lock()
	c.waitErr = err
	c.processExited = true
	c.stateMu.Unlock()
	close(c.waitDone)
}

func (c *Client) readStdout() {
	defer close(c.stdoutDone)
	defer c.stdoutRead.Close()
	reader := bufio.NewReaderSize(c.stdoutRead, boundedReaderSize(c.limits.MaxStdoutBytes))
	for {
		line, err := readBoundedLine(reader, c.limits.MaxStdoutBytes)
		if err != nil {
			select {
			case c.stdoutResults <- stdoutResult{err: err}:
			case <-c.stdoutStop:
			}
			return
		}
		select {
		case c.stdoutResults <- stdoutResult{line: line}:
		case <-c.stdoutStop:
			return
		}
	}
}

func (c *Client) drainStderr(stderr io.ReadCloser) {
	defer close(c.stderrDone)
	defer stderr.Close()
	readLimit := c.limits.MaxStderrRead
	if readLimit == 0 || readLimit > defaultStderrRead {
		readLimit = defaultStderrRead
	}
	buffer := make([]byte, readLimit)
	var total uint64
	truncated := false
	for {
		n, err := stderr.Read(buffer)
		if n > 0 {
			if ^uint64(0)-total < uint64(n) {
				total = ^uint64(0)
			} else {
				total += uint64(n)
			}
			if total > c.limits.MaxStderrBytes {
				truncated = true
			}
		}
		if err != nil {
			break
		}
	}
	c.stateMu.Lock()
	c.stats.StderrBytes = total
	c.stats.StderrTruncated = truncated
	c.stateMu.Unlock()
}

func validateProviderConfig(config contextapi.ProviderConfig, resource contextapi.ProviderResource) error {
	if config.ID == "" || config.Kind != contextapi.ProviderKindExecutable || config.Executable == "" {
		return fmt.Errorf("%w: executable provider requires id, kind executable, and executable", ErrInvalidProviderConfig)
	}
	if resource.Provider != "" && resource.Provider != config.ID {
		return fmt.Errorf("%w: provider resource %q conflicts with config %q", ErrInvalidProviderConfig, resource.Provider, config.ID)
	}
	if resource.Scope.ID == "" || resource.Scope.CanonicalRoot == "" {
		return fmt.Errorf("%w: provider resource requires scope id and canonical root", ErrInvalidProviderConfig)
	}
	return nil
}

func normalizeResource(config contextapi.ProviderConfig, resource contextapi.ProviderResource) contextapi.ProviderResource {
	if resource.Provider == "" {
		resource.Provider = config.ID
	}
	return resource
}

func normalizeOptions(config contextapi.ProviderConfig, options Options) (Options, error) {
	if options.MaxRequestBytes == 0 {
		options.MaxRequestBytes = defaultRequestBytes
	}
	if options.MaxStdoutBytes == 0 {
		options.MaxStdoutBytes = options.MaxResponseBytes
		if options.MaxStdoutBytes == 0 {
			options.MaxStdoutBytes = config.Limits.MaxResponseBytes
		}
		if options.MaxStdoutBytes == 0 {
			options.MaxStdoutBytes = defaultResponseBytes
		}
	}
	if options.MaxResponseBytes == 0 {
		options.MaxResponseBytes = options.MaxStdoutBytes
	}
	if options.MaxStderrBytes == 0 {
		options.MaxStderrBytes = defaultStderrBytes
	}
	if options.MaxStderrRead == 0 {
		options.MaxStderrRead = defaultStderrRead
	}
	if options.MaxRequestBytes > maxTransportBytes || options.MaxStdoutBytes > maxTransportBytes || options.MaxStderrBytes > maxTransportBytes {
		return Options{}, fmt.Errorf("%w: transport bound exceeds %d bytes", ErrInvalidProviderConfig, maxTransportBytes)
	}
	return options, nil
}

func limitsForDeadline(provider contextapi.ProviderLimits, options Options) time.Duration {
	if provider.DeadlineMs != 0 {
		return time.Duration(provider.DeadlineMs) * time.Millisecond
	}
	return 0
}

func operationContext(ctx context.Context, deadline time.Duration) (context.Context, context.CancelFunc) {
	if deadline <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, deadline)
}

func writeAll(writer io.Writer, bytes []byte) error {
	for len(bytes) > 0 {
		n, err := writer.Write(bytes)
		if n > 0 {
			bytes = bytes[n:]
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

func readBoundedLine(reader *bufio.Reader, maximum uint64) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			if uint64(len(line))+uint64(len(chunk)) > maximum {
				return nil, ErrResponseTooLarge
			}
			line = append(line, chunk...)
		}
		if err == nil {
			return line[:len(line)-1], nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
}

func boundedReaderSize(maximum uint64) int {
	const defaultSize = 32 << 10
	if maximum >= defaultSize {
		return defaultSize
	}
	return int(maximum) + 1
}

func containsCapability(capabilities []contextapi.ProviderCapability, wanted contextapi.ProviderCapability) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func cloneCapabilities(input []contextapi.ProviderCapability) []contextapi.ProviderCapability {
	output := append([]contextapi.ProviderCapability(nil), input...)
	sort.Slice(output, func(i, j int) bool { return output[i] < output[j] })
	return output
}

func cloneProviderConfig(input contextapi.ProviderConfig) contextapi.ProviderConfig {
	input.Arguments = append([]string(nil), input.Arguments...)
	input.Settings = cloneRawMessage(input.Settings)
	input.Capabilities = cloneCapabilities(input.Capabilities)
	return input
}

func cloneRawMessage(input json.RawMessage) json.RawMessage {
	if input == nil {
		return nil
	}
	return append(json.RawMessage(nil), input...)
}

func scopeIdentityEqual(a, b contextapi.ScopeIdentity) bool {
	return a.ID == b.ID && a.Authority == b.Authority && a.CanonicalRoot == b.CanonicalRoot && a.ConfigDigest == b.ConfigDigest
}

func isZeroScope(scope contextapi.ScopeIdentity) bool {
	return scope == (contextapi.ScopeIdentity{})
}

func isKilledProcessError(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}
