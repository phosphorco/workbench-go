package contextdaemon

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var errTransportTestListener = errors.New("transport test listener failed")

type transportTestListener struct {
	first      net.Conn
	afterFirst func() (net.Conn, error)
	accepted   chan struct{}
}

func (listener *transportTestListener) Accept() (net.Conn, error) {
	if listener.first != nil {
		connection := listener.first
		listener.first = nil
		close(listener.accepted)
		return connection, nil
	}
	return listener.afterFirst()
}

func (*transportTestListener) Close() error { return nil }

func (*transportTestListener) Addr() net.Addr {
	return transportTestAddr("transport-test")
}

func (*transportTestListener) SetDeadline(time.Time) error { return nil }

type transportTestAddr string

func (address transportTestAddr) Network() string { return "transport-test" }
func (address transportTestAddr) String() string  { return string(address) }

func newTransportTestRuntime(t *testing.T, idleTTLMs int) *Runtime {
	t.Helper()
	root := t.TempDir()
	paths := DefaultPaths(filepath.Join(root, "runtime"))
	if idleTTLMs > 0 {
		paths.HomeConfigPath = filepath.Join(root, "home.json")
		config := []byte(`{"schemaVersion":1,"runtime":{"idleTTLMs":` + strconv.Itoa(idleTTLMs) + `}}`)
		if err := os.WriteFile(paths.HomeConfigPath, config, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := NewRuntime(RuntimeOptions{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func waitForTransportTestConnection(t *testing.T, connection net.Conn) {
	t.Helper()
	if _, err := connection.Write([]byte(`{"jsonrpc":"2.0",`)); err != nil {
		t.Fatal(err)
	}
}

func assertTransportTestConnectionClosed(t *testing.T, connection net.Conn) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		// net.Pipe may reject a deadline after its endpoint has already been
		// closed; that is itself proof that the peer was closed.
		return
	}
	_, err := connection.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("connection remained open after ServeListener returned")
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("connection was not closed before ServeListener returned: %v", err)
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		// net.Pipe reports a plain error after its peer is closed on some Go
		// versions; any non-timeout read failure still proves closure.
		return
	}
}

func TestServeListenerIdleReturnClosesAndJoinsPartialConnections(t *testing.T) {
	runtime := newTransportTestRuntime(t, 100)
	serverOne, clientOne := net.Pipe()
	serverTwo, clientTwo := net.Pipe()
	defer clientOne.Close()
	defer clientTwo.Close()
	allowSecond := make(chan struct{})
	listener := &transportTestListener{
		first:    serverOne,
		accepted: make(chan struct{}),
		afterFirst: func() (net.Conn, error) {
			<-allowSecond
			time.Sleep(150 * time.Millisecond)
			return serverTwo, nil
		},
	}
	serveContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.ServeListener(serveContext, listener) }()
	select {
	case <-listener.accepted:
	case <-time.After(time.Second):
		t.Fatal("ServeListener did not accept the partial connection")
	}
	waitForTransportTestConnection(t, clientOne)
	close(allowSecond)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeListener did not return after idle lifetime")
	}
	assertTransportTestConnectionClosed(t, clientOne)
	assertTransportTestConnectionClosed(t, clientTwo)
}

func TestServeListenerFatalReturnClosesAndJoinsPartialConnection(t *testing.T) {
	runtime := newTransportTestRuntime(t, 0)
	server, client := net.Pipe()
	defer client.Close()
	allowFatal := make(chan struct{})
	listener := &transportTestListener{
		first:    server,
		accepted: make(chan struct{}),
		afterFirst: func() (net.Conn, error) {
			<-allowFatal
			return nil, errTransportTestListener
		},
	}
	serveContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.ServeListener(serveContext, listener) }()
	select {
	case <-listener.accepted:
	case <-time.After(time.Second):
		t.Fatal("ServeListener did not accept the partial connection")
	}
	waitForTransportTestConnection(t, client)
	close(allowFatal)
	select {
	case err := <-done:
		if !errors.Is(err, errTransportTestListener) {
			t.Fatalf("ServeListener error = %v, want fatal listener error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeListener did not join the partial connection after listener failure")
	}
	assertTransportTestConnectionClosed(t, client)
}
