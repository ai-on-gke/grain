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
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// flusher stands in for the daemon's run-host flush (Config.Flush),
// recording the order things happened in.
type flusher struct {
	mu     sync.Mutex
	events []string
	marker string
}

func (f *flusher) note(e string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *flusher) flush(context.Context) (func(), error) {
	// The restart has not run yet when the cursors are flushed.
	if _, err := os.Stat(f.marker); err == nil {
		f.note("flushed after the restart")
	} else {
		f.note("flushed")
	}
	return func() { f.note("resumed") }, nil
}

func (f *flusher) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// The upgrade protocol's step 3 (docs/non-disruptive-upgrade.md, piece
// 6): every run's spool cursor is flushed after the build is staged and
// before the restart -- and a restart that leaves this process running
// hands the runs back to it, as it hands back the queue.
func TestAnUpgradeFlushesBeforeItRestartsAndResumesIfItIsStillHere(t *testing.T) {
	shortRestartGrace(t)
	_, checkout := newFixtureRepo(t, "feature")
	g := &gate{}
	restartMarker := filepath.Join(t.TempDir(), "restarted")
	f := &flusher{marker: restartMarker}

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Hold:        g.hold,
		Flush:       f.flush,
		RestartCmd:  []string{"sh", "-c", "touch " + restartMarker},
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForFile(t, restartMarker)
	waitForIdle(t, u)

	if got := f.got(); len(got) != 2 || got[0] != "flushed" || got[1] != "resumed" {
		t.Fatalf("flush events = %v, want [flushed resumed]", got)
	}
	if held, released := g.counts(); held != 1 || released != 1 {
		t.Errorf("held %d, released %d", held, released)
	}
}

// A build that never stages never restarts, so nothing is flushed: the
// runs carry on under this daemon without so much as a pause in the tail.
func TestAFailedUpgradeFlushesNothing(t *testing.T) {
	_, checkout := newFixtureRepo(t, "feature")
	f := &flusher{marker: filepath.Join(t.TempDir(), "restarted")}
	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "exit 1"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Flush:       f.flush,
		RestartCmd:  []string{"true"},
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseFailed)
	waitForIdle(t, u)
	if got := f.got(); len(got) != 0 {
		t.Fatalf("flush events = %v, want none", got)
	}
}

// kubectlAround wraps a fake kubectl: the Deployment's current image is
// answered with image, and `set image` exits setImageExit. Everything
// else goes to the fake it wraps.
func kubectlAround(t *testing.T, kubectl, image string, setImageExit int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *'get deployment grain -o go-template='*) \"" + kubectl + "\" \"$@\" >/dev/null; printf '%s' '" + image + "'; exit 0 ;;\n" +
		"  *'set image'*) \"" + kubectl + "\" \"$@\"; exit " + strconv.Itoa(setImageExit) + " ;;\n" +
		"esac\n" +
		"exec \"" + kubectl + "\" \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the kubectl wrapper: %v", err)
	}
	return path
}

// kubeFlusher records a flush, and whether the Deployment had already
// been repointed when it came.
func kubeFlusher(f *flusher, calls func() string) func(context.Context) (func(), error) {
	return func(context.Context) (func(), error) {
		if strings.Contains(calls(), "set image") {
			f.note("flushed after the rollout began")
		} else {
			f.note("flushed")
		}
		return func() { f.note("resumed") }, nil
	}
}

// The Kubernetes path has no restart command: repointing the Deployment
// is the restart. So its flush comes inside staging, before the
// repointing, and nothing resumes it -- the next pod adopts the runs.
func TestAKubeUpgradeFlushesBeforeItRepointsTheDeployment(t *testing.T) {
	fake, calls, _ := fakeKubectl(t, kubeSucceeded, "")
	kubectl := kubectlAround(t, fake, "example/grain:old@sha256:0ld", 0)
	f := &flusher{}
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Flush = kubeFlusher(f, calls)
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)
	if !strings.Contains(calls(), "set image") {
		t.Fatalf("calls = %q, want the deployment repointed", calls())
	}
	if got := f.got(); len(got) != 1 || got[0] != "flushed" {
		t.Fatalf("flush events = %v, want [flushed]", got)
	}
}

// A repointing the API server refused starts no rollout, so this daemon
// is staying: the runs it let go of are handed back to it.
func TestAKubeUpgradeWhoseRepointingFailsResumesTheRuns(t *testing.T) {
	fake, calls, _ := fakeKubectl(t, kubeSucceeded, "")
	kubectl := kubectlAround(t, fake, "example/grain:old@sha256:0ld", 1)
	f := &flusher{}
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Flush = kubeFlusher(f, calls)
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseFailed)
	waitForIdle(t, u)
	if got := f.got(); len(got) != 2 || got[0] != "flushed" || got[1] != "resumed" {
		t.Fatalf("flush events = %v, want [flushed resumed]", got)
	}
}

// `kubectl set image` to the image the Deployment already runs rolls
// nothing out, and no new pod would come to adopt the runs: nothing is
// flushed.
func TestAKubeUpgradeToTheImageAlreadyRunningFlushesNothing(t *testing.T) {
	fake, calls, _ := fakeKubectl(t, kubeSucceeded, "")
	kubectl := kubectlAround(t, fake, "example/grain:main@"+kubeDigest, 0)
	f := &flusher{}
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Flush = kubeFlusher(f, calls)
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)
	if got := f.got(); len(got) != 0 {
		t.Fatalf("flush events = %v, want none", got)
	}
}

// A restart that only asks for one -- the container install's touch of
// control/restart, which a systemd path unit turns into a stop of this
// container -- comes back at once, and the stop it starts arrives a moment
// later and then takes minutes to drain. This process is then still
// running after restartGrace, but it is being stopped, and must not reopen
// the queue or resume the runs it flushed: the next startup does both
// (task 1504).
func TestARestartThatStopsThisProcessLaterLeavesTheQueueHeld(t *testing.T) {
	shortRestartGrace(t)
	_, checkout := newFixtureRepo(t, "feature")
	g := &gate{}
	restartMarker := filepath.Join(t.TempDir(), "restarted")
	f := &flusher{marker: restartMarker}
	stopping := make(chan struct{})

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Hold:        g.hold,
		Flush:       f.flush,
		RestartCmd:  []string{"sh", "-c", "touch " + restartMarker},
		Stopping:    stopping,
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForFile(t, restartMarker)
	// Well past restartGrace, as systemd's stop was in task 1504.
	time.Sleep(20 * restartGrace)
	close(stopping)
	waitForIdle(t, u)

	if got := f.got(); len(got) != 1 || got[0] != "flushed" {
		t.Fatalf("flush events = %v, want [flushed]: a daemon being stopped must not resume its runs", got)
	}
	if held, released := g.counts(); held != 1 || released != 0 {
		t.Errorf("held %d, released %d; a daemon being stopped must leave the hold for the next startup", held, released)
	}
}

// A restart command that fails because the restart killed it ("signal:
// terminated") is the restart under way too, when this process is being
// stopped: nothing is resumed and the restart is not reported as failed.
func TestAFailedRestartCommandWhileStoppingLeavesTheQueueHeld(t *testing.T) {
	shortRestartGrace(t)
	_, checkout := newFixtureRepo(t, "feature")
	g := &gate{}
	f := &flusher{marker: filepath.Join(t.TempDir(), "restarted")}
	stopping := make(chan struct{})
	close(stopping)

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Hold:        g.hold,
		Flush:       f.flush,
		RestartCmd:  []string{"sh", "-c", "exit 1"},
		Stopping:    stopping,
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)

	if got := f.got(); len(got) != 1 || got[0] != "flushed" {
		t.Fatalf("flush events = %v, want [flushed]", got)
	}
	if held, released := g.counts(); held != 1 || released != 0 {
		t.Errorf("held %d, released %d; want the hold left for the next startup", held, released)
	}
	if status, err := u.Status(); err != nil || status.Phase != PhaseOK {
		t.Errorf("status = %+v, %v; a restart under way is not a failed one", status, err)
	}
}

// A restart that succeeded but never stops this process (it restarted
// something else, or nothing) still hands the queue and the runs back
// once restartSettle runs out, even with Stopping wired.
func TestARestartThatNeverStopsThisProcessResumesAfterRestartSettle(t *testing.T) {
	shortRestartGrace(t)
	old := restartSettle
	restartSettle = 50 * time.Millisecond
	t.Cleanup(func() { restartSettle = old })
	_, checkout := newFixtureRepo(t, "feature")
	g := &gate{}
	restartMarker := filepath.Join(t.TempDir(), "restarted")
	f := &flusher{marker: restartMarker}

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Hold:        g.hold,
		Flush:       f.flush,
		RestartCmd:  []string{"sh", "-c", "touch " + restartMarker},
		Stopping:    make(chan struct{}),
		StatusFile:  filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)

	if got := f.got(); len(got) != 2 || got[0] != "flushed" || got[1] != "resumed" {
		t.Fatalf("flush events = %v, want [flushed resumed]", got)
	}
	if held, released := g.counts(); held != 1 || released != 1 {
		t.Errorf("held %d, released %d", held, released)
	}
}

// Nothing reads the upgrade as done while the outgoing daemon is still
// flushing: PhaseOK is what the upgrade check takes for "a new build is
// up", and written before the flush it let the process about to be
// restarted away file that check itself (task 1937). By the time the
// restart command runs the status says succeeded, with the restart step
// started, so the process the restart brings up does file it.
func TestAnUpgradeIsNotReportedSucceededUntilItsRestartBegins(t *testing.T) {
	shortRestartGrace(t)
	_, checkout := newFixtureRepo(t, "feature")
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")
	restartMarker := filepath.Join(t.TempDir(), "restarted")
	duringFlush := make(chan Status, 1)
	beforeRestart := make(chan Status, 1)

	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		Flush: func(context.Context) (func(), error) {
			status, err := ReadStatus(statusFile)
			if err != nil {
				t.Errorf("reading the status during the flush: %v", err)
			}
			duringFlush <- status
			return func() {}, nil
		},
		BeforeRestart: func(string) {
			status, err := ReadStatus(statusFile)
			if err != nil {
				t.Errorf("reading the status before the restart: %v", err)
			}
			beforeRestart <- status
		},
		RestartCmd: []string{"sh", "-c", "touch " + restartMarker},
		StatusFile: statusFile,
	})
	outgoing := time.Now().UTC().Add(-time.Hour)
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFile(t, restartMarker)
	waitForIdle(t, u)

	flushing := <-duringFlush
	if flushing.Phase != PhaseRunning || flushing.FinishedAt != nil {
		t.Errorf("during the flush the status is %s (finished %v), want still running", flushing.Phase, flushing.FinishedAt)
	}
	if flushing.RestartPending(outgoing) {
		t.Error("RestartPending during the flush: the status is not a success yet, so nothing should be read off it")
	}

	restarting := <-beforeRestart
	if restarting.Phase != PhaseOK || restarting.FinishedAt == nil {
		t.Fatalf("before the restart the status is %s (finished %v), want succeeded", restarting.Phase, restarting.FinishedAt)
	}
	for _, step := range restarting.Steps {
		if step.Name == stepRestart && step.State != StepRunning {
			t.Errorf("restart step is %s before the restart command runs, want running", step.State)
		}
	}
	if !restarting.RestartPending(outgoing) {
		t.Error("RestartPending = false for the process the restart replaces, want true")
	}
	if restarting.RestartPending(time.Now().UTC().Add(time.Second)) {
		t.Error("RestartPending = true for a process started after the restart, want false")
	}
}
