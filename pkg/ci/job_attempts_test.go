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

package ci

import (
	"strings"
	"testing"
	"time"
)

func attempt(run int64, n int, sha, name, conclusion string) GitHubJob {
	c := conclusion
	return GitHubJob{RunID: run, RunAttempt: n, HeadSHA: sha, Name: name, Status: "completed", Conclusion: &c}
}

func TestJobAttemptSummariesJudgeEachCommitFromEveryAttempt(t *testing.T) {
	at := time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC)
	later, latest := at.Add(time.Hour), at.Add(2*time.Hour)
	runs := []JobAttemptRun{
		// rc.1: a run that failed, then a re-dispatched run that passed.
		{Run: Run{ID: "gha:a:b:1", Ref: "v1.0.0-rc.1", Commit: "aaa", StartedAt: &at}, Jobs: []GitHubJob{
			attempt(1, 1, "aaa", "ui e2e (1)", "success"),
			attempt(1, 1, "aaa", "ui e2e (2)", "failure"),
			attempt(1, 1, "aaa", "go", "success"),
			attempt(1, 1, "aaa", "Stage 1 qualification verdict", "failure"),
		}},
		{Run: Run{ID: "gha:a:b:2", Ref: "v1.0.0-rc.1", Commit: "aaa", StartedAt: &later}, Jobs: []GitHubJob{
			attempt(2, 1, "aaa", "ui e2e (1)", "success"),
			attempt(2, 1, "aaa", "ui e2e (2)", "success"),
			attempt(2, 1, "aaa", "go", "skipped"),
		}},
		// rc.2: ui e2e failed on its retry too; go was cancelled.
		{Run: Run{ID: "gha:a:b:3", Ref: "v1.0.0-rc.2", Commit: "bbb", StartedAt: &latest}, Jobs: []GitHubJob{
			attempt(3, 1, "bbb", "ui e2e (1)", "failure"),
			attempt(3, 2, "bbb", "ui e2e (1)", "failure"),
			attempt(3, 1, "bbb", "go", "cancelled"),
		}},
	}
	got := JobAttemptSummaries(runs, Stage1RollupJob)
	if len(got) != 2 {
		t.Fatalf("summaries = %+v, want go and ui e2e (the roll-up left out, shards as one)", got)
	}
	goJob, ui := got[0], got[1]
	if goJob.Job != "go" || goJob.Passed != 1 || goJob.Infra != 1 || goJob.Commits != 2 || goJob.Decisive() != 1 {
		t.Errorf("go = %+v, want one pass (a later skip does not undo it) and one infra", goJob)
	}
	if ui.Job != "ui e2e" || ui.PassedOnRetry != 1 || ui.Failed != 1 || ui.ConfirmedRed != 1 || ui.Passed != 0 {
		t.Errorf("ui e2e = %+v, want one pass-on-retry and one confirmed red", ui)
	}
	if ui.RetryRate() != 50 {
		t.Errorf("ui e2e retry rate = %v, want 50", ui.RetryRate())
	}
	if v := ui.Verdicts[0]; v.Ref != "v1.0.0-rc.2" || v.Verdict != JobVerdictFail || strings.Join(v.Attempts, ",") != "failure,failure" {
		t.Errorf("newest ui e2e verdict = %+v", v)
	}
	if s, ok := FindJobSummary(got, "UI E2E (3)"); !ok || s.Job != "ui e2e" {
		t.Errorf("FindJobSummary by a shard's name, in other case = %+v %v", s, ok)
	}

	var report TestHistoryReport
	report.TestName = "ui e2e"
	report.PassesUncounted = true
	report.ApplyJobSummary(ui)
	out := report.Render()
	for _, want := range []string{"Executions found: 2", "Passed on retry: 1 (50.0%", "Confirmed red", "attempts: failure,success"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "count only this test's failures") {
		t.Errorf("a job-level report still says passes are uncounted:\n%s", out)
	}

	flake := NewFlakeReport(runs, Stage1RollupJob)
	flake.Workflow, flake.Ref = "nightly.yml", "v*-rc.*"
	table := flake.Render()
	if flake.Commits != 2 || !strings.Contains(table, "over budget") || !strings.Contains(table, "ui e2e") {
		t.Errorf("flake report = %+v\n%s", flake, table)
	}
}

func TestMatchRefGlob(t *testing.T) {
	for _, c := range []struct {
		pattern, ref string
		want         bool
	}{
		{"v*-rc.*", "v0.9.0-rc.3", true},
		{"v*-rc.*", "v0.9.0", false},
		{"v*-rc.*", "grain/task-1", false},
		{"V*-RC.*", "v1.0.0-rc.1", true},
	} {
		if got := MatchRefGlob(c.pattern, c.ref); got != c.want {
			t.Errorf("MatchRefGlob(%q, %q) = %v, want %v", c.pattern, c.ref, got, c.want)
		}
	}
	if IsRefGlob("main") || !IsRefGlob("v*-rc.*") {
		t.Error("IsRefGlob")
	}
}

func TestConfirmedRedStaysFailPastLaterAttempts(t *testing.T) {
	at := time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC)
	runs := []JobAttemptRun{
		{Run: Run{ID: "gha:a:b:1", Ref: "v1.0.0-rc.1", Commit: "aaa", StartedAt: &at}, Jobs: []GitHubJob{
			attempt(1, 1, "aaa", "ui e2e", "failure"),
			attempt(1, 2, "aaa", "ui e2e", "failure"),
			attempt(1, 3, "aaa", "ui e2e", "success"),
			attempt(1, 1, "aaa", "go", "failure"),
			attempt(1, 2, "aaa", "go", "failure"),
			attempt(1, 3, "aaa", "go", "cancelled"),
		}},
	}
	got := JobAttemptSummaries(runs, Stage1RollupJob)
	if len(got) != 2 {
		t.Fatalf("summaries = %+v, want go and ui e2e", got)
	}
	for _, s := range got {
		if s.Failed != 1 || s.ConfirmedRed != 1 || s.PassedOnRetry != 0 || s.Infra != 0 || s.Decisive() != 1 {
			t.Errorf("%s = %+v, want Failed=1 ConfirmedRed=1 (a job gets only one flake retry)", s.Job, s)
		}
	}
}
