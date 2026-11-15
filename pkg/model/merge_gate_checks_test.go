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

func ciGateWith(t *testing.T, checks *model.HeadChecks) model.MergeGate {
	t.Helper()
	task := model.Task{ID: "t1", Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#7"}}}
	for _, g := range model.ComputeMergeGatesWithChecks(task, &model.Observation{TaskID: "t1"}, nil, checks) {
		if g.ID == model.GateCI {
			return g
		}
	}
	t.Fatal("no CI gate")
	return model.MergeGate{}
}

func ghJob(id int64, commit, name, conclusion string, at time.Time) model.GitHubCIJob {
	return model.GitHubCIJob{Repo: "acme/widgets", CheckRunID: id, Ref: "grain/task-t1", Commit: commit,
		Workflow: "Tests", JobName: name, Conclusion: conclusion, CompletedAt: &at, RecordedAt: at}
}

// A pull request GitHub Actions gates has no grain CI verdict, so its CI
// gate is read off the checks at its head: green, red with the failing
// check named, running with the unfinished ones, and -- when grain has
// recorded nothing -- saying so, never "waiting for checks".
func TestCIGateReadsGitHubChecksAtTheHead(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	head := "abcdef0123456789abcdef0123456789abcdef01"
	old := "1111111111111111111111111111111111111111"

	t.Run("green", func(t *testing.T) {
		jobs := []model.GitHubCIJob{
			ghJob(1, old, "go", "failure", t0),
			ghJob(2, head, "go", "success", t0.Add(time.Hour)),
			ghJob(3, head, "ui e2e", "success", t0.Add(time.Hour)),
			ghJob(4, head, "lint", "skipped", t0.Add(time.Hour)),
		}
		g := ciGateWith(t, model.HeadChecksFrom(jobs, ""))
		if g.State != model.GateStatePassed || g.Overridable {
			t.Fatalf("gate = %+v, want passed", g)
		}
		if want := "passed (3 checks at abcdef012345)"; g.Detail != want {
			t.Errorf("detail = %q, want %q", g.Detail, want)
		}
	})

	t.Run("red", func(t *testing.T) {
		jobs := []model.GitHubCIJob{
			ghJob(2, head, "go", "success", t0),
			ghJob(3, head, "ui e2e", "failure", t0),
		}
		g := ciGateWith(t, model.HeadChecksFrom(jobs, head))
		if g.State != model.GateStateFailing || !g.Overridable {
			t.Fatalf("gate = %+v, want failing", g)
		}
		if !strings.Contains(g.Detail, "Tests / ui e2e") || strings.Contains(g.Detail, "Tests / go") {
			t.Errorf("detail = %q, want it to name the failing check only", g.Detail)
		}
	})

	t.Run("a re-run that passed replaces the failure", func(t *testing.T) {
		rerun := ghJob(9, head, "ui e2e", "success", t0.Add(time.Hour))
		rerun.RunAttempt = 2
		first := ghJob(3, head, "ui e2e", "failure", t0)
		first.RunAttempt = 1
		g := ciGateWith(t, model.HeadChecksFrom([]model.GitHubCIJob{first, rerun}, head))
		if g.State != model.GateStatePassed {
			t.Fatalf("gate = %+v, want passed", g)
		}
	})

	t.Run("pending", func(t *testing.T) {
		running := model.GitHubCIJob{Repo: "acme/widgets", CheckRunID: 5, Commit: head, Workflow: "Tests", JobName: "go e2e"}
		jobs := []model.GitHubCIJob{ghJob(2, head, "go", "success", t0), running}
		g := ciGateWith(t, model.HeadChecksFrom(jobs, head))
		if g.State != model.GateStateRunning {
			t.Fatalf("gate = %+v, want running", g)
		}
		if want := "running: 1 of 2 checks unfinished (Tests / go e2e) at abcdef012345"; g.Detail != want {
			t.Errorf("detail = %q, want %q", g.Detail, want)
		}
	})

	t.Run("nothing at a new head", func(t *testing.T) {
		jobs := []model.GitHubCIJob{ghJob(1, old, "go", "success", t0)}
		g := ciGateWith(t, model.HeadChecksFrom(jobs, head))
		if g.State != model.GateStateRunning || !strings.Contains(g.Detail, "no check results recorded at abcdef012345") {
			t.Fatalf("gate = %+v, want running with no results at the head", g)
		}
	})

	t.Run("no data", func(t *testing.T) {
		g := ciGateWith(t, model.HeadChecksFrom(nil, ""))
		if g.State != model.GateStateRunning || g.Detail != "grain has no record of this pull request's checks" {
			t.Fatalf("gate = %+v, want it to say grain has no record", g)
		}
	})

	t.Run("not looked", func(t *testing.T) {
		if g := ciGateWith(t, nil); g.Detail != "waiting for checks" {
			t.Fatalf("gate = %+v, want the old placeholder when nobody read the checks", g)
		}
	})
}

// Grain's own CI verdict, where there is one, still decides the gate.
func TestCIGatePrefersGrainCIOverGitHubChecks(t *testing.T) {
	t.Parallel()
	task := model.Task{ID: "t1", Links: []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#7"}}}
	obs := &model.Observation{TaskID: "t1", CI: &model.CIStatus{RunID: "r1", Conclusion: "failure", Detail: "go failed"}}
	checks := &model.HeadChecks{Passed: 3}
	for _, g := range model.ComputeMergeGatesWithChecks(task, obs, nil, checks) {
		if g.ID == model.GateCI && (g.State != model.GateStateFailing || g.Detail != "go failed") {
			t.Fatalf("gate = %+v, want grain CI's failure", g)
		}
	}
}

func TestFormatFindingsSummary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		findings []model.FindingSummaryItem
		want     string
	}{
		{"none", nil, ""},
		{"one located", []model.FindingSummaryItem{{Path: "pkg/x.go", Line: 12, Body: "The lock is never released. It leaks on error."}},
			"1 finding: `pkg/x.go:12` The lock is never released."},
		{"several", []model.FindingSummaryItem{
			{Path: "pkg/x.go", Line: 12, Body: "**P1** Unchecked error\nmore detail"},
			{Body: "second"}, {Body: "third"},
		}, "3 findings: `pkg/x.go:12` **P1** Unchecked error"},
		{"unlocated", []model.FindingSummaryItem{{Body: "- Missing test for the retry path"}, {Body: "  "}},
			"1 finding: Missing test for the retry path"},
	} {
		if got := model.FormatFindingsSummary(tc.findings); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := model.SummaryFromClosingNote("5381", "## Review\n\nTwo issues in the retry loop. Details below."); got != "review 5381: Two issues in the retry loop." {
		t.Errorf("closing-note summary = %q", got)
	}
}

// A findings record left with only "review N left findings" is rewritten
// with what the review found, and nothing else about it changes.
func TestReplaceReviewFindingsSummaryRewritesOnlyBareSummaries(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	task := model.Task{ID: "t1"}
	task.Links = append(task.Links,
		model.ReviewFindingsRecord{Gate: "trigger:every-pr", ReviewTask: "r1", Summary: "review r1 left findings", HeadSHA: "h1", At: at}.Link(),
		model.ReviewFindingsRecord{Gate: model.GateSecurityReview, ReviewTask: "r1", Summary: "already says something", At: at}.Link(),
	)
	if !model.IsBareFindingsSummary("review 5381 left findings") || model.IsBareFindingsSummary("1 finding: x") {
		t.Fatal("IsBareFindingsSummary misreads")
	}
	if !model.ReplaceReviewFindingsSummary(&task, "r1", "2 findings: `a.go:1` Bad.") {
		t.Fatal("nothing replaced")
	}
	if model.ReplaceReviewFindingsSummary(&task, "r1", "again") {
		t.Fatal("a rewritten summary was rewritten again")
	}
	got := model.ReviewFindings(task)
	if len(got) != 2 || got[0].Summary != "2 findings: `a.go:1` Bad." || got[0].HeadSHA != "h1" || got[0].Gate != "trigger:every-pr" ||
		got[1].Summary != "already says something" {
		t.Fatalf("records = %+v", got)
	}
}
