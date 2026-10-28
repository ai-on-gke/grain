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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What an upgrade says about itself while it runs (progress.go,
// grain/task-37). The Upgrade pane polls Status and has nothing else to
// go on, so everything it can show has to be in there: the plan, which
// step is running, and what that step's own command last printed.

// stepByName is the step called name, or a fatal test failure -- every
// assertion below is about one particular step of the plan.
func stepByName(t *testing.T, status Status, name string) Step {
	t.Helper()
	step, ok := findStep(status, name)
	if !ok {
		t.Fatalf("no step named %q in %v", name, stepNames(status))
	}
	return step
}

// findStep is stepByName for a poll loop, where a status read in the
// moment between Start returning and run writing the plan legitimately
// has no steps in it yet.
func findStep(status Status, name string) (Step, bool) {
	for _, step := range status.Steps {
		if step.Name == name {
			return step, true
		}
	}
	return Step{}, false
}

func stepNames(status Status) []string {
	var names []string
	for _, step := range status.Steps {
		names = append(names, step.Name+"="+string(step.State))
	}
	return names
}

// waitForStep polls Status until the named step reaches want.
func waitForStep(t *testing.T, u *Upgrader, name string, want StepState) Status {
	t.Helper()
	var last Status
	if waitFor(func() bool {
		status, err := u.Status()
		if err != nil {
			t.Fatal(err)
		}
		last = status
		step, ok := findStep(status, name)
		return ok && step.State == want
	}) {
		return last
	}
	t.Fatalf("timed out after %s waiting for step %q to be %q; the plan then read %v",
		waitTimeout, name, want, stepNames(last))
	return Status{}
}

func TestStatusCarriesTheWholePlanFromTheStart(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")
	// The build stays where it is until the assertions below have read
	// the plan out of the status file -- an upgrade still going is the
	// state the whole plan exists for (latch).
	building := newLatch(t)
	defer building.open()

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", building.wait() + "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  statusFile,
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// While the build runs: the steps still to come are already listed,
	// so the pane can say "step 2 of 5" rather than only naming what has
	// already happened.
	running := waitForStep(t, u, stepBuild, StepRunning)
	if got := len(running.Steps); got != 5 {
		t.Fatalf("plan = %v, want five steps", stepNames(running))
	}
	if got := stepByName(t, running, stepCheckout).State; got != StepDone {
		t.Errorf("checkout = %q while the build runs, want %q", got, StepDone)
	}
	if got := stepByName(t, running, stepInstall).State; got != StepPending {
		t.Errorf("install = %q while the build runs, want %q", got, StepPending)
	}
	if got := stepByName(t, running, stepBuild).StartedAt; got == nil {
		t.Error("the running build step carries no start time to count from")
	}
	if !strings.Contains(running.Detail, stepBuild) {
		t.Errorf("detail = %q while the build runs, want it to name the step", running.Detail)
	}

	building.open()
	final := waitForPhase(t, u, PhaseOK)
	if got := stepByName(t, final, stepBuild).State; got != StepDone {
		t.Errorf("build = %q after a successful upgrade, want %q", got, StepDone)
	}
	if got := stepByName(t, final, stepBuild).FinishedAt; got == nil {
		t.Error("the finished build step carries no finish time")
	}
	// The health check is off on this Upgrader (no HealthCheckArgs), and
	// that is reported rather than left out: a step missing from the
	// plan and a step deliberately not run look nothing alike to an
	// operator wondering whether the new binary was ever tried.
	health := stepByName(t, final, stepHealthCheck)
	if health.State != StepSkipped {
		t.Errorf("health check = %q, want %q", health.State, StepSkipped)
	}
	if health.Detail == "" {
		t.Error("a skipped step does not say why it was skipped")
	}
	// The restart is absent entirely, because this deployment has no
	// restart command to plan for.
	for _, step := range final.Steps {
		if step.Name == stepRestart {
			t.Errorf("plan = %v, want no restart step without a RestartCmd", stepNames(final))
		}
	}
}

func TestARunningStepShowsWhatItsCommandLastPrinted(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	// The build prints its first line and then stops there until the
	// poll below has seen that line in the status file. That line
	// reaches the file on progress's own flush loop rather than as it is
	// printed (progress.note throttles), so the wait for it is a wait
	// for up to progressFlushInterval -- and what a latch guarantees,
	// which a sleep in the build could not, is that nothing the build
	// says next overtakes it while that flush is pending.
	building := newLatch(t)
	defer building.open()

	u := newUpgrader(t, Config{
		SrcDir: checkout,
		BuildCmd: []string{"sh", "-c",
			"echo compiling one; " + building.wait() +
				"echo compiling two; mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The pane's whole answer to "is this build moving or wedged" is
	// this line, so it has to appear while the command is still running
	// rather than once it has finished.
	var build Step
	if !waitFor(func() bool {
		status, err := u.Status()
		if err != nil {
			t.Fatal(err)
		}
		build, _ = findStep(status, stepBuild)
		return build.State == StepRunning && build.Detail == "compiling one"
	}) {
		t.Fatalf("the build step never reported its output within %s: %+v", waitTimeout, build)
	}
	building.open()
	waitForPhase(t, u, PhaseOK)
}

func TestAFailedStepIsTheOneMarkedFailed(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "echo 'no rule to make target' >&2; exit 2"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitForPhase(t, u, PhaseFailed)
	build := stepByName(t, final, stepBuild)
	if build.State != StepFailed {
		t.Errorf("build = %q after a failed build, want %q", build.State, StepFailed)
	}
	if !strings.Contains(build.Detail, "no rule to make target") {
		t.Errorf("the failed step's detail = %q, want the command's own complaint in it", build.Detail)
	}
	// The step before it stays done, and the ones after stay pending:
	// where an upgrade stopped is as much of the story as that it did.
	if got := stepByName(t, final, stepCheckout).State; got != StepDone {
		t.Errorf("checkout = %q, want %q", got, StepDone)
	}
	if got := stepByName(t, final, stepInstall).State; got != StepPending {
		t.Errorf("install = %q, want it never to have started", got)
	}
}

func TestAFailureReportsTheEndOfALongBuildRatherThanAllOfIt(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")

	u := newUpgrader(t, Config{
		SrcDir: checkout,
		// A build that says a great deal before it fails -- the shape of
		// a real `make container-build`, whose whole output used to be
		// pasted into the status file and from there into a browser.
		BuildCmd: []string{"sh", "-c",
			"i=0; while [ $i -lt 400 ]; do echo \"line $i of a very long build log\"; i=$((i+1)); done; exit 1"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(final.Detail, "line 399 of a very long build log") {
		t.Errorf("detail = %q, want the end of the build's output in it", final.Detail)
	}
	if strings.Contains(final.Detail, "line 0 of a very long build log") {
		t.Errorf("detail = %q, want it bounded to the tail rather than the whole log", final.Detail)
	}
	if len(final.Detail) > 8000 {
		t.Errorf("detail is %d bytes -- too much to put in a status pane", len(final.Detail))
	}
}

func TestARestartThatFailsIsNotAnUpgradeThatSucceeded(t *testing.T) {
	// A restart command that comes back with an error means this process
	// is still here, which means the deployment is still running the old
	// build -- the one failure mode the pane used to report as "ok;
	// restarting" forever.

	// restartGrace is a package-level var, and the goroutine that reads
	// it is this upgrade's own run -- so putting it back has to wait
	// until that goroutine is gone, or the two are an unsynchronised
	// write and read of the same variable for -race to find. Restoring
	// it from a cleanup registered before newUpgrader's does exactly
	// that: cleanups run last-registered-first, so the wait for the run
	// to finish happens first.
	old := restartGrace
	restartGrace = 10 * time.Millisecond
	t.Cleanup(func() { restartGrace = old })

	_, checkout := newFixtureRepo(t, "feature")
	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		RestartCmd:  []string{"sh", "-c", "echo 'Failed to restart grain-daemon.service' >&2; exit 1"},
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(final.Detail, "restart") {
		t.Errorf("detail = %q, want it to say the restart is what failed", final.Detail)
	}
	if !strings.Contains(final.Detail, "still running the old one") {
		t.Errorf("detail = %q, want it to say the deployment did not move", final.Detail)
	}
	// And what the restart command itself said, which is the only part of
	// this that names the actual reason -- the restart runs through
	// runLogged like every other step, so its output reaches the log and
	// the error rather than being thrown away.
	if !strings.Contains(final.Detail, "Failed to restart grain-daemon.service") {
		t.Errorf("detail = %q, want it to carry what the restart command printed", final.Detail)
	}
	if got := stepByName(t, final, stepRestart).State; got != StepFailed {
		t.Errorf("restart = %q, want %q", got, StepFailed)
	}
	// The build is installed either way: nothing about a failed restart
	// undoes the staging that led up to it.
	if _, err := os.Stat(u.cfg.InstallPath); err != nil {
		t.Errorf("the binary this upgrade installed is gone: %v", err)
	}
}

// TestARestartThatHangsIsBoundedLikeEveryOtherStep is the restart's half
// of bwsalmon/agents#633. Checkout and build were put under a context
// then; the restart was not, and it ran through exec.CommandContext and
// CombinedOutput rather than through runLogged -- so a cancellation
// killed only the direct child and left the read of a pipe some
// surviving grandchild still held open blocking indefinitely. `sudo
// systemctl restart` is that shape (sudo forks systemctl), and a restart
// that wedged rather than failing meant run never returned: the Upgrader
// was never released, so every later Start got ErrUpgradeInProgress, and
// the pane sat on "restarting" for as long as the process lived.
//
// The command below is that shape and nothing else: `wait` keeps the
// shell alive while a background child holds the inherited pipe, so
// killing the shell alone leaves the read outstanding. Reaching
// PhaseFailed at all is the assertion -- without the bound this test
// hangs for the full sleep rather than failing.
func TestARestartThatHangsIsBoundedLikeEveryOtherStep(t *testing.T) {
	// Both package-level vars, restored the same way and for the same
	// reason TestARestartThatFailsIsNotAnUpgradeThatSucceeded explains:
	// the cleanup registered before newUpgrader's runs after the wait for
	// the run goroutine to be gone.
	//
	// Small numbers, and not a window anything here has to catch: what is
	// asserted is the failure the bound produces, and a bound that
	// expires early on a loaded runner produces that same failure -- it
	// kills a restart that had not got going yet rather than one that
	// had, which is the same PhaseFailed carrying the same "ran out of
	// time". So a busy machine can make this test prove less than it
	// means to, but it cannot make it fail; anyone here hunting a flake
	// in this package should look at what a test has to *observe* while
	// an upgrade runs rather than at what it bounds (latch's comment in
	// upgrade_test.go).
	oldGrace, oldTimeout := restartGrace, restartTimeout
	restartGrace = 10 * time.Millisecond
	restartTimeout = 100 * time.Millisecond
	t.Cleanup(func() { restartGrace, restartTimeout = oldGrace, oldTimeout })

	_, checkout := newFixtureRepo(t, "feature")
	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		RestartCmd:  []string{"sh", "-c", "sleep 300 & wait"},
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(final.Detail, "restart") {
		t.Errorf("detail = %q, want it to say the restart is what failed", final.Detail)
	}
	// And why, rather than the bare "signal: killed" that cancelling it
	// produces -- an operator reading this has to be able to tell a
	// restart that refused from one that never finished.
	if !strings.Contains(final.Detail, "ran out of time") {
		t.Errorf("detail = %q, want it to say the restart was killed on its deadline", final.Detail)
	}
	if got := stepByName(t, final, stepRestart).State; got != StepFailed {
		t.Errorf("restart = %q, want %q", got, StepFailed)
	}

	// And the Upgrader is back: a hung restart that held it would leave
	// the deployment unable to ever try again, which is the whole of what
	// made this worth bounding.
	waitForIdle(t, u)
	select {
	case <-u.Done():
	default:
		t.Error("the Upgrader is still held after the restart was bounded")
	}
}

func TestTheStatusFileSurvivesOutputFasterThanItCanBeWritten(t *testing.T) {
	// progress.note throttles its writes, and the throttle is the only
	// thing between a build printing thousands of lines a second and a
	// status file rewritten thousands of times a second. What matters
	// here is that the last line still lands: a throttle that dropped
	// the final update would leave the pane showing a line from the
	// middle of the build forever.
	_, checkout := newFixtureRepo(t, "feature")
	// The build stops after its last line and stays stopped until that
	// line has been read out of the status file: the flush that carries
	// it can be a whole progressFlushInterval behind the print, and the
	// step finishing would overwrite the Detail it lands in.
	building := newLatch(t)
	defer building.open()

	u := newUpgrader(t, Config{
		SrcDir: checkout,
		BuildCmd: []string{"sh", "-c",
			"i=0; while [ $i -lt 2000 ]; do echo \"chatter $i\"; i=$((i+1)); done; " +
				"echo 'the last thing this build said'; " + building.wait() +
				"mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var build Step
	if !waitFor(func() bool {
		status, err := u.Status()
		if err != nil {
			t.Fatal(err)
		}
		build, _ = findStep(status, stepBuild)
		return build.Detail == "the last thing this build said"
	}) {
		t.Fatalf("the build step's last output line never reached the status file within %s; it said %q",
			waitTimeout, build.Detail)
	}
	building.open()
	waitForPhase(t, u, PhaseOK)
}

func TestStepDetailIsBoundedForOneAbsurdlyLongLine(t *testing.T) {
	// A progress of its own rather than a whole upgrade, so this is what
	// claims the log for this test (log_test.go) -- every transition
	// below logs a line.
	logToTest(t)
	// A single output line is not something this package controls, and
	// the status file is read by a browser.
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")
	p := newProgress(statusFile, "main", Status{Branch: "main", Phase: PhaseRunning}, []string{stepBuild})
	// Nothing here runs an upgrade, so nothing else would ever stop the
	// flush loop this started -- and a loop still ticking after the test
	// returns is one more thing that can write into t.TempDir while it
	// is being deleted.
	defer p.stop()
	p.start(stepBuild)
	p.done(stepBuild, strings.Repeat("x", 10_000))
	status, err := readStatus(statusFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(status.Steps[0].Detail); got > maxStepDetail+len("…") {
		t.Errorf("step detail is %d bytes, want it capped near %d", got, maxStepDetail)
	}
}

func TestFormattingHelpers(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "2s"},
		{90 * time.Second, "1m30s"},
	} {
		if got := shortDuration(tc.d); got != tc.want {
			t.Errorf("shortDuration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if got := firstLine("first\nsecond"); got != "first" {
		t.Errorf("firstLine = %q, want %q", got, "first")
	}
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Errorf("truncate = %q, want %q", got, "abc…")
	}
	if got := elapsed(nil, time.Now()); got == "" {
		t.Error("elapsed on a step that never started should still say something")
	}
	if got := fmt.Sprint(StepDone); got != "done" {
		t.Errorf("StepDone = %q", got)
	}
}
