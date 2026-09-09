package contextdaemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contexttrace"
)

const (
	rpcObserve     = "runtime.observe"
	rpcConfirm     = "runtime.confirm"
	rpcStatus      = "runtime.status"
	rpcQuery       = "runtime.query"
	rpcInspect     = "runtime.inspect"
	rpcClear       = "runtime.clear"
	rpcCacheStatus = "runtime.cacheStatus"
	rpcPing        = "runtime.ping"
)

// Ensure connects to the existing per-user daemon or starts one under the
// startup flock. After readiness, the process belongs to Serve; this client
// only owns its socket connection.
func Ensure(ctx context.Context, options ClientOptions) (*Client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil ensure context", ErrRuntimeInput)
	}
	paths, err := normalizePaths(options.Paths)
	if err != nil {
		return nil, err
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = defaultStartupTimeout
	}
	if options.DialTimeout <= 0 {
		options.DialTimeout = defaultDialTimeout
	}
	if options.MaxWireBytes <= 0 {
		options.MaxWireBytes = defaultWireBytes
	}
	if err := os.MkdirAll(paths.RuntimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create context runtime directory: %w", err)
	}
	if client, dialErr := dialClient(ctx, paths, options.DialTimeout, options.MaxWireBytes); dialErr == nil {
		return client, nil
	}
	startupCtx, cancel := context.WithTimeout(ctx, options.StartupTimeout)
	defer cancel()
	lock, err := acquireFlock(startupCtx, paths.LockPath)
	if err != nil {
		return nil, fmt.Errorf("acquire context startup lock: %w", err)
	}
	defer releaseFlock(lock)
	if client, dialErr := dialClient(startupCtx, paths, options.DialTimeout, options.MaxWireBytes); dialErr == nil {
		return client, nil
	}
	if options.Starter == nil {
		return nil, fmt.Errorf("%w: daemon is unavailable and no starter was supplied", ErrRuntimeInput)
	}
	process, err := options.Starter(startupCtx, paths)
	if err != nil {
		return nil, fmt.Errorf("start context daemon: %w", err)
	}
	if process == nil {
		return nil, fmt.Errorf("%w: starter returned no process", ErrRuntimeInput)
	}
	client, err := waitForSocket(startupCtx, paths, options.DialTimeout, options.MaxWireBytes)
	if err != nil {
		_ = process.Kill()
		_, _ = process.Wait()
		return nil, fmt.Errorf("wait for context daemon readiness: %w", err)
	}
	return client, nil
}

func dialClient(ctx context.Context, paths Paths, timeout time.Duration, maxWire int) (*Client, error) {
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", paths.SocketPath)
	if err != nil {
		return nil, err
	}
	return &Client{paths: paths, dial: timeout, maxWire: maxWire, conn: conn}, nil
}

// Connect opens one bounded connection to an already-running daemon. It does
// not create the runtime directory, acquire the startup lock, start a
// process, open the explanation cache, or renew any runtime residency. A
// status-only CLI path should use Connect for enabled contexts; an absent
// daemon is reported as the dial error and can be handled without Ensure.
func Connect(ctx context.Context, options ClientOptions) (*Client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil connect context", ErrRuntimeInput)
	}
	paths, err := normalizePaths(options.Paths)
	if err != nil {
		return nil, err
	}
	if options.DialTimeout <= 0 {
		options.DialTimeout = defaultDialTimeout
	}
	if options.MaxWireBytes <= 0 {
		options.MaxWireBytes = defaultWireBytes
	}
	return dialClient(ctx, paths, options.DialTimeout, options.MaxWireBytes)
}

func waitForSocket(ctx context.Context, paths Paths, timeout time.Duration, maxWire int) (*Client, error) {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	for {
		client, err := dialClient(ctx, paths, timeout, maxWire)
		if err == nil {
			if err = client.ping(ctx); err == nil {
				return client, nil
			}
			_ = client.Close()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			timer.Reset(10 * time.Millisecond)
		}
	}
}

func acquireFlock(ctx context.Context, path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func releaseFlock(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed {
		return nil
	}
	client.closed = true
	if client.conn == nil {
		return nil
	}
	err := client.conn.Close()
	client.conn = nil
	return err
}

func (client *Client) Observe(ctx context.Context, input ObserveInput) (ObserveResult, error) {
	var result ObserveResult
	err := client.call(ctx, rpcObserve, input, &result)
	return result, err
}

func (client *Client) Confirm(ctx context.Context, input ConfirmInput) contextapi.ConfirmationResult {
	var result contextapi.ConfirmationResult
	if err := client.call(ctx, rpcConfirm, input, &result); err != nil {
		return confirmationFailure(err, "confirm runtime offer")
	}
	return result
}

func (client *Client) Status(ctx context.Context, input StatusRequest) (Status, error) {
	var result Status
	err := client.call(ctx, rpcStatus, input, &result)
	return result, err
}

func (client *Client) Query(ctx context.Context, input QueryRequest) (contexttrace.QueryResult, error) {
	var result contexttrace.QueryResult
	err := client.call(ctx, rpcQuery, input, &result)
	return result, err
}

func (client *Client) Inspect(ctx context.Context, input InspectRequest) (contexttrace.QueryResult, error) {
	var result contexttrace.QueryResult
	err := client.call(ctx, rpcInspect, input, &result)
	return result, err
}

func (client *Client) Clear(ctx context.Context, workingDirectory string) (contexttrace.ClearResult, error) {
	var result contexttrace.ClearResult
	err := client.call(ctx, rpcClear, workingDirectory, &result)
	return result, err
}

func (client *Client) CacheStatus(ctx context.Context, workingDirectory string) (contexttrace.Stats, error) {
	var result contexttrace.Stats
	err := client.call(ctx, rpcCacheStatus, workingDirectory, &result)
	return result, err
}

func (client *Client) ping(ctx context.Context) error {
	var result bool
	return client.call(ctx, rpcPing, struct{}{}, &result)
}

func (client *Client) call(ctx context.Context, method string, params any, result any) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil client context", ErrRuntimeInput)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed || client.conn == nil {
		return ErrRuntimeClosed
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := contextapi.RequestID(time.Now().UnixNano())
	if id == 0 {
		id = 1
	}
	line, err := json.Marshal(contextapi.JSONRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: paramsJSON})
	if err != nil {
		return err
	}
	if len(line)+1 > client.maxWire {
		return fmt.Errorf("%w: request exceeds wire bound", ErrRuntimeBounds)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = client.conn.SetDeadline(deadline)
		defer client.conn.SetDeadline(time.Time{})
	}
	if _, err := client.conn.Write(append(line, '\n')); err != nil {
		return err
	}
	responseLine, err := readWireLine(bufio.NewReaderSize(client.conn, 4096), client.maxWire)
	if err != nil {
		return err
	}
	var response contextapi.JSONRPCResponse
	if err := json.Unmarshal(responseLine, &response); err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("runtime RPC %d: %s", response.Error.Code, response.Error.Message)
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return err
	}
	return nil
}

func readWireLine(reader *bufio.Reader, max int) ([]byte, error) {
	line := make([]byte, 0, 4096)
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > max {
			return nil, fmt.Errorf("%w: wire message exceeds bound", ErrRuntimeBounds)
		}
		line = append(line, part...)
		if err == nil {
			return line[:len(line)-1], nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func (runtime *Runtime) Serve(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil serve context", ErrRuntimeInput)
	}
	if err := runtime.Start(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(runtime.paths.RuntimeDir, 0o700); err != nil {
		return err
	}
	lock, err := acquireServerLock(runtime.paths.ServerLockPath)
	if err != nil {
		return err
	}
	defer releaseFlock(lock)
	if err := os.Remove(runtime.paths.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", runtime.paths.SocketPath)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close(); _ = os.Remove(runtime.paths.SocketPath) }()
	if err := os.Chmod(runtime.paths.SocketPath, 0o600); err != nil {
		return err
	}
	return runtime.ServeListener(ctx, listener)
}

func acquireServerLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func (runtime *Runtime) ServeListener(ctx context.Context, listener net.Listener) error {
	if ctx == nil || listener == nil {
		return fmt.Errorf("%w: listener and context are required", ErrRuntimeInput)
	}
	if ctx.Err() != nil {
		return nil
	}
	serveContext, serveCancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() {
		// Cancel before joining: serveConnection owns a cancellation watcher
		// that closes blocked connections, allowing every worker to finish.
		serveCancel()
		workers.Wait()
	}()
	if err := runtime.Start(serveContext); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	startedAt := time.Now().UTC()
	for {
		runtime.evictIdle(time.Now().UTC())
		if idle, ttl := runtime.profileAndIdle(time.Now().UTC()); idle && time.Since(startedAt) >= ttl {
			return nil
		}
		if deadline, ok := listener.(interface{ SetDeadline(time.Time) error }); ok {
			_ = deadline.SetDeadline(time.Now().Add(250 * time.Millisecond))
		}
		connection, err := listener.Accept()
		if err != nil {
			if serveContext.Err() != nil {
				return nil
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return err
		}
		select {
		case runtime.connections <- struct{}{}:
		case <-serveContext.Done():
			_ = connection.Close()
			return nil
		}
		if serveContext.Err() != nil {
			<-runtime.connections
			_ = connection.Close()
			return nil
		}
		workers.Add(1)
		go func(connection net.Conn) {
			defer workers.Done()
			defer func() { <-runtime.connections }()
			runtime.serveConnection(serveContext, connection)
		}(connection)
	}
}

func (runtime *Runtime) serveConnection(parent context.Context, connection net.Conn) {
	watchDone := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-parent.Done():
			_ = connection.Close()
		case <-watchDone:
		}
	}()
	defer func() {
		close(watchDone)
		<-watcherDone
		_ = connection.Close()
	}()
	reader := bufio.NewReaderSize(connection, 4096)
	for {
		if err := connection.SetReadDeadline(time.Now().Add(runtime.options.WholeHookDeadline)); err != nil {
			return
		}
		line, err := readWireLine(reader, defaultWireBytes)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = writeRPCError(connection, 0, -32000, err.Error(), defaultWireBytes)
			}
			return
		}
		var request contextapi.JSONRPCRequest
		if err := json.Unmarshal(line, &request); err != nil {
			_ = writeRPCError(connection, 0, -32700, err.Error(), defaultWireBytes)
			return
		}
		requestCtx, cancel := context.WithTimeout(parent, runtime.options.WholeHookDeadline)
		response := runtime.dispatch(requestCtx, request)
		cancel()
		if err := connection.SetWriteDeadline(time.Now().Add(runtime.options.WholeHookDeadline)); err != nil {
			return
		}
		encoded, err := json.Marshal(response)
		if err != nil || len(encoded)+1 > defaultWireBytes {
			_ = writeRPCError(connection, request.ID, -32000, "response exceeds wire bound", defaultWireBytes)
			return
		}
		if _, err := connection.Write(append(encoded, '\n')); err != nil {
			return
		}
	}
}

func (runtime *Runtime) dispatch(ctx context.Context, request contextapi.JSONRPCRequest) contextapi.JSONRPCResponse {
	response := contextapi.JSONRPCResponse{JSONRPC: "2.0", ID: request.ID}
	fail := func(code int, err error) contextapi.JSONRPCResponse {
		message := "runtime request failed"
		if err != nil {
			message = err.Error()
		}
		response.Error = &contextapi.JSONRPCError{Code: code, Message: message}
		return response
	}
	switch request.Method {
	case rpcPing:
		response.Result, _ = json.Marshal(true)
	case rpcObserve:
		var input ObserveInput
		if err := json.Unmarshal(request.Params, &input); err != nil {
			return fail(-32602, err)
		}
		result, err := runtime.Observe(ctx, input)
		if err != nil {
			return fail(-32001, err)
		}
		response.Result, _ = json.Marshal(result)
	case rpcConfirm:
		var input ConfirmInput
		if err := json.Unmarshal(request.Params, &input); err != nil {
			return fail(-32602, err)
		}
		response.Result, _ = json.Marshal(runtime.Confirm(ctx, input))
	case rpcStatus:
		var input StatusRequest
		if err := json.Unmarshal(request.Params, &input); err != nil {
			return fail(-32602, err)
		}
		result, err := runtime.Status(ctx, input)
		if err != nil {
			return fail(-32001, err)
		}
		response.Result, _ = json.Marshal(result)
	case rpcQuery:
		var input QueryRequest
		if err := json.Unmarshal(request.Params, &input); err != nil {
			return fail(-32602, err)
		}
		result, err := runtime.Query(ctx, input)
		if err != nil {
			return fail(-32001, err)
		}
		response.Result, _ = json.Marshal(result)
	case rpcInspect:
		var input InspectRequest
		if err := json.Unmarshal(request.Params, &input); err != nil {
			return fail(-32602, err)
		}
		result, err := runtime.Inspect(ctx, input)
		if err != nil {
			return fail(-32001, err)
		}
		response.Result, _ = json.Marshal(result)
	case rpcClear:
		var workingDirectory string
		if err := json.Unmarshal(request.Params, &workingDirectory); err != nil {
			return fail(-32602, err)
		}
		result, err := runtime.Clear(ctx, workingDirectory)
		if err != nil {
			return fail(-32001, err)
		}
		response.Result, _ = json.Marshal(result)
	case rpcCacheStatus:
		var workingDirectory string
		if err := json.Unmarshal(request.Params, &workingDirectory); err != nil {
			return fail(-32602, err)
		}
		result, err := runtime.CacheStatus(ctx, workingDirectory)
		if err != nil {
			return fail(-32001, err)
		}
		response.Result, _ = json.Marshal(result)
	default:
		return fail(-32601, fmt.Errorf("unknown runtime method %q", request.Method))
	}
	return response
}

func writeRPCError(writer io.Writer, id contextapi.RequestID, code int, message string, max int) error {
	response := contextapi.JSONRPCResponse{JSONRPC: "2.0", ID: id, Error: &contextapi.JSONRPCError{Code: code, Message: message}}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded)+1 > max {
		return err
	}
	_, err = writer.Write(append(encoded, '\n'))
	return err
}
