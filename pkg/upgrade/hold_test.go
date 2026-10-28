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

package upgrade

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// gate stands in for the deployment's dispatch gate: Config.Hold's own
// two halves, counted, so a test can say which of them ran.
//
// The counters are guarded because the hold is taken and released on the
// upgrade's own goroutine and read from the test's.
type gate struct {
	// err, when set, is what Hold reports instead of taking anything --
	// a store that cannot be written, on a real deployment.
	err error
	// takeNothing makes Hold succeed with no release, which is what
	// orchestrator.UpgradeHold does on a deployment somebody has already
	// paused: there is nothing for the upgrade to let go of.
	takeNothing bool

	mu       sync.Mutex
	held     int
	released int
}

func (g *gate) hold(string) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return nil, g.err
	}
	g.held++
	if g.takeNothing {
		return nil, nil
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.released++
	}, nil
}

func (g *gate) counts() (held, released int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held, g.released
}

// shortRestartGrace shrinks the wait run makes after a restart command
// comes back, for the tests below that would otherwise sit through the
// full three seconds of it -- the same swap progress_test.go already
// makes, and safe for the same reason: nothing here restarts a real
// service, so there is no window for a successful restart to kill this
// process in.
func shortRestartGrace(t *testing.T) {
	t.Helper()
	old := restartGrace
	restartGrace = 10 * time.Millisecond
	t.Cleanup(func() { restartGrace = old })
}

// An upgrade that never gets as far as restarting anything leaves this
// deployment on the build it already had, and the hold has to come off:
// nothing is coming up to release it on the far side of a restart that
// is not going to happen.
func TestAFailedUpgradeLetsTheQueueGoAgain(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	g := &gate{}

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "echo no; exit 1"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Hold:        g.hold,
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseFailed)
	waitForIdle(t, u)

	if held, released := g.counts(); held != 1 || released != 1 {
		t.Errorf("held %d, released %d; a failed upgrade must let the queue go", held, released)
	}
}

// A deployment with no restart command builds and installs and then
// carries on serving from the process that did it -- so that process is
// the one that has to open the gate again. Nothing else ever will.
func TestAnUpgradeWithNothingToRestartLetsTheQueueGoItself(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	g := &gate{}

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Hold:        g.hold,
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)

	if held, released := g.counts(); held != 1 || released != 1 {
		t.Errorf("held %d, released %d; an upgrade that restarts nothing must let the queue go",
			held, released)
	}
}

// An operator asked for an upgrade, not for a pause. A gate that will
// not close is worth a line in the upgrade's own log and nothing more --
// refusing to upgrade because of it would be the worse failure.
func TestAHoldThatCannotBeTakenDoesNotStopTheUpgrade(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	g := &gate{err: errors.New("the store is not answering")}

	// The refusal is only ever reported into the upgrade's own log --
	// deliberately, since it is not a failure of the upgrade -- so the
	// log is where a test has to read it. logToTest is where every test
	// in this package's log goes (log_test.go); this is the one that
	// reads it back.
	journal := logToTest(t)

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Hold:        g.hold,
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)

	if held, released := g.counts(); held != 0 || released != 0 {
		t.Errorf("held %d, released %d; a hold that failed has nothing to release", held, released)
	}
	if !strings.Contains(journal.String(), "could not hold the queue") {
		t.Errorf("journal = %q; want it to say the queue could not be held", journal.String())
	}
}

// The shape orchestrator.UpgradeHold takes on a deployment somebody has
// already paused: it takes no hold, hands back no release, and run must
// neither call it nor trip over it.
func TestAnUpgradeWithNoHoldToReleaseFinishesAnyway(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	g := &gate{takeNothing: true}

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Hold:        g.hold,
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)

	if _, released := g.counts(); released != 0 {
		t.Errorf("released %d times; there was no hold to release", released)
	}
}

// A restart command that succeeds is expected to take this process with
// it, and then the hold is the next startup's to release. A process
// still here afterwards restarted something that was not this
// deployment -- and then nobody else is coming to open the gate, so the
// upgrade has to.
func TestARestartThatDoesNotEndThisProcessStillLetsTheQueueGo(t *testing.T) {
	shortRestartGrace(t)
	_, checkout := newFixtureRepo(t, "feature")
	g := &gate{}
	restartMarker := filepath.Join(t.TempDir(), "restarted")

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Hold:        g.hold,
		RestartCmd:  []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})

	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForFile(t, restartMarker)
	waitForIdle(t, u)

	if held, released := g.counts(); held != 1 || released != 1 {
		t.Errorf("held %d, released %d; a restart that left this process running must let the queue go",
			held, released)
	}
}
