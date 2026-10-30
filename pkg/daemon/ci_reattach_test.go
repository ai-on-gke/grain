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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/sandboxaccess"
)

// fakeWrapper is granule's wrapper as a restart sees it: execs that keep
// running, and keep their output and exit status, whichever grain process
// is or is not attached to them. Its commands are real bash on this host.
type fakeWrapper struct {
	mu      sync.Mutex
	next    int
	execs   map[string]*fakeExec
	live    map[string]bool
	started chan string
}

type fakeExec struct {
	mu   sync.Mutex
	out  bytes.Buffer
	code int
	done chan struct{}
}

func newFakeWrapper() *fakeWrapper {
	return &fakeWrapper{execs: map[string]*fakeExec{}, live: map[string]bool{}, started: make(chan string, 16)}
}

func (w *fakeWrapper) start(script string) (string, *fakeExec) {
	ex := &fakeExec{done: make(chan struct{})}
	cmd := exec.Command("bash", "-c", script)
	cmd.Stdout, cmd.Stderr = (*lockedWriter)(ex), (*lockedWriter)(ex)
	if err := cmd.Start(); err != nil {
		ex.code = 127
		close(ex.done)
	} else {
		go func() {
			err := cmd.Wait()
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				ex.mu.Lock()
				ex.code = exitErr.ExitCode()
				ex.mu.Unlock()
			}
			close(ex.done)
		}()
	}
	w.mu.Lock()
	w.next++
	id := fmt.Sprintf("exec-%d", w.next)
	w.execs[id] = ex
	w.mu.Unlock()
	return id, ex
}

func (w *fakeWrapper) execCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.execs)
}

func (w *fakeWrapper) waitAll() {
	w.mu.Lock()
	var all []*fakeExec
	for _, ex := range w.execs {
		all = append(all, ex)
	}
	w.mu.Unlock()
	for _, ex := range all {
		<-ex.done
	}
}

// lockedWriter appends to a fakeExec's output.
type lockedWriter fakeExec

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out.Write(p)
}

func (ex *fakeExec) snapshot() ([]byte, int, bool) {
	select {
	case <-ex.done:
		ex.mu.Lock()
		defer ex.mu.Unlock()
		return append([]byte(nil), ex.out.Bytes()...), ex.code, true
	default:
		ex.mu.Lock()
		defer ex.mu.Unlock()
		return append([]byte(nil), ex.out.Bytes()...), 0, false
	}
}

// fakeChannelSandboxes hands out sandboxes reached over fakeWrapper, and
// gives one back across a restart for as long as it is live.
type fakeChannelSandboxes struct{ w *fakeWrapper }

func (s fakeChannelSandboxes) Acquire(_ context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	s.w.mu.Lock()
	s.w.live[req.Name] = true
	s.w.mu.Unlock()
	return &fakeChannelSandbox{name: req.Name, w: s.w}, nil
}

func (s fakeChannelSandboxes) Readopt(_ context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if !s.w.live[req.Name] {
		return nil, fmt.Errorf("pod for %s not found", req.Name)
	}
	return &fakeChannelSandbox{name: req.Name, w: s.w}, nil
}

type fakeChannelSandbox struct {
	name string
	w    *fakeWrapper
}

func (s *fakeChannelSandbox) Name() string { return s.name }

func (s *fakeChannelSandbox) Release(context.Context) error {
	s.w.mu.Lock()
	delete(s.w.live, s.name)
	s.w.mu.Unlock()
	return nil
}

// Exec is sandboxaccess.Runner.ExecAttached's contract: start (or, with an
// exec id, reattach to) the command, report its id and cursor, and on a
// cancelled context leave it running.
func (s *fakeChannelSandbox) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	var (
		id     string
		ex     *fakeExec
		cursor int64
	)
	if cmd.Attach != nil && cmd.Attach.ExecID != "" {
		s.w.mu.Lock()
		id, ex, cursor = cmd.Attach.ExecID, s.w.execs[cmd.Attach.ExecID], cmd.Attach.Cursor
		live := s.w.live[s.name]
		s.w.mu.Unlock()
		if ex == nil || !live {
			return ci.CommandResult{ExitCode: -1}, &sandboxaccess.ChannelLostError{Sandbox: s.name, Cause: errors.New("exec not found")}
		}
	} else {
		id, ex = s.w.start(cmd.Script)
		s.w.started <- id
	}
	if cmd.Attach != nil && cmd.Attach.Started != nil {
		cmd.Attach.Started(id)
	}
	for {
		out, code, done := ex.snapshot()
		if cmd.Attach != nil && cmd.Attach.Progress != nil {
			cmd.Attach.Progress(int64(len(out)))
		}
		if cursor > int64(len(out)) {
			cursor = int64(len(out))
		}
		if done {
			return ci.CommandResult{Output: string(out[cursor:]), ExitCode: code}, nil
		}
		select {
		case <-ctx.Done():
			return ci.CommandResult{Output: string(out[cursor:]), ExitCode: -1}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// reattachHarness is grain's CI on a store and a wrapper that both outlive
// it, so that a second grain can be started after the first stops
// mid-step.
type reattachHarness struct {
	t       *testing.T
	dataDir string
	source  ci.Source
	w       *fakeWrapper
}

func newReattachHarness(t *testing.T, workflowYAML string) *reattachHarness {
	t.Helper()
	wf, err := ci.Parse(".grain/workflows/tests.yml", []byte(workflowYAML))
	if err != nil {
		t.Fatal(err)
	}
	return &reattachHarness{
		t: t, dataDir: t.TempDir(), w: newFakeWrapper(),
		source: testCISource{workflows: []ci.Workflow{wf}, commit: strings.Repeat("a", 40)},
	}
}

func (h *reattachHarness) grain() (*ci.Service, *ci.StoredRunStore) {
	h.t.Helper()
	store, closeStore := openTestStore(h.t, h.dataDir)
	h.t.Cleanup(closeStore)
	runs := ci.NewStoredRunStore(store, ci.StoredRunStoreOptions{})
	return &ci.Service{
		Source: h.source,
		Engine: &ci.Engine{
			Sandboxes:            fakeChannelSandboxes{w: h.w},
			Attachments:          runs,
			Log:                  h.t.Logf,
			AttachmentCheckpoint: 20 * time.Millisecond,
		},
		Runs:    runs,
		PlanDir: filepath.Join(h.dataDir, "state", "ci-plans"),
	}, runs
}

// startAndStop starts a run on a first grain, waits for its step to be
// running with its exec recorded, and stops that grain under it.
func (h *reattachHarness) startAndStop(ctx context.Context) string {
	h.t.Helper()
	svc, runs := h.grain()
	run, err := svc.Start(ctx, ci.Request{Repo: "acme/widgets", Ref: "main", Commit: strings.Repeat("a", 40), Event: ci.EventPush})
	if err != nil {
		h.t.Fatalf("starting CI run: %v", err)
	}
	select {
	case <-h.w.started:
	case <-time.After(15 * time.Second):
		h.t.Fatal("the step never started")
	}
	waitUntil(h.t, "the step's exec to be recorded with output", func() bool {
		as, _ := runs.StepAttachments(ctx, run.ID)
		return len(as) == 1 && as[0].ExecID != "" && as[0].Cursor > 0
	})
	svc.Detach()
	return run.ID
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitConcluded(t *testing.T, svc *ci.Service, id string) ci.Run {
	t.Helper()
	var run ci.Run
	waitUntil(t, "the run to conclude", func() bool {
		r, ok := svc.Get(id)
		run = r
		return ok && r.Conclusion.Done()
	})
	return run
}

func flagStep(flag string, exit int) string {
	return fmt.Sprintf(`name: tests
on: push
jobs:
  slow:
    steps:
      - name: wait-for-flag
        run: |
          echo before-restart
          while [ ! -e %q ]; do sleep 0.02; done
          echo after-restart
          exit %d
      - name: next
        run: echo next-step
`, flag, exit)
}

func TestARunningStepIsReattachedAcrossARestartAndTheRunGetsItsRealVerdict(t *testing.T) {
	ctx := context.Background()
	flag := filepath.Join(t.TempDir(), "go")
	h := newReattachHarness(t, flagStep(flag, 0))
	runID := h.startAndStop(ctx)

	svc, runs := h.grain()
	adopted, lost := AdoptCIRuns(ctx, svc)
	if len(adopted) != 1 || len(lost) != 0 || adopted[0].Job != "slow" {
		t.Fatalf("AdoptCIRuns = (%+v, %+v), want job slow adopted and nothing lost", adopted, lost)
	}
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run := waitConcluded(t, svc, runID)
	if run.Conclusion != ci.ConclusionSuccess || run.Interrupted {
		t.Fatalf("run = %s (interrupted %v), want success: %+v", run.Conclusion, run.Interrupted, run)
	}
	// One exec for the reattached step and one for the step after it:
	// the step that was running was not started a second time.
	if n := h.w.execCount(); n != 2 {
		t.Fatalf("the wrapper ran %d execs, want 2", n)
	}
	job := run.Jobs[0]
	if st := job.Steps[0]; st.ExitCode != 0 || !strings.Contains(st.Output, "after-restart") || strings.Contains(st.Output, "before-restart") {
		t.Fatalf("step 0 = %+v, want exit 0 and only the output after the stored cursor", st)
	}
	if job.Steps[1].Conclusion != ci.ConclusionSuccess {
		t.Fatalf("step 1 = %+v, want the job to carry on after the reattached step", job.Steps[1])
	}
	if as, _ := runs.StepAttachments(ctx, runID); len(as) != 0 {
		t.Fatalf("attachments after the job ended = %+v, want none", as)
	}
}

func TestAStepThatFinishedWhileGrainWasDownGetsItsRealExitCode(t *testing.T) {
	ctx := context.Background()
	flag := filepath.Join(t.TempDir(), "go")
	h := newReattachHarness(t, flagStep(flag, 3))
	runID := h.startAndStop(ctx)

	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.w.waitAll()

	svc, _ := h.grain()
	if adopted, lost := AdoptCIRuns(ctx, svc); len(adopted) != 1 || len(lost) != 0 {
		t.Fatalf("AdoptCIRuns = (%+v, %+v), want one adopted", adopted, lost)
	}
	run := waitConcluded(t, svc, runID)
	if run.Conclusion != ci.ConclusionFailure || run.Interrupted {
		t.Fatalf("run = %s (interrupted %v), want the step's own failure: %+v", run.Conclusion, run.Interrupted, run)
	}
	if st := run.Jobs[0].Steps[0]; st.ExitCode != 3 || st.Conclusion != ci.ConclusionFailure {
		t.Fatalf("step 0 = %+v, want exit 3 and failure", st)
	}
}

func TestASandboxGoneWhileGrainWasDownIsAnInfraFaultNotAnInterruptedRun(t *testing.T) {
	ctx := context.Background()
	flag := filepath.Join(t.TempDir(), "go")
	h := newReattachHarness(t, flagStep(flag, 0))
	runID := h.startAndStop(ctx)

	h.w.mu.Lock()
	h.w.live = map[string]bool{}
	h.w.mu.Unlock()

	svc, _ := h.grain()
	adopted, lost := AdoptCIRuns(ctx, svc)
	if len(adopted) != 1 || len(lost) != 0 {
		t.Fatalf("AdoptCIRuns = (%+v, %+v), want the run adopted to end its job as infra", adopted, lost)
	}
	// The infra path: the step that was running ends with the channel
	// lost, and the job is run again in a fresh sandbox.
	select {
	case <-h.w.started:
	case <-time.After(15 * time.Second):
		r, _ := svc.Get(runID)
		t.Fatalf("the job was never re-run after its sandbox was lost: %+v", r)
	}
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run := waitConcluded(t, svc, runID)
	if run.Interrupted || run.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("run = %s (interrupted %v), want the re-run's own success: %+v", run.Conclusion, run.Interrupted, run)
	}
	job := run.Jobs[0]
	if !job.Retried || !strings.Contains(job.RetryReason, sandboxaccess.ChannelLostFault) {
		t.Fatalf("job retried %v for %q, want a re-run for %q", job.Retried, job.RetryReason, sandboxaccess.ChannelLostFault)
	}
	if len(job.Attempts) == 0 || job.Attempts[0].Conclusion != ci.ConclusionErrored {
		t.Fatalf("job attempts = %+v, want the first errored as infra", job.Attempts)
	}
	h.w.waitAll()
}

func TestARunWithNoSandboxYetIsStillStampedInterrupted(t *testing.T) {
	ctx := context.Background()
	h := newReattachHarness(t, flagStep("/nonexistent", 0))
	svc, runs := h.grain()
	now := time.Now().UTC()
	runs.Put(ci.Run{
		ID: "queued-1", Repo: "acme/widgets", Ref: "main", Commit: strings.Repeat("a", 40), Event: ci.EventPush,
		StartedAt: &now,
		Jobs:      []ci.JobRun{{JobID: "slow", Name: "slow"}},
	})
	adopted, lost := AdoptCIRuns(ctx, svc)
	if len(adopted) != 0 || len(lost) != 0 {
		t.Fatalf("AdoptCIRuns = (%+v, %+v), want nothing to attach", adopted, lost)
	}
	run, ok := runs.Get("queued-1")
	if !ok || !run.Interrupted || run.Conclusion != ci.ConclusionCancelled {
		t.Fatalf("queued run = %+v, want it stamped cancelled and interrupted", run)
	}
}
