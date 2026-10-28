package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestDisabledSandboxesAcquireFails(t *testing.T) {
	t.Parallel()
	sb := NewDisabledSandboxes()
	_, err := sb.Acquire(context.Background(), "test-run", Shape{})
	if err == nil {
		t.Fatal("Acquire = nil, want error when native sandboxes are disabled")
	}
	if !strings.Contains(err.Error(), "native sandboxes are disabled") {
		t.Fatalf("Acquire error = %q, want it to mention native sandboxes are disabled", err)
	}
}

func TestDisabledSandboxesNetworkAndOrphans(t *testing.T) {
	t.Parallel()
	sb := NewDisabledSandboxes()
	if !sb.IsolatesNetwork() {
		t.Error("IsolatesNetwork = false, want true")
	}
	reaped, err := sb.ReapOrphans(context.Background())
	if err != nil || reaped != 0 {
		t.Fatalf("ReapOrphans = (%d, %v), want (0, nil)", reaped, err)
	}
}
