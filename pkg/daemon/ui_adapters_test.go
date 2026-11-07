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

package daemon_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// fixedHealthSandboxes is an orchestrator.Sandboxes whose Health is
// whatever the test says, so the adapter's own copying is what is tested.
type fixedHealthSandboxes struct {
	orchestrator.Sandboxes
	health []orchestrator.SandboxHealth
}

func (f fixedHealthSandboxes) Health(context.Context) []orchestrator.SandboxHealth {
	return f.health
}

// The start time a backend records is what the System pane names the
// longest-running CI sandbox by, so it has to reach /api/sandboxes; a
// backend that does not know it gives a row with no startedAt at all,
// rather than one claiming the sandbox started in year 1.
func TestSandboxHealthAdapterCarriesStartedAt(t *testing.T) {
	started := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	got := daemon.SandboxHealthAdapter{Inner: fixedHealthSandboxes{health: []orchestrator.SandboxHealth{
		{Sandbox: "c1-0", Kind: "ci", Backend: "host", Ready: true, StartedAt: started},
		{Sandbox: "t1-1", Kind: "agent", Backend: "host", Ready: true},
	}}}.Health(context.Background())
	if len(got) != 2 {
		t.Fatalf("Health = %+v, want 2 rows", got)
	}
	if got[0].StartedAt == nil || !got[0].StartedAt.Equal(started) {
		t.Errorf("c1-0 StartedAt = %v, want %v", got[0].StartedAt, started)
	}
	if got[1].StartedAt != nil {
		t.Errorf("t1-1 StartedAt = %v, want nil for a backend that gave none", got[1].StartedAt)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"startedAt":"2026-10-26T08:00:00Z"`) {
		t.Errorf("JSON %s lacks c1-0's startedAt", raw)
	}
	if strings.Count(string(raw), `"startedAt"`) != 1 {
		t.Errorf("JSON %s carries startedAt on a row whose backend gave none", raw)
	}
}

// A granule backend charges each VM its guest plus the VMM headroom
// (model.DefaultVMMHeadroom), and committedMemoryMB says so; the pool
// breakdown beside it on Sandbox health has to add up to the same
// figure rather than listing bare guest sizes. One 2 GiB VM is 3 GiB
// committed, and 3 GiB in its pool (docs/test-runs/2026-11-03-sandbox-resources.md).
func TestSandboxBudgetPoolsIncludeTheVMMHeadroom(t *testing.T) {
	ledger := &model.Ledger{VMMHeadroom: model.DefaultVMMHeadroom()}
	budget := model.NewBudget(model.Commitment{CPUs: 8, MemoryMB: 32768}, 0, 1, 1)
	shape := model.Commitment{CPUs: 2, MemoryMB: 2048}
	if verdict, _ := ledger.Admit(budget, model.PoolAgent, "run-1", shape); !verdict.OK {
		t.Fatalf("Admit = %+v, want admitted", verdict)
	}
	adapter := daemon.SandboxHealthAdapter{Limits: func() model.Limits {
		return model.Limits{Budget: budget, Ledger: ledger}
	}}
	report, _ := adapter.BudgetAndReconciliation()
	if report == nil {
		t.Fatal("no budget report")
	}
	if report.CommittedMemoryMB != 3072 {
		t.Fatalf("CommittedMemoryMB = %d, want 3072 (2048 + 1024 headroom)", report.CommittedMemoryMB)
	}
	if len(report.Pools) != 1 || report.Pools[0].MemoryMB != report.CommittedMemoryMB {
		t.Fatalf("Pools = %+v, want one pool charged the same %d MiB as CommittedMemoryMB", report.Pools, report.CommittedMemoryMB)
	}
}

// ContainerSandboxes and plain K8sSandboxes apply a shape (SandboxShapeIgnored
// is false) yet still cannot withhold a network, so console-session routes ask
// SandboxHealthAdapter.IsolatesNetwork rather than !SandboxShapeIgnored.
func TestSandboxHealthAdapterCarriesNetworkIsolation(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		inner orchestrator.Sandboxes
		want  bool
	}{
		"nil":         {nil, true},
		"disabled":    {orchestrator.NewDisabledSandboxes(), true},
		"granule":     {orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{}), true},
		"k8s-granule": {orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{}), true},
		"container":   {orchestrator.NewContainerSandboxes(orchestrator.ContainerConfig{}), false},
		"k8s":         {orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{}), false},
		"host":        {orchestrator.NewHostSandboxes(t.TempDir()), false},
	} {
		if got := (daemon.SandboxHealthAdapter{Inner: tc.inner}).IsolatesNetwork(); got != tc.want {
			t.Errorf("%s: IsolatesNetwork() = %v, want %v", name, got, tc.want)
		}
	}
}
