package evaluate

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/vmihailenco/msgpack/v5"
)

//go:embed testdata/context/owned-worker.sh
var ownedWorkerScript []byte

func TestContextEvaluatorOwnedLifecycleAndFreshness(t *testing.T) {
	pkl := testPklExecutable(t)
	root := t.TempDir()
	worker := testWorker(t, root)
	lock := testRuntimeLock(t, root)
	record := filepath.Join(root, "worker-args.txt")
	evaluator := testContextEvaluator(t, pkl, worker, lock, filepath.Join(root, "evaluator.lock"), []string{"--record", record, "--write-stderr", "owned helper started"})
	entryPath := filepath.Join(root, "workbench context #%.pkl")
	if err := os.WriteFile(filepath.Join(root, "rules.pkl"), []byte("module test.Rules\nvalue: String = \"imported\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := []byte("amends \"workbench:context\"\nimport \"./rules.pkl\" as rules\nenabled = rules.value == \"imported\"\n")
	if err := os.WriteFile(entryPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	input := testEvaluationInput(entryPath, root, source, 5_000)

	result, err := evaluator.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != contextapi.DeclarationKindProject || len(result.Dependencies.Captures) != 2 {
		t.Fatalf("unexpected owned result: kind=%q captures=%d", result.Kind, len(result.Dependencies.Captures))
	}
	if !strings.HasPrefix(result.Dependencies.Captures[0].Designation, contextLocalScheme+":/") {
		t.Fatalf("entry capture designation = %q", result.Dependencies.Captures[0].Designation)
	}
	for _, escaped := range []string{"%20", "%23", "%25"} {
		if !strings.Contains(result.Dependencies.Captures[0].Designation, escaped) {
			t.Fatalf("entry designation did not escape %s: %q", escaped, result.Dependencies.Captures[0].Designation)
		}
	}
	if err := evaluator.Freshness(context.Background(), input, result); err != nil {
		t.Fatalf("freshness rejected unchanged capture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "rules.pkl"), []byte("module test.Rules\nvalue: String = \"changed\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Freshness(context.Background(), input, result); err == nil {
		t.Fatal("freshness accepted changed imported module")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "rules.pkl"), []byte("module test.Rules\nvalue: String = \"outside\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "rules.pkl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "rules.pkl"), filepath.Join(root, "rules.pkl")); err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Freshness(context.Background(), input, result); err == nil {
		t.Fatal("freshness accepted an import retargeted outside the authority root")
	}
	if err := os.WriteFile(entryPath, []byte("amends \"workbench:context\"\n// changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Freshness(context.Background(), input, result); err == nil {
		t.Fatal("freshness accepted changed entry")
	}

	recorded, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	recordText := string(recorded)
	if !strings.Contains(recordText, "pkl="+pkl) || !strings.Contains(recordText, "max-data-bytes=67108864") {
		t.Fatalf("worker did not receive exact path and finite cap: %q", recordText)
	}
}

func TestContextEvaluatorRejectsWorkerExitAfterOutput(t *testing.T) {
	pkl := testPklExecutable(t)
	root := t.TempDir()
	worker := testWorker(t, root)
	lock := testRuntimeLock(t, root)
	evaluator := testContextEvaluator(t, pkl, worker, lock, filepath.Join(root, "evaluator.lock"), []string{"--fail-after"})
	entryPath := filepath.Join(root, "workbench-context.pkl")
	source := []byte("amends \"workbench:context\"\n")
	if err := os.WriteFile(entryPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := evaluator.Evaluate(context.Background(), testEvaluationInput(entryPath, root, source, 5_000))
	if err == nil || !strings.Contains(err.Error(), "exited unsuccessfully") {
		t.Fatalf("worker exit after output was accepted: %v", err)
	}
}

func TestContextEvaluatorHomeDeclaration(t *testing.T) {
	pkl := testPklExecutable(t)
	root := t.TempDir()
	entryPath := filepath.Join(root, "workbench-context-home.pkl")
	source := []byte("amends \"workbench:context-home\"\n")
	if err := os.WriteFile(entryPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	evaluator := testContextEvaluator(t, pkl, testWorker(t, root), testRuntimeLock(t, root), filepath.Join(root, "evaluator.lock"), nil)
	input := testEvaluationInput(entryPath, root, source, 5_000)
	input.Origin.Authority = contextapi.ScopeAuthorityHome
	input.SchemaURI = "workbench:context-home"
	result, err := evaluator.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != contextapi.DeclarationKindHome || len(result.Dependencies.Captures) != 1 {
		t.Fatalf("unexpected home result: kind=%q captures=%d", result.Kind, len(result.Dependencies.Captures))
	}
	if err := evaluator.Freshness(context.Background(), input, result); err != nil {
		t.Fatalf("home freshness rejected unchanged declaration: %v", err)
	}
}

func TestContextEvaluatorBoundsBlockedWorkerAndDiagnostics(t *testing.T) {
	pkl := testPklExecutable(t)
	root := t.TempDir()
	lock := testRuntimeLock(t, root)
	entryPath := filepath.Join(root, "workbench-context.pkl")
	source := []byte("amends \"workbench:context\"\n")
	if err := os.WriteFile(entryPath, source, 0o600); err != nil {
		t.Fatal(err)
	}

	blocked := testContextEvaluator(t, pkl, testWorker(t, root), lock, filepath.Join(root, "blocked.lock"), []string{"--block"})
	started := time.Now()
	_, err := blocked.Evaluate(context.Background(), testEvaluationInput(entryPath, root, source, 100))
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("blocked worker was not bounded: err=%v elapsed=%s", err, time.Since(started))
	}

	ignoredClose := testContextEvaluator(t, pkl, testWorker(t, root), lock, filepath.Join(root, "ignored-close.lock"), []string{"--ignore-close"})
	started = time.Now()
	if _, err := ignoredClose.Evaluate(context.Background(), testEvaluationInput(entryPath, root, source, 5_000)); err != nil {
		t.Fatalf("ignored-close worker failed before joined cleanup: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("ignored-close cleanup exceeded bound: %s", elapsed)
	}

	noisyRecord := filepath.Join(root, "noisy-args.txt")
	noisy := testContextEvaluator(t, pkl, testWorker(t, root), lock, filepath.Join(root, "noisy.lock"), []string{"--record", noisyRecord, "--stderr-bytes", "128"})
	input := testEvaluationInput(entryPath, root, source, 5_000)
	input.Bounds.MaxDiagnosticsBytes = 32
	_, err = noisy.Evaluate(context.Background(), input)
	if err == nil || !strings.Contains(err.Error(), "diagnostics") {
		recorded, _ := os.ReadFile(noisyRecord)
		t.Fatalf("diagnostic overflow was not rejected: %v (worker=%q)", err, recorded)
	}
}

func TestContextEvaluatorCancelsAfterHandshakeWhenWorkerStopsReading(t *testing.T) {
	pkl := testPklExecutable(t)
	root := t.TempDir()
	worker := testStopReader(t, root)
	entryPath := filepath.Join(root, "workbench-context.pkl")
	source := append([]byte("amends \"workbench:context\"\n"), bytes.Repeat([]byte(" "), 2<<20)...)
	if err := os.WriteFile(entryPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	evaluator := testContextEvaluator(t, pkl, worker, testRuntimeLock(t, root), filepath.Join(root, "evaluator.lock"), nil)
	input := testEvaluationInput(entryPath, root, source, 150)
	input.Bounds.MaxInputBytes = uint64(len(source) + 1024)
	started := time.Now()
	_, err := evaluator.Evaluate(context.Background(), input)
	if err == nil {
		t.Fatal("stop-reading worker was accepted")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("blocked stdin cleanup exceeded bound: %s", elapsed)
	}
}

func testPklExecutable(t *testing.T) string {
	t.Helper()
	if value := os.Getenv("PKL_EXECUTABLE"); value != "" {
		absolute, err := filepath.Abs(value)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("PKL_EXECUTABLE is not a regular file: %q: %v", absolute, err)
		}
		return absolute
	}
	t.Skip("set PKL_EXECUTABLE to the exact pinned Pkl executable for evaluator tests")
	return ""
}

func testWorker(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "owned-worker.sh")
	if err := os.WriteFile(path, ownedWorkerScript, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func testStopReader(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "stop-reader")
	command := exec.Command("go", "build", "-o", path, "./testdata/context/stop_reader.go")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build stop-reader helper: %v: %s", err, output)
	}
	return path
}

func testRuntimeLock(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "runtime-lock.json")
	value := map[string]any{
		"runtimes": map[string]any{
			"pkl": map[string]any{
				"version":   "0.32.1",
				"artifacts": map[string]any{contextPlatform(): map[string]string{"sha256": strings.Repeat("a", 64)}},
			},
		},
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testContextEvaluator(t *testing.T, pkl, worker, lock, lease string, workerArguments []string) ContextEvaluator {
	t.Helper()
	runtimeEvaluator, err := NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := NewContextEvaluator(runtimeEvaluator, ContextOptions{
		WorkerExecutable:    worker,
		WorkerArguments:     workerArguments,
		RuntimeLockPath:     lock,
		EvaluatorLeasePath:  lease,
		MaxProcessDataBytes: 64 * 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	return evaluator
}

func testEvaluationInput(path, root string, source []byte, deadline uint64) contextapi.EvaluationInput {
	return contextapi.EvaluationInput{
		Origin:      contextapi.DeclarationOrigin{Path: path, Root: root, Authority: contextapi.ScopeAuthorityProject},
		SchemaURI:   "workbench:context",
		SourceBytes: append([]byte(nil), source...),
		Bounds: contextapi.EvaluationBounds{
			MaxInputBytes:       1 << 20,
			MaxOutputBytes:      1 << 20,
			MaxDiagnosticsBytes: 4 << 10,
			MaxImports:          8,
			DeadlineMs:          deadline,
		},
	}
}

func TestContextWorkerSpecRejectsRelativeExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix worker entry is compile-only on Windows")
	}
	err := RunContextWorker(ContextWorkerSpec{PklExecutable: "pkl", MaxProcessDataBytes: 1})
	if err == nil {
		t.Fatal("relative worker executable was accepted")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("unexpected worker validation error: %v", err)
	}
}

func TestContextProtocolOutputScalarIsBoundedAndSingular(t *testing.T) {
	var encoded bytes.Buffer
	encoder := msgpack.NewEncoder(&encoded)
	if err := encoder.EncodeString(`{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	if got, err := contextResultJSON(encoded.Bytes(), 64); err != nil || string(got) != `{"enabled":true}` {
		t.Fatalf("valid output.text scalar: got=%q err=%v", got, err)
	}
	if _, err := contextResultJSON(encoded.Bytes(), 4); err == nil {
		t.Fatal("oversized output.text scalar was accepted")
	}
	if err := encoder.EncodeString("trailing"); err != nil {
		t.Fatal(err)
	}
	if _, err := contextResultJSON(encoded.Bytes(), 64); err == nil {
		t.Fatal("trailing output.text scalar was accepted")
	}
	huge := []byte{0xdb, 0xff, 0xff, 0xff, 0xff}
	if _, err := contextResultJSON(huge, 64); err == nil {
		t.Fatal("huge declared output.text scalar was accepted")
	}
}

func TestContextEvaluatorLeaseIsOnePerUserSlot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file lease is compile-only on Windows")
	}
	path := filepath.Join(t.TempDir(), "private", "evaluator.lock")
	release, err := lockEvaluatorLease(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := lockEvaluatorLease(ctx, path); err == nil {
		t.Fatal("second evaluator slot acquired while first was held")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if next, err := lockEvaluatorLease(context.Background(), path); err != nil {
		t.Fatal(err)
	} else if err := next(); err != nil {
		t.Fatal(err)
	}
}

func TestContextEvaluatorRepeatedSuccessClosesOwnedDescriptors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor witness uses Linux /proc")
	}
	pkl := testPklExecutable(t)
	root := t.TempDir()
	entryPath := filepath.Join(root, "workbench-context.pkl")
	source := []byte("amends \"workbench:context\"\n")
	if err := os.WriteFile(entryPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	evaluator := testContextEvaluator(t, pkl, testWorker(t, root), testRuntimeLock(t, root), filepath.Join(root, "evaluator.lock"), nil)
	input := testEvaluationInput(entryPath, root, source, 5_000)
	before := testOpenDescriptorCount(t)
	for index := 0; index < 12; index++ {
		if _, err := evaluator.Evaluate(context.Background(), input); err != nil {
			t.Fatalf("iteration %d: %v", index, err)
		}
	}
	after := testOpenDescriptorCount(t)
	if after > before+2 {
		t.Fatalf("owned evaluator descriptors grew from %d to %d", before, after)
	}
}

func testOpenDescriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestNewContextEvaluatorIsLazyAboutRuntimeIdentity(t *testing.T) {
	runtimeEvaluator, err := NewEvaluator(filepath.Join(t.TempDir(), "pkl"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewContextEvaluator(runtimeEvaluator, ContextOptions{
		WorkerExecutable:   filepath.Join(t.TempDir(), "worker"),
		RuntimeLockPath:    filepath.Join(t.TempDir(), "runtime-lock.json"),
		EvaluatorLeasePath: filepath.Join(t.TempDir(), "evaluator.lock"),
	})
	if err != nil {
		t.Fatalf("constructor performed an unexpected identity read: %v", err)
	}
}
