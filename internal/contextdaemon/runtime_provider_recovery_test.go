package contextdaemon_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextdaemon"
)

// This subprocess implements only the public provider protocol. It has no BB
// queue, native sender or model, and records each invocation before a fault.
func TestRuntimeLifecycleProvider(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--lifecycle-provider" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	if len(args) != 3 {
		os.Exit(21)
	}
	control, bodyPath, logPath := args[0], args[1], args[2]
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request contextapi.JSONRPCRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(22)
		}
		var wire struct {
			Input struct {
				Observation struct {
					Invocation struct {
						ID string `json:"id"`
					} `json:"invocation"`
				} `json:"observation"`
			} `json:"input"`
		}
		_ = json.Unmarshal(request.Params, &wire)
		mode, _ := os.ReadFile(control)
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(23)
		}
		_ = json.NewEncoder(file).Encode(map[string]any{"pid": os.Getpid(), "method": request.Method, "rpcID": request.ID, "invocation": wire.Input.Observation.Invocation.ID, "mode": string(mode)})
		_ = file.Close()
		var result any
		switch request.Method {
		case contextapi.RPCMethodInitialize:
			result = map[string]any{"accepted": true, "capabilities": []string{"contribute"}, "reasons": []any{}}
		case contextapi.RPCMethodContribute:
			if string(mode) == "timeout" || string(mode) == "exit" {
				if os.WriteFile(control, []byte("normal"), 0600) != nil {
					os.Exit(24)
				}
				if string(mode) == "exit" {
					process, err := os.FindProcess(os.Getpid())
					if err != nil {
						os.Exit(27)
					}
					if process.Kill() != nil {
						os.Exit(27)
					}
					time.Sleep(time.Second)
					os.Exit(27)
				}
				time.Sleep(30 * time.Second) // The owning client must terminate this process.
				os.Exit(28)
			}
			body, err := os.ReadFile(bodyPath)
			if err != nil {
				os.Exit(25)
			}
			digest := sha256.Sum256(body)
			result = map[string]any{"output": map[string]any{"contributions": []any{map[string]any{
				"slot": map[string]any{"key": "lifecycle-slot"}, "source": map[string]any{"identity": map[string]any{"id": "lifecycle-source"}, "kind": "session"},
				"sourceRevision": map[string]any{"revision": "sha256:" + hex.EncodeToString(digest[:])}, "body": string(body), "reasons": []any{},
			}}, "reasons": []any{}}}
		case contextapi.RPCMethodShutdown:
			var shutdown contextapi.ProviderShutdownRequest
			if json.Unmarshal(request.Params, &shutdown) != nil {
				os.Exit(26)
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"requestId": shutdown.RequestID, "reasons": []any{}}})
			return
		default:
			os.Exit(29)
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			os.Exit(30)
		}
	}
}

func TestRuntimeProviderRecoveryPreservesReceiptsAndReadsCurrentState(t *testing.T) {
	for _, fault := range []string{"timeout", "exit"} {
		t.Run(fault, func(t *testing.T) {
			f := newReviewFixture(t, map[string]any{"runtime": map[string]any{"maxProviderProcesses": 1}}, false)
			control, bodyPath, logPath := filepath.Join(f.root, "mode"), filepath.Join(f.root, "body"), filepath.Join(f.root, "requests.jsonl")
			write := func(path, text string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(control, "normal")
			write(bodyPath, "LIFECYCLE_A")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			root := f.executableProject(t, "provider-recovery", "unused", executable, []string{"-test.run=^TestRuntimeLifecycleProvider$", "--", "--lifecycle-provider", control, bodyPath, logPath})
			observe := func(invocation string) contextdaemon.ObserveResult {
				t.Helper()
				v := f.observe(t, f.input(t, root, "recovery-session", invocation, false, true))
				return v
			}
			initial := observe("initial")
			if initial.Decision.State != contextapi.DeliveryOffered || !strings.Contains(initial.Decision.Offer.Body, "LIFECYCLE_A") {
				t.Fatalf("baseline offer missing: %#v", initial)
			}
			if result := f.confirm(t, root, initial.Decision.Offer, time.Now()); result.State != contextapi.DeliveryConfirmed {
				t.Fatalf("baseline confirmation: %#v", result)
			}
			write(control, fault)
			failed := observe("fault")
			if failed.Decision.Offer.Identity.ID != 0 {
				t.Fatalf("faulting callback invented delivery: %#v", failed)
			}
			if len(failed.Decision.Reasons) == 0 {
				t.Fatal("faulting callback lost its error")
			}
			// Let an unexpected exit be reaped; deadline termination is synchronous.
			if fault == "exit" {
				time.Sleep(30 * time.Millisecond)
			}
			same := observe("same-after-fault")
			if same.Decision.Offer.Identity.ID != 0 {
				t.Errorf("provider replacement reset the confirmed same-state receipt: %#v", same)
			}
			for _, reason := range same.Decision.Reasons {
				if reason.Code == contextapi.ReasonProviderFailed {
					t.Errorf("later callback reused failed provider: %#v", same)
				}
			}
			write(bodyPath, "LIFECYCLE_B")
			changed := observe("changed-after-fault")
			if changed.Decision.State != contextapi.DeliveryOffered || !strings.Contains(changed.Decision.Offer.Body, "LIFECYCLE_B") {
				t.Errorf("later callback did not read/deliver CURRENT state: %#v", changed)
			} else if result := f.confirm(t, root, changed.Decision.Offer, time.Now()); result.State != contextapi.DeliveryConfirmed {
				t.Errorf("replacement confirmation: %#v", result)
			}
			repeat := observe("repeat-after-fault")
			if repeat.Decision.Offer.Identity.ID != 0 {
				t.Errorf("new same-state receipt not suppressed: %#v", repeat)
			}
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			var starts = map[int]bool{}
			faultCalls := 0
			sameCalls := 0
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var r struct {
					PID        int    `json:"pid"`
					Method     string `json:"method"`
					Invocation string `json:"invocation"`
					Mode       string `json:"mode"`
				}
				if json.Unmarshal([]byte(line), &r) != nil {
					t.Fatal("malformed helper receipt")
				}
				if r.Method == string(contextapi.RPCMethodInitialize) {
					starts[r.PID] = true
				}
				if r.Invocation == "fault" && r.Method == string(contextapi.RPCMethodContribute) {
					faultCalls++
				}
				if r.Invocation == "same-after-fault" {
					sameCalls++
				}
			}
			if len(starts) != 2 {
				t.Errorf("want exactly one new helper on a later callback, got %d processes; log=%s", len(starts), raw)
			}
			if faultCalls != 1 {
				t.Errorf("faulting callback replayed %d times; log=%s", faultCalls, raw)
			}
			if sameCalls != 1 {
				t.Errorf("later unchanged callback did not re-read exactly once: %d; log=%s", sameCalls, raw)
			}
			status, err := f.runtime.Status(context.Background(), contextdaemon.StatusRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if status.ProviderProcesses != 1 {
				t.Errorf("replacement exceeded/lost provider charge: %#v", status)
			}
		})
	}
}
