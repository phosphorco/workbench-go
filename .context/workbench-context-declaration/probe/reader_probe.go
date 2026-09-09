package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/apple/pkl-go/pkl"
	"github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

const (
	maxProtocolBytes    = 1024 * 1024
	maxProtocolFields   = 32
	maxProtocolKeyBytes = 256
	maxInputBytes       = 4 * 1024 * 1024
	maxStderrBytes      = 64 * 1024
)

type capture struct {
	Designation      string `json:"designation"`
	ImportingOrigin  string `json:"importing_origin"`
	ResolvedTarget   string `json:"resolved_canonical_target"`
	ExactBytesSHA256 string `json:"exact_bytes_sha256"`
	ExactBytesLength int    `json:"exact_bytes_length"`
}

type probeResult struct {
	Mode                   string    `json:"mode"`
	OK                     bool      `json:"ok"`
	ElapsedMS              float64   `json:"elapsed_ms"`
	FirstElapsedMS         float64   `json:"first_elapsed_ms,omitempty"`
	SecondElapsedMS        float64   `json:"second_elapsed_ms,omitempty"`
	NewEvaluatorElapsedMS  float64   `json:"new_evaluator_elapsed_ms,omitempty"`
	ManagerCloseElapsedMS  float64   `json:"manager_close_elapsed_ms,omitempty"`
	ContextTimeoutMS       int       `json:"context_timeout_ms,omitempty"`
	Value                  any       `json:"value,omitempty"`
	OutputBytes            int       `json:"output_bytes,omitempty"`
	DiagnosticBytes        int       `json:"diagnostic_bytes,omitempty"`
	InputBytes             int       `json:"input_bytes,omitempty"`
	Error                  string    `json:"error,omitempty"`
	CloseError             string    `json:"close_error,omitempty"`
	ReaderCalls            int       `json:"reader_calls,omitempty"`
	Captures               []capture `json:"captures,omitempty"`
	EntrySourceCapture     *capture  `json:"entry_source_capture,omitempty"`
	ChildPIDsBeforeClose   []int     `json:"child_pids_before_close,omitempty"`
	ChildPIDsAfterClose    []int     `json:"child_pids_after_close,omitempty"`
	ChildPeakTreeRSSKB     int64     `json:"child_peak_tree_rss_kb,omitempty"`
	ReaderOriginLimitation string    `json:"reader_origin_limitation,omitempty"`
	Joined                 bool      `json:"joined,omitempty"`
	ProtocolBytesLimit     int       `json:"protocol_bytes_limit,omitempty"`
	ProtocolRejected       bool      `json:"protocol_rejected,omitempty"`
	StderrBytes            int       `json:"stderr_bytes,omitempty"`
	StderrTruncated        bool      `json:"stderr_truncated,omitempty"`
	Freshness              any       `json:"freshness,omitempty"`
}

type exactReader struct {
	mu       sync.Mutex
	modules  map[string]string
	targets  map[string]string
	captures []capture
}

func (r *exactReader) Scheme() string                                  { return "probe" }
func (r *exactReader) IsGlobbable() bool                               { return false }
func (r *exactReader) HasHierarchicalUris() bool                       { return true }
func (r *exactReader) IsLocal() bool                                   { return true }
func (r *exactReader) ListElements(url.URL) ([]pkl.PathElement, error) { return nil, nil }

func (r *exactReader) Read(request url.URL) (string, error) {
	key := request.String()
	r.mu.Lock()
	contents, ok := r.modules[key]
	target := r.targets[key]
	if ok {
		digest := sha256.Sum256([]byte(contents))
		r.captures = append(r.captures, capture{
			Designation: key,
			// pkl.ModuleReader receives only the requested URI. It does not
			// provide the importing module URI, so this field is intentionally
			// empty rather than guessed.
			ImportingOrigin:  "",
			ResolvedTarget:   target,
			ExactBytesSHA256: hex.EncodeToString(digest[:]),
			ExactBytesLength: len(contents),
		})
	}
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("probe module %q not found", key)
	}
	return contents, nil
}

func (r *exactReader) snapshot() []capture {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]capture(nil), r.captures...)
}

func makeCapture(designation, target, contents string) capture {
	digest := sha256.Sum256([]byte(contents))
	return capture{
		Designation:      designation,
		ResolvedTarget:   target,
		ExactBytesSHA256: hex.EncodeToString(digest[:]),
		ExactBytesLength: len(contents),
	}
}

func moduleOptions(reader pkl.ModuleReader) func(*pkl.EvaluatorOptions) {
	return func(options *pkl.EvaluatorOptions) {
		options.AllowedModules = []string{`^probe:/.*$`, `^repl:.*$`, `^pkl:[A-Za-z0-9]+$`}
		options.AllowedResources = []string{`^prop:pkl.outputFormat$`}
		options.Env = map[string]string{}
		options.Properties = map[string]string{}
		options.OutputFormat = "json"
		if reader != nil {
			options.ModuleReaders = []pkl.ModuleReader{reader}
		}
	}
}

func directSource(uri, contents string) *pkl.ModuleSource {
	parsed, err := url.Parse(uri)
	if err != nil {
		panic(err)
	}
	return &pkl.ModuleSource{Uri: parsed, Contents: contents}
}

func processParents() map[int]int {
	parents := make(map[int]int)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return parents
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		closeParen := strings.LastIndexByte(string(data), ')')
		if closeParen < 0 {
			continue
		}
		fields := strings.Fields(string(data)[closeParen+1:])
		if len(fields) < 2 {
			continue
		}
		parent, err := strconv.Atoi(fields[1])
		if err == nil {
			parents[pid] = parent
		}
	}
	return parents
}

func processTreePIDs() []int {
	parents := processParents()
	root := os.Getpid()
	var result []int
	for pid := range parents {
		for parent := parents[pid]; parent != 0; parent = parents[parent] {
			if parent == root {
				result = append(result, pid)
				break
			}
		}
	}
	return result
}

func residentKB(pid int) int64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				value, _ := strconv.ParseInt(fields[1], 10, 64)
				return value
			}
		}
	}
	return 0
}

func monitorChildren(stop <-chan struct{}, done chan<- struct{}, peak *int64) {
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			var treeRSS int64
			for _, pid := range processTreePIDs() {
				treeRSS += residentKB(pid)
			}
			if treeRSS > atomic.LoadInt64(peak) {
				atomic.StoreInt64(peak, treeRSS)
			}
		}
	}
}

func newManager(pklPath string) pkl.EvaluatorManager {
	return pkl.NewEvaluatorManagerWithCommand([]string{pklPath})
}

func runReader(pklPath string, warm bool) probeResult {
	started := time.Now()
	result := probeResult{Mode: "reader", ContextTimeoutMS: 3000}
	reader := &exactReader{
		modules: map[string]string{
			"probe:/policy.pkl": "module probe.Policy\nenabled = false\nvalue = \"captured-reader-value\"\n",
		},
		targets: map[string]string{"probe:/policy.pkl": "/fixture/home/workbench-context.pkl"},
	}
	manager := newManager(pklPath)
	stop := make(chan struct{})
	done := make(chan struct{})
	var peak int64
	go monitorChildren(stop, done, &peak)
	defer func() {
		close(stop)
		<-done
		result.ChildPeakTreeRSSKB = atomic.LoadInt64(&peak)
	}()
	defer func() {
		if err := manager.Close(); err != nil && result.CloseError == "" {
			result.CloseError = err.Error()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	newStarted := time.Now()
	evaluator, err := manager.NewEvaluator(ctx, moduleOptions(reader))
	result.FirstElapsedMS = time.Since(newStarted).Seconds() * 1000
	if err != nil {
		result.Error = err.Error()
		result.ElapsedMS = time.Since(started).Seconds() * 1000
		return result
	}
	if evaluator == nil {
		result.Error = "NewEvaluator returned nil evaluator"
		result.ElapsedMS = time.Since(started).Seconds() * 1000
		return result
	}
	defer evaluator.Close()
	source := directSource("probe:/main.pkl", "module probe.Main\nimport \"probe:/policy.pkl\"\n")
	entry := makeCapture(source.Uri.String(), "/fixture/project/workbench-context.pkl", source.Contents)
	result.EntrySourceCapture = &entry
	var first bool
	firstStart := time.Now()
	err = evaluator.EvaluateExpression(ctx, source, "policy.enabled", &first)
	result.FirstElapsedMS = time.Since(firstStart).Seconds() * 1000
	if err != nil {
		result.Error = err.Error()
		result.ElapsedMS = time.Since(started).Seconds() * 1000
		return result
	}
	result.Value = first
	if warm {
		var second bool
		secondStart := time.Now()
		err = evaluator.EvaluateExpression(ctx, source, "policy.enabled", &second)
		result.SecondElapsedMS = time.Since(secondStart).Seconds() * 1000
		if err != nil {
			result.Error = err.Error()
			result.ElapsedMS = time.Since(started).Seconds() * 1000
			return result
		}
		if second != first {
			result.Error = "warm evaluation changed the captured value"
			result.ElapsedMS = time.Since(started).Seconds() * 1000
			return result
		}
	}
	result.ReaderCalls = len(reader.snapshot())
	result.Captures = append([]capture{entry}, reader.snapshot()...)
	result.ReaderOriginLimitation = "pkl-go ModuleReader.Read receives designation only; importing origin is not exposed by the installed API"
	result.OK = result.ReaderCalls == 1
	if !result.OK {
		result.Error = fmt.Sprintf("expected exactly one captured repeat read, got %d", result.ReaderCalls)
	}
	result.ElapsedMS = time.Since(started).Seconds() * 1000
	return result
}

func runCold(pklPath string) probeResult {
	result := runReader(pklPath, false)
	result.Mode = "home-only-cold"
	return result
}

func runLifecycle(pklPath string, timeoutMS int) probeResult {
	started := time.Now()
	result := probeResult{Mode: "lifecycle", ContextTimeoutMS: timeoutMS}
	manager := newManager(pklPath)
	stop := make(chan struct{})
	done := make(chan struct{})
	var peak int64
	go monitorChildren(stop, done, &peak)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	newStarted := time.Now()
	evaluator, err := manager.NewEvaluator(ctx, moduleOptions(nil))
	result.NewEvaluatorElapsedMS = time.Since(newStarted).Seconds() * 1000
	cancel()
	if evaluator != nil {
		_ = evaluator.Close()
	}
	result.ChildPIDsBeforeClose = processTreePIDs()
	closeStarted := time.Now()
	closeErr := manager.Close()
	result.ManagerCloseElapsedMS = time.Since(closeStarted).Seconds() * 1000
	close(stop)
	<-done
	result.ChildPIDsAfterClose = processTreePIDs()
	result.ChildPeakTreeRSSKB = atomic.LoadInt64(&peak)
	if err != nil {
		result.Error = err.Error()
	}
	if closeErr != nil {
		result.CloseError = closeErr.Error()
	}
	result.OK = true
	result.ElapsedMS = time.Since(started).Seconds() * 1000
	return result
}

func runOwnedCancellation(pklPath string, timeoutMS int) probeResult {
	started := time.Now()
	result := probeResult{Mode: "owned-process-cancellation", ContextTimeoutMS: timeoutMS}
	fixture, err := os.CreateTemp("", "workbench-owned-probe-*.pkl")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	fixtureName := fixture.Name()
	defer os.Remove(fixtureName)
	if _, err := fixture.WriteString("module probe.Owned\nvalue = \"owned-process\"\n"); err != nil {
		result.Error = err.Error()
		_ = fixture.Close()
		return result
	}
	if err := fixture.Close(); err != nil {
		result.Error = err.Error()
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	command := exec.Command(pklPath, "eval", "--no-project", "--no-cache", "--format", "json", "--expression", "value", fixtureName)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		result.Error = err.Error()
		return result
	}
	result.ChildPIDsBeforeClose = processTreePIDs()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		result.Error = errorPreview(errString(err, stderr.String()))
		result.Joined = true
	case <-ctx.Done():
		killErr := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		waitErr := <-wait
		result.Joined = true
		result.Error = errorPreview(errString(killErr, errString(waitErr, stderr.String())))
	}
	result.ChildPIDsAfterClose = processTreePIDs()
	result.OK = result.Joined && len(result.ChildPIDsAfterClose) == 0
	result.ElapsedMS = time.Since(started).Seconds() * 1000
	return result
}

func errString(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

type rootedCapture struct {
	Target string
	Bytes  []byte
	Digest [32]byte
}

func pathUnderRoot(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func captureRooted(root, path string) (rootedCapture, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return rootedCapture{}, err
	}
	canonicalTarget, err := filepath.EvalSymlinks(path)
	if err != nil {
		return rootedCapture{}, err
	}
	if !pathUnderRoot(canonicalRoot, canonicalTarget) {
		return rootedCapture{}, fmt.Errorf("resolved target %q escapes authority root %q", canonicalTarget, canonicalRoot)
	}
	info, err := os.Stat(canonicalTarget)
	if err != nil {
		return rootedCapture{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxInputBytes {
		return rootedCapture{}, fmt.Errorf("target %q is not a bounded regular file", canonicalTarget)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return rootedCapture{}, err
	}
	canonicalAfter, err := filepath.EvalSymlinks(path)
	if err != nil {
		return rootedCapture{}, err
	}
	if canonicalAfter != canonicalTarget {
		return rootedCapture{}, fmt.Errorf("target changed during capture from %q to %q", canonicalTarget, canonicalAfter)
	}
	digest := sha256.Sum256(contents)
	return rootedCapture{Target: canonicalTarget, Bytes: contents, Digest: digest}, nil
}

func revalidateRooted(root, path string, captured rootedCapture) (bool, error) {
	current, err := captureRooted(root, path)
	if err != nil {
		return false, err
	}
	return current.Target == captured.Target && bytes.Equal(current.Bytes, captured.Bytes), nil
}

func runFileFreshness() probeResult {
	started := time.Now()
	result := probeResult{Mode: "file-freshness"}
	root, err := os.MkdirTemp("", "workbench-reader-freshness-")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer os.RemoveAll(root)
	authority := filepath.Join(root, "authority")
	if err := os.Mkdir(authority, 0o755); err != nil {
		result.Error = err.Error()
		return result
	}
	targetA := filepath.Join(authority, "a.pkl")
	targetB := filepath.Join(authority, "b.pkl")
	link := filepath.Join(authority, "module.pkl")
	contentsA := []byte("module probe.A\nvalue = \"A\"\n")
	contentsB := []byte("module probe.B\nvalue = \"B\"\n")
	if err := os.WriteFile(targetA, contentsA, 0o644); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := os.WriteFile(targetB, contentsB, 0o644); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := os.Symlink("a.pkl", link); err != nil {
		result.Error = err.Error()
		return result
	}
	retarget := func(target string) error {
		if err := os.Remove(link); err != nil {
			return err
		}
		return os.Symlink(target, link)
	}

	unchanged, err := captureRooted(authority, link)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	unchangedAccepted, err := revalidateRooted(authority, link, unchanged)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	if err := retarget("b.pkl"); err != nil {
		result.Error = err.Error()
		return result
	}
	retargeted, err := captureRooted(authority, link)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	retargetAccepted, err := revalidateRooted(authority, link, unchanged)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	if err := retarget("a.pkl"); err != nil {
		result.Error = err.Error()
		return result
	}
	abaBefore, err := filepath.EvalSymlinks(link)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if err := retarget("b.pkl"); err != nil {
		result.Error = err.Error()
		return result
	}
	abaCapture, err := captureRooted(authority, link)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if err := retarget("a.pkl"); err != nil {
		result.Error = err.Error()
		return result
	}
	abaAfter, err := filepath.EvalSymlinks(link)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	abaAccepted, err := revalidateRooted(authority, link, abaCapture)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	naiveEndpointMiss := abaBefore == abaAfter && !bytes.Equal(abaCapture.Bytes, contentsA)
	result.Freshness = map[string]any{
		"authority_root":        authority,
		"regular_target_checks": len(unchanged.Bytes) <= maxInputBytes && len(retargeted.Bytes) <= maxInputBytes && len(abaCapture.Bytes) <= maxInputBytes,
		"unchanged_control": map[string]any{
			"captured_target":       unchanged.Target,
			"captured_bytes_sha256": hex.EncodeToString(unchanged.Digest[:]),
			"revalidated":           unchangedAccepted,
			"accepted":              unchangedAccepted,
		},
		"retarget": map[string]any{
			"expected_target":                unchanged.Target,
			"captured_target_after_retarget": retargeted.Target,
			"captured_bytes_sha256":          hex.EncodeToString(retargeted.Digest[:]),
			"revalidated":                    retargetAccepted,
			"rejected":                       !retargetAccepted,
		},
		"aba": map[string]any{
			"before_target":               abaBefore,
			"after_target":                abaAfter,
			"captured_target_during_aba":  abaCapture.Target,
			"captured_bytes_sha256":       hex.EncodeToString(abaCapture.Digest[:]),
			"endpoints_equal":             abaBefore == abaAfter,
			"naive_endpoint_check_missed": naiveEndpointMiss,
			"revalidated":                 abaAccepted,
			"rejected":                    !abaAccepted,
		},
	}
	result.OK = unchangedAccepted && !retargetAccepted && !abaAccepted && naiveEndpointMiss
	if !result.OK {
		result.Error = "rooted capture/revalidation did not accept the stable control and reject retarget/ABA captures"
	}
	result.ElapsedMS = time.Since(started).Seconds() * 1000
	return result
}

func runOversizedFixed(pklPath string, kind string, size int) probeResult {
	started := time.Now()
	result := probeResult{Mode: "oversized-" + kind, InputBytes: size + 64, ContextTimeoutMS: 5000}
	manager := newManager(pklPath)
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	evaluator, err := manager.NewEvaluator(ctx, moduleOptions(nil))
	if err != nil {
		result.Error = err.Error()
		result.ElapsedMS = time.Since(started).Seconds() * 1000
		return result
	}
	defer evaluator.Close()
	text := strings.Repeat("x", size)
	sourceText := "module probe.Large\nvalue = \"" + text + "\"\n"
	if kind == "diagnostic" {
		sourceText = "module probe.Large\nvalue = throw(\"" + text + "\")\n"
	}
	var value string
	err = evaluator.EvaluateExpression(ctx, directSource("probe:/large.pkl", sourceText), "value", &value)
	if err != nil {
		result.DiagnosticBytes = len(err.Error())
		result.Error = errorPreview(err.Error())
	} else {
		result.OutputBytes = len(value)
	}
	result.OK = true
	result.ElapsedMS = time.Since(started).Seconds() * 1000
	return result
}

func errorPreview(value string) string {
	if len(value) <= 512 {
		return value
	}
	return value[:256] + "...<truncated>..." + value[len(value)-256:]
}

// The following is a deliberately small owned server client. It is not a
// general pkl-go replacement: it proves only the bounded request/reply subset
// needed by this feasibility probe. Message codes and field names are copied
// from the pinned pkl-go v0.14.0 msgapi sources, without importing internal
// packages.
type boundedProtocolReader struct {
	r         io.Reader
	remaining int64
}

func (r *boundedProtocolReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, fmt.Errorf("protocol byte budget %d exceeded", maxProtocolBytes)
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	return n, err
}

type ownedInbound struct {
	RequestID   int64
	EvaluatorID int64
	URI         string
	Error       string
	Result      []byte
	HasResult   bool
}

func discardBounded(decoder *msgpack.Decoder, depth int) error {
	if depth > 8 {
		return fmt.Errorf("protocol nesting exceeds depth 8")
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
		if length < 0 {
			return nil
		}
		if length > maxProtocolBytes {
			return fmt.Errorf("protocol string/bytes field exceeds %d bytes", maxProtocolBytes)
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
		if count < 0 || count > maxProtocolFields {
			return fmt.Errorf("protocol array count %d exceeds bound %d", count, maxProtocolFields)
		}
		for index := 0; index < count; index++ {
			if err := discardBounded(decoder, depth+1); err != nil {
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
		if count < 0 || count > maxProtocolFields {
			return fmt.Errorf("protocol map count %d exceeds bound %d", count, maxProtocolFields)
		}
		for index := 0; index < count*2; index++ {
			if err := discardBounded(decoder, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return decoder.Skip()
}

func decodeBoundedKey(decoder *msgpack.Decoder) (string, error) {
	length, err := decoder.DecodeBytesLen()
	if err != nil {
		return "", err
	}
	if length < 0 || length > maxProtocolKeyBytes {
		return "", fmt.Errorf("protocol key length %d exceeds bound %d", length, maxProtocolKeyBytes)
	}
	buffer := make([]byte, length)
	if err := decoder.ReadFull(buffer); err != nil {
		return "", err
	}
	return string(buffer), nil
}

func decodeBoundedString(decoder *msgpack.Decoder, limit int) (string, error) {
	length, err := decoder.DecodeBytesLen()
	if err != nil {
		return "", err
	}
	if length < 0 || length > limit {
		return "", fmt.Errorf("protocol string length %d exceeds bound %d", length, limit)
	}
	buffer := make([]byte, length)
	if err := decoder.ReadFull(buffer); err != nil {
		return "", err
	}
	return string(buffer), nil
}

func decodeBoundedBytes(decoder *msgpack.Decoder, limit int) ([]byte, error) {
	length, err := decoder.DecodeBytesLen()
	if err != nil {
		return nil, err
	}
	if length < 0 || length > limit {
		return nil, fmt.Errorf("protocol bytes length %d exceeds bound %d", length, limit)
	}
	value := make([]byte, length)
	if err := decoder.ReadFull(value); err != nil {
		return nil, err
	}
	return value, nil
}

func readProtocolMessage(decoder *msgpack.Decoder) (int, ownedInbound, error) {
	var message ownedInbound
	count, err := decoder.DecodeArrayLen()
	if err != nil {
		return 0, message, err
	}
	if count != 2 {
		return 0, message, fmt.Errorf("protocol envelope has %d fields, want 2", count)
	}
	code, err := decoder.DecodeInt()
	if err != nil {
		return 0, message, err
	}
	count, err = decoder.DecodeMapLen()
	if err != nil {
		return 0, message, err
	}
	if count < 0 || count > maxProtocolFields {
		return 0, message, fmt.Errorf("protocol map field count %d exceeds bound %d", count, maxProtocolFields)
	}
	seen := make(map[string]struct{}, count)
	for index := 0; index < count; index++ {
		key, err := decodeBoundedKey(decoder)
		if err != nil {
			return 0, message, err
		}
		if _, duplicate := seen[key]; duplicate {
			return 0, message, fmt.Errorf("protocol field %q repeated", key)
		}
		seen[key] = struct{}{}
		switch key {
		case "requestId":
			message.RequestID, err = int64Value(decoder)
		case "evaluatorId":
			message.EvaluatorID, err = int64Value(decoder)
		case "uri":
			message.URI, err = decodeBoundedString(decoder, maxProtocolKeyBytes)
		case "error":
			message.Error, err = decodeBoundedString(decoder, 64*1024)
		case "result":
			message.Result, err = decodeBoundedBytes(decoder, maxProtocolBytes)
			message.HasResult = err == nil
		default:
			err = discardBounded(decoder, 0)
		}
		if err != nil {
			return 0, message, fmt.Errorf("decode protocol field %q: %w", key, err)
		}
	}
	return int(code), message, nil
}

func int64Value(decoder *msgpack.Decoder) (int64, error) {
	return decoder.DecodeInt64()
}

type protocolWriter struct {
	writer io.Writer
	wg     sync.WaitGroup
}

func (writer *protocolWriter) send(ctx context.Context, code int, body any) error {
	encoder := msgpack.NewEncoder(nil)
	var buffer bytes.Buffer
	encoder.Reset(&buffer)
	if err := encoder.EncodeArrayLen(2); err != nil {
		return err
	}
	if err := encoder.EncodeInt(int64(code)); err != nil {
		return err
	}
	if err := encoder.Encode(body); err != nil {
		return err
	}
	if buffer.Len() > maxInputBytes {
		return fmt.Errorf("outgoing protocol message is %d bytes, exceeds %d-byte bound", buffer.Len(), maxInputBytes)
	}
	done := make(chan error, 1)
	writer.wg.Add(1)
	go func() {
		defer writer.wg.Done()
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

func (writer *protocolWriter) join(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		writer.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

type ownedReaderSpec struct {
	Scheme              string `msgpack:"scheme"`
	HasHierarchicalURIs bool   `msgpack:"hasHierarchicalUris"`
	IsGlobbable         bool   `msgpack:"isGlobbable"`
	IsLocal             bool   `msgpack:"isLocal"`
}

type ownedCreateRequest struct {
	RequestID        int64             `msgpack:"requestId"`
	ModuleReaders    []ownedReaderSpec `msgpack:"clientModuleReaders,omitempty"`
	Env              map[string]string `msgpack:"env,omitempty"`
	Properties       map[string]string `msgpack:"properties,omitempty"`
	OutputFormat     string            `msgpack:"outputFormat,omitempty"`
	AllowedModules   []string          `msgpack:"allowedModules,omitempty"`
	AllowedResources []string          `msgpack:"allowedResources,omitempty"`
}

type ownedEvaluateRequest struct {
	RequestID   int64  `msgpack:"requestId"`
	EvaluatorID int64  `msgpack:"evaluatorId"`
	ModuleURI   string `msgpack:"moduleUri"`
	ModuleText  string `msgpack:"moduleText,omitempty"`
	Expression  string `msgpack:"expr,omitempty"`
}

type ownedCloseRequest struct {
	EvaluatorID int64 `msgpack:"evaluatorId,omitempty"`
}

type ownedModuleResponse struct {
	RequestID   int64  `msgpack:"requestId"`
	EvaluatorID int64  `msgpack:"evaluatorId"`
	Contents    string `msgpack:"contents,omitempty"`
	Error       string `msgpack:"error,omitempty"`
}

type ownedStderr struct {
	buffer    bytes.Buffer
	total     int
	truncated bool
}

func (w *ownedStderr) Write(value []byte) (int, error) {
	originalLength := len(value)
	w.total += len(value)
	if w.buffer.Len() < maxStderrBytes {
		remaining := maxStderrBytes - w.buffer.Len()
		if len(value) > remaining {
			value = value[:remaining]
			w.truncated = true
		}
		_, _ = w.buffer.Write(value)
	} else {
		w.truncated = true
	}
	return originalLength, nil
}

type protocolReadResult struct {
	code    int
	message ownedInbound
	err     error
}

func readOwnedUntil(decoder *msgpack.Decoder, writer *protocolWriter, ctx context.Context, target int, modules map[string]string, captures *[]capture) <-chan protocolReadResult {
	result := make(chan protocolReadResult, 1)
	go func() {
		for {
			code, message, err := readProtocolMessage(decoder)
			if err != nil {
				result <- protocolReadResult{err: err}
				return
			}
			switch code {
			case 0x25: // evaluate log
				continue
			case 0x28: // evaluate readModule
				requestID := message.RequestID
				evaluatorID := message.EvaluatorID
				designation := message.URI
				contents, ok := modules[designation]
				response := ownedModuleResponse{RequestID: requestID, EvaluatorID: evaluatorID}
				if !ok {
					response.Error = fmt.Sprintf("module %q is outside bounded probe reader", designation)
				} else {
					digest := sha256.Sum256([]byte(contents))
					*captures = append(*captures, capture{Designation: designation, ResolvedTarget: "/fixture/home/workbench-context.pkl", ExactBytesSHA256: hex.EncodeToString(digest[:]), ExactBytesLength: len(contents)})
					response.Contents = contents
				}
				if err := writer.send(ctx, 0x29, response); err != nil {
					result <- protocolReadResult{err: err}
					return
				}
			case 0x24, 0x21:
				if code == target {
					result <- protocolReadResult{code: code, message: message}
					return
				}
				result <- protocolReadResult{err: fmt.Errorf("unexpected protocol response code 0x%x", code)}
				return
			default:
				result <- protocolReadResult{err: fmt.Errorf("unsupported protocol code 0x%x", code)}
				return
			}
		}
	}()
	return result
}

func runOwnedWriteCancellation(pklPath string, timeoutMS int) probeResult {
	started := time.Now()
	result := probeResult{Mode: "owned-server-blocked-stdin-write", ContextTimeoutMS: timeoutMS, ProtocolBytesLimit: maxProtocolBytes}
	command := exec.Command(pklPath, "server")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	childStdin, stdin, err := os.Pipe()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		result.Error = err.Error()
		return result
	}
	command.Stdin = childStdin
	command.Stdout = childStdout
	stderr := &ownedStderr{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		result.Error = err.Error()
		return result
	}
	_ = childStdin.Close()
	_ = childStdout.Close()
	result.ChildPIDsBeforeClose = processTreePIDs()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	writer := &protocolWriter{writer: stdin}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	// The fixture does not consume stdin. This body is below the outgoing
	// message bound but larger than the pipe buffer, so cancellation must
	// interrupt Write rather than merely close a completed request.
	source := strings.Repeat("x", maxInputBytes-64*1024)
	request := ownedEvaluateRequest{RequestID: 9, EvaluatorID: 1, ModuleURI: "probe:/blocked.pkl", ModuleText: source, Expression: "value"}
	sendErr := writer.send(ctx, 0x23, request)
	_ = stdin.Close()
	_ = stdout.Close()
	if sendErr == nil {
		result.Error = "blocked stdin fixture unexpectedly accepted the complete write"
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	processJoined := false
	select {
	case <-wait:
		processJoined = true
	case <-time.After(750 * time.Millisecond):
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		select {
		case <-wait:
			processJoined = true
		case <-time.After(750 * time.Millisecond):
		}
	}
	writerJoined := writer.join(750 * time.Millisecond)
	result.Joined = processJoined && writerJoined
	result.InputBytes = len(source)
	result.StderrBytes = stderr.total
	result.StderrTruncated = stderr.truncated
	result.ChildPIDsAfterClose = processTreePIDs()
	result.OK = sendErr != nil && errors.Is(sendErr, context.DeadlineExceeded) && result.Joined && len(result.ChildPIDsAfterClose) == 0
	if !result.OK && result.Error == "" {
		result.Error = fmt.Sprintf("blocked stdin write result: %v", sendErr)
	}
	result.ElapsedMS = time.Since(started).Seconds() * 1000
	return result
}

func runOwnedServer(pklPath, kind string, timeoutMS int) probeResult {
	started := time.Now()
	result := probeResult{Mode: "owned-server-" + kind, ContextTimeoutMS: timeoutMS, ProtocolBytesLimit: maxProtocolBytes}
	monitorStop := make(chan struct{})
	monitorDone := make(chan struct{})
	var monitorPeak int64
	go monitorChildren(monitorStop, monitorDone, &monitorPeak)
	defer func() {
		close(monitorStop)
		<-monitorDone
		result.ChildPeakTreeRSSKB = atomic.LoadInt64(&monitorPeak)
	}()
	command := exec.Command(pklPath, "server")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	childStdin, stdin, err := os.Pipe()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		result.Error = err.Error()
		return result
	}
	command.Stdin = childStdin
	command.Stdout = childStdout
	stderr := &ownedStderr{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = childStdin.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		result.Error = err.Error()
		return result
	}
	_ = childStdin.Close()
	_ = childStdout.Close()
	result.ChildPIDsBeforeClose = processTreePIDs()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	protocolReader := &boundedProtocolReader{r: stdout, remaining: maxProtocolBytes}
	decoder := msgpack.NewDecoder(protocolReader)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	modules := map[string]string{"probe:/policy.pkl": "module probe.Policy\nenabled = false\nvalue = \"captured-reader-value\"\n"}
	captures := make([]capture, 0, 1)
	writer := &protocolWriter{writer: stdin}
	waitProcess := func(force bool) bool {
		if force {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		select {
		case <-wait:
			return true
		case <-time.After(750 * time.Millisecond):
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			select {
			case <-wait:
				return true
			case <-time.After(750 * time.Millisecond):
				return false
			}
		}
	}

	terminate := func(force bool) {
		_ = stdin.Close()
		_ = stdout.Close()
		processJoined := waitProcess(force)
		writerJoined := writer.join(750 * time.Millisecond)
		result.Joined = processJoined && writerJoined
	}
	failed := func(err error) probeResult {
		result.Error = errorPreview(err.Error())
		result.StderrBytes = stderr.total
		result.StderrTruncated = stderr.truncated
		result.ChildPIDsAfterClose = processTreePIDs()
		result.ElapsedMS = time.Since(started).Seconds() * 1000
		return result
	}

	create := ownedCreateRequest{
		RequestID:        1,
		ModuleReaders:    []ownedReaderSpec{{Scheme: "probe", HasHierarchicalURIs: true, IsLocal: true}},
		Env:              map[string]string{},
		Properties:       map[string]string{},
		OutputFormat:     "json",
		AllowedModules:   []string{`^probe:/.*$`, `^repl:.*$`, `^pkl:[A-Za-z0-9]+$`},
		AllowedResources: []string{`^prop:pkl.outputFormat$`},
	}
	if err := writer.send(ctx, 0x20, create); err != nil {
		terminate(true)
		if ctx.Err() != nil {
			result.Error = "owned server create write cancelled and joined"
			result.ChildPIDsAfterClose = processTreePIDs()
			result.OK = result.Joined && len(result.ChildPIDsAfterClose) == 0
			result.ElapsedMS = time.Since(started).Seconds() * 1000
			return result
		}
		return failed(err)
	}
	read := readOwnedUntil(decoder, writer, ctx, 0x21, modules, &captures)
	var created protocolReadResult
	select {
	case created = <-read:
	case <-ctx.Done():
		terminate(true)
		<-read
		result.Error = "owned server handshake cancelled and joined"
		result.ChildPIDsAfterClose = processTreePIDs()
		result.Joined = len(result.ChildPIDsAfterClose) == 0
		result.OK = result.Joined
		result.ElapsedMS = time.Since(started).Seconds() * 1000
		return result
	}
	if created.err != nil {
		terminate(true)
		return failed(created.err)
	}
	if created.message.Error != "" {
		terminate(true)
		return failed(errors.New(created.message.Error))
	}
	evaluatorID := created.message.EvaluatorID
	if evaluatorID == 0 {
		err = fmt.Errorf("owned server create response omitted evaluatorId")
	}
	if err != nil {
		terminate(true)
		return failed(err)
	}

	source := "module probe.Main\nimport \"probe:/policy.pkl\"\n"
	expression := "policy.enabled"
	if kind == "output" {
		value := strings.Repeat("x", 2*1024*1024)
		source = "module probe.Main\nvalue = \"" + value + "\"\n"
		expression = "value"
	}
	if kind == "diagnostic" {
		value := strings.Repeat("x", 2*1024*1024)
		source = "module probe.Main\nvalue = throw(\"" + value + "\")\n"
		expression = "value"
	}
	entry := makeCapture("probe:/main.pkl", "/fixture/project/workbench-context.pkl", source)
	result.EntrySourceCapture = &entry
	if len(source) > maxInputBytes {
		terminate(true)
		return failed(fmt.Errorf("input source is %d bytes, exceeds %d-byte bound", len(source), maxInputBytes))
	}
	evaluate := ownedEvaluateRequest{RequestID: 2, EvaluatorID: evaluatorID, ModuleURI: "probe:/main.pkl", ModuleText: source, Expression: expression}
	if err := writer.send(ctx, 0x23, evaluate); err != nil {
		terminate(true)
		if ctx.Err() != nil {
			result.Error = "owned server evaluate write cancelled and joined"
			result.ChildPIDsAfterClose = processTreePIDs()
			result.OK = result.Joined && len(result.ChildPIDsAfterClose) == 0
			result.ElapsedMS = time.Since(started).Seconds() * 1000
			return result
		}
		return failed(err)
	}
	read = readOwnedUntil(decoder, writer, ctx, 0x24, modules, &captures)
	var evaluated protocolReadResult
	select {
	case evaluated = <-read:
	case <-ctx.Done():
		terminate(true)
		<-read
		return failed(context.DeadlineExceeded)
	}
	if evaluated.err != nil {
		if strings.Contains(evaluated.err.Error(), "protocol") || strings.Contains(evaluated.err.Error(), "budget") {
			result.ProtocolRejected = true
			terminate(true)
			result.Captures = append([]capture{entry}, captures...)
			result.StderrBytes = stderr.total
			result.StderrTruncated = stderr.truncated
			result.ChildPIDsAfterClose = processTreePIDs()
			result.OK = result.Joined && len(result.ChildPIDsAfterClose) == 0
			result.Error = errorPreview(evaluated.err.Error())
			result.ElapsedMS = time.Since(started).Seconds() * 1000
			return result
		}
		terminate(true)
		return failed(evaluated.err)
	}
	if evaluated.message.Error != "" {
		terminate(true)
		return failed(errors.New("owned server evaluate: " + evaluated.message.Error))
	}
	if !evaluated.message.HasResult {
		terminate(true)
		return failed(errors.New("owned server evaluate response omitted result"))
	}
	payload := evaluated.message.Result
	result.OutputBytes = len(payload)
	result.Captures = append([]capture{entry}, captures...)
	if kind == "normal" {
		var value bool
		if err := pkl.Unmarshal(payload, &value); err != nil {
			terminate(true)
			return failed(err)
		}
		result.Value = value
	}
	_ = writer.send(ctx, 0x22, ownedCloseRequest{EvaluatorID: evaluatorID})
	_ = stdin.Close()
	result.Joined = waitProcess(false) && writer.join(750*time.Millisecond)
	_ = stdout.Close()
	result.ChildPIDsAfterClose = processTreePIDs()
	result.StderrBytes = stderr.total
	result.StderrTruncated = stderr.truncated
	result.ProtocolRejected = false
	result.OK = result.Joined && len(result.ChildPIDsAfterClose) == 0
	result.ElapsedMS = time.Since(started).Seconds() * 1000
	return result
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: reader_probe <reader|cold|warm|lifecycle|file-freshness|oversized-output|oversized-diagnostic|owned-cancel|owned-write-cancel|owned-server> <pkl> [kind] [timeout-ms]")
		os.Exit(2)
	}
	mode, pklPath := os.Args[1], os.Args[2]
	var result probeResult
	switch mode {
	case "reader":
		result = runReader(pklPath, false)
	case "cold":
		result = runCold(pklPath)
	case "warm":
		result = runReader(pklPath, true)
		result.Mode = "warm"
	case "lifecycle":
		timeoutMS := 100
		if len(os.Args) > 3 {
			timeoutMS, _ = strconv.Atoi(os.Args[3])
		}
		result = runLifecycle(pklPath, timeoutMS)
	case "file-freshness":
		result = runFileFreshness()
	case "oversized-output":
		result = runOversizedFixed(pklPath, "output", 2*1024*1024)
	case "oversized-diagnostic":
		result = runOversizedFixed(pklPath, "diagnostic", 2*1024*1024)
	case "owned-cancel":
		timeoutMS := 100
		if len(os.Args) > 3 {
			timeoutMS, _ = strconv.Atoi(os.Args[3])
		}
		result = runOwnedCancellation(pklPath, timeoutMS)
	case "owned-write-cancel":
		timeoutMS := 100
		if len(os.Args) > 3 {
			timeoutMS, _ = strconv.Atoi(os.Args[3])
		}
		result = runOwnedWriteCancellation(pklPath, timeoutMS)
	case "owned-server":
		kind := "normal"
		timeoutMS := 3000
		if len(os.Args) > 3 {
			kind = os.Args[3]
		}
		if len(os.Args) > 4 {
			timeoutMS, _ = strconv.Atoi(os.Args[4])
		}
		result = runOwnedServer(pklPath, kind, timeoutMS)
	default:
		result = probeResult{Mode: mode, Error: "unknown mode"}
	}
	if !result.OK && result.Error == "" {
		result.Error = "probe failed"
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}
