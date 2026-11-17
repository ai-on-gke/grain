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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunChecks qualifies acceptance of real scoped task/CI/policy changes on both
// engines, including replay through a new controller instance and legacy wakes.
func RunChecks(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP}
	task := model.Task{ID: "shadow-checks", Intent: model.IntentImplement, Title: "Repair CI", Target: &repo, Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#7"}}, Filing: model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect}, Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective, CreatedAt: &now}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutTask(ctx, task))
	subject := model.CheckSubjects(task)[0]
	f := gates.ChecksFacts{TaskID: task.ID, Subject: subject, Head: "H1", Mode: "github", PolicyVersion: gates.Digest("base-policy"), PolicyComplete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute), Sources: []gates.CheckSource{{Kind: "github", Head: "H1", Version: gates.Digest("set"), Complete: true, All: true, Attempts: []gates.CheckAttempt{{Identity: "app/go", Names: []string{"go"}, Head: "H1", Version: gates.Digest("run"), Order: 1, State: "completed", Conclusion: "success"}}}}}
	read := func(fence string, want string) (*gates.ObservationView, *model.GateObservationDiagnostics) {
		t.Helper()
		before := legacyChecksSnapshot(t, ctx, db)
		wake, commits := s.Changed(), s.CommitCount()
		v, d, err := s.ObserveTaskGatesWithChecks(ctx, task.ID, now, fence, []gates.ChecksFacts{f})
		must(err)
		if len(v.Results) != 4 || v.Results[2].Result.Status != want {
			t.Fatalf("checks want %s: %+v", want, v.Results)
		}
		if v.Authority != "legacy" || v.Mode != "observation_only" || v.LifecycleEffects || v.ActionEffects || v.DispatchEffects || v.AutomaticCutover {
			t.Fatal("authority changed")
		}
		for _, r := range v.Results {
			if len(r.Result.Actions) > 0 || len(r.Result.Work) > 0 {
				t.Fatal("effectful prediction")
			}
		}
		if before != legacyChecksSnapshot(t, ctx, db) || commits != s.CommitCount() {
			t.Fatal("legacy rows or commit accounting changed")
		}
		select {
		case <-wake:
			t.Fatal("observation signalled legacy Changed")
		default:
		}
		return v, d
	}
	fence := func() string { t.Helper(); _, _, v, err := s.GateChecksInput(ctx, task.ID); must(err); return v }
	v, d := read(fence(), "met")
	revision := v.Results[2].Revision
	_, again := read(fence(), "met")
	if again.Evaluations != d.Evaluations {
		t.Fatal("replay not coalesced")
	}
	s = model.New(db)
	replay, _ := read(fence(), "met")
	if replay.Results[2].Revision != revision {
		t.Fatal("restart lost accepted generation")
	}
	now = now.Add(time.Second)
	f.Sources[0].Attempts[0].Conclusion = "cancelled"
	f.Sources[0].Attempts[0].Version = gates.Digest("cancelled")
	read(fence(), "pending")
	now = now.Add(time.Second)
	f.Head = "H2"
	read(fence(), "unknown")
	f.Sources[0].Head = "H2"
	f.Sources[0].Attempts[0].Head = "H2"
	f.Sources[0].Attempts[0].Conclusion = "success"
	f.Sources[0].Version = gates.Digest("new head")
	read(fence(), "met")
	now = now.Add(time.Second)
	oldFence := fence()
	must(s.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "new", Commit: "H2", Conclusion: "cancelled"}
	}))
	read(oldFence, "unknown")
	read(fence(), "met")
	now = now.Add(time.Second)
	oldFence = fence()
	must(s.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeNone}))
	read(oldFence, "unknown")
	f.Sources[0].Complete = false
	read(fence(), "unknown")
	f.Sources[0].Complete = true
	f.ValidUntil = now
	read(fence(), "unknown")
	f.ValidUntil = now.Add(time.Minute)
	now = now.Add(time.Second)
	f.PolicyVersion = gates.Digest("new base-owned definitions")
	read(fence(), "met")
	// Errors are reconstructed from the registry and current task scope, ignoring
	// forged accepted evidence/predictions. This also survives a fresh Store.
	before := legacyChecksSnapshot(t, ctx, db)
	wake := s.Changed()
	fail := gates.UnavailableObservation(task.ID, now)
	now = now.Add(time.Second)
	must(s.RecordGateObservationFailure(ctx, task.ID, now, &fail))
	if len(fail.Results) != 4 || fail.Results[2].Result.Status != "error" || len(fail.Results[2].Result.Evidence) > 0 {
		t.Fatal("failure lost scoped CI or retained accepted evidence")
	}
	if before != legacyChecksSnapshot(t, ctx, db) {
		t.Fatal("failure mutated legacy state")
	}
	select {
	case <-wake:
		t.Fatal("failure signalled Changed")
	default:
	}
	s = model.New(db)
	replay, _, err := s.ObserveTaskGatesWithChecks(ctx, task.ID, now, fence(), []gates.ChecksFacts{f})
	must(err)
	if !replay.Dirty || replay.Results[2].Result.Status != "error" {
		t.Fatal("backoff did not survive restart")
	}
	// Reject duplicate or foreign resources even when cardinality appears right.
	foreign := f
	foreign.Subject.ID = "999"
	if _, _, err = s.ObserveTaskGatesWithChecks(ctx, task.ID, now.Add(time.Minute), fence(), []gates.ChecksFacts{foreign}); err == nil {
		t.Fatal("foreign check subject accepted")
	}
}

func legacyChecksSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	all := []any{}
	for _, table := range []string{"task", "task_observation", "task_run", "task_run_tool", "task_comment", "task_attachment", "task_goal", "task_state", "task_ready", "task_pause", "task_link", "task_grant", "task_grant_wait", "task_event", "integration_event", "merge_batch"} {
		rows, err := db.QueryContext(ctx, "SELECT * FROM `"+table+"`")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		records := [][]string{}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			record := []string{}
			for _, v := range values {
				record = append(record, fmt.Sprint(v))
			}
			records = append(records, record)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		all = append(all, records)
	}
	b, _ := json.Marshal(all)
	return string(b)
}
