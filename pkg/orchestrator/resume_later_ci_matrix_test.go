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

package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// forgetfulRunner is a grain CI runner that remembers no run at all.
type forgetfulRunner struct{ ci.Runner }

func (forgetfulRunner) Get(string) (ci.Run, bool)         { return ci.Run{}, false }
func (forgetfulRunner) Find(ci.RunFilter, int) ci.RunPage { return ci.RunPage{} }

// ciWakeFixture is one repository in one CI mode with a task about to
// wait on CI, and the clock its ui.Client reads.
type ciWakeFixture struct {
	store *model.Store
	ctx   context.Context
	sim   *githubsim.Sim
	gh    *fakeActionsClient
	ui    *ui.Client
	repo  model.RepoRef
	now   *time.Time
}

func newCIWakeFixture(t *testing.T, mode model.CIMode) *ciWakeFixture {
	t.Helper()
	store, ctx := openStore(t)
	sim, base := newSim(t, "acme", "widgets", "main")
	gh := newFakeActionsClient(base)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: mode}); err != nil {
		t.Fatal(err)
	}
	now := baseTime.Add(time.Minute)
	f := &ciWakeFixture{store: store, ctx: ctx, sim: sim, gh: gh, repo: repo, now: &now}
	f.ui = &ui.Client{
		Store:  store,
		Now:    func() time.Time { return *f.now },
		Config: ui.Config{Actions: gh, GitHubPullRequests: gh},
	}
	return f
}

func (f *ciWakeFixture) deps() orchestrator.Deps {
	return orchestrator.Deps{Store: f.store, Client: f.gh}
}

func (f *ciWakeFixture) wake(t *testing.T, at time.Time) {
	t.Helper()
	if err := orchestrator.ReconcileWake(f.ctx, f.deps(), at); err != nil {
		t.Fatalf("ReconcileWake: %v", err)
	}
}

func (f *ciWakeFixture) wantState(t *testing.T, id string, want model.State, when string) {
	t.Helper()
	if got := taskState(t, f.ctx, f.store, id); got != want {
		t.Fatalf("%s: task %s is %q, want %q", when, id, got, want)
	}
}

// actionsRun puts workflow run id on branch at head, with its check run.
func (f *ciWakeFixture) actionsRun(id int64, branch, head, status, conclusion string, at time.Time) {
	start := baseTime.Add(30 * time.Second)
	f.gh.putRun(github.WorkflowRun{
		ID: id, Name: "Tests", Path: ".github/workflows/tests.yml",
		HeadBranch: branch, HeadSHA: head, Event: "push",
		Status: status, Conclusion: conclusion, StartedAt: &start, UpdatedAt: &at,
	})
	check := github.CheckRun{ID: id + 1000, Name: "Tests / go", Status: status, HeadSHA: head, StartedAt: &start}
	if status == "completed" {
		c := conclusion
		check.Conclusion, check.CompletedAt = &c, &at
	}
	f.sim.CheckRuns[head] = []github.CheckRun{check}
}

// Every combination the wake has to answer: a ci_mode: github and a
// ci_mode: grain repository, waiting on the task's own branch and on one
// named run, for a run that succeeds, fails and is cancelled. Each stays
// deferred while CI is running and is queued again on the first cycle
// after the verdict.
func TestResumeLaterCIWakesOnEveryVerdict(t *testing.T) {
	t.Parallel()
	for _, mode := range []model.CIMode{model.CIModeGitHub, model.CIModeGrain} {
		for _, target := range []string{"my-branch", "run-id"} {
			for _, conclusion := range []string{"success", "failure", "cancelled"} {
				mode, target, conclusion := mode, target, conclusion
				t.Run(string(mode)+"/"+target+"/"+conclusion, func(t *testing.T) {
					t.Parallel()
					f := newCIWakeFixture(t, mode)
					task := filedTask(t, f.ctx, f.store, "w", f.repo)
					pushBranch(t, f.sim.BareRepo, task.BranchName())
					head, err := f.gh.GetBranchHead("acme", "widgets", task.BranchName())
					if err != nil || head == nil {
						t.Fatalf("GetBranchHead: %v %v", head, err)
					}

					ci := model.ResumeOnMyBranch
					switch {
					case mode == model.CIModeGitHub:
						f.actionsRun(7001, task.BranchName(), head.SHA, "in_progress", "", baseTime.Add(30*time.Second))
						if target == "run-id" {
							ci = "gha:acme:widgets:7001"
						}
					case target == "run-id":
						if err := f.store.PutCIRun(f.ctx, model.CIRun{ID: "ci-7001", Key: "k", Repo: f.repo.String(),
							Ref: task.BranchName(), StartedAt: baseTime.Add(30 * time.Second)}); err != nil {
							t.Fatal(err)
						}
						ci = "ci-7001"
					}
					parkTaskWithResumeLater(t, f.ctx, f.store, f.gh, f.ui, task, "w-1", map[string]any{"ci": ci}, baseTime)
					f.wantState(t, task.ID, model.StateDeferred, "after resume_later")

					f.wake(t, baseTime.Add(3*time.Minute))
					f.wantState(t, task.ID, model.StateDeferred, "while CI is still running")

					done := baseTime.Add(4 * time.Minute)
					if mode == model.CIModeGitHub {
						f.actionsRun(7001, task.BranchName(), head.SHA, "completed", conclusion, done)
					} else {
						id := "ci-7001"
						if target == "my-branch" {
							id = "ci-7002"
						}
						if err := f.store.PutCIRun(f.ctx, model.CIRun{ID: id, Key: "k", Repo: f.repo.String(),
							Ref: task.BranchName(), StartedAt: baseTime.Add(30 * time.Second), FinishedAt: done,
							Conclusion: conclusion}); err != nil {
							t.Fatal(err)
						}
					}
					f.wake(t, baseTime.Add(5*time.Minute))
					f.wantState(t, task.ID, model.StateQueued, "one cycle after a "+conclusion+" verdict")
				})
			}
		}
	}
}

// The bug this was filed for: a run calls resume_later and then ends in
// an error -- a usage limit, its runtime cap -- so its result is never
// processed. The task was deferred the moment it called the tool; the
// wake condition has to be held from that moment too, or nothing ever
// wakes the task.
func TestResumeLaterCIWaitSurvivesARunThatEndsInError(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"failed", "cancelled", model.PausedOutcome} {
		outcome := outcome
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			f := newCIWakeFixture(t, model.CIModeGitHub)
			task := filedTask(t, f.ctx, f.store, "e", f.repo)
			pushBranch(t, f.sim.BareRepo, task.BranchName())
			head, _ := f.gh.GetBranchHead("acme", "widgets", task.BranchName())
			f.actionsRun(7101, task.BranchName(), head.SHA, "in_progress", "", baseTime.Add(30*time.Second))

			if err := f.store.StartRun(f.ctx, model.Run{ID: "e-1", TaskID: task.ID, Sandbox: "s", Attempt: 1,
				StartedAt: baseTime}, model.Limits{}); err != nil {
				t.Fatal(err)
			}
			res := callResumeLaterTool(t, f.ctx, f.ui, task.ID, map[string]any{
				"reason": "waiting on CI", "when": map[string]any{"ci": "my-branch"}})
			if res.IsError {
				t.Fatalf("resume_later refused: %s", res.Text)
			}
			// No ProcessResult: the run's ending is all that is recorded.
			if err := f.store.FinishRun(f.ctx, "e-1", baseTime.Add(2*time.Minute), outcome, "it broke"); err != nil {
				t.Fatal(err)
			}
			held, err := f.store.HeldCIResumes(f.ctx)
			if err != nil || len(held) != 1 || held[0].TaskID != task.ID || held[0].Head != head.SHA {
				t.Fatalf("HeldCIResumes after a %s run = %+v, %v; want task %s held at %s", outcome, held, err, task.ID, head.SHA)
			}

			f.actionsRun(7101, task.BranchName(), head.SHA, "completed", "failure", baseTime.Add(4*time.Minute))
			f.wake(t, baseTime.Add(5*time.Minute))
			f.wantState(t, task.ID, model.StateQueued, "after the verdict, for a run that ended "+outcome)
		})
	}
}

// A branch that moves to a new head while the task waits wakes it; so
// does a named run GitHub or grain CI no longer has.
func TestResumeLaterCIWakesOnANewHeadAndAVanishedRun(t *testing.T) {
	t.Parallel()
	t.Run("new head", func(t *testing.T) {
		t.Parallel()
		f := newCIWakeFixture(t, model.CIModeGitHub)
		task := filedTask(t, f.ctx, f.store, "h", f.repo)
		pushBranch(t, f.sim.BareRepo, task.BranchName())
		head, _ := f.gh.GetBranchHead("acme", "widgets", task.BranchName())
		f.actionsRun(7201, task.BranchName(), head.SHA, "in_progress", "", baseTime.Add(30*time.Second))
		parkTaskWithResumeLater(t, f.ctx, f.store, f.gh, f.ui, task, "h-1", map[string]any{"ci": "my-branch"}, baseTime)
		f.wake(t, baseTime.Add(3*time.Minute))
		f.wantState(t, task.ID, model.StateDeferred, "before the push")
		pushBranch(t, f.sim.BareRepo, task.BranchName())
		f.wake(t, baseTime.Add(4*time.Minute))
		f.wantState(t, task.ID, model.StateQueued, "after the branch moved")
	})
	t.Run("github run gone", func(t *testing.T) {
		t.Parallel()
		f := newCIWakeFixture(t, model.CIModeGitHub)
		task := filedTask(t, f.ctx, f.store, "g", f.repo)
		pushBranch(t, f.sim.BareRepo, task.BranchName())
		head, _ := f.gh.GetBranchHead("acme", "widgets", task.BranchName())
		f.actionsRun(7301, task.BranchName(), head.SHA, "in_progress", "", baseTime.Add(30*time.Second))
		parkTaskWithResumeLater(t, f.ctx, f.store, f.gh, f.ui, task, "g-1",
			map[string]any{"ci": "gha:acme:widgets:7301"}, baseTime)
		f.wake(t, baseTime.Add(3*time.Minute))
		f.wantState(t, task.ID, model.StateDeferred, "while the run exists")
		f.gh.mu.Lock()
		delete(f.gh.runs, 7301)
		f.gh.mu.Unlock()
		f.wake(t, baseTime.Add(4*time.Minute))
		f.wantState(t, task.ID, model.StateQueued, "after GitHub lost the run")
	})
	t.Run("grain run gone", func(t *testing.T) {
		t.Parallel()
		f := newCIWakeFixture(t, model.CIModeGrain)
		task := filedTask(t, f.ctx, f.store, "r", f.repo)
		pushBranch(t, f.sim.BareRepo, task.BranchName())
		if err := f.store.PutCIRun(f.ctx, model.CIRun{ID: "ci-old", Key: "k1", Repo: f.repo.String(),
			Ref: task.BranchName(), StartedAt: baseTime.Add(30 * time.Second)}); err != nil {
			t.Fatal(err)
		}
		parkTaskWithResumeLater(t, f.ctx, f.store, f.gh, f.ui, task, "r-1", map[string]any{"ci": "ci-old"}, baseTime)
		f.wake(t, baseTime.Add(3*time.Minute))
		f.wantState(t, task.ID, model.StateDeferred, "while the run exists")
		// The daemon restarted mid-run: its runner has forgotten the run,
		// and the store's row for it will never conclude.
		deps := f.deps()
		deps.CI = forgetfulRunner{}
		if err := orchestrator.ReconcileWake(f.ctx, deps, baseTime.Add(4*time.Minute)); err != nil {
			t.Fatal(err)
		}
		f.wantState(t, task.ID, model.StateQueued, "after the runner lost the run")
	})
}

// A deferral made before the hold was recorded on the run -- the shape
// every stuck task in the deployment is in -- is read back from the
// comment resume_later left, and woken on its verdict like any other.
func TestResumeLaterCIWaitLostBeforeTheFixIsRecovered(t *testing.T) {
	t.Parallel()
	f := newCIWakeFixture(t, model.CIModeGitHub)
	task := filedTask(t, f.ctx, f.store, "old", f.repo)
	pushBranch(t, f.sim.BareRepo, task.BranchName())
	head, _ := f.gh.GetBranchHead("acme", "widgets", task.BranchName())
	f.actionsRun(7401, task.BranchName(), head.SHA, "in_progress", "", baseTime.Add(30*time.Second))
	if err := f.store.StartRun(f.ctx, model.Run{ID: "old-1", TaskID: task.ID, Sandbox: "s", Attempt: 1,
		StartedAt: baseTime}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	// What ui.Client.ResumeLater did before it held the condition.
	at := baseTime.Add(time.Minute)
	if err := f.store.SetDeferred(f.ctx, task.ID, &at, nil, at); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AddComment(f.ctx, model.Comment{TaskID: task.ID,
		Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body: "grain: this task's run stopped with resume_later. It goes back in the queue once " +
			"CI (gha:acme:widgets:7401) has a verdict; Undefer releases it sooner.",
		CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishRun(f.ctx, "old-1", baseTime.Add(2*time.Minute), "failed", "it broke"); err != nil {
		t.Fatal(err)
	}
	held, err := f.store.HeldCIResumes(f.ctx)
	if err != nil || len(held) != 1 || !held[0].Recovered || held[0].TargetCI != "gha:acme:widgets:7401" {
		t.Fatalf("HeldCIResumes = %+v, %v; want the lost wait recovered", held, err)
	}
	f.wake(t, baseTime.Add(3*time.Minute))
	f.wantState(t, task.ID, model.StateDeferred, "while the run is in progress")
	f.actionsRun(7401, task.BranchName(), head.SHA, "completed", "cancelled", baseTime.Add(4*time.Minute))
	f.wake(t, baseTime.Add(5*time.Minute))
	f.wantState(t, task.ID, model.StateQueued, "after the cancelled verdict")
}

// The backstop is configurable: a shorter one wakes a wait whose CI never
// concludes at that bound, with a note; a negative one never fires.
func TestResumeLaterCIBackstopIsConfigurable(t *testing.T) {
	t.Parallel()
	for _, backstop := range []time.Duration{2 * time.Hour, -time.Second} {
		backstop := backstop
		t.Run(backstop.String(), func(t *testing.T) {
			t.Parallel()
			f := newCIWakeFixture(t, model.CIModeGitHub)
			task := filedTask(t, f.ctx, f.store, "b", f.repo)
			pushBranch(t, f.sim.BareRepo, task.BranchName())
			head, _ := f.gh.GetBranchHead("acme", "widgets", task.BranchName())
			f.actionsRun(7501, task.BranchName(), head.SHA, "in_progress", "", baseTime.Add(30*time.Second))
			parkTaskWithResumeLater(t, f.ctx, f.store, f.gh, f.ui, task, "b-1", map[string]any{"ci": "my-branch"}, baseTime)
			deps := f.deps()
			deps.Config.CIWakeBackstop = backstop

			if err := orchestrator.ReconcileWake(f.ctx, deps, baseTime.Add(time.Minute+2*time.Hour-time.Minute)); err != nil {
				t.Fatal(err)
			}
			f.wantState(t, task.ID, model.StateDeferred, "inside the backstop")
			if err := orchestrator.ReconcileWake(f.ctx, deps, baseTime.Add(time.Minute+2*time.Hour+5*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if backstop < 0 {
				f.wantState(t, task.ID, model.StateDeferred, "with the backstop disabled")
				return
			}
			f.wantState(t, task.ID, model.StateQueued, "past a 2h backstop")
			comments, err := f.store.Comments(f.ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if last := comments[len(comments)-1].Body; !strings.Contains(last, "2h backstop") {
				t.Fatalf("backstop note = %q, want it to name the 2h backstop", last)
			}
		})
	}
}

// The backstop covers every task deferred with no wake time whose latest
// resume_later comment names CI, however its suspension row reads: one a
// later run marked resumed while the deferral stayed, and one that holds
// some other condition only. Neither may leave the task deferred past the
// backstop, with nothing waking it (tasks 4014, 4187, 4523, 4526).
func TestResumeLaterCIBackstopSeesTheWaitHoweverItsSuspensionReads(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{"resumed by a later run", "holding a message only"} {
		shape := shape
		t.Run(shape, func(t *testing.T) {
			t.Parallel()
			f := newCIWakeFixture(t, model.CIModeGitHub)
			task := filedTask(t, f.ctx, f.store, "s", f.repo)
			pushBranch(t, f.sim.BareRepo, task.BranchName())
			head, _ := f.gh.GetBranchHead("acme", "widgets", task.BranchName())
			f.actionsRun(7601, task.BranchName(), head.SHA, "in_progress", "", baseTime.Add(30*time.Second))

			switch shape {
			case "resumed by a later run":
				parkTaskWithResumeLater(t, f.ctx, f.store, f.gh, f.ui, task, "s-1", map[string]any{"ci": "my-branch"}, baseTime)
				// A run that started while the task stayed deferred: its
				// start marks the suspension resumed, and it ends without
				// touching the deferral.
				if err := f.store.StartRun(f.ctx, model.Run{ID: "s-2", TaskID: task.ID, Sandbox: "s", Attempt: 2,
					StartedAt: baseTime.Add(3 * time.Minute)}, model.Limits{}); err != nil {
					t.Fatal(err)
				}
				if err := f.store.FinishRun(f.ctx, "s-2", baseTime.Add(4*time.Minute), "failed", "it broke"); err != nil {
					t.Fatal(err)
				}
			case "holding a message only":
				if err := f.store.StartRun(f.ctx, model.Run{ID: "s-1", TaskID: task.ID, Sandbox: "s", Attempt: 1,
					StartedAt: baseTime}, model.Limits{}); err != nil {
					t.Fatal(err)
				}
				at := baseTime.Add(time.Minute)
				if err := f.store.SetDeferred(f.ctx, task.ID, &at, nil, at); err != nil {
					t.Fatal(err)
				}
				if err := f.store.HoldResumeOn(f.ctx, task.ID, model.ResumeOn{Message: true}, at); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.AddComment(f.ctx, model.Comment{TaskID: task.ID,
					Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
					Body: "grain: this task's run stopped with resume_later. It goes back in the queue once " +
						"CI (my-branch) has a verdict; Undefer releases it sooner.",
					CreatedAt: at}); err != nil {
					t.Fatal(err)
				}
				if err := f.store.FinishRun(f.ctx, "s-1", baseTime.Add(2*time.Minute), "succeeded", ""); err != nil {
					t.Fatal(err)
				}
			}
			f.wantState(t, task.ID, model.StateDeferred, "after the wait was taken")
			held, err := f.store.HeldCIResumes(f.ctx)
			if err != nil || len(held) != 1 || held[0].TaskID != task.ID || held[0].TargetCI != model.ResumeOnMyBranch {
				t.Fatalf("HeldCIResumes = %+v, %v; want task %s held on my-branch", held, err, task.ID)
			}
			f.wake(t, baseTime.Add(5*time.Minute))
			f.wantState(t, task.ID, model.StateDeferred, "while CI is running")
			f.wake(t, baseTime.Add(time.Minute+orchestrator.DefaultCIWakeStallBound+time.Minute))
			f.wantState(t, task.ID, model.StateQueued, "past the backstop")
		})
	}
}
