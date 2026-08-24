package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunRejectsCommandsOutsidePublicSurface(t *testing.T) {
	called := false
	err := run(context.Background(), []string{"inspect"}, func() (string, error) {
		called = true
		return "", nil
	}, io.Discard)
	if err == nil || err.Error() != "usage: workbench setup" {
		t.Fatalf("run error = %v, want setup usage", err)
	}
	if called {
		t.Fatal("working directory observed for an invalid command")
	}
}

func TestRunReportsWorkingDirectoryFailure(t *testing.T) {
	want := errors.New("unavailable")
	err := run(context.Background(), []string{"setup"}, func() (string, error) {
		return "", want
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("run error = %v, want error containing %q", err, want)
	}
}
