package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// TestCiSandboxExecErrorsAStepWhoseSessionWasCutOff is the installer
// job's "docker create ... exited 1": a `make build` whose exec session
// was torn down part-way used to come back as a step that passed. A
// session that ended without the command's status is the transport
// failing, which errors the job rather than concluding the step.
func TestCiSandboxExecErrorsAStepWhoseSessionWasCutOff(t *testing.T) {
	t.Parallel()
	s := &CiSandbox{
		SandboxName: "ci-cut-off",
		Run: func(context.Context, map[string]any) mcp.Result {
			return mcp.Result{
				Text:    "exit=-1\nstdout:\ngo build ./cmd/grain\nstderr:\n" + mcp.SessionCutOffNotice + "\n",
				IsError: true,
			}
		},
	}
	res, err := s.exec(context.Background(), ci.Command{Script: "make build"}, nil)
	if err == nil || !strings.Contains(err.Error(), "exec session ended") {
		t.Fatalf("exec = (%+v, %v), want a transport error", res, err)
	}
	if !strings.Contains(res.Output, "go build ./cmd/grain") {
		t.Errorf("Output = %q, want the partial output kept", res.Output)
	}
}

// TestCiSandboxExecKeepsAnOrdinaryFailureAStepFailure: a command that
// ran and failed is the step's answer, not a transport error.
func TestCiSandboxExecKeepsAnOrdinaryFailureAStepFailure(t *testing.T) {
	t.Parallel()
	s := &CiSandbox{
		SandboxName: "ci-failed",
		Run: func(context.Context, map[string]any) mcp.Result {
			return mcp.Result{Text: "exit=2\nstdout:\n\nstderr:\nmake: *** Error 2\n", IsError: true}
		},
	}
	res, err := s.exec(context.Background(), ci.Command{Script: "make build"}, nil)
	if err != nil || res.ExitCode != 2 {
		t.Fatalf("exec = (%+v, %v), want exit 2 and no error", res, err)
	}
}
