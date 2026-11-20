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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func shadowStatus(t *testing.T, store *model.Store, id, binding string, now time.Time) string {
	t.Helper()
	v, _, err := store.ObserveTaskGates(context.Background(), id, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range v.Bindings {
		if b.ID == binding {
			return v.Results[i].Result.Status
		}
	}
	t.Fatalf("binding %s missing", binding)
	return ""
}
func TestShadowProductsFollowActualFinishDecisions(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		result                         *agent.Result
		product, want, report, receipt string
		intent                         model.Intent
	}{
		{"comment-on-issue-implement", toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "Durable closing note"}}), "any", "met", "met", "pending", model.IntentImplement},
		{"final-text-relayed", &agent.Result{FinalText: "A durable final report"}, "any", "met", "met", "pending", model.IntentImplement},
		{"nothing-needed", toolResult(agent.ToolCall{Name: "nothing_needed", Arguments: map[string]any{"reason": "Already correct"}}), "nothing_needed", "met", "unmet", "unknown", model.IntentImplement},
		{"question-precedes-report", toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "The partial report"}}, agent.ToolCall{Name: "ask_question", Arguments: map[string]any{"question": "Which target?", "summary": "Choose the target"}}), "any", "pending", "pending", "unknown", model.IntentImplement},
		{"no-action-tools", toolResult(agent.ToolCall{Name: "run_command", Arguments: map[string]any{"command": "true"}}), "any", "unmet", "unmet", "unknown", model.IntentImplement},
		{"review-delivery", &agent.Result{FinalText: "Review delivered with findings"}, "review", "met", "unmet", "unknown", model.IntentReview},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := openStore(t)
			now := baseTime
			task := filedTask(t, ctx, store, "shadow-finish-"+tc.name, model.RepoRef{Owner: "acme", Name: "widgets"})
			task.Target = nil
			task.Intent = tc.intent
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}
			runID := task.ID + "-run"
			if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Attempt: 1, StartedAt: now, Sandbox: "test"}, model.Limits{}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Second)
			if err := store.FinishRun(ctx, runID, now, "succeeded", ""); err != nil {
				t.Fatal(err)
			}
			if err := orchestrator.ProcessResult(ctx, store, nil, task, tc.result, runID, now); err != nil {
				t.Fatal(err)
			}
			before, _ := store.GetObservation(ctx, task.ID)
			wake := store.Changed()
			got := shadowStatus(t, store, task.ID, "delivery-"+tc.product, now)
			if got != tc.want {
				t.Fatalf("%s=%s want %s", tc.product, got, tc.want)
			}
			if gotReport := shadowStatus(t, store, task.ID, "report", now); gotReport != tc.report {
				t.Fatalf("report=%s want %s", gotReport, tc.report)
			}
			if gotReceipt := shadowStatus(t, store, task.ID, "receipt", now); gotReceipt != tc.receipt {
				t.Fatalf("receipt=%s want %s", gotReceipt, tc.receipt)
			}
			after, _ := store.GetObservation(ctx, task.ID)
			if gates.Digest(before) != gates.Digest(after) {
				t.Fatal("shadow altered legacy finish")
			}
			select {
			case <-wake:
				t.Fatal("shadow woke dispatch")
			default:
			}
			if tc.name == "nothing-needed" {
				client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice")}, store)
				client.Now = func() time.Time { return now.Add(time.Second) }
				person := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP})
				beforeReopen, _, err := store.ObserveTaskGates(ctx, task.ID, now)
				if err != nil {
					t.Fatal(err)
				}
				if err := client.Reopen(person, task.ID); err != nil {
					t.Fatal(err)
				}
				afterReopen, _, err := store.ObserveTaskGates(ctx, task.ID, now.Add(time.Second))
				if err != nil {
					t.Fatal(err)
				}
				if afterReopen.SourceRevision == beforeReopen.SourceRevision {
					t.Fatal("reopen did not invalidate the source fence")
				}
				reopened, err := store.GetObservation(ctx, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if reopened.ClosedAt != nil {
					t.Fatal("delivery prediction reclosed the task")
				}
				// Legacy Reopen preserves a delivered product. A reply requesting
				// more work clears settlement; diagnostics must preserve that.
				if err := client.AddComment(person, task.ID, "Please do the next part", nil); err != nil {
					t.Fatal(err)
				}
				if got := shadowStatus(t, store, task.ID, "delivery-nothing_needed", now.Add(time.Second)); got == "met" {
					t.Fatal("diagnostic receipt restored settlement cleared by a reply")
				}

			}
		})
	}
}
func TestShadowPushWinsOverNoWorkAndReport(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "shadow-push", repo)
	now := baseTime
	runID := task.ID + "-run"
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Attempt: 1, StartedAt: now, Sandbox: "test"}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	now = now.Add(time.Second)
	if err := store.FinishRun(ctx, runID, now, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "nothing_needed", Arguments: map[string]any{"reason": "No more work"}}, agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "The pushed change is ready"}})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, runID, now); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if shadowStatus(t, store, task.ID, "delivery-any", now.Add(time.Second)) != "met" || shadowStatus(t, store, task.ID, "delivery-change", now.Add(time.Second)) != "met" || shadowStatus(t, store, task.ID, "delivery-nothing_needed", now.Add(time.Second)) != "unmet" || shadowStatus(t, store, task.ID, "report", now.Add(time.Second)) != "unmet" || shadowStatus(t, store, task.ID, "receipt", now.Add(time.Second)) != "unknown" {
		t.Fatal("push/open PR finish precedence was lost")
	}
}

func TestShadowCoordinatorReportRetainsPersonClosePolicy(t *testing.T) {
	store, ctx := openStore(t)
	now := baseTime
	task := filedTask(t, ctx, store, "shadow-coordinator", model.RepoRef{Owner: "acme", Name: "widgets"})
	task.Target = nil
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	fresh, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	task = *fresh
	if err := store.SetTaskGoal(ctx, task.ID, model.GoalDecl{Kind: model.GoalPersonCloses}, "alice", now); err != nil {
		t.Fatal(err)
	}
	runID := task.ID + "-run"
	if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Attempt: 1, StartedAt: now, Sandbox: "test"}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := store.FinishRun(ctx, runID, now, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.ProcessResult(ctx, store, nil, task, &agent.Result{FinalText: "The coordinator's durable report"}, runID, now); err != nil {
		t.Fatal(err)
	}
	if got := shadowStatus(t, store, task.ID, "delivery-any", now); got != "met" {
		t.Fatalf("settled coordinator report=%s", got)
	}
	if gotReport, gotReceipt := shadowStatus(t, store, task.ID, "report", now), shadowStatus(t, store, task.ID, "receipt", now); gotReport != "met" || gotReceipt != "pending" {
		t.Fatalf("coordinator report/receipt=%s/%s", gotReport, gotReceipt)
	}
	state, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != model.StateDeferred {
		t.Fatalf("person-closes coordinator changed state: %s", state)
	}
}

func TestShadowFinishProvenanceWithSubMicrosecondTimestamp(t *testing.T) {
	engines := []struct {
		name string
		open func(t *testing.T) (*model.Store, context.Context)
	}{
		{"sqlite", openStore},
		{"postgres", func(t *testing.T) (*model.Store, context.Context) {
			orchestrator.LogToTest(t)
			store, _ := postgrestest.Open(t)
			return store, context.Background()
		}},
	}
	for _, eng := range engines {
		t.Run(eng.name, func(t *testing.T) {
			store, ctx := eng.open(t)
			// Real time.Now() timestamps carry sub-microsecond nanoseconds; PostgreSQL
			// TIMESTAMPTZ truncates them to microseconds on persistence.
			now := baseTime.Add(123456789 * time.Nanosecond)
			sim, client := newSim(t, "acme", "widgets", "main")
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}

			changeTask := filedTask(t, ctx, store, "subus-change-"+eng.name, repo)
			changeRun := changeTask.ID + "-run"
			if err := store.StartRun(ctx, model.Run{ID: changeRun, TaskID: changeTask.ID, Attempt: 1, StartedAt: now, Sandbox: "test"}, model.Limits{}); err != nil {
				t.Fatal(err)
			}
			pushBranch(t, sim.BareRepo, model.BranchName(changeTask.ID))
			if err := store.FinishRun(ctx, changeRun, now.Add(time.Second), "succeeded", ""); err != nil {
				t.Fatal(err)
			}
			if err := orchestrator.ProcessResult(ctx, store, client, changeTask, &agent.Result{FinalText: "Pushed PR"}, changeRun, now); err != nil {
				t.Fatal(err)
			}
			if err := orchestrator.SyncPullRequests(ctx, store, client, now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			if got := shadowStatus(t, store, changeTask.ID, "delivery-change", now.Add(2*time.Second)); got != "met" {
				t.Fatalf("%s: delivery-change=%s want met (sub-microsecond timestamp must not drop finish provenance)", eng.name, got)
			}

			nnTask := filedTask(t, ctx, store, "subus-nn-"+eng.name, repo)
			nnTask.Target = nil
			nnTask.NothingNeededAction = model.NothingNeededAck
			if err := store.PutTask(ctx, nnTask); err != nil {
				t.Fatal(err)
			}
			nnRun := nnTask.ID + "-run"
			if err := store.StartRun(ctx, model.Run{ID: nnRun, TaskID: nnTask.ID, Attempt: 1, StartedAt: now, Sandbox: "test"}, model.Limits{}); err != nil {
				t.Fatal(err)
			}
			if err := store.FinishRun(ctx, nnRun, now.Add(time.Second), model.NothingNeededOutcome, ""); err != nil {
				t.Fatal(err)
			}
			nnRes := toolResult(agent.ToolCall{Name: "nothing_needed", Arguments: map[string]any{"reason": "No work needed"}})
			if err := orchestrator.ProcessResult(ctx, store, nil, nnTask, nnRes, nnRun, now); err != nil {
				t.Fatal(err)
			}
			if got := shadowStatus(t, store, nnTask.ID, "delivery-nothing_needed", now.Add(2*time.Second)); got != "met" {
				t.Fatalf("%s: delivery-nothing_needed=%s want met", eng.name, got)
			}
		})
	}
}
