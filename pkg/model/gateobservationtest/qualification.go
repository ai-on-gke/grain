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
	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
	"os"
	"strings"
	"testing"
	"time"
)

// RunQualifications qualifies acceptance fences, durable replay, failures and
// the absence of legacy writes/wakes on both real database engines.
func RunQualifications(t *testing.T, s *model.Store, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := model.RepoRef{Owner: "acme", Name: "grain"}
	id := "qualification-storage"
	must(s.PutTask(ctx, model.Task{ID: id, Title: "Observe qualifications", Intent: model.IntentImplement, Target: &repo}))
	yes := true
	f := gates.StageFacts{TaskID: id, Head: strings.Repeat("a", 40), FlowRevision: gates.Digest("flow"), PolicyVersion: gates.Digest("policy"), AssignmentVersion: gates.Digest("assignment"), Engine: 4, Arm: "treatment", Supported: true, Complete: true, S4Required: &yes, Checks: []gates.StageCheck{{Stage: "S3", ID: "integrity", Severity: "blocking"}}, ObservedAt: now, ValidUntil: now.Add(time.Minute)}
	f.Runs = []gates.StageRun{{ID: "1", Stage: "S3", CheckID: "integrity", Head: f.Head, FlowRevision: f.FlowRevision, Engine: f.Engine, Arm: f.Arm, Verdict: "fail", Version: gates.Digest("run")}}
	c := gates.QualificationCollection{QA: &f}
	c.StoreFence, _ = s.QualificationObservationFence(ctx, id)
	legacy := func() string {
		t.Helper()
		var all []any
		for _, table := range []string{"task", "task_observation", "task_run", "task_run_tool", "task_comment", "task_attachment", "task_goal", "task_ready", "task_state", "task_link", "task_grant", "task_grant_wait", "task_event", "integration_event", "task_qa_assignment", "task_qa_check_run", "task_qa_finding", "qa_enrollment", "qa_experiment", "release_approval"} {
			rows, err := db.QueryContext(ctx, "SELECT * FROM "+fmt.Sprintf("\"%s\"", table))
			must(err)
			cols, err := rows.Columns()
			must(err)
			var records [][]string
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(vals))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				must(rows.Scan(ptrs...))
				record := make([]string, len(vals))
				for i, v := range vals {
					record[i] = fmt.Sprint(v)
				}
				records = append(records, record)
			}
			must(rows.Err())
			_ = rows.Close()
			all = append(all, records)
		}
		return gates.Digest(all)
	}
	read := func(store *model.Store, at time.Time, collection gates.QualificationCollection) *gates.ObservationView {
		t.Helper()
		before := legacy()
		changed := store.Changed()
		view, _, err := store.ObserveTaskGatesWithQualifications(ctx, id, at, collection)
		must(err)
		if legacy() != before {
			t.Fatal("diagnostic mutated legacy data")
		}
		select {
		case <-changed:
			t.Fatal("diagnostic woke legacy scheduling")
		default:
		}
		for _, entry := range view.Results {
			if len(entry.Result.Work) > 0 || len(entry.Result.Actions) > 0 {
				t.Fatal("diagnostic supplied effects")
			}
		}
		return view
	}
	qaIndex := func(v *gates.ObservationView) int {
		t.Helper()
		for i, b := range v.Bindings {
			if b.ID == "qa-s3" {
				return i
			}
		}
		t.Fatal("missing S3 monitor")
		return 0
	}
	qa := func(v *gates.ObservationView) gates.EvaluationState { return v.Results[qaIndex(v)] }
	first := read(s, now, c)
	if path := os.Getenv("GRAIN_QUALIFICATION_OBSERVATION_FIXTURE"); path != "" {
		data, err := json.MarshalIndent(first, "", "  ")
		must(err)
		must(os.WriteFile(path, data, 0600))
	}
	if len(first.Results) < 5 || qa(first).Result.Status != "unmet" {
		t.Fatalf("missing stage prediction %+v", first)
	}
	replayed := read(model.New(db), now.Add(time.Second), c)
	if qa(replayed).Revision != qa(first).Revision {
		t.Fatal("restart or heartbeat generated a source event")
	}
	next := *c.QA
	next.Head = strings.Repeat("b", 40)
	c.QA = &next
	second := read(s, now.Add(2*time.Second), c)
	if qa(second).Result.Status != "unknown" || qa(second).Revision <= qa(first).Revision {
		t.Fatal("old-head stage survived source event")
	}
	old := gates.QualificationCollection{StoreFence: c.StoreFence, QA: &f}
	older := read(s, now.Add(time.Second), old)
	if older.SourceRevision != second.SourceRevision {
		t.Fatal("slow old success rewound accepted source")
	}
	must(s.UpdateTask(ctx, id, func(task *model.Task) error { task.QAFlowRef = "another-flow"; return nil }))
	stale := read(s, now.Add(3*time.Second), c)
	if qa(stale).Result.Status != "unknown" || qa(stale).Result.Dependencies[0].Complete {
		t.Fatal("changed task/policy accepted old source")
	}
	c.StoreFence, _ = s.QualificationObservationFence(ctx, id)
	c.QA = &f
	accepted := read(s, now.Add(4*time.Second), c)
	before := legacy()
	changed := s.Changed()
	errorAt := now.Add(5 * time.Second)
	unavailable := gates.UnavailableQualifications(id, errorAt, c)
	must(s.RecordGateQualificationFailure(ctx, id, errorAt, &unavailable, c))
	if legacy() != before {
		t.Fatal("failure mutated legacy")
	}
	select {
	case <-changed:
		t.Fatal("failure woke legacy")
	default:
	}
	if len(unavailable.Results) != len(accepted.Results) {
		t.Fatal("failure dropped QA shape")
	}
	for _, r := range unavailable.Results {
		if r.Disposition != "rejected" || len(r.Result.Evidence) != 0 || len(r.Result.Waits) != 0 || len(r.Result.Actions) != 0 {
			t.Fatal("failure reused accepted evidence/effects")
		}
	}
	rejected := read(model.New(db), errorAt.Add(time.Millisecond), c)
	if !rejected.Dirty || qa(rejected).Result.Status != "error" {
		t.Fatal("restart lost rejected backoff")
	}
	recovered := read(model.New(db), now.Add(8*time.Second), c)
	if recovered.Dirty || qa(recovered).Result.Status != "unmet" {
		t.Fatal("did not recover")
	}
	late := gates.UnavailableQualifications(id, errorAt, c)
	must(s.RecordGateQualificationFailure(ctx, id, errorAt, &late, c))
	if late.SourceRevision != recovered.SourceRevision {
		t.Fatal("late failure replaced recovery")
	}
	if qa(accepted).Revision >= qa(recovered).Revision {
		t.Fatal("recovery revision rewound")
	}

	// A verified person proof expires inside the ordinary one-minute cache.
	// Refreshing it updates diagnostics/history only, never an operational wait.
	release := gates.ReleaseFacts{TaskID: id, Repository: repo.String(), Candidate: "v3.1.0-rc.1", Head: f.Head, Version: "v3.1.0", PolicyVersion: gates.Digest("release-policy"), SourceVersion: gates.Digest("release-source"), Complete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute)}
	release.Person = &gates.ReleasePerson{Identity: "alice", Kind: "human", Verified: true, Decision: "approve", Commit: f.Head, Version: release.Version, IssuedAt: now, ExpiresAt: now.Add(20 * time.Second)}
	c.Release = &release
	granted := read(s, now.Add(10*time.Second), c)
	var personIndex int
	for i, b := range granted.Bindings {
		if b.ID == "release-person" {
			personIndex = i
		}
	}
	if granted.Results[personIndex].Result.Status != "met" || !granted.Results[personIndex].Result.ValidUntil.Equal(release.Person.ExpiresAt) {
		t.Fatal("person proof outlived its receipt")
	}
	expired := read(model.New(db), now.Add(21*time.Second), c)
	if expired.Results[personIndex].Result.Status != "unmet" || expired.Results[personIndex].Revision <= granted.Results[personIndex].Revision {
		t.Fatal("cached person proof survived expiry")
	}
	// A supported but mismatched cached stage config is rejected, rather than
	// silently reusing the prediction for a different stage/profile.
	corrupt := *expired
	corrupt.Bindings = append([]gates.Binding{}, expired.Bindings...)
	corrupt.Bindings[qaIndex(&corrupt)].Config = json.RawMessage("{\"stage\":\"s4\",\"flow_revision\":\"flow\",\"assignment\":\"treatment\"}")
	raw, err := json.Marshal(corrupt)
	must(err)
	_, err = db.ExecContext(ctx, "UPDATE `gate_observation_shadow` SET `view_json` = ? WHERE `task_id` = ?", string(raw), id)
	must(err)
	rebuilt := read(s, now.Add(22*time.Second), c)
	if qa(rebuilt).Result.Status != "unknown" || !strings.Contains(qa(rebuilt).Result.Summary, "regenerated") {
		t.Fatal("mismatched cache profile was reused")
	}
	must(s.UpdateTask(ctx, id, func(task *model.Task) error {
		task.Links = []model.Link{{Kind: model.LinkFixes, Target: repo.String() + "#77"}}
		for i := 0; i < 40; i++ {
			task.Links = append(task.Links, model.Link{Kind: model.LinkReviewTask, Target: fmt.Sprintf("review-scope-%d", i)})
		}
		return nil
	}))
	c.StoreFence, _ = s.QualificationObservationFence(ctx, id)
	combined := read(s, now.Add(23*time.Second), c)
	qaCount, releaseCount, reviewCount := 0, 0, 0
	for _, b := range combined.Bindings {
		switch b.Type {
		case "qa_stage":
			qaCount++
		case "release_qualification":
			releaseCount++
		case "review":
			reviewCount++
		}
	}
	if len(combined.Bindings) > 64 || qaCount != 3 || releaseCount != 8 || reviewCount == 0 {
		t.Fatal("combined projection lost monitor families or exceeded its bound")
	}
	// The CI port can contribute two profiles for each of eight changes.
	// Preserve all typed families when review scope collection also overflows.
	must(s.UpdateTask(ctx, id, func(task *model.Task) error {
		for i := 78; i < 85; i++ {
			task.Links = append(task.Links, model.Link{Kind: model.LinkExternalPR, Target: fmt.Sprintf("%s#%d", repo.String(), i)})
		}
		return nil
	}))
	c.StoreFence, err = s.QualificationObservationFence(ctx, id)
	must(err)
	currentTask, _, checkFence, err := s.GateChecksInput(ctx, id)
	must(err)
	checks := []gates.ChecksFacts{}
	for _, subject := range model.CheckSubjects(*currentTask) {
		checks = append(checks, gates.ChecksFacts{TaskID: id, Subject: subject, ObservedAt: now, ValidUntil: now.Add(time.Minute)})
	}
	children := gates.ChildrenConfig{Tasks: []string{"missing-selected-child"}, Predicate: "success"}
	beforeCombined, wakeCombined := legacy(), s.Changed()
	all, _, err := s.ObserveTaskGatesWithEvidence(ctx, id, now.Add(24*time.Second), checkFence, checks, c, children)
	must(err)
	counts := map[string]int{}
	for _, b := range all.Bindings {
		counts[b.Type]++
	}
	if len(all.Bindings) > 64 || counts["checks"] != 16 || counts["qa_stage"] != 3 || counts["release_qualification"] != 8 || counts["review"] == 0 || counts["children"] != 1 || counts["delivery"] < 5 || counts["integration"] == 0 {
		t.Fatalf("combined CI/outcome/QA/release/review families lost or unbounded: %v (%d bindings)", counts, len(all.Bindings))
	}
	if legacy() != beforeCombined {
		t.Fatal("combined observations changed legacy rows")
	}
	select {
	case <-wakeCombined:
		t.Fatal("combined observations woke legacy scheduling")
	default:
	}
	restart, _, err := model.New(db).ObserveTaskGatesWithEvidence(ctx, id, now.Add(25*time.Second), checkFence, checks, c, children)
	must(err)
	if restart.Results[0].Revision != all.Results[0].Revision {
		t.Fatal("combined restart generated an event")
	}
	failed := gates.UnavailableObservationForBindings(id, now.Add(26*time.Second), all.Bindings)
	must(s.RecordGateQualificationFailure(ctx, id, now.Add(26*time.Second), &failed, c))
	backoff, _, err := model.New(db).GateObservationRetry(ctx, id, now.Add(26*time.Second), false)
	must(err)
	if backoff == nil || !backoff.Dirty || len(backoff.Bindings) != len(all.Bindings) {
		t.Fatal("combined failure lost bounded source families after restart")
	}
	for _, r := range backoff.Results {
		if len(r.Result.Evidence) != 0 || len(r.Result.Actions) != 0 || len(r.Result.Work) != 0 {
			t.Fatal("combined failure retained accepted evidence or effects")
		}
	}
	for i := 0; i < 513; i++ {
		_, err := s.RecordQACheckRun(ctx, model.QACheckRun{TaskID: id, CommitSHA: f.Head, Stage: "S3", CheckID: "check", Verdict: "pass"}, nil)
		must(err)
	}
	runs, _, complete, err := s.QAGateEvidence(ctx, id, f.Head)
	must(err)
	if complete || len(runs) != 512 {
		t.Fatal("partial collection claimed complete")
	}
	runs, _, complete, err = s.QAGateEvidence(ctx, "foreign", f.Head)
	must(err)
	if !complete || len(runs) != 0 {
		t.Fatal("foreign task leaked records")
	}
	rows, err := db.QueryContext(ctx, "SELECT \"view_json\" FROM \"gate_observation_shadow_history\" WHERE \"task_id\" = ?", id)
	must(err)
	defer rows.Close()
	for rows.Next() {
		var raw string
		must(rows.Scan(&raw))
		if len(raw) > 512*1024 {
			t.Fatal("unexpectedly unbounded combined persisted view")
		}
		var view gates.ObservationView
		must(json.Unmarshal([]byte(raw), &view))
	}
	must(rows.Err())
}
