package mcp

import (
	"context"
	"testing"
	"time"
)

// tunnelDownRunner answers every exec the way KubectlExecRunner does once
// its own retries are spent on an API server that cannot dial the node.
type tunnelDownRunner struct{ stderr string }

func (r tunnelDownRunner) Run(context.Context, []string, string) (string, string, int) {
	return "", r.stderr, -1
}

const backendDialFailure = "error: unable to upgrade connection: error dialing backend: dial timeout, backstop\n"

func TestSSHToolsMarkACallTheExecTunnelFailed(t *testing.T) {
	for _, stderr := range []string{backendDialFailure, "partial\n" + SessionCutOffNotice + "\n"} {
		tools := NewSSHSandboxTools(tunnelDownRunner{stderr: stderr}, "/home/debian/work")
		for _, call := range []struct {
			tool string
			args map[string]any
		}{
			{"run_command", map[string]any{"command": "make test"}},
			{"read_file", map[string]any{"file_path": "go.mod"}},
			{"write_file", map[string]any{"file_path": "x.txt", "content": "x"}},
			{"edit_file", map[string]any{"file_path": "x.txt", "old_string": "a", "new_string": "b"}},
		} {
			res := toolNamed(t, tools, call.tool).Handler(context.Background(), call.args)
			if !res.IsError {
				t.Errorf("%s: a call the transport failed is not an error: %q", call.tool, res.Text)
			}
			if !ExecTransportFailed(res.Text) {
				t.Errorf("%s with stderr %q: not marked as an exec-tunnel failure:\n%s", call.tool, stderr, res.Text)
			}
		}
	}
}

func TestExecTransportFailedReadsOnlyGrainsOwnNotice(t *testing.T) {
	// The command ran and said these words itself -- an in-sandbox
	// kubectl against a cluster of its own -- so it has a status.
	ran := formatRunCommandResult(1, "", backendDialFailure, "")
	if ExecTransportFailed(ran) {
		t.Errorf("a command's own kubectl error read as grain's transport failing:\n%s", ran)
	}
	quoted := formatRunCommandResult(0, "grep found: "+execTransportNotice+"\n", "", "")
	if ExecTransportFailed(quoted) {
		t.Errorf("output quoting the notice read as a transport failure:\n%s", quoted)
	}
	// No status, but nothing that names the transport: a runner that
	// could not start the command for a reason of its own.
	if execTransportFailed(-1, "error: pods \"x\" not found") {
		t.Error("a missing pod read as the exec tunnel failing")
	}
	failed := formatRunCommandResult(-1, "", backendDialFailure, execTransportNotice)
	if !ExecTransportFailed(withDeadlineNotice(failed, runDeadlineNotice(3*time.Minute))) {
		t.Errorf("the deadline notice hid the transport notice:\n%s", failed)
	}
}
