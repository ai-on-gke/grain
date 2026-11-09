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

package orchestrator_test

// A red release candidate's notifications and hold (releaseredrc.go),
// driven through SyncReleaseQualification against the same fake forge as
// releasequalification_test.go.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// rcTasks is every task filed for rcTag, by template, oldest first.
func rcTasks(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef, rcTag string) map[string][]string {
	t.Helper()
	runs, err := store.SuiteRunsTagged(ctx, model.ReleaseQualificationTag(repo, rcTag))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, run := range runs {
		for _, task := range run.Tasks {
			out[task.TemplateID] = append(out[task.TemplateID], task.TaskID)
		}
	}
	return out
}

// failItemTwice drives item of rcTag to a confirmed red: it completes
// with no verdict footer, is retried, and the retry does the same.
func failItemTwice(t *testing.T, ctx context.Context, store *model.Store, forge *fakeQualificationForge,
	repo model.RepoRef, rcTag, item string, at time.Time) {
	t.Helper()
	syncQualification(t, ctx, store, forge, at)
	completeTask(t, ctx, store, rcTasks(t, ctx, store, repo, rcTag)[item][0], at)
	syncQualification(t, ctx, store, forge, at)
	retries := rcTasks(t, ctx, store, repo, rcTag)[item]
	if len(retries) != 2 {
		t.Fatalf("%s of %s has attempts %v, want a retry", item, rcTag, retries)
	}
	completeTask(t, ctx, store, retries[1], at)
	syncQualification(t, ctx, store, forge, at)
}

func openNotification(t *testing.T, ctx context.Context, store *model.Store, family, key string) *model.Notification {
	t.Helper()
	n, err := store.OpenNotification(ctx, model.NotificationSubject(family, key))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// checkNamed is the newest check run named name, or nil.
func (f *fakeQualificationForge) checkNamed(name string) *github.CheckRun {
	var out *github.CheckRun
	for i := range f.checks {
		if f.checks[i].Name == name {
			out = &f.checks[i]
		}
	}
	return out
}

func (f *fakeQualificationForge) countNamed(name string) int {
	n := 0
	for _, c := range f.checks {
		if c.Name == name {
			n++
		}
	}
	return n
}

func TestARedCandidateRaisesRCFailedAndHoldsTheNextUntilItsBlockersLand(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	failItemTwice(t, ctx, store, forge, repo, rqTag, "qualification-base-triage", baseTime)

	if c := forge.checkNamed(model.ReleaseQualificationCheckName); c == nil || c.Conclusion == nil ||
		*c.Conclusion != model.ConclusionFailure {
		t.Fatalf("qualification check is %+v, want completed failure", c)
	}
	n := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag)
	if n == nil {
		t.Fatalf("no pipeline.rc-failed:%s raised", rqTag)
	}
	floor, rc1 := strings.Repeat("3", 40), strings.Repeat("1", 40)
	for _, want := range []string{
		"attempt 2 of RELEASE_MAX_RC_ATTEMPTS (3)",
		"`" + floor + ".." + rqSHA + "` (everything since v1.3.0)",
		"New since v1.4.0-rc.1: `" + rc1 + ".." + rqSHA + "`",
		"qualification-base-triage: fail",
		model.RCBlockerMarker(rqTag),
		"is in progress and holds v1.4.0-rc.3 while triage is pending (up to 1h",
	} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("rc-failed body does not say %q:\n%s", want, n.Body)
		}
	}
	if n.Repo != repo.String() || n.Severity != model.NotificationWarning {
		t.Errorf("rc-failed is %+v, want a warning on %s", n, repo)
	}
	if e := openNotification(t, ctx, store, model.FamilyReleaseEscalated, "v1.4.0"); e != nil {
		t.Fatalf("rc.2 escalated %+v; only the last attempt does", e)
	}
	// grain/rc-hold is posted in progress as soon as failure is concluded,
	// before any [rc-blocker] task exists, so the train's 20-minute tick
	// cannot cut rc.3 off the unfixed tree while triage is pending.
	hold := forge.checkNamed(model.ReleaseHoldCheckName)
	if hold == nil || hold.Status != "in_progress" || forge.countNamed(model.ReleaseHoldCheckName) != 1 {
		t.Fatalf("initial hold is %+v (%d posted), want one triage-pending check in progress",
			hold, forge.countNamed(model.ReleaseHoldCheckName))
	}
	if forge.heads[hold.ID] != rqSHA {
		t.Fatalf("hold is on %s, want the red candidate's commit %s", forge.heads[hold.ID], rqSHA)
	}
	if !strings.Contains(hold.Output, "triage pending") || !strings.Contains(hold.Output, "awaits triage") {
		t.Fatalf("initial hold output is %q, want triage-pending summary", hold.Output)
	}

	// The steward files a fix titled with the marker: the same check run is
	// updated in place to name the blocker, and stays in progress past the
	// 1h triage grace window while the blocker remains open.
	blocker, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: blocker, Title: model.RCBlockerMarker(rqTag) + " fix base triage", Body: "fix", Target: &repo,
		CreatedAt: &baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	at := baseTime.Add(time.Minute)
	syncQualification(t, ctx, store, forge, at)
	syncQualification(t, ctx, store, forge, baseTime.Add(2*model.ReleaseHoldTriageGrace))
	hold = forge.checkNamed(model.ReleaseHoldCheckName)
	if hold == nil || hold.Status != "in_progress" || forge.countNamed(model.ReleaseHoldCheckName) != 1 {
		t.Fatalf("hold is %+v (%d posted), want one in progress", hold, forge.countNamed(model.ReleaseHoldCheckName))
	}
	if !strings.Contains(hold.Output, "task "+blocker+".") {
		t.Fatalf("hold output is %q, want it updated to name task %s", hold.Output, blocker)
	}

	// A blocker that lands inside the triage window still completes the hold
	// at once, without waiting out the rest of the grace period; rc-failed
	// stays open until rc.3 is cut.
	closeTask(t, ctx, store, blocker, at.Add(2*time.Minute))
	syncQualification(t, ctx, store, forge, at.Add(2*time.Minute))
	hold = forge.checkNamed(model.ReleaseHoldCheckName)
	if hold.Status != "completed" || hold.Conclusion == nil || *hold.Conclusion != model.ConclusionSuccess ||
		!strings.Contains(hold.Output, "Blockers landed") {
		t.Fatalf("hold is %+v once its blocker landed, want completed success (Blockers landed)", hold)
	}
	if openNotification(t, ctx, store, model.FamilyRCFailed, rqTag) == nil {
		t.Fatal("rc-failed cleared before rc.3 was cut")
	}

	forge.tags = append(forge.tags,
		github.Tag{Name: rqTag + "-discarded", SHA: rqSHA},
		github.Tag{Name: "v1.4.0-rc.3", SHA: strings.Repeat("4", 40)})
	syncQualification(t, ctx, store, forge, at.Add(3*time.Minute))
	if n := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag); n != nil {
		t.Fatalf("rc-failed still open after rc.3 was cut: %+v", n)
	}
}

func TestTriagePendingHoldCompletesWhenStewardTriagesWithNoBlockerOrCleared(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	failItemTwice(t, ctx, store, forge, repo, rqTag, "qualification-base-triage", baseTime)

	stewardID, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTask(ctx, model.Task{
		ID: stewardID, Title: "Pipeline steward", Body: "steward",
		TemplateID: model.PipelineStewardTemplateID,
		Tags:       []string{model.ScheduleFiringTag(model.PipelineStewardTemplateID)},
		CreatedAt:  &baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	// Neither an unrelated steward report, nor a comment naming v1.4.0-rc.20,
	// nor a note from somebody other than the steward's own run lifts the hold.
	for _, c := range []model.Comment{
		{TaskID: stewardID, Author: ownRunAuthor(stewardID), Body: "Task 99 stalled on CI [task 99]", CreatedAt: baseTime.Add(time.Minute)},
		{TaskID: stewardID, Author: ownRunAuthor(stewardID), Body: "Checked v1.4.0-rc.20 earlier [task 99]", CreatedAt: baseTime.Add(2 * time.Minute)},
		{TaskID: stewardID, Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "op"}},
			Body: "triaged " + rqTag, CreatedAt: baseTime.Add(3 * time.Minute)},
	} {
		if _, err := store.AddComment(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	syncQualification(t, ctx, store, forge, baseTime.Add(5*time.Minute))
	if hold := forge.checkNamed(model.ReleaseHoldCheckName); hold == nil || hold.Status != "in_progress" {
		t.Fatalf("hold after unrelated comments is %+v, want still in progress", hold)
	}

	// The steward's own run triages rqTag and files no blocker: the hold completes.
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    stewardID,
		Author:    ownRunAuthor(stewardID),
		Body:      rqTag + " qualification-base-triage failed on transient runner timeout; no blocker needed [task 1]",
		CreatedAt: baseTime.Add(10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	syncQualification(t, ctx, store, forge, baseTime.Add(11*time.Minute))
	syncQualification(t, ctx, store, forge, baseTime.Add(12*time.Minute))
	hold := forge.checkNamed(model.ReleaseHoldCheckName)
	if hold == nil || hold.Status != "completed" || hold.Conclusion == nil || *hold.Conclusion != model.ConclusionSuccess ||
		!strings.Contains(hold.Output, "Triaged with no blocker") || forge.countNamed(model.ReleaseHoldCheckName) != 1 {
		t.Fatalf("hold after no-blocker triage is %+v (%d posted), want one completed success",
			hold, forge.countNamed(model.ReleaseHoldCheckName))
	}

	// A person files a late [rc-blocker] task before rc.3 is cut: a new hold
	// goes up and completes once that blocker closes.
	late, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	later := baseTime.Add(15 * time.Minute)
	if err := store.PutTask(ctx, model.Task{
		ID: late, Title: model.RCBlockerMarker(rqTag) + " revert suspect merge", Body: "revert", Target: &repo,
		CreatedAt: &later,
	}); err != nil {
		t.Fatal(err)
	}
	syncQualification(t, ctx, store, forge, later)
	hold = forge.checkNamed(model.ReleaseHoldCheckName)
	if hold == nil || hold.Status != "in_progress" || forge.countNamed(model.ReleaseHoldCheckName) != 2 {
		t.Fatalf("late blocker hold is %+v (%d posted), want second check run in progress",
			hold, forge.countNamed(model.ReleaseHoldCheckName))
	}
	closeTask(t, ctx, store, late, later.Add(time.Minute))
	syncQualification(t, ctx, store, forge, later.Add(time.Minute))
	hold = forge.checkNamed(model.ReleaseHoldCheckName)
	if hold.Status != "completed" || hold.Conclusion == nil || *hold.Conclusion != model.ConclusionSuccess {
		t.Fatalf("late blocker hold after close is %+v, want completed success", hold)
	}
}

func TestTriagePendingHoldCompletesAfterGraceWindowOrInboxClear(t *testing.T) {
	t.Setenv("GRAIN_RC_HOLD_GRACE", "30m")
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	failItemTwice(t, ctx, store, forge, repo, rqTag, "qualification-base-triage", baseTime)

	n := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag)
	if n == nil || !strings.Contains(n.Body, "up to 30m") {
		t.Fatalf("rc-failed body does not reflect GRAIN_RC_HOLD_GRACE=30m:\n%+v", n)
	}
	// At the 20-minute train tick, triage is still within the 30m window.
	syncQualification(t, ctx, store, forge, baseTime.Add(20*time.Minute))
	if hold := forge.checkNamed(model.ReleaseHoldCheckName); hold == nil || hold.Status != "in_progress" {
		t.Fatalf("hold at 20m is %+v, want still in progress", hold)
	}
	// Once the 30m grace window elapses with no blocker, the hold completes neutral.
	syncQualification(t, ctx, store, forge, baseTime.Add(30*time.Minute))
	syncQualification(t, ctx, store, forge, baseTime.Add(31*time.Minute))
	hold := forge.checkNamed(model.ReleaseHoldCheckName)
	if hold == nil || hold.Status != "completed" || hold.Conclusion == nil || *hold.Conclusion != model.ConclusionNeutral ||
		!strings.Contains(hold.Output, "Triage window elapsed") || forge.countNamed(model.ReleaseHoldCheckName) != 1 {
		t.Fatalf("hold after 30m grace is %+v (%d posted), want one completed neutral",
			hold, forge.countNamed(model.ReleaseHoldCheckName))
	}

	// Clearing pipeline.rc-failed in the Inbox while triage is pending also lifts the hold.
	store2, ctx2, repo2, forge2 := newQualificationRepo(t)
	forge2.nightly = "success"
	failItemTwice(t, ctx2, store2, forge2, repo2, rqTag, "qualification-base-triage", baseTime)
	if _, err := store2.ClearNotification(ctx2, model.NotificationSubject(model.FamilyRCFailed, rqTag), "human:op"); err != nil {
		t.Fatal(err)
	}
	syncQualification(t, ctx2, store2, forge2, baseTime.Add(5*time.Minute))
	hold2 := forge2.checkNamed(model.ReleaseHoldCheckName)
	if hold2 == nil || hold2.Status != "completed" || hold2.Conclusion == nil || *hold2.Conclusion != model.ConclusionSuccess ||
		!strings.Contains(hold2.Output, "Triage acknowledged") {
		t.Fatalf("hold after Inbox clear is %+v, want completed success (Triage acknowledged)", hold2)
	}
}

func TestTheLastRedAttemptEscalatesTheVersion(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	rc2 := strings.Repeat("2", 40)
	forge.tags = []github.Tag{
		{Name: "v1.3.0", SHA: strings.Repeat("3", 40)},
		{Name: "v1.4.0-rc.1", SHA: strings.Repeat("1", 40)},
		{Name: "v1.4.0-rc.1-discarded", SHA: strings.Repeat("1", 40)},
		{Name: "v1.4.0-rc.2", SHA: rc2},
		{Name: "v1.4.0-rc.2-discarded", SHA: rc2},
		{Name: "v1.4.0-rc.3", SHA: rqSHA},
	}
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject: model.NotificationSubject(model.FamilyRCFailed, "v1.4.0-rc.2"), Severity: model.NotificationWarning,
		Title: "rc.2 failed", Body: "rc.2 broke the merge queue", Repo: repo.String(), RaisedAt: baseTime,
		RaisedBy: "automation:release-qualification",
	}); err != nil {
		t.Fatal(err)
	}
	forge.nightly = "success"
	failItemTwice(t, ctx, store, forge, repo, "v1.4.0-rc.3", "qualification-mergequeue", baseTime)

	n := openNotification(t, ctx, store, model.FamilyRCFailed, "v1.4.0-rc.3")
	if n == nil || !strings.Contains(n.Body, "last attempt") || strings.Contains(n.Body, "holds v1.4.0-rc.4") {
		t.Fatalf("rc-failed is %+v, want it to say rc.3 was the last attempt", n)
	}
	e := openNotification(t, ctx, store, model.FamilyReleaseEscalated, "v1.4.0")
	if e == nil {
		t.Fatal("no pipeline.release-escalated:v1.4.0 after rc.3 failed")
	}
	if e.Severity != model.NotificationCritical {
		t.Errorf("escalation is %s, want critical", e.Severity)
	}
	for _, want := range []string{"### v1.4.0-rc.2", "rc.2 broke the merge queue", "### v1.4.0-rc.3",
		"qualification-mergequeue: fail"} {
		if !strings.Contains(e.Body, want) {
			t.Errorf("escalation body does not say %q:\n%s", want, e.Body)
		}
	}
	if strings.Index(e.Body, "### v1.4.0-rc.2") > strings.Index(e.Body, "### v1.4.0-rc.3") {
		t.Errorf("escalation lists the candidates out of order:\n%s", e.Body)
	}

	// A person cuts rc.4 by hand: the train is moving again.
	forge.tags = append(forge.tags,
		github.Tag{Name: "v1.4.0-rc.3-discarded", SHA: rqSHA},
		github.Tag{Name: "v1.4.0-rc.4", SHA: strings.Repeat("5", 40)})
	syncQualification(t, ctx, store, forge, baseTime.Add(time.Minute))
	if e := openNotification(t, ctx, store, model.FamilyReleaseEscalated, "v1.4.0"); e != nil {
		t.Fatalf("escalation still open after rc.4 was cut: %+v", e)
	}
}

func TestAnItemBlockedOnInfraForTwoHoursStallsTheCandidate(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	syncQualification(t, ctx, store, forge, baseTime)
	at := baseTime.Add(time.Minute)
	finishQualificationTask(t, ctx, store, rcTasks(t, ctx, store, repo, rqTag)["qualification-ci-diagnostics"][0], "INFRA", at)
	syncQualification(t, ctx, store, forge, at)
	syncQualification(t, ctx, store, forge, at.Add(time.Hour))
	if n := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rqTag); n != nil {
		t.Fatalf("stalled after an hour: %+v", n)
	}

	syncQualification(t, ctx, store, forge, at.Add(model.ReleaseInfraStalledAfter))
	n := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rqTag)
	if n == nil || !strings.Contains(n.Body, "qualification-ci-diagnostics has been blocked on infrastructure for 2h") {
		t.Fatalf("infra-stalled is %+v, want it raised naming the item", n)
	}
	if c := forge.checkNamed(model.ReleaseQualificationCheckName); c.Status != "in_progress" {
		t.Fatalf("check is %+v, want the candidate still qualifying", c)
	}

	retry := rcTasks(t, ctx, store, repo, rqTag)["qualification-ci-diagnostics"][1]
	finishQualificationTask(t, ctx, store, retry, "PASS", at.Add(3*time.Hour))
	syncQualification(t, ctx, store, forge, at.Add(3*time.Hour))
	if n := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rqTag); n != nil {
		t.Fatalf("infra-stalled still open once the item passed: %+v", n)
	}
}

func rcAttemptTasks(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef, rcTag string, attempt int) map[string][]string {
	t.Helper()
	tag := model.QualificationAttemptTag(model.ReleaseQualificationTag(repo, rcTag), attempt)
	runs, err := store.SuiteRunsTagged(ctx, tag)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, run := range runs {
		for _, task := range run.Tasks {
			out[task.TemplateID] = append(out[task.TemplateID], task.TaskID)
		}
	}
	return out
}

// stallAttemptOnInfra drives attempt of rcTag to a completed neutral
// verdict by failing qualification-ci-diagnostics on INFRA across its
// initial run and all MaxInfraReruns re-runs while the other gating items
// pass. When ackMidRun is true, it also exercises a 2h mid-run stall
// warning acknowledged before the attempt exhausts its re-runs.
func stallAttemptOnInfra(t *testing.T, ctx context.Context, store *model.Store, forge *fakeQualificationForge,
	repo model.RepoRef, rcTag string, attempt int, ackMidRun bool, start time.Time) time.Time {
	t.Helper()
	syncQualification(t, ctx, store, forge, start)
	tasks := rcAttemptTasks(t, ctx, store, repo, rcTag, attempt)
	at := start.Add(time.Minute)
	for _, item := range []string{"qualification-mergequeue", "qualification-base-triage"} {
		finishQualificationTask(t, ctx, store, tasks[item][0], "PASS", at)
	}
	for i := 0; i <= model.DefaultQualificationPolicy.MaxInfraReruns; i++ {
		tasks = rcAttemptTasks(t, ctx, store, repo, rcTag, attempt)
		if len(tasks["qualification-ci-diagnostics"]) != i+1 {
			t.Fatalf("attempt %d step %d: ci-diagnostics tasks = %v, want %d",
				attempt, i, tasks["qualification-ci-diagnostics"], i+1)
		}
		finishQualificationTask(t, ctx, store, tasks["qualification-ci-diagnostics"][i], "INFRA", at)
		syncQualification(t, ctx, store, forge, at)
		if i == 0 && ackMidRun {
			at = at.Add(model.ReleaseInfraStalledAfter)
			syncQualification(t, ctx, store, forge, at)
			mid := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rcTag)
			if mid == nil {
				t.Fatal("expected mid-run 2h infra-stalled notification")
			}
			if err := store.AckNotification(ctx, mid.ID, "human:alice", at); err != nil {
				t.Fatalf("acking mid-run warning: %v", err)
			}
		}
		at = at.Add(time.Minute)
	}
	return at
}

func TestANeutralCandidateRequalifiesOnceTheInfraStallIsAckedOrCleared(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly = "success"
	baseTag := model.ReleaseQualificationTag(repo, rqTag)

	// Attempt 1 exhausts its infra re-runs (even after an operator acked
	// the earlier 2h warning): Stage 2 posts completed/neutral and leaves
	// an unacknowledged stall notification telling a person how to retry.
	at := stallAttemptOnInfra(t, ctx, store, forge, repo, rqTag, 1, true, baseTime)
	if forge.countNamed(model.ReleaseQualificationCheckName) != 1 {
		t.Fatalf("checks = %+v, want one check run on attempt 1", forge.checks)
	}
	c1 := forge.checks[0]
	if c1.Status != "completed" || c1.Conclusion == nil || *c1.Conclusion != model.ConclusionNeutral ||
		c1.ExternalID != baseTag {
		t.Fatalf("attempt 1 check = %+v, want completed neutral with external_id %s", c1, baseTag)
	}
	n := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rqTag)
	if n == nil || n.AckedAt != nil || !strings.Contains(n.Body, "acknowledge or clear this notification") {
		t.Fatalf("infra-stalled after neutral = %+v, want open and unacknowledged with retry instructions", n)
	}

	// While the stall is neither acked nor cleared, reconcile cycles leave
	// the candidate waiting and file nothing new.
	syncQualification(t, ctx, store, forge, at.Add(10*time.Minute))
	if got := rcAttemptTasks(t, ctx, store, repo, rqTag, 2); len(got) != 0 {
		t.Fatalf("attempt 2 started before stall cleared: %v", got)
	}
	if forge.countNamed(model.ReleaseQualificationCheckName) != 1 {
		t.Fatalf("checks while stalled = %+v, want still 1", forge.checks)
	}

	// Acknowledging the stall starts fresh Stage 2 attempt 2 (#2) in a new
	// check run, clears the acknowledged stall, and leaves attempt 1's
	// neutral check run intact. If attempt 2 stalls again, it stops on a
	// fresh unacknowledged notification rather than looping.
	if err := store.AckNotification(ctx, n.ID, "human:alice", at.Add(15*time.Minute)); err != nil {
		t.Fatalf("AckNotification: %v", err)
	}
	at = stallAttemptOnInfra(t, ctx, store, forge, repo, rqTag, 2, false, at.Add(16*time.Minute))
	if forge.countNamed(model.ReleaseQualificationCheckName) != 2 {
		t.Fatalf("checks after attempt 2 = %+v, want 2 check runs", forge.checks)
	}
	if forge.checks[0].Conclusion == nil || *forge.checks[0].Conclusion != model.ConclusionNeutral ||
		forge.checks[0].ExternalID != baseTag {
		t.Fatalf("attempt 1 check mutated: %+v", forge.checks[0])
	}
	if c2 := forge.checks[1]; c2.Status != "completed" || c2.Conclusion == nil ||
		*c2.Conclusion != model.ConclusionNeutral || c2.ExternalID != baseTag+"#2" {
		t.Fatalf("attempt 2 check = %+v, want completed neutral with external_id %s#2", c2, baseTag)
	}
	n2 := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rqTag)
	if n2 == nil || n2.AckedAt != nil {
		t.Fatalf("infra-stalled after attempt 2 = %+v, want a fresh unacknowledged notification", n2)
	}
	syncQualification(t, ctx, store, forge, at.Add(time.Minute))
	if got := rcAttemptTasks(t, ctx, store, repo, rqTag, 3); len(got) != 0 {
		t.Fatalf("attempt 3 started without clearing attempt 2's stall: %v", got)
	}

	// Clearing the stall starts attempt 3 (#3), which passes and qualifies
	// the same rc without cutting rc.3 or spending an RC attempt.
	subject := model.NotificationSubject(model.FamilyReleaseInfraStalled, rqTag)
	if _, err := store.ClearNotification(ctx, subject, "human:alice"); err != nil {
		t.Fatalf("ClearNotification: %v", err)
	}
	syncQualification(t, ctx, store, forge, at.Add(2*time.Minute))
	tasks3 := rcAttemptTasks(t, ctx, store, repo, rqTag, 3)
	if len(tasks3) != 3 {
		t.Fatalf("attempt 3 gating tasks = %v, want 3 items", tasks3)
	}
	if c3 := forge.checkNamed(model.ReleaseQualificationCheckName); c3 == nil || c3.Status != "in_progress" ||
		c3.ExternalID != baseTag+"#3" {
		t.Fatalf("attempt 3 in-progress check = %+v, want in_progress with external_id %s#3", c3, baseTag)
	}
	for _, item := range rqItems[:3] {
		finishQualificationTask(t, ctx, store, tasks3[item][0], "PASS", at.Add(3*time.Minute))
	}
	syncQualification(t, ctx, store, forge, at.Add(3*time.Minute))
	tasks3 = rcAttemptTasks(t, ctx, store, repo, rqTag, 3)
	finishQualificationTask(t, ctx, store, tasks3["qualification-system-smoke"][0], "PASS", at.Add(4*time.Minute))
	syncQualification(t, ctx, store, forge, at.Add(4*time.Minute))

	if forge.countNamed(model.ReleaseQualificationCheckName) != 3 {
		t.Fatalf("total check runs = %d, want 3", forge.countNamed(model.ReleaseQualificationCheckName))
	}
	c3 := forge.checkNamed(model.ReleaseQualificationCheckName)
	if c3 == nil || c3.Status != "completed" || c3.Conclusion == nil || *c3.Conclusion != model.ConclusionSuccess ||
		c3.ExternalID != baseTag+"#3" {
		t.Fatalf("attempt 3 final check = %+v, want completed success on %s#3", c3, baseTag)
	}
	if open := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rqTag); open != nil {
		t.Fatalf("infra-stalled still open after qualification passed: %+v", open)
	}
}

func TestTwoFlakeRetriesOfOneItemInADaySpendItsFlakeBudget(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newQualificationRepo(t)
	forge.nightly, forge.anySHA = "success", true
	item := "qualification-base-triage"
	subject := repo.String() + ":" + item

	syncQualification(t, ctx, store, forge, baseTime)
	completeTask(t, ctx, store, rcTasks(t, ctx, store, repo, rqTag)[item][0], baseTime)
	syncQualification(t, ctx, store, forge, baseTime)
	if n := openNotification(t, ctx, store, model.FamilyFlakeBudget, subject); n != nil {
		t.Fatalf("flake-budget after one retry: %+v", n)
	}

	// rc.2 is dropped for something else and rc.3 flakes on the same item.
	rc3 := "v1.4.0-rc.3"
	forge.tags = append(forge.tags,
		github.Tag{Name: rqTag + "-discarded", SHA: rqSHA},
		github.Tag{Name: rc3, SHA: strings.Repeat("4", 40)})
	at := baseTime.Add(6 * time.Hour)
	syncQualification(t, ctx, store, forge, at)
	completeTask(t, ctx, store, rcTasks(t, ctx, store, repo, rc3)[item][0], at)
	syncQualification(t, ctx, store, forge, at)

	n := openNotification(t, ctx, store, model.FamilyFlakeBudget, subject)
	if n == nil {
		t.Fatalf("no pipeline.flake-budget:%s after two retries in a day", subject)
	}
	if !strings.Contains(n.Body, "retried 2 times") || n.ClearsWhen == nil || n.ClearsWhen.Kind != model.ClearsOnTTL {
		t.Fatalf("flake-budget is %+v, want it to count two retries and clear on a TTL", n)
	}
}

// stage1Forge is fakeQualificationForge that can also read Stage 1 job by
// job: jobs is every attempt of every nightly job, by commit.
type stage1Forge struct {
	*fakeQualificationForge
	jobs map[string][]github.ActionsRunJob
}

func (f *stage1Forge) WorkflowRunsOnCommit(owner, repo, sha string) ([]github.WorkflowRun, error) {
	if len(f.jobs[sha]) == 0 {
		return nil, nil
	}
	return []github.WorkflowRun{{ID: 900, Name: model.ReleaseQualificationStage1Workflow, HeadSHA: sha}}, nil
}

func (f *stage1Forge) ListActionsJobAttempts(owner, repo string, runID int64) ([]github.ActionsRunJob, error) {
	var out []github.ActionsRunJob
	for _, jobs := range f.jobs {
		for _, j := range jobs {
			if j.RunID == runID {
				out = append(out, j)
			}
		}
	}
	return out, nil
}

// nightlyJob is one attempt of a nightly job on sha.
func nightlyJob(sha, name string, attempt int, conclusion string) github.ActionsRunJob {
	return github.ActionsRunJob{RunID: 900, RunAttempt: attempt, Name: name, WorkflowName: "nightly",
		HeadSHA: sha, Status: "completed", Conclusion: conclusion}
}

func newStage1Repo(t *testing.T) (*model.Store, context.Context, model.RepoRef, *stage1Forge) {
	t.Helper()
	store, ctx, repo, forge := newQualificationRepo(t)
	return store, ctx, repo, &stage1Forge{fakeQualificationForge: forge, jobs: map[string][]github.ActionsRunJob{}}
}

func syncStage1(t *testing.T, ctx context.Context, store *model.Store, forge *stage1Forge, at time.Time) {
	t.Helper()
	if err := orchestrator.SyncReleaseQualification(ctx, store, forge, at); err != nil {
		t.Fatalf("SyncReleaseQualification: %v", err)
	}
}

// A candidate red in Stage 1 -- a nightly job that failed its one retry --
// raises pipeline.rc-failed and holds rc.(N+1) on its commit, exactly as a
// Stage 2 failure does, without Stage 2 ever starting.
func TestARedStage1RaisesRCFailedAndHoldsTheNext(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newStage1Repo(t)
	forge.nightly = "failure"
	forge.jobs[rqSHA] = []github.ActionsRunJob{
		nightlyJob(rqSHA, "go, shuffled and repeated (3)", 1, "failure"),
		nightlyJob(rqSHA, "go, shuffled and repeated (1)", 1, "success"),
		nightlyJob(rqSHA, "sustained load", 1, "success"),
		nightlyJob(rqSHA, "stage 1 qualification verdict", 1, "failure"),
	}
	syncStage1(t, ctx, store, forge, baseTime)
	if n := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag); n != nil {
		t.Fatalf("raised %+v on a first failure, which still has its flake retry", n)
	}

	forge.jobs[rqSHA] = append(forge.jobs[rqSHA],
		nightlyJob(rqSHA, "go, shuffled and repeated (3)", 2, "failure"),
		nightlyJob(rqSHA, "stage 1 qualification verdict", 2, "failure"))
	syncStage1(t, ctx, store, forge, baseTime.Add(time.Minute))
	n := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag)
	if n == nil {
		t.Fatalf("no pipeline.rc-failed:%s raised for a red Stage 1", rqTag)
	}
	floor := strings.Repeat("3", 40)
	for _, want := range []string{
		"failed Stage 1 qualification (nightly): attempt 2 of RELEASE_MAX_RC_ATTEMPTS (3)",
		"`" + floor + ".." + rqSHA + "` (everything since v1.3.0)",
		"Stage 1 (nightly):",
		"go, shuffled and repeated: failed again on its retry (failure,failure): confirmed red",
		model.RCBlockerMarker(rqTag),
	} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("rc-failed body does not say %q:\n%s", want, n.Body)
		}
	}
	if strings.Contains(n.Body, "stage 1 qualification verdict") {
		t.Errorf("rc-failed names nightly's roll-up job, which carries no verdict:\n%s", n.Body)
	}
	hold := forge.checkNamed(model.ReleaseHoldCheckName)
	if hold == nil || hold.Status != "in_progress" || forge.heads[hold.ID] != rqSHA {
		t.Fatalf("hold is %+v, want triage pending in progress on %s", hold, rqSHA)
	}
	if forge.checkNamed(model.ReleaseQualificationCheckName) != nil || len(rcTasks(t, ctx, store, repo, rqTag)) != 0 {
		t.Fatalf("Stage 2 started on a red Stage 1: checks %+v", forge.checks)
	}
	if e := openNotification(t, ctx, store, model.FamilyReleaseEscalated, "v1.4.0"); e != nil {
		t.Fatalf("rc.2 escalated %+v; only the last attempt does", e)
	}

	// Cleared by the steward, it is not raised again by the same red run.
	if _, err := store.ClearNotification(ctx, n.Subject, "test"); err != nil {
		t.Fatal(err)
	}
	syncStage1(t, ctx, store, forge, baseTime.Add(2*time.Minute))
	if again := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag); again != nil {
		t.Fatalf("raised %+v again after it was cleared", again)
	}
}

// The version's last candidate red in Stage 1 escalates the version, and
// holds nothing: the train cuts no rc.4.
func TestARedStage1OnTheLastAttemptEscalates(t *testing.T) {
	t.Parallel()
	store, ctx, _, forge := newStage1Repo(t)
	rc2 := strings.Repeat("2", 40)
	forge.tags = []github.Tag{
		{Name: "v1.4.0-rc.1", SHA: strings.Repeat("1", 40)},
		{Name: "v1.4.0-rc.1-discarded", SHA: strings.Repeat("1", 40)},
		{Name: "v1.4.0-rc.2", SHA: rc2},
		{Name: "v1.4.0-rc.2-discarded", SHA: rc2},
		{Name: "v1.4.0-rc.3", SHA: rqSHA},
		{Name: "v1.3.0", SHA: strings.Repeat("3", 40)},
	}
	forge.nightly = "failure"
	forge.jobs[rqSHA] = []github.ActionsRunJob{
		nightlyJob(rqSHA, "sustained load", 1, "failure"),
		nightlyJob(rqSHA, "sustained load", 2, "failure"),
	}
	syncStage1(t, ctx, store, forge, baseTime)
	if n := openNotification(t, ctx, store, model.FamilyRCFailed, "v1.4.0-rc.3"); n == nil ||
		!strings.Contains(n.Body, "This was the version's last attempt") {
		t.Fatalf("rc-failed for rc.3 is %+v, want it raised as the last attempt", n)
	}
	e := openNotification(t, ctx, store, model.FamilyReleaseEscalated, "v1.4.0")
	if e == nil || e.Severity != model.NotificationCritical {
		t.Fatalf("release-escalated is %+v, want critical", e)
	}
	if hold := forge.checkNamed(model.ReleaseHoldCheckName); hold != nil {
		t.Fatalf("posted hold %+v on the last attempt, which cuts no next candidate", hold)
	}
}

// A Stage 1 job that spends its flake retry on two candidates in a day
// raises pipeline.flake-budget for that job; each candidate's retry counts
// once however many cycles read it.
func TestAStage1FlakeRetrySpendsTheFlakeBudget(t *testing.T) {
	t.Parallel()
	store, ctx, repo, forge := newStage1Repo(t)
	forge.nightly = "success"
	forge.anySHA = true
	flake := func(sha string) []github.ActionsRunJob {
		return []github.ActionsRunJob{
			nightlyJob(sha, "go, shuffled and repeated (3)", 1, "failure"),
			nightlyJob(sha, "go, shuffled and repeated (3)", 2, "success"),
		}
	}
	forge.jobs[rqSHA] = flake(rqSHA)
	subject := "acme/widgets:go, shuffled and repeated"
	for i := range 3 {
		syncStage1(t, ctx, store, forge, baseTime.Add(time.Duration(i)*time.Minute))
	}
	if n := openNotification(t, ctx, store, model.FamilyFlakeBudget, subject); n != nil {
		t.Fatalf("one candidate's flake retry raised %+v, want the budget to hold", n)
	}
	if len(rcTasks(t, ctx, store, repo, rqTag)) == 0 {
		t.Fatalf("a Stage 1 that passed on its flake retry did not start Stage 2")
	}

	rc3 := strings.Repeat("4", 40)
	forge.tags = append(forge.tags, github.Tag{Name: "v1.4.0-rc.3", SHA: rc3})
	forge.jobs[rc3] = flake(rc3)
	syncStage1(t, ctx, store, forge, baseTime.Add(time.Hour))
	n := openNotification(t, ctx, store, model.FamilyFlakeBudget, subject)
	if n == nil {
		t.Fatalf("no pipeline.flake-budget:%s after two candidates' flake retries", subject)
	}
	if !strings.Contains(n.Body, "retried 2 times") || !strings.Contains(n.Body, "Stage 1 (nightly)") ||
		!strings.Contains(n.Body, "v1.4.0-rc.3") {
		t.Errorf("flake-budget body is %q", n.Body)
	}
}

// A Stage 1 job starved of capacity three attempts in a row stalls the
// candidate on infrastructure, never red.
func TestAStage1InfraStallRaisesReleaseInfraStalled(t *testing.T) {
	t.Parallel()
	store, ctx, _, forge := newStage1Repo(t)
	forge.nightly = "cancelled"
	for i := 1; i <= 3; i++ {
		forge.jobs[rqSHA] = append(forge.jobs[rqSHA], nightlyJob(rqSHA, "real KVM sandboxes", i, "cancelled"))
	}
	syncStage1(t, ctx, store, forge, baseTime)
	n := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rqTag)
	if n == nil || !strings.Contains(n.Body, "real KVM sandboxes was cancelled, timed out or starved of capacity 3 attempts in a row") {
		t.Fatalf("infra-stalled is %+v, want it raised for the stalled Stage 1 job", n)
	}
	if f := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag); f != nil {
		t.Fatalf("an infrastructure stall raised %+v", f)
	}
	forge.jobs[rqSHA] = append(forge.jobs[rqSHA], nightlyJob(rqSHA, "real KVM sandboxes", 4, "success"))
	forge.nightly = "success"
	syncStage1(t, ctx, store, forge, baseTime.Add(time.Minute))
	if n := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rqTag); n != nil {
		t.Fatalf("infra-stalled %+v still open once Stage 1 is green", n)
	}
}

// A candidate the train dropped on a red Stage 1 before grain judged it --
// or on a forge whose jobs grain cannot read -- still raises
// pipeline.rc-failed and the hold, from its -discarded tag.
func TestADiscardedStage1FailureRaisesRCFailedFromTheTag(t *testing.T) {
	t.Parallel()
	store, ctx, _, forge := newQualificationRepo(t)
	forge.nightly = "failure"
	forge.tags = append(forge.tags, github.Tag{Name: rqTag + "-discarded", SHA: rqSHA})
	syncQualification(t, ctx, store, forge, baseTime)
	n := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag)
	if n == nil || !strings.Contains(n.Body, "failed Stage 1 qualification (nightly)") ||
		!strings.Contains(n.Body, "the train dropped "+rqTag+" ("+rqTag+"-discarded)") {
		t.Fatalf("rc-failed is %+v, want it raised from the -discarded tag", n)
	}
	if hold := forge.checkNamed(model.ReleaseHoldCheckName); hold == nil || hold.Status != "in_progress" {
		t.Fatalf("hold is %+v, want triage pending", hold)
	}
	// rc.1, dropped too but superseded by rc.2, is not raised.
	if old := openNotification(t, ctx, store, model.FamilyRCFailed, "v1.4.0-rc.1"); old != nil {
		t.Fatalf("raised %+v for a superseded candidate", old)
	}
}

// A forge connection grain cannot name an App for (a PAT) posts no check
// run, but a red Stage 1 still raises pipeline.rc-failed in grain.
func TestARedStage1RaisesRCFailedWithoutAGitHubApp(t *testing.T) {
	t.Parallel()
	store, ctx, _, forge := newStage1Repo(t)
	forge.app = ""
	forge.nightly = "failure"
	forge.jobs[rqSHA] = []github.ActionsRunJob{
		nightlyJob(rqSHA, "sustained load", 1, "failure"),
		nightlyJob(rqSHA, "sustained load", 2, "failure"),
	}
	if err := orchestrator.SyncReleaseQualification(ctx, store, forge, baseTime); err == nil ||
		!strings.Contains(err.Error(), "not a GitHub App") {
		t.Fatalf("SyncReleaseQualification = %v, want Stage 2 still refused", err)
	}
	if n := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag); n == nil {
		t.Fatalf("no pipeline.rc-failed:%s without a GitHub App", rqTag)
	}
	if len(forge.checks) != 0 {
		t.Fatalf("posted %+v without a GitHub App, want no check run", forge.checks)
	}
}
