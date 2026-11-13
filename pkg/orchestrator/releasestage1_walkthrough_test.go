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

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The Stage 1 job histories of docs/test-plans/2026-11-10.md §8 item 2,
// each fed to SyncReleaseQualification through a stub
// WorkflowRunsOnCommit: what is raised, whether grain/rc-hold is posted
// and whether Stage 2 starts. scripts/candidate-gate.sh judges the same
// histories in docs/test-runs/2026-11-10-release-train/stage1-gate.sh.
func TestStage1JobHistoriesFromTheReleaseTrainWalkthrough(t *testing.T) {
	t.Parallel()
	type want struct {
		rcFailed string // a line the rc-failed body must carry; "" is none raised
		hold     bool   // grain/rc-hold posted (triage pending)
		stage2   bool   // Stage 2 started
		infra    string // a line release-infra-stalled must carry; "" is none
	}
	job := nightlyJob
	queued := func(name string, attempt int) github.ActionsRunJob {
		j := job(rqSHA, name, attempt, "")
		j.Status = "queued"
		return j
	}
	for _, tc := range []struct {
		name    string
		nightly string // nightly.yml's run conclusion
		jobs    []github.ActionsRunJob
		later   time.Duration // a second sync this long after the first
		want    want
	}{
		{
			name:    "one job failing once then passing is a flake",
			nightly: "success",
			jobs:    []github.ActionsRunJob{job(rqSHA, "sustained load", 1, "failure"), job(rqSHA, "sustained load", 2, "success")},
			want:    want{stage2: true},
		},
		{
			name:    "one job failing twice is confirmed red",
			nightly: "failure",
			jobs:    []github.ActionsRunJob{job(rqSHA, "sustained load", 1, "failure"), job(rqSHA, "sustained load", 2, "failure")},
			want:    want{rcFailed: "sustained load: failed again on its retry (failure,failure): confirmed red", hold: true},
		},
		{
			name:    "three jobs failing once each is systemic",
			nightly: "failure",
			jobs: []github.ActionsRunJob{job(rqSHA, "sustained load", 1, "failure"), job(rqSHA, "govulncheck", 1, "failure"),
				job(rqSHA, "merge liveness stress", 1, "failure"), job(rqSHA, "state repository soak", 1, "success")},
			want: want{rcFailed: "systemic: 3 jobs failed (govulncheck, merge liveness stress, sustained load)", hold: true},
		},
		{
			name:    "matrix shards are one job: two shards failing once each is still waiting on a retry",
			nightly: "failure",
			jobs: []github.ActionsRunJob{job(rqSHA, "go, shuffled and repeated (1)", 1, "failure"),
				job(rqSHA, "go, shuffled and repeated (2)", 1, "failure"), job(rqSHA, "go, shuffled and repeated (3)", 1, "success")},
			want: want{},
		},
		{
			name:    "matrix shards failing on the retry are confirmed red",
			nightly: "failure",
			jobs: []github.ActionsRunJob{job(rqSHA, "go, shuffled and repeated (1)", 1, "failure"),
				job(rqSHA, "go, shuffled and repeated (2)", 1, "success"), job(rqSHA, "go, shuffled and repeated (1)", 2, "failure")},
			want: want{rcFailed: "go, shuffled and repeated: failed again on its retry (failure,failure): confirmed red", hold: true},
		},
		{
			// nightly.yml's run concludes failure, but candidate-gate.sh
			// ignores the roll-up and qualifies: Stage 2 has to start, or
			// the train waits on a check run grain never posts.
			name:    "the roll-up job failing alone carries no verdict",
			nightly: "failure",
			jobs: []github.ActionsRunJob{job(rqSHA, "sustained load", 1, "success"),
				job(rqSHA, "stage 1 qualification verdict", 1, "failure"), job(rqSHA, "stage 1 qualification verdict", 2, "failure")},
			later: time.Minute,
			want:  want{stage2: true},
		},
		{
			// The roll-up failed because a job it needs never ran: that is
			// no pass, so grain keeps to the run's own conclusion.
			name:    "the roll-up failing on a job that never ran holds Stage 2",
			nightly: "failure",
			jobs: []github.ActionsRunJob{job(rqSHA, "sustained load", 1, "success"), job(rqSHA, "govulncheck", 1, "skipped"),
				job(rqSHA, "stage 1 qualification verdict", 1, "failure")},
			later: time.Minute,
			want:  want{},
		},
		{
			name:    "a job cancelled three attempts in a row stalls on infrastructure",
			nightly: "cancelled",
			jobs: []github.ActionsRunJob{job(rqSHA, "sustained load", 1, "cancelled"), job(rqSHA, "sustained load", 2, "cancelled"),
				job(rqSHA, "sustained load", 3, "cancelled")},
			want: want{infra: "sustained load was cancelled, timed out or starved of capacity 3 attempts in a row"},
		},
		{
			// A retry dispatched by a key the job does not have runs
			// nothing: each skipped retry is infrastructure, so the rc
			// stalls rather than waiting on a retry forever.
			name:    "a failed job skipped on three retries in a row stalls on infrastructure",
			nightly: "failure",
			jobs: []github.ActionsRunJob{job(rqSHA, "grant matrix through the UI", 1, "failure"),
				job(rqSHA, "grant matrix through the UI", 2, "skipped"), job(rqSHA, "grant matrix through the UI", 3, "skipped"),
				job(rqSHA, "grant matrix through the UI", 4, "skipped")},
			want: want{infra: "grant matrix through the UI was cancelled, timed out or starved of capacity 3 attempts in a row"},
		},
		{
			name:    "a failed job passing after a skipped retry is a flake",
			nightly: "failure",
			jobs: []github.ActionsRunJob{job(rqSHA, "sustained load", 1, "failure"), job(rqSHA, "sustained load", 2, "skipped"),
				job(rqSHA, "sustained load", 3, "success")},
			want: want{stage2: true},
		},
		{
			name:    "a job blocked on infrastructure for over 2h stalls",
			nightly: "cancelled",
			jobs:    []github.ActionsRunJob{job(rqSHA, "sustained load", 1, "cancelled"), queued("sustained load", 2)},
			later:   model.ReleaseInfraStalledAfter + time.Minute,
			want:    want{infra: "sustained load has been blocked on infrastructure for 2h1m"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx, repo, forge := newStage1Repo(t)
			forge.nightly = tc.nightly
			forge.jobs[rqSHA] = tc.jobs
			syncStage1(t, ctx, store, forge, baseTime)
			if tc.later > 0 {
				syncStage1(t, ctx, store, forge, baseTime.Add(tc.later))
			}
			n := openNotification(t, ctx, store, model.FamilyRCFailed, rqTag)
			switch {
			case tc.want.rcFailed == "" && n != nil:
				t.Errorf("raised rc-failed %q", n.Body)
			case tc.want.rcFailed != "" && (n == nil || !strings.Contains(n.Body, tc.want.rcFailed)):
				t.Errorf("rc-failed is %+v, want it to say %q", n, tc.want.rcFailed)
			case n != nil && strings.Contains(n.Body, "stage 1 qualification verdict"):
				t.Errorf("rc-failed names the roll-up job:\n%s", n.Body)
			}
			hold := forge.checkNamed(model.ReleaseHoldCheckName)
			if got := hold != nil && hold.Status == "in_progress"; got != tc.want.hold {
				t.Errorf("grain/rc-hold is %+v, want posted %v", hold, tc.want.hold)
			}
			if got := len(rcTasks(t, ctx, store, repo, rqTag)) > 0; got != tc.want.stage2 {
				t.Errorf("Stage 2 started = %v, want %v", got, tc.want.stage2)
			}
			s := openNotification(t, ctx, store, model.FamilyReleaseInfraStalled, rqTag)
			switch {
			case tc.want.infra == "" && s != nil:
				t.Errorf("raised release-infra-stalled %q", s.Body)
			case tc.want.infra != "" && (s == nil || !strings.Contains(s.Body, tc.want.infra)):
				t.Errorf("release-infra-stalled is %+v, want it to say %q", s, tc.want.infra)
			}
			if f := openNotification(t, ctx, store, model.FamilyFlakeBudget, "acme/widgets:sustained load"); f != nil {
				t.Errorf("one candidate's flake spent the budget: %q", f.Body)
			}
		})
	}
}

// docs/test-plans/2026-11-10.md §8 item 3: rc.3, rc.4 and rc.5 of one
// version red in Stage 1 in turn. Each posts grain/rc-hold on its own
// commit, pipeline.release-escalated is raised once (on rc.3, the
// RELEASE_MAX_RC_ATTEMPTS-th) and stands through rc.4 and rc.5, and the
// release clears it.
func TestRedRC3ToRC5HoldEachAndEscalateOnceUntilTheRelease(t *testing.T) {
	t.Parallel()
	store, ctx, _, forge := newStage1Repo(t)
	forge.nightly = "failure"
	forge.anySHA = true
	forge.tags = []github.Tag{{Name: "v1.3.0", SHA: strings.Repeat("3", 40)}}
	for i := 1; i <= 2; i++ {
		sha := strings.Repeat(fmt.Sprintf("%x", 10+i), 40)
		forge.tags = append(forge.tags, github.Tag{Name: fmt.Sprintf("v1.4.0-rc.%d", i), SHA: sha},
			github.Tag{Name: fmt.Sprintf("v1.4.0-rc.%d-discarded", i), SHA: sha})
	}
	var escalation int64
	for i := 3; i <= 5; i++ {
		rc := fmt.Sprintf("v1.4.0-rc.%d", i)
		sha := strings.Repeat(fmt.Sprintf("%x", 10+i), 40)
		forge.tags = append(forge.tags, github.Tag{Name: rc, SHA: sha})
		forge.jobs[sha] = []github.ActionsRunJob{nightlyJob(sha, "sustained load", 1, "failure"), nightlyJob(sha, "sustained load", 2, "failure")}
		syncStage1(t, ctx, store, forge, baseTime.Add(time.Duration(i)*time.Hour))
		if n := openNotification(t, ctx, store, model.FamilyRCFailed, rc); n == nil {
			t.Fatalf("no pipeline.rc-failed:%s", rc)
		}
		held := false
		for _, c := range forge.checks {
			held = held || (c.Name == model.ReleaseHoldCheckName && forge.heads[c.ID] == sha && c.Status == "in_progress")
		}
		if !held {
			t.Fatalf("no triage-pending grain/rc-hold on %s's commit: %+v", rc, forge.checks)
		}
		e := openNotification(t, ctx, store, model.FamilyReleaseEscalated, "v1.4.0")
		if e == nil {
			t.Fatalf("no pipeline.release-escalated:v1.4.0 after %s", rc)
		}
		if escalation != 0 && e.ID != escalation {
			t.Fatalf("%s raised escalation %d again, want %d to stand", rc, e.ID, escalation)
		}
		escalation = e.ID
		forge.tags = append(forge.tags, github.Tag{Name: rc + "-discarded", SHA: sha})
	}
	forge.tags = append(forge.tags, github.Tag{Name: "v1.4.0-rc.6", SHA: rqSHA}, github.Tag{Name: "v1.4.0", SHA: rqSHA})
	forge.nightly = "success"
	syncStage1(t, ctx, store, forge, baseTime.Add(9*time.Hour))
	if e := openNotification(t, ctx, store, model.FamilyReleaseEscalated, "v1.4.0"); e != nil {
		t.Fatalf("escalation still open after v1.4.0 released: %+v", e)
	}
}
