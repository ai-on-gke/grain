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

package cli

// The disk floor's half of the same move the budget's three ratios made
// (grain/task-372, and daemon_sandbox_budget_seed_test.go for the pair
// beside it): -sandbox-disk-floor-share and -sandbox-disk-floor-min-mb
// seed a deployment that has never stored a row, and are ignored the
// moment one exists.
//
// It matters more here than for the ratios. The moment an operator wants
// to move this number is the moment a filling volume has already stopped
// every dispatch, and the lever they had was a systemd unit edit plus a
// daemon restart on a deployment that is in trouble. A seed that dropped
// the floor would leave a fresh deployment holding nothing back at all,
// which is the failure model.DiskFloor exists to close; a flag that went
// on beating the row would make the pane's field a decoration.

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
)

// floorFlags is a daemon started with the two flags at the defaults the
// flag block declares for them: a tenth of each volume, never less than
// 5 GiB.
func floorFlags() daemon.Config {
	cfg := startupConfig()
	cfg.DiskFloorShare = model.DefaultDiskFloorShare
	cfg.DiskFloorMinMB = model.MinDiskFloorMB
	return cfg
}

func TestLoadConfigSeedsTheDiskFloorFromTheFlags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)

	got, err := daemon.LoadConfig(ctx, store, floorFlags())
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got.DiskFloor() != model.DefaultDiskFloor() {
		t.Errorf("floor in effect on a fresh deployment = %+v, want the flags' own %+v",
			got.DiskFloor(), model.DefaultDiskFloor())
	}
	// And written to the row, so the Settings pane opens showing what
	// this deployment is actually holding work over rather than a blank.
	stored, err := store.GetConfig(ctx)
	if err != nil || stored == nil {
		t.Fatalf("GetConfig after seeding: (%+v, %v)", stored, err)
	}
	if stored.SandboxDiskFloor() != model.DefaultDiskFloor() {
		t.Errorf("seeded row = %+v, want the flags' own %+v",
			stored.SandboxDiskFloor(), model.DefaultDiskFloor())
	}
}

func TestLoadConfigPrefersTheStoredDiskFloorOverTheFlags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := floorFlags()

	stored := cfg.ToModelConfig()
	// The change an operator makes from the pane while a full volume is
	// holding every dispatch: a twentieth of the volume instead of a
	// tenth, and the minimum halved with it.
	stored.SandboxDiskFloorShare, stored.SandboxDiskFloorMinMB = 0.05, 2048
	putConfig(t, store, stored)

	got, err := daemon.LoadConfig(ctx, store, cfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := model.DiskFloor{Share: 0.05, MinMB: 2048}
	if got.DiskFloor() != want {
		t.Fatalf("floor in effect = %+v, want the stored %+v", got.DiskFloor(), want)
	}
	// Down to the budget the gate and the dispatch are handed, which is
	// what is actually holding the work: 443 GB with 30 GiB free is under
	// the flags' 44.3 GiB floor and over the stored 22.1 GiB one.
	budget := got.Budget()
	budget.Disk = model.DiskSpace{Path: "/var/lib/grain/data", TotalMB: 443 * 1024, FreeMB: 30 * 1024}
	if verdict := budget.Admits(model.Commitment{}, model.Commitment{}); !verdict.OK {
		t.Errorf("the lowered floor did not reach the budget: %s", verdict.Reason)
	}
}

// A row written before these two columns existed reads back as grain's
// own floor, not as no floor at all -- the migration's default, which is
// the difference between a deployment that upgrades and goes on holding
// work over a filling volume and one that quietly stops.
func TestADeploymentUpgradingAcrossTheDiskFloorColumnsKeepsAFloor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := floorFlags()

	// What a row written by an older build carries: everything else
	// chosen, and the two floor fields at their Go zero values.
	stored := cfg.ToModelConfig()
	stored.SandboxDiskFloorShare, stored.SandboxDiskFloorMinMB = 0, 0
	putConfig(t, store, stored)

	got, err := daemon.LoadConfig(ctx, store, cfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// A zero row is an operator's choice once these columns exist, and
	// loadConfig honours it: "hold nothing back" is a setting. What the
	// migration guarantees is that a database predating the columns is
	// not written with zeros in the first place, which is
	// Store.ensureConfigColumn's DEFAULT and is covered in pkg/model.
	if got.DiskFloor().Known() {
		t.Errorf("a stored floor of nothing was overridden by the flags: %+v", got.DiskFloor())
	}
}
