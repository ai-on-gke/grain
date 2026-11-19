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
	"slices"
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
	var canonical struct {
		Defs map[string]struct {
			Properties map[string]struct{ Enum []string }
		} `json:"$defs"`
	}
	schema, err := os.ReadFile("../../../design/spec/gates/contracts.schema.json")
	must(err)
	must(json.Unmarshal(schema, &canonical))
	evidenceKinds := canonical.Defs["evidence"].Properties["kind"].Enum
	if len(evidenceKinds) == 0 {
		t.Fatal("canonical evidence kinds unavailable")
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
		assertShadowView(t, v)
		if legacy() != before {
			t.Fatal("shadow observation mutated legacy records")
		}
		select {
		case <-wake:
			t.Fatal("shadow observation woke legacy listeners")
		default:
		}
		if path := os.Getenv("GRAIN_TEST_OUTCOME_FIXTURE"); path != "" {
			raw, err := json.Marshal(v)
			must(err)
			file, err := os.OpenFile(path+".jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			must(err)
			_, err = file.Write(append(raw, '\n'))
			must(err)
			must(file.Close())
		}
		for _, result := range v.Results {
			for _, evidence := range result.Result.Evidence {
				if !slices.Contains(evidenceKinds, evidence.Kind) {
					t.Fatalf("noncanonical evidence kind %q for %s", evidence.Kind, result.Result.BindingID)
				}
			}
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
		if path := os.Getenv("GRAIN_TEST_OUTCOME_FIXTURE"); path != "" {
			raw, err := json.Marshal(v)
			must(err)
			file, err := os.OpenFile(path+".jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			must(err)
			_, err = file.Write(append(raw, '\n'))
			must(err)
			must(file.Close())
		}
		for _, result := range v.Results {
			for _, evidence := range result.Result.Evidence {
				if !slices.Contains(evidenceKinds, evidence.Kind) {
					t.Fatalf("noncanonical evidence kind %q for %s", evidence.Kind, result.Result.BindingID)
				}
			}
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
		if path := os.Getenv("GRAIN_TEST_OUTCOME_FIXTURE"); path != "" {
			must(os.WriteFile(path+".positive", raw, 0600))
		}
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
		task.Grants = append(task.Grants, model.CanonicalGrant(model.APIGrantString("tasks:read", "acme/outcomes")))
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
		if path := os.Getenv("GRAIN_TEST_OUTCOME_FIXTURE"); path != "" {
			raw, err := json.Marshal(v)
			must(err)
			must(os.WriteFile(path+".cls", raw, 0600))
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
	t.Run("child-success-cannot-replay-a-cleared-or-reopened-finish", func(t *testing.T) {
		parent := seed("outcome-stale-finish-parent")
		for _, product := range []string{"nothing_needed", "report"} {
			t.Run(product, func(t *testing.T) {
				child := seed("outcome-stale-finish-" + product)
				outcome := model.NothingNeededOutcome
				if product == "report" {
					outcome = "succeeded"
					child.Intent = model.IntentAnalyze
					child.Proposer = model.TaskProposer{TaskID: parent.ID, RunID: "filing-run", Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: parent.ID}}}
					must(s.PutTask(ctx, child))
				}
				settle(child, outcome, product, "The original successful disposition")
				must(s.ObserveClose(ctx, child.ID, now, model.GrainPrincipal, "finished", func(o *model.Observation) { o.ClosedAt = &now }))
				selection := gates.ChildrenConfig{Tasks: []string{child.ID}, Predicate: "success"}
				childStatus := func() string {
					t.Helper()
					for _, r := range observe(parent, selection).Results {
						if r.Result.Type == "children" {
							return r.Result.Status
						}
					}
					t.Fatal("missing child result")
					return ""
				}
				if got := childStatus(); got != "met" {
					t.Fatalf("original disposition=%s", got)
				}
				// A reply can clear completion independently of reopening.
				completedAt := now
				must(s.ObserveField(ctx, child.ID, now, func(o *model.Observation) { o.CompletedAt = nil }))
				if got := childStatus(); got != "unknown" {
					t.Fatalf("child without current completion reused success: %s", got)
				}
				must(s.ObserveField(ctx, child.ID, now, func(o *model.Observation) { o.CompletedAt = &completedAt }))
				if got := childStatus(); got != "met" {
					t.Fatalf("restored original completion=%s", got)
				}
				// Reopen preserves delivered-product milestones, but a later
				// manual close cannot restore the child's previous success proof.
				now = now.Add(time.Second)
				must(s.ObserveField(ctx, child.ID, now, func(o *model.Observation) { o.ClosedAt = nil }))
				_, err := s.RecordTaskEvent(ctx, model.TaskEvent{TaskID: child.ID, Kind: model.TaskEventReopened, At: now, By: human})
				must(err)
				now = now.Add(time.Second)
				must(s.ObserveClose(ctx, child.ID, now, human, "stop this work", func(o *model.Observation) { o.ClosedAt = &now }))
				if got := childStatus(); got != "unknown" {
					t.Fatalf("reclosed child reused pre-reopen success: %s", got)
				}
				closed := gates.ChildrenConfig{Tasks: []string{child.ID}, Predicate: "closed"}
				for _, r := range observe(parent, closed).Results {
					if r.Result.Type == "children" && r.Result.Status != "met" {
						t.Fatal("explicit closure predicate changed with success provenance")
					}
				}
			})
		}
	})

	t.Run("no-work-acknowledges-the-finish-product-without-a-report", func(t *testing.T) {
		for _, body := range []string{"", "Already correct", strings.Repeat("x", 8193)} {
			task := seed(fmt.Sprintf("outcome-no-work-ack-%d", len(body)))
			task.NothingNeededAction = model.NothingNeededAck
			must(s.PutTask(ctx, task))
			settle(task, model.NothingNeededOutcome, "nothing_needed", body)
			v := observe(task)
			if status(v, "legacy_nothing_needed_ack") != "pending" {
				t.Fatal("typed no-work finish was coupled to report completeness")
			}
			if v.Results[1].Result.Status != "unknown" || len(v.Results[1].Result.Waits) != 0 {
				t.Fatalf("no-work finish became pending explicit report receipt: body=%d %+v", len(body), v.Results[1].Result)
			}
			// A close simultaneous with settlement cannot attest receipt.
			must(s.ObserveClose(ctx, task.ID, now, human, "same clock", func(o *model.Observation) { o.ClosedAt = &now }))
			if status(observe(task), "legacy_nothing_needed_ack") != "unknown" {
				t.Fatal("ambiguous same-clock no-work receipt passed")
			}
			now = now.Add(time.Second)
			must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.ClosedAt = nil }))
			must(s.ObserveClose(ctx, task.ID, now, model.GrainPrincipal, "automated close", func(o *model.Observation) { o.ClosedAt = &now }))
			if status(observe(task), "legacy_nothing_needed_ack") != "unknown" {
				t.Fatal("automation became a person's acknowledgment")
			}
			now = now.Add(time.Second)
			must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.ClosedAt = nil }))
			must(s.ObserveClose(ctx, task.ID, now, human, "read the no-work result", func(o *model.Observation) { o.ClosedAt = &now }))
			v = observe(task)
			if status(v, "legacy_nothing_needed_ack") != "met" {
				t.Fatalf("verified no-work receipt omitted: body=%d status=%s", len(body), status(v, "legacy_nothing_needed_ack"))
			}
			if v.Results[1].Result.Status != "unknown" || len(v.Results[1].Result.Waits) != 0 {
				t.Fatalf("no-work receipt was reused as explicit report acknowledgment: body=%d %+v", len(body), v.Results[1].Result)
			}
			replayed, _, err := model.New(db).ObserveTaskGates(ctx, task.ID, now)
			must(err)
			if gates.Digest(v) != gates.Digest(replayed) {
				t.Fatal("no-work receipt failed durable replay")
			}
			// A reply/reopen clears completion even though the finish remains.
			now = now.Add(time.Second)
			must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.CompletedAt = nil; o.ClosedAt = nil }))
			if status(observe(task), "legacy_nothing_needed_ack") == "met" {
				t.Fatal("cleared no-work settlement reused its receipt")
			}
		}
	})

	t.Run("standing-policy-does-not-hide-selected-resource-outcomes", func(t *testing.T) {
		parent := seed("outcome-standing-parent")
		parent.KeepRunning = true
		parent.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/outcomes#70"}}
		must(s.PutTask(ctx, parent))
		source(parent, "acme/outcomes#70", "standing-head", "merged", "")
		child := seed("outcome-standing-child")
		must(s.ObserveClose(ctx, child.ID, now, human, "stop", func(o *model.Observation) { o.ClosedAt = &now }))
		selected := gates.ChildrenConfig{Tasks: []string{child.ID}, Predicate: "closed"}
		v := observe(parent, selected)
		if status(v, "pr_merged") != "met" {
			t.Fatal("standing lifecycle policy hid authenticated integration")
		}
		for i, b := range v.Bindings {
			if b.Type == "children" && v.Results[i].Result.Status != "met" {
				t.Fatal("standing lifecycle policy hid selected child closure")
			}
			if b.Required || b.Purpose != "monitor" || b.Action != nil {
				t.Fatal("observation migrated a standing task requirement")
			}
		}
		replayed, diagnostics, err := model.New(db).ObserveTaskGates(ctx, parent.ID, now, selected)
		must(err)
		if diagnostics.Counts["outcome_discrepancy"] == 0 || diagnostics.Counts["not_applicable"] != 0 {
			t.Fatal("standing resource comparisons were suppressed by disposition policy")
		}
		if gates.Digest(v) != gates.Digest(replayed) {
			t.Fatal("standing resource observation failed restart replay")
		}
		now = now.Add(time.Second)
		must(s.ObserveField(ctx, child.ID, now, func(o *model.Observation) { o.ClosedAt = nil }))
		for _, r := range observe(parent, selected).Results {
			if r.Result.Type == "children" && r.Result.Status != "pending" {
				t.Fatal("reopen did not invalidate standing child prediction")
			}
		}
		private := seed("outcome-standing-private")
		private.Target = &model.RepoRef{Owner: "acme", Name: "private"}
		must(s.PutTask(ctx, private))
		for _, r := range observe(parent, gates.ChildrenConfig{Tasks: []string{private.ID}, Predicate: "closed"}).Results {
			if r.Result.Type == "children" && (r.Result.Status != "unknown" || len(r.Result.Evidence) > 0) {
				t.Fatal("standing selection widened child read scope")
			}
		}
		now = now.Add(73 * time.Hour)
		if status(observe(parent), "pr_merged") != "unknown" {
			t.Fatal("standing integration reused expired source")
		}
	})

	t.Run("late-read-revalidates-local-finish-after-newer-acceptance", func(t *testing.T) {
		task := seed("outcome-late-local-finish")
		task.NothingNeededAction = model.NothingNeededAck
		must(s.PutTask(ctx, task))
		settle(task, model.NothingNeededOutcome, "nothing_needed", "")
		now = now.Add(time.Second)
		must(s.ObserveClose(ctx, task.ID, now, human, "acknowledged", func(o *model.Observation) { o.ClosedAt = &now }))
		writeAt := now.Add(2 * time.Second)
		now = now.Add(10 * time.Second)
		accepted := observe(task)
		if status(accepted, "legacy_nothing_needed_ack") != "met" {
			t.Fatal("setup lacks acknowledged finish")
		}
		// An earlier-started writer commits after the newer observation.
		// The older reader now sees the changed local facts under its lock.
		now = writeAt
		must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.ClosedAt = nil; o.CompletedAt = nil }))
		replayed := observe(task)
		if status(replayed, "legacy_nothing_needed_ack") == "met" || status(replayed, "delivery-nothing_needed") == "met" {
			t.Fatal("late request replayed stale local finish/receipt after reopen")
		}
		if replayed.SourceRevision == accepted.SourceRevision || replayed.ComparedAt.Before(accepted.ComparedAt) {
			t.Fatal("local source change was dropped or accepted time rewound")
		}
	})

	t.Run("late-CI-read-revalidates-selected-child-without-rewinding-external-head", func(t *testing.T) {
		parent := seed("outcome-late-ci-parent")
		parent.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/outcomes#71"}}
		must(s.PutTask(ctx, parent))
		child := seed("outcome-late-ci-child")
		must(s.ObserveClose(ctx, child.ID, now, human, "done", func(o *model.Observation) { o.ClosedAt = &now }))
		selected := gates.ChildrenConfig{Tasks: []string{child.ID}, Predicate: "closed"}
		_, _, fence, err := s.GateChecksInput(ctx, parent.ID)
		must(err)
		subject := model.CheckSubjects(parent)[0]
		facts := func(at time.Time, head string) gates.ChecksFacts {
			return gates.ChecksFacts{TaskID: parent.ID, Subject: subject, Head: head, PolicyVersion: gates.Digest("grain-policy"), PolicyComplete: true, Mode: "grain", ObservedAt: at, ValidUntil: at.Add(time.Minute),
				Sources: []gates.CheckSource{{Kind: "grain", Head: head, Version: gates.Digest(head), Complete: true, All: true, Verdict: "success", LegacyAggregate: true,
					Attempts: []gates.CheckAttempt{{Identity: "grain/go", Names: []string{"go"}, Head: head, Version: gates.Digest(head + "-job"), Order: 1, State: "completed", Conclusion: "success"}}}}}
		}
		read := func(at time.Time, head string) *gates.ObservationView {
			t.Helper()
			before, wake := legacy(), s.Changed()
			v, _, err := model.New(db).ObserveTaskGatesWithChecks(ctx, parent.ID, at, fence, []gates.ChecksFacts{facts(at, head)}, selected)
			must(err)
			if legacy() != before {
				t.Fatal("late source revalidation mutated legacy rows")
			}
			select {
			case <-wake:
				t.Fatal("late source revalidation woke legacy work")
			default:
			}
			for _, entry := range v.Results {
				if len(entry.Result.Work) > 0 || len(entry.Result.Actions) > 0 {
					t.Fatal("late source revalidation proposed effects")
				}
			}
			if path := os.Getenv("GRAIN_TEST_OUTCOME_FIXTURE"); path != "" {
				raw, err := json.Marshal(v)
				must(err)
				file, err := os.OpenFile(path+".jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
				must(err)
				_, err = file.Write(append(raw, '\n'))
				must(err)
				must(file.Close())
			}
			return v
		}
		childStatus := func(v *gates.ObservationView) string {
			for _, entry := range v.Results {
				if entry.Result.Type == "children" {
					return entry.Result.Status
				}
			}
			t.Fatal("selected child omitted")
			return ""
		}
		acceptedAt := now.Add(10 * time.Second)
		accepted := read(acceptedAt, "H2")
		if childStatus(accepted) != "met" || status(accepted, gates.ChecksBinding(facts(acceptedAt, "H2"), false).ID) != "met" {
			t.Fatal("setup lacks positive child/CI observations")
		}
		writeAt := now.Add(2 * time.Second)
		must(s.ObserveField(ctx, child.ID, writeAt, func(o *model.Observation) { o.ClosedAt = nil }))
		late := read(writeAt, "H1")
		if childStatus(late) != "pending" {
			t.Fatal("late request replayed a closed child after its scoped source changed")
		}
		for _, entry := range late.Results {
			if entry.Result.Type == "checks" && entry.Result.Status != "unknown" {
				t.Fatal("older remote CI evidence rewound a newer accepted head")
			}
		}
		if late.ComparedAt.Before(accepted.ComparedAt) || late.SourceRevision == accepted.SourceRevision {
			t.Fatal("late local source change lost its monotonic acceptance/version")
		}
		fresh := read(acceptedAt.Add(time.Second), "H2")
		if childStatus(fresh) != "pending" || status(fresh, gates.ChecksBinding(facts(acceptedAt, "H2"), false).ID) != "met" {
			t.Fatal("fresh external evidence did not recover independently of child outcome")
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

	t.Run("outcomes-coexist-with-shipped-review-and-approval-monitors", func(t *testing.T) {
		task := seed("outcome-combined")
		child := seed("outcome-combined-child")
		must(s.ObserveClose(ctx, child.ID, now, model.GrainPrincipal, "closed", func(o *model.Observation) { o.ClosedAt = &now }))
		task.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/outcomes#121"}, {Kind: model.LinkReviewTask, Target: child.ID}}
		must(s.PutTask(ctx, task))
		settle(task, "success", "change", "Delivered change")
		source(task, "acme/outcomes#121", "combined-head", "merged", "")
		must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.PrMergedAt = &now }))
		selection := gates.ChildrenConfig{Tasks: []string{child.ID}, Predicate: "closed"}
		view := observe(task, selection)
		if status(view, "delivery-change") != "met" || status(view, "pr_merged") != "met" {
			t.Fatal("review provenance gap hid delivered/integrated change")
		}
		types := map[string]bool{}
		for i, binding := range view.Bindings {
			types[binding.Type] = true
			if (binding.Type == "review" && !binding.Origin.Imported || binding.Type == "approval") && view.Results[i].Result.Status != "unknown" {
				t.Fatal("integration fabricated current review/approval proof")
			}
		}
		if len(types) != 6 || len(view.Bindings) > 64 {
			t.Fatalf("combined monitor projection: %v", types)
		}
		_, diag, err := s.ObserveTaskGates(ctx, task.ID, now, selection)
		must(err)
		if diag.Counts["review_approval_provenance_gap"] == 0 || diag.Counts["equivalent_outcome_milestones"] == 0 {
			t.Fatal("one gate family hid the other's comparison")
		}
	})

	t.Run("valid-long-resource-identities-fit-the-wire-contract", func(t *testing.T) {
		task := seed("outcome-long-resource")
		task.Target = &model.RepoRef{Owner: strings.Repeat("a", 39), Name: strings.Repeat("b", 100)}
		ref := task.Target.String() + "#12345"
		task.Links = []model.Link{{Kind: model.LinkFixes, Target: ref}}
		must(s.PutTask(ctx, task))
		settle(task, "success", "change", "Delivered change in a long repository name")
		source(task, ref, "long-resource-head", "merged", "")
		view := observe(task)
		if status(view, "delivery-change") != "met" || status(view, "pr_merged") != "met" {
			t.Fatal("long valid resource lost its positive proof")
		}
		now = now.Add(time.Millisecond)
		fresh, err := s.GetTask(ctx, task.ID)
		must(err)
		s.RecordGateOutcomeSource(ctx, task.ID, model.GateOutcomeSource{Resource: ref, Head: "long-resource-head", State: "merged", Policy: model.GateOutcomePolicy(*fresh), Attribution: "external:" + strings.Repeat("attributed-provider-", 50), At: now})
		view = observe(task)
		if status(view, "pr_merged") != "met" {
			t.Fatal("bounded identity changed the predicate")
		}
		for _, entry := range view.Results {
			for _, evidence := range entry.Result.Evidence {
				if len([]rune(evidence.ID)) > 256 {
					t.Fatal("long valid resource exceeded canonical evidence ID limit")
				}
			}
			for _, dependency := range entry.Result.Dependencies {
				if len([]rune(dependency.Key)) > 256 {
					t.Fatal("long valid resource exceeded canonical dependency key limit")
				}
			}
		}
	})

	t.Run("report-only-implement-task-delivery-and-receipt", func(t *testing.T) {
		// Ordinary UI/CLI/template tasks are filed as IntentImplement. Without a
		// change link (AwaitsAck true), a settled closing note is projected as a
		// report product for delivery(report/current_v1) and acknowledgment(explicit_receipt),
		// even without a separate gate_outcome_source finish row.
		task := seed("outcome-implement-report")
		if v := observe(task); v.Results[0].Result.Status != "unmet" || v.Results[1].Result.Status != "unknown" {
			t.Fatalf("pre-run implement report statuses: %+v", v.Results[:2])
		}
		now = now.Add(time.Second)
		runID := task.ID + "-run"
		must(s.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Attempt: 1, StartedAt: now, Sandbox: "test"}, model.Limits{}))
		now = now.Add(time.Second)
		if v := observe(task); v.Results[0].Result.Status != "pending" || v.Results[1].Result.Status != "unknown" {
			t.Fatalf("running implement report statuses: %+v", v.Results[:2])
		}
		_, err := s.AddComment(ctx, model.Comment{
			TaskID:    task.ID,
			Author:    model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: task.ID}},
			Body:      "Investigation summary: no code change needed, findings attached in text.",
			CreatedAt: now,
		})
		must(err)
		must(s.FinishRun(ctx, runID, now, "succeeded", ""))
		must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.CompletedAt = &now }))
		v, diag, err := s.ObserveTaskGates(ctx, task.ID, now)
		must(err)
		assertShadowView(t, v)
		if v.Results[0].Result.Status != "met" || v.Results[1].Result.Status != "pending" || status(v, "legacy_report") != "pending" || status(v, "delivery-change") != "unmet" || status(v, "delivery-any") != "met" {
			t.Fatalf("settled report-only implement statuses: %+v", v.Results)
		}
		if len(v.Results[0].Result.Evidence) != 1 || len(v.Results[1].Result.Evidence) != 1 || len(v.Results[1].Result.Waits) != 1 {
			t.Fatalf("expected report evidence and predicted wait: %+v", v.Results[:2])
		}
		if diag.Counts["equivalent_milestones"] == 0 {
			t.Fatalf("expected equivalent_milestones count: %+v", diag.Counts)
		}
		// Unverified or simultaneous close cannot satisfy explicit receipt.
		must(s.ObserveClose(ctx, task.ID, now, human, "same clock", func(o *model.Observation) { o.ClosedAt = &now }))
		if v := observe(task); v.Results[0].Result.Status != "met" || v.Results[1].Result.Status != "unknown" {
			t.Fatalf("same-clock close satisfied explicit receipt: %+v", v.Results[:2])
		}
		now = now.Add(time.Second)
		must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.ClosedAt = nil }))
		must(s.ObserveClose(ctx, task.ID, now, human, "acknowledged", func(o *model.Observation) { o.ClosedAt = &now }))
		acked := observe(task)
		if acked.Results[0].Result.Status != "met" || acked.Results[1].Result.Status != "met" || status(acked, "legacy_report") != "met" {
			t.Fatalf("verified person close did not satisfy explicit receipt: %+v", acked.Results)
		}
		if len(acked.Results[1].Result.Evidence) != 2 {
			t.Fatalf("expected report and receipt evidence: %+v", acked.Results[1].Result.Evidence)
		}
		replayed, _, err := model.New(db).ObserveTaskGates(ctx, task.ID, now)
		must(err)
		if gates.Digest(acked) != gates.Digest(replayed) {
			t.Fatal("report-only implement observation failed durable replay")
		}
		// Prior v9 observer cache regenerates through unknown_unsupported_config.
		prior := *acked
		prior.Bindings = append([]gates.Binding(nil), acked.Bindings...)
		for i := range prior.Bindings {
			prior.Bindings[i].Origin.Revision = "builtin-gate-observer-v9"
		}
		rawPrior, _ := json.Marshal(prior)
		_, err = db.ExecContext(ctx, "UPDATE `gate_observation_shadow` SET `view_json` = ? WHERE `task_id` = ?", string(rawPrior), task.ID)
		must(err)
		regenerated, regenDiag, err := s.ObserveTaskGates(ctx, task.ID, now)
		must(err)
		if regenerated.Results[0].Result.Status != "unknown" || regenerated.Results[1].Result.Status != "unknown" || regenDiag.Counts["unknown_unsupported_config"] == 0 {
			t.Fatalf("old v9 profile replayed without regeneration: %+v %+v", regenerated.Results[:2], regenDiag.Counts)
		}
		now = now.Add(2 * time.Minute)
		if recovered := observe(task); recovered.Results[0].Result.Status != "met" || recovered.Results[1].Result.Status != "met" {
			t.Fatalf("regenerated v10 profile did not recover: %+v", recovered.Results[:2])
		}
		// Adding a change link removes AwaitsAck: the closing comment must no
		// longer project as a report product or satisfy explicit receipt.
		now = now.Add(time.Second)
		task.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/outcomes#404"}}
		must(s.PutTask(ctx, task))
		linked := observe(task)
		if linked.Results[0].Result.Status != "unknown" || linked.Results[1].Result.Status != "unknown" || status(linked, "legacy_report") != "unknown" || status(linked, "delivery-change") != "unknown" {
			t.Fatalf("change-linked implement task without finish source projected as report: %+v", linked.Results)
		}
		if len(linked.Results[1].Result.Evidence) != 0 || len(linked.Results[1].Result.Waits) != 0 {
			t.Fatalf("change-linked task retained explicit report receipt evidence/waits: %+v", linked.Results[1].Result)
		}
		s.RecordGateOutcomeSource(ctx, task.ID, model.GateOutcomeSource{Resource: "finish", RunID: runID, Product: "change", State: "settled", Policy: model.GateOutcomePolicy(task), At: now})
		source(task, "acme/outcomes#404", "head-404", "open", "")
		verifiedChange := observe(task)
		if verifiedChange.Results[0].Result.Status != "unmet" || verifiedChange.Results[1].Result.Status != "unknown" || status(verifiedChange, "delivery-change") != "met" || len(verifiedChange.Results[1].Result.Evidence) != 0 || len(verifiedChange.Results[1].Result.Waits) != 0 {
			t.Fatalf("verified change task leaked report/receipt projection: %+v", verifiedChange.Results)
		}
		// Settled review and nothing_needed tasks with comments never fall back
		// to report delivery or explicit report receipt, with or without finish records.
		for _, kind := range []string{"review-no-finish", "review-with-finish", "nowork-no-finish"} {
			other := seed("outcome-" + kind)
			if strings.HasPrefix(kind, "review") {
				other.Filing.Reason = model.ReasonReview
				must(s.PutTask(ctx, other))
			}
			now = now.Add(time.Second)
			otherRun := other.ID + "-run"
			must(s.StartRun(ctx, model.Run{ID: otherRun, TaskID: other.ID, Attempt: 1, StartedAt: now, Sandbox: "test"}, model.Limits{}))
			now = now.Add(time.Second)
			_, err := s.AddComment(ctx, model.Comment{
				TaskID:    other.ID,
				Author:    model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: other.ID}},
				Body:      "Closing note on non-report task",
				CreatedAt: now,
			})
			must(err)
			runOutcome := "succeeded"
			if kind == "nowork-no-finish" {
				runOutcome = model.NothingNeededOutcome
			}
			must(s.FinishRun(ctx, otherRun, now, runOutcome, ""))
			must(s.ObserveField(ctx, other.ID, now, func(o *model.Observation) { o.CompletedAt = &now }))
			if kind == "review-with-finish" {
				s.RecordGateOutcomeSource(ctx, other.ID, model.GateOutcomeSource{Resource: "finish", RunID: otherRun, Product: "review", State: "settled", Policy: model.GateOutcomePolicy(other), At: now})
			}
			res := observe(other)
			wantReport := "unknown"
			if kind == "review-with-finish" {
				wantReport = "unmet"
			}
			if res.Results[0].Result.Status != wantReport || res.Results[1].Result.Status != "unknown" || status(res, "legacy_report") != "unknown" || len(res.Results[1].Result.Evidence) != 0 || len(res.Results[1].Result.Waits) != 0 {
				t.Fatalf("%s misprojected as report/receipt: %+v", kind, res.Results)
			}
		}
	})

}
