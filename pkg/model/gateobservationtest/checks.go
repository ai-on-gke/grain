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
		known, e := s.RecordedGateChecks(ctx, repo, f.Head)
		must(e)
		f.RecordedChecksVersion = known.Version
		before := legacyChecksSnapshot(t, ctx, db)
		wake, commits := s.Changed(), s.CommitCount()
		v, d, err := s.ObserveTaskGatesWithChecks(ctx, task.ID, now, fence, []gates.ChecksFacts{f})
		must(err)
		assertShadowView(t, v)
		if len(v.Results) < 4 || v.Results[2].Result.Status != want {
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

	// A late remote collection from an older request must not rewind an accepted
	// same-head pending attempt to the old passing result (including after restart).
	// Both starts are within a single Postgres timestamp microsecond.
	now = now.Add(time.Second + 900*time.Nanosecond)
	f.Sources[0].Attempts[0].Conclusion = "cancelled"
	newest, nd := read(fence(), "pending")
	stale := f
	stale.Sources = append([]gates.CheckSource(nil), f.Sources...)
	stale.Sources[0].Attempts = append([]gates.CheckAttempt(nil), f.Sources[0].Attempts...)
	stale.Sources[0].Attempts[0].Conclusion = "success"
	beforeLate := legacyChecksSnapshot(t, ctx, db)
	lateWake, lateCommits := s.Changed(), s.CommitCount()
	late, ld, e := model.New(db).ObserveTaskGatesWithChecks(ctx, task.ID, now.Add(-100*time.Nanosecond), fence(), []gates.ChecksFacts{stale})
	must(e)
	if late.Results[2].Result.Status != "pending" || late.Results[2].Revision != newest.Results[2].Revision || ld.Evaluations != nd.Evaluations {
		t.Fatal("older remote CI collection rewound newer accepted evidence")
	}
	lateFailure := gates.UnavailableObservation(task.ID, now.Add(-100*time.Nanosecond))
	must(model.New(db).RecordGateObservationFailure(ctx, task.ID, now.Add(-100*time.Nanosecond), &lateFailure))
	if lateFailure.Results[2].Result.Status != "pending" || lateFailure.Results[2].Revision != newest.Results[2].Revision {
		t.Fatal("older failed collection rewound newer evidence within one timestamp microsecond")
	}
	if beforeLate != legacyChecksSnapshot(t, ctx, db) || lateCommits != s.CommitCount() {
		t.Fatal("late collection changed legacy state")
	}
	select {
	case <-lateWake:
		t.Fatal("late collection signalled Changed")
	default:
	}
	now = now.Add(time.Second)
	f.Sources[0].Attempts[0].Conclusion = "success"
	read(fence(), "met")
	// A webhook arriving during an older external read invalidates even a
	// newer cached success. Neither accepting the gap nor recovering may emit
	// another source event or wake legacy scheduling.
	collected := f
	collectedFence := fence()
	must(s.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: repo.String(), CheckRunID: 91, Commit: f.Head, App: "app", JobName: "go", Conclusion: "cancelled", RunAttempt: 2}))
	beforeEvent := legacyChecksSnapshot(t, ctx, db)
	eventWake, eventCommits := s.Changed(), s.CommitCount()
	gap, _, e := model.New(db).ObserveTaskGatesWithChecks(ctx, task.ID, now.Add(-time.Second), collectedFence, []gates.ChecksFacts{collected})
	must(e)
	if gap.Results[2].Result.Status != "unknown" || gap.ComparedAt.Before(newest.ComparedAt) {
		t.Fatal("CI event during an older collection did not invalidate cached success")
	}
	if beforeEvent != legacyChecksSnapshot(t, ctx, db) || eventCommits != s.CommitCount() {
		t.Fatal("CI fence acceptance mutated legacy state")
	}
	select {
	case <-eventWake:
		t.Fatal("CI fence acceptance signalled Changed")
	default:
	}
	known, e := s.RecordedGateChecks(ctx, repo, f.Head)
	must(e)
	// Other heads/repositories and log enrichment are outside this source fence.
	must(s.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: repo.String(), CheckRunID: 92, Commit: "foreign-head", Conclusion: "failure"}))
	must(s.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: "other/repo", CheckRunID: 91, Commit: f.Head, Conclusion: "failure"}))
	must(s.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: repo.String(), CheckRunID: 91, Output: "log enrichment", FailureTail: "bounded tail", TailFetched: true}))
	enriched, e := s.RecordedGateChecks(ctx, repo, f.Head)
	must(e)
	if known.Version != enriched.Version || len(enriched.Checks) != 1 {
		t.Fatal("unrelated CI records or logs changed the source fence")
	}
	now = now.Add(time.Second)
	read(fence(), "met")
	// Errors are reconstructed from the registry and current task scope, ignoring
	// forged accepted evidence/predictions. This also survives a fresh Store.
	before := legacyChecksSnapshot(t, ctx, db)
	wake := s.Changed()
	fail := gates.UnavailableObservation(task.ID, now)
	now = now.Add(time.Second)
	must(s.RecordGateObservationFailure(ctx, task.ID, now, &fail))
	if len(fail.Results) < 4 || fail.Results[2].Result.Status != "error" || len(fail.Results[2].Result.Evidence) > 0 {
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
	// CI and the already merged review observers share the canonical wire
	// bound. An incomplete review subset must not suppress or acquire CI proof.
	must(s.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		for i := range 45 {
			t.Links = append(t.Links, model.Link{Kind: model.LinkReviewTask, Target: fmt.Sprintf("missing-review-%d", i)})
		}
		return nil
	}))
	now = now.Add(2 * time.Minute)
	f.ObservedAt, f.ValidUntil = now, now.Add(time.Minute)
	combined, diagnostics := read(fence(), "met")
	if len(combined.Bindings) != 64 {
		t.Fatal("combined observer did not honor the canonical 64-binding bound")
	}
	for i, b := range combined.Bindings {
		if b.Type == "review" && !b.Origin.Imported && combined.Results[i].Result.Status != "unknown" {
			t.Fatal("truncated review collection passed")
		}
	}
	if diagnostics.Counts["checks_met"] < 1 || diagnostics.Counts["review_approval_incomplete"] != 1 || diagnostics.Counts["outcome_evidence_gap"] < 1 || diagnostics.History[0].Comparison != "outcome_evidence_gap;checks_met;review_approval_incomplete" {
		t.Fatalf("combined diagnostic lost incomplete review context: counts=%v latest=%s", diagnostics.Counts, diagnostics.History[0].Comparison)
	}
	for i := int64(1); i <= 512; i++ {
		must(s.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: repo.String(), CheckRunID: 1000 + i, Commit: "bounded", Conclusion: "success"}))
	}
	combinedTask := task
	combinedTask.ID = "shadow-checks-outcomes"
	combinedTask.Links = []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#8"}}
	must(s.PutTask(ctx, combinedTask))
	child := model.Task{ID: "shadow-checks-child", Intent: model.IntentAnalyze, Title: "Explicit child", Target: &repo}
	must(s.PutTask(ctx, child))
	must(s.ObserveClose(ctx, child.ID, now, model.GrainPrincipal, "closed", func(o *model.Observation) { o.ClosedAt = &now }))
	fresh, err := s.GetTask(ctx, combinedTask.ID)
	must(err)
	s.RecordGateOutcomeSource(ctx, fresh.ID, model.GateOutcomeSource{Resource: "acme/widgets#8", Head: "combined-head", State: "merged", Policy: model.GateOutcomePolicy(*fresh), Attribution: "external-test", At: now})
	cf := gates.ChecksFacts{TaskID: fresh.ID, Subject: model.CheckSubjects(*fresh)[0], Head: "combined-head", Mode: "github", PolicyVersion: gates.Digest("combined-policy"), PolicyComplete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute), Sources: []gates.CheckSource{{Kind: "github", Head: "combined-head", Version: gates.Digest("combined-set"), Complete: true, All: true, Attempts: []gates.CheckAttempt{{Identity: "app/go", Names: []string{"go"}, Head: "combined-head", Version: gates.Digest("combined-run"), Order: 1, State: "completed", Conclusion: "success"}}}}}
	known, err = s.RecordedGateChecks(ctx, repo, cf.Head)
	must(err)
	cf.RecordedChecksVersion = known.Version
	_, _, combinedFence, err := s.GateChecksInput(ctx, fresh.ID)
	must(err)
	combinedBefore := legacyChecksSnapshot(t, ctx, db)
	combinedWake, combinedCommits := s.Changed(), s.CommitCount()
	cv, cd, err := s.ObserveTaskGatesWithChecks(ctx, fresh.ID, now, combinedFence, []gates.ChecksFacts{cf}, gates.ChildrenConfig{Tasks: []string{child.ID}, Predicate: "closed"})
	must(err)
	if path := os.Getenv("GRAIN_TEST_OUTCOME_FIXTURE"); path != "" {
		raw, err := json.Marshal(cv)
		must(err)
		must(os.WriteFile(path+".combined", raw, 0600))
	}
	types := map[string]bool{}
	foundIntegration, foundChild := false, false
	for i, b := range cv.Bindings {
		types[b.Type] = true
		r := cv.Results[i].Result
		if b.Type == "integration" {
			foundIntegration = true
			if r.Status != "met" {
				t.Fatal("CI obscured positive integration")
			}
		}
		if b.Type == "children" {
			foundChild = true
			if r.Status != "met" {
				t.Fatal("CI lost explicit child selection")
			}
		}
		if (b.Type == "approval" || b.Type == "review" && !b.Origin.Imported) && r.Status != "unknown" {
			t.Fatal("CI fabricated current review or approval proof")
		}
		if b.Required || b.Action != nil || len(r.Work) != 0 || len(r.Actions) != 0 {
			t.Fatal("combined monitors acquired authority")
		}
	}
	if len(types) != 7 || !foundIntegration || !foundChild || cv.Results[2].Result.Status != "met" {
		t.Fatal("combined projection lost a gate family")
	}
	if cd.Counts["checks_met"] != 1 || cd.Counts["outcome_discrepancy"] != 1 || cd.Counts["review_approval_provenance_gap"] != 1 {
		t.Fatal("combined comparison hid an independent family")
	}
	if legacyChecksSnapshot(t, ctx, db) != combinedBefore || s.CommitCount() != combinedCommits {
		t.Fatal("combined observation mutated legacy state")
	}
	select {
	case <-combinedWake:
		t.Fatal("combined observation woke legacy listeners")
	default:
	}
	if err := s.RecordGateObservationFailure(ctx, fresh.ID, now.Add(time.Millisecond), cv); err != nil {
		t.Fatal(err)
	}
	if len(cv.Bindings) != len(cv.Results) || len(cv.Bindings) < 7 {
		t.Fatal("combined failure projection was truncated")
	}
	for _, entry := range cv.Results {
		if entry.Result.Status != "error" || len(entry.Result.Evidence) != 0 || len(entry.Result.Actions) != 0 || len(entry.Result.Work) != 0 {
			t.Fatal("combined rejection retained proof or actions")
		}
	}
	// Large historical diagnostics must not consume the local collection budget.
	// Retention/counts remain complete, multiple complete samples fit within the
	// 512 KiB read budget, and no individual view is trimmed.
	historyView := *cv
	historyView.Results = append([]gates.EvaluationState(nil), cv.Results...)
	largeGuidance := strings.Repeat("h", 8192)
	var historyRaw []byte
	for i := range historyView.Results {
		historyView.Results[i].Result.HumanText = &largeGuidance
		historyView.Results[i].Result.AgentText = &largeGuidance
		historyRaw, err = json.Marshal(historyView)
		must(err)
		if len(historyRaw) >= 128*1024 {
			break
		}
	}
	if len(historyRaw) <= model.MaxGateObservationHistoryBytes/6 || len(historyRaw) > model.MaxGateObservationViewBytes {
		t.Fatalf("unexpected test history view size: %d", len(historyRaw))
	}
	for generation := 100; generation < 106; generation++ {
		_, err := db.ExecContext(ctx, "INSERT INTO `gate_observation_shadow_history` (`task_id`,`generation`,`at`,`view_json`,`comparison`) VALUES (?,?,?,?,?)", fresh.ID, generation, now, string(historyRaw), "history-byte-budget")
		must(err)
	}
	_, limited, err := s.ObserveTaskGates(ctx, fresh.ID, now.Add(2*time.Millisecond))
	must(err)
	wantSamples := model.MaxGateObservationHistoryBytes / len(historyRaw)
	if wantSamples < 2 || wantSamples >= 6 {
		t.Fatalf("expected byte budget to admit 2..5 samples of %d bytes, got %d", len(historyRaw), wantSamples)
	}
	if limited.Counts["history-byte-budget"] != 6 || len(limited.History) != wantSamples || limited.History[0].Generation != 105 {
		t.Fatalf("history load bound lost counts or expected samples: counts=%+v len=%d want=%d", limited.Counts, len(limited.History), wantSamples)
	}
	loaded, err := json.Marshal(limited.History[0].View)
	must(err)
	if string(loaded) != string(historyRaw) {
		t.Fatal("historical evidence was truncated instead of returning a complete sample")
	}
	bounded, e := s.RecordedGateChecks(ctx, repo, "bounded")
	must(e)
	if len(bounded.Checks) != 512 {
		t.Fatal("complete recorded collection lost checks")
	}
	must(s.PutGitHubCIJob(ctx, model.GitHubCIJob{Repo: repo.String(), CheckRunID: 2000, Commit: "bounded", Conclusion: "failure"}))
	if _, e = s.RecordedGateChecks(ctx, repo, "bounded"); e == nil {
		t.Fatal("oversized recorded set silently truncated")
	}
}

func legacyChecksSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	all := []any{}
	for _, table := range []string{"task", "task_observation", "task_run", "task_run_tool", "task_comment", "task_attachment", "task_goal", "task_state", "task_ready", "task_pause", "task_link", "task_grant", "task_grant_wait", "task_event", "integration_event", "merge_batch", "github_ci_job", "task_qa_assignment", "task_qa_check_run", "task_qa_finding", "qa_enrollment", "qa_experiment", "release_approval"} {
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
