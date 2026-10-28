package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// A cluster whose nodes could not be read at startup -- a slow API
// server, or a ClusterRoleBinding applied after the daemon came up --
// gets its budget on the next reconcile tick that can read them, rather
// than running unbounded until somebody restarts the daemon.
func TestRefreshRetriesHostCapacityUnknownAtStartup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	// The fake kubectl is forbidden from listing nodes until the test
	// "applies" the binding by creating the allowed file.
	dir := t.TempDir()
	allowed := filepath.Join(dir, "allowed")
	kubectl := filepath.Join(dir, "kubectl")
	script := `#!/bin/sh
if [ ! -e '` + allowed + `' ]; then
  echo 'Error from server (Forbidden): nodes is forbidden' >&2
  exit 1
fi
cat <<'EOF'
{"items": [{"metadata": {"name": "kvm-1"}, "spec": {},
  "status": {"allocatable": {"cpu": "16", "memory": "32Gi"},
             "conditions": [{"type": "Ready", "status": "True"}]}}]}
EOF
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}
	sb := orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{
		Image:   "example/granule",
		Kubectl: kubectl,
	})

	cfg := Config{
		PollInterval:        30 * time.Second,
		MaxWorkers:          1,
		K8sGranuleSandboxes: true,
		K8sGranuleImage:     "example/granule",
		CpuOvercommit:       1,
		MemoryOvercommit:    1,
	}
	if err := store.PutConfig(ctx, cfg.ToModelConfig()); err != nil {
		t.Fatalf("store.PutConfig: %v", err)
	}
	cfg = cfg.withHostCapacity(sb)
	if cfg.Budget().Known() {
		t.Fatal("startup Budget().Known() = true with nodes forbidden, want false")
	}
	live := NewLiveConfig(store, sb, cfg, nil)
	deps := orchestrator.Deps{Budget: cfg.Budget()}

	live.Refresh(ctx, &deps)
	if live.Current().Budget().Known() || deps.Budget.Known() {
		t.Fatal("Budget().Known() = true after a refresh that still could not read nodes, want false")
	}

	if err := os.WriteFile(allowed, nil, 0o644); err != nil {
		t.Fatalf("allowing node reads: %v", err)
	}
	live.Refresh(ctx, &deps)
	applied := live.Current()
	if applied.HostCPUs != 16 || applied.HostMemoryMB != 32768 {
		t.Fatalf("Applied host = (%d CPUs, %d MiB), want (16, 32768)", applied.HostCPUs, applied.HostMemoryMB)
	}
	if !applied.Budget().Known() {
		t.Fatal("Applied.Budget().Known() = false after nodes became readable, want true")
	}
	if !deps.Budget.Known() {
		t.Fatal("deps.Budget.Known() = false after nodes became readable, want true")
	}
}

// writeScalingClusterKubectl is a fake kubectl whose `get nodes` answers
// with whatever setNodes last wrote, and fails while fail(true) is in
// effect -- a cluster an autoscaler is resizing under a running daemon.
func writeScalingClusterKubectl(t *testing.T) (kubectl string, setNodes func(n int), fail func(bool)) {
	t.Helper()
	dir := t.TempDir()
	nodes := filepath.Join(dir, "nodes.json")
	failing := filepath.Join(dir, "fail")
	kubectl = filepath.Join(dir, "kubectl")
	script := `#!/bin/sh
if [ -e '` + failing + `' ]; then
  echo 'Unable to connect to the server: dial tcp: i/o timeout' >&2
  exit 1
fi
cat '` + nodes + `'
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake kubectl: %v", err)
	}
	setNodes = func(n int) {
		t.Helper()
		items := make([]string, n)
		for i := range items {
			items[i] = fmt.Sprintf(`{"metadata": {"name": "kvm-%d"}, "spec": {},
  "status": {"allocatable": {"cpu": "16", "memory": "32Gi"},
             "conditions": [{"type": "Ready", "status": "True"}]}}`, i+1)
		}
		if err := os.WriteFile(nodes, []byte(`{"items": [`+strings.Join(items, ",")+`]}`), 0o644); err != nil {
			t.Fatalf("writing fake node list: %v", err)
		}
	}
	fail = func(on bool) {
		t.Helper()
		if !on {
			_ = os.Remove(failing)
			return
		}
		if err := os.WriteFile(failing, nil, 0o644); err != nil {
			t.Fatalf("failing fake kubectl: %v", err)
		}
	}
	return kubectl, setNodes, fail
}

// A cluster autoscaler adds and removes nodes under a running daemon.
// Every reconcile tick re-reads the cluster, so the budget the ledger,
// the dispatch loop and the CI gate hold sandboxes to follows the
// cluster both ways without a restart; a tick that cannot read it, or
// reads no nodes at all, keeps the last figure rather than unbounding
// the budget; and a cluster that grew wakes whatever is waiting on it.
func TestRefreshFollowsClusterScalingOnKubernetes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		granule bool
	}{
		{name: "K8sSandboxes"},
		{name: "K8sGranuleSandboxes", granule: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
			if err != nil {
				t.Fatalf("sqlite.Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store := model.New(db)
			if err := store.Init(ctx); err != nil {
				t.Fatalf("store.Init: %v", err)
			}

			kubectl, setNodes, fail := writeScalingClusterKubectl(t)
			setNodes(3)
			var sb orchestrator.Sandboxes
			cfg := Config{
				PollInterval:     30 * time.Second,
				MaxWorkers:       1,
				SandboxCPUs:      8,
				SandboxMemoryMB:  16384,
				CpuOvercommit:    1,
				MemoryOvercommit: 1,
			}
			if tc.granule {
				sb = orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{Image: "example/granule", Kubectl: kubectl})
				cfg.K8sGranuleSandboxes, cfg.K8sGranuleImage = true, "example/granule"
			} else {
				sb = orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{Image: "example/sandbox", Kubectl: kubectl})
				cfg.K8sSandboxes, cfg.K8sSandboxImage = true, "example/sandbox"
			}
			var woken atomic.Int32
			cfg.Ledger = &model.Ledger{Wake: func() { woken.Add(1) }}
			if err := store.PutConfig(ctx, cfg.ToModelConfig()); err != nil {
				t.Fatalf("store.PutConfig: %v", err)
			}
			cfg = cfg.withHostCapacity(sb)
			live := NewLiveConfig(store, sb, cfg, nil)
			deps := orchestrator.Deps{Budget: cfg.Budget()}
			// What BuildCIService hands ci.SharedGate: the budget as it
			// stands on the attempt, not as it stood at startup.
			gate := &ci.SharedGate{
				Budget:  func() model.Budget { return live.Current().Limits().Budget },
				Ledger:  func() *model.Ledger { return live.Current().Ledger },
				Default: func() model.Commitment { return live.Current().Limits().AgentShape },
				Poll:    10 * time.Millisecond,
			}

			check := func(step string, wantCPUs, wantMemoryMB int) {
				t.Helper()
				applied := live.Current()
				if applied.HostCPUs != wantCPUs || applied.HostMemoryMB != wantMemoryMB {
					t.Fatalf("%s: Applied host = (%d CPUs, %d MiB), want (%d, %d)",
						step, applied.HostCPUs, applied.HostMemoryMB, wantCPUs, wantMemoryMB)
				}
				want := Config{HostCPUs: wantCPUs, HostMemoryMB: wantMemoryMB, CpuOvercommit: 1, MemoryOvercommit: 1}.Budget()
				wantCPU, _ := want.CPUBudget()
				for name, budget := range map[string]model.Budget{
					"live.Current().Budget()": applied.Budget(),
					"deps.Budget":             deps.Budget,
					"SharedGate.Budget()":     gate.Budget(),
				} {
					if got, _ := budget.CPUBudget(); !budget.Known() || got != wantCPU {
						t.Fatalf("%s: %s CPU budget = %v (known %v), want %v", step, name, got, budget.Known(), wantCPU)
					}
				}
			}
			check("startup", 48, 3*32768)

			// Scale-up: 3 nodes -> 5. A sixth 8-CPU sandbox (48 > 46
			// usable) does not fit three nodes, and does fit five.
			job := model.Commitment{CPUs: 8, MemoryMB: 16384}
			var releases []func()
			t.Cleanup(func() {
				for _, rel := range releases {
					rel()
				}
			})
			for i := 0; i < 5; i++ {
				rel, err := gate.Acquire(ctx, ci.GateRequest{Want: job})
				if err != nil {
					t.Fatalf("admitting CI job %d on 3 nodes: %v", i, err)
				}
				releases = append(releases, rel)
			}
			short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			_, err = gate.Acquire(short, ci.GateRequest{Want: job})
			cancel()
			if err == nil {
				t.Fatal("SharedGate admitted a 6th 8-CPU job on a 3-node cluster")
			}
			setNodes(5)
			live.Refresh(ctx, &deps)
			check("after scale-up", 80, 5*32768)
			if woken.Load() != 1 {
				t.Fatalf("Ledger.Wake called %d times after the cluster grew, want 1", woken.Load())
			}
			rel, err := gate.Acquire(ctx, ci.GateRequest{Want: job})
			if err != nil {
				t.Fatalf("admitting a 6th CI job on 5 nodes: %v", err)
			}
			releases = append(releases, rel)

			// An unchanged cluster changes nothing and wakes nobody.
			live.Refresh(ctx, &deps)
			check("unchanged", 80, 5*32768)

			// A tick that cannot reach the API server, or that reads no
			// Ready nodes, keeps the last reading.
			fail(true)
			live.Refresh(ctx, &deps)
			check("after a failed read", 80, 5*32768)
			fail(false)
			setNodes(0)
			live.Refresh(ctx, &deps)
			check("after an empty read", 80, 5*32768)

			// Scale-down: 5 nodes -> 3. It is not a growth, so it wakes
			// nobody.
			setNodes(3)
			live.Refresh(ctx, &deps)
			check("after scale-down", 48, 3*32768)
			if woken.Load() != 1 {
				t.Fatalf("Ledger.Wake called %d times in all, want 1 (only the scale-up)", woken.Load())
			}
		})
	}
}
