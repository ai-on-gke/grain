package orchestrator

import (
	"context"
	"time"

	"github.com/bwsalmon/grain/pkg/k8s"
)

// SandboxPreemptionReporter is implemented by a Sandbox the platform can
// take away for its own reasons while it is still in use -- a pod the
// scheduler preempts to make room for a higher-priority one
// (K8sPriorityClasses), or that is evicted or simply vanishes. Asked,
// like SandboxOOMReporter, once a CI job has failed and before its
// sandbox is released, so that a job whose machine was taken from under
// it is retried as infrastructure rather than reported as the tree's
// failure.
//
// Exported because a CI sandbox wraps one of these (daemon.CiSandbox)
// and forwards it.
type SandboxPreemptionReporter interface {
	// SandboxPreempted reports whether this sandbox was taken away, with
	// a sentence saying how grain knows. Best effort: anything it cannot
	// read is "not preempted".
	SandboxPreempted(ctx context.Context) (detail string, preempted bool)
}

// preemptCheckTimeout bounds one SandboxPreempted, which runs on the way
// out of a job that has already failed.
const preemptCheckTimeout = 20 * time.Second

// podPreempted is SandboxPreempted for a sandbox pod of either k8s
// backend.
func podPreempted(ctx context.Context, client k8s.Client, podName string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), preemptCheckTimeout)
	defer cancel()
	how, disrupted, err := client.Disrupted(ctx, podName)
	if err != nil || !disrupted {
		return "", false
	}
	return how, true
}

// SandboxPreempted implements SandboxPreemptionReporter for a pod.
func (s *k8sSandbox) SandboxPreempted(ctx context.Context) (string, bool) {
	return podPreempted(ctx, s.owner.client, s.podName)
}

// SandboxPreempted implements SandboxPreemptionReporter for a granule pod.
func (s *k8sGranuleSandbox) SandboxPreempted(ctx context.Context) (string, bool) {
	return podPreempted(ctx, s.owner.client, s.podName)
}
