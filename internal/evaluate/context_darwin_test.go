//go:build darwin

package evaluate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestContextDataLimitDarwinHelper(t *testing.T) {
	if os.Getenv("CONTEXT_DATA_LIMIT_DARWIN_HELPER") != "1" {
		return
	}
	const limit = uint64(128 << 20)
	if err := setContextDataLimit(limit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var resource syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_DATA, &resource); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if uint64(resource.Cur) != limit || uint64(resource.Max) != limit {
		fmt.Fprintf(os.Stderr, "Darwin data limit = (%d, %d), want (%d, %d)\n", resource.Cur, resource.Max, limit, limit)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestContextDataLimitDarwinUsesNativeBestEffortPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestContextDataLimitDarwinHelper$")
	command.Env = append(os.Environ(), "CONTEXT_DATA_LIMIT_DARWIN_HELPER=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Darwin RLIMIT_DATA worker path failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func TestContextDataLimitDarwinRejectsInvalidLimit(t *testing.T) {
	if err := setContextDataLimit(0); err == nil {
		t.Fatal("zero Darwin data limit was accepted")
	}
	if err := setContextDataLimit(uint64(^uint64(0)>>1) + 1); err == nil {
		t.Fatal("overflow Darwin data limit was accepted")
	}
}
