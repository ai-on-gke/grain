package daemon

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// A CI job's sandbox is the lowest class, so on a full Kubernetes cluster
// it is the pod an interactive session preempts.
func TestCiJobSandboxIsTheCIClass(t *testing.T) {
	t.Parallel()
	got := CiSandboxes{}.jobShape(ci.SandboxRequest{})
	if got.Class != orchestrator.SandboxClassCI {
		t.Errorf("CI job shape class = %q, want %q", got.Class, orchestrator.SandboxClassCI)
	}
}
