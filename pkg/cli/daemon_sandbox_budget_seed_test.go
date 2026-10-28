package cli

// The other half of moving the sandbox budget's three knobs out of the
// daemon's flags and into grain_config (grain/task-197, and the flag
// block's own seedOnly note): the flags are the seed for a deployment
// that has never stored a row, and are ignored the moment one exists.
//
// Neither half is visible from the Settings pane, which is why they are
// pinned here. A seed that dropped the ratios would leave a fresh
// deployment's budget at the Go zero value, which bounds nothing at all
// -- the exact failure model.Budget exists to close, arriving silently
// on the first start. A flag that went on beating the row afterwards
// would make the pane's field a decoration: an operator would raise the
// ratio, watch the journal go on refusing sandboxes against the old
// number, and be back to editing a systemd unit.

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
)

// budgetFlags is a daemon started with the three flags at the defaults
// the flag block declares for them: 2x vCPU, 1x memory, and grain's own
// eighth-of-the-machine reserve.
func budgetFlags() daemon.Config {
	cfg := startupConfig()
	cfg.CpuOvercommit = model.DefaultCPUOvercommit
	cfg.MemoryOvercommit = model.DefaultMemoryOvercommit
	cfg.MemoryReserveMB = model.ReserveMemoryDefault
	return cfg
}

func TestLoadConfigSeedsTheSandboxBudgetFromTheFlags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := budgetFlags()

	got, err := daemon.LoadConfig(ctx, store, cfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got.CpuOvercommit != model.DefaultCPUOvercommit ||
		got.MemoryOvercommit != model.DefaultMemoryOvercommit ||
		got.MemoryReserveMB != model.ReserveMemoryDefault {
		t.Errorf("budget in effect on a fresh deployment = %v/%v at %d MiB reserved, want the flags' own %v/%v at %d",
			got.CpuOvercommit, got.MemoryOvercommit, got.MemoryReserveMB,
			model.DefaultCPUOvercommit, model.DefaultMemoryOvercommit, model.ReserveMemoryDefault)
	}
	// And written to the row, so the Settings pane opens showing what
	// this deployment is actually admitting against rather than three
	// empty boxes.
	stored, err := store.GetConfig(ctx)
	if err != nil || stored == nil {
		t.Fatalf("GetConfig after seeding: (%+v, %v)", stored, err)
	}
	if stored.SandboxCPUOvercommit != model.DefaultCPUOvercommit ||
		stored.SandboxMemoryOvercommit != model.DefaultMemoryOvercommit ||
		stored.SandboxMemoryReserveMB != model.ReserveMemoryDefault {
		t.Errorf("seeded row = %v/%v at %d MiB reserved, want the flags' own %v/%v at %d",
			stored.SandboxCPUOvercommit, stored.SandboxMemoryOvercommit, stored.SandboxMemoryReserveMB,
			model.DefaultCPUOvercommit, model.DefaultMemoryOvercommit, model.ReserveMemoryDefault)
	}
}

// A stored budget beats the flags, which is the whole point of the move:
// an operator relieving a throttled deployment changes it in the pane,
// and the unit file the daemon was started from -- which on a deployment
// configured by pull request nobody has touched in months -- has no say.
func TestLoadConfigPrefersTheStoredSandboxBudgetOverTheFlags(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := budgetFlags()
	cfg.HostCPUs, cfg.HostMemoryMB = 8, 32*1024

	stored := cfg.ToModelConfig()
	stored.SandboxCPUOvercommit = 4
	stored.SandboxMemoryOvercommit = 1.5
	stored.SandboxMemoryReserveMB = 4096
	putConfig(t, store, stored)

	got, err := daemon.LoadConfig(ctx, store, cfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got.CpuOvercommit != 4 || got.MemoryOvercommit != 1.5 || got.MemoryReserveMB != 4096 {
		t.Fatalf("budget in effect = %v/%v at %d MiB reserved, want the stored 4/1.5 at 4096",
			got.CpuOvercommit, got.MemoryOvercommit, got.MemoryReserveMB)
	}
	// Down to the budget the gate and the dispatch are handed, since
	// that is what an operator is actually moving: 8 vCPU less 2 reserved
	// at 4x (24 vCPU), and 32 GiB less the stored 4 at 1.5x.
	budget := got.Budget()
	if cpus, ok := budget.CPUBudget(); !ok || cpus != 24 {
		t.Errorf("cpu budget = %v (bounded %t), want 24", cpus, ok)
	}
	if memMB, ok := budget.MemoryBudgetMB(); !ok || memMB != int(1.5*float64(32*1024-4096)) {
		t.Errorf("memory budget = %d MiB (bounded %t), want %d", memMB, ok, int(1.5*float64(32*1024-4096)))
	}
}
