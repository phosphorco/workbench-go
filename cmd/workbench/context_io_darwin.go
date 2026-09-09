//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

func fileStatusFlags(fd int) (int, error) {
	value, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFL), 0)
	if errno != 0 {
		return 0, errno
	}
	return int(value), nil
}

func setFileStatusFlags(fd, flags int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFL), uintptr(flags))
	if errno != 0 {
		return errno
	}
	return nil
}

func waitForFile(ctx context.Context, fd int, write bool) error {
	if fd < 0 || fd >= len((syscall.FdSet{}).Bits)*32 {
		return &HookStreamError{Stream: "hook", Reason: "file descriptor cannot be bounded by select"}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		wait, err := filePollWait(ctx)
		if err != nil {
			return err
		}
		timeout := syscall.NsecToTimeval(wait.Nanoseconds())
		if timeout.Sec == 0 && timeout.Usec == 0 {
			timeout.Usec = 1
		}
		var readSet, writeSet syscall.FdSet
		word := fd / 32
		bit := int32(1) << uint(fd%32)
		if write {
			writeSet.Bits[word] |= bit
		} else {
			readSet.Bits[word] |= bit
		}
		if err := syscall.Select(fd+1, &readSet, &writeSet, nil, &timeout); errors.Is(err, syscall.EINTR) {
			continue
		} else if err != nil {
			return &HookStreamError{Stream: "hook", Reason: fmt.Sprintf("poll stream: %v", err)}
		}
		if (write && writeSet.Bits[word]&bit != 0) || (!write && readSet.Bits[word]&bit != 0) {
			return nil
		}
	}
}

func filePollWait(ctx context.Context) (time.Duration, error) {
	wait := 100 * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok {
		wait = time.Until(deadline)
		if wait <= 0 {
			return 0, context.DeadlineExceeded
		}
		if wait > 100*time.Millisecond {
			wait = 100 * time.Millisecond
		}
	}
	return wait, nil
}
