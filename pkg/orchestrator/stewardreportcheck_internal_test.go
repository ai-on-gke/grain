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
		Filing:     model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
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
			Filing:     model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
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
		Filing:     model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
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
		Filing:     model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
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
			Filing:     model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
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

// grain report check sees the reads the grain CLI made earlier in the
// same run, and fails a draft citing a task the run never read.
func TestCheckStewardReportDraftUsesTheRunsCLIReadsSoFar(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)

	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	for _, id := range []string{"768", "832", "833"} {
		task := model.Task{
			ID:         id,
			Intent:     model.IntentImplement,
			Title:      "task " + id,
			Body:       "body",
			Filing:     model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Approval:   &model.Attribution{Actor: human},
			Binding:    model.BindingDirective,
			CreatedAt:  &now,
			ApprovedAt: &now,
		}
		if id == "768" {
			task.TemplateID = model.PipelineStewardTemplateID
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("PutTask %s: %v", id, err)
		}
	}
	if err := store.RecordProxyAPICall(ctx, "768-3", "read_pipeline_changes", "832", 512); err != nil {
		t.Fatalf("RecordProxyAPICall: %v", err)
	}

	text, failed, err := CheckStewardReportDraft(ctx, store, "768", "768-3", "- Task 832 needs a person [task 832]")
	if err != nil || failed {
		t.Fatalf("draft citing a read task: failed=%v err=%v\n%s", failed, err, text)
	}
	text, failed, err = CheckStewardReportDraft(ctx, store, "768", "768-3",
		"- Task 832 needs a person [task 832]\n- Task 833 needs a person [task 833]")
	if err != nil || !failed {
		t.Fatalf("draft citing an unread task: failed=%v err=%v\n%s", failed, err, text)
	}
	if !strings.Contains(text, "Cited but not read in this run: 833.") || !strings.Contains(text, "## Unverified") {
		t.Errorf("failing draft's text does not name 833 and the Unverified heading:\n%s", text)
	}
}

func TestStewardAuditQuietSkipsFiringWhenNoStewardWakesRan(t *testing.T) {
	t.Parallel()
	store, ctx := recreateStore(t)
	deps := systemScheduleDeps(store)
	seeded := time.Date(2026, 11, 13, 16, 4, 46, 0, time.UTC)

	if err := reconcileSystemSchedules(ctx, deps, seeded); err != nil {
		t.Fatalf("reconcileSystemSchedules: %v", err)
	}
	sched, err := store.GetSchedule(ctx, model.StewardAuditTemplateID)
	if err != nil || sched == nil {
		t.Fatalf("GetSchedule(%s): %v %v", model.StewardAuditTemplateID, sched, err)
	}
	due1 := sched.NextRunAt

	// Week 1: no pipeline steward task exists at all -> firing is dropped as quiet.
	if err := reconcileSchedule(ctx, deps, due1); err != nil {
		t.Fatalf("reconcileSchedule week 1: %v", err)
	}
	auditTask, err := store.OpenTaskWithTag(ctx, model.ScheduleFiringTag(model.StewardAuditTemplateID))
	if err != nil {
		t.Fatalf("OpenTaskWithTag: %v", err)
	}
	if auditTask != nil {
		t.Fatalf("quiet week 1 filed steward audit task %s, want none", auditTask.ID)
	}
	after1, err := store.GetSchedule(ctx, model.StewardAuditTemplateID)
	if err != nil || after1 == nil {
		t.Fatalf("GetSchedule after week 1: %v %v", after1, err)
	}
	if !after1.NextRunAt.After(due1) {
		t.Errorf("NextRunAt = %s, want advanced past %s", after1.NextRunAt, due1)
	}
	if after1.LastRunAt != nil {
		t.Errorf("LastRunAt = %v, want nil when skipped quiet", after1.LastRunAt)
	}
	skips, err := store.ScheduleSkips(ctx, model.StewardAuditTemplateID)
	if err != nil {
		t.Fatalf("ScheduleSkips: %v", err)
	}
	if len(skips) != 1 || skips[0].Reason != model.SkippedQuiet || skips[0].Outcome != model.ScheduleSkipDropped {
		t.Errorf("skips = %+v, want 1 dropped SkippedQuiet record", skips)
	}

	// Week 2: a steward wake older than 7 days and an interrupted steward run
	// inside the window still leave 0 completed wakes to sample -> skipped again.
	due2 := after1.NextRunAt
	oldWake := due2.Add(-8 * 24 * time.Hour)
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	steward := model.Task{
		ID:         "768",
		Intent:     model.IntentImplement,
		Title:      "Pipeline steward",
		Body:       "steward",
		TemplateID: model.PipelineStewardTemplateID,
		Tags:       []string{model.ScheduleFiringTag(model.PipelineStewardTemplateID)},
		Filing:     model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonSchedule},
		Approval:   &model.Attribution{Actor: human},
		Binding:    model.BindingDirective,
		CreatedAt:  &oldWake,
		ApprovedAt: &oldWake,
	}
	if err := store.PutTask(ctx, steward); err != nil {
		t.Fatalf("PutTask steward: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "768-1", TaskID: steward.ID, Sandbox: "sbx-1", Attempt: 1, StartedAt: oldWake.Add(-time.Minute)}, model.Limits{}); err != nil {
		t.Fatalf("StartRun 768-1: %v", err)
	}
	if err := store.FinishRun(ctx, "768-1", oldWake, "succeeded", "ok"); err != nil {
		t.Fatalf("FinishRun 768-1: %v", err)
	}
	interruptedAt := due2.Add(-2 * time.Hour)
	if err := store.StartRun(ctx, model.Run{ID: "768-2", TaskID: steward.ID, Sandbox: "sbx-2", Attempt: 2, StartedAt: interruptedAt.Add(-time.Minute)}, model.Limits{}); err != nil {
		t.Fatalf("StartRun 768-2: %v", err)
	}
	if err := store.FinishRun(ctx, "768-2", interruptedAt, model.InterruptedOutcome, "daemon restart"); err != nil {
		t.Fatalf("FinishRun 768-2: %v", err)
	}

	if err := reconcileSchedule(ctx, deps, due2); err != nil {
		t.Fatalf("reconcileSchedule week 2: %v", err)
	}
	if auditTask, _ := store.OpenTaskWithTag(ctx, model.ScheduleFiringTag(model.StewardAuditTemplateID)); auditTask != nil {
		t.Fatalf("week 2 with only stale/interrupted wakes filed steward audit task %s, want none", auditTask.ID)
	}

	// Week 3: a steward wake finishes inside the 7-day window -> audit fires!
	after2, _ := store.GetSchedule(ctx, model.StewardAuditTemplateID)
	due3 := after2.NextRunAt
	recentWake := due3.Add(-24 * time.Hour)
	if err := store.StartRun(ctx, model.Run{ID: "768-3", TaskID: steward.ID, Sandbox: "sbx-3", Attempt: 3, StartedAt: recentWake.Add(-2 * time.Minute)}, model.Limits{}); err != nil {
		t.Fatalf("StartRun 768-3: %v", err)
	}
	if err := store.FinishRun(ctx, "768-3", recentWake, "succeeded", "ok"); err != nil {
		t.Fatalf("FinishRun 768-3: %v", err)
	}
	if err := reconcileSchedule(ctx, deps, due3); err != nil {
		t.Fatalf("reconcileSchedule week 3: %v", err)
	}
	auditTask, err = store.OpenTaskWithTag(ctx, model.ScheduleFiringTag(model.StewardAuditTemplateID))
	if err != nil || auditTask == nil {
		t.Fatalf("week 3 with a recent steward wake did not file steward audit (err=%v)", err)
	}
}

func TestStewardAuditProcessResultClosesInactionableAuditsAndRecordsCounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	now := time.Date(2026, 11, 23, 3, 5, 0, 0, time.UTC)

	newAuditTask := func(id string) (model.Task, string) {
		t.Helper()
		task := model.Task{
			ID:         id,
			Intent:     model.IntentImplement,
			Title:      "Audit sampled pipeline steward reports",
			Body:       "audit",
			TemplateID: model.StewardAuditTemplateID,
			Tags:       []string{model.ScheduleFiringTag(model.StewardAuditTemplateID)},
			Filing:     model.Filing{Attribution: model.Attribution{Actor: model.SchedulePrincipal}, Reason: model.ReasonSchedule},
			Approval:   &model.Attribution{Actor: model.SchedulePrincipal},
			Binding:    model.BindingDirective,
			CreatedAt:  &now,
			ApprovedAt: &now,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("PutTask %s: %v", id, err)
		}
		runID := id + "-1"
		if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: id, Sandbox: "sbx-" + id, Attempt: 1, StartedAt: now.Add(-time.Minute)}, model.Limits{}); err != nil {
			t.Fatalf("StartRun %s: %v", runID, err)
		}
		if err := store.FinishRun(ctx, runID, now, "succeeded", "ok"); err != nil {
			t.Fatalf("FinishRun %s: %v", runID, err)
		}
		return task, runID
	}

	// 1. Clean audit ending on nothing_needed: records [run ...] counts in
	// task_run_report_check and closes the task without entering awaiting_reply.
	t1, r1 := newAuditTask("950")
	res1 := &agent.Result{
		ToolCalls: []agent.ToolCall{{
			Name:      "nothing_needed",
			Arguments: map[string]any{"reason": "Audited 1 wake:\n\n```\n[run 768-1] claims=5 uncited=0 unread=0 mismatched=0\n```\n"},
		}},
	}
	if err := processResult(ctx, store, nil, nil, nil, t1, res1, r1, now); err != nil {
		t.Fatalf("processResult t1: %v", err)
	}
	if st, err := store.State(ctx, t1.ID); err != nil || st != model.StateClosed {
		t.Errorf("clean nothing_needed audit state = %q (%v), want %q", st, err, model.StateClosed)
	}
	if c, ok, err := store.GetRunReportCheck(ctx, "768-1"); err != nil || !ok || c.Claims != 5 || c.Mismatched != 0 {
		t.Errorf("GetRunReportCheck(768-1) = %+v, ok=%v, err=%v; want Claims=5 Mismatched=0", c, ok, err)
	}

	// 2. Empty audit ("nothing to sample") where the agent called comment_on_issue
	// anyway (task #46 reproduction): closes automatically instead of awaiting_reply.
	t2, r2 := newAuditTask("951")
	res2 := &agent.Result{
		ToolCalls: []agent.ToolCall{{
			Name: "comment_on_issue",
			Arguments: map[string]any{
				"summary": "Steward audit, week to 2026-11-23: no steward wakes ran, nothing to sample",
				"comment": "Steward audit, week ending 2026-11-23: nothing to sample\nThe pipeline steward did not wake at all in the past 7 days.",
			},
		}},
	}
	if err := processResult(ctx, store, nil, nil, nil, t2, res2, r2, now); err != nil {
		t.Fatalf("processResult t2: %v", err)
	}
	if st, err := store.State(ctx, t2.ID); err != nil || st != model.StateClosed {
		t.Errorf("empty comment_on_issue audit state = %q (%v), want %q", st, err, model.StateClosed)
	}

	// 3. Clean audit where the agent called comment_on_issue with 0 mismatches:
	// records counts and still closes automatically instead of awaiting_reply.
	t3, r3 := newAuditTask("952")
	res3 := &agent.Result{
		ToolCalls: []agent.ToolCall{{
			Name:      "comment_on_issue",
			Arguments: map[string]any{"comment": "[run 768-2] claims=4 uncited=0 unread=0 mismatched=0"},
		}},
	}
	if err := processResult(ctx, store, nil, nil, nil, t3, res3, r3, now); err != nil {
		t.Fatalf("processResult t3: %v", err)
	}
	if st, err := store.State(ctx, t3.ID); err != nil || st != model.StateClosed {
		t.Errorf("clean comment_on_issue audit state = %q (%v), want %q", st, err, model.StateClosed)
	}
	if c, ok, err := store.GetRunReportCheck(ctx, "768-2"); err != nil || !ok || c.Claims != 4 {
		t.Errorf("GetRunReportCheck(768-2) = %+v, ok=%v, err=%v; want Claims=4", c, ok, err)
	}

	// 4. Audit with mismatches using comment_on_issue: stays in awaiting_reply
	// so the operator sees the actionable report.
	t4, r4 := newAuditTask("953")
	res4 := &agent.Result{
		ToolCalls: []agent.ToolCall{{
			Name: "comment_on_issue",
			Arguments: map[string]any{
				"summary": "Steward audit found 2 mismatched claims on wake 768-3",
				"comment": "[run 768-3] claims=6 uncited=1 unread=0 mismatched=2\n- Claimed #832 was a port task.",
			},
		}},
	}
	if err := processResult(ctx, store, nil, nil, nil, t4, res4, r4, now); err != nil {
		t.Fatalf("processResult t4: %v", err)
	}
	if st, err := store.State(ctx, t4.ID); err != nil || st != model.StateAwaitingReply {
		t.Errorf("mismatched comment_on_issue audit state = %q (%v), want %q", st, err, model.StateAwaitingReply)
	}
	if c, ok, err := store.GetRunReportCheck(ctx, "768-3"); err != nil || !ok || c.Mismatched != 2 || c.Uncited != 1 {
		t.Errorf("GetRunReportCheck(768-3) = %+v, ok=%v, err=%v; want Mismatched=2 Uncited=1", c, ok, err)
	}

	// 5. Audit on a quiet cluster whose comment_on_issue reports an API/grant
	// failure rather than "nothing to sample": stays in awaiting_reply so the
	// operator sees the broken audit rather than having it silently closed.
	t5, r5 := newAuditTask("954")
	res5 := &agent.Result{
		ToolCalls: []agent.ToolCall{{
			Name: "comment_on_issue",
			Arguments: map[string]any{
				"summary": "Steward audit failed: self-debug task list returned 403",
				"comment": "Cannot run steward audit: `grain task list` failed with 403 forbidden.",
			},
		}},
	}
	if err := processResult(ctx, store, nil, nil, nil, t5, res5, r5, now); err != nil {
		t.Fatalf("processResult t5: %v", err)
	}
	if st, err := store.State(ctx, t5.ID); err != nil || st != model.StateAwaitingReply {
		t.Errorf("error comment_on_issue audit state = %q (%v), want %q", st, err, model.StateAwaitingReply)
	}

	// 6. Audit where the agent mistakenly called nothing_needed even though its
	// [run ...] summary line reported non-zero mismatches (below the 5%
	// regression threshold): records counts and still stays in awaiting_reply.
	t6, r6 := newAuditTask("955")
	res6 := &agent.Result{
		ToolCalls: []agent.ToolCall{{
			Name:      "nothing_needed",
			Arguments: map[string]any{"reason": "[run 768-4] claims=25 uncited=0 unread=0 mismatched=1\n- Claimed #832 was closed."},
		}},
	}
	if err := processResult(ctx, store, nil, nil, nil, t6, res6, r6, now); err != nil {
		t.Fatalf("processResult t6: %v", err)
	}
	if st, err := store.State(ctx, t6.ID); err != nil || st != model.StateAwaitingReply {
		t.Errorf("mismatched nothing_needed audit state = %q (%v), want %q", st, err, model.StateAwaitingReply)
	}
	if c, ok, err := store.GetRunReportCheck(ctx, "768-4"); err != nil || !ok || c.Claims != 25 || c.Mismatched != 1 {
		t.Errorf("GetRunReportCheck(768-4) = %+v, ok=%v, err=%v; want Claims=25 Mismatched=1", c, ok, err)
	}

	// 7. Audit with one clean [run ...] line and a second unparsed [run ...]
	// line: stays in awaiting_reply rather than silently dropping the unparsed wake.
	t7, r7 := newAuditTask("956")
	res7 := &agent.Result{
		ToolCalls: []agent.ToolCall{{
			Name: "comment_on_issue",
			Arguments: map[string]any{
				"comment": "[run 768-5] claims=4 uncited=0 unread=0 mismatched=0\n[run 768-6] mismatched=2 (missing claims field)",
			},
		}},
	}
	if err := processResult(ctx, store, nil, nil, nil, t7, res7, r7, now); err != nil {
		t.Fatalf("processResult t7: %v", err)
	}
	if st, err := store.State(ctx, t7.ID); err != nil || st != model.StateAwaitingReply {
		t.Errorf("partially unparsed comment_on_issue audit state = %q (%v), want %q", st, err, model.StateAwaitingReply)
	}

	// 8. Once a pipeline-steward wake has completed in the 7-day window, a
	// comment_on_issue with zero [run ...] lines (even if it mentions "did not
	// wake" or prose findings) is never auto-closed as an empty audit.
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	steward := model.Task{
		ID:         "768",
		Intent:     model.IntentImplement,
		Title:      "Pipeline steward",
		Body:       "steward",
		TemplateID: model.PipelineStewardTemplateID,
		Tags:       []string{model.ScheduleFiringTag(model.PipelineStewardTemplateID)},
		Filing:     model.Filing{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonSchedule},
		Approval:   &model.Attribution{Actor: human},
		Binding:    model.BindingDirective,
		CreatedAt:  &now,
		ApprovedAt: &now,
	}
	if err := store.PutTask(ctx, steward); err != nil {
		t.Fatalf("PutTask steward: %v", err)
	}
	wakeAt := now.Add(-2 * time.Hour)
	if err := store.StartRun(ctx, model.Run{ID: "768-7", TaskID: steward.ID, Sandbox: "sbx-st", Attempt: 1, StartedAt: wakeAt.Add(-time.Minute)}, model.Limits{}); err != nil {
		t.Fatalf("StartRun 768-7: %v", err)
	}
	if err := store.FinishRun(ctx, "768-7", wakeAt, "succeeded", "ok"); err != nil {
		t.Fatalf("FinishRun 768-7: %v", err)
	}
	t8, r8 := newAuditTask("957")
	res8 := &agent.Result{
		ToolCalls: []agent.ToolCall{{
			Name: "comment_on_issue",
			Arguments: map[string]any{
				"summary": "Steward audit found ungrounded claim in prose without summary block",
				"comment": "Wake 768-7 claimed task #832 was a port task, which contradicts the record.",
			},
		}},
	}
	if err := processResult(ctx, store, nil, nil, nil, t8, res8, r8, now); err != nil {
		t.Fatalf("processResult t8: %v", err)
	}
	if st, err := store.State(ctx, t8.ID); err != nil || st != model.StateAwaitingReply {
		t.Errorf("prose-only comment_on_issue when wakes exist state = %q (%v), want %q", st, err, model.StateAwaitingReply)
	}
}
