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
		assertShadowView(t, v)
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
		if diag.Counts["unknown_unsupported_config"] != 0 {
			t.Fatal("normal source/assignment change was mislabeled as unsupported cached configuration")
		}
		for i, b := range next.Bindings {
			if b.Type != "review" || !b.Origin.Imported {
				continue
			}
			var config gates.ReviewConfig
			must(json.Unmarshal(b.Config, &config))
			if config.RequirementID == "ordinary/task:missing-review" && next.Results[i].Result.Status != "met" {
				t.Fatal("normal assignment change suppressed the independently known legacy comparison")
			}
		}
		previousGeneration := generation
		v = next
		generation = v.Results[0].Revision
		var retained int
		must(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `gate_observation_shadow_history` WHERE `task_id` = ? AND `generation` IN (?,?)", parent, previousGeneration, generation).Scan(&retained))
		if retained != 2 || len(diag.History) < 2 || diag.History[0].Generation != generation || diag.History[1].Generation != previousGeneration {
			t.Fatalf("source event missing from persisted history or newest read samples: retained=%d len=%d", retained, len(diag.History))
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
	// Scoped current-only projections do not load retained payloads. An invalid
	// older diagnostic record must not break today's independently valid result.
	_, err = db.ExecContext(ctx, "UPDATE `gate_observation_shadow_history` SET `view_json` = ? WHERE `task_id` = ? AND `generation` = (SELECT MIN(`generation`) FROM `gate_observation_shadow_history` WHERE `task_id` = ?)", "not-json", parent, parent)
	must(err)
	if current, err := reopened.ObserveTaskGatePredictions(ctx, parent, now); err != nil || current == nil {
		t.Fatalf("current-only projection depended on retained payloads: %v", err)
	}
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
	oversized := task
	oversized.ID = "review-metadata-overflow"
	oversized.Links = []model.Link{{Kind: model.LinkFixes, Target: "owner/reviewed#78"}}
	for i := range 400 {
		oversized.Links = append(oversized.Links, model.Link{Kind: model.LinkReviewTask, Target: fmt.Sprintf("%s-%d", strings.Repeat("x", 200), i)})
	}
	if err := s.PutTask(ctx, oversized); err != nil {
		t.Fatal(err)
	}
	large, _, err := s.ObserveTaskGates(ctx, oversized.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	changeBindings := 0
	for _, binding := range large.Bindings {
		if binding.Type == "review" || binding.Type == "approval" {
			changeBindings++
		}
	}
	if changeBindings != 3 || len(large.Bindings) > 64 {
		t.Fatal("oversized review metadata was expanded instead of rejected as incomplete")
	}
	for i, binding := range large.Bindings {
		if binding.Type == "review" && !binding.Origin.Imported && (large.Results[i].Result.Status != "unknown" || !strings.Contains(large.Results[i].Result.Summary, "metadata")) {
			t.Fatal("oversized metadata did not produce an explained unknown")
		}
	}
	b, _ := json.Marshal(v)
	if len(b) > 512*1024 {
		t.Fatalf("bounded view unexpectedly large: %d", len(b))
	}
}

// RunPRHistoryBudget verifies that a realistic PR task (~18 bindings with
// review and approval scopes) returns several complete history samples within
// MaxGateObservationHistoryBytes (including samples predating a repository
// move), that even 64-binding maximum views retain before/after history, and
// that scoped current-only reads omit history.
func RunPRHistoryBudget(t *testing.T, s *model.Store, db *sql.DB) {
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	actor := model.Principal{Kind: model.PrincipalHuman, ID: "alice", Source: model.SourceIAP}
	oldRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
	newRepo := model.RepoRef{Owner: "acme", Name: "gadgets"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seedTask := func(id string, target model.RepoRef, links ...model.Link) {
		t.Helper()
		must(s.PutTask(ctx, model.Task{
			ID:        id,
			Intent:    model.IntentImplement,
			Title:     id,
			Target:    &target,
			AutoMerge: true,
			Filing:    model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
			Approval:  &model.Attribution{Actor: actor},
			CreatedAt: &now,
			Binding:   model.BindingDirective,
			Links:     links,
		}))
	}

	// Build a realistic ~18-binding PR projection with CI checks, security,
	// path, ordinary review and approval scopes, plus findings, linked fix, and
	// base-owned trigger context so each serialized sample exceeds the old
	// 64 KiB budget (~66–74 KiB, matching real PR walkthroughs).
	seedTask("pr-hist-fix", oldRepo, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#43"})
	seedTask("pr-hist-sec", oldRepo, model.Link{Kind: model.LinkReviewerHead, Target: "aaaa1111bbbb2222cccc3333dddd4444eeee5555"}, model.Link{Kind: model.LinkDependsOn, Target: "pr-hist-fix"})
	seedTask("pr-hist-path", oldRepo, model.Link{Kind: model.LinkReviewerHead, Target: "aaaa1111bbbb2222cccc3333dddd4444eeee5555"})
	seedTask("pr-hist-ord-1", oldRepo, model.Link{Kind: model.LinkReviewerHead, Target: "aaaa1111bbbb2222cccc3333dddd4444eeee5555"}, model.Link{Kind: model.LinkDependsOn, Target: "pr-hist-fix"})
	seedTask("pr-hist-ord-2", oldRepo, model.Link{Kind: model.LinkReviewerHead, Target: "aaaa1111bbbb2222cccc3333dddd4444eeee5555"})
	parent := "pr-history-budget"
	seedTask(parent, oldRepo,
		model.Link{Kind: model.LinkFixes, Target: "acme/widgets#42"},
		model.Link{Kind: model.LinkSecurityReviewTask, Target: "pr-hist-sec"},
		model.Link{Kind: model.LinkPathTriggerTask, Target: "sensitive=pr-hist-path"},
		model.Link{Kind: model.LinkTargetedReviewTrigger, Target: "sensitive"},
		model.Link{Kind: model.LinkReviewTask, Target: "pr-hist-ord-1"},
		model.Link{Kind: model.LinkReviewTask, Target: "pr-hist-ord-2"},
		model.ReviewFindingsRecord{Gate: model.GateSecurityReview, ReviewTask: "pr-hist-sec", HeadSHA: "aaaa1111bbbb2222cccc3333dddd4444eeee5555", Summary: "Validate scope bounds before replaying cached diagnostics across repository moves and verify related fix tasks stay in scope.", At: now}.Link(),
		model.ReviewFindingsRecord{Gate: model.GateReview, ReviewTask: "pr-hist-ord-1", HeadSHA: "aaaa1111bbbb2222cccc3333dddd4444eeee5555", Summary: "Ensure retry backoff resets after a clean source refresh and never suppresses subsequent valid history samples.", At: now}.Link(),
		model.Link{Kind: model.LinkApprovedBy, Target: "trigger:sensitive=alice"},
		model.Link{Kind: model.LinkHumanApproved, Target: model.HumanSubmittedByTarget("alice", now, "aaaa1111bbbb2222cccc3333dddd4444eeee5555")},
		model.Link{Kind: model.LinkBaseRepairBypass, Target: "aaaa1111bbbb2222cccc3333dddd4444eeee5555"},
		model.GateOverrideRecord{Gates: []string{model.GateReview}, HeadSHA: "aaaa1111bbbb2222cccc3333dddd4444eeee5555", By: "alice", Reason: "Existing override context", At: now}.Link(),
		model.ApprovalRequiredLink("trigger:sensitive", []string{"alice", "bob"}),
		model.ApprovalRequiredLink("builtin:owners", []string{"carol"}),
	)

	// Record 9 distinct source events before a repository move, plus 1 after.
	// Each transition produces one persisted history sample.
	sampleSizes := []int{}
	for step := 0; step < 9; step++ {
		head := fmt.Sprintf("head-%02d-aaaa1111bbbb2222cccc3333dddd4444", step)
		must(s.ObserveField(ctx, parent, now, func(o *model.Observation) {
			o.CI = &model.CIStatus{
				RunID:      fmt.Sprintf("ci-run-%d", step),
				Commit:     head,
				Conclusion: "success",
			}
		}))
		v, _, err := s.ObserveTaskGates(ctx, parent, now)
		must(err)
		assertShadowView(t, v)
		if step == 0 {
			if len(v.Bindings) < 16 || len(v.Bindings) > 22 {
				t.Fatalf("expected realistic ~18-binding PR projection, got %d bindings", len(v.Bindings))
			}
		}
		raw, err := json.Marshal(v)
		must(err)
		if len(raw) <= 64*1024 {
			t.Fatalf("expected realistic PR projection to exceed the old 64 KiB budget, got %d bytes (%d bindings)", len(raw), len(v.Bindings))
		}
		if len(raw) > model.MaxGateObservationViewBytes {
			t.Fatalf("PR projection exceeded per-view byte cap: %d", len(raw))
		}
		sampleSizes = append(sampleSizes, len(raw))
		now = now.Add(time.Second)
	}
	t.Logf("PR projection: %d samples, bytes=%v", len(sampleSizes), sampleSizes)

	// Simulate a repository move on step 10: unrestricted readers must see both
	// the post-move sample and pre-move samples within the history byte budget.
	must(s.UpdateTask(ctx, parent, func(tk *model.Task) error {
		tk.Target = &newRepo
		tk.Links = []model.Link{
			{Kind: model.LinkFixes, Target: "acme/gadgets#7"},
			model.ApprovalRequiredLink("trigger:sensitive", []string{"alice"}),
		}
		return nil
	}))
	now = now.Add(time.Second)

	wake, commits := s.Changed(), s.CommitCount()
	view, diag, err := s.ObserveTaskGates(ctx, parent, now)
	must(err)
	assertShadowView(t, view)
	if commits != s.CommitCount() {
		t.Fatal("PR history read mutated legacy commit count")
	}
	select {
	case <-wake:
		t.Fatal("PR history read woke legacy listeners")
	default:
	}

	var storedCount int
	must(db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `gate_observation_shadow_history` WHERE `task_id` = ?", parent).Scan(&storedCount))
	if storedCount != 10 {
		t.Fatalf("stored history rows = %d, want 10", storedCount)
	}
	if len(diag.History) < 5 {
		t.Fatalf("realistic PR projection returned only %d history samples (sample sizes=%v), want >= 5", len(diag.History), sampleSizes)
	}

	// Verify descending generation order, byte budget enforcement, complete
	// untruncated views, observation-only invariants, and pre-move visibility.
	totalBytes := 0
	sawOldRepo, sawNewRepo := false, false
	for i, sample := range diag.History {
		assertShadowView(t, &sample.View)
		if i > 0 && sample.Generation >= diag.History[i-1].Generation {
			t.Fatalf("history generations not strictly descending: %d after %d", sample.Generation, diag.History[i-1].Generation)
		}
		var storedRaw string
		must(db.QueryRowContext(ctx, "SELECT `view_json` FROM `gate_observation_shadow_history` WHERE `task_id` = ? AND `generation` = ?", parent, sample.Generation).Scan(&storedRaw))
		marshaled, err := json.Marshal(sample.View)
		must(err)
		if string(marshaled) != storedRaw {
			t.Fatalf("history sample generation %d was truncated or altered", sample.Generation)
		}
		totalBytes += len(storedRaw)
		for _, b := range sample.View.Bindings {
			if b.Subject.Kind == "change" {
				switch b.Subject.Repository {
				case oldRepo.String():
					sawOldRepo = true
				case newRepo.String():
					sawNewRepo = true
				}
			}
		}
	}
	if totalBytes > model.MaxGateObservationHistoryBytes {
		t.Fatalf("returned history exceeded byte budget: %d > %d", totalBytes, model.MaxGateObservationHistoryBytes)
	}
	if len(diag.History) < storedCount {
		var nextRaw string
		must(db.QueryRowContext(ctx, "SELECT `view_json` FROM `gate_observation_shadow_history` WHERE `task_id` = ? AND `generation` < ? ORDER BY `generation` DESC LIMIT 1", parent, diag.History[len(diag.History)-1].Generation).Scan(&nextRaw))
		if totalBytes+len(nextRaw) <= model.MaxGateObservationHistoryBytes {
			t.Fatalf("history stopped early at %d bytes when next sample (%d bytes) fit within %d", totalBytes, len(nextRaw), model.MaxGateObservationHistoryBytes)
		}
	}
	if !sawOldRepo || !sawNewRepo {
		t.Fatalf("unrestricted post-move history missing pre-move or post-move repo: old=%t new=%t", sawOldRepo, sawNewRepo)
	}
	if diag.Counts["review_approval_provenance_gap"] != 10 {
		t.Fatalf("aggregate counts lost retained rows: %+v", diag.Counts)
	}

	// Scoped current-only reads return predictions without any history.
	scopedCtx := model.WithExternalRefReadable(ctx, func(repo string) bool { return repo == newRepo.String() })
	scopedView, err := s.ObserveTaskGatePredictions(scopedCtx, parent, now)
	must(err)
	assertShadowView(t, scopedView)
	for _, b := range scopedView.Bindings {
		if b.Subject.Kind == "change" && b.Subject.Repository != newRepo.String() {
			t.Fatalf("scoped prediction exposed pre-move repository %q", b.Subject.Repository)
		}
	}

	// Verify the 64-binding maximum projection still retains at least two
	// complete samples within MaxGateObservationHistoryBytes (2 x 256 KiB).
	maxID := "pr-history-max-64"
	maxLinks := []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#99"}}
	for i := 0; i < 30; i++ {
		rid := fmt.Sprintf("max-rev-%02d", i)
		seedTask(rid, oldRepo, model.Link{Kind: model.LinkReviewerHead, Target: "bbbb2222cccc3333dddd4444eeee5555ffff6666"})
		maxLinks = append(maxLinks, model.Link{Kind: model.LinkReviewTask, Target: rid})
	}
	maxLinks = append(maxLinks, model.ApprovalRequiredLink("builtin:owners", []string{"alice"}))
	seedTask(maxID, oldRepo, maxLinks...)
	for step := 0; step < 4; step++ {
		must(s.ObserveField(ctx, maxID, now, func(o *model.Observation) {
			o.CI = &model.CIStatus{RunID: fmt.Sprintf("max-ci-%d", step), Commit: fmt.Sprintf("max-head-%d", step), Conclusion: "success"}
		}))
		v, _, err := s.ObserveTaskGates(ctx, maxID, now)
		must(err)
		assertShadowView(t, v)
		if step == 0 && len(v.Bindings) != 64 {
			t.Fatalf("expected 64-binding maximum projection, got %d bindings", len(v.Bindings))
		}
		now = now.Add(time.Second)
	}
	_, maxDiag, err := s.ObserveTaskGates(ctx, maxID, now)
	must(err)
	if len(maxDiag.History) < 2 {
		t.Fatalf("64-binding maximum projection returned %d samples, want >= 2", len(maxDiag.History))
	}
	maxBytes := 0
	for _, sample := range maxDiag.History {
		assertShadowView(t, &sample.View)
		raw, err := json.Marshal(sample.View)
		must(err)
		maxBytes += len(raw)
	}
	if maxBytes > model.MaxGateObservationHistoryBytes {
		t.Fatalf("64-binding history exceeded byte budget: %d > %d", maxBytes, model.MaxGateObservationHistoryBytes)
	}
}
