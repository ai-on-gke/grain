package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
)

// supersedingRunner is a GrainCIRunner whose runs are read by id, so a
// wait can be handed one run and find it replaced by another.
type supersedingRunner struct {
	fakeGrainCIRunner
	mu    sync.Mutex
	start ci.Run
	byID  map[string]ci.Run
	read  []string
}

func (r *supersedingRunner) StartGrainCI(_ context.Context, jobs []string) (ci.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	r.jobs = append([]string(nil), jobs...)
	return r.start, nil
}

func (r *supersedingRunner) GrainCIRun(_ context.Context, id string) (ci.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.read = append(r.read, id)
	return r.byID[id], nil
}

func (r *supersedingRunner) reads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.read...)
}

// docs/test-plans/2026-10-25.md §6, gap 3: a wait_for_checks that joined
// one retrigger_ci's recheck, when a second retrigger_ci supersedes it,
// waits for the second rather than reporting the first's cancellation as
// the branch's verdict.
func TestWaitForChecksFollowsTheRecheckThatSupersededTheOneItJoined(t *testing.T) {
	old := checkPollInterval
	checkPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { checkPollInterval = old })

	const commit = "abcdef012345"
	first := ci.Run{ID: "ci-recheck-1", Repo: testCIRepo, Ref: "grain/task-1", Commit: commit, Recheck: true,
		Conclusion: ci.ConclusionCancelled, CancelReason: ci.SupersededByRunReason("ci-recheck-2"),
		Jobs: []ci.JobRun{{JobID: "e2e", Name: "e2e", Conclusion: ci.ConclusionCancelled}}}
	second := ci.Run{ID: "ci-recheck-2", Repo: testCIRepo, Ref: "grain/task-1", Commit: commit, Recheck: true,
		Conclusion: ci.ConclusionSuccess,
		Jobs:       []ci.JobRun{{JobID: "e2e", Name: "e2e", Conclusion: ci.ConclusionSuccess}}}
	runner := &supersedingRunner{start: ci.Run{ID: first.ID, Commit: commit},
		byID: map[string]ci.Run{first.ID: first, second.ID: second}}

	result := ciTools(runner)[0].Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if !strings.Contains(result.Text, "ci-recheck-2") || !strings.Contains(result.Text, "success") {
		t.Fatalf("wait reported %q, want the superseding recheck's success", result.Text)
	}
	if strings.Contains(result.Text, "cancelled") {
		t.Fatalf("wait reported the superseded recheck's cancellation: %s", result.Text)
	}
	if got := runner.reads(); len(got) < 2 || got[len(got)-1] != "ci-recheck-2" {
		t.Fatalf("runs read = %v, want the wait to end on ci-recheck-2", got)
	}
	if runner.starts != 1 {
		t.Fatalf("wait started %d runs, want only its own first one", runner.starts)
	}
}

// What a wait does not follow: a selective wait (the replacing run may
// sample other jobs), a supersede to another commit (the branch moved),
// and a cancellation somebody asked for.
func TestWaitForChecksReportsACancellationItShouldNotFollow(t *testing.T) {
	old := checkPollInterval
	checkPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { checkPollInterval = old })

	next := ci.Run{ID: "ci-2", Repo: testCIRepo, Ref: "grain/task-1", Commit: "abcdef012345", Conclusion: ci.ConclusionSuccess}
	for _, tc := range []struct {
		name string
		run  ci.Run
		next ci.Run
		jobs []any
	}{
		{"selective", ci.Run{ID: "ci-1", Commit: "abcdef012345", Conclusion: ci.ConclusionCancelled,
			CancelReason: ci.SupersededByRunReason("ci-2")}, next, []any{"go"}},
		{"other commit", ci.Run{ID: "ci-1", Commit: "0000000aaaa", Conclusion: ci.ConclusionCancelled,
			CancelReason: ci.SupersededByRunReason("ci-2")}, next, nil},
		{"operator", ci.Run{ID: "ci-1", Commit: "abcdef012345", Conclusion: ci.ConclusionCancelled,
			CancelReason: ci.SupersededByRunReason("ci-2"), CancelledBy: ci.CancelByOperator}, next, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &supersedingRunner{start: ci.Run{ID: "ci-1"},
				byID: map[string]ci.Run{"ci-1": tc.run, "ci-2": tc.next}}
			args := map[string]any{}
			if tc.jobs != nil {
				args["jobs"] = tc.jobs
			}
			result := ciTools(runner)[0].Handler(context.Background(), args)
			if result.IsError {
				t.Fatal(result.Text)
			}
			if !strings.Contains(result.Text, "cancelled") || strings.Contains(result.Text, "ci-2:") {
				t.Fatalf("wait reported %q, want ci-1's cancellation", result.Text)
			}
			for _, id := range runner.reads() {
				if id == "ci-2" && tc.name != "other commit" {
					t.Fatalf("wait read ci-2, which it should not follow: %v", runner.reads())
				}
			}
		})
	}
}

// A reason grain wrote on its own cancellation is said as grain's: before
// this, "task 7 was closed" or a supersede read as a person's decision.
func TestWaitForChecksSaysWhenGrainCancelledTheRunItself(t *testing.T) {
	runner := &fakeGrainCIRunner{run: &ci.Run{
		ID: "ci-1", Ref: "grain/task-1", Commit: "abcdef012345",
		Conclusion: ci.ConclusionCancelled, CancelReason: "task 1 was closed",
	}}
	result := ciTools(runner)[0].Handler(context.Background(), map[string]any{})
	if result.IsError {
		t.Fatal(result.Text)
	}
	if strings.Contains(result.Text, "by hand") || !strings.Contains(result.Text, "Grain cancelled this run itself: task 1 was closed") {
		t.Fatalf("wait reported %q, want grain's own cancellation said as grain's", result.Text)
	}
}
