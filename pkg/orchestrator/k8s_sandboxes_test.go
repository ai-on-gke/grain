// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestPodNameForNamesTheRun(t *testing.T) {
	t.Parallel()
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox"})
	name, err := k.PodNameFor("358-1")
	if err != nil {
		t.Fatalf("PodNameFor: %v", err)
	}
	if name != PodNamePrefix+"358-1" {
		t.Errorf("PodNameFor = %q, want the run's name behind %q", name, PodNamePrefix)
	}
}

func TestPodNameForRefusesANameKubernetesWould(t *testing.T) {
	t.Parallel()
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox"})
	// Refused here rather than several layers down in a message about
	// DNS label syntax that says nothing about the run it came from.
	for _, sandbox := range []string{
		strings.Repeat("9", maxPodNameLen), // too long
		"Upper-1",                          // a pod name is lowercase
		"7_1",                              // underscores are not DNS-1123
		"7-1-",                             // may not end in a dash
	} {
		if name, err := k.PodNameFor(sandbox); err == nil {
			t.Errorf("PodNameFor(%q) = %q, want an error", sandbox, name)
		}
	}
}

// TestAcquireRefusesNetworkNoneWithoutAnEnforcingCluster is the whole
// reason K8sConfig.NetworkPolicyEnforced exists: a policy object is
// accepted by every cluster and enforced by only some, so a deployment
// that has not said its own does gets a refusal rather than a task
// silently handed the internet it was promised it would not have.
func TestAcquireRefusesNetworkNoneWithoutAnEnforcingCluster(t *testing.T) {
	t.Parallel()
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox"})
	_, err := k.Acquire(context.Background(), "7-1", Shape{NetworkAccess: model.NetworkNone})
	if err == nil {
		t.Fatal("Acquire with network none on a cluster that may not enforce it succeeded")
	}
	if !strings.Contains(err.Error(), "-k8s-network-policy") {
		t.Errorf("error = %q, want it to name the flag that turns this on", err)
	}
}

// TestAcquireRefusesNetworkNoneFromTheDeploymentDefaultToo: the refusal
// has to happen after the deployment-wide default has had its say, or a
// deployment that set network none in Settings would quietly get open
// sandboxes for every task that named nothing itself.
func TestAcquireRefusesNetworkNoneFromTheDeploymentDefaultToo(t *testing.T) {
	t.Parallel()
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox", DefaultNetworkAccess: model.NetworkNone})
	if _, err := k.Acquire(context.Background(), "7-1", Shape{}); err == nil {
		t.Fatal("Acquire with a deployment-wide network none succeeded")
	}
}

func TestPodOptionsLabelANetworkNoneSandboxForThePolicy(t *testing.T) {
	t.Parallel()
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox", NetworkPolicyEnforced: true})
	// The label is the only thing standing between such a task and the
	// internet: deploy/kubernetes/networkpolicy.yaml selects on exactly
	// it.
	opts := k.podOptions(Shape{NetworkAccess: model.NetworkNone})
	if opts.Labels[NetworkLabel] != NetworkNoneValue {
		t.Errorf("labels = %v, want %s=%s", opts.Labels, NetworkLabel, NetworkNoneValue)
	}
	if opts := k.podOptions(Shape{NetworkAccess: model.NetworkOpen}); opts.Labels[NetworkLabel] != "" {
		t.Errorf("an open sandbox is labelled %v, which the deny policy would select", opts.Labels)
	}
}

func TestPodShapeFillsEveryDimensionNobodyNamed(t *testing.T) {
	t.Parallel()
	// A dimension nobody chose still reaches the cluster as a number,
	// rather than as an absent request a namespace's LimitRange fills
	// with whatever it likes.
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox"})
	got := k.podShape(Shape{})
	want := DefaultShape()
	if got != (k8s.PodShape{CPUs: want.CPUs, MemoryMB: want.MemoryMB, DiskGB: want.DiskGB}) {
		t.Errorf("podShape of an unset shape = %+v, want grain's own DefaultShape %+v", got, want)
	}
}

func TestPodShapeTakesTheDeploymentDefaultPerDimension(t *testing.T) {
	t.Parallel()
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox", DefaultCPUs: 8, DefaultMemoryMB: 16384})
	// The task's own CPU request wins; the memory it said nothing about
	// comes from the deployment; the disk neither named falls through to
	// grain's own default.
	got := k.podShape(Shape{CPUs: 2}.orDefault(k.defaultShape()))
	if got.CPUs != 2 {
		t.Errorf("CPUs = %d, want the task's own 2", got.CPUs)
	}
	if got.MemoryMB != 16384 {
		t.Errorf("MemoryMB = %d, want the deployment's own 16384", got.MemoryMB)
	}
	if got.DiskGB != DefaultShape().DiskGB {
		t.Errorf("DiskGB = %d, want grain's own default %d", got.DiskGB, DefaultShape().DiskGB)
	}
}

func TestSetDefaultShapeReachesTheNextSandbox(t *testing.T) {
	t.Parallel()
	// A change in Settings has to reach the next sandbox built rather
	// than the next restart, which is what makes this backend behave
	// like the granule one under liveConfig.
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox", DefaultCPUs: 1})
	k.SetDefaultShape(Shape{CPUs: 6, MemoryMB: 2048, DiskGB: 10})
	if got := k.podShape(Shape{}.orDefault(k.defaultShape())); got.CPUs != 6 {
		t.Errorf("CPUs after SetDefaultShape = %d, want 6", got.CPUs)
	}
	var _ interface{ SetDefaultShape(Shape) } = k
}

func TestK8sSandboxesSetOvercommitReachesPodShape(t *testing.T) {
	t.Parallel()
	k := NewK8sSandboxes(K8sConfig{
		Image:            "example/sandbox",
		DefaultCPUs:      8,
		DefaultMemoryMB:  16384,
		DefaultDiskGB:    30,
		CPUOvercommit:    10,
		MemoryOvercommit: 10,
	})
	got := k.podShape(Shape{}.orDefault(k.defaultShape()))
	if got.CPUOvercommit != 10 || got.MemoryOvercommit != 10 {
		t.Fatalf("initial podShape overcommit = %v/%v, want 10/10", got.CPUOvercommit, got.MemoryOvercommit)
	}
	k.SetOvercommit(4, 2, 5)
	got = k.podShape(Shape{}.orDefault(k.defaultShape()))
	if got.CPUOvercommit != 4 || got.MemoryOvercommit != 2 || got.DiskOvercommit != 5 {
		t.Fatalf("updated podShape overcommit = %v/%v/%v, want 4/2/5", got.CPUOvercommit, got.MemoryOvercommit, got.DiskOvercommit)
	}
	var _ interface {
		SetOvercommit(cpu, memory, disk float64)
	} = k
}

// TestK8sSandboxesAnswersTheSameInterfacesGranuleDoes pins the set of
// optional interfaces a Sandboxes/Sandbox pair has to answer for
// everything above Deps.Sandboxes to work: without SandboxPlacer, every
// grant that materializes a sandbox-side file fails during preparation,
// which is exactly the gap granule VMs had before they grew one.
func TestK8sSandboxesAnswersTheSameInterfacesGranuleDoes(t *testing.T) {
	t.Parallel()
	var sandboxes Sandboxes = NewK8sSandboxes(K8sConfig{Image: "example/sandbox"})
	if _, ok := sandboxes.(interface {
		ReapOrphans(context.Context) (int, error)
	}); !ok {
		t.Error("K8sSandboxes has no startup orphan sweep, so pods a killed process left behind are never reaped")
	}
	if _, ok := sandboxes.(interface {
		SweepOrphans(context.Context) (int, error)
	}); !ok {
		t.Error("K8sSandboxes has no live orphan sweep, so a pod whose delete failed holds its node until the next restart")
	}
	var sandbox Sandbox = &k8sSandbox{}
	if _, ok := sandbox.(SandboxPlacer); !ok {
		t.Error("k8sSandbox is not a SandboxPlacer: every capability placement would fail during preparation")
	}
	if _, ok := sandbox.(SandboxRebuilder); !ok {
		t.Error("k8sSandbox is not a SandboxRebuilder: recreate_sandbox would be refused on this backend alone")
	}
	if _, ok := sandbox.(SandboxShell); !ok {
		t.Error("k8sSandbox is not a SandboxShell: persistent sandboxes and streaming CI steps would fail on this backend")
	}
	if _, ok := sandbox.(SandboxPortDialer); !ok {
		t.Error("k8sSandbox is not a SandboxPortDialer: persistent sandbox port forwarding would fail on this backend")
	}
	if _, ok := sandbox.(SandboxRootless); !ok {
		t.Error("k8sSandbox is not a SandboxRootless: every dispatch would log its CA, DNS and package-cache " +
			"writes failing with \"sudo: not found\"")
	}
	// Deliberately *not* a rootedSandbox: its filesystem is inside a pod
	// this process can only reach by exec'ing into it, so a local
	// directory would only ever be a staging copy on the wrong machine.
	if _, ok := sandbox.(rootedSandbox); ok {
		t.Error("k8sSandbox claims a local root it does not have")
	}
}

// A framework that forks its own mcpserver (claude, codex, antigravity)
// reaches a sandbox pod through podNamedSandbox; without it every such
// run on -k8s-sandboxes failed before the agent started
// (docs/test-runs/2026-10-15-k8s-run-hosts-sandboxes.md, F1).
func TestK8sSandboxNamesItsPodToForkedMCPServers(t *testing.T) {
	t.Parallel()
	k := NewK8sSandboxes(K8sConfig{Image: "example/sandbox", Namespace: "walk", Workspace: "/home/agent"})
	var sandbox Sandbox = &k8sSandbox{owner: k, name: "7-1", podName: "grain-sandbox-7-1"}
	named, ok := sandbox.(podNamedSandbox)
	if !ok {
		t.Fatal("k8sSandbox is not a podNamedSandbox: claude, codex and antigravity runs have no way into it")
	}
	got := named.KubePod()
	if got.Namespace != "walk" || got.Pod != "grain-sandbox-7-1" || got.Workspace != "/home/agent" {
		t.Fatalf("KubePod() = %+v, want namespace walk, pod grain-sandbox-7-1, workspace /home/agent", got)
	}
}

func TestK8sSandboxesHostCapacityQueriesClusterNodesWithNodeSelector(t *testing.T) {
	t.Parallel()
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	script := `#!/bin/sh
cat <<'EOF'
{
  "items": [
    {
      "metadata": {"name": "node-1", "labels": {"pool": "sandbox"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "3890m", "memory": "15360Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "node-2", "labels": {"pool": "sandbox"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "4110m", "memory": "15360Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "node-system", "labels": {"pool": "system"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "8", "memory": "32Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    }
  ]
}
EOF
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}
	k := NewK8sSandboxes(K8sConfig{
		Image:        "example/sandbox",
		Kubectl:      kubectl,
		NodeSelector: map[string]string{"pool": "sandbox"},
	})
	cpus, memoryMB, _, err := k.HostCapacity(context.Background())
	if err != nil {
		t.Fatalf("HostCapacity: %v", err)
	}
	if cpus != 8 || memoryMB != 30720 {
		t.Fatalf("HostCapacity() = (%d, %d), want (8, 30720)", cpus, memoryMB)
	}
}

// TestK8sSweepOrphansReapsWhatNoRunHolds is
// TestK8sGranuleSweepOrphansSparesBuildingAndLivePods' counterpart for
// plain pods: a live pod is spared, one nothing holds is deleted, and
// ReapOrphans is the same pass.
func TestK8sSweepOrphansReapsWhatNoRunHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kubectl := writeSweepKubectl(t)
	k := NewK8sSandboxes(K8sConfig{Kubectl: kubectl.bin, Image: "example/sandbox", ReadyPollInterval: 5 * time.Millisecond})
	if _, err := k.Acquire(ctx, "780-3", Shape{}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	kubectl.takeDeleted(t)
	kubectl.setPods(t, "grain-sandbox-780-3", "grain-sandbox-779-1")
	reaped, err := k.SweepOrphans(ctx)
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if got := kubectl.takeDeleted(t); reaped != 1 || len(got) != 1 || got[0] != "grain-sandbox-779-1" {
		t.Fatalf("SweepOrphans = %d, deleted %v; want 1, only grain-sandbox-779-1", reaped, got)
	}
	kubectl.setPods(t, "grain-sandbox-780-3")
	if reaped, err := k.ReapOrphans(ctx); err != nil || reaped != 0 {
		t.Fatalf("ReapOrphans with only the live pod = (%d, %v), want (0, nil)", reaped, err)
	}
}

func TestK8sSandboxesLabelAndSweepTheirInstance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kubectl := writeSweepKubectl(t)
	k := NewK8sSandboxes(K8sConfig{Kubectl: kubectl.bin, Image: "example/sandbox", Instance: "a1b2c3d4"})
	if got, _ := podInstanceLabel(t, k.pods(), "grain-sandbox-780-3", k.podShape(Shape{}), k.podOptions(Shape{})); got != "a1b2c3d4" {
		t.Errorf("pod %s = %q, want a1b2c3d4", k8s.InstanceLabel, got)
	}
	kubectl.setPods(t, "grain-sandbox-780-3")
	if _, err := k.Readopt(ctx, "780-3"); err != nil {
		t.Fatalf("Readopt: %v", err)
	}
	assertSweepSelects(t, kubectl.takeCalls(t), "a1b2c3d4")

	var _ SandboxInstanceSetter = k
	k.SetInstance("renamed")
	if got, _ := podInstanceLabel(t, k.pods(), "grain-sandbox-780-3", k.podShape(Shape{}), k.podOptions(Shape{})); got != "renamed" {
		t.Errorf("pod %s after SetInstance = %q, want renamed", k8s.InstanceLabel, got)
	}
	if _, err := k.SweepOrphans(ctx); err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	assertSweepSelects(t, kubectl.takeCalls(t), "renamed")

	// No instance configured labels nothing, as before the label existed.
	plain := NewK8sSandboxes(K8sConfig{Image: "example/sandbox"})
	if got, ok := podInstanceLabel(t, plain.pods(), "grain-sandbox-780-3", plain.podShape(Shape{}), plain.podOptions(Shape{})); ok {
		t.Errorf("pod with no instance configured is labelled %s=%q", k8s.InstanceLabel, got)
	}
}

// TestSweepOrphanPodsAsksBeforeEachDelete: an Acquire that starts building
// a pod under a listed orphan's name while the sweep is part-way through
// the list is seen by the time the sweep reaches that name, so the pod it
// just created is not deleted out from under it.
func TestSweepOrphanPodsAsksBeforeEachDelete(t *testing.T) {
	t.Parallel()
	kubectl := writeSweepKubectl(t)
	kubectl.setPods(t, "grain-sandbox-779-1", "grain-sandbox-780-3")
	var mu sync.Mutex
	building := map[string]bool{}
	reaped, err := sweepOrphanPods(context.Background(), k8s.Client{Kubectl: kubectl.bin}, &mu, func(pod string) bool {
		held := building[pod]
		// An Acquire for 780-3 starts once the sweep has moved past the
		// list, while it is still deleting 779-1.
		building["grain-sandbox-780-3"] = true
		return held
	})
	if err != nil {
		t.Fatalf("sweepOrphanPods: %v", err)
	}
	if got := kubectl.takeDeleted(t); reaped != 1 || len(got) != 1 || got[0] != "grain-sandbox-779-1" {
		t.Fatalf("sweepOrphanPods = %d, deleted %v; want 1, only grain-sandbox-779-1", reaped, got)
	}
}

// TestK8sSandboxReleaseOutlivesACancelledContext: a run that was
// cancelled or timed out releases its sandbox on the context that just
// ended, and the pod must still be deleted rather than left behind for
// the next orphan sweep.
func TestK8sSandboxReleaseOutlivesACancelledContext(t *testing.T) {
	t.Parallel()
	kubectl := writeSweepKubectl(t)
	k := NewK8sSandboxes(K8sConfig{Kubectl: kubectl.bin, Image: "example/sandbox"})
	sb := &k8sSandbox{owner: k, name: "784-1", podName: "grain-sandbox-784-1"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sb.Release(ctx); err != nil {
		t.Fatalf("Release on a cancelled context: %v", err)
	}
	if got := kubectl.takeDeleted(t); len(got) != 1 || got[0] != "grain-sandbox-784-1" {
		t.Fatalf("Release on a cancelled context deleted %v, want grain-sandbox-784-1", got)
	}
}
