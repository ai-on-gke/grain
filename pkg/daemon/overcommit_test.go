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

package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/sysstat"
)

func TestBudgetFixedModeIgnoresTheController(t *testing.T) {
	ctrl := model.NewOvercommitController(model.OvercommitSettings{Mode: model.OvercommitAuto}, 5, 1.8)
	c := Config{HostCPUs: 34, HostMemoryMB: 136 * 1024, MemoryReserveMB: 8 * 1024,
		CpuOvercommit: 2, MemoryOvercommit: 1, Overcommit: ctrl}
	b := c.Budget()
	if b.CPUOvercommit != 2 || b.MemoryOvercommit != 1 {
		t.Fatalf("fixed mode admits at %.2f/%.2f, want the stored 2/1", b.CPUOvercommit, b.MemoryOvercommit)
	}
	c.SandboxOvercommit = model.OvercommitSettings{Mode: model.OvercommitAuto}
	b = c.Budget()
	if b.CPUOvercommit != 5 || b.MemoryOvercommit != 1.8 {
		t.Fatalf("auto mode admits at %.2f/%.2f, want the controller's 5/1.8", b.CPUOvercommit, b.MemoryOvercommit)
	}
	// Settings that do not validate are fixed mode.
	c.SandboxOvercommit.CPUMin, c.SandboxOvercommit.CPUMax = 4, 3
	if b = c.Budget(); b.CPUOvercommit != 2 {
		t.Fatalf("invalid auto settings admit at %.2f, want the stored 2", b.CPUOvercommit)
	}
	// And no controller -- anything that is not a running daemon -- is
	// fixed mode too.
	c.SandboxOvercommit = model.OvercommitSettings{Mode: model.OvercommitAuto}
	c.Overcommit = nil
	if b = c.Budget(); b.CPUOvercommit != 2 {
		t.Fatalf("auto mode with no controller admits at %.2f, want the stored 2", b.CPUOvercommit)
	}
}

func TestOvercommitSamplerRatesAndDeltas(t *testing.T) {
	var s overcommitSampler
	at := time.Unix(1000, 0)
	snap := sysstat.Snapshot{CPUs: 8, LoadAverage1: 3, MemTotalMB: 1000, MemUsedMB: 600, SwapUsedMB: 50}
	first := s.signals(at, snap, sysstat.Pressure{SwapIns: 1000, OOMKills: 7, MemorySome: 2}, model.Commitment{CPUs: 4})
	if first.OOMKills != 0 || first.SwapInMBps != 0 {
		t.Fatalf("first reading counted the counters since boot: %+v", first)
	}
	if first.AvailableMemoryMB != 400 || first.MemorySome != 2 || first.Committed.CPUs != 4 {
		t.Fatalf("levels not carried: %+v", first)
	}
	// 2560 pages in 10s is 10 MiB in 10s.
	second := s.signals(at.Add(10*time.Second), snap, sysstat.Pressure{SwapIns: 3560, OOMKills: 9}, model.Commitment{})
	if second.OOMKills != 2 {
		t.Errorf("oom kills %d, want 2", second.OOMKills)
	}
	if d := second.SwapInMBps - 1.0; d > 1e-9 || d < -1e-9 {
		t.Errorf("swap-in %.3f MB/s, want 1", second.SwapInMBps)
	}
}

func TestLiveConfigRefreshPushesOvercommitToKubernetesSandboxes(t *testing.T) {
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

	cfg := Config{
		PollInterval:        30 * time.Second,
		MaxWorkers:          1,
		K8sGranuleSandboxes: true,
		K8sGranuleImage:     "example/granule:latest",
		CpuOvercommit:       1,
		MemoryOvercommit:    1,
	}
	if err := store.PutConfig(ctx, cfg.ToModelConfig()); err != nil {
		t.Fatalf("store.PutConfig initial: %v", err)
	}

	cpuInit, memInit := cfg.overcommitRatios()
	granuleSandboxes := orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{
		Image:            cfg.K8sGranuleImage,
		CPUOvercommit:    cpuInit,
		MemoryOvercommit: memInit,
	})
	live := NewLiveConfig(store, granuleSandboxes, cfg, nil)
	deps := orchestrator.Deps{}

	updated := cfg.ToModelConfig()
	updated.SandboxCPUOvercommit = 10
	updated.SandboxMemoryOvercommit = 10
	updated.SandboxDiskOvercommit = 10
	if err := store.PutConfig(ctx, updated); err != nil {
		t.Fatalf("store.PutConfig updated: %v", err)
	}

	live.Refresh(ctx, &deps)

	gotCPU, gotMem, gotDisk := granuleSandboxes.Overcommit()
	if gotCPU != 10 || gotMem != 10 || gotDisk != 10 {
		t.Fatalf("granuleSandboxes.Overcommit() = %v/%v/%v, want 10/10/10", gotCPU, gotMem, gotDisk)
	}

	k8sSandboxes := orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
		Image:            "example/sandbox:latest",
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
		DiskOvercommit:   1,
	})
	liveK8s := NewLiveConfig(store, k8sSandboxes, cfg, nil)
	liveK8s.Refresh(ctx, &deps)

	gotCPU, gotMem, gotDisk = k8sSandboxes.Overcommit()
	if gotCPU != 10 || gotMem != 10 || gotDisk != 10 {
		t.Fatalf("k8sSandboxes.Overcommit() = %v/%v/%v, want 10/10/10", gotCPU, gotMem, gotDisk)
	}
}
