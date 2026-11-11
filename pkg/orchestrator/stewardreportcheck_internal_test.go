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

package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Replay fixture from steward task 768's wake-1 report (2026-10-25) and the
// records of 832, 833, 834, and 836 (design/design/durable-tasks.design.md#DT-14).
const task768Wake1ReportFixture = `### Pipeline Steward Wake 1 Report

- Checked base branch status on ` + "`bwsalmon/grain`" + ` (` + "`main`" + ` and ` + "`candidate`" + `) [task 768].
- Task #832: main->candidate port task queued behind the pipeline pause; reordered to the front of the backlog.
- Task #833: main->candidate port task waiting on base repair; moved ahead in queue order.
- Task #834: main→candidate port task queued for dispatch; reordered beside #832 and #833.
- Task #836: main->candidate port task merged cleanly into candidate.`

func TestStewardReportCheckFlagsTask768Wake1ReplayFixture(t *testing.T) {
	t.Parallel()

	// Actual records of 768, 832, 833, 834, 836 at the time of 768's wake 1:
	// 832, 833, 834 were operator-filed feature/fix proposals (queued, NOT ports),
	// and 836 was queued (not a port and not merged).
	tasks := map[string]StewardCheckTask{
		"768": {ID: "768", State: model.StateRunning, Kinds: []string{model.TaskKindDurable}},
		"832": {ID: "832", State: model.StateQueued, Kinds: nil},
		"833": {ID: "833", State: model.StateQueued, Kinds: nil},
		"834": {ID: "834", State: model.StateQueued, Kinds: nil},
		"836": {ID: "836", State: model.StateQueued, Kinds: nil},
	}
	calls := []model.RunAPICall{
		{RunID: "768-2", Seq: 1, Tool: "list_grain_tasks", TargetIDs: "768,832,833,834,836", ResultBytes: 4096},
		{RunID: "768-2", Seq: 2, Tool: "read_grain_task", TargetIDs: "832", ResultBytes: 1200},
		{RunID: "768-2", Seq: 3, Tool: "read_grain_task", TargetIDs: "833", ResultBytes: 1200},
		{RunID: "768-2", Seq: 4, Tool: "read_grain_task", TargetIDs: "834", ResultBytes: 1200},
		{RunID: "768-2", Seq: 5, Tool: "read_grain_task", TargetIDs: "836", ResultBytes: 1200},
	}

	res := stewardReportCheck("768-2", task768Wake1ReportFixture, calls, tasks, "768")
	if res.Check.Claims < 5 {
		t.Fatalf("Claims = %d, want at least 5", res.Check.Claims)
	}
	if res.Check.Mismatched < 4 {
		t.Fatalf("Mismatched = %d, want at least 4 (832, 833, 834, 836 port/state mismatches); offending = %v",
			res.Check.Mismatched, res.MismatchLines)
	}
	for _, wantID := range []string{"#832", "#833", "#834", "#836"} {
		found := false
		for _, line := range res.MismatchLines {
			if strings.Contains(line, wantID) && strings.Contains(line, "port task") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected mismatch lines to flag %s port task line, got %v", wantID, res.MismatchLines)
		}
	}
}

func TestStewardReportCheckDetectsUncitedUnreadAndAttributeMismatches(t *testing.T) {
	t.Parallel()

	tasks := map[string]StewardCheckTask{
		"101": {ID: "101", State: model.StateQueued, Kinds: []string{model.TaskKindPort}},
		"102": {ID: "102", State: model.StateRunning, Kinds: []string{model.TaskKindRepair}},
		"103": {ID: "103", State: model.StateQueued, Kinds: nil},
	}
	calls := []model.RunAPICall{
		{RunID: "768-3", Seq: 1, Tool: "read_grain_task", TargetIDs: "101", ResultBytes: 800},
	}
	report := strings.Join([]string{
		"- Port task #101 is queued.",
		"- Repair task #102 is running.",
		"- Task #103 is merged.",
		"- A stalled port task failed twice and was retriggered.",
	}, "\n")

	res := stewardReportCheck("768-3", report, calls, tasks)
	if res.Check.Claims != 4 {
		t.Errorf("Claims = %d, want 4", res.Check.Claims)
	}
	if res.Check.Uncited != 1 {
		t.Errorf("Uncited = %d, want 1 (uncited lines: %v)", res.Check.Uncited, res.UncitedLines)
	}
	// #102 and #103 were cited but never read in calls.
	if res.Check.Unread != 2 {
		t.Errorf("Unread = %d, want 2 (unread IDs: %v)", res.Check.Unread, res.UnreadIDs)
	}
	// #103 is queued, so calling it "merged" is an attribute mismatch.
	if res.Check.Mismatched != 1 {
		t.Errorf("Mismatched = %d, want 1 (mismatch lines: %v)", res.Check.Mismatched, res.MismatchLines)
	}
}

func TestCheckAndRecordStewardReportRaisesMismatchAndRegressionNotifications(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)

	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	steward := model.Task{
		ID:         "768",
		Intent:     model.IntentImplement,
		Title:      "Pipeline steward",
		Body:       "steward",
		TemplateID: model.PipelineStewardTemplateID,
		Origin:     model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval:   &model.Attribution{Actor: human},
		Binding:    model.BindingDirective,
		CreatedAt:  &now,
		ApprovedAt: &now,
		Admission:  model.AdmissionDurable,
	}
	if err := store.PutTask(ctx, steward); err != nil {
		t.Fatalf("PutTask steward: %v", err)
	}
	if err := store.SetTaskNotificationSubscriptions(ctx, "768", model.PipelineStewardSubscriptions); err != nil {
		t.Fatalf("SetTaskNotificationSubscriptions: %v", err)
	}

	for _, id := range []string{"832", "833", "834", "836"} {
		task := model.Task{
			ID:         id,
			Intent:     model.IntentImplement,
			Title:      "fix: operator proposal " + id,
			Body:       "proposal",
			Origin:     model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Approval:   &model.Attribution{Actor: human},
			Binding:    model.BindingDirective,
			CreatedAt:  &now,
			ApprovedAt: &now,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("PutTask %s: %v", id, err)
		}
	}

	if err := store.StartRun(ctx, model.Run{
		ID: "768-1", TaskID: "768", Sandbox: "sbx-768-1", Attempt: 1, StartedAt: now.Add(-5 * time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := store.FinishRun(ctx, "768-1", now, "succeeded", "ok"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	result := &agent.Result{
		ToolCalls: []agent.ToolCall{
			{
				Name:      "read_grain_task",
				Arguments: map[string]any{"task_id": "832"},
				Text:      "832 [queued] fix: operator proposal 832",
			},
			{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": task768Wake1ReportFixture},
				Text:      "Comment recorded.",
			},
		},
	}

	telemetry := runTelemetryAt(result, now)
	if err := store.RecordRunTelemetry(ctx, "768-1", telemetry); err != nil {
		t.Fatalf("RecordRunTelemetry: %v", err)
	}

	checkAndRecordStewardReport(ctx, store, steward, "768-1", result, now)

	stored, found, err := store.GetRunReportCheck(ctx, "768-1")
	if err != nil || !found {
		t.Fatalf("GetRunReportCheck: found=%v err=%v", found, err)
	}
	if stored.Mismatched < 4 || stored.Unread < 3 {
		t.Errorf("stored report check = %+v, want Mismatched >= 4 and Unread >= 3", *stored)
	}

	open, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true, Limit: 100})
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	var sawMismatch, sawRegression bool
	for _, n := range open {
		switch n.Subject {
		case model.SubjectStewardReportMismatch:
			sawMismatch = true
			if !strings.Contains(n.Body, "#832") || !strings.Contains(n.Body, "port task") {
				t.Errorf("mismatch notification body = %q, want offending #832 port task line", n.Body)
			}
		case model.SubjectStewardRegression:
			sawRegression = true
			// Ensure pipeline.steward-regression does not self-wake the steward.
			woke, err := store.WakeNotificationSubscribers(ctx, n, now)
			if err != nil {
				t.Fatalf("WakeNotificationSubscribers: %v", err)
			}
			if len(woke) != 0 {
				t.Errorf("WakeNotificationSubscribers woke %v on %s, want none", woke, n.Subject)
			}
		}
	}
	if !sawMismatch {
		t.Errorf("expected %s notification to be raised, got %+v", model.SubjectStewardReportMismatch, open)
	}
	if !sawRegression {
		t.Errorf("expected %s notification to be raised when 7d rate > 5%%, got %+v", model.SubjectStewardRegression, open)
	}
}

func TestStewardAuditScheduleAndReportRecording(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)

	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

	// Verify 1-in-10 sampling.
	var wakes []string
	for i := 1; i <= 25; i++ {
		wakes = append(wakes, fmt.Sprintf("768-%d", i))
	}
	sampled := SampleStewardWakesForAudit(wakes, model.StewardAuditSampleEvery)
	wantSampled := []string{"768-1", "768-11", "768-21"}
	if !slices.Equal(sampled, wantSampled) {
		t.Errorf("SampleStewardWakesForAudit = %v, want %v", sampled, wantSampled)
	}

	// Verify weekly schedule recurrence and judgement model on system:steward-audit.
	tmpl := model.SystemTemplate(model.StewardAuditTemplateID, model.RepoRef{Owner: "acme", Name: "state"})
	if tmpl == nil {
		t.Fatalf("SystemTemplate(%q) = nil", model.StewardAuditTemplateID)
	}
	if tmpl.Model != model.JudgementModelName {
		t.Errorf("steward-audit template model = %q, want %q", tmpl.Model, model.JudgementModelName)
	}
	rec := model.SystemScheduleRecurrence(model.StewardAuditTemplateID, 180)
	if rec.Kind != model.RecurrenceWeekly {
		t.Errorf("steward-audit recurrence = %+v, want weekly", rec)
	}

	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	auditTask := model.Task{
		ID:         "900",
		Intent:     model.IntentImplement,
		Title:      "Audit sampled pipeline steward reports",
		Body:       "audit",
		TemplateID: model.StewardAuditTemplateID,
		Origin:     model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval:   &model.Attribution{Actor: human},
		Binding:    model.BindingDirective,
		CreatedAt:  &now,
		ApprovedAt: &now,
	}
	if err := store.PutTask(ctx, auditTask); err != nil {
		t.Fatalf("PutTask audit: %v", err)
	}
	auditComment := "Audited 2 sampled wakes:\n\n```\n[run 768-1] claims=6 uncited=0 unread=1 mismatched=2\n[run 768-11] claims=4 uncited=0 unread=0 mismatched=0\n```\n"
	auditResult := &agent.Result{
		ToolCalls: []agent.ToolCall{
			{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": auditComment},
				Text:      "Comment recorded.",
			},
		},
	}

	checkAndRecordStewardReport(ctx, store, auditTask, "900-1", auditResult, now)

	c1, ok, err := store.GetRunReportCheck(ctx, "768-1")
	if err != nil || !ok {
		t.Fatalf("GetRunReportCheck(768-1): ok=%v err=%v", ok, err)
	}
	if c1.Claims != 6 || c1.Unread != 1 || c1.Mismatched != 2 {
		t.Errorf("768-1 report check = %+v, want claims=6 unread=1 mismatched=2", *c1)
	}
	c11, ok, err := store.GetRunReportCheck(ctx, "768-11")
	if err != nil || !ok {
		t.Fatalf("GetRunReportCheck(768-11): ok=%v err=%v", ok, err)
	}
	if c11.Claims != 4 || c11.Mismatched != 0 {
		t.Errorf("768-11 report check = %+v, want claims=4 mismatched=0", *c11)
	}

	// An audit report must not lower or erase deterministic findings already recorded for a wake.
	zeroedAudit := &agent.Result{
		ToolCalls: []agent.ToolCall{
			{
				Name:      "comment_on_issue",
				Arguments: map[string]any{"comment": "[run 768-1] claims=2 uncited=0 unread=0 mismatched=0"},
				Text:      "Comment recorded.",
			},
		},
	}
	checkAndRecordStewardReport(ctx, store, auditTask, "900-2", zeroedAudit, now)
	c1After, ok, err := store.GetRunReportCheck(ctx, "768-1")
	if err != nil || !ok {
		t.Fatalf("GetRunReportCheck(768-1) after zeroed audit: ok=%v err=%v", ok, err)
	}
	if c1After.Claims != 6 || c1After.Unread != 1 || c1After.Mismatched != 2 {
		t.Errorf("768-1 report check after zeroed audit = %+v, want deterministic floor preserved (claims=6 unread=1 mismatched=2)", *c1After)
	}
}

func TestStewardReportCheckFlagsZeroCallAndErroredReadWakes(t *testing.T) {
	t.Parallel()
	tasks := map[string]StewardCheckTask{
		"101": {ID: "101", State: model.StateQueued, Kinds: []string{model.TaskKindPort}},
	}
	// A wake with no API calls at all that cites #101 must still flag #101 as unread.
	zeroRes := stewardReportCheck("768-4", "- Port task #101 is queued.", nil, tasks)
	if zeroRes.Check.Claims != 1 || zeroRes.Check.Unread != 1 {
		t.Errorf("zero-call wake check = %+v, want Claims=1 Unread=1", zeroRes.Check)
	}

	// An errored read_grain_task call must not record TargetIDs as read.
	telemetry := runTelemetry(&agent.Result{
		ToolCalls: []agent.ToolCall{
			{
				Name:      "read_grain_task",
				Arguments: map[string]any{"task_id": "101"},
				Text:      "task not found",
				IsError:   true,
			},
		},
	})
	if len(telemetry.APICalls) != 1 || telemetry.APICalls[0].TargetIDs != "" {
		t.Fatalf("errored read_grain_task APICalls = %+v, want empty TargetIDs", telemetry.APICalls)
	}
	errRes := stewardReportCheck("768-5", "- Port task #101 is queued.", telemetry.APICalls, tasks)
	if errRes.Check.Claims != 1 || errRes.Check.Unread != 1 {
		t.Errorf("errored-read wake check = %+v, want Claims=1 Unread=1", errRes.Check)
	}
}

func TestRunAPICallPrunedAfter30Days(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)

	now := time.Now().UTC()
	oldAt := now.Add(-31 * 24 * time.Hour)
	recentAt := now.Add(-2 * 24 * time.Hour)

	if err := store.RecordRunAPICalls(ctx, "old-1", []model.RunAPICall{
		{Seq: 1, Tool: "read_grain_task", TargetIDs: "10", ResultBytes: 500, At: oldAt},
	}); err != nil {
		t.Fatalf("RecordRunAPICalls old: %v", err)
	}
	if err := store.RecordRunAPICalls(ctx, "recent-1", []model.RunAPICall{
		{Seq: 1, Tool: "read_grain_task", TargetIDs: "20", ResultBytes: 600, At: recentAt},
	}); err != nil {
		t.Fatalf("RecordRunAPICalls recent: %v", err)
	}

	calls, err := store.RunAPICalls(ctx)
	if err != nil {
		t.Fatalf("RunAPICalls: %v", err)
	}
	if len(calls) != 1 || calls[0].RunID != "recent-1" {
		t.Fatalf("RunAPICalls after 30d prune = %+v, want only recent-1", calls)
	}
}

func TestStewardProcessResultRelaysAndChecksFiledCommentOnIssueOverFinalText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)

	now := time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	steward := model.Task{
		ID:         "2117",
		Intent:     model.IntentImplement,
		Title:      "Pipeline steward",
		Body:       "steward",
		TemplateID: model.PipelineStewardTemplateID,
		Tags:       []string{model.ScheduleFiringTag(model.PipelineStewardTemplateID)},
		Origin:     model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval:   &model.Attribution{Actor: human},
		Binding:    model.BindingDirective,
		CreatedAt:  &now,
		ApprovedAt: &now,
		Admission:  model.AdmissionDurable,
	}
	if err := store.PutTask(ctx, steward); err != nil {
		t.Fatalf("PutTask steward: %v", err)
	}
	if err := store.SetTaskGoal(ctx, steward.ID, *model.PipelineStewardGoal(), "test", now); err != nil {
		t.Fatalf("SetTaskGoal: %v", err)
	}
	for _, id := range []string{"832", "979", "1245"} {
		if err := store.PutTask(ctx, model.Task{
			ID: id, Intent: model.IntentImplement, Title: "fix: task " + id, Body: "body",
			Origin:     model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Approval:   &model.Attribution{Actor: human},
			Binding:    model.BindingDirective,
			CreatedAt:  &now,
			ApprovedAt: &now,
		}); err != nil {
			t.Fatalf("PutTask %s: %v", id, err)
		}
	}

	// Wake 1 (citation-dropping pattern): comment_on_issue carries per-line
	// [task N] and [ci run ...] citations; FinalText is a longer free-text
	// summary that drops the run IDs.
	run1 := "2117-56"
	if err := store.StartRun(ctx, model.Run{
		ID: run1, TaskID: steward.ID, Sandbox: "sbx-1", Attempt: 56, StartedAt: now.Add(-2 * time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun %s: %v", run1, err)
	}
	if err := store.FinishRun(ctx, run1, now, "succeeded", "ok"); err != nil {
		t.Fatalf("FinishRun %s: %v", run1, err)
	}
	filedCited := strings.Join([]string{
		"- Task 832 remains queued while its branch checks run [task 832].",
		"- Main grain CI passed on the latest trunk merge [ci run 5f7d9ba82a6aa836].",
		"- Pull request grain CI passed all required jobs [ci run 7497fb8bd8e2374b].",
	}, "\n")
	res1 := &agent.Result{
		FinalText: "I reviewed the pipeline delta feed and updated the handover cursor. Task 832 is queued while checks run, and both the latest trunk CI run and pull request CI run succeeded cleanly with no steward action required.",
		ToolCalls: []agent.ToolCall{
			{Name: "read_grain_task", Arguments: map[string]any{"task_id": "832"}, Text: "832 [queued]"},
			{Name: "comment_on_issue", Arguments: map[string]any{"comment": filedCited}, Text: "Recorded."},
		},
	}
	if err := store.RecordRunTelemetry(ctx, run1, runTelemetryAt(res1, now)); err != nil {
		t.Fatalf("RecordRunTelemetry %s: %v", run1, err)
	}
	if err := processResult(ctx, store, nil, nil, nil, steward, res1, run1, now); err != nil {
		t.Fatalf("processResult %s: %v", run1, err)
	}
	comments, err := store.Comments(ctx, steward.ID)
	if err != nil || len(comments) != 1 {
		t.Fatalf("Comments after %s = %+v, err=%v; want 1 comment", run1, comments, err)
	}
	if comments[0].Body != filedCited {
		t.Errorf("posted comment = %q, want filed comment_on_issue %q", comments[0].Body, filedCited)
	}
	check1, ok, err := store.GetRunReportCheck(ctx, run1)
	if err != nil || !ok {
		t.Fatalf("GetRunReportCheck(%s): ok=%v err=%v", run1, ok, err)
	}
	if check1.Claims != 3 || check1.Uncited != 0 || check1.Unread != 0 || check1.Mismatched != 0 {
		t.Errorf("check1 = %+v, want Claims=3 Uncited=0 Unread=0 Mismatched=0", *check1)
	}

	// Wake 2 (extra-claims pattern): comment_on_issue files a single "Nothing
	// needed doing" line; FinalText adds status claims about unread tasks.
	now2 := now.Add(10 * time.Minute)
	if _, err := store.WakeRestingTask(ctx, steward.ID, "wake", now2); err != nil {
		t.Fatalf("WakeRestingTask: %v", err)
	}
	run2 := "2117-57"
	if err := store.StartRun(ctx, model.Run{
		ID: run2, TaskID: steward.ID, Sandbox: "sbx-2", Attempt: 57, StartedAt: now2.Add(-time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun %s: %v", run2, err)
	}
	if err := store.FinishRun(ctx, run2, now2, "succeeded", "ok"); err != nil {
		t.Fatalf("FinishRun %s: %v", run2, err)
	}
	filedNoop := "Nothing needed doing: the pipeline has not changed since 17:46Z."
	res2 := &agent.Result{
		FinalText: "Nothing changed since 17:46Z. Tasks #1245 and #979 are still queued and the main CI run is green.",
		ToolCalls: []agent.ToolCall{
			{Name: "comment_on_issue", Arguments: map[string]any{"comment": filedNoop}, Text: "Recorded."},
		},
	}
	if err := processResult(ctx, store, nil, nil, nil, steward, res2, run2, now2); err != nil {
		t.Fatalf("processResult %s: %v", run2, err)
	}
	comments, err = store.Comments(ctx, steward.ID)
	if err != nil || len(comments) == 0 {
		t.Fatalf("Comments after %s = %+v, err=%v", run2, comments, err)
	}
	if got := comments[len(comments)-1].Body; got != filedNoop {
		t.Errorf("posted comment = %q, want filed comment_on_issue %q", got, filedNoop)
	}
	check2, ok, err := store.GetRunReportCheck(ctx, run2)
	if err != nil || !ok {
		t.Fatalf("GetRunReportCheck(%s): ok=%v err=%v", run2, ok, err)
	}
	if check2.Claims != 0 || check2.Uncited != 0 || check2.Unread != 0 || check2.Mismatched != 0 {
		t.Errorf("check2 = %+v, want zero claims/mismatches", *check2)
	}

	// Wake 3 (plain FinalText fallback with no comment_on_issue call): the
	// fallback comment posted onto the goal task is also checked by
	// stewardReportCheck so an omitted comment_on_issue cannot bypass it.
	now3 := now2.Add(10 * time.Minute)
	if _, err := store.WakeRestingTask(ctx, steward.ID, "wake", now3); err != nil {
		t.Fatalf("WakeRestingTask 3: %v", err)
	}
	run3 := "2117-58"
	if err := store.StartRun(ctx, model.Run{
		ID: run3, TaskID: steward.ID, Sandbox: "sbx-3", Attempt: 58, StartedAt: now3.Add(-time.Minute),
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun %s: %v", run3, err)
	}
	if err := store.FinishRun(ctx, run3, now3, "succeeded", "ok"); err != nil {
		t.Fatalf("FinishRun %s: %v", run3, err)
	}
	res3 := &agent.Result{
		FinalText: "- Task #979 is queued.",
	}
	if err := processResult(ctx, store, nil, nil, nil, steward, res3, run3, now3); err != nil {
		t.Fatalf("processResult %s: %v", run3, err)
	}
	check3, ok, err := store.GetRunReportCheck(ctx, run3)
	if err != nil || !ok {
		t.Fatalf("GetRunReportCheck(%s): ok=%v err=%v", run3, ok, err)
	}
	if check3.Claims != 1 || check3.Unread != 1 {
		t.Errorf("check3 = %+v, want Claims=1 Unread=1 on unread fallback FinalText", *check3)
	}
}

// A steward reads through the in-sandbox grain CLI, and those reads are
// stored after the run's MCP calls -- comment_on_issue included -- so they
// must still count (model.Store.mergeWithProxyAPICalls).
func TestStewardReportCheckCountsCLIReadsStoredAfterTheClosingReport(t *testing.T) {
	t.Parallel()

	tasks := map[string]StewardCheckTask{
		"900": {ID: "900", State: model.StateQueued},
		"901": {ID: "901", State: model.StateRunning},
	}
	calls := []model.RunAPICall{
		{RunID: "r", Seq: 7, Tool: "comment_on_issue"},
		{RunID: "r", Seq: 8, Tool: "read_pipeline_changes", TargetIDs: "900"},
		{RunID: "r", Seq: 9, Tool: "read_grain_task", TargetIDs: "901"},
	}
	report := "- Task 900 is queued behind the dispatch pause [task 900]\n- Task 901 is running [task 901]"
	res := stewardReportCheck("r", report, calls, tasks)
	if res.Check.Unread != 0 || res.Check.Mismatched != 0 || res.Check.Claims != 2 {
		t.Fatalf("check = %+v (unread %v, mismatched %v), want 2 claims and nothing unread or mismatched",
			res.Check, res.UnreadIDs, res.MismatchLines)
	}
}

// Claims the steward could not ground this wake go under "Unverified" and
// are not counted; claims after the section ends are checked again.
func TestStewardReportCheckSkipsTheUnverifiedSection(t *testing.T) {
	t.Parallel()

	tasks := map[string]StewardCheckTask{
		"910": {ID: "910", State: model.StateQueued},
		"911": {ID: "911", State: model.StateQueued},
		"912": {ID: "912", State: model.StateQueued},
	}
	calls := []model.RunAPICall{{RunID: "r", Seq: 1, Tool: "read_grain_task", TargetIDs: "910"}}
	for _, heading := range []string{"## Unverified", "**Unverified:**", "### Unverified (not re-read this wake)"} {
		report := "- Task 910 is queued [task 910]\n" + heading +
			"\n- Task 911 may be stalled; handover says so [task 911]\n## Next\n- Task 912 is running [task 912]"
		res := stewardReportCheck("r", report, calls, tasks)
		if res.Check.Claims != 2 {
			t.Errorf("%q: Claims = %d, want 2 (910 and 912; 911 is unverified)", heading, res.Check.Claims)
		}
		if !slices.Equal(res.UnreadIDs, []string{"912"}) {
			t.Errorf("%q: UnreadIDs = %v, want [912]", heading, res.UnreadIDs)
		}
		if res.Check.Mismatched != 1 {
			t.Errorf("%q: Mismatched = %d, want 1 (912 is queued, not running)", heading, res.Check.Mismatched)
		}
	}
}

// A transition's source state and a task's CI failing are not claims about
// the state the task is in; the state it is said to be in still is.
func TestStewardReportCheckReadsOnlyTheClaimedState(t *testing.T) {
	t.Parallel()

	tasks := map[string]StewardCheckTask{
		"920": {ID: "920", State: model.StateRunning},
		"921": {ID: "921", State: model.StateAwaitingSubmit},
	}
	calls := []model.RunAPICall{{RunID: "r", Seq: 1, Tool: "read_grain_task", TargetIDs: "920,921"}}
	for _, line := range []string{
		"- Task 920 moved queued→running [task 920]",
		"- Task 920 went from queued to running [task 920]",
		"- Task 921's CI failed on go unit; re-run is in hand [task 921]",
		"- go e2e checks failed on task 921, a known flake [task 921]",
	} {
		if res := stewardReportCheck("r", line, calls, tasks); res.Check.Mismatched != 0 {
			t.Errorf("%q: Mismatched = %d, want 0", line, res.Check.Mismatched)
		}
	}
	for _, line := range []string{
		"- Task 920 moved running→queued [task 920]",
		"- Task 921 failed and needs a retry [task 921]",
	} {
		if res := stewardReportCheck("r", line, calls, tasks); res.Check.Mismatched != 1 {
			t.Errorf("%q: Mismatched = %d, want 1", line, res.Check.Mismatched)
		}
	}
}
