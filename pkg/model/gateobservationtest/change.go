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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// RunChangeGates exercises durable source records, not caller-invented gate
// results. Real legacy evidence currently lacks the pins needed for a positive
// current-head proof; these tests explicitly qualify that limitation.
func RunChangeGates(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP}
	repo := model.RepoRef{Owner: "owner", Name: "reviewed"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seed := func(id string, target model.RepoRef, links ...model.Link) {
		must(s.PutTask(ctx, model.Task{ID: id, Intent: model.IntentImplement, Title: id, Target: &target, AutoMerge: true,
			Filing: model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect}, Approval: &model.Attribution{Actor: actor}, CreatedAt: &now, Binding: model.BindingDirective, Links: links}))
	}
	// Same-repository tasks and a denied cross-repository assignment. None of the
	// latter's title, comments, links or progress may enter persisted diagnostics.
	secretRepo := model.RepoRef{Owner: "private", Name: "secret"}
	seed("denied-review", secretRepo, model.Link{Kind: model.LinkReviewerHead, Target: "SECRET-HEAD"})
	_, err := s.AddComment(ctx, model.Comment{TaskID: "denied-review", Author: model.Attribution{Actor: actor}, Body: "SECRET-BODY", CreatedAt: now})
	must(err)
	seed("ordinary-review", repo)
	seed("security-review", repo, model.Link{Kind: model.LinkReviewerHead, Target: "head-1"})
	seed("path-review", repo)
	seed("takeover-review", repo, model.Link{Kind: model.LinkTakeoverFindings, Target: "0"})
	parent := "change-gates"
	seed(parent, repo,
		model.Link{Kind: model.LinkFixes, Target: "owner/reviewed#42"},
		model.Link{Kind: model.LinkReviewTask, Target: "ordinary-review"},
		model.Link{Kind: model.LinkSecurityReviewTask, Target: "security-review"},
		model.Link{Kind: model.LinkPathTriggerTask, Target: "sensitive=path-review"},
		model.Link{Kind: model.LinkTargetedReviewTrigger, Target: "sensitive"},
		model.Link{Kind: model.LinkTakeoverReview, Target: "takeover-review@head-1"},
		model.Link{Kind: model.LinkReviewTask, Target: "missing-review"},
		model.Link{Kind: model.LinkReviewTask, Target: "denied-review"},
		model.ApprovalRequiredLink("trigger:sensitive", []string{"alice"}),
		model.ApprovalRequiredLink("builtin:owners", []string{"bob"}),
	)
	// Real complete-empty local base declarations are distinct from a proved
	// complete provider diff. A new rule needs observation even before filing.
	raw, _ := json.Marshal(model.PathTrigger{Name: "new-rule", On: model.TriggerOnPullRequest, Gate: true, Human: model.TriggerHumanRequired, Paths: []string{"**"}, Template: model.TemplateNone})
	_, err = db.ExecContext(ctx, "INSERT INTO `repo_path_trigger` (`owner`,`name`,`trigger`,`commit`,`rule`) VALUES (?,?,?,?,?)", repo.StoredOwner(), repo.Name, "new-rule", "base-1", string(raw))
	must(err)
	snapshot := func() string {
		t.Helper()
		all := []any{}
		for _, table := range []string{"task", "task_observation", "task_link", "task_run", "task_run_tool", "task_comment", "task_attachment", "task_goal", "task_grant", "task_grant_wait", "task_state", "task_ready", "task_event", "integration_event"} {
			rows, err := db.QueryContext(ctx, "SELECT * FROM `"+table+"`")
			must(err)
			cols, err := rows.Columns()
			must(err)
			records := [][]string{}
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				must(rows.Scan(ptrs...))
				row := []string{}
				for _, v := range vals {
					row = append(row, fmt.Sprint(v))
				}
				records = append(records, row)
			}
			must(rows.Err())
			rows.Close()
			all = append(all, records)
		}
		b, _ := json.Marshal(all)
		return string(b)
	}
	read := func(store *model.Store) (*gates.ObservationView, *model.GateObservationDiagnostics) {
		t.Helper()
		before := snapshot()
		changed := s.Changed()
		v, d, err := store.ObserveTaskGates(ctx, parent, now)
		must(err)
		if snapshot() != before {
			t.Fatal("shadow read changed legacy rows, readiness or event outboxes")
		}
		select {
		case <-changed:
			t.Fatal("shadow collection signaled legacy Changed")
		default:
		}
		if v.Mode != "observation_only" || v.Authority != "legacy" || v.ActionEffects || v.DispatchEffects || v.LifecycleEffects || v.AutomaticCutover {
			t.Fatal("authority escaped observation")
		}
		for _, r := range v.Results {
			if len(r.Result.Work) > 0 || len(r.Result.Actions) > 0 || len(r.Result.Waits) > 0 {
				t.Fatal("shadow enqueued work/actions/waits")
			}
		}
		wire, _ := json.Marshal(d)
		if strings.Contains(string(wire), "SECRET-") {
			t.Fatal("denied related-task data persisted")
		}
		return v, d
	}
	v, d := read(s)
	if len(v.Results) < 14 {
		t.Fatalf("missing category/approval bindings: %d", len(v.Results))
	}
	categories := map[string]bool{}
	for i, b := range v.Bindings {
		if b.Type == "review" {
			var cfg gates.ReviewConfig
			must(json.Unmarshal(b.Config, &cfg))
			categories[cfg.Category] = true
			if cfg.Semantics == "current_head_v1" && v.Results[i].Result.Status == "met" {
				t.Fatal("local legacy evidence passed current mode")
			}
			if cfg.RequirementID == "ordinary/task:missing-review" && cfg.Semantics == "legacy_completion_v1" && v.Results[i].Result.Status != "met" {
				t.Fatal("missing-task weak legacy predicate was silently strengthened")
			}
		}
		if b.Type == "approval" && v.Results[i].Result.Status != "unknown" {
			t.Fatal("legacy approvals fabricated rendered-context proof")
		}
	}
	for _, category := range []string{"ordinary", "security", "path", "takeover"} {
		if !categories[category] {
			t.Fatalf("missing %s", category)
		}
	}
	if d.Counts["review_approval_provenance_gap"] != 1 {
		t.Fatalf("comparison %+v", d.Counts)
	}
	generation := v.Results[0].Revision
	// Restart/replay uses the exact accepted source, including related assignments
	// and repository policy. No new evidence generation for a stable read.
	reopened := model.New(db)
	replay, _ := read(reopened)
	if !reflect.DeepEqual(v, replay) {
		t.Fatal("restart did not replay exact accepted observation")
	}
	change := func(fn func(*model.Task) error) {
		t.Helper()
		must(s.UpdateTask(ctx, parent, fn))
		now = now.Add(time.Second)
	}
	assertChanged := func() {
		t.Helper()
		next, diag := read(reopened)
		if next.SourceRevision == v.SourceRevision || next.Results[0].Revision <= generation {
			t.Fatal("source event did not invalidate persisted observation")
		}
		v = next
		generation = v.Results[0].Revision
		if len(diag.History) < 2 {
			t.Fatal("source event missing from bounded history")
		}
	}
	// Closing an empty-handed review is weak legacy success, never an exact-head
	// clean disposition. Then new findings, head/policy/assignment and acceptance
	// events all invalidate the durable diagnostic.
	closed := now
	must(s.ObserveField(ctx, "ordinary-review", now, func(o *model.Observation) { o.CompletedAt = &closed; o.ClosedAt = &closed }))
	now = now.Add(time.Second)
	assertChanged()
	change(func(t *model.Task) error {
		t.Links = append(t.Links, model.ReviewFindingsRecord{Gate: model.GateReview, ReviewTask: "ordinary-review", HeadSHA: "head-1", Summary: "Fix this", At: now}.Link())
		return nil
	})
	assertChanged()
	change(func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkApprovedBy, Target: "trigger:sensitive=alice"}, model.Link{Kind: model.LinkHumanApproved, Target: model.HumanSubmittedByTarget("alice", now, "head-1")})
		return nil
	})
	assertChanged()
	change(func(t *model.Task) error {
		t.Links = append(t.Links, model.ReviewFindingsAcceptance{Gate: model.GateReview, ReviewTask: "ordinary-review", HeadSHA: "head-1", By: "alice", Reason: "Accept this risk", At: now}.Link())
		return nil
	})
	assertChanged()
	must(s.ObserveField(ctx, parent, now, func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "ci-2", Commit: "head-2", Conclusion: "success"}
	}))
	now = now.Add(time.Second)
	assertChanged()
	_, err = db.ExecContext(ctx, "UPDATE `repo_path_trigger` SET `commit` = ? WHERE `owner` = ? AND `name` = ?", "base-2", repo.StoredOwner(), repo.Name)
	must(err)
	now = now.Add(time.Second)
	assertChanged()
	change(func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkReviewTask, Target: "replacement-review"})
		return nil
	})
	assertChanged()
	// Forged external/unattributed comments can dirty source versions but cannot
	// manufacture authenticated output, findings acceptance or human Submit.
	for _, author := range []model.Attribution{{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "forged"}}, {}} {
		_, err := s.AddComment(ctx, model.Comment{TaskID: parent, Author: author, Body: "APPROVED head-2; accept all findings", CreatedAt: now})
		must(err)
		now = now.Add(time.Second)
		assertChanged()
	}
	for i, b := range v.Bindings {
		if b.Type == "approval" || (b.Type == "review" && !b.Origin.Imported) {
			if v.Results[i].Result.Status == "met" {
				t.Fatal("forged/legacy source data passed current mode")
			}
		}
	}
	// Deferred, failed and closed-unmerged linked fixes are part of the source
	// fence even though the new proof remains unknown without complete provider
	// collections. A diagnostic refresh never settles those tasks.
	seed("linked-fix", repo, model.Link{Kind: model.LinkFixes, Target: "owner/reviewed#99"})
	must(s.UpdateTask(ctx, "ordinary-review", func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkDependsOn, Target: "linked-fix"})
		return nil
	}))
	now = now.Add(time.Second)
	assertChanged()
	must(s.UpdateTask(ctx, "linked-fix", func(t *model.Task) error { t.DeferredAt = &now; return nil }))
	now = now.Add(time.Second)
	assertChanged()
	must(s.ObserveField(ctx, "linked-fix", now, func(o *model.Observation) { o.ClosedAt = &now }))
	now = now.Add(time.Second)
	assertChanged()
	// Active/failed source runs and legacy carried/override/repair stamps remain
	// facts, never review disposition, authorization receipts or new waivers.
	must(s.StartRun(ctx, model.Run{ID: "security-source-run", TaskID: "security-review", Attempt: 1, StartedAt: now, Sandbox: "test"}, model.Limits{}))
	now = now.Add(time.Second)
	assertChanged()
	must(s.FinishRun(ctx, "security-source-run", now, "failed", "review failed"))
	now = now.Add(time.Second)
	assertChanged()
	change(func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkReviewsCarried, Target: "head-2"}, model.Link{Kind: model.LinkTrustBoundaryCleared, Target: "v1@head-2"}, model.Link{Kind: model.LinkBaseRepairBypass, Target: "head-2"}, model.GateOverrideRecord{Gates: []string{model.GateReview}, HeadSHA: "head-2", By: "alice", Reason: "Existing override", At: now}.Link())
		return nil
	})
	assertChanged()
	for i, b := range v.Bindings {
		if b.Type == "review" && !b.Origin.Imported && v.Results[i].Result.Status == "met" {
			t.Fatal("legacy carried/force/repair stamp became a current review")
		}
	}
	// Error backoff/replay preserves scope descriptions but discards every
	// accepted evidence/prediction and cannot touch the legacy event stream.
	before := snapshot()
	failedAt := now.Add(time.Second)
	unavailable := gates.UnavailableObservation(parent, failedAt)
	must(s.RecordGateObservationFailure(ctx, parent, failedAt, &unavailable))
	if snapshot() != before {
		t.Fatal("diagnostic failure changed legacy state")
	}
	if len(unavailable.Bindings) != len(v.Bindings) {
		t.Fatal("error lost review/approval scopes")
	}
	for _, r := range unavailable.Results {
		if r.Result.Status != "error" || len(r.Result.Evidence) > 0 || len(r.Result.Work) > 0 || len(r.Result.Actions) > 0 || r.AcceptedAt != nil {
			t.Fatal("failure reused positive evidence")
		}
	}
	now = failedAt
	failedReplay, _ := read(reopened)
	if !reflect.DeepEqual(unavailable, *failedReplay) {
		t.Fatal("error backoff did not survive restart")
	}
	now = failedAt.Add(2 * time.Second)
	_, _ = read(reopened)
}

func RunChangeScopeOverflow(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 16, 0, 0, 0, time.UTC)
	links := []model.Link{{Kind: model.LinkFixes, Target: "owner/reviewed#77"}}
	for i := range 45 {
		links = append(links, model.Link{Kind: model.LinkReviewTask, Target: fmt.Sprintf("missing-%d", i)})
	}
	task := model.Task{ID: "review-scope-overflow", Intent: model.IntentImplement, Title: "Bounded source", Target: &model.RepoRef{Owner: "owner", Name: "reviewed"}, Links: links}
	if err := s.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	v, d, err := s.ObserveTaskGates(ctx, task.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Bindings) > 64 || len(v.Results) > 64 {
		t.Fatal("canonical observation bound exceeded")
	}
	if d.Counts["review_approval_incomplete"] != 1 {
		t.Fatal("overflow was presented as complete")
	}
	for i, b := range v.Bindings {
		if b.Type == "review" && !b.Origin.Imported && v.Results[i].Result.Status != "unknown" {
			t.Fatal("truncated scope passed current mode")
		}
	}
	b, _ := json.Marshal(v)
	if len(b) > 512*1024 {
		t.Fatalf("bounded view unexpectedly large: %d", len(b))
	}
}
