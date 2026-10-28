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

// grain/task-223: the arithmetic itself lives in pkg/ui and is tested
// there. What is worth holding here is the claim this package makes on
// its behalf -- that the /proc pkg/sysstat reads belongs to the machine
// the sandboxes are actually built on -- which is true of exactly one
// backend and must not be inherited by the next one.
package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func writeFakeClusterKubectl(t *testing.T, jsonOut string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kubectl")
	script := "#!/bin/sh\ncat <<'EOF'\n" + jsonOut + "\nEOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}
	return bin
}

func TestSandboxHostCapacityOnlySpeaksForABackendOnThisMachine(t *testing.T) {
	t.Parallel()
	// A granule VM is a docker container created by a granule binary this
	// process execs on its own host, so this process's /proc is the
	// sandbox host's.
	granuleSB := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{})
	if !localHostSandboxes(granuleSB) {
		t.Fatal("localHostSandboxes(GranuleSandboxes) = false, want true")
	}
	granule := sandboxHostCapacity(granuleSB)
	if granule == nil {
		t.Fatal("sandboxHostCapacity(GranuleSandboxes) = nil, want a reading of this machine")
	}
	if runtime.GOOS == "linux" {
		host, err := granule()
		if err != nil {
			t.Fatalf("reading this host's capacity: %v", err)
		}
		if host.CPUs <= 0 || host.MemoryMB <= 0 {
			t.Errorf("capacity = %+v, want the cores and memory this machine has", host)
		}
	}

	// Host sandboxing is on this same machine outright -- the sandbox is
	// a directory on this filesystem -- so it gets a reading too
	// (grain/task-285). What it has no shape to multiply changes what ui
	// says about it, not whether this daemon can speak for the host.
	hostSB := orchestrator.NewHostSandboxes(t.TempDir())
	if !localHostSandboxes(hostSB) {
		t.Error("localHostSandboxes(HostSandboxes) = false, want true")
	}
	if host := sandboxHostCapacity(hostSB); host == nil {
		t.Error("sandboxHostCapacity(HostSandboxes) = nil, want a reading -- the runs are on this machine too")
	}

	// Kubernetes backends read cluster node allocatable capacity via
	// kubectl rather than this host's /proc, while localHostSandboxes stays
	// false so the /proc PSI/swap auto-overcommit controller does not step
	// on the controller pod's own /proc.
	kubectl := writeFakeClusterKubectl(t, `{
  "items": [
    {
      "metadata": {"name": "node-1", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "node-2", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    }
  ]
}`)
	k8sSB := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
		Image:        "example/sandbox",
		Kubectl:      kubectl,
		NodeSelector: map[string]string{"pool": "kvm"},
	})
	if localHostSandboxes(k8sSB) {
		t.Error("localHostSandboxes(K8sSandboxes) = true, want false")
	}
	k8sCap := sandboxHostCapacity(k8sSB)
	if k8sCap == nil {
		t.Fatal("sandboxHostCapacity(K8sSandboxes) = nil, want cluster allocatable capacity")
	}
	gotK8s, err := k8sCap()
	if err != nil {
		t.Fatalf("k8sCap(): %v", err)
	}
	if gotK8s.CPUs != 31 || gotK8s.MemoryMB != 122880 {
		t.Errorf("k8sCap() = %+v, want {CPUs: 31, MemoryMB: 122880}", gotK8s)
	}

	k8sGranuleSB := orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{
		Image:        "example/granule",
		Kubectl:      kubectl,
		NodeSelector: map[string]string{"pool": "kvm"},
	})
	if localHostSandboxes(k8sGranuleSB) {
		t.Error("localHostSandboxes(K8sGranuleSandboxes) = true, want false")
	}
	k8sGranuleCap := sandboxHostCapacity(k8sGranuleSB)
	if k8sGranuleCap == nil {
		t.Fatal("sandboxHostCapacity(K8sGranuleSandboxes) = nil, want cluster allocatable capacity")
	}
	gotK8sGranule, err := k8sGranuleCap()
	if err != nil {
		t.Fatalf("k8sGranuleCap(): %v", err)
	}
	if gotK8sGranule.CPUs != 31 || gotK8sGranule.MemoryMB != 122880 {
		t.Errorf("k8sGranuleCap() = %+v, want {CPUs: 31, MemoryMB: 122880}", gotK8sGranule)
	}

	// And nothing else gets one: a backend that built its sandboxes
	// elsewhere would make the claim false outright, so the test is a
	// concrete one that a new backend has to come here and change rather
	// than an interface it could satisfy by accident.
	if localHostSandboxes(elsewhereSandboxes{}) {
		t.Error("localHostSandboxes(elsewhereSandboxes) = true, want false")
	}
	if got := sandboxHostCapacity(elsewhereSandboxes{}); got != nil {
		t.Error("sandboxHostCapacity(an unknown backend) is set, want nil -- nobody made that claim for it")
	}
}

// elsewhereSandboxes stands in for the backend grain does not have yet:
// one whose sandboxes are somewhere other than this process's own
// machine, and which therefore must not inherit this daemon's /proc as
// its host.
type elsewhereSandboxes struct{ orchestrator.Sandboxes }
