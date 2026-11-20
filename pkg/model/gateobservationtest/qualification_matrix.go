// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunShadowQualification crosses evaluator states with existing lifecycle
// controls. All mutations below are explicit legacy fixture writes, before the
// snapshot. Neither successful predictions nor retry/expiry may act on them.
func RunShadowQualification(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "qualifier", Source: model.SourceIAP}
	repo := model.RepoRef{Owner: "acme", Name: "qualification"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, lifecycle := range []string{"queued", "running", "question", "stopped", "paused", "closed", "standing"} {
		t.Run(lifecycle, func(t *testing.T) {
			id := "qualification-" + lifecycle
			task := model.Task{ID: id, Intent: model.IntentImplement, Title: id, Target: &repo,
				Filing:   model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
				Approval: &model.Attribution{Actor: human}, Binding: model.BindingDirective, CreatedAt: &base,
				Links:       []model.Link{{Kind: model.LinkFixes, Target: repo.String() + "#7"}},
				KeepRunning: lifecycle == "standing"}
			must(s.PutTask(ctx, task))
			if lifecycle == "running" {
				must(s.StartRun(ctx, model.Run{ID: id + "-run", TaskID: id, Attempt: 1, StartedAt: base, Sandbox: "isolated"}, model.Limits{}))
			}
			if lifecycle == "question" || lifecycle == "stopped" {
				body := "Choose the release scope"
				if lifecycle == "stopped" {
					body = model.TurnStoppedComment
				}
				q, err := s.AddComment(ctx, model.Comment{TaskID: id, Author: model.Attribution{Actor: human}, Body: body, CreatedAt: base})
				must(err)
				must(s.ObserveField(ctx, id, base, func(o *model.Observation) { o.PendingQuestionCommentID = &q }))
			}
			if lifecycle == "paused" {
				_, err := s.Pause(ctx, model.TaskPause{TaskID: id, PausedAt: base, PausedBy: human, Immediate: true})
				must(err)
			}
			if lifecycle == "closed" {
				must(s.ObserveClose(ctx, id, base, human, "explicit legacy close", func(o *model.Observation) { o.ClosedAt = &base }))
			}
			// Keep failure accounting, queue intent and real wait descriptions populated.
			must(s.ObserveField(ctx, id, base, func(o *model.Observation) {
				o.MergeQueueState = "waiting_ci"
				o.MergeQueueNote = "authoritative legacy wait"
			}))
			for i, want := range []string{"met", "unmet", "pending", "unknown", "error"} {
				now := base.Add(time.Duration(i+1) * time.Second)
				known, err := s.RecordedGateChecks(ctx, repo, "H1")
				must(err)
				f := gates.ChecksFacts{TaskID: id, Subject: model.CheckSubjects(task)[0], Head: "H1", Mode: "github",
					PolicyVersion: gates.Digest("base-owned-policy"), PolicyComplete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute), RecordedChecksVersion: known.Version,
					Sources: []gates.CheckSource{{Kind: "github", Head: "H1", Version: gates.Digest(want), Complete: true, All: true,
						Attempts: []gates.CheckAttempt{{Identity: "trusted/test", Names: []string{"test"}, Head: "H1", Version: gates.Digest(want), Order: 1, State: "completed", Conclusion: "success"}}}}}
				switch want {
				case "unmet":
					f.Sources[0].Attempts[0].Conclusion = "failure"
				case "pending":
					f.Sources[0].Attempts[0].State = "in_progress"
					f.Sources[0].Attempts[0].Conclusion = ""
				case "unknown":
					f.Sources[0].Complete = false
				case "error":
					f.Sources[0].Error = true
				}
				_, _, fence, err := s.GateChecksInput(ctx, id)
				must(err)
				before := legacyChecksSnapshot(t, ctx, db)
				wake, commits := s.Changed(), s.CommitCount()
				view, diag, err := s.ObserveTaskGatesWithChecks(ctx, id, now, fence, []gates.ChecksFacts{f})
				must(err)
				assertShadowView(t, view)
				found := false
				for n, b := range view.Bindings {
					if b.Type == "checks" && !b.Origin.Imported {
						found = true
						if view.Results[n].Result.Status != want {
							t.Fatalf("%s: got %s want %s", lifecycle, view.Results[n].Result.Status, want)
						}
					}
				}
				if !found {
					t.Fatal("current check monitor absent")
				}
				assertUnchanged := func() {
					t.Helper()
					if before != legacyChecksSnapshot(t, ctx, db) || commits != s.CommitCount() {
						t.Fatal("shadow path changed legacy state or failure/commit accounting")
					}
					select {
					case <-wake:
						t.Fatal("shadow path woke dispatch listeners")
					default:
					}
				}
				assertUnchanged()
				// A restart and repeated reads must retain accepted versions and history,
				// including error backoff; they must not create work for queued/live tasks.
				restarted := model.New(db)
				restartedWake, restartedCommits := restarted.Changed(), restarted.CommitCount()
				again, againDiag, err := restarted.ObserveTaskGatesWithChecks(ctx, id, now, fence, []gates.ChecksFacts{f})
				must(err)
				assertShadowView(t, again)
				if restarted.CommitCount() != restartedCommits {
					t.Fatal("restart replay changed commit accounting")
				}
				select {
				case <-restartedWake:
					t.Fatal("restart replay woke dispatch")
				default:
				}
				if again.SourceRevision != view.SourceRevision || againDiag.Evaluations != diag.Evaluations {
					t.Fatal("restart duplicated the evaluation")
				}
				assertUnchanged()
			}
			// Synthetic clock advancement exercises the release criterion, not a claim
			// of a live observation window. Expired external success is never authority.
			before := legacyChecksSnapshot(t, ctx, db)
			wake, commits := s.Changed(), s.CommitCount()
			view, _, err := s.ObserveTaskGates(ctx, id, base.Add(48*time.Hour))
			must(err)
			assertShadowView(t, view)
			if before != legacyChecksSnapshot(t, ctx, db) || commits != s.CommitCount() {
				t.Fatal("elapsed time activated effects")
			}
			select {
			case <-wake:
				t.Fatal("elapsed observation woke dispatch")
			default:
			}
		})
	}
}

func assertShadowView(t *testing.T, view *gates.ObservationView) {
	t.Helper()
	if view == nil || view.Mode != "observation_only" || view.Authority != "legacy" || view.LifecycleEffects || view.ActionEffects || view.DispatchEffects || view.AutomaticCutover {
		t.Fatal("observation authority/effects violated")
	}
	if len(view.Bindings) != len(view.Results) {
		t.Fatal("binding/result mismatch")
	}
	if path := os.Getenv("GRAIN_TEST_QUALIFICATION_VIEWS"); path != "" {
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(append(raw, '\n'))
		closeErr := f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	for i, b := range view.Bindings {
		if b.Required || b.Action != nil || b.Purpose != "monitor" {
			t.Fatalf("operational binding %+v", b)
		}
		r := view.Results[i].Result
		if r.Status == "error" && (view.Results[i].Disposition != "rejected" || view.Results[i].AcceptedAt != nil || view.Results[i].RetryAt == nil) {
			t.Fatal("evaluator error was accepted or lacks a retry boundary")
		}
		if r.Type != b.Type {
			t.Fatal("result type does not match its binding")
		}
		if len(r.Work) > 0 || len(r.Actions) > 0 {
			t.Fatal("prediction supplied executable work/actions")
		}
	}
}

// RunWideShadowGraph keeps evidence collection independent from visible comment
// pages, and checks unchanged read cost over
// a broad explicit dependency set with a realistic long conversation.
func RunWideShadowGraph(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	parent := model.Task{ID: "wide-parent", Title: "Qualify child outcomes", Intent: model.IntentAnalyze}
	must(s.PutTask(ctx, parent))
	ids := make([]string, 32)
	for i := range ids {
		ids[i] = fmt.Sprintf("wide-child-%02d", i)
		must(s.PutTask(ctx, model.Task{ID: ids[i], Title: ids[i], Intent: model.IntentAnalyze}))
		at := now.Add(-time.Minute)
		must(s.ObserveField(ctx, ids[i], now, func(o *model.Observation) { o.ClosedAt = &at }))
	}
	// 1024 comments are independent persisted source records, not 1024 copies
	// inserted into a mock evaluator. The newest local source fence must survive.
	for i := 0; i < 1024; i++ {
		_, err := s.AddComment(ctx, model.Comment{TaskID: parent.ID, Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "person"}}, Body: fmt.Sprintf("Historical qualification discussion %d", i), CreatedAt: now.Add(-time.Hour)})
		must(err)
	}
	selection := gates.ChildrenConfig{Tasks: ids, Predicate: "closed"}
	before := legacyChecksSnapshot(t, ctx, db)
	wake, commits := s.Changed(), s.CommitCount()
	start := time.Now()
	v, d, err := s.ObserveTaskGates(ctx, parent.ID, now, selection)
	must(err)
	assertShadowView(t, v)
	for i, b := range v.Bindings {
		if b.Type == "children" && v.Results[i].Result.Status != "met" {
			t.Fatalf("wide complete closed set: %+v", v.Results[i].Result)
		}
	}
	raw, err := json.Marshal(v)
	must(err)
	if len(raw) > 256*1024 {
		t.Fatal("view exceeded admission bound")
	}
	initial := d.Evaluations
	for i := 0; i < 16; i++ {
		replay, diag, e := s.ObserveTaskGates(ctx, parent.ID, now, selection)
		must(e)
		if replay.SourceRevision != v.SourceRevision || diag.Evaluations != initial {
			t.Fatal("unchanged read repeatedly scanned into new history")
		}
	}
	t.Logf("synthetic fixture: 32 children, 1024 comments, 17 reads, view=%d bytes, elapsed=%s, evaluations=%d", len(raw), time.Since(start), initial)
	if before != legacyChecksSnapshot(t, ctx, db) || commits != s.CommitCount() {
		t.Fatal("wide reads mutated legacy state")
	}
	select {
	case <-wake:
		t.Fatal("wide reads woke dispatch")
	default:
	}
	// A missed explicit dirty notification must still be found by local source
	// reconciliation. Direct writes are fault injection only.
	_, err = db.ExecContext(ctx, "UPDATE task_observation SET closed_at = NULL WHERE task_id = ?", ids[31])
	must(err)
	changed, _, err := s.ObserveTaskGates(ctx, parent.ID, now, selection)
	must(err)
	if changed.SourceRevision == v.SourceRevision {
		t.Fatal("missed child event left accepted met cached")
	}
	for i, b := range changed.Bindings {
		if b.Type == "children" && changed.Results[i].Result.Status == "met" {
			t.Fatal("reopened required child still satisfied")
		}
	}
}
