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

package model_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestNormalizeExternalRef(t *testing.T) {
	got, err := model.NormalizeExternalRef("  CA-204  ")
	if err != nil || got != "CA-204" {
		t.Fatalf("NormalizeExternalRef = (%q, %v), want (\"CA-204\", nil)", got, err)
	}
	if _, err := model.NormalizeExternalRef("bad\nref"); err == nil {
		t.Fatal("expected control character in externalRef to be rejected")
	}
	if _, err := model.NormalizeExternalRef(strings.Repeat("x", model.MaxExternalRefLen+1)); err == nil {
		t.Fatal("expected oversized externalRef to be rejected")
	}
}

func TestExternalRefInheritanceAndClearing(t *testing.T) {
	store, ctx := open(t)
	widgets, _ := model.ParseRepo("acme/widgets")

	// 1. InheritRequester copies ExternalRef onto spawned tasks (e.g. base-branch
	// repairs that carry no task_link) unless the derived task set one explicitly.
	src := model.Task{ID: "root-req", ExternalRef: "  CA-100  "}
	var derived model.Task
	model.InheritRequester(&derived, src)
	if derived.ExternalRef != "CA-100" {
		t.Fatalf("InheritRequester ExternalRef = %q, want CA-100", derived.ExternalRef)
	}
	explicit := model.Task{ExternalRef: "CA-OVERRIDE"}
	model.InheritRequester(&explicit, src)
	if explicit.ExternalRef != "CA-OVERRIDE" {
		t.Fatalf("InheritRequester overwrote explicit ExternalRef: got %q", explicit.ExternalRef)
	}

	// 2. Store PutTask inherits across every spawn link kind, multi-hop chains,
	// and WorkOrigin task:<id>.
	if err := store.PutTask(ctx, model.Task{
		ID:          "parent",
		Intent:      model.IntentImplement,
		Title:       "parent",
		Target:      &widgets,
		ExternalRef: "CA-204",
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id   string
		link model.LinkKind
	}{
		{"prop", model.LinkProposedBy},
		{"child", model.LinkChildOf},
		{"port", model.LinkPortOf},
		{"rev-push", model.LinkPushesReviewedBranch},
		{"takeover", model.LinkTakeoverReviewOf},
		{"revert", model.LinkReverts},
		{"cont", model.LinkContinues},
		{"super", model.LinkSupersedes},
	} {
		if err := store.PutTask(ctx, model.Task{
			ID:     tc.id,
			Intent: model.IntentImplement,
			Title:  tc.id,
			Target: &widgets,
			Links:  []model.Link{{Kind: tc.link, Target: "parent"}},
		}); err != nil {
			t.Fatalf("PutTask(%s): %v", tc.id, err)
		}
		got, err := store.GetTask(ctx, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.ExternalRef != "CA-204" {
			t.Errorf("%s (%s): ExternalRef = %q, want CA-204", tc.id, tc.link, got.ExternalRef)
		}
	}

	// Multi-hop inheritance (grandchild -> child -> parent) and WorkOrigin fallback.
	if err := store.PutTask(ctx, model.Task{
		ID:     "grandchild",
		Intent: model.IntentImplement,
		Title:  "grandchild",
		Target: &widgets,
		Links:  []model.Link{{Kind: model.LinkChildOf, Target: "child"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "grandchild"); got.ExternalRef != "CA-204" {
		t.Errorf("grandchild ExternalRef = %q, want CA-204", got.ExternalRef)
	}

	if err := store.PutTask(ctx, model.Task{
		ID:         "origin-child",
		Intent:     model.IntentImplement,
		Title:      "origin-child",
		Target:     &widgets,
		WorkOrigin: model.TaskOrigin("parent"),
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "origin-child"); got.ExternalRef != "CA-204" {
		t.Errorf("origin-child ExternalRef = %q, want CA-204", got.ExternalRef)
	}

	// Explicit ExternalRef on a spawned task wins over the parent's.
	if err := store.PutTask(ctx, model.Task{
		ID:          "explicit-child",
		Intent:      model.IntentImplement,
		Title:       "explicit-child",
		Target:      &widgets,
		ExternalRef: "CA-999",
		Links:       []model.Link{{Kind: model.LinkProposedBy, Target: "parent"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "explicit-child"); got.ExternalRef != "CA-999" {
		t.Errorf("explicit-child ExternalRef = %q, want CA-999", got.ExternalRef)
	}

	// Clearing ExternalRef on an existing child stays cleared rather than
	// re-inheriting from parent on update.
	if err := store.UpdateTask(ctx, "child", func(tk *model.Task) error {
		tk.ExternalRef = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "child"); got.ExternalRef != "" {
		t.Errorf("cleared child ExternalRef = %q, want empty", got.ExternalRef)
	}

	// 3. Scoped inheritance guards: WithExternalRefReadable blocks inheriting
	// directly or transitively from unreadable repositories, and explicit
	// WorkOrigin=task:<id> on a proposed task requires tasks:read on that task's
	// repository when the proposing parent itself carries no ExternalRef.
	secretRepo, _ := model.ParseRepo("acme/secret")
	if err := store.PutTask(ctx, model.Task{
		ID:          "secret-root",
		Intent:      model.IntentImplement,
		Title:       "secret-root",
		Target:      &secretRepo,
		ExternalRef: "SEC-777",
	}); err != nil {
		t.Fatal(err)
	}
	scopedCtx := model.WithExternalRefReadable(ctx, func(repo string) bool {
		return repo == "acme/widgets"
	})
	if err := store.PutTask(scopedCtx, model.Task{
		ID:         "blocked-cross-repo",
		Intent:     model.IntentImplement,
		Title:      "blocked-cross-repo",
		Target:     &widgets,
		WorkOrigin: model.TaskOrigin("secret-root"),
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "blocked-cross-repo"); got.ExternalRef != "" {
		t.Errorf("blocked-cross-repo ExternalRef = %q, want empty", got.ExternalRef)
	}

	if err := store.PutTask(ctx, model.Task{
		ID:     "unreferenced-proposer",
		Intent: model.IntentImplement,
		Title:  "unreferenced-proposer",
		Target: &widgets,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:         "proposal-probing-secret",
		Intent:     model.IntentImplement,
		Title:      "proposal-probing-secret",
		Target:     &widgets,
		Links:      []model.Link{{Kind: model.LinkProposedBy, Target: "unreferenced-proposer"}},
		WorkOrigin: model.TaskOrigin("secret-root"),
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "proposal-probing-secret"); got.ExternalRef != "" {
		t.Errorf("proposal-probing-secret ExternalRef = %q, want empty", got.ExternalRef)
	}

	// Even when the proposer holds tasks:read on acme/widgets, an explicit
	// origin=task:<widgets-bridge> must not walk through a non-ExternalRef
	// bridge task in acme/widgets into an unreadable ancestor in acme/secret.
	if err := store.PutTask(ctx, model.Task{
		ID:     "secret-late-ref",
		Intent: model.IntentImplement,
		Title:  "secret-late-ref",
		Target: &secretRepo,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:     "widgets-bridge",
		Intent: model.IntentImplement,
		Title:  "widgets-bridge",
		Target: &widgets,
		Links:  []model.Link{{Kind: model.LinkReverts, Target: "secret-late-ref"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, "secret-late-ref", func(t *model.Task) error {
		t.ExternalRef = "SEC-LATE-999"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:     "widgets-reader-proposer",
		Intent: model.IntentImplement,
		Title:  "widgets-reader-proposer",
		Target: &widgets,
		Grants: []model.Grant{{Capability: "api:tasks:read@acme/widgets", Via: model.GrantByLabel}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:         "proposal-transitive-bounce",
		Intent:     model.IntentImplement,
		Title:      "proposal-transitive-bounce",
		Target:     &widgets,
		Links:      []model.Link{{Kind: model.LinkProposedBy, Target: "widgets-reader-proposer"}},
		WorkOrigin: model.TaskOrigin("widgets-bridge"),
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "proposal-transitive-bounce"); got.ExternalRef != "" {
		t.Errorf("proposal-transitive-bounce ExternalRef = %q, want empty", got.ExternalRef)
	}

	// Same transitive guard applies to LinkSupersedes on a proposed task and to
	// a second-hop child spawned from that proposal in daemon context.
	if err := store.PutTask(ctx, model.Task{
		ID:     "proposal-supersedes-bounce",
		Intent: model.IntentImplement,
		Title:  "proposal-supersedes-bounce",
		Target: &widgets,
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
				OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: "widgets-reader-proposer"},
			},
			Reason: model.ReasonDirect,
		},
		Links: []model.Link{
			{Kind: model.LinkProposedBy, Target: "widgets-reader-proposer"},
			{Kind: model.LinkSupersedes, Target: "widgets-bridge"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "proposal-supersedes-bounce"); got.ExternalRef != "" {
		t.Errorf("proposal-supersedes-bounce ExternalRef = %q, want empty", got.ExternalRef)
	}
	if err := store.PutTask(ctx, model.Task{
		ID:     "proposal-two-hop-child",
		Intent: model.IntentReview,
		Title:  "proposal-two-hop-child",
		Target: &widgets,
		Links:  []model.Link{{Kind: model.LinkProposedBy, Target: "proposal-supersedes-bounce"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetTask(ctx, "proposal-two-hop-child"); got.ExternalRef != "" {
		t.Errorf("proposal-two-hop-child ExternalRef = %q, want empty", got.ExternalRef)
	}
}

func TestRefTaskRollupRowsAndSummary(t *testing.T) {
	store, ctx := open(t)
	widgets, _ := model.ParseRepo("acme/widgets")
	secretRepo, _ := model.ParseRepo("acme/secret")
	base := time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)

	// Task 1: completed with open on-target PR and Grain CI run (2/3 passed).
	if err := store.PutTask(ctx, model.Task{
		ID:          "1",
		Intent:      model.IntentImplement,
		Title:       "Implement API",
		Target:      &widgets,
		Branch:      "grain/task-1",
		ExternalRef: "CA-204",
		Links: []model.Link{
			{Kind: model.LinkFixes, Target: "acme/widgets#10"},
			// Off-target PR link must be ignored.
			{Kind: model.LinkFixes, Target: "other/repo#99"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	prOpened := base.Add(10 * time.Minute)
	completedAt := base.Add(15 * time.Minute)
	if err := store.PutCIRun(ctx, model.CIRun{
		ID:         "ci-1",
		Key:        "k1",
		Repo:       "acme/widgets",
		Ref:        "grain/task-1",
		Commit:     "aaa111",
		Event:      "push",
		StartedAt:  base.Add(11 * time.Minute),
		FinishedAt: base.Add(14 * time.Minute),
		Conclusion: "failure",
		Jobs: []model.CIJob{
			{JobID: "unit", Name: "unit", Conclusion: "success"},
			{JobID: "lint", Name: "lint", Conclusion: "skipped"},
			{JobID: "e2e", Name: "e2e", Conclusion: "failure"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID:      "1",
		PrOpenedAt:  &prOpened,
		CompletedAt: &completedAt,
	}); err != nil {
		t.Fatal(err)
	}

	// Task 2: review task sharing Task 1's branch, PR #10, and Grain CI run ci-1
	// (must NOT double-count PR #10 or ci-1's 3 jobs), parked on an open question.
	if err := store.PutTask(ctx, model.Task{
		ID:     "2",
		Intent: model.IntentReview,
		Title:  "Review API",
		Target: &widgets,
		Branch: "grain/task-1",
		Links: []model.Link{
			{Kind: model.LinkPushesReviewedBranch, Target: "1"},
			{Kind: model.LinkFixes, Target: "acme/widgets#10"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	qTime := base.Add(25 * time.Minute)
	qid, err := store.AddComment(ctx, model.Comment{
		TaskID:    "2",
		Summary:   "Keep v1 endpoint alongside v2?",
		Body:      "Full details of the API compatibility question.",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAgent, ID: "run-2"}},
		CreatedAt: qTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID:                   "2",
		PendingQuestionCommentID: &qid,
	}); err != nil {
		t.Fatal(err)
	}

	// Task 3: closed + merged PR #11 with GitHub CI checks (2/2 on latest commit;
	// older commit's failure must be ignored).
	if err := store.PutTask(ctx, model.Task{
		ID:     "3",
		Intent: model.IntentImplement,
		Title:  "Schema migration",
		Target: &widgets,
		Branch: "grain/task-3",
		Links: []model.Link{
			{Kind: model.LinkChildOf, Target: "1"},
			{Kind: model.LinkFixes, Target: "acme/widgets#11"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	oldRec := base.Add(5 * time.Minute)
	newRec := base.Add(30 * time.Minute)
	for _, job := range []model.GitHubCIJob{
		{Repo: "acme/widgets", CheckRunID: 101, Ref: "grain/task-3", Commit: "oldsha", JobName: "build", Conclusion: "failure", RecordedAt: oldRec},
		{Repo: "acme/widgets", CheckRunID: 201, Ref: "grain/task-3", Commit: "newsha", JobName: "build", Conclusion: "success", RecordedAt: newRec},
		{Repo: "acme/widgets", CheckRunID: 202, Ref: "grain/task-3", Commit: "newsha", JobName: "test", Conclusion: "neutral", RecordedAt: newRec},
	} {
		if err := store.PutGitHubCIJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	prMerged := base.Add(35 * time.Minute)
	if err := store.Observe(ctx, model.Observation{
		TaskID:     "3",
		PrOpenedAt: &prOpened,
		PrMergedAt: &prMerged,
		ClosedAt:   &prMerged,
	}); err != nil {
		t.Fatal(err)
	}

	// Task 4: task in another repo (acme/secret) carrying the same externalRef,
	// parked on a secret request (with TurnStoppedComment on the parking comment
	// so PendingSecret fallback summary is exercised).
	if err := store.PutTask(ctx, model.Task{
		ID:          "4",
		Intent:      model.IntentImplement,
		Title:       "Secret rotation",
		Target:      &secretRepo,
		ExternalRef: "CA-204",
	}); err != nil {
		t.Fatal(err)
	}
	secretAt := base.Add(45 * time.Minute)
	secretQID, err := store.AddComment(ctx, model.Comment{
		TaskID:    "4",
		Body:      model.TurnStoppedComment,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		CreatedAt: secretAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID:                   "4",
		PendingQuestionCommentID: &secretQID,
		PendingSecret:            "prod-api-key",
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.RefTaskRollupRows(ctx, "CA-204")
	if err != nil {
		t.Fatalf("RefTaskRollupRows: %v", err)
	}
	all := model.BuildRefSummary("CA-204", rows, nil)
	if all.Total != 4 {
		t.Fatalf("Total = %d, want 4", all.Total)
	}
	if all.ByState[model.StateAwaitingSubmit] != 1 || all.ByState[model.StateAwaitingReply] != 2 || all.ByState[model.StateClosed] != 1 {
		t.Errorf("ByState = %+v, want 1 awaiting_submit, 2 awaiting_reply, 1 closed", all.ByState)
	}
	// Checks: 2/3 from ci-1 (deduplicated across tasks 1 & 2) + 2/2 from task 3's latest commit = 4/5.
	if all.Checks != (model.RefCheckCounts{Passed: 4, Total: 5}) {
		t.Errorf("Checks = %+v, want {Passed:4 Total:5}", all.Checks)
	}
	// PRs: #10 (open, deduplicated across tasks 1 & 2) + #11 (merged) => opened=2, merged=1, open=1.
	if all.PullRequests != (model.RefPullRequestCounts{Opened: 2, Merged: 1, Open: 1}) {
		t.Errorf("PullRequests = %+v, want {Opened:2 Merged:1 Open:1}", all.PullRequests)
	}
	if len(all.OpenQuestions) != 2 {
		t.Fatalf("OpenQuestions = %+v, want 2", all.OpenQuestions)
	}
	if all.OpenQuestions[0].TaskID != "2" || all.OpenQuestions[0].Summary != "Keep v1 endpoint alongside v2?" {
		t.Errorf("OpenQuestions[0] = %+v", all.OpenQuestions[0])
	}
	if all.OpenQuestions[1].TaskID != "4" || all.OpenQuestions[1].Summary != "set prod-api-key in the encrypted secret input" {
		t.Errorf("OpenQuestions[1] = %+v, want secret summary (not TurnStoppedComment)", all.OpenQuestions[1])
	}
	if all.LastActivityAt == nil || !all.LastActivityAt.Equal(secretAt) {
		t.Errorf("LastActivityAt = %v, want %v", all.LastActivityAt, secretAt)
	}

	// Visibility predicate hides acme/secret completely.
	widgetsOnly := model.BuildRefSummary("CA-204", rows, func(repo string) bool {
		return repo == "acme/widgets"
	})
	if widgetsOnly.Total != 3 || len(widgetsOnly.OpenQuestions) != 1 {
		t.Errorf("widgetsOnly = %+v, want 3 tasks and 1 open question", widgetsOnly)
	}
	if widgetsOnly.LastActivityAt == nil || !widgetsOnly.LastActivityAt.Equal(prMerged) {
		t.Errorf("widgetsOnly.LastActivityAt = %v, want %v", widgetsOnly.LastActivityAt, prMerged)
	}
}
