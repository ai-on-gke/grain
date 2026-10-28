//go:build grain_e2e

package daemon

import (
	"errors"
	"fmt"
	"os"

	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// The browser e2e suite (ui/e2e/global-setup.js) builds grain with the
// grain_e2e tag so a spec that boots a real daemon can dispatch a run
// without a container runtime of its own. See LocalSandboxes.
func init() { LocalSandboxes = newLocalSandboxes }

func newLocalSandboxes(dir string) (orchestrator.Sandboxes, error) {
	if dir == "" {
		return nil, errors.New("-sandbox-dir is required for local e2e sandboxes")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating sandbox directory: %w", err)
	}
	return orchestrator.NewHostSandboxes(dir), nil
}
