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

package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// fakeOOMReporter is a sandbox that reports whatever it is told to.
type fakeOOMReporter struct {
	killed bool
	asked  int
}

func (f *fakeOOMReporter) SandboxOOM(context.Context) (string, bool) {
	f.asked++
	if !f.killed {
		return "", false
	}
	return "the sandbox container was OOM-killed (docker State.OOMKilled, exit 137)", true
}

// An agent run whose sandbox the kernel OOM-killed is recorded as
// model.OOMOutcome: the task stays queued with no strike against it, its
// conversation says why, the overcommit controller hears of it, the
// second kill in a row doubles the task's memory, and a kill past
// model.MaxOOMRetries is an ordinary failure again.
func TestAnOOMKilledAgentRunIsRequeuedWithoutAStrike(t *testing.T) {
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, preemptTask("t1", "main")); err != nil {
		t.Fatal(err)
	}
	var noted []time.Time
	reporter := &fakeOOMReporter{killed: true}
	cfg := Config{
		Now:                    func() time.Time { return now },
		NoteOOM:                func(at time.Time) { noted = append(noted, at) },
		sandboxOOM:             reporter,
		sandboxDefaultMemoryMB: 8192,
	}
	attempt := 0
	run := func() runEnding {
		t.Helper()
		attempt++
		id := "t1-" + string(rune('0'+attempt))
		at := now.Add(time.Duration(attempt) * time.Minute)
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: "t1", Attempt: attempt, StartedAt: at}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		task, err := store.GetTask(ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		end := applySandboxOOM(ctx, store, cfg, *task, id,
			runEnding{outcome: "failed", detail: "transport: EOF", runErr: errors.New("transport: EOF")})
		if err := store.FinishRun(ctx, id, at.Add(30*time.Second), end.outcome, end.detail); err != nil {
			t.Fatal(err)
		}
		return end
	}
	requeued := func(when string) {
		t.Helper()
		if state, err := store.State(ctx, "t1"); err != nil {
			t.Fatal(err)
		} else if state != model.StateQueued {
			t.Errorf("%s: state = %q, want queued", when, state)
		}
		streak, err := store.FailureStreak(ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		if streak == nil || streak.Count != 0 || streak.BackoffCount != 0 {
			t.Errorf("%s: FailureStreak = %+v, want no strike and no backoff", when, streak)
		}
	}
	memory := func() int {
		t.Helper()
		task, err := store.GetTask(ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		return task.SandboxMemoryMB
	}

	end := run()
	if end.outcome != model.OOMOutcome || !strings.HasPrefix(end.detail, model.OOMDetailPrefix) {
		t.Fatalf("first OOM kill ended %q (%q), want %q with the OOM detail", end.outcome, end.detail, model.OOMOutcome)
	}
	if !errors.Is(end.runErr, errSandboxOOM) {
		t.Errorf("runErr = %v, want it to wrap errSandboxOOM", end.runErr)
	}
	requeued("after one OOM kill")
	if len(noted) != 1 || !noted[0].Equal(now) {
		t.Errorf("NoteOOM calls = %v, want one at %v", noted, now)
	}
	if got := memory(); got != 0 {
		t.Errorf("after one OOM kill the task asks for %d MiB, want its size left alone", got)
	}
	comments, err := store.Comments(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "sandbox was OOM-killed under memory overcommit; retrying") {
		t.Fatalf("comments = %+v, want the OOM note", comments)
	}

	run()
	requeued("after two OOM kills")
	if got := memory(); got != 16384 {
		t.Errorf("after the second OOM kill the task asks for %d MiB, want twice the 8192 MiB default", got)
	}
	comments, _ = store.Comments(ctx, "t1")
	if len(comments) != 2 || !strings.Contains(comments[1].Body, "It is the 2nd OOM kill in a row, so the task's sandbox memory has been raised from 8192 MiB to 16384 MiB.") {
		t.Errorf("second note = %+v, want it to say the memory was raised", comments)
	}

	// The third kill raises it again, to the ceiling, and its note counts
	// it as the third -- it said "second" for every raise until the
	// 2026-10-25 walkthrough (docs/test-runs/2026-10-25-sandbox-budget-oom-retry.md).
	run()
	requeued("after three OOM kills")
	if got := memory(); got != 32768 {
		t.Errorf("after the third OOM kill the task asks for %d MiB, want 32768", got)
	}
	comments, _ = store.Comments(ctx, "t1")
	if len(comments) != 3 || !strings.Contains(comments[2].Body, "It is the 3rd OOM kill in a row, so the task's sandbox memory has been raised from 16384 MiB to 32768 MiB.") {
		t.Errorf("third note = %+v, want it to count the third kill and the raise to 32768 MiB", comments)
	}

	for i := 3; i < model.MaxOOMRetries; i++ {
		run()
	}
	requeued("at the last OOM retry")
	end = run()
	if end.outcome != "failed" || !strings.Contains(end.detail, "counted as a failure") {
		t.Errorf("OOM kill past MaxOOMRetries ended %q (%q), want an ordinary failure", end.outcome, end.detail)
	}
	if streak, _ := store.FailureStreak(ctx, "t1"); streak == nil || streak.Count != 1 {
		t.Errorf("FailureStreak after the OOM kill past the limit = %+v, want one strike", streak)
	}
	if len(noted) != model.MaxOOMRetries+1 {
		t.Errorf("NoteOOM was called %d times, want once per kill (%d)", len(noted), model.MaxOOMRetries+1)
	}
}

// raiseMemoryAfterOOM never takes a task past oomMemoryCeilingFactor
// times its backend's default, however many OOM streaks it has: every
// ordinary failure in between starts a new one, and part of what counts
// as an OOM kill is the guest's own word.
func TestRaiseMemoryAfterOOMStopsAtTheCeiling(t *testing.T) {
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	if err := store.PutTask(ctx, preemptTask("t1", "main")); err != nil {
		t.Fatal(err)
	}
	cfg := Config{sandboxDefaultMemoryMB: 8192}
	for i, want := range []int{16384, 32768, 32768, 32768} {
		task, err := store.GetTask(ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		note := raiseMemoryAfterOOM(ctx, store, cfg, *task, "r1", i+2)
		task, _ = store.GetTask(ctx, "t1")
		if task.SandboxMemoryMB != want {
			t.Errorf("raise %d: task asks for %d MiB, want %d", i+1, task.SandboxMemoryMB, want)
		}
		if (note == "") != (i >= 2) {
			t.Errorf("raise %d: note = %q, want one only when the memory changed", i+1, note)
		}
	}
}

// A run whose sandbox was not OOM-killed keeps its own ending, and a run
// that ended any way but an ordinary failure is not even asked.
func TestApplySandboxOOMLeavesOtherEndingsAlone(t *testing.T) {
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	reporter := &fakeOOMReporter{}
	cfg := Config{sandboxOOM: reporter, NoteOOM: func(time.Time) { t.Error("NoteOOM called for a sandbox that was not killed") }}
	failed := runEnding{outcome: "failed", detail: "boom"}
	if got := applySandboxOOM(ctx, store, cfg, preemptTask("t1", "main"), "r1", failed); got.outcome != "failed" || got.detail != "boom" {
		t.Errorf("not killed: ending = %+v, want it unchanged", got)
	}
	reporter.killed = true
	for _, outcome := range []string{"succeeded", "cancelled", model.InterruptedOutcome, model.CapacityOutcome} {
		before := reporter.asked
		got := applySandboxOOM(ctx, store, cfg, preemptTask("t1", "main"), "r1", runEnding{outcome: outcome})
		if got.outcome != outcome || reporter.asked != before {
			t.Errorf("%s: ending = %+v, asked %d times; want it unchanged and the sandbox not asked", outcome, got, reporter.asked-before)
		}
	}
}

// Granule: the VM process OOM-killed or SIGKILLed, or the guest kernel's
// own OOM message on the console.
func TestGranuleOOMDetection(t *testing.T) {
	const console = "[  12.3] systemd[1]: Started foo.\n" +
		"[ 812.1] go invoked oom-killer: gfp_mask=0x140cca(GFP_HIGHUSER_MOVABLE|__GFP_COMP), order=0\n" +
		"[ 812.2] Out of memory: Killed process 4242 (go) total-vm:9000000kB, anon-rss:7000000kB\n" +
		"[ 812.3] oom_reaper: reaped process 4242 (go)\n"
	for _, tc := range []struct {
		name    string
		exit    granule.ContainerExit
		exitErr error
		console string
		want    string
	}{
		{"oom-killed vm process", granule.ContainerExit{Status: "exited", ExitCode: 137, OOMKilled: true}, nil, "", "VM process was OOM-killed"},
		{"sigkilled vm process", granule.ContainerExit{Status: "exited", ExitCode: 137}, nil, "", "SIGKILL"},
		{"guest kernel", granule.ContainerExit{Status: "running"}, nil, console, "guest kernel reported an OOM kill"},
		{"guest kernel, container unreadable", granule.ContainerExit{}, errors.New("no such container"), console, "guest kernel reported"},
		{"healthy", granule.ContainerExit{Status: "running"}, nil, "[ 1.0] booted\n", ""},
		{"exited cleanly", granule.ContainerExit{Status: "exited", ExitCode: 0}, nil, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := &GranuleSandboxes{
				vmExit: func(_ context.Context, vm string) (granule.ContainerExit, error) {
					if vm != "vm-1" {
						t.Errorf("inspected %q, want vm-1", vm)
					}
					return tc.exit, tc.exitErr
				},
				consoleLog: func(context.Context, string) (string, error) { return tc.console, nil },
			}
			sb := &granuleSandbox{owner: k, name: "r1", vmName: "vm-1"}
			how, killed := sb.SandboxOOM(context.Background())
			if killed != (tc.want != "") || !strings.Contains(how, tc.want) {
				t.Errorf("SandboxOOM = %q, %v; want killed=%v mentioning %q", how, killed, tc.want != "", tc.want)
			}
			// The console is the guest's to write; nothing of it
			// reaches the conversation note or a CI job's warning.
			if strings.Contains(how, "4242") {
				t.Errorf("SandboxOOM = %q, quotes the guest's console", how)
			}
		})
	}
}

// Container: docker's own State.OOMKilled, not exit 137 alone.
func TestContainerOOMDetection(t *testing.T) {
	for _, tc := range []struct {
		inspect string
		killed  bool
	}{
		{"true 137", true},
		{"false 137", false},
		{"false 0", false},
	} {
		dir := t.TempDir()
		docker := filepath.Join(dir, "docker")
		script := "#!/bin/sh\n[ \"$1\" = inspect ] && echo '" + tc.inspect + "' && exit 0\nexit 1\n"
		if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		c := NewContainerSandboxes(ContainerConfig{Docker: docker})
		sb := &containerSandbox{owner: c, name: "r1", container: "grain-sandbox-r1"}
		how, killed := sb.SandboxOOM(context.Background())
		if killed != tc.killed {
			t.Errorf("inspect %q: SandboxOOM = %q, %v; want killed=%v", tc.inspect, how, killed, tc.killed)
		}
		if killed && !strings.Contains(how, "OOMKilled") {
			t.Errorf("inspect %q: detail %q does not say how grain knows", tc.inspect, how)
		}
	}
}

// Kubernetes: a container status, current or last, with reason OOMKilled.
func TestK8sOOMDetection(t *testing.T) {
	for _, tc := range []struct {
		statuses string
		killed   bool
	}{
		{"sandbox=OOMKilled ", true},
		{"sandbox=Error sandbox=OOMKilled ", true},
		{"sandbox=Completed ", false},
		{"", false},
	} {
		dir := t.TempDir()
		kubectl := filepath.Join(dir, "kubectl")
		script := "#!/bin/sh\nprintf '%s' '" + tc.statuses + "'\n"
		if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		k := NewK8sSandboxes(K8sConfig{Kubectl: kubectl})
		sb := &k8sSandbox{owner: k, name: "r1", podName: "grain-sandbox-r1"}
		how, killed := sb.SandboxOOM(context.Background())
		if killed != tc.killed {
			t.Errorf("statuses %q: SandboxOOM = %q, %v; want killed=%v", tc.statuses, how, killed, tc.killed)
		}
		if killed && !strings.Contains(how, "OOMKilled") {
			t.Errorf("statuses %q: detail %q does not say how grain knows", tc.statuses, how)
		}
	}
}
