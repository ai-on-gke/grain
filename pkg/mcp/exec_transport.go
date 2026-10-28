package mcp

// A sandbox tool call the cluster could not carry.
//
// KubectlExecRunner already runs an exec again when it never reached the
// container -- kubectl's own dial timing out, or the API server failing
// to dial the node's kubelet -- up to kubectlgate.MaxAttempts. A call
// that still fails after those tries, or whose session was cut off
// part-way, is the cluster's exec tunnel failing under a run that had
// already reached its agent: the same weather that fails sandbox setups
// (kubectlgate.IsTransientTransport), arriving mid-run instead. Setup
// failures are recorded on the run itself; these were not recorded
// anywhere, so a tunnel that failed only mid-run raised nothing.
//
// The tools say so on the result, as grain's own trailing notice, which
// does two jobs: the agent is told the cluster failed rather than its
// command, and orchestrator's census can count the call
// (ExecTransportFailed) out of agent.Result, the one place a tool call's
// result survives into the daemon.

import (
	"strings"

	"github.com/bwsalmon/grain/pkg/kubectlgate"
)

// execTransportMarker opens execTransportNotice, and is what
// ExecTransportFailed looks for.
const execTransportMarker = "[grain] The cluster's exec tunnel failed"

// execTransportNotice is the paragraph a tool result ends with when its
// exec never reached the sandbox, or was cut off on the way back.
const execTransportNotice = execTransportMarker + " on this call: the Kubernetes API server could not " +
	"carry it to the sandbox (grain already retried it), or the session was cut off part-way. The " +
	"sandbox itself is not at fault and recreate_sandbox will not help. Wait a minute and try the " +
	"call again; a command that was cut off may have done part of its work."

// execTransportFailed reports whether a runner's answer is the exec
// transport failing rather than anything the command said: no status at
// all (-1), with stderr that names a hop on the way to the container
// failing, or the cut-off notice KubectlExecRunner writes when a session
// ended without the command's exit status.
func execTransportFailed(exitCode int, stderr string) bool {
	if exitCode != -1 {
		return false
	}
	return kubectlgate.IsTransientTransport(stderr) || strings.Contains(stderr, SessionCutOffNotice)
}

// withExecTransportNotice appends execTransportNotice to an error text
// whose exec failed on the transport (execTransportFailed), and returns
// text unchanged otherwise.
func withExecTransportNotice(text string, exitCode int, stderr string) string {
	if !execTransportFailed(exitCode, stderr) {
		return text
	}
	return text + "\n\n" + execTransportNotice
}

// ExecTransportFailed reports whether a sandbox tool's answer is one
// whose exec the cluster could not carry -- execTransportNotice, which
// run_command, read_file, write_file and edit_file append.
//
// Only the trailing paragraphs are searched, for RunCommandTimedOut's
// reason: a command whose output quotes this sentence is not a call
// that failed on the transport.
func ExecTransportFailed(text string) bool {
	for _, para := range trailingParagraphs(text, 3) {
		if strings.HasPrefix(strings.TrimSpace(para), execTransportMarker) {
			return true
		}
	}
	return false
}
