package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/k8s"
	"github.com/bwsalmon/grain/pkg/model"
)

// fitCluster is a fake kubectl for the fit check: one node with
// allocatableCPU cores of which usedCPU are requested by a pod already
// bound to it, a pod status the test chooses, and a log of every call so
// a test can say whether a pod was ever created.
type fitCluster struct {
	kubectl string
	log     string
}

func newFitCluster(t *testing.T, allocatableCPU, usedCPU, podStatus string) fitCluster {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	nodes := write("nodes", `{"items":[{"metadata":{"name":"n1"},"spec":{},"status":{"allocatable":`+
		`{"cpu":"`+allocatableCPU+`","memory":"64Gi","ephemeral-storage":"500Gi"},`+
		`"conditions":[{"type":"Ready","status":"True"}]}}]}`)
	pods := write("pods", `{"items":[{"metadata":{"name":"other","namespace":"default"},"spec":{"nodeName":"n1",`+
		`"containers":[{"resources":{"requests":{"cpu":"`+usedCPU+`"}}}]},"status":{"phase":"Running"}}]}`)
	status := write("status", podStatus)
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nshift 2\necho \"$*\" >> " + log + "\n" +
		"case \"$1 $2\" in\n" +
		"  'get nodes') cat " + nodes + ";;\n" +
		"  'get pods') cat " + pods + ";;\n" +
		"  'get pod') cat " + status + ";;\n" +
		"  'apply --validate=false') cat >/dev/null;;\n" +
		"esac\nexit 0\n"
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return fitCluster{kubectl: kubectl, log: log}
}

func (c fitCluster) calls(t *testing.T) []string {
	t.Helper()
	data, _ := os.ReadFile(c.log)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (c fitCluster) created(t *testing.T) bool {
	for _, call := range c.calls(t) {
		if strings.HasPrefix(call, "apply") {
			return true
		}
	}
	return false
}

func fitBackend(c fitCluster) *K8sSandboxes {
	return NewK8sSandboxes(K8sConfig{
		Image: "example/sandbox", Kubectl: c.kubectl, Namespace: "grain",
		DefaultCPUs: 4, DefaultMemoryMB: 8192, DefaultDiskGB: 30,
		ReadyTimeout: 5 * time.Second, UnschedulableTimeout: time.Millisecond, ReadyPollInterval: time.Millisecond,
	})
}

func TestAcquireHoldsAPodBackWhileNoNodeHasRoomForIt(t *testing.T) {
	t.Parallel()
	// 16 cores, 14 requested: a 4-core pod does not fit now, though it
	// would on an empty node.
	cluster := newFitCluster(t, "16", "14", "Running|True||")
	_, err := fitBackend(cluster).Acquire(context.Background(), "805-1", Shape{})
	if !errors.Is(err, ErrClusterFull) {
		t.Fatalf("Acquire = %v, want ErrClusterFull", err)
	}
	if !heldBack(err) {
		t.Errorf("heldBack(%v) = false, want true: no pod was created", err)
	}
	if cluster.created(t) {
		t.Errorf("a pod was applied although the cluster had no room: calls %q", cluster.calls(t))
	}
	// And the run that asked is requeued, not failed.
	if outcome, _ := setupFailedEnding(err); outcome != model.CapacityOutcome {
		t.Errorf("setupFailedEnding outcome = %q, want %q", outcome, model.CapacityOutcome)
	}
}

func TestAcquireCreatesThePodOnceANodeHasRoom(t *testing.T) {
	t.Parallel()
	cluster := newFitCluster(t, "16", "4", "Running|True||")
	k := fitBackend(cluster)
	sb, err := k.Acquire(context.Background(), "805-2", Shape{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !cluster.created(t) {
		t.Fatalf("no pod was applied: calls %q", cluster.calls(t))
	}
	// The pod just created is counted against the reading it was
	// admitted on: two more 4-core pods fit in the 12 free cores less
	// this one's 4, a third does not.
	if ok, _ := k.FitsNow(model.Commitment{CPUs: 8}); !ok {
		t.Errorf("FitsNow(8 cores) = false, want true: 8 of 16 cores are still free")
	}
	if ok, reason := k.FitsNow(model.Commitment{CPUs: 9}); ok || !strings.Contains(reason, "no node") {
		t.Errorf("FitsNow(9 cores) = %v %q, want a refusal naming the node shortage", ok, reason)
	}
	if err := sb.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if ok, _ := k.FitsNow(model.Commitment{CPUs: 12}); !ok {
		t.Errorf("FitsNow(12 cores) after Release = false, want the released pod's room back")
	}
}

func TestAcquireRequeuesAPodTheSchedulerLeftUnschedulable(t *testing.T) {
	t.Parallel()
	// Room on the reading, but the scheduler disagrees (a race with
	// another workload): the pod is released and the run requeued.
	cluster := newFitCluster(t, "16", "0", "Pending|||Unschedulable: 0/1 nodes are available: 1 Insufficient cpu.")
	_, err := fitBackend(cluster).Acquire(context.Background(), "805-3", Shape{})
	if !errors.Is(err, ErrClusterFull) {
		t.Fatalf("Acquire = %v, want ErrClusterFull", err)
	}
	if heldBack(err) {
		t.Errorf("heldBack = true, want false: this pod was created and then released")
	}
	deleted := false
	for _, call := range cluster.calls(t) {
		if strings.HasPrefix(call, "delete pod grain-sandbox-805-3") {
			deleted = true
		}
	}
	if !deleted {
		t.Errorf("the unschedulable pod was not deleted: calls %q", cluster.calls(t))
	}
	outcome, detail := setupFailedEnding(err)
	if outcome != model.CapacityOutcome || !strings.Contains(detail, "not counted as failed") {
		t.Errorf("setupFailedEnding = %q %q, want a capacity outcome that says it is not counted", outcome, detail)
	}
}

func TestAcquireFailsAtOnceForAPodNoNodeCouldEverFit(t *testing.T) {
	t.Parallel()
	cluster := newFitCluster(t, "16", "0", "Running|True||")
	k := fitBackend(cluster)
	_, err := k.Acquire(context.Background(), "805-4", Shape{CPUs: 64})
	if err == nil || errors.Is(err, ErrClusterFull) {
		t.Fatalf("Acquire = %v, want a failure that is not ErrClusterFull", err)
	}
	for _, want := range []string{"can never be scheduled", "shrink the sandbox", "add a node"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if cluster.created(t) {
		t.Errorf("a pod no node can fit was applied anyway")
	}
	if outcome, _ := setupFailedEnding(err); outcome != "setup-failed" {
		t.Errorf("setupFailedEnding outcome = %q, want setup-failed: a pod that never fits must surface", outcome)
	}
	// The budget does not hold such a run in the queue either: it is
	// admitted, so that this failure is what the operator sees.
	if ok, _ := k.FitsNow(model.Commitment{CPUs: 64}); !ok {
		t.Errorf("FitsNow(64 cores) = false, want true for a shape no node could ever fit")
	}
}

func TestAnUnschedulablePodThatCanNeverFitStaysAFailure(t *testing.T) {
	t.Parallel()
	// The node shrank (or was replaced) between admission and the
	// scheduler's verdict: a fresh reading says the pod can never fit,
	// so it is not requeued for ever.
	cluster := newFitCluster(t, "2", "0", "")
	k := fitBackend(cluster)
	other := errors.New("an image that will not pull")
	if got := unschedulableOutcome(context.Background(), k.fit, "p", k.podShape(Shape{CPUs: 4}), other); got != other {
		t.Errorf("a failure that is not Unschedulable came back changed: %v", got)
	}
	unsched := fmt.Errorf("waiting: %w", &k8s.UnschedulableError{Pod: "p", For: time.Minute, Reason: "Unschedulable: 1 Insufficient cpu"})
	got := unschedulableOutcome(context.Background(), k.fit, "p", k.podShape(Shape{CPUs: 4}), unsched)
	if errors.Is(got, ErrClusterFull) || !strings.Contains(got.Error(), "shrink the sandbox") {
		t.Errorf("unschedulableOutcome = %v, want the scheduler's failure with its advice, not a requeue", got)
	}
	// The same pod on a node that could take it is a requeue.
	fits := fitBackend(newFitCluster(t, "16", "0", ""))
	if got := unschedulableOutcome(context.Background(), fits.fit, "p", fits.podShape(Shape{CPUs: 4}), unsched); !errors.Is(got, ErrClusterFull) {
		t.Errorf("unschedulableOutcome on a cluster that can fit it = %v, want ErrClusterFull", got)
	}
}
