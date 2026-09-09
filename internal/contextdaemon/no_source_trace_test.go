package contextdaemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextcache"
	"github.com/phosphorco/workbench-go/internal/contextconfig"
	"github.com/phosphorco/workbench-go/internal/contextdaemon"
)

func TestWarmNoSourceObserveDoesNotCreateTraceStore(t *testing.T) {
	root := t.TempDir()
	paths := contextdaemon.DefaultPaths(filepath.Join(root, "runtime"))
	paths.CacheDir = filepath.Join(root, "cache")
	pool, err := contextcache.Open(paths.CacheDir, contextcache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := contextdaemon.NewRuntime(contextdaemon.RuntimeOptions{
		Paths:            paths,
		LoadDependencies: contextconfig.LoadDependencies{Cache: pool},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.Observe(context.Background(), contextdaemon.ObserveInput{
		WorkingDirectory: root,
		Host:             contextapi.HostSnapshot{Harness: contextapi.HarnessCodex, WorkingDirectory: root},
		Observation:      contextapi.Observation{Audience: contextapi.Audience{ID: "warm-no-source"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Activation.State != contextapi.ActivationInactive {
		t.Fatalf("no-source observe state = %s, want inactive", result.Activation.State)
	}
	if _, err := os.Stat(filepath.Join(paths.CacheDir, "trace")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("warm no-source Observe created trace namespace: %v", err)
	}
}
