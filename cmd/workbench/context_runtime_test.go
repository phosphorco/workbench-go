package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contexttrace"
)

func TestReadHookInputStopsOnUnclosedPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = readHookInput(ctx, reader, 1024)
	if err == nil {
		t.Fatal("unclosed stdin pipe unexpectedly completed")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked stdin took %s", elapsed)
	}
}

func TestWriteHookOutputStopsOnFullPipeWithoutWorker(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd := int(writer.Fd())
	originalFlags, err := fileStatusFlags(fd)
	if err != nil {
		reader.Close()
		writer.Close()
		t.Fatal(err)
	}
	if err := setFileStatusFlags(fd, originalFlags|syscall.O_NONBLOCK); err != nil {
		reader.Close()
		writer.Close()
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	for {
		_, writeErr := syscall.Write(fd, bytes.Repeat([]byte("x"), 32*1024))
		if writeErr == nil {
			continue
		}
		if !errors.Is(writeErr, syscall.EAGAIN) && !errors.Is(writeErr, syscall.EWOULDBLOCK) {
			t.Fatalf("fill pipe: %v", writeErr)
		}
		break
	}
	if err := setFileStatusFlags(fd, originalFlags); err != nil {
		t.Fatal(err)
	}
	flagsBeforeHook, err := fileStatusFlags(fd)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = writeHookOutput(ctx, writer, []byte("offer"))
	if err == nil {
		t.Fatal("blocked stdout pipe unexpectedly completed")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked stdout took %s", elapsed)
	}
	flagsAfterHook, err := fileStatusFlags(fd)
	if err != nil {
		t.Fatal(err)
	}
	if flagsAfterHook != flagsBeforeHook {
		t.Fatalf("stdout descriptor flags changed: before=%#x after=%#x", flagsBeforeHook, flagsAfterHook)
	}
}

func TestHookRegularFilesUseBoundedByteOperations(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "input-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.WriteString(`{"cwd":"/tmp/project","hook_event_name":"PostToolUse"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	inputContext, cancelInput := context.WithTimeout(context.Background(), time.Second)
	data, err := readHookInput(inputContext, input, 1024)
	cancelInput()
	input.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("regular input file was empty")
	}

	output, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	outputContext, cancelOutput := context.WithTimeout(context.Background(), time.Second)
	err = writeHookOutput(outputContext, output, []byte("exact"))
	cancelOutput()
	output.Close()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "exact" {
		t.Fatalf("regular output = %q", encoded)
	}
}

func TestHookOutputRejectsUnboundedWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := writeHookOutput(ctx, io.Discard, []byte("offer"))
	var streamErr *HookStreamError
	if err == nil || !errors.As(err, &streamErr) || !strings.Contains(err.Error(), "bounded deadline capability") {
		t.Fatalf("unbounded writer error = %v", err)
	}
}

func TestTraceHumanOutputIncludesEvidenceAndPagination(t *testing.T) {
	var output bytes.Buffer
	result := contexttrace.QueryResult{
		Generation: 4,
		Records: []contexttrace.Record{{
			ID: 7, Kind: contexttrace.KindContribution, At: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
			Scope: "/tmp/project", Audience: "session-1", Turn: "turn-2", Profile: "profile-3",
			Source: "ai-context", Contributor: "builtin", ContributionID: 9,
			Reasons: []contexttrace.Reason{{Code: 12, Rule: "docs", Provider: "ai-context", Params: []contexttrace.Parameter{{Key: "path", Value: contexttrace.StringValue("README.md")}}}},
			Outcome: contexttrace.OutcomeSuppressed,
			Sample:  contexttrace.Sample{OriginalBytes: 100, OmittedBytes: 92, Excerpts: []contexttrace.Excerpt{{Offset: 4, Bytes: 8, Text: "guidance"}}},
		}},
		HasMore: true, NextAfter: 7, NextCursor: contexttrace.Cursor{BlockSequence: 2, RecordID: 7},
		Gaps: []contexttrace.Gap{{Kind: contexttrace.GapDropped}},
	}
	if err := writeTraceResult(&output, result, contextOptions{}, "history"); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{"Record 7", "suppressed", "Why [12]", "Sample: 8/100 bytes retained", "More records available: --after 7 --cursor 2:7", "Evidence incomplete: dropped"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("trace output missing %q: %s", expected, text)
		}
	}
}

func TestTraceHumanOutputDistinguishesEmptyAndUnavailableHistory(t *testing.T) {
	var empty bytes.Buffer
	if err := writeTraceResult(&empty, contexttrace.QueryResult{}, contextOptions{}, "history"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.String(), "absence is not evidence") {
		t.Fatalf("empty history = %q", empty.String())
	}
	var unavailable bytes.Buffer
	if err := writeTraceResult(&unavailable, contexttrace.QueryResult{Gaps: []contexttrace.Gap{{Kind: contexttrace.GapRotated}, {Kind: contexttrace.GapDropped}}}, contextOptions{}, "history"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unavailable.String(), "rotated, dropped") {
		t.Fatalf("unavailable history = %q", unavailable.String())
	}
}
