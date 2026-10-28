package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// This package's tests drive run() with nothing but a -sandbox-dir, and
// dispatch into plain directories under it. See daemon.LocalSandboxes.
func init() {
	daemon.LocalSandboxes = func(dir string) (orchestrator.Sandboxes, error) {
		if dir == "" {
			return nil, errors.New("-sandbox-dir is required for local test sandboxes")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating sandbox directory: %w", err)
		}
		return orchestrator.NewHostSandboxes(dir), nil
	}
}
