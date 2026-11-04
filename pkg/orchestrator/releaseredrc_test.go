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
		"holds v1.4.0-rc.3",
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
	if forge.countNamed(model.ReleaseHoldCheckName) != 0 {
		t.Fatalf("posted %s with no blocker linked", model.ReleaseHoldCheckName)
	}

	// The steward files a fix titled with the marker: rc.3 is held.
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
	syncQualification(t, ctx, store, forge, at.Add(time.Minute))
	hold := forge.checkNamed(model.ReleaseHoldCheckName)
	if hold == nil || hold.Status != "in_progress" || forge.countNamed(model.ReleaseHoldCheckName) != 1 {
		t.Fatalf("hold is %+v (%d posted), want one in progress", hold, forge.countNamed(model.ReleaseHoldCheckName))
	}
	if forge.heads[hold.ID] != rqSHA {
		t.Fatalf("hold is on %s, want the red candidate's commit %s", forge.heads[hold.ID], rqSHA)
	}

	// It lands: the hold completes, and rc-failed stays until rc.3 is cut.
	closeTask(t, ctx, store, blocker, at.Add(2*time.Minute))
	syncQualification(t, ctx, store, forge, at.Add(2*time.Minute))
	hold = forge.checkNamed(model.ReleaseHoldCheckName)
	if hold.Status != "completed" || hold.Conclusion == nil || *hold.Conclusion != model.ConclusionSuccess {
		t.Fatalf("hold is %+v once its blocker landed, want completed success", hold)
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
