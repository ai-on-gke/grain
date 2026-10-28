package daemon

import (
	"context"
	"testing"
	"time"
)

// Run defers the state sync's stop and can return with its own context
// still live (a startup error). The stop must end the loop itself, with
// its final export, rather than wait for a cancellation that never
// comes (docs/test-runs/2026-10-15-k8s-run-hosts-sandboxes.md, F3).
func TestStateSyncStopsWithoutItsParentContextEnding(t *testing.T) {
	finals := make(chan struct{}, 1)
	sync := func(context.Context) (bool, error) { return false, nil }
	syncAll := func(context.Context) (bool, error) { finals <- struct{}{}; return false, nil }
	stop := startStateSync(context.Background(), sync, syncAll, make(chan struct{}))

	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stopping the state sync waited for a parent context that was never cancelled")
	}
	select {
	case <-finals:
	default:
		t.Fatal("the state sync stopped without its final export")
	}
}
