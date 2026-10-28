package daemon

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

func TestWithHostCapacityPopulatesBudgetFromClusterNodesOnKubernetes(t *testing.T) {
	t.Parallel()
	kubectl := writeFakeClusterKubectl(t, `{
  "items": [
    {
      "metadata": {"name": "kvm-1", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "kvm-2", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "kvm-3", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    }
  ]
}`)

	for _, tc := range []struct {
		name      string
		sandboxes orchestrator.Sandboxes
		cfg       Config
	}{
		{
			name: "K8sSandboxes",
			sandboxes: orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
				Image:        "example/sandbox",
				Kubectl:      kubectl,
				NodeSelector: map[string]string{"pool": "kvm"},
			}),
			cfg: Config{
				K8sSandboxes:     true,
				SandboxCPUs:      8,
				SandboxMemoryMB:  16384,
				CpuOvercommit:    2,
				MemoryOvercommit: 2,
			},
		},
		{
			name: "K8sGranuleSandboxes",
			sandboxes: orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{
				Image:        "example/granule",
				Kubectl:      kubectl,
				NodeSelector: map[string]string{"pool": "kvm"},
			}),
			cfg: Config{
				K8sGranuleSandboxes: true,
				SandboxCPUs:         8,
				SandboxMemoryMB:     16384,
				CpuOvercommit:       2,
				MemoryOvercommit:    2,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg.withHostCapacity(tc.sandboxes)
			// 3 * 15890m = 47670m -> 47 whole cores; 3 * 61440Mi = 184320 MiB.
			if cfg.HostCPUs != 47 || cfg.HostMemoryMB != 184320 {
				t.Fatalf("withHostCapacity = (%d CPUs, %d MiB), want (47 CPUs, 184320 MiB)", cfg.HostCPUs, cfg.HostMemoryMB)
			}
			budget := cfg.Budget()
			if !budget.Known() {
				t.Fatal("cfg.Budget().Known() = false, want true")
			}
			// (47 - 2 reserve) * 2 = 90 CPUs; 184320 * 2 = 368640 MiB (ReserveMemoryMB == 0).
			cpuCap, ok := budget.CPUBudget()
			if !ok || cpuCap != 90 {
				t.Fatalf("budget.CPUBudget() = (%v, %v), want (90, true)", cpuCap, ok)
			}
			memCap, ok := budget.MemoryBudgetMB()
			if !ok || memCap != 368640 {
				t.Fatalf("budget.MemoryBudgetMB() = (%v, %v), want (368640, true)", memCap, ok)
			}
			sentence := BudgetSentence(cfg.Limits(), true)
			if !strings.Contains(sentence, budget.Summary()) {
				t.Errorf("BudgetSentence = %q, want it to include %q", sentence, budget.Summary())
			}
		})
	}
}

func TestLogSandboxBudgetReportsClusterBudgetWhenKnownAndSchedulerFallbackWhenUnknown(t *testing.T) {
	var buf bytes.Buffer
	origOut := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	}()

	knownCfg := Config{
		K8sGranuleSandboxes: true,
		HostCPUs:            47,
		HostMemoryMB:        184320,
		SandboxCPUs:         8,
		SandboxMemoryMB:     16384,
		CpuOvercommit:       2,
		MemoryOvercommit:    2,
	}
	LogSandboxBudget(knownCfg)
	if !strings.Contains(buf.String(), "sandbox budget --") || !strings.Contains(buf.String(), knownCfg.Budget().Summary()) {
		t.Fatalf("LogSandboxBudget(known) = %q, want active budget summary", buf.String())
	}

	buf.Reset()
	unknownCfg := Config{
		K8sGranuleSandboxes: true,
	}
	LogSandboxBudget(unknownCfg)
	if !strings.Contains(buf.String(), "scheduler") {
		t.Fatalf("LogSandboxBudget(unknown) = %q, want scheduler fallback", buf.String())
	}
}

func TestOvercommitGatedOffOnKubernetesWhileFixedOvercommitBoundsBudget(t *testing.T) {
	t.Parallel()
	kubectl := writeFakeClusterKubectl(t, `{
  "items": [
    {
      "metadata": {"name": "kvm-1"},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "16", "memory": "32Gi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    }
  ]
}`)
	sb := orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{
		Image:   "example/granule",
		Kubectl: kubectl,
	})
	autoSettings := model.OvercommitSettings{Mode: model.OvercommitAuto}.Resolved()
	cfg := Config{
		K8sGranuleSandboxes: true,
		SandboxOvercommit:   autoSettings,
		CpuOvercommit:       3,
		MemoryOvercommit:    2,
		Overcommit:          model.NewOvercommitController(autoSettings, 1, 1),
	}.withHostCapacity(sb)

	live := NewLiveConfig(nil, sb, cfg, nil)
	if got := overcommitState(live, sb); got != nil {
		t.Fatal("overcommitState on K8sGranuleSandboxes is non-nil, want nil because /proc reflects only the controller pod")
	}

	// Even when SandboxOvercommit has Mode=="auto" in the config, Kubernetes
	// uses the fixed CpuOvercommit/MemoryOvercommit ratios rather than a
	// local-host OvercommitController initialized at 1x/1x.
	budget := cfg.Budget()
	if budget.CPUOvercommit != 3 || budget.MemoryOvercommit != 2 {
		t.Fatalf("budget overcommit = (%v, %v), want fixed (3, 2)", budget.CPUOvercommit, budget.MemoryOvercommit)
	}
	// (16 - 2 reserve) * 3 = 42 CPUs; 32768 * 2 = 65536 MiB.
	cpuCap, _ := budget.CPUBudget()
	memCap, _ := budget.MemoryBudgetMB()
	if cpuCap != 42 || memCap != 65536 {
		t.Fatalf("budget caps = (%v CPUs, %d MiB), want (42 CPUs, 65536 MiB)", cpuCap, memCap)
	}
}

func TestK8sClusterBudgetRationsCISharedGateHonoursAgentWaitingAndAdmitsConsoleExceptions(t *testing.T) {
	t.Parallel()
	// 3 nodes x (15890m CPU, 61440Mi memory) = 47 CPUs, 184320 MiB allocatable.
	kubectl := writeFakeClusterKubectl(t, `{
  "items": [
    {
      "metadata": {"name": "kvm-1", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "kvm-2", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    },
    {
      "metadata": {"name": "kvm-3", "labels": {"pool": "kvm"}},
      "spec": {},
      "status": {
        "allocatable": {"cpu": "15890m", "memory": "61440Mi"},
        "conditions": [{"type": "Ready", "status": "True"}]
      }
    }
  ]
}`)
	sb := orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{
		Image:        "example/granule",
		Kubectl:      kubectl,
		NodeSelector: map[string]string{"pool": "kvm"},
	})
	ledger := &model.Ledger{}
	cfg := Config{
		K8sGranuleSandboxes: true,
		Ledger:              ledger,
		SandboxCPUs:         8,
		SandboxMemoryMB:     16384,
		CpuOvercommit:       1,
		MemoryOvercommit:    1,
	}.withHostCapacity(sb)

	limits := cfg.Limits()
	if !limits.Budget.Known() {
		t.Fatal("limits.Budget.Known() = false, want true")
	}

	gate := &ci.SharedGate{
		Budget:  func() model.Budget { return limits.Budget },
		Ledger:  func() *model.Ledger { return ledger },
		Default: func() model.Commitment { return limits.AgentShape },
		Poll:    10 * time.Millisecond,
	}

	jobShape := model.Commitment{CPUs: 8, MemoryMB: 16384}
	// With 47 CPUs (45 usable after 2-CPU reserve) and 184320 MiB at 1x
	// overcommit, at most 5 sandboxes of 8 CPUs (40 <= 45 < 48) fit
	// concurrently. Admit 4 CI jobs first (32 CPUs).
	var releases []func()
	defer func() {
		for _, rel := range releases {
			rel()
		}
	}()
	for i := 0; i < 4; i++ {
		rel, err := gate.Acquire(context.Background(), ci.GateRequest{Want: jobShape})
		if err != nil {
			t.Fatalf("admitting CI job %d: %v", i, err)
		}
		releases = append(releases, rel)
	}

	// 1. When an agent task is waiting for capacity, Ledger.AgentWaiting()
	// holds back new CI jobs even though an 8-CPU CI job would fit in the
	// remaining 13 usable CPUs.
	ledger.SetAgentDemand(true, model.Commitment{CPUs: 16, MemoryMB: 32768})
	ciCtx, ciCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer ciCancel()
	if _, err := gate.Acquire(ciCtx, ci.GateRequest{Want: jobShape}); err == nil {
		t.Fatal("SharedGate admitted a CI job while an agent run was waiting for capacity")
	}

	// 2. Once the agent stops waiting, the 5th CI job fits (40/45 usable CPUs),
	// and a 6th CI job (48 > 45 usable CPUs) is rationed by the cluster budget
	// instead of flooding kube-scheduler with 70+ Pending pods.
	ledger.SetAgentDemand(false, model.Commitment{})
	rel5, err := gate.Acquire(context.Background(), ci.GateRequest{Want: jobShape})
	if err != nil {
		t.Fatalf("admitting 5th CI job after agent wait cleared: %v", err)
	}
	releases = append(releases, rel5)

	sixthCtx, sixthCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer sixthCancel()
	if _, err := gate.Acquire(sixthCtx, ci.GateRequest{Want: jobShape}); err == nil {
		t.Fatal("SharedGate admitted a 6th 8-CPU CI job past 45-CPU usable cluster budget")
	}

	// 3. Even with 5 CI jobs holding 40/45 usable CPUs (where an 8-CPU
	// ordinary admission is refused because 40+8 > 45), a console session
	// (director / silo) can claim capacity via AdmitException
	// (VerdictException) without being starved behind Pending CI pods.
	consoleVerdict, consoleRelease := ledger.AdmitException(
		limits.Budget, model.PoolAgent, "console-director",
		model.Commitment{CPUs: 8, MemoryMB: 16384})
	if !consoleVerdict.OK || !consoleVerdict.Exception {
		t.Fatalf("console AdmitException = %+v, want OK=true, Exception=true", consoleVerdict)
	}
	consoleRelease()
}

// withHostCapacity is re-run against a cluster on every reconcile tick,
// so a read that fails or finds no Ready nodes keeps the figure already
// known; only a deployment with no figure yet takes whatever it reads.
func TestWithHostCapacityKeepsKnownClusterReadingOnFailedOrEmptyRead(t *testing.T) {
	t.Parallel()
	known := Config{K8sSandboxes: true, HostCPUs: 48, HostMemoryMB: 98304, CpuOvercommit: 1, MemoryOvercommit: 1}
	for name, kubectl := range map[string]string{
		"empty node list": writeFakeClusterKubectl(t, `{"items": []}`),
		"no Ready nodes": writeFakeClusterKubectl(t, `{"items": [{"metadata": {"name": "kvm-1"}, "spec": {},
  "status": {"allocatable": {"cpu": "16", "memory": "32Gi"},
             "conditions": [{"type": "Ready", "status": "False"}]}}]}`),
		"kubectl failure": writeFakeKubectlFailing(t),
	} {
		sb := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{Image: "example/sandbox", Kubectl: kubectl})
		got := known.withHostCapacity(sb)
		if got.HostCPUs != 48 || got.HostMemoryMB != 98304 {
			t.Errorf("%s: host = (%d CPUs, %d MiB), want the known (48, 98304)", name, got.HostCPUs, got.HostMemoryMB)
		}
		if !got.Budget().Known() {
			t.Errorf("%s: Budget().Known() = false, want the known budget kept", name)
		}
		if fresh := (Config{K8sSandboxes: true}).withHostCapacity(sb); fresh.HostCPUs != 0 || fresh.HostMemoryMB != 0 {
			t.Errorf("%s: unknown host = (%d CPUs, %d MiB), want (0, 0)", name, fresh.HostCPUs, fresh.HostMemoryMB)
		}
	}
}

func writeFakeKubectlFailing(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'Unable to connect to the server' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}
	return bin
}
