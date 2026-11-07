//go:build unix

package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestHandleInterrupts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	abort := make(chan struct{})
	stop := handleInterrupts(cancel, abort)
	defer stop()

	interrupt := func() {
		t.Helper()
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
	}

	interrupt()
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the first Ctrl-C didn't cancel the context")
	}
	select {
	case <-abort:
		t.Fatal("the first Ctrl-C closed abort, want only the context cancelled")
	default:
	}

	interrupt()
	select {
	case <-abort:
	case <-time.After(10 * time.Second):
		t.Fatal("the second Ctrl-C didn't close abort")
	}
}
