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

package gateobservationtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunOutcomes exercises the ordinary durable records on both real engines.
func RunOutcomes(t *testing.T, s *model.Store, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP}
	repo := model.RepoRef{Owner: "acme", Name: "outcomes"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seed := func(id string) model.Task {
		task := model.Task{ID: id, Intent: model.IntentImplement, Title: "Observe " + id, Target: &repo, Filing: model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect}, Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective, CreatedAt: &now}
		must(s.PutTask(ctx, task))
		fresh, err := s.GetTask(ctx, id)
		must(err)
		return *fresh
	}
	legacy := func() string {
		t.Helper()
		var snapshot []any
		for _, table := range []string{"task", "task_observation", "task_run", "task_run_tool", "task_comment", "task_attachment", "task_goal", "task_state", "task_ready", "task_pause", "task_link", "task_grant", "task_event", "integration_event"} {
			rows, err := db.QueryContext(ctx, "SELECT * FROM `"+table+"`")
			must(err)
			cols, err := rows.Columns()
			must(err)
			var records [][]string
			for rows.Next() {
				values := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range values {
					ptrs[i] = &values[i]
				}
				must(rows.Scan(ptrs...))
				record := make([]string, len(values))
				for i, v := range values {
					record[i] = fmt.Sprint(v)
				}
				records = append(records, record)
			}
			must(rows.Err())
			rows.Close()
			snapshot = append(snapshot, records)
		}
		raw, _ := json.Marshal(snapshot)
		return string(raw)
	}
	observe := func(task model.Task, selection ...gates.ChildrenConfig) *gates.ObservationView {
		t.Helper()
		before := legacy()
		wake := s.Changed()
		v, _, err := s.ObserveTaskGates(ctx, task.ID, now, selection...)
		must(err)
		if legacy() != before {
			t.Fatal("shadow observation mutated legacy records")
		}
		select {
		case <-wake:
			t.Fatal("shadow observation woke legacy listeners")
		default:
		}
		for _, result := range v.Results {
			if len(result.Result.Work) > 0 || len(result.Result.Actions) > 0 {
				t.Fatal("shadow emitted work or actions")
			}
		}
		return v
	}
	status := func(v *gates.ObservationView, id string) string {
		t.Helper()
		for i, b := range v.Bindings {
			if b.ID == id {
				return v.Results[i].Result.Status
			}
		}
		t.Fatalf("missing binding %s", id)
		return ""
	}
	source := func(task model.Task, resource, head, state, canonical string) {
		t.Helper()
		fresh, err := s.GetTask(ctx, task.ID)
		must(err)
		task = *fresh
		before := legacy()
		wake := s.Changed()
		s.RecordGateOutcomeSource(ctx, task.ID, model.GateOutcomeSource{Resource: resource, Head: head, State: state, Product: canonical, Policy: model.GateOutcomePolicy(task), Attribution: "external-test-observation", At: now})
		if legacy() != before {
			t.Fatal("source recorder mutated legacy state")
		}
		select {
		case <-wake:
			t.Fatal("source recorder woke legacy listeners")
		default:
		}
	}
	settle := func(task model.Task, outcome, product, body string) {
		t.Helper()
		fresh, err := s.GetTask(ctx, task.ID)
		must(err)
		task = *fresh
		run := task.ID + "-run"
		must(s.StartRun(ctx, model.Run{ID: run, TaskID: task.ID, Attempt: 1, StartedAt: now, Sandbox: "test"}, model.Limits{}))
		now = now.Add(time.Second)
		if body != "" {
			_, err := s.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: task.ID}}, Body: body, CreatedAt: now})
			must(err)
		}
		must(s.FinishRun(ctx, run, now, outcome, ""))
		must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.CompletedAt = &now }))
		s.RecordGateOutcomeSource(ctx, task.ID, model.GateOutcomeSource{Resource: "finish", RunID: run, Product: product, State: "settled", Policy: model.GateOutcomePolicy(task), At: now})
	}

	t.Run("pr-head-policy-source-and-restart", func(t *testing.T) {
		task := seed("outcome-pr")
		task.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/outcomes#7"}}
		must(s.PutTask(ctx, task))
		settle(task, "succeeded", "change", "")
		v := observe(task)
		if status(v, "pr_merged") != "unknown" || status(v, "delivery-change") != "unknown" {
			t.Fatal("bare change link proved integration or lost authenticated delivery")
		}
		for _, result := range v.Results {
			for _, dependency := range result.Result.Dependencies {
				if dependency.Head != nil && *dependency.Head == "" {
					t.Fatal("missing head violated the nullable v1 wire contract")
				}
			}
		}
		if path := os.Getenv("GRAIN_TEST_OUTCOME_FIXTURE"); path != "" {
			raw, err := json.MarshalIndent(v, "", "  ")
			must(err)
			must(os.WriteFile(path, raw, 0600))
		}
		source(task, "acme/outcomes#7", "head-1", "open", "")
		v = observe(task)
		if status(v, "pr_merged") != "unmet" {
			t.Fatal("open PR satisfied integration")
		}
		now = now.Add(time.Second)
		source(task, "acme/outcomes#7", "head-1", "merged", "")
		v = observe(task)
		if status(v, "pr_merged") != "met" {
			t.Fatal("positive merge receipt missing")
		}
		raw, _ := json.Marshal(v)
		if !strings.Contains(string(raw), "external-test-observation") || !strings.Contains(string(raw), "head-1") {
			t.Fatal("attribution/head provenance missing")
		}
		replay, _, err := model.New(db).ObserveTaskGates(ctx, task.ID, now)
		must(err)
		if replay.Results[0].Revision != v.Results[0].Revision {
			t.Fatal("restart duplicated accepted collection")
		}
		now = now.Add(time.Second)
		source(task, "acme/outcomes#7", "head-1", "closed", "")
		if status(observe(task), "delivery-change") != "met" || status(observe(task), "pr_merged") != "unmet" {
			t.Fatal("delivered change was confused with integration")
		}
		now = now.Add(time.Second)
		source(task, "acme/outcomes#7", "head-1", "merged", "")
		// A slow old failed collector cannot rewind the positive source.
		s.RecordGateOutcomeSource(ctx, task.ID, model.GateOutcomeSource{Resource: "acme/outcomes#7", State: "unknown", At: now.Add(-time.Second)})
		if status(observe(task), "pr_merged") != "met" {
			t.Fatal("older source replaced current receipt")
		}
		task.Grants = append(task.Grants, model.CanonicalGrant("api:tasks:read@acme/outcomes"))
		must(s.PutTask(ctx, task))
		if status(observe(task), "pr_merged") != "unknown" {
			t.Fatal("old authorization policy reused")
		}
		now = now.Add(time.Second)
		source(task, "acme/outcomes#7", "head-2", "open", "")
		if status(observe(task), "pr_merged") != "unmet" {
			t.Fatal("new open head inherited merge")
		}
		now = now.Add(time.Second)
		source(task, "acme/outcomes#7", "", "unknown", "")
		if status(observe(task), "pr_merged") != "unknown" {
			t.Fatal("denied/missing source converted to negative or success")
		}
	})
	t.Run("multiple-cl-renumbering-and-completeness", func(t *testing.T) {
		task := seed("outcome-cls")
		task.Target = nil
		task.ChangeTarget = &model.ChangeTarget{Kind: "changelist", Spec: json.RawMessage(`{"workspace":"multiple-cls"}`)}
		task.Links = []model.Link{{Kind: model.LinkChangelist, Target: "cl/11"}, {Kind: model.LinkChangelist, Target: "cl/12"}}
		must(s.PutTask(ctx, task))
		settle(task, "succeeded", "change", "")
		source(task, "cl/11", "snapshot/3", "submitted", "cl/111")
		if status(observe(task), "cl_submitted") != "unknown" {
			t.Fatal("partial CL set or unverified renumbering passed")
		}
		task.Links = append(task.Links, model.Link{Kind: model.LinkChangelistSubmitted, Target: "cl/111 (was cl/11)"})
		must(s.PutTask(ctx, task))
		source(task, "cl/12", "snapshot/4", "open", "")
		if status(observe(task), "cl_submitted") != "unmet" {
			t.Fatal("one submitted CL satisfied all-change scope")
		}
		now = now.Add(time.Second)
		source(task, "cl/12", "snapshot/4", "submitted", "cl/12")
		v := observe(task)
		if status(v, "cl_submitted") != "met" {
			t.Fatal("complete submitted CL set missing")
		}
		task.Links = append(task.Links, model.Link{Kind: model.LinkChangelist, Target: "cl/13"})
		must(s.PutTask(ctx, task))
		if status(observe(task), "cl_submitted") != "unknown" {
			t.Fatal("new scoped CL did not invalidate collection")
		}
		source(task, "cl/13", "snapshot/1", "submitted", "cl/13")
		if status(observe(task), "cl_submitted") != "met" {
			t.Fatal("third submitted CL missing")
		}
		now = now.Add(73 * time.Hour)
		if status(observe(task), "cl_submitted") != "unknown" {
			t.Fatal("expired source refreshed itself through task reads")
		}
	})
	t.Run("explicit-child-outcomes-relations-and-reopen", func(t *testing.T) {
		parent := seed("outcome-parent")
		child := seed("outcome-failed-child")
		settle(child, "cancelled", "report", "")
		must(s.ObserveClose(ctx, child.ID, now, human, "cancel", func(o *model.Observation) { o.ClosedAt = &now; o.PrClosedAt = &now }))
		selections := []gates.ChildrenConfig{{Tasks: []string{child.ID}, Predicate: "closed"}, {Tasks: []string{child.ID}, Predicate: "success"}, {Tasks: []string{child.ID}, Predicate: "merged"}}
		check := func(v *gates.ObservationView, closed, success, merged string) {
			t.Helper()
			for i, b := range v.Bindings {
				if b.Type != "children" {
					continue
				}
				var c gates.ChildrenConfig
				must(json.Unmarshal(b.Config, &c))
				want := map[string]string{"closed": closed, "success": success, "merged": merged}[c.Predicate]
				if v.Results[i].Result.Status != want {
					t.Fatalf("%s=%s want %s", c.Predicate, v.Results[i].Result.Status, want)
				}
			}
		}
		v := observe(parent, selections...)
		check(v, "met", "unmet", "unmet")
		before := v.SourceRevision
		must(s.ObserveField(ctx, child.ID, now, func(o *model.Observation) { o.ClosedAt = nil; o.PrClosedAt = nil }))
		v = observe(parent, selections...)
		check(v, "pending", "unmet", "pending")
		if before == v.SourceRevision {
			t.Fatal("reopen did not invalidate parent")
		}
		_, err := s.AddComment(ctx, model.Comment{TaskID: child.ID, Author: model.Attribution{Actor: human}, Body: "Please continue", CreatedAt: now})
		must(err)
		next := observe(parent, selections...)
		if next.SourceRevision == v.SourceRevision {
			t.Fatal("incoming child message did not invalidate parent")
		}
		// Spawned/provenance-only children are never selected automatically.
		advisory := seed("outcome-advisory")
		advisory.Links = []model.Link{{Kind: model.LinkProposedBy, Target: parent.ID}}
		must(s.PutTask(ctx, advisory))
		if observe(parent).SourceRevision == "" {
			t.Fatal("missing observation")
		}
		for _, b := range observe(parent).Bindings {
			if b.Type == "children" {
				t.Fatal("spawned child became a requirement")
			}
		}
		missing := gates.ChildrenConfig{Tasks: []string{"missing-private-child"}, Predicate: "success"}
		for i, b := range observe(parent, missing).Bindings {
			if b.Type == "children" && observe(parent, missing).Results[i].Result.Status != "unknown" {
				t.Fatal("missing child succeeded")
			}
		}
		private := seed("outcome-private-child")
		private.Target = &model.RepoRef{Owner: "acme", Name: "private"}
		must(s.PutTask(ctx, private))
		v = observe(parent, gates.ChildrenConfig{Tasks: []string{private.ID}, Predicate: "closed"})
		for _, r := range v.Results {
			if r.Result.Type == "children" && (r.Result.Status != "unknown" || len(r.Result.Evidence) > 0) {
				t.Fatal("private child evidence leaked")
			}
		}
		must(s.SetTaskGoal(ctx, parent.ID, model.GoalDecl{Kind: model.GoalTasksClosed, Spec: model.GoalSpec{Tasks: []string{child.ID}, Until: "closed"}}, "alice", now))
		must(s.SetTaskGoal(ctx, child.ID, model.GoalDecl{Kind: model.GoalTasksClosed, Spec: model.GoalSpec{Tasks: []string{advisory.ID}, Until: "closed"}}, "alice", now))
		must(s.SetTaskGoal(ctx, advisory.ID, model.GoalDecl{Kind: model.GoalTasksClosed, Spec: model.GoalSpec{Tasks: []string{parent.ID}, Until: "closed"}}, "alice", now))
		v = observe(parent)
		for _, r := range v.Results {
			if r.Result.Type == "children" && (r.Result.Status != "unknown" || !strings.Contains(r.Result.Summary, "cycle")) {
				t.Fatal("long relation cycle not diagnosed")
			}
		}
	})
	t.Run("no-action-is-not-success-and-goal-contracts-are-inert", func(t *testing.T) {
		task := seed("outcome-no-action")
		settle(task, "no_action", "report", "a tool ran")
		if status(observe(task), "delivery-any") != "unmet" {
			t.Fatal("tool use/no_action became delivery")
		}
		target := seed("outcome-goal-target")
		target.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/outcomes#9"}}
		must(s.PutTask(ctx, target))
		source(target, "acme/outcomes#9", "head-9", "merged", "")
		must(s.SetTaskGoal(ctx, task.ID, model.GoalDecl{Kind: model.GoalPRMerged, Spec: model.GoalSpec{Task: target.ID}}, "alice", now))
		if status(observe(task), "goal-integration") != "met" {
			t.Fatal("pr-merged observation mapping missing")
		}
		now = now.Add(time.Second)
		source(target, "acme/outcomes#9", "head-10", "open", "")
		if status(observe(task), "goal-integration") != "unmet" {
			t.Fatal("foreign target source failed to invalidate parent")
		}
		clTarget := seed("outcome-cl-goal-target")
		clTarget.Target = nil
		clTarget.ChangeTarget = &model.ChangeTarget{Kind: "changelist", Spec: json.RawMessage(`{"workspace":"goal-cl"}`)}
		clTarget.Links = []model.Link{{Kind: model.LinkChangelist, Target: "cl/99"}}
		must(s.PutTask(ctx, clTarget))
		source(clTarget, "cl/99", "snapshot/1", "submitted", "cl/99")
		coordinator := seed("outcome-cl-coordinator")
		coordinator.Target = nil
		coordinator.ChangeTarget = clTarget.ChangeTarget.Clone()
		must(s.PutTask(ctx, coordinator))
		must(s.SetTaskGoal(ctx, coordinator.ID, model.GoalDecl{Kind: model.GoalCLSubmitted, Spec: model.GoalSpec{Task: clTarget.ID}}, "alice", now))
		if status(observe(coordinator), "goal-integration") != "met" {
			t.Fatal("cl-submitted mapping missing")
		}

	})
	t.Run("report-child-provenance-in-place-head-and-rejected-projections", func(t *testing.T) {
		parent := seed("outcome-answer-parent")
		child := seed("outcome-answer-child")
		child.Intent = model.IntentAnalyze
		child.Proposer = model.TaskProposer{TaskID: parent.ID, RunID: "filing-run", Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: parent.ID}}}
		must(s.PutTask(ctx, child))
		settle(child, "succeeded", "report", "The original child answer")
		must(s.ObserveClose(ctx, child.ID, now, model.GrainPrincipal, "child answered", func(o *model.Observation) { o.ClosedAt = &now }))
		selection := gates.ChildrenConfig{Tasks: []string{child.ID}, Predicate: "success"}
		childStatus := func(v *gates.ObservationView) string {
			for _, r := range v.Results {
				if r.Result.Type == "children" {
					return r.Result.Status
				}
			}
			t.Fatal("child result missing")
			return ""
		}
		if got := childStatus(observe(parent, selection)); got != "met" {
			t.Fatalf("provenance-backed child=%s", got)
		}
		child.Proposer.Attribution.Actor.Source = model.SourceSession
		must(s.PutTask(ctx, child))
		if got := childStatus(observe(parent, selection)); got != "unknown" {
			t.Fatalf("session filing incorrectly auto-acknowledged child: %s", got)
		}
		child.Proposer.Attribution.Actor.Source = ""
		must(s.PutTask(ctx, child))
		_, err := s.AddComment(ctx, model.Comment{TaskID: child.ID, Author: model.Attribution{Actor: human}, Body: "There is more work", CreatedAt: now.Add(time.Second)})
		must(err)
		if got := childStatus(observe(parent, selection)); got != "unknown" {
			t.Fatalf("unseen child message was closure-ready: %s", got)
		}
		// Explicit closed monitoring is preserved even when success is unknown.
		closed := gates.ChildrenConfig{Tasks: []string{child.ID}, Predicate: "closed"}
		if got := childStatus(observe(parent, closed)); got != "met" {
			t.Fatalf("legacy closed wait was relabeled: %s", got)
		}
		owner := seed("outcome-fix-owner")
		owner.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/outcomes#88"}, {Kind: model.LinkTrustBoundaryCleared, Target: "policy@accepted-head"}}
		must(s.PutTask(ctx, owner))
		source(owner, "acme/outcomes#88", "accepted-head", "open", "")
		review := seed("outcome-in-place-review")
		review.Filing.Reason = model.ReasonReview
		review.Links = []model.Link{{Kind: model.LinkPushesReviewedBranch, Target: owner.ID}, {Kind: model.LinkReviewerHead, Target: "accepted-head"}, {Kind: model.LinkReviewsPullRequest, Target: "acme/outcomes#88"}}
		must(s.PutTask(ctx, review))
		settle(review, "succeeded", "review", "Delivered findings after fixes")
		if status(observe(review), "delivery-change") != "met" {
			t.Fatal("accepted in-place fix missing")
		}
		now = now.Add(time.Second)
		source(owner, "acme/outcomes#88", "new-head", "open", "")
		if status(observe(review), "delivery-change") == "met" {
			t.Fatal("owning head change did not invalidate in-place acceptance")
		}
		owner.Target = &model.RepoRef{Owner: "acme", Name: "private"}
		must(s.PutTask(ctx, owner))
		if status(observe(review), "delivery-change") != "unknown" {
			t.Fatal("private owning-change data was treated as a known negative")
		}
		// Failure reconstruction preserves every selected monitor and removes all evidence/predictions.
		view := observe(parent, selection)
		before := legacy()
		wake := s.Changed()
		now = now.Add(time.Millisecond)
		must(s.RecordGateObservationFailure(ctx, parent.ID, now, view))
		if legacy() != before {
			t.Fatal("extended rejection altered legacy records")
		}
		select {
		case <-wake:
			t.Fatal("rejection woke dispatch")
		default:
		}
		rejected, _, err := model.New(db).ObserveTaskGates(ctx, parent.ID, now, selection)
		must(err)
		if len(rejected.Bindings) != len(view.Bindings) || !rejected.Dirty {
			t.Fatal("extended failure projection was truncated")
		}
		for _, result := range rejected.Results {
			if result.Result.Status != "error" || len(result.Result.Evidence) > 0 || len(result.Result.Waits) > 0 || len(result.Result.Actions) > 0 || len(result.Result.Work) > 0 {
				t.Fatal("rejection retained accepted evidence or effects")
			}
		}
	})

	t.Run("reopen-and-source-order-cannot-replay-old-product", func(t *testing.T) {
		task := seed("outcome-reopened-no-work")
		settle(task, model.NothingNeededOutcome, "nothing_needed", "Validated no-work reason")
		if status(observe(task), "delivery-nothing_needed") != "met" {
			t.Fatal("no-work product missing")
		}
		must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.CompletedAt = nil; o.ClosedAt = nil }))
		if status(observe(task), "delivery-any") == "met" || status(observe(task), "delivery-nothing_needed") == "met" {
			t.Fatal("finish receipt restored a legacy settlement cleared by reopen/message")
		}
		change := seed("outcome-ambiguous-heads")
		change.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/outcomes#100"}}
		must(s.PutTask(ctx, change))
		source(change, "acme/outcomes#100", "head-a", "open", "")
		source(change, "acme/outcomes#100", "head-b", "open", "")
		if status(observe(change), "pr_merged") != "unknown" {
			t.Fatal("same-time conflicting heads were ordered by last writer")
		}
		source(change, "acme/outcomes#100", "head-a", "open", "")
		restarted, _, err := model.New(db).ObserveTaskGates(ctx, change.ID, now)
		must(err)
		if status(restarted, "pr_merged") != "unknown" {
			t.Fatal("replay resolved an ambiguous collection without a new version")
		}
		now = now.Add(time.Second)
		source(change, "acme/outcomes#100", "head-b", "open", "")
		if status(observe(change), "pr_merged") != "unmet" {
			t.Fatal("new scoped observation did not recover ambiguity")
		}
	})

}
