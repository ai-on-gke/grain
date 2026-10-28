package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/bwsalmon/grain/pkg/k8s"
	"github.com/bwsalmon/grain/pkg/model"
)

// ErrClusterFull is what a Kubernetes backend's Acquire returns, wrapped,
// when the cluster has no node with room for the sandbox pod right now:
// either the fit check refused to create it, or the scheduler went on
// marking it Unschedulable until the wait gave up and released it.
//
// Neither is a fault of the run that asked. A run whose setup fails with
// it is finished as model.CapacityOutcome -- not counted against its
// task's failure streak, but counted for retry backoff -- and a CI job
// waits for room instead of failing. On 2026-10-25 the same pod was a
// setup-failed attempt, and three of them retired a task that had never
// been tried (grain/task-805).
var ErrClusterFull = errors.New("orchestrator: the cluster has no room for this sandbox pod right now")

// ClusterFullError is ErrClusterFull with the pod and the cluster's own
// reason attached.
type ClusterFullError struct {
	Pod    string
	Reason string
	// Cause is the scheduler's refusal this was read from, when there was
	// one (k8s.UnschedulableError); nil when the fit check held the pod
	// back before it was created.
	Cause error
}

func (e *ClusterFullError) Error() string {
	return fmt.Sprintf("the cluster has no room for sandbox pod %q right now: %s", e.Pod, e.Reason)
}

func (e *ClusterFullError) Is(target error) bool { return target == ErrClusterFull }

func (e *ClusterFullError) Unwrap() error { return e.Cause }

// heldBack reports whether err is admitPod holding a pod back before it
// was created, as against a pod that was created and then never
// scheduled.
func heldBack(err error) bool {
	var full *ClusterFullError
	return errors.As(err, &full) && full.Cause == nil
}

// newFitChecker is the fit check both Kubernetes backends hold, reading
// the cluster through the backend's own client.
func newFitChecker(client k8s.Client, nodeSelector map[string]string) *k8s.FitChecker {
	return &k8s.FitChecker{Reader: client, NodeSelector: nodeSelector}
}

// admitPod is the check a Kubernetes backend's Acquire makes before it
// creates a pod: some schedulable node must have room for the pod's
// requests now, on top of what is already bound to it and what this
// backend has created since the cluster was last read. A pod no node
// could ever fit fails at once, in words that say what to change; one
// that does not fit *now* is ErrClusterFull, and nothing is created.
func admitPod(ctx context.Context, fit *k8s.FitChecker, name string, shape k8s.PodShape) error {
	verdict := fit.Check(ctx, shape.Requests())
	switch {
	case verdict.OK:
		return nil
	case verdict.Never:
		return fmt.Errorf("orchestrator: sandbox pod %q can never be scheduled: %s -- %s", name, verdict.Reason, k8s.ShrinkOrAddNode)
	default:
		return &ClusterFullError{Pod: name, Reason: verdict.Reason}
	}
}

// unschedulableOutcome reads a pod's failed readiness wait: a pod the
// scheduler gave up placing is ErrClusterFull -- the cluster was full
// when it was created, which a race with other workloads can make true
// whatever admitPod read -- unless a fresh reading says no node could
// ever take it, which stays the failure it was, so it surfaces rather
// than being retried for ever. Any other failure is returned unchanged.
func unschedulableOutcome(ctx context.Context, fit *k8s.FitChecker, name string, shape k8s.PodShape, err error) error {
	var unschedulable *k8s.UnschedulableError
	if !errors.As(err, &unschedulable) {
		return err
	}
	if verdict := fit.Check(ctx, shape.Requests()); verdict.Known && verdict.Never {
		return err
	}
	return &ClusterFullError{Pod: name, Reason: unschedulable.Reason, Cause: err}
}

// commitmentFits is a Kubernetes backend's model.ClusterFit: whether a
// sandbox committing want, built the way this backend would build it
// (podShape, with its default disk and overcommit), would be scheduled
// now. Answered from the fit check's cached reading. A shape that could
// never fit is answered yes, so its run is admitted and its Acquire
// fails at once with the reason, rather than waiting in a queue.
func commitmentFits(fit *k8s.FitChecker, shape k8s.PodShape) (bool, string) {
	verdict := fit.Peek(shape.Requests())
	if verdict.OK || verdict.Never {
		return true, ""
	}
	return false, verdict.Reason
}

// commitmentShape is the Shape a ledger commitment stands for: its cpu,
// memory and disk, with every zero dimension left to the backend's default.
func commitmentShape(want model.Commitment) Shape {
	return Shape{CPUs: want.CPUs, MemoryMB: want.MemoryMB, DiskGB: want.DiskGB}
}
