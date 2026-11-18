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

// Package gateobservationtest qualifies diagnostic storage on both engines.
package gateobservationtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

func Run(t *testing.T, s *model.Store, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP}
	task := model.Task{ID: "shadow-test", Intent: model.IntentAnalyze, Title: "Find the cause", Filing: model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect}, Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective, CreatedAt: &now}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutTask(ctx, task))
	// Snapshot every legacy table affected by progress, permissions or merge.
	legacy := func() string {
		var all []any
		for _, table := range []string{"task", "task_observation", "task_run", "task_run_tool", "task_comment", "task_attachment", "task_goal", "task_state", "task_ready", "task_pause", "task_link", "task_grant", "task_grant_wait", "task_event", "integration_event"} {
			rows, err := db.QueryContext(ctx, "SELECT * FROM `"+table+"`")
			must(err)
			cols, err := rows.Columns()
			must(err)
			var records [][]string
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				must(rows.Scan(ptrs...))
				r := make([]string, len(vals))
				for i, v := range vals {
					r[i] = fmt.Sprint(v)
				}
				records = append(records, r)
			}
			must(rows.Err())
			rows.Close()
			all = append(all, records)
		}
		b, _ := json.Marshal(all)
		return string(b)
	}
	observe := func(delivery, ack string) {
		t.Helper()
		before := legacy()
		changed := s.Changed()
		v, _, err := s.ObserveTaskGates(ctx, task.ID, now)
		must(err)
		if v.Results[0].Result.Status != delivery || v.Results[1].Result.Status != ack {
			t.Fatalf("statuses: %+v", v.Results)
		}
		if v.LifecycleEffects || v.ActionEffects || v.DispatchEffects || v.AutomaticCutover {
			t.Fatal("effects enabled")
		}
		for _, r := range v.Results {
			if len(r.Result.Actions) != 0 || len(r.Result.Work) != 0 {
				t.Fatal("operational output")
			}
		}
		if after := legacy(); after != before {
			t.Fatal("shadow changed legacy tables/views")
		}
		select {
		case <-changed:
			t.Fatal("shadow woke store listeners")
		default:
		}
	}
	observe("unmet", "unknown")
	must(s.StartRun(ctx, model.Run{ID: "report-run", TaskID: task.ID, Attempt: 1, StartedAt: now.Add(time.Second), Sandbox: "test"}, model.Limits{}))
	now = now.Add(2 * time.Second)
	observe("pending", "unknown")
	author := model.Attribution{Actor: model.GrainPrincipal, OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: task.ID}}
	_, err := s.AddComment(ctx, model.Comment{TaskID: task.ID, Author: author, Body: "The persisted report (private body)", CreatedAt: now})
	must(err)
	must(s.FinishRun(ctx, "report-run", now, "succeeded", ""))
	must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.CompletedAt = &now }))
	observe("met", "pending")
	v, d, err := s.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	first := v.Results[0].EvaluationID
	count := d.Evaluations
	// Restart/replay: a new store object shares accepted versions and coalesces.
	restarted := model.New(db)
	v, d, err = restarted.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	if v.Results[0].EvaluationID != first || d.Evaluations != count {
		t.Fatal("replay duplicated evaluation")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := s.ObserveTaskGates(ctx, task.ID, now); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(err)
	}
	_, d, err = s.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	if d.Evaluations != count {
		t.Fatal("read/event storm was not coalesced")
	}
	// Unknown or malformed persisted profiles cannot replay an accepted met.
	for _, mutate := range []func(*gates.ObservationView){
		func(v *gates.ObservationView) { v.Bindings[0].Type = "unregistered" },
		func(v *gates.ObservationView) { v.Bindings[0].Version = 99 },
		func(v *gates.ObservationView) {
			v.Bindings[0].Config = []byte(`{"product":"change","semantics":"current_v1"}`)
		},
	} {
		cached, _, e := s.ObserveTaskGates(ctx, task.ID, now)
		must(e)
		mutate(cached)
		raw, _ := json.Marshal(cached)
		_, e = db.ExecContext(ctx, "UPDATE `gate_observation_shadow` SET `view_json` = ? WHERE `task_id` = ?", string(raw), task.ID)
		must(e)
		unknown, ud, e := s.ObserveTaskGates(ctx, task.ID, now)
		must(e)
		if unknown.Results[0].Result.Status != "unknown" || ud.Counts["unknown_unsupported_config"] == 0 {
			t.Fatal("unsupported persisted config replayed met")
		}
		now = now.Add(2 * time.Minute)
		observe("met", "pending")
	}
	// A verified close is receipt; an automation close is never a receipt.
	closed := now.Add(time.Second)
	must(s.ObserveClose(ctx, task.ID, closed, human, "read", func(o *model.Observation) { o.ClosedAt = &closed }))
	now = closed
	observe("met", "met")
	_, d, err = s.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	if d.Counts["equivalent_milestones"] == 0 {
		t.Fatal("missing equivalent-milestone diagnostics")
	}
	// An attachment changes the report product even without a new comment.
	var reportComment int64
	must(db.QueryRowContext(ctx, "SELECT MAX(`id`) FROM `task_comment` WHERE `task_id` = ?", task.ID).Scan(&reportComment))
	attachmentID, e := s.AddAttachment(ctx, model.Attachment{TaskID: task.ID, CommentID: &reportComment, Filename: "evidence.txt", ContentType: "text/plain", Content: []byte("new evidence"), Size: 12, CreatedAt: now})
	must(e)
	observe("unknown", "unknown")
	incomplete, _, e := s.ObserveTaskGates(ctx, task.ID, now)
	must(e)
	if incomplete.Results[0].Result.Dependencies[0].Complete {
		t.Fatal("attachment collection falsely complete")
	}
	_, e = db.ExecContext(ctx, "DELETE FROM `task_attachment` WHERE `id` = ?", attachmentID)
	must(e)
	observe("met", "met")
	before, _ := s.GetObservation(ctx, task.ID)
	// A new report invalidates the old receipt, even while old results were met.
	now = now.Add(time.Second)
	_, err = s.AddComment(ctx, model.Comment{TaskID: task.ID, Author: author, Body: "Revised report", CreatedAt: now})
	must(err)
	must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) { o.CompletedAt = &now }))
	observe("met", "unknown")
	after, _ := s.GetObservation(ctx, task.ID)
	if !reflect.DeepEqual(before.ClosedAt, after.ClosedAt) {
		t.Fatal("shadow changed close")
	}
	// A new human message is a distinct source fence, not a receipt.
	v, _, err = s.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	source := v.SourceRevision
	_, err = s.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: human}, Body: "Please continue", CreatedAt: now})
	must(err)
	v, _, err = s.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	if v.SourceRevision == source {
		t.Fatal("message did not invalidate fence")
	}
	// Heartbeat-only legacy observation updates are coalesced.
	_, d, err = s.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	priorCount := d.Evaluations
	must(s.ObserveField(ctx, task.ID, now.Add(time.Millisecond), func(o *model.Observation) {}))
	_, d, err = s.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	if d.Evaluations != priorCount {
		t.Fatal("observation heartbeat flooded history")
	}
	// Concurrent message arrival cannot be lost by an older completing read.
	// Compare against the version after the earlier human message, so that
	// message cannot satisfy the assertion for this concurrent arrival.
	source = v.SourceRevision
	raceErrs := make(chan error, 9)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, e := s.ObserveTaskGates(ctx, task.ID, now); raceErrs <- e }()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, e := s.AddComment(ctx, model.Comment{TaskID: task.ID, Author: model.Attribution{Actor: human}, Body: "Evidence arrived during refresh", CreatedAt: now})
		raceErrs <- e
	}()
	wg.Wait()
	close(raceErrs)
	for e := range raceErrs {
		must(e)
	}
	v, _, err = s.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	if v.SourceRevision == source {
		t.Fatal("concurrent message fence lost")
	}
	// Error/work/action/wait diagnostics stay inert, are retained through
	// restart/backoff, and cannot contaminate authoritative result slots.
	failure := *v
	failure.Dirty = true
	failure.Results = append([]gates.EvaluationState(nil), v.Results...)
	for i := range failure.Results {
		r := &failure.Results[i]
		r.Disposition = "rejected"
		r.AcceptedAt = nil
		r.Result.Status = "error"
		r.Result.Error = &gates.EvaluationError{Code: "unavailable", Message: "safe test failure", Retryable: true}
		r.Result.Work = []struct{}{{}}
		r.Result.Actions = []struct{}{{}}
	}
	now = now.Add(time.Millisecond) // this failed collection starts after the accepted one
	beforeError := legacy()
	wake := s.Changed()
	must(s.RecordGateObservationFailure(ctx, task.ID, now, &failure))
	if legacy() != beforeError {
		t.Fatal("rejected diagnostic changed legacy progress")
	}
	select {
	case <-wake:
		t.Fatal("error diagnostic woke dispatch")
	default:
	}
	failed, fd, err := model.New(db).ObserveTaskGates(ctx, task.ID, now)
	must(err)
	if !failed.Dirty || fd.StartedAt.IsZero() || fd.Evaluations == 0 || fd.Counts["unknown_error"] == 0 || len(fd.History) == 0 {
		t.Fatalf("backoff lost diagnostics: %+v", fd)
	}
	for _, r := range failed.Results {
		if len(r.Result.Work) > 0 || len(r.Result.Actions) > 0 {
			t.Fatal("failure retained actionable outputs")
		}
		if r.RetryAt == nil || !r.RetryAt.After(now) {
			t.Fatal("missing persisted retry")
		}
	}
	now = now.Add(2 * time.Minute)
	v, _, err = s.ObserveTaskGates(ctx, task.ID, now)
	must(err)
	if v.Dirty {
		t.Fatal("retry did not recover")
	}
	task.Intent = model.IntentImplement
	must(s.PutTask(ctx, task))
	observe("unknown", "unknown")
	// Diagnostic expiry causes refresh, with no task transition or event.
	now = now.Add(2 * time.Minute)
	observe("unknown", "unknown")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	beforeState := legacy()
	_, _, err = s.ObserveTaskGates(canceled, task.ID, now)
	if err == nil {
		t.Fatal("canceled diagnostic succeeded")
	}
	if legacy() != beforeState {
		t.Fatal("error changed progress")
	}
	// Backoff restores the nominal delay before adding fresh jitter. Replaying
	// a persisted retry may never compound jitter beyond the 20% contract.
	now = now.Add(time.Millisecond)
	for step := 0; step < 8; step++ {
		failure.ComparedAt = now
		for i := range failure.Results {
			failure.Results[i].Result.ObservedAt = now
			failure.Results[i].Result.ValidUntil = now.Add(time.Minute)
		}
		must(s.RecordGateObservationFailure(ctx, task.ID, now, &failure))
		stored, _, e := model.New(db).ObserveTaskGates(ctx, task.ID, now)
		must(e)
		delay := stored.Results[0].RetryAt.Sub(now)
		nominal := min(time.Duration(1<<step)*time.Second, time.Minute)
		if delay < nominal || delay > nominal+nominal/5 {
			t.Fatalf("retry %d: %v outside [%v,%v]", step, delay, nominal, nominal+nominal/5)
		}
		now = *stored.Results[0].RetryAt
	}
	// Rotating the bounded cohort can evict a current cache while its
	// diagnostic history remains. Both accepted/error replay must continue
	// beyond retained generations instead of colliding with history keys.
	latest, priorDiag, e := s.ObserveTaskGates(ctx, task.ID, now)
	must(e)
	priorGeneration := latest.Results[0].Revision
	beforeEviction := legacy()
	unchanged := s.Changed()
	_, e = db.ExecContext(ctx, "DELETE FROM `gate_observation_shadow` WHERE `task_id` = ?", task.ID)
	must(e)
	restored, restoredDiag, e := model.New(db).ObserveTaskGates(ctx, task.ID, now)
	must(e)
	if restored.Results[0].Revision <= priorGeneration {
		t.Fatal("cold cache reused a retained generation")
	}
	if !restoredDiag.StartedAt.Equal(priorDiag.StartedAt) {
		t.Fatal("cold cache lost the retained start marker")
	}
	_, e = db.ExecContext(ctx, "DELETE FROM `gate_observation_shadow` WHERE `task_id` = ?", task.ID)
	must(e)
	failure.ComparedAt = now
	must(s.RecordGateObservationFailure(ctx, task.ID, now, &failure))
	failedAgain, failedDiag, e := model.New(db).ObserveTaskGates(ctx, task.ID, now)
	must(e)
	if !failedAgain.Dirty || failedAgain.Results[0].Revision <= restored.Results[0].Revision || failedDiag.Counts["unknown_error"] == 0 {
		t.Fatal("cold error replay lost retained history")
	}
	if legacy() != beforeEviction {
		t.Fatal("cold diagnostic replay changed legacy progress")
	}
	select {
	case <-unchanged:
		t.Fatal("cold diagnostic replay woke dispatch")
	default:
	}

}
