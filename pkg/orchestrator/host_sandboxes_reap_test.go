package orchestrator_test

// The startup sweep for host-directory sandboxes.
//
// HostSandboxes creates a directory for a run and removes it with the
// run, so a directory nothing holds is the residue of a process that
// died before its Release could run -- which is the ordinary outcome of
// an upgrade or a restart landing on a run in flight, not a rare one.
// Nothing removed those before ReapOrphans existed, and each one is a
// whole checkout: enough of them fill the filesystem the next run's own
// sandbox has to be created on, and then every task fails at setup with
// "no space left on device" until somebody clears it by hand. These
// tests pin down that the sweep clears exactly that residue and nothing
// a running deployment still needs.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestHostSandboxesReapOrphansRemovesWhatAPreviousProcessLeft(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	// Two runs' worth of leftovers, one of them with a checkout under it
	// -- the reap has to take the whole tree, not just an empty directory.
	for _, name := range []string{"1-1", "2-3"} {
		if err := os.MkdirAll(filepath.Join(base, name, "repo", ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "2-3", "repo", "big.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	h := orchestrator.NewHostSandboxes(base)
	reaped, err := h.ReapOrphans(context.Background())
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if reaped != 2 {
		t.Errorf("reaped = %d, want 2", reaped)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%q still holds %d entrie(s) after the reap, want none", base, len(entries))
	}
}

// The live check, which is what keeps the sweep from being a foot-gun if
// it is ever called anywhere but startup: a sandbox this process is
// holding is a running task's working tree, and removing it would fail
// the run rather than reclaim anything.
func TestHostSandboxesReapOrphansLeavesALiveSandboxAlone(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "9-1"), 0o755); err != nil {
		t.Fatal(err)
	}

	h := orchestrator.NewHostSandboxes(base)
	if _, err := h.Acquire(context.Background(), "10-1", orchestrator.Shape{}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	reaped, err := h.ReapOrphans(context.Background())
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if reaped != 1 {
		t.Errorf("reaped = %d, want 1 (the leftover only)", reaped)
	}
	if _, err := os.Stat(filepath.Join(base, "10-1")); err != nil {
		t.Errorf("the live run's own sandbox was reaped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "9-1")); !os.IsNotExist(err) {
		t.Errorf("the leftover survived the reap (stat err = %v)", err)
	}
}

// A deployment's first start has no sandbox directory yet -- cmd/grain
// creates it right after this, and there is nothing to sweep from a
// directory that does not exist.
func TestHostSandboxesReapOrphansIgnoresAMissingBaseDir(t *testing.T) {
	t.Parallel()
	base := filepath.Join(t.TempDir(), "not-created-yet")

	reaped, err := orchestrator.NewHostSandboxes(base).ReapOrphans(context.Background())
	if err != nil {
		t.Fatalf("ReapOrphans on a missing base dir: %v", err)
	}
	if reaped != 0 {
		t.Errorf("reaped = %d, want 0", reaped)
	}
}
