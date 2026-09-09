package evaluate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	contracts "github.com/phosphorco/workbench-go/pkl"
	"github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

const (
	contextLocalScheme      = "context-local"
	contextMaxFields        = 32
	contextMaxKeyBytes      = 256
	contextMaxDepth         = 8
	contextMaxProtocolBytes = 64 * 1024 * 1024
	contextMaxLockBytes     = 64 * 1024
	contextJoinWait         = 750 * time.Millisecond
	contextProtocolRevision = "workbench-context-owned-msgpack-v1"
	contextDefaultDataBytes = 128 * 1024 * 1024
)

// ContextOptions are immutable construction inputs for the bounded context
// evaluator. Construction performs no filesystem reads or writes.
type ContextOptions struct {
	WorkerExecutable    string
	WorkerArguments     []string
	RuntimeLockPath     string
	EvaluatorLeasePath  string
	MaxProcessDataBytes uint64
}

// ContextEvaluator is the process-owned context declaration capability. The
// existing Evaluator remains independent for plan and legacy callers.
type ContextEvaluator struct {
	pklExecutable       string
	workerExecutable    string
	workerArguments     []string
	runtimeLockPath     string
	evaluatorLeasePath  string
	maxProcessDataBytes uint64
}

// ContextWorkerSpec describes the narrow hidden worker entry. The worker sets
// its own child-only limit and replaces itself with the exact pinned Pkl.
type ContextWorkerSpec struct {
	PklExecutable       string
	MaxProcessDataBytes uint64
}

// RunContextWorker is the narrow hidden-dispatch entry. It applies the
// child-only data limit and replaces this process with the exact designated
// Pkl executable in server mode.
func RunContextWorker(spec ContextWorkerSpec) error {
	return runContextWorker(spec)
}

// NewContextEvaluator wraps an existing exact-path Evaluator without changing
// its unrelated plan/subject behavior. Runtime identity is loaded lazily by
// Identity after source discovery.
func NewContextEvaluator(runtimeEvaluator Evaluator, options ContextOptions) (ContextEvaluator, error) {
	if runtimeEvaluator.pklExecutable == "" || !filepath.IsAbs(runtimeEvaluator.pklExecutable) {
		return ContextEvaluator{}, fmt.Errorf("context evaluator requires an absolute Pkl executable")
	}
	for name, value := range map[string]string{
		"worker executable": options.WorkerExecutable,
		"runtime lock":      options.RuntimeLockPath,
		"evaluator lease":   options.EvaluatorLeasePath,
	} {
		if value == "" || !filepath.IsAbs(value) {
			return ContextEvaluator{}, fmt.Errorf("context %s must be an absolute path", name)
		}
	}
	dataLimit := options.MaxProcessDataBytes
	if dataLimit == 0 {
		dataLimit = contextDefaultDataBytes
	}
	return ContextEvaluator{
		pklExecutable:       filepath.Clean(runtimeEvaluator.pklExecutable),
		workerExecutable:    filepath.Clean(options.WorkerExecutable),
		workerArguments:     cloneStrings(options.WorkerArguments),
		runtimeLockPath:     filepath.Clean(options.RuntimeLockPath),
		evaluatorLeasePath:  filepath.Clean(options.EvaluatorLeasePath),
		maxProcessDataBytes: dataLimit,
	}, nil
}

// Identity returns the immutable evaluator identity from the bounded private
// runtime lock and embedded schema/protocol content. It performs no process
// start and does not hash the installed executable.
func (e ContextEvaluator) Identity(ctx context.Context) (contextapi.EvaluatorIdentity, error) {
	return loadContextIdentity(ctx, e.runtimeLockPath)
}

// Evaluate runs one declaration through the owned worker protocol. The entry
// bytes are validated against the current rooted file, but the exact bytes
// supplied by the caller remain the capture and the bytes sent as moduleText.
func (e ContextEvaluator) Evaluate(ctx context.Context, input contextapi.EvaluationInput) (contextapi.EvaluatedDeclaration, error) {
	if err := input.Validate(); err != nil {
		return contextapi.EvaluatedDeclaration{}, fmt.Errorf("invalid context evaluation input: %w", err)
	}
	input = contextapi.CloneEvaluationInput(input)
	bounded, cancel, err := contextDeadline(ctx, input.Bounds.DeadlineMs)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, err
	}
	defer cancel()
	root, err := os.OpenRoot(input.Origin.Root)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, fmt.Errorf("open context authority root: %w", err)
	}
	defer root.Close()
	entry, err := contextEntryCapture(root, input, input.SourceBytes)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, fmt.Errorf("capture context entry: %w", err)
	}
	identity, err := e.Identity(bounded)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, err
	}
	releaseLease, err := lockEvaluatorLease(bounded, e.evaluatorLeasePath)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, fmt.Errorf("acquire context evaluator lease: %w", err)
	}
	defer func() { _ = releaseLease() }()
	server, err := newContextServer(e, input.Bounds)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, err
	}
	encoded, imports, evaluatorID, evaluateErr := server.evaluate(bounded, input, root, entry)
	if evaluateErr == nil {
		closeRequest := contextCloseRequest{EvaluatorID: evaluatorID}
		if err := server.send(bounded, contextCloseCode, closeRequest, uint64(contextProtocolLimit(input.Bounds))); err != nil {
			evaluateErr = fmt.Errorf("send context evaluator close: %w", err)
		}
	}
	finishErr := server.finish(bounded, evaluateErr != nil || bounded.Err() != nil)
	if evaluateErr != nil {
		return contextapi.EvaluatedDeclaration{}, evaluateErr
	}
	if finishErr != nil {
		return contextapi.EvaluatedDeclaration{}, finishErr
	}
	currentIdentity, err := e.Identity(bounded)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, err
	}
	if currentIdentity != identity {
		return contextapi.EvaluatedDeclaration{}, fmt.Errorf("context evaluator identity changed during evaluation")
	}
	result, err := decodeContextDeclaration(input, encoded, imports, entry, identity)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, err
	}
	if result.Evaluator != identity {
		return contextapi.EvaluatedDeclaration{}, fmt.Errorf("context worker returned a different evaluator identity")
	}
	return result, nil
}

// Freshness revalidates the entry and every captured local import through the
// same confined reader boundary used by Evaluate. It performs no evaluation,
// cache access, or worker acquisition.
func (e ContextEvaluator) Freshness(ctx context.Context, input contextapi.EvaluationInput, result contextapi.EvaluatedDeclaration) error {
	if err := input.Validate(); err != nil {
		return fmt.Errorf("invalid context freshness input: %w", err)
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("invalid context freshness result: %w", err)
	}
	if input.Origin != result.Origin || input.SchemaURI != schemaURIForAuthority(result.Origin.Authority) {
		return fmt.Errorf("context freshness origin or schema changed")
	}
	entryDesignation, err := contextEntryDesignation(input)
	if err != nil {
		return err
	}
	entry, captures, err := validateContextManifestBounds(input, result.Dependencies.Captures, entryDesignation)
	if err != nil {
		return err
	}
	identity, err := e.Identity(ctx)
	if err != nil {
		return err
	}
	if identity != result.Evaluator {
		return fmt.Errorf("context evaluator identity changed")
	}
	root, err := os.OpenRoot(input.Origin.Root)
	if err != nil {
		return fmt.Errorf("open context authority root for freshness: %w", err)
	}
	defer root.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := contextEntryCapture(root, input, input.SourceBytes)
	if err != nil {
		return err
	}
	if !sameContextCapture(current, entry) {
		return fmt.Errorf("context entry capture is stale")
	}
	for _, capture := range captures {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := contextImportCapture(root, input.Origin.Root, capture.Designation, input.Bounds.MaxInputBytes)
		if err != nil {
			return err
		}
		if !sameContextCapture(current, capture) {
			return fmt.Errorf("context dependency %q is stale", capture.Designation)
		}
	}
	ordered := append([]contextapi.DependencyCapture(nil), captures...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Designation < ordered[right].Designation })
	manifest := append([]contextapi.DependencyCapture{entry}, ordered...)
	if contextRevision(input.SchemaURI, identity, manifest) != result.Revision {
		return fmt.Errorf("context declaration revision changed")
	}
	return nil
}

func validateContextManifestBounds(input contextapi.EvaluationInput, manifest []contextapi.DependencyCapture, entryDesignation string) (contextapi.DependencyCapture, []contextapi.DependencyCapture, error) {
	if len(manifest) == 0 || uint64(len(manifest)) > uint64(input.Bounds.MaxImports)+1 {
		return contextapi.DependencyCapture{}, nil, fmt.Errorf("context freshness manifest exceeds import bound")
	}
	if uint64(len(input.SourceBytes)) > input.Bounds.MaxInputBytes {
		return contextapi.DependencyCapture{}, nil, fmt.Errorf("context entry exceeds input bound")
	}
	seen := make(map[string]struct{}, len(manifest))
	var entry contextapi.DependencyCapture
	imports := make([]contextapi.DependencyCapture, 0, len(manifest)-1)
	used := uint64(len(input.SourceBytes))
	for _, capture := range manifest {
		if capture.ImportingOrigin.Known || capture.ImportingOrigin.Path != "" {
			return contextapi.DependencyCapture{}, nil, fmt.Errorf("context freshness has fabricated importing origin for %q", capture.Designation)
		}
		if _, duplicate := seen[capture.Designation]; duplicate {
			return contextapi.DependencyCapture{}, nil, fmt.Errorf("context freshness manifest repeats %q", capture.Designation)
		}
		seen[capture.Designation] = struct{}{}
		if capture.Digest != contextBytesDigest(capture.Bytes) {
			return contextapi.DependencyCapture{}, nil, fmt.Errorf("context freshness digest does not match %q", capture.Designation)
		}
		if capture.Designation == entryDesignation {
			if entry.Designation != "" || !bytes.Equal(capture.Bytes, input.SourceBytes) {
				return contextapi.DependencyCapture{}, nil, fmt.Errorf("context entry capture does not equal input source bytes")
			}
			entry = capture
			continue
		}
		if !strings.HasPrefix(capture.Designation, contextLocalScheme+":/") {
			return contextapi.DependencyCapture{}, nil, fmt.Errorf("context freshness has unsupported capture %q", capture.Designation)
		}
		if uint32(len(imports)) >= input.Bounds.MaxImports {
			return contextapi.DependencyCapture{}, nil, fmt.Errorf("context freshness imports exceed bound %d", input.Bounds.MaxImports)
		}
		if uint64(len(capture.Bytes)) > input.Bounds.MaxInputBytes-used {
			return contextapi.DependencyCapture{}, nil, fmt.Errorf("context dependency bytes exceed input bound")
		}
		used += uint64(len(capture.Bytes))
		imports = append(imports, capture)
	}
	if entry.Designation == "" {
		return contextapi.DependencyCapture{}, nil, fmt.Errorf("context freshness manifest omits entry capture")
	}
	return entry, imports, nil
}

func schemaURIForAuthority(authority contextapi.ScopeAuthority) string {
	if authority == contextapi.ScopeAuthorityHome {
		return contracts.ContextHomeURI
	}
	return contracts.ContextURI
}

func sameContextCapture(left, right contextapi.DependencyCapture) bool {
	return left.Designation == right.Designation && left.ResolvedCanonicalTarget == right.ResolvedCanonicalTarget && left.Digest == right.Digest && bytes.Equal(left.Bytes, right.Bytes)
}

type contextRuntimeLock struct {
	Runtimes map[string]struct {
		Version   string `json:"version"`
		Artifacts map[string]struct {
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
	} `json:"runtimes"`
}

func loadContextIdentity(ctx context.Context, filename string) (contextapi.EvaluatorIdentity, error) {
	if err := ctx.Err(); err != nil {
		return contextapi.EvaluatorIdentity{}, err
	}
	file, err := openContextMetadata(filename)
	if err != nil {
		return contextapi.EvaluatorIdentity{}, fmt.Errorf("open context runtime lock: %w", err)
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, contextMaxLockBytes+1))
	if err != nil {
		return contextapi.EvaluatorIdentity{}, fmt.Errorf("read context runtime lock: %w", err)
	}
	if len(encoded) > contextMaxLockBytes {
		return contextapi.EvaluatorIdentity{}, fmt.Errorf("context runtime lock exceeds %d bytes", contextMaxLockBytes)
	}
	if err := ctx.Err(); err != nil {
		return contextapi.EvaluatorIdentity{}, err
	}
	var lock contextRuntimeLock
	if err := json.Unmarshal(encoded, &lock); err != nil {
		return contextapi.EvaluatorIdentity{}, fmt.Errorf("decode context runtime lock: %w", err)
	}
	pklLock, ok := lock.Runtimes["pkl"]
	if !ok || pklLock.Version == "" {
		return contextapi.EvaluatorIdentity{}, fmt.Errorf("context runtime lock has no Pkl runtime")
	}
	artifact, ok := pklLock.Artifacts[contextPlatform()]
	if !ok || !isSHA256(artifact.SHA256) {
		return contextapi.EvaluatorIdentity{}, fmt.Errorf("context runtime lock has no valid Pkl artifact for %s", contextPlatform())
	}
	schemaDigest := contextSchemaDigest()
	digestInput := pklLock.Version + "\x00" + strings.ToLower(artifact.SHA256) + "\x00" + schemaDigest + "\x00" + contextProtocolRevision
	digest := sha256.Sum256([]byte(digestInput))
	return contextapi.EvaluatorIdentity{
		Name:    "pkl",
		Version: pklLock.Version,
		Digest:  contextapi.ContentID("sha256:" + hex.EncodeToString(digest[:])),
	}, nil
}

func contextPlatform() string {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "linux-x64"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "linux-arm64"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		return "macos-x64"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "macos-arm64"
	default:
		return runtime.GOOS + "-" + runtime.GOARCH
	}
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func contextSchemaDigest() string {
	hash := sha256.New()
	for _, part := range []string{
		contracts.ContextURI, contracts.Context, contracts.ContextHomeURI, contracts.ContextHome,
		contracts.ContextTypesURI, contracts.ContextTypes,
	} {
		_, _ = io.WriteString(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func cloneStrings(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.Clone(value)
	}
	return result
}

func contextDeadline(ctx context.Context, milliseconds uint64) (context.Context, context.CancelFunc, error) {
	if milliseconds == 0 || milliseconds > uint64((time.Duration(1<<63-1))/time.Millisecond) {
		return nil, nil, fmt.Errorf("context deadline is not representable")
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(milliseconds)*time.Millisecond)
	return bounded, cancel, nil
}

type contextInbound struct {
	RequestID   int64
	EvaluatorID int64
	URI         string
	Error       string
	Message     string
	Result      []byte
	HasResult   bool
}

type contextCreateRequest struct {
	RequestID        int64                 `msgpack:"requestId"`
	ModuleReaders    []contextModuleReader `msgpack:"clientModuleReaders,omitempty"`
	Env              map[string]string     `msgpack:"env,omitempty"`
	Properties       map[string]string     `msgpack:"properties,omitempty"`
	OutputFormat     string                `msgpack:"outputFormat,omitempty"`
	AllowedModules   []string              `msgpack:"allowedModules,omitempty"`
	AllowedResources []string              `msgpack:"allowedResources,omitempty"`
}

type contextModuleReader struct {
	Scheme              string `msgpack:"scheme"`
	HasHierarchicalURIs bool   `msgpack:"hasHierarchicalUris"`
	IsGlobbable         bool   `msgpack:"isGlobbable"`
	IsLocal             bool   `msgpack:"isLocal"`
}

type contextEvaluateRequest struct {
	RequestID   int64  `msgpack:"requestId"`
	EvaluatorID int64  `msgpack:"evaluatorId"`
	ModuleURI   string `msgpack:"moduleUri"`
	ModuleText  string `msgpack:"moduleText,omitempty"`
	Expression  string `msgpack:"expr,omitempty"`
}

type contextCloseRequest struct {
	EvaluatorID int64 `msgpack:"evaluatorId,omitempty"`
}

type contextModuleResponse struct {
	RequestID   int64  `msgpack:"requestId"`
	EvaluatorID int64  `msgpack:"evaluatorId"`
	Contents    string `msgpack:"contents,omitempty"`
	Error       string `msgpack:"error,omitempty"`
}

type contextStderr struct {
	bytes.Buffer
	budget     *contextDiagnosticBudget
	storeLimit uint64
	total      uint64
	truncated  bool
}

func (output *contextStderr) Write(value []byte) (int, error) {
	original := len(value)
	output.total += uint64(original)
	if output.budget != nil && !output.budget.add(uint64(original)) {
		output.truncated = true
	}
	if uint64(output.Len()) < output.storeLimit {
		remaining := output.storeLimit - uint64(output.Len())
		if uint64(len(value)) > remaining {
			value = value[:int(remaining)]
			output.truncated = true
		}
		_, _ = output.Buffer.Write(value)
	} else {
		output.truncated = true
	}
	return original, nil
}

func (output *contextStderr) ReadFrom(reader io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			written, _ := output.Write(buffer[:read])
			total += int64(written)
		}
		if err != nil {
			if err == io.EOF {
				return total, nil
			}
			return total, err
		}
	}
}

type contextDiagnosticBudget struct {
	limit uint64
	used  atomic.Uint64
	over  atomic.Bool
}

func (budget *contextDiagnosticBudget) add(amount uint64) bool {
	used := budget.used.Add(amount)
	if used > budget.limit {
		budget.over.Store(true)
		return false
	}
	return true
}

func (budget *contextDiagnosticBudget) exceeded() bool {
	return budget.over.Load()
}

type contextOwnedServer struct {
	command     *exec.Cmd
	stdin       *os.File
	stdout      *os.File
	wait        <-chan error
	writer      *contextProtocolWriter
	closeOnce   sync.Once
	stderr      *contextStderr
	stderrIn    *os.File
	stderrWait  <-chan error
	diagnostics *contextDiagnosticBudget
}

func newContextServer(e ContextEvaluator, bounds contextapi.EvaluationBounds) (*contextOwnedServer, error) {
	arguments := append(cloneStrings(e.workerArguments), "--pkl", e.pklExecutable, "--max-data-bytes", strconv.FormatUint(e.maxProcessDataBytes, 10), "server")
	command := exec.Command(e.workerExecutable, arguments...)
	configureContextCommand(command)
	childStdin, stdin, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create context evaluator stdin: %w", err)
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		return nil, fmt.Errorf("create context evaluator stdout: %w", err)
	}
	stderrIn, childStderr, err := os.Pipe()
	if err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		return nil, fmt.Errorf("create context evaluator stderr: %w", err)
	}
	command.Stdin = childStdin
	command.Stdout = childStdout
	diagnosticBudget := &contextDiagnosticBudget{limit: bounds.MaxDiagnosticsBytes}
	storeLimit := bounds.MaxDiagnosticsBytes
	if storeLimit > contextMaxLockBytes {
		storeLimit = contextMaxLockBytes
	}
	stderr := &contextStderr{budget: diagnosticBudget, storeLimit: storeLimit}
	command.Stderr = childStderr
	if err := command.Start(); err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		_ = childStderr.Close()
		_ = stderrIn.Close()
		return nil, fmt.Errorf("start context evaluator worker: %w", err)
	}
	_ = childStdin.Close()
	_ = childStdout.Close()
	_ = childStderr.Close()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	stderrWait := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(stderr, stderrIn)
		stderrWait <- copyErr
	}()
	return &contextOwnedServer{
		command:     command,
		stdin:       stdin,
		stdout:      stdout,
		wait:        wait,
		writer:      &contextProtocolWriter{writer: stdin},
		stderr:      stderr,
		stderrIn:    stderrIn,
		stderrWait:  stderrWait,
		diagnostics: diagnosticBudget,
	}, nil
}

func (server *contextOwnedServer) abort(force bool) {
	server.closeOnce.Do(func() {
		_ = server.stdin.Close()
		_ = server.stdout.Close()
	})
	if force {
		_ = killContextCommand(server.command)
	}
}

func (server *contextOwnedServer) finish(ctx context.Context, force bool) error {
	defer func() { _ = server.stderrIn.Close() }()
	server.abort(force)
	var waitErr error
	killed := force
	waitDuration := contextJoinDuration(ctx)
	waitTimer := time.NewTimer(waitDuration)
	select {
	case waitErr = <-server.wait:
	case <-waitTimer.C:
		_ = killContextCommand(server.command)
		killed = true
		// SIGKILL is sent only to this owned process group. Reap it before
		// returning so no child or Wait goroutine outlives the capability.
		waitErr = <-server.wait
	}
	if !waitTimer.Stop() {
		select {
		case <-waitTimer.C:
		default:
		}
	}
	var stderrErr error
	stderrTimer := time.NewTimer(contextJoinDuration(ctx))
	select {
	case stderrErr = <-server.stderrWait:
	case <-stderrTimer.C:
		_ = server.stderrIn.Close()
		stderrErr = <-server.stderrWait
	}
	if !stderrTimer.Stop() {
		select {
		case <-stderrTimer.C:
		default:
		}
	}
	server.writer.join()
	if stderrErr != nil {
		return fmt.Errorf("join context evaluator stderr: %w", stderrErr)
	}
	if server.diagnostics.exceeded() || server.stderr.total > server.diagnostics.limit {
		return fmt.Errorf("context diagnostics exceed %d bytes", server.diagnostics.limit)
	}
	if waitErr != nil && !killed {
		return fmt.Errorf("context evaluator exited unsuccessfully: %w", waitErr)
	}
	return nil
}

func contextJoinDuration(ctx context.Context) time.Duration {
	duration := contextJoinWait
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < duration {
			duration = remaining
		}
	}
	if duration <= 0 {
		return time.Nanosecond
	}
	return duration
}

func (server *contextOwnedServer) send(ctx context.Context, code int, body any, maxBytes uint64) error {
	return server.writer.send(ctx, code, body, maxBytes)
}

func (server *contextOwnedServer) evaluate(ctx context.Context, input contextapi.EvaluationInput, root *os.Root, entry contextapi.DependencyCapture) ([]byte, []contextapi.DependencyCapture, int64, error) {
	watchDone := make(chan struct{})
	var watchWait sync.WaitGroup
	watchWait.Add(1)
	go func() {
		defer watchWait.Done()
		select {
		case <-ctx.Done():
			server.abort(true)
		case <-watchDone:
		}
	}()
	defer func() {
		close(watchDone)
		watchWait.Wait()
	}()
	protocolLimit := contextProtocolLimit(input.Bounds)
	decoder := msgpack.NewDecoder(&contextBoundedReader{reader: server.stdout, remaining: protocolLimit})
	create := contextCreateRequest{
		RequestID:        1,
		ModuleReaders:    []contextModuleReader{{Scheme: "workbench", HasHierarchicalURIs: true, IsLocal: false}, {Scheme: contextLocalScheme, HasHierarchicalURIs: true, IsLocal: true}},
		Env:              map[string]string{},
		Properties:       map[string]string{},
		OutputFormat:     "json",
		AllowedModules:   []string{`^workbench:context$`, `^workbench:context-home$`, `^workbench:context-types$`, `^` + contextLocalScheme + `:/.*$`, `^pkl:[A-Za-z0-9]+$`},
		AllowedResources: []string{`^prop:pkl.outputFormat$`},
	}
	if err := server.send(ctx, contextNewEvaluatorCode, create, uint64(protocolLimit)); err != nil {
		return nil, nil, 0, fmt.Errorf("send context evaluator create: %w", err)
	}
	code, message, err := decodeContextMessage(decoder, input.Bounds)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("read context evaluator create: %w", err)
	}
	if !server.diagnostics.add(uint64(len(message.Error) + len(message.Message))) {
		return nil, nil, 0, fmt.Errorf("context diagnostics exceed %d bytes", server.diagnostics.limit)
	}
	if code != contextNewEvaluatorResponseCode || message.RequestID != create.RequestID || message.Error != "" {
		return nil, nil, 0, fmt.Errorf("context evaluator create failed: %s", message.Error)
	}
	if message.EvaluatorID == 0 {
		return nil, nil, 0, fmt.Errorf("context evaluator create omitted evaluator id")
	}
	evaluatorID := message.EvaluatorID
	moduleURI, err := contextEntryDesignation(input)
	if err != nil {
		return nil, nil, 0, err
	}
	evaluate := contextEvaluateRequest{RequestID: 2, EvaluatorID: message.EvaluatorID, ModuleURI: moduleURI, ModuleText: string(input.SourceBytes), Expression: "output.text"}
	if err := server.send(ctx, contextEvaluateCode, evaluate, uint64(protocolLimit)); err != nil {
		return nil, nil, 0, fmt.Errorf("send context evaluator request: %w", err)
	}
	imports := make(map[string]contextapi.DependencyCapture)
	usedInputBytes := uint64(len(input.SourceBytes))
	for {
		code, message, err = decodeContextMessage(decoder, input.Bounds)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("read context evaluator response: %w", err)
		}
		if !server.diagnostics.add(uint64(len(message.Error) + len(message.Message))) {
			return nil, nil, 0, fmt.Errorf("context diagnostics exceed %d bytes", server.diagnostics.limit)
		}
		switch code {
		case contextEvaluateLogCode:
			if message.EvaluatorID != evaluatorID {
				return nil, nil, 0, fmt.Errorf("context log belongs to evaluator %d, want %d", message.EvaluatorID, evaluatorID)
			}
			continue
		case contextEvaluateReadModuleCode:
			if message.RequestID == 0 || message.EvaluatorID != evaluatorID {
				return nil, nil, 0, fmt.Errorf("context module read has invalid request or evaluator id")
			}
			if message.URI == entry.Designation {
				response := contextModuleResponse{RequestID: message.RequestID, EvaluatorID: message.EvaluatorID, Contents: string(entry.Bytes)}
				if err := server.send(ctx, contextEvaluateReadModuleResponseCode, response, uint64(protocolLimit)); err != nil {
					return nil, nil, 0, fmt.Errorf("send repeated context entry response: %w", err)
				}
				continue
			}
			if previous, ok := imports[message.URI]; ok {
				response := contextModuleResponse{RequestID: message.RequestID, EvaluatorID: message.EvaluatorID, Contents: string(previous.Bytes)}
				if err := server.send(ctx, contextEvaluateReadModuleResponseCode, response, uint64(protocolLimit)); err != nil {
					return nil, nil, 0, fmt.Errorf("send repeated context module response: %w", err)
				}
				continue
			}
			if !isEmbeddedContextModule(message.URI) {
				if uint32(len(imports)) >= input.Bounds.MaxImports {
					return nil, nil, 0, fmt.Errorf("context imports exceed bound %d", input.Bounds.MaxImports)
				}
			}
			response := contextModuleResponse{RequestID: message.RequestID, EvaluatorID: message.EvaluatorID}
			if embedded, ok := embeddedContextModule(message.URI); ok {
				response.Contents = embedded
			} else {
				capture, readErr := contextImportCapture(root, input.Origin.Root, message.URI, input.Bounds.MaxInputBytes)
				if readErr != nil {
					return nil, nil, 0, fmt.Errorf("read context module %q: %w", message.URI, readErr)
				} else {
					if uint64(len(capture.Bytes)) > input.Bounds.MaxInputBytes-usedInputBytes {
						return nil, nil, 0, fmt.Errorf("context dependency bytes exceed input bound %d", input.Bounds.MaxInputBytes)
					}
					usedInputBytes += uint64(len(capture.Bytes))
					imports[message.URI] = capture
					response.Contents = string(capture.Bytes)
				}
			}
			if err := server.send(ctx, contextEvaluateReadModuleResponseCode, response, uint64(protocolLimit)); err != nil {
				return nil, nil, 0, fmt.Errorf("send context module response: %w", err)
			}
		case contextEvaluateResponseCode:
			if message.RequestID != evaluate.RequestID || message.EvaluatorID != evaluatorID {
				return nil, nil, 0, fmt.Errorf("context evaluation response has invalid request or evaluator id")
			}
			if message.Error != "" {
				return nil, nil, 0, fmt.Errorf("Pkl context evaluation: %s", message.Error)
			}
			if !message.HasResult {
				return nil, nil, 0, fmt.Errorf("Pkl context evaluation omitted result")
			}
			ordered := make([]contextapi.DependencyCapture, 0, len(imports))
			for _, capture := range imports {
				ordered = append(ordered, capture)
			}
			sort.Slice(ordered, func(left, right int) bool { return ordered[left].Designation < ordered[right].Designation })
			return message.Result, ordered, evaluatorID, nil
		default:
			return nil, nil, 0, fmt.Errorf("unsupported context evaluator response code 0x%x", code)
		}
	}
}

func contextProtocolLimit(bounds contextapi.EvaluationBounds) int64 {
	limit := uint64(0)
	overflow := false
	add := func(value uint64) {
		if limit > ^uint64(0)-value {
			overflow = true
			return
		}
		limit += value
	}
	add(bounds.MaxOutputBytes)
	add(bounds.MaxDiagnosticsBytes)
	add(bounds.MaxInputBytes)
	if bounds.MaxInputBytes != 0 && uint64(bounds.MaxImports) > ^uint64(0)/bounds.MaxInputBytes {
		overflow = true
	} else {
		add(uint64(bounds.MaxImports) * bounds.MaxInputBytes)
	}
	if overflow || limit > contextMaxProtocolBytes {
		limit = contextMaxProtocolBytes
	}
	if limit < 1024*1024 {
		limit = 1024 * 1024
	}
	return int64(limit)
}

func decodeContextDeclaration(input contextapi.EvaluationInput, encoded []byte, imports []contextapi.DependencyCapture, entry contextapi.DependencyCapture, identity contextapi.EvaluatorIdentity) (contextapi.EvaluatedDeclaration, error) {
	jsonResult, err := contextResultJSON(encoded, input.Bounds.MaxOutputBytes)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, err
	}
	captures := make([]contextapi.DependencyCapture, 0, len(imports)+1)
	captures = append(captures, entry)
	captures = append(captures, imports...)
	var project contextapi.ProjectDeclaration
	var home contextapi.HomeDeclaration
	kind := contextapi.DeclarationKindProject
	if input.Origin.Authority == contextapi.ScopeAuthorityHome {
		kind = contextapi.DeclarationKindHome
		home, err = contextapi.DecodeHomeDeclaration(jsonResult)
	} else {
		project, err = contextapi.DecodeProjectDeclaration(jsonResult)
	}
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, fmt.Errorf("decode evaluated context declaration: %w", err)
	}
	result := contextapi.EvaluatedDeclaration{
		Origin:       input.Origin,
		Kind:         kind,
		Project:      project,
		Home:         home,
		Dependencies: contextapi.DependencyManifest{Captures: captures},
		Revision:     contextRevision(input.SchemaURI, identity, captures),
		Evaluator:    identity,
	}
	if err := result.Validate(); err != nil {
		return contextapi.EvaluatedDeclaration{}, fmt.Errorf("validate evaluated context declaration: %w", err)
	}
	return result, nil
}

func contextResultJSON(encoded []byte, maxOutputBytes uint64) ([]byte, error) {
	if len(encoded) == 0 || uint64(len(encoded)) > maxOutputBytes || len(encoded) > contextMaxProtocolBytes {
		return nil, fmt.Errorf("context evaluator result exceeds protocol bound")
	}
	rendered, err := decodeBoundedContextString(encoded, maxOutputBytes)
	if err != nil {
		return nil, fmt.Errorf("decode context output.text scalar: %w", err)
	}
	if rendered == "" {
		return nil, fmt.Errorf("context evaluator returned empty JSON text")
	}
	if uint64(len(rendered)) > maxOutputBytes {
		return nil, fmt.Errorf("context evaluator JSON text exceeds output bound")
	}
	return []byte(rendered), nil
}

func decodeBoundedContextString(encoded []byte, limit uint64) (string, error) {
	if len(encoded) == 0 {
		return "", fmt.Errorf("empty MessagePack value")
	}
	code := encoded[0]
	var length uint64
	header := 1
	switch {
	case msgpcode.IsFixedString(code):
		length = uint64(code & msgpcode.FixedStrMask)
	case code == msgpcode.Str8:
		if len(encoded) < 2 {
			return "", fmt.Errorf("truncated str8 length")
		}
		header = 2
		length = uint64(encoded[1])
	case code == msgpcode.Str16:
		if len(encoded) < 3 {
			return "", fmt.Errorf("truncated str16 length")
		}
		header = 3
		length = uint64(binary.BigEndian.Uint16(encoded[1:3]))
	case code == msgpcode.Str32:
		if len(encoded) < 5 {
			return "", fmt.Errorf("truncated str32 length")
		}
		header = 5
		length = uint64(binary.BigEndian.Uint32(encoded[1:5]))
	default:
		return "", fmt.Errorf("output.text result is not a MessagePack string")
	}
	if length > limit {
		return "", fmt.Errorf("output.text string length %d exceeds %d", length, limit)
	}
	if length > uint64(len(encoded)-header) {
		return "", fmt.Errorf("output.text string is truncated")
	}
	if length != uint64(len(encoded)-header) {
		return "", fmt.Errorf("output.text scalar has trailing bytes")
	}
	return string(encoded[header:]), nil
}

func contextRevision(schema string, identity contextapi.EvaluatorIdentity, captures []contextapi.DependencyCapture) contextapi.ConfigDigest {
	hash := sha256.New()
	write := func(value string) {
		_, _ = io.WriteString(hash, value)
		_, _ = hash.Write([]byte{0})
	}
	write(contextProtocolRevision)
	write(schema)
	write(identity.Name)
	write(identity.Version)
	write(string(identity.Digest))
	for _, capture := range captures {
		write(capture.Designation)
		write(capture.ResolvedCanonicalTarget)
		write(string(capture.Digest))
	}
	return contextapi.ConfigDigest("sha256:" + hex.EncodeToString(hash.Sum(nil)))
}

func contextEntryCapture(root *os.Root, input contextapi.EvaluationInput, expected []byte) (contextapi.DependencyCapture, error) {
	designation, err := contextEntryDesignation(input)
	if err != nil {
		return contextapi.DependencyCapture{}, err
	}
	relative, err := filepath.Rel(filepath.Clean(input.Origin.Root), filepath.Clean(input.Origin.Path))
	if err != nil || !filepath.IsLocal(relative) {
		return contextapi.DependencyCapture{}, fmt.Errorf("context entry is outside authority root")
	}
	target, contents, err := readContextRooted(root, input.Origin.Root, relative, input.Bounds.MaxInputBytes)
	if err != nil {
		return contextapi.DependencyCapture{}, err
	}
	if !bytes.Equal(contents, expected) {
		return contextapi.DependencyCapture{}, fmt.Errorf("entry bytes changed since discovery")
	}
	return contextCapture(designation, target, expected), nil
}

func contextEntryDesignation(input contextapi.EvaluationInput) (string, error) {
	relative, err := filepath.Rel(filepath.Clean(input.Origin.Root), filepath.Clean(input.Origin.Path))
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return "", fmt.Errorf("context entry is outside authority root")
	}
	return (&url.URL{Scheme: contextLocalScheme, Path: "/" + filepath.ToSlash(relative)}).String(), nil
}

func contextCapture(designation, target string, contents []byte) contextapi.DependencyCapture {
	return contextapi.DependencyCapture{
		Designation:             strings.Clone(designation),
		ImportingOrigin:         contextapi.ModuleOrigin{},
		ResolvedCanonicalTarget: strings.Clone(target),
		Bytes:                   append([]byte(nil), contents...),
		Digest:                  contextBytesDigest(contents),
	}
}

func contextBytesDigest(contents []byte) contextapi.ContentID {
	digest := sha256.Sum256(contents)
	return contextapi.ContentID("sha256:" + hex.EncodeToString(digest[:]))
}

func contextImportCapture(root *os.Root, rootPath, designation string, maxBytes uint64) (contextapi.DependencyCapture, error) {
	parsed, err := url.Parse(designation)
	if err != nil || parsed.Scheme != contextLocalScheme || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return contextapi.DependencyCapture{}, fmt.Errorf("module %q is outside the confined local reader", designation)
	}
	relative := filepath.FromSlash(strings.TrimPrefix(parsed.Path, "/"))
	if relative == "." || !filepath.IsLocal(relative) || !strings.HasSuffix(relative, ".pkl") {
		return contextapi.DependencyCapture{}, fmt.Errorf("module %q is not a local Pkl file", designation)
	}
	target, contents, err := readContextRooted(root, rootPath, relative, maxBytes)
	if err != nil {
		return contextapi.DependencyCapture{}, fmt.Errorf("read module %q: %w", designation, err)
	}
	return contextCapture(designation, target, contents), nil
}

func contextCanonicalTarget(root, path string) (string, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !contextPathUnderRoot(canonicalRoot, target) {
		return "", fmt.Errorf("target %q escapes authority root %q", target, canonicalRoot)
	}
	return target, nil
}

func contextPathUnderRoot(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func readContextRooted(root *os.Root, rootPath, relative string, maxBytes uint64) (string, []byte, error) {
	return readContextRootedWithHook(root, rootPath, relative, maxBytes, nil)
}

// readContextRootedWithHook reads a confined module twice. The first read is
// the capture returned to the evaluator; the second reopens the current path
// and verifies both object identity and exact bytes. The hook is test-only
// plumbing for deterministic replacement witnesses and is nil in production.
func readContextRootedWithHook(root *os.Root, rootPath, relative string, maxBytes uint64, afterFirst func() error) (string, []byte, error) {
	if maxBytes > uint64(^uint64(0)>>1)-1 {
		return "", nil, fmt.Errorf("module bound is too large")
	}
	canonicalRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return "", nil, err
	}
	candidate := filepath.Join(rootPath, relative)
	before, err := contextCanonicalTarget(rootPath, candidate)
	if err != nil {
		return "", nil, err
	}
	canonicalRelative, err := filepath.Rel(canonicalRoot, before)
	if err != nil || !filepath.IsLocal(canonicalRelative) {
		return "", nil, fmt.Errorf("resolved module target is outside authority root")
	}
	contents, firstInfo, err := readContextRootedPass(root, canonicalRelative, maxBytes)
	if err != nil {
		return "", nil, err
	}
	if afterFirst != nil {
		if err := afterFirst(); err != nil {
			return "", nil, err
		}
	}
	after, err := contextCanonicalTarget(rootPath, candidate)
	if err != nil {
		return "", nil, err
	}
	if before != after {
		return "", nil, fmt.Errorf("module target changed during read")
	}
	afterRelative, err := filepath.Rel(canonicalRoot, after)
	if err != nil || !filepath.IsLocal(afterRelative) {
		return "", nil, fmt.Errorf("resolved module target is outside authority root")
	}
	check, secondInfo, err := readContextRootedPass(root, afterRelative, maxBytes)
	if err != nil {
		return "", nil, err
	}
	final, err := contextCanonicalTarget(rootPath, candidate)
	if err != nil {
		return "", nil, err
	}
	if after != final || !os.SameFile(firstInfo, secondInfo) {
		return "", nil, fmt.Errorf("module changed during read")
	}
	if !bytes.Equal(contents, check) {
		return "", nil, fmt.Errorf("module bytes changed during read")
	}
	return before, contents, nil
}

func readContextRootedPass(root *os.Root, relative string, maxBytes uint64) ([]byte, os.FileInfo, error) {
	file, err := contextOpenRooted(root, relative)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || uint64(info.Size()) > maxBytes {
		return nil, nil, fmt.Errorf("module is not a bounded regular file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, nil, err
	}
	if uint64(len(contents)) > maxBytes {
		return nil, nil, fmt.Errorf("module exceeds %d bytes", maxBytes)
	}
	afterInfo, err := file.Stat()
	if err != nil || !afterInfo.Mode().IsRegular() || afterInfo.Size() != info.Size() || !os.SameFile(info, afterInfo) {
		return nil, nil, fmt.Errorf("module changed during read")
	}
	return contents, info, nil
}

func embeddedContextModule(designation string) (string, bool) {
	switch designation {
	case contracts.ContextURI:
		return contracts.Context, true
	case contracts.ContextHomeURI:
		return contracts.ContextHome, true
	case contracts.ContextTypesURI:
		return contracts.ContextTypes, true
	default:
		return "", false
	}
}

func isEmbeddedContextModule(designation string) bool {
	_, ok := embeddedContextModule(designation)
	return ok
}

type contextBoundedReader struct {
	reader    io.Reader
	remaining int64
}

func (reader *contextBoundedReader) Read(value []byte) (int, error) {
	if reader.remaining <= 0 {
		return 0, fmt.Errorf("context protocol byte bound exceeded")
	}
	if int64(len(value)) > reader.remaining {
		value = value[:reader.remaining]
	}
	read, err := reader.reader.Read(value)
	reader.remaining -= int64(read)
	return read, err
}

type contextProtocolWriter struct {
	writer io.Writer
	mutex  sync.Mutex
	wait   sync.WaitGroup
}

func (writer *contextProtocolWriter) send(ctx context.Context, code int, body any, maxBytes uint64) error {
	var buffer bytes.Buffer
	encoder := msgpack.NewEncoder(&buffer)
	if err := encoder.EncodeArrayLen(2); err != nil {
		return err
	}
	if err := encoder.EncodeInt(int64(code)); err != nil {
		return err
	}
	if err := encoder.Encode(body); err != nil {
		return err
	}
	if uint64(buffer.Len()) > maxBytes {
		return fmt.Errorf("context protocol request exceeds %d bytes", maxBytes)
	}
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	done := make(chan error, 1)
	writer.wait.Add(1)
	go func() {
		defer writer.wait.Done()
		_, err := writer.writer.Write(buffer.Bytes())
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (writer *contextProtocolWriter) join() {
	writer.wait.Wait()
}

func decodeContextMessage(decoder *msgpack.Decoder, bounds contextapi.EvaluationBounds) (int, contextInbound, error) {
	var message contextInbound
	count, err := decoder.DecodeArrayLen()
	if err != nil {
		return 0, message, err
	}
	if count != 2 {
		return 0, message, fmt.Errorf("context protocol envelope has %d fields", count)
	}
	code, err := decoder.DecodeInt()
	if err != nil {
		return 0, message, err
	}
	count, err = decoder.DecodeMapLen()
	if err != nil {
		return 0, message, err
	}
	if count < 0 || count > contextMaxFields {
		return 0, message, fmt.Errorf("context protocol field count %d exceeds %d", count, contextMaxFields)
	}
	seen := make(map[string]struct{}, count)
	for index := 0; index < count; index++ {
		key, err := decodeContextString(decoder, contextMaxKeyBytes)
		if err != nil {
			return 0, message, err
		}
		if _, duplicate := seen[key]; duplicate {
			return 0, message, fmt.Errorf("context protocol field %q repeated", key)
		}
		seen[key] = struct{}{}
		switch key {
		case "requestId":
			message.RequestID, err = decoder.DecodeInt64()
		case "evaluatorId":
			message.EvaluatorID, err = decoder.DecodeInt64()
		case "uri":
			message.URI, err = decodeContextString(decoder, contextMaxKeyBytes)
		case "error":
			message.Error, err = decodeContextString(decoder, contextStringLimit(bounds.MaxDiagnosticsBytes))
		case "message":
			message.Message, err = decodeContextString(decoder, contextStringLimit(bounds.MaxDiagnosticsBytes))
		case "result":
			message.Result, err = decodeContextBytes(decoder, bounds.MaxOutputBytes)
			message.HasResult = err == nil
		default:
			err = discardContextValue(decoder, 0, bounds.MaxInputBytes)
		}
		if err != nil {
			return 0, message, fmt.Errorf("decode context protocol field %q: %w", key, err)
		}
	}
	return int(code), message, nil
}

func decodeContextString(decoder *msgpack.Decoder, limit int) (string, error) {
	length, err := decoder.DecodeBytesLen()
	if err != nil {
		return "", err
	}
	if length < 0 || length > limit {
		return "", fmt.Errorf("context protocol string length %d exceeds %d", length, limit)
	}
	value := make([]byte, length)
	if err := decoder.ReadFull(value); err != nil {
		return "", err
	}
	return string(value), nil
}

func contextStringLimit(limit uint64) int {
	maxInt := uint64(^uint(0) >> 1)
	if limit > maxInt {
		return int(maxInt)
	}
	return int(limit)
}

func decodeContextBytes(decoder *msgpack.Decoder, limit uint64) ([]byte, error) {
	length, err := decoder.DecodeBytesLen()
	if err != nil {
		return nil, err
	}
	if length < 0 || uint64(length) > limit {
		return nil, fmt.Errorf("context protocol bytes length %d exceeds %d", length, limit)
	}
	value := make([]byte, length)
	if err := decoder.ReadFull(value); err != nil {
		return nil, err
	}
	return value, nil
}

func discardContextValue(decoder *msgpack.Decoder, depth int, maxBytes uint64) error {
	if depth > contextMaxDepth {
		return fmt.Errorf("context protocol nesting exceeds %d", contextMaxDepth)
	}
	code, err := decoder.PeekCode()
	if err != nil {
		return err
	}
	if msgpcode.IsFixedNum(code) || code == msgpcode.Nil || code == msgpcode.False || code == msgpcode.True {
		return decoder.Skip()
	}
	if msgpcode.IsString(code) || msgpcode.IsBin(code) {
		length, err := decoder.DecodeBytesLen()
		if err != nil {
			return err
		}
		if length < 0 || uint64(length) > maxBytes {
			return fmt.Errorf("context protocol value length %d exceeds %d", length, maxBytes)
		}
		buffer := make([]byte, 4096)
		for length > 0 {
			chunk := length
			if chunk > len(buffer) {
				chunk = len(buffer)
			}
			if err := decoder.ReadFull(buffer[:chunk]); err != nil {
				return err
			}
			length -= chunk
		}
		return nil
	}
	if msgpcode.IsFixedArray(code) || code == msgpcode.Array16 || code == msgpcode.Array32 {
		count, err := decoder.DecodeArrayLen()
		if err != nil {
			return err
		}
		if count < 0 || count > contextMaxFields {
			return fmt.Errorf("context protocol array count %d exceeds %d", count, contextMaxFields)
		}
		for index := 0; index < count; index++ {
			if err := discardContextValue(decoder, depth+1, maxBytes); err != nil {
				return err
			}
		}
		return nil
	}
	if msgpcode.IsFixedMap(code) || code == msgpcode.Map16 || code == msgpcode.Map32 {
		count, err := decoder.DecodeMapLen()
		if err != nil {
			return err
		}
		if count < 0 || count > contextMaxFields {
			return fmt.Errorf("context protocol map count %d exceeds %d", count, contextMaxFields)
		}
		for index := 0; index < count*2; index++ {
			if err := discardContextValue(decoder, depth+1, maxBytes); err != nil {
				return err
			}
		}
		return nil
	}
	return decoder.Skip()
}

const (
	contextNewEvaluatorCode               = 0x20
	contextNewEvaluatorResponseCode       = 0x21
	contextCloseCode                      = 0x22
	contextEvaluateCode                   = 0x23
	contextEvaluateResponseCode           = 0x24
	contextEvaluateLogCode                = 0x25
	contextEvaluateReadModuleCode         = 0x28
	contextEvaluateReadModuleResponseCode = 0x29
)
