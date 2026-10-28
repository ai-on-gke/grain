// The startup sweep for whatever sandboxes a previous process left
// behind, on the host-directory backend.
//
// It existed only for granule VMs until now, which left the default
// backend -- one directory per run under -sandbox-dir -- with nothing
// that ever removed a directory whose process died before it could
// Release it. grain-daemon.service stops its container with `docker stop
// --time 30` while a run's own unwinding is allowed minutes (see
// shutdownDrain), so every upgrade or restart landing on a run in flight
// leaves a whole checkout on disk. Enough of those fill the filesystem
// -sandbox-dir sits on, and then every task fails at setup with "no
// space left on device" before its agent ever starts.
//
// This test drives the daemon the way run() does and asserts the
// leftover is gone, which is the wiring (a host-backed deployment is
// swept at all) rather than the sweep itself --
// pkg/orchestrator's own TestHostSandboxesReapOrphans* cover that.
package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
)

func TestRunReapsHostSandboxesLeftByAPreviousProcess(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedTestConnections(t, dataDir, "git:\n  - credential: anonymous\n")

	// A run's directory as a killed process would have left it: named
	// for the run ("<task id>-<attempt>", dispatch.RunID) with a
	// checkout under it.
	sandboxDir := t.TempDir()
	orphan := filepath.Join(sandboxDir, "7-2")
	if err := os.MkdirAll(filepath.Join(orphan, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, daemon.Config{
			DataDir: dataDir, SandboxDir: sandboxDir, MaxWorkers: 1, PollInterval: time.Hour,
			GithubHost: "127.0.0.1:0", GithubInsecureHTTP: true, Actor: "tester",
		})
	}()

	deadline := time.Now().Add(daemonWaitLimit)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(orphan); os.IsNotExist(err) {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatalf("%q survived the daemon's startup (run() returned %v); a host-backed deployment never sweeps "+
		"what a killed process left, so its sandbox disk only ever fills", orphan, <-done)
}
