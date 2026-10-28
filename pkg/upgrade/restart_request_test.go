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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// An upgrade says, just before its restart command runs, that the
// restart is coming -- which is how the daemon tells the start that
// follows apart from a crash (pkg/daemon's restart requests, grain task
// 9). The hook runs before the command, never after it: on a real
// deployment nothing after the command runs at all.
func TestAnUpgradeAnnouncesItsRestartBeforeRunningIt(t *testing.T) {
	shortRestartGrace(t)
	_, checkout := newFixtureRepo(t, "feature")
	restartMarker := filepath.Join(t.TempDir(), "restarted")
	statusFile := filepath.Join(t.TempDir(), "upgrade-status.json")

	var mu sync.Mutex
	var announced []string
	markerExisted := false
	u := newUpgrader(t, Config{
		SrcDir:      checkout,
		BuildCmd:    []string{"sh", "-c", "mkdir -p bin && printf fake-binary > bin/grain"},
		BuiltBinary: filepath.Join(checkout, "bin", "grain"),
		InstallPath: filepath.Join(t.TempDir(), "grain"),
		RestartCmd:  []string{"sh", "-c", "touch " + restartMarker},
		BeforeRestart: func(branch string) {
			mu.Lock()
			defer mu.Unlock()
			announced = append(announced, branch)
			if _, err := os.Stat(restartMarker); err == nil {
				markerExisted = true
			}
		},
		StatusFile: statusFile,
	})
	if err := u.Start("feature"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, u, PhaseOK)
	waitForFile(t, restartMarker)

	mu.Lock()
	defer mu.Unlock()
	if len(announced) != 1 || announced[0] != "feature" {
		t.Fatalf("BeforeRestart saw %v, want exactly one call for the branch being cut over to", announced)
	}
	if markerExisted {
		t.Error("BeforeRestart ran after the restart command, want it before")
	}

	// And the status file it leaves behind says the same, for a daemon
	// built before the hook existed to have written anything else.
	status, err := ReadStatus(statusFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := status.RestartRequestedAt(); !ok {
		t.Errorf("status %+v does not read as an upgrade that restarted", status)
	}
}

func TestRestartRequestedAtIsOnlyASuccessfulUpgradesRestart(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 10, 17, 18, 8, 31, 0, time.UTC)
	finished := started.Add(-time.Second)
	for _, tc := range []struct {
		name   string
		status Status
		want   bool
	}{
		{"restart step started", Status{Phase: PhaseOK, FinishedAt: &finished, Steps: []Step{
			{Name: stepInstall, State: StepDone}, {Name: stepRestart, State: StepRunning, StartedAt: &started},
		}}, true},
		{"restart step failed", Status{Phase: PhaseOK, FinishedAt: &finished, Steps: []Step{
			{Name: stepRestart, State: StepFailed, StartedAt: &started},
		}}, false},
		{"restart step never reached", Status{Phase: PhaseOK, FinishedAt: &finished, Steps: []Step{
			{Name: stepRestart, State: StepPending},
		}}, false},
		{"no restart step at all", Status{Phase: PhaseOK, FinishedAt: &finished, Detail: "installed", Steps: []Step{
			{Name: stepInstall, State: StepDone},
		}}, false},
		{"failed upgrade", Status{Phase: PhaseFailed, FinishedAt: &finished, Steps: []Step{
			{Name: stepRestart, State: StepRunning, StartedAt: &started},
		}}, false},
		{"older status without steps", Status{Phase: PhaseOK, FinishedAt: &finished, Detail: "installed abc; restarting"}, true},
		{"no upgrade ever", Status{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := tc.status.RestartRequestedAt(); got != tc.want {
				t.Errorf("RestartRequestedAt ok = %v, want %v", got, tc.want)
			}
		})
	}
}
