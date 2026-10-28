package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"
)

func TestScheduledBackupLoopRunsAndStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(done)
		daemon.RunBackupLoop(ctx, time.Millisecond, func(context.Context) {
			if calls.Add(1) == 2 {
				cancel()
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(daemonWaitLimit):
		t.Fatal("backup loop did not stop after cancellation")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("scheduled backup calls = %d, want 2", got)
	}
}

func TestBackupLoopDoesNotRunBeforeItsFirstInterval(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		daemon.RunBackupLoop(ctx, time.Hour, func(context.Context) { calls.Add(1) })
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(daemonWaitLimit):
		t.Fatal("backup loop did not stop")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("scheduled backup calls = %d before first interval", got)
	}
}

// TestBackupHelpExitsZero pins that `grain backup -h` is a success: the
// usage it asked for is printed, and the flag package's ErrHelp is not
// reported as a failure with exit status 1.
func TestBackupHelpExitsZero(t *testing.T) {
	err := runBackup(context.Background(), []string{"-h"}, io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("runBackup(-h) = %v, want flag.ErrHelp", err)
	}
	if code := backupExitCode(err); code != 0 {
		t.Errorf("backupExitCode(ErrHelp) = %d, want 0", code)
	}
	if code := backupExitCode(errors.New("boom")); code != 1 {
		t.Errorf("backupExitCode(boom) = %d, want 1", code)
	}
	if code := backupExitCode(nil); code != 0 {
		t.Errorf("backupExitCode(nil) = %d, want 0", code)
	}
}
