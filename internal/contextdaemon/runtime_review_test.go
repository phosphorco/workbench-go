package contextdaemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextcache"
	"github.com/phosphorco/workbench-go/internal/contextconfig"
	"github.com/phosphorco/workbench-go/internal/contextdaemon"
	"github.com/phosphorco/workbench-go/internal/contexttrace"
	"github.com/phosphorco/workbench-go/internal/evaluate"
)

func TestMain(m *testing.M) {
	for index, argument := range os.Args {
		if argument != "--review-context-worker" {
			continue
		}
		var pkl string
		var maximum uint64
		for index++; index < len(os.Args); index++ {
			switch os.Args[index] {
			case "--pkl":
				if index+1 < len(os.Args) {
					pkl = os.Args[index+1]
					index++
				}
			case "--max-data-bytes":
				if index+1 < len(os.Args) {
					maximum, _ = strconv.ParseUint(os.Args[index+1], 10, 64)
					index++
				}
			}
		}
		if err := evaluate.RunContextWorker(evaluate.ContextWorkerSpec{PklExecutable: pkl, MaxProcessDataBytes: maximum}); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type reviewFixture struct {
	runtime *contextdaemon.Runtime
	deps    contextconfig.LoadDependencies
	home    string
	root    string
}

func newReviewFixture(t *testing.T, home map[string]any, badCache bool) reviewFixture {
	return newReviewFixtureOptions(t, home, badCache, contextdaemon.RuntimeOptions{})
}

func newReviewFixtureOptions(t *testing.T, home map[string]any, badCache bool, options contextdaemon.RuntimeOptions) reviewFixture {
	t.Helper()
	root := t.TempDir()
	if home == nil {
		home = map[string]any{}
	}
	homePath := filepath.Join(root, "workbench-context-home.pkl")
	if err := os.WriteFile(homePath, reviewHomeSource(home), 0600); err != nil {
		t.Fatal(err)
	}
	paths := contextdaemon.DefaultPaths(filepath.Join(root, "runtime"))
	paths.HomeConfigPath = homePath
	paths.CacheDir = filepath.Join(root, "cache")
	deps := reviewLoadDependencies(t, root)
	if badCache {
		if err := os.MkdirAll(deps.Cache.Root(), 0700); err != nil {
			t.Fatal(err)
		}
		// The pool remains valid, but its trace-owned child is deliberately
		// malformed. This exercises the runtime's cache-diagnostic path without
		// making construction invent a second pool or a fake evaluator.
		if err := os.WriteFile(filepath.Join(deps.Cache.Root(), "trace"), []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	options.LoadDependencies = deps
	options.Paths = paths
	runtime, err := contextdaemon.NewRuntime(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return reviewFixture{runtime: runtime, deps: deps, home: homePath, root: root}
}

func (f reviewFixture) project(t *testing.T, name, canary string) string {
	t.Helper()
	root := filepath.Join(f.root, name)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workbench-context.pkl"), []byte(reviewProjectSource(true)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("Read target without canary.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeReviewManifest(t, root, canary)
	return root
}

func (f reviewFixture) executableProject(t *testing.T, name, canary, executable string, arguments []string) string {
	t.Helper()
	root := f.project(t, name, canary)
	if err := os.WriteFile(filepath.Join(root, "workbench-context.pkl"), []byte(reviewExecutableProjectSource(true, "review-provider", executable, arguments)), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeReviewExecutableProvider(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
log="$1"
closing="$2"
release="$3"
slow="$4"
delay="${5:-0}"
while IFS= read -r request; do
  printf '%s\n' "$request" >> "$log"
  id=$(printf '%s\n' "$request" | sed -n 's#.*"id":\([0-9][0-9]*\),"method".*#\1#p')
  if printf '%s\n' "$request" | grep -q '"method":"initialize"'; then
    printf '{"jsonrpc":"2.0","id":%s,"result":{"accepted":true,"capabilities":["contribute"],"reasons":[]}}\n' "$id"
  elif printf '%s\n' "$request" | grep -q '"method":"shutdown"'; then
    : > "$closing"
    if [ "$slow" = "true" ]; then
      while [ ! -e "$release" ]; do sleep 0.01; done
    fi
    printf '{"jsonrpc":"2.0","id":%s,"result":{"requestId":%s,"reasons":[]}}\n' "$id" "$id"
    exit 0
  else
    if [ "$delay" != "0" ]; then sleep "$delay"; fi
    printf '{"jsonrpc":"2.0","id":%s,"result":{"output":{"contributions":[{"slot":{"key":"review-slot"},"source":{"identity":{"id":"review-source"},"kind":"file","path":"provider.md"},"body":"PROVIDER_GUIDANCE","reasons":[]}],"reasons":[]}}}\n' "$id"
  fi
done
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeReviewManifest(t *testing.T, root, canary string) {
	t.Helper()
	text := "---\nroot = true\n[[docs]]\nfiles = [\"README.md\"]\nmessage = \"" + canary + "\"\n---\n"
	if err := os.WriteFile(filepath.Join(root, "ai-context.md"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func reviewHomeSource(home map[string]any) []byte {
	var source strings.Builder
	source.WriteString("amends \"workbench:context-home\"\nlimits {\n")
	if values, ok := home["runtime"].(map[string]any); ok {
		source.WriteString("  runtime {\n")
		for _, field := range []string{"maxPendingItems", "maxPendingMemoryBytes", "maxReceipts", "maxProviderProcesses", "idleTTLMs"} {
			if value, ok := reviewUnsigned(values[field]); ok {
				fmt.Fprintf(&source, "    %s = %d\n", field, value)
			}
		}
		source.WriteString("  }\n")
	}
	if values, ok := home["delivery"].(map[string]any); ok {
		if queue, ok := values["queue"].(map[string]any); ok {
			if value, ok := reviewUnsigned(queue["maxItemBytes"]); ok {
				fmt.Fprintf(&source, "  delivery { queue { maxItemBytes = %d } }\n", value)
			}
		}
	}
	source.WriteString("}\n")
	return []byte(source.String())
}

func reviewUnsigned(value any) (uint64, bool) {
	switch value := value.(type) {
	case int:
		return uint64(value), value >= 0
	case int64:
		return uint64(value), value >= 0
	case uint64:
		return value, true
	case uint32:
		return uint64(value), true
	case uint:
		return uint64(value), true
	default:
		return 0, false
	}
}

func reviewProjectSource(enabled bool) []byte {
	var source strings.Builder
	source.WriteString("amends \"workbench:context\"\n")
	if !enabled {
		source.WriteString("enabled = false\n")
	}
	source.WriteString("contributors { [\"project-guidance\"] = new AiContext {} }\n")
	return []byte(source.String())
}

func reviewExecutableProjectSource(enabled bool, id, executable string, arguments []string) []byte {
	var source strings.Builder
	source.WriteString("amends \"workbench:context\"\n")
	if !enabled {
		source.WriteString("enabled = false\n")
	}
	fmt.Fprintf(&source, "contributors { [\"%s\"] = new Executable { executable = %s\n", id, strconv.Quote(executable))
	if len(arguments) > 0 {
		source.WriteString("arguments { ")
		for _, argument := range arguments {
			source.WriteString(strconv.Quote(argument))
			source.WriteString("; ")
		}
		source.WriteString("}\n")
	}
	source.WriteString("capabilities { \"contribute\" } } }\n")
	return []byte(source.String())
}

func reviewMixedProjectSource(enabled bool, executable string, arguments []string) []byte {
	var source strings.Builder
	source.WriteString("amends \"workbench:context\"\n")
	if !enabled {
		source.WriteString("enabled = false\n")
	}
	source.WriteString("contributors {\n  [\"ai-context\"] = new AiContext {}\n  [\"review-provider\"] = new Executable {\n")
	fmt.Fprintf(&source, "    executable = %s\n", strconv.Quote(executable))
	if len(arguments) > 0 {
		source.WriteString("    arguments { ")
		for _, argument := range arguments {
			source.WriteString(strconv.Quote(argument))
			source.WriteString("; ")
		}
		source.WriteString("}\n")
	}
	source.WriteString("    capabilities { \"contribute\" }\n  }\n}\n")
	return []byte(source.String())
}

func reviewLoadDependencies(t *testing.T, root string) contextconfig.LoadDependencies {
	t.Helper()
	pkl := os.Getenv("PKL_EXECUTABLE")
	if pkl == "" {
		t.Skip("set PKL_EXECUTABLE to the exact pinned Pkl executable for evaluator-backed daemon review")
	}
	var err error
	pkl, err = filepath.Abs(pkl)
	if err != nil {
		t.Fatal(err)
	}
	pkl, err = filepath.EvalSymlinks(pkl)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	lock := map[string]any{
		"runtimes": map[string]any{
			"pkl": map[string]any{
				"version":   "0.32.1",
				"artifacts": map[string]any{reviewContextPlatform(): map[string]string{"sha256": strings.Repeat("a", 64)}},
			},
		},
	}
	encoded, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "context-runtime-lock.json")
	if err := os.WriteFile(lockPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	evaluator, err := evaluate.NewEvaluator(pkl)
	if err != nil {
		t.Fatal(err)
	}
	contextEvaluator, err := evaluate.NewContextEvaluator(evaluator, evaluate.ContextOptions{
		WorkerExecutable:   worker,
		WorkerArguments:    []string{"--review-context-worker"},
		RuntimeLockPath:    lockPath,
		EvaluatorLeasePath: filepath.Join(root, "context-evaluator.lock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := contextcache.Open(filepath.Join(root, "cache"), contextcache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return contextconfig.LoadDependencies{
		Evaluate:  contextEvaluator.Evaluate,
		Freshness: contextEvaluator.Freshness,
		Identity:  contextEvaluator.Identity,
		Cache:     pool,
	}
}

func reviewContextPlatform() string {
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

func (f reviewFixture) input(t *testing.T, root, session, invocation string, read, available bool) contextdaemon.ObserveInput {
	t.Helper()
	now := time.Now()
	loaded, err := contextconfig.Load(t.Context(), contextconfig.LoadOptions{WorkingDirectory: root, HomeConfigPath: f.home, Now: now}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Result.State != contextapi.ActivationEnabled {
		t.Fatalf("activation: %#v", loaded.Result)
	}
	audience := contextapi.Audience{ID: contextapi.AudienceID("codex:" + session), Continuity: contextapi.ContinuityUnknown}
	turn := contextapi.TurnRef{ID: "turn-1", State: contextapi.TurnKnown}
	observation := contextapi.Observation{At: now, Audience: audience, Scope: loaded.Result.Scope, Turn: turn, Invocation: contextapi.InvocationRef{ID: contextapi.InvocationID(invocation), HookEvent: "PostToolUse"}}
	if read {
		observation.Resources = []contextapi.ObservedResource{{Path: "README.md", Kind: contextapi.ResourceFile, Operation: contextapi.ResourceRead, Outcome: contextapi.ResourceResolved, Confidence: contextapi.ConfidenceObserved}}
	}
	return contextdaemon.ObserveInput{WorkingDirectory: root, Host: contextapi.HostSnapshot{Harness: contextapi.HarnessCodex, WorkingDirectory: root, Turn: turn}, Observation: observation, Opportunity: contextapi.DeliveryOpportunity{ID: invocation, Available: available, Surface: contextapi.DeliveryCodexContext, MaxBytes: 32000, MaxItems: 64, Invocation: observation.Invocation}}
}

func (f reviewFixture) observe(t *testing.T, input contextdaemon.ObserveInput) contextdaemon.ObserveResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := f.runtime.Observe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireReviewOffer(t *testing.T, result contextdaemon.ObserveResult, canary string) contextapi.Offer {
	t.Helper()
	if result.Decision.State != contextapi.DeliveryOffered || !strings.Contains(result.Decision.Offer.Body, canary) {
		t.Fatalf("expected complete canary offer %q: %#v", canary, result.Decision)
	}
	return result.Decision.Offer
}

func (f reviewFixture) confirm(t *testing.T, root string, offer contextapi.Offer, at time.Time) contextapi.ConfirmationResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	return f.runtime.Confirm(ctx, contextdaemon.ConfirmInput{WorkingDirectory: root, Identity: offer.Identity, Handoff: contextapi.HandoffOutcome{State: contextapi.HandoffConfirmed, EmittedContent: offer.Identity.Body}, At: at})
}

func TestRuntimeReviewDelayedGuidanceAndClearKeepsReceipt(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "a", "DELAYED_CANARY")
	first := f.observe(t, f.input(t, root, "one", "read-1", true, false))
	if first.Decision.Offer.Identity.ID != 0 {
		t.Fatal("offered without a delivery opportunity")
	}
	second := f.observe(t, f.input(t, root, "one", "delivery-2", false, true))
	offer := requireReviewOffer(t, second, "DELAYED_CANARY")
	if result := f.confirm(t, root, offer, time.Now()); result.State != contextapi.DeliveryConfirmed {
		t.Fatalf("confirm: %#v", result)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := f.runtime.Clear(ctx, root); err != nil {
		t.Fatal(err)
	}
	repeated := f.observe(t, f.input(t, root, "one", "read-3", true, true))
	if repeated.Decision.Offer.Identity.ID != 0 {
		t.Fatal("clearing explanations reset a delivery receipt")
	}
}

func TestRuntimeReviewChangedPendingSourceIsWithdrawnAndRecruitableAgain(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "a", "OLD_CANARY")
	f.observe(t, f.input(t, root, "one", "read-1", true, false))
	writeReviewManifest(t, root, "NEW_CANARY")
	delayed := f.observe(t, f.input(t, root, "one", "delivery-2", false, true))
	if strings.Contains(delayed.Decision.Offer.Body, "OLD_CANARY") {
		t.Fatal("stale queued source reached delivery")
	}
	recruited := f.observe(t, f.input(t, root, "one", "read-3", true, true))
	requireReviewOffer(t, recruited, "NEW_CANARY")
}

func TestRuntimeReviewDeletedSourceCannotConfirm(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "a", "DELETE_CANARY")
	offer := requireReviewOffer(t, f.observe(t, f.input(t, root, "one", "read-1", true, true)), "DELETE_CANARY")
	if err := os.Remove(filepath.Join(root, "ai-context.md")); err != nil {
		t.Fatal(err)
	}
	if result := f.confirm(t, root, offer, time.Now()); result.State == contextapi.DeliveryConfirmed {
		t.Fatalf("deleted source confirmed: %#v", result)
	}
}

func TestRuntimeReviewInactiveConfirmationWithdrawsExactOffer(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "inactive-confirm", "INACTIVE-CONFIRM-CANARY")
	offer := requireReviewOffer(t, f.observe(t, f.input(t, root, "one", "read", true, true)), "INACTIVE-CONFIRM-CANARY")
	if err := os.WriteFile(filepath.Join(root, "workbench-context.pkl"), reviewProjectSource(false), 0600); err != nil {
		t.Fatal(err)
	}
	result := f.confirm(t, root, offer, time.Now())
	if result.State == contextapi.DeliveryConfirmed {
		t.Fatalf("inactive confirmation was accepted: %#v", result)
	}
	status, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{WorkingDirectory: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Partitions) != 1 || status.Partitions[0].Engine.LiveOffers != 0 || status.Partitions[0].Engine.PendingItems != 0 {
		t.Fatalf("inactive confirmation did not withdraw the exact offer: %#v", status.Partitions)
	}
}

func TestRuntimeReviewInactiveObserveOnlyRevokesMemoryWithoutHistoryWrite(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "inactive-observe", "INACTIVE-OBSERVE-CANARY")
	input := f.input(t, root, "one", "read", true, true)
	f.observe(t, input)
	before, err := f.runtime.Query(t.Context(), contextdaemon.QueryRequest{WorkingDirectory: root, Query: contexttrace.Query{Limit: 64}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workbench-context.pkl"), reviewProjectSource(false), 0600); err != nil {
		t.Fatal(err)
	}
	result := f.observe(t, input)
	if result.Decision.State != contextapi.DeliveryWithdrawn {
		t.Fatalf("inactive Observe = %#v, want withdrawn", result.Decision)
	}
	after, err := f.runtime.Query(t.Context(), contextdaemon.QueryRequest{WorkingDirectory: root, Query: contexttrace.Query{Limit: 64}})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Records) != len(before.Records) {
		t.Fatalf("inactive Observe wrote history: before=%d after=%d", len(before.Records), len(after.Records))
	}
}

func TestRuntimeReviewColdInactiveConfirmationDoesNotStartRuntime(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "cold-inactive-confirm", "COLD-INACTIVE-CANARY")
	if err := os.WriteFile(filepath.Join(root, "workbench-context.pkl"), reviewProjectSource(false), 0600); err != nil {
		t.Fatal(err)
	}
	result := f.runtime.Confirm(t.Context(), contextdaemon.ConfirmInput{
		WorkingDirectory: root,
		Identity:         contextapi.OfferIdentity{ID: 1},
	})
	if result.State != contextapi.DeliveryWithdrawn {
		t.Fatalf("cold inactive confirmation = %#v, want withdrawn", result)
	}
	paths := contextdaemon.DefaultPaths(filepath.Join(f.root, "runtime"))
	for _, path := range []string{paths.RuntimeDir, paths.CacheDir} {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("cold inactive confirmation created runtime path %q", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat runtime path %q: %v", path, err)
		}
	}
}

func TestRuntimeReviewUnavailableCacheDoesNotDisableGuidance(t *testing.T) {
	f := newReviewFixture(t, nil, true)
	root := f.project(t, "a", "CACHE_FAILURE_CANARY")
	requireReviewOffer(t, f.observe(t, f.input(t, root, "one", "read-1", true, true)), "CACHE_FAILURE_CANARY")
}

func TestRuntimeReviewResetInAnotherScopeInvalidatesReceivingContextHistory(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	a := f.project(t, "a", "A_CANARY")
	b := f.project(t, "b", "B_CANARY")
	for _, item := range []struct{ root, marker string }{{a, "A_CANARY"}, {b, "B_CANARY"}} {
		offer := requireReviewOffer(t, f.observe(t, f.input(t, item.root, "shared", "read-"+item.marker, true, true)), item.marker)
		if result := f.confirm(t, item.root, offer, time.Now()); result.State != contextapi.DeliveryConfirmed {
			t.Fatalf("confirm: %#v", result)
		}
	}
	reset := f.input(t, b, "shared", "reset", false, false)
	reset.Transition = contextapi.AudienceTransition{Kind: contextapi.TransitionReset, At: time.Now(), CausalID: "reset-one"}
	f.observe(t, reset)
	requireReviewOffer(t, f.observe(t, f.input(t, a, "shared", "read-after-reset", true, true)), "A_CANARY")
}

func TestRuntimeReviewResetThenOrdinaryOfferKeepsProfileIdentity(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "reset", "RESET_CANARY")

	first := requireReviewOffer(t, f.observe(t, f.input(t, root, "shared", "first", true, true)), "RESET_CANARY")
	if result := f.confirm(t, root, first, time.Now()); result.State != contextapi.DeliveryConfirmed {
		t.Fatalf("initial confirm: %#v", result)
	}

	reset := f.input(t, root, "shared", "reset", false, false)
	reset.Transition = contextapi.AudienceTransition{Kind: contextapi.TransitionReset, At: time.Now(), CausalID: "reset"}
	f.observe(t, reset)
	afterReset := requireReviewOffer(t, f.observe(t, f.input(t, root, "shared", "after-reset", true, true)), "RESET_CANARY")
	if result := f.confirm(t, root, afterReset, time.Now()); result.State != contextapi.DeliveryConfirmed {
		t.Fatalf("post-reset confirm: %#v", result)
	}
}

func TestRuntimeReviewRetainedHostFactsAreIndependent(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "clone-host", "CLONE-HOST-CANARY")
	raw := []byte("host-fact-original")
	aliased := unsafe.String(&raw[0], len(raw))
	input := f.input(t, root, "one", "read", true, true)
	input.Host.Facts = []contextapi.Fact{{Key: aliased, Value: contextapi.FactValue{Kind: contextapi.FactText, Text: aliased}, Origin: contextapi.FactHost}}
	offer := requireReviewOffer(t, f.observe(t, input), "CLONE-HOST-CANARY")
	raw[0] = 'X'
	if result := f.confirm(t, root, offer, time.Now()); result.State != contextapi.DeliveryConfirmed {
		t.Fatalf("caller mutation changed retained host/profile evidence: %#v", result)
	}
}

func TestRuntimeReviewStatusScopesItsPartitions(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	a := f.project(t, "a", "A_CANARY")
	b := f.project(t, "b", "B_CANARY")
	f.observe(t, f.input(t, a, "one", "a", true, true))
	f.observe(t, f.input(t, b, "two", "b", true, true))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	status, err := f.runtime.Status(ctx, contextdaemon.StatusRequest{WorkingDirectory: a})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Partitions) != 1 || status.Partitions[0].Scope.CanonicalRoot != a {
		t.Fatalf("unscoped status: %#v", status.Partitions)
	}
}

func TestRuntimeReviewScopedStatusSuppressesTraceStatsForInvalidCurrentPolicy(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "stale-status", "STALE_STATUS_CANARY")
	f.observe(t, f.input(t, root, "one", "read", true, true))
	if err := os.WriteFile(f.home, []byte("not valid pkl"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{WorkingDirectory: root})
	if err != nil {
		t.Fatal(err)
	}
	if status.Trace.DiskCap != 0 || status.Trace.DiskBytes != 0 || status.Trace.BlockCount != 0 {
		t.Fatalf("scoped status reported old trace stats under invalid policy: %#v", status.Trace)
	}
	if !strings.Contains(strings.ToLower(fmt.Sprint(status.Reasons)), "policy") {
		t.Fatalf("scoped status omitted current-policy diagnostic: %#v", status.Reasons)
	}
}

func TestRuntimeReviewConcurrentCloseHasOneOwner(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "a", "CLOSE_CANARY")
	f.observe(t, f.input(t, root, "one", "read-1", true, true))
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() { _ = f.runtime.Close() })
	}
	group.Wait()
}

func TestRuntimeReviewGlobalPendingItemsBoundAcrossAudiences(t *testing.T) {
	f := newReviewFixture(t, map[string]any{"runtime": map[string]any{"maxPendingItems": 1}}, false)
	root := f.project(t, "a", "GLOBAL-BOUND-CANARY")
	for _, session := range []string{"one", "two", "three"} {
		input := f.input(t, root, session, "queue-"+session, true, false)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		_, _ = f.runtime.Observe(ctx, input) // A bounded rejection is an acceptable admission outcome.
		cancel()
	}
	status, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var items uint32
	for _, part := range status.Partitions {
		items += part.Engine.PendingItems
	}
	if items > 1 {
		t.Fatalf("home global pending item cap=1, retained=%d across %d partitions", items, len(status.Partitions))
	}
}

func TestRuntimeReviewGlobalRetainedBytesIncludesAdmissionMetadata(t *testing.T) {
	for _, capBytes := range []uint64{1500, 3000, 5000} {
		t.Run(fmt.Sprint(capBytes), func(t *testing.T) {
			f := newReviewFixture(t, map[string]any{"runtime": map[string]any{"maxPendingMemoryBytes": capBytes}}, false)
			root := f.project(t, "a", strings.Repeat("B", 100))
			for i := 0; i < 8; i++ {
				input := f.input(t, root, fmt.Sprint(i), "queue", true, false)
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				_, _ = f.runtime.Observe(ctx, input)
				cancel()
				status, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
				if err != nil {
					t.Fatal(err)
				}
				var bytes uint64
				for _, part := range status.Partitions {
					bytes += part.Engine.RetainedBytes
				}
				if bytes > capBytes {
					t.Fatalf("global retained byte cap=%d exceeded by engine data alone: %d after audience %d", capBytes, bytes, i)
				}
			}
		})
	}
}

func TestRuntimeReviewGlobalReceiptBoundAppliesAtConfirmation(t *testing.T) {
	f := newReviewFixture(t, map[string]any{"runtime": map[string]any{"maxReceipts": 1}}, false)
	root := f.project(t, "a", "RECEIPT-CANARY")
	for _, session := range []string{"one", "two", "three"} {
		result := f.observe(t, f.input(t, root, session, "read", true, true))
		if result.Decision.Offer.Identity.ID != 0 {
			f.confirm(t, root, result.Decision.Offer, time.Now())
		}
	}
	status, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var receipts uint32
	for _, part := range status.Partitions {
		receipts += part.Engine.Receipts
	}
	if receipts > 1 {
		t.Fatalf("global receipts cap=1 exceeded: %d", receipts)
	}
}

func TestRuntimeReviewTracePreservesWholeContributionSampling(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	body := "HEAD-CANARY" + strings.Repeat("界", 7000) + "TAIL-CANARY"
	root := f.project(t, "a", body)
	f.observe(t, f.input(t, root, "one", "large", true, false))
	query, err := f.runtime.Query(t.Context(), contextdaemon.QueryRequest{WorkingDirectory: root, Query: contexttrace.Query{Kind: contexttrace.KindContribution, Limit: 16, MaxBytes: 256000}})
	if err != nil {
		t.Fatal(err)
	}
	if len(query.Records) != 1 {
		t.Fatalf("one accepted contribution must be queryable after Observe: records=%d gaps=%#v", len(query.Records), query.Gaps)
	}
	sample := query.Records[0].Sample
	if sample.OriginalBytes != uint64(len(body)) {
		t.Fatalf("original bytes=%d want %d", sample.OriginalBytes, len(body))
	}
	var joined string
	var sampled int
	for _, excerpt := range sample.Excerpts {
		if !utf8.ValidString(excerpt.Text) {
			t.Fatal("invalid UTF-8 excerpt")
		}
		sampled += len(excerpt.Text)
		joined += excerpt.Text
	}
	if sampled > 10000 || !strings.Contains(joined, "HEAD-CANARY") || !strings.Contains(joined, "TAIL-CANARY") {
		t.Fatalf("gist lost head/tail or exceeded10k: bytes=%d", sampled)
	}
}

func TestRuntimeReviewTraceKeepsObservationIDNumeric(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "a", "NUMERIC-OBSERVATION-CANARY")
	input := f.input(t, root, "one", "numeric-observation", true, false)
	input.Observation.ID = 42
	f.observe(t, input)

	result, err := f.runtime.Query(t.Context(), contextdaemon.QueryRequest{WorkingDirectory: root, Query: contexttrace.Query{Kind: contexttrace.KindObservation, Limit: 8}})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range result.Records {
		for _, item := range record.Inputs {
			if item.Key != "observation.id" {
				continue
			}
			if item.Value.Kind != contexttrace.ValueUint64 || item.Value.Uint64 != 42 {
				t.Fatalf("observation.id trace input = %#v, want uint64(42)", item.Value)
			}
			return
		}
	}
	t.Fatal("observation.id trace input was not retained")
}

func TestRuntimeReviewClearOrdersAfterPreviouslyAcceptedTrace(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "a", "CLEAR-ORDER-CANARY")
	for i := 0; i < 20; i++ {
		f.observe(t, f.input(t, root, "one", fmt.Sprint(i), true, false))
		if _, err := f.runtime.Clear(t.Context(), root); err != nil {
			t.Fatal(err)
		}
		result, err := f.runtime.Query(t.Context(), contextdaemon.QueryRequest{WorkingDirectory: root, Query: contexttrace.Query{Limit: 100}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Records) != 0 {
			t.Fatalf("pre-clear evidence reappeared after clear returned: %d records on iteration%d", len(result.Records), i)
		}
	}
}

func TestRuntimeReviewServeCancellationJoinsPartialRequestConnection(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	listener, err := net.Listen("unix", filepath.Join(f.root, "partial.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.runtime.ServeListener(ctx, listener) }()
	conn, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0",`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		_ = conn.Close()
		t.Fatal("ServeListener did not join partial-request connection on cancellation")
	}
}

func TestRuntimeReviewServeHonorsConfiguredIdleWithoutInspection(t *testing.T) {
	f := newReviewFixture(t, map[string]any{"runtime": map[string]any{"idleTTLMs": 100}}, false)
	root := f.project(t, "idle", "IDLE-SERVE-CANARY")
	// Serve has no cwd from which to establish policy. One accepted ordinary
	// hook request establishes the real home policy before the server's idle
	// loop; the resulting partition is then passively evicted by that policy.
	result := f.observe(t, f.input(t, root, "idle-session", "idle-policy", true, false))
	if result.Activation.State != contextapi.ActivationEnabled {
		t.Fatalf("policy-establishing observe was not accepted: %#v", result.Activation)
	}
	listener, err := net.Listen("unix", filepath.Join(f.root, "idle.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.runtime.ServeListener(ctx, listener) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("configured100ms idle runtime stayed resident after policy-establishing hook")
	}
}

func TestRuntimeReviewOffersFromDifferentPartitionsConfirmIndependently(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "a", "PARTITION-CANARY")
	first := requireReviewOffer(t, f.observe(t, f.input(t, root, "one", "read-one", true, true)), "PARTITION-CANARY")
	second := requireReviewOffer(t, f.observe(t, f.input(t, root, "two", "read-two", true, true)), "PARTITION-CANARY")
	for _, offer := range []contextapi.Offer{first, second} {
		result := f.confirm(t, root, offer, time.Now())
		if result.State != contextapi.DeliveryConfirmed {
			t.Fatalf("exact offer for %s failed after other partition offered ID%d: %#v", offer.Identity.Audience.ID, offer.Identity.ID, result)
		}
	}
}

func TestRuntimeReviewInactiveConfirmationDoesNotWithdrawForeignItems(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "inactive-foreign", "INACTIVE-FOREIGN-CANARY")
	first := requireReviewOffer(t, f.observe(t, f.input(t, root, "one", "read-one", true, true)), "INACTIVE-FOREIGN-CANARY")
	writeReviewManifest(t, root, "INACTIVE-FOREIGN-SECOND-CANARY")
	second := requireReviewOffer(t, f.observe(t, f.input(t, root, "two", "read-two", true, true)), "INACTIVE-FOREIGN-SECOND-CANARY")
	if err := os.WriteFile(filepath.Join(root, "workbench-context.pkl"), reviewProjectSource(false), 0600); err != nil {
		t.Fatal(err)
	}
	foreign := first
	foreign.Identity.Items = second.Identity.Items
	result := f.confirm(t, root, foreign, time.Now())
	if result.State == contextapi.DeliveryConfirmed {
		t.Fatalf("inactive foreign confirmation was accepted: %#v", result)
	}
	status, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{WorkingDirectory: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Partitions) != 2 {
		t.Fatalf("unexpected partition status: %#v", status.Partitions)
	}
	for _, partition := range status.Partitions {
		if partition.Engine.LiveOffers != 1 {
			t.Fatalf("foreign inactive confirmation withdrew a live offer: %#v", status.Partitions)
		}
	}
}

func TestRuntimeReviewConfirmPreservesOtherPendingSources(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	root := f.project(t, "a", "REPLACED")
	bodyA := "FIRST-" + strings.Repeat("A", 400)
	bodyB := "SECOND-" + strings.Repeat("B", 400)
	manifest := "---\nroot=true\n[[docs]]\nfiles=[\"README.md\"]\nmessage=\"" + bodyA + "\"\n[[docs]]\nfiles=[\"README.md\"]\nmessage=\"" + bodyB + "\"\n---\n"
	if err := os.WriteFile(filepath.Join(root, "ai-context.md"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	input := f.input(t, root, "one", "read-two-sources", true, true)
	input.Opportunity.MaxBytes = 600
	result := f.observe(t, input)
	if len(result.Decision.Offer.Identity.Items) != 1 {
		t.Fatalf("expected one of two bounded sources offered: %#v", result.Decision)
	}
	confirm := f.confirm(t, root, result.Decision.Offer, time.Now())
	if confirm.State != contextapi.DeliveryConfirmed {
		t.Fatalf("unrelated pending source prevented exact confirmation: %#v", confirm)
	}
	status, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Partitions) != 1 || status.Partitions[0].Engine.PendingItems != 1 {
		t.Fatalf("confirmation lost unrelated pending source: %#v", status.Partitions)
	}
}

func TestRuntimeReviewIdleReleasesPendingAndUnconfirmedOffers(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(fmt.Sprint(available), func(t *testing.T) {
			f := newReviewFixture(t, map[string]any{"runtime": map[string]any{"idleTTLMs": 100}}, false)
			root := f.project(t, "a", "IDLE-PENDING-CANARY")
			f.observe(t, f.input(t, root, "one", "read", true, available))
			listener, err := net.Listen("unix", filepath.Join(f.root, "pending-idle.sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- f.runtime.ServeListener(ctx, listener) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatal("unconfirmed or queued context pinned idle runtime despite100ms TTL")
			}
		})
	}
}

func TestRuntimeReviewIdleEvictionExplainsConfirmedPartition(t *testing.T) {
	f := newReviewFixture(t, map[string]any{"runtime": map[string]any{"idleTTLMs": 100}}, false)
	root := f.project(t, "confirmed-idle", "CONFIRMED-IDLE-CANARY")
	offer := requireReviewOffer(t, f.observe(t, f.input(t, root, "one", "read", true, true)), "CONFIRMED-IDLE-CANARY")
	if result := f.confirm(t, root, offer, time.Now()); result.State != contextapi.DeliveryConfirmed {
		t.Fatalf("confirm: %#v", result)
	}
	time.Sleep(150 * time.Millisecond)
	listener, err := net.Listen("unix", filepath.Join(f.root, "confirmed-idle.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.runtime.ServeListener(ctx, listener) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("confirmed-only partition stayed resident past idle TTL")
	}
	status, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Partitions) != 0 {
		t.Fatalf("confirmed partition was not evicted: %#v", status.Partitions)
	}
	result, err := f.runtime.Query(t.Context(), contextdaemon.QueryRequest{WorkingDirectory: f.root, Query: contexttrace.Query{Kind: contexttrace.KindWithdrawn, Limit: 32}})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range result.Records {
		if record.Profile == "" {
			continue
		}
		hasSummary := false
		counts := make(map[string]int64, 3)
		for _, reason := range record.Reasons {
			for _, parameter := range reason.Params {
				switch parameter.Key {
				case "_reason.summary":
					hasSummary = strings.Contains(parameter.Value.String, "idle TTL expired")
				case "idle.pendingItems", "idle.liveOffers", "idle.receipts":
					if parameter.Value.Kind != contexttrace.ValueInt64 {
						t.Fatalf("idle count %s was not numeric: %#v", parameter.Key, parameter.Value)
					}
					counts[parameter.Key] = parameter.Value.Int64
				}
			}
		}
		if hasSummary && len(counts) == 3 && counts["idle.pendingItems"] == 0 && counts["idle.liveOffers"] == 0 && counts["idle.receipts"] == 1 {
			return
		}
	}
	t.Fatalf("confirmed-only idle eviction lacked profile/disposition history: %#v", result.Records)
}

func TestRuntimeReviewHookDeadlineBoundsProviderWork(t *testing.T) {
	f := newReviewFixtureOptions(t, nil, false, contextdaemon.RuntimeOptions{HookDeadline: 25 * time.Millisecond, WholeHookDeadline: 500 * time.Millisecond})
	script := filepath.Join(f.root, "deadline-provider.sh")
	log := filepath.Join(f.root, "provider.log")
	closing := filepath.Join(f.root, "closing")
	release := filepath.Join(f.root, "release")
	writeReviewExecutableProvider(t, script)
	root := f.executableProject(t, "deadline-provider", "unused", script, []string{log, closing, release, "false", "1"})

	started := time.Now()
	result, err := f.runtime.Observe(context.Background(), f.input(t, root, "deadline-session", "deadline-read", true, false))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Observe exceeded bounded hook deadline: %s", elapsed)
	}
	if result.Decision.Offer.Identity.ID != 0 {
		t.Fatalf("deadline-canceled provider unexpectedly offered guidance: %#v", result.Decision)
	}
}

func TestRuntimeReviewCurrentProviderEvidenceIsValidatedOnce(t *testing.T) {
	f := newReviewFixture(t, nil, false)
	script := filepath.Join(f.root, "current-provider.sh")
	log := filepath.Join(f.root, "provider.log")
	closing := filepath.Join(f.root, "closing")
	release := filepath.Join(f.root, "release")
	writeReviewExecutableProvider(t, script)
	root := f.executableProject(t, "current-provider", "unused", script, []string{log, closing, release, "false", "0.05"})

	started := time.Now()
	result, err := f.runtime.Observe(context.Background(), f.input(t, root, "current-provider-session", "current-provider-read", true, true))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.State != contextapi.DeliveryOffered || !strings.Contains(result.Decision.Offer.Body, "PROVIDER_GUIDANCE") {
		t.Fatalf("current provider evidence did not produce an offer: %#v", result.Decision)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), `"method":"context.contribute"`); count != 1 {
		t.Fatalf("current Observe revalidated provider evidence %d times, want once; log=%s", count, contents)
	}
	t.Logf("external-provider Observe latency with current evidence: %s (one contribute call)", elapsed)
}
func TestRuntimeReviewOversizedBuiltinDoesNotSuppressIndependentProvider(t *testing.T) {
	f := newReviewFixture(t, map[string]any{"delivery": map[string]any{"queue": map[string]any{"maxItemBytes": 128}}}, false)
	script := filepath.Join(f.root, "mixed-provider.sh")
	log := filepath.Join(f.root, "provider.log")
	closing := filepath.Join(f.root, "closing")
	release := filepath.Join(f.root, "release")
	writeReviewExecutableProvider(t, script)
	root := f.executableProject(t, "mixed-provider", "unused", script, []string{log, closing, release, "false"})
	if err := os.WriteFile(filepath.Join(root, "workbench-context.pkl"), reviewMixedProjectSource(true, script, []string{log, closing, release, "false"}), 0600); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("OVERSIZED_BUILTIN_", 64)
	manifest := "---\nroot=true\n[[docs]]\nfiles=[\"README.md\"]\nmessage=\"" + body + "\"\n---\n"
	if err := os.WriteFile(filepath.Join(root, "ai-context.md"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := f.runtime.Observe(context.Background(), f.input(t, root, "mixed-provider-session", "mixed-provider-read", true, true))
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.State != contextapi.DeliveryOffered || !strings.Contains(result.Decision.Offer.Body, "PROVIDER_GUIDANCE") {
		t.Fatalf("valid independent provider was suppressed by oversized builtin: %#v", result.Decision)
	}
	if strings.Contains(result.Decision.Offer.Body, body) {
		t.Fatal("oversized builtin body reached the offer")
	}
	var oversized bool
	for _, reason := range result.Decision.Reasons {
		if reason.Code == contextapi.ReasonQueueFull && reason.Provider == "ai-context" {
			oversized = true
		}
	}
	if !oversized {
		t.Fatalf("oversized builtin was not attributed in reasons: %#v", result.Decision.Reasons)
	}
}
func TestRuntimeReviewIdleExecutableProviderReceivesShutdown(t *testing.T) {
	f := newReviewFixture(t, map[string]any{"runtime": map[string]any{"idleTTLMs": 100}}, false)
	script := filepath.Join(f.root, "provider.sh")
	log := filepath.Join(f.root, "provider.log")
	closing := filepath.Join(f.root, "closing")
	release := filepath.Join(f.root, "release")
	writeReviewExecutableProvider(t, script)
	root := f.executableProject(t, "provider", "unused", script, []string{log, closing, release, "false"})

	result := f.observe(t, f.input(t, root, "provider-session", "provider-read", true, false))
	if result.Decision.Offer.Identity.ID != 0 {
		t.Fatalf("unavailable delivery unexpectedly offered provider guidance: %#v", result.Decision)
	}
	healthy, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if healthy.ProviderProcesses != 1 || len(healthy.Partitions) != 1 || healthy.Partitions[0].Engine.PendingItems == 0 {
		t.Fatalf("provider contribution was not healthy and queued before idle: %#v", healthy)
	}

	time.Sleep(150 * time.Millisecond)
	after, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if after.ProviderProcesses != 0 {
		t.Fatalf("idle provider remained resident: %#v", after)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"method":"shutdown"`) {
		t.Fatalf("idle provider did not receive graceful shutdown: %s", contents)
	}
}

func TestRuntimeReviewClosingProviderStillConsumesGlobalProcessCapacity(t *testing.T) {
	f := newReviewFixture(t, map[string]any{"runtime": map[string]any{"idleTTLMs": 50, "maxProviderProcesses": 1}}, false)
	script := filepath.Join(f.root, "slow-provider.sh")
	log := filepath.Join(f.root, "provider.log")
	closing := filepath.Join(f.root, "closing")
	release := filepath.Join(f.root, "release")
	writeReviewExecutableProvider(t, script)
	firstRoot := f.executableProject(t, "first-provider", "unused", script, []string{log, closing, release, "true"})
	secondRoot := f.executableProject(t, "second-provider", "unused", script, []string{log, closing, release, "true"})

	first := f.observe(t, f.input(t, firstRoot, "first-session", "first-read", true, false))
	if first.Decision.Offer.Identity.ID != 0 {
		t.Fatalf("unavailable delivery unexpectedly offered first provider guidance: %#v", first.Decision)
	}
	healthy, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if healthy.ProviderProcesses != 1 || len(healthy.Partitions) != 1 || healthy.Partitions[0].Engine.PendingItems == 0 {
		t.Fatalf("first provider contribution was not healthy and queued before idle: %#v", healthy)
	}
	time.Sleep(75 * time.Millisecond)
	statusDone := make(chan error, 1)
	go func() {
		_, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
		statusDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(closing); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow provider did not begin shutdown")
		}
		time.Sleep(5 * time.Millisecond)
	}

	second := f.observe(t, f.input(t, secondRoot, "second-session", "second-read", true, false))
	if second.Decision.Offer.Identity.ID != 0 {
		t.Fatalf("replacement provider was admitted while predecessor was closing: %#v", second.Decision)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), `"method":"initialize"`); count != 1 {
		t.Fatalf("maxProviderProcesses=1 admitted %d provider starts during shutdown", count)
	}
	during, err := f.runtime.Status(t.Context(), contextdaemon.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if during.ProviderProcesses != 1 {
		t.Fatalf("status dropped closing provider before Client.Close joined: %#v", during)
	}

	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-statusDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("status did not join slow provider shutdown")
	}
}
