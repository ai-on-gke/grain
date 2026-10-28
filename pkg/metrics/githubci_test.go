package metrics_test

// GitHubCI: the CI beside grain's own, from github_ci_job alone. The
// fixtures are the shapes ingestion really produces -- a re-run on the
// same commit, a late result for a commit a branch has moved past, a
// red that started before the window -- and the last case holds the
// promise the section was built under: grain CI's numbers are the same
// bytes with or without any GitHub rows beside them.

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/metrics"
	"github.com/ai-on-gke/grain/pkg/model"
)

var gitHubCheckRunID int64

// ghJob is one concluded GitHub job, d into the window.
func ghJob(repo, ref, commit, workflow, name string, attempt int, conclusion string, d time.Duration) model.GitHubCIOutcome {
	gitHubCheckRunID++
	return model.GitHubCIOutcome{
		Repo: repo, CheckRunID: gitHubCheckRunID, Ref: ref, Commit: commit,
		Workflow: workflow, JobName: name, RunAttempt: attempt, Conclusion: conclusion,
		At: since.Add(d),
	}
}

func TestGitHubCIRatesCountOnlyWhatARunDecided(t *testing.T) {
	const repo = "acme/widgets"
	h := time.Hour
	rep := metrics.Compute(metrics.Input{
		Window: win,
		GitHubCIJobs: []model.GitHubCIOutcome{
			ghJob(repo, "pr-1", "c1", "CI", "build", 1, "success", 1*h),
			ghJob(repo, "pr-1", "c1", "CI", "test", 1, "failure", 1*h),
			ghJob(repo, "pr-2", "c2", "CI", "build", 1, "success", 2*h),
			ghJob(repo, "pr-2", "c2", "CI", "test", 1, "timed_out", 2*h),
			ghJob(repo, "pr-3", "c3", "CI", "build", 1, "success", 3*h),
			ghJob(repo, "pr-3", "c3", "CI", "test", 1, "cancelled", 3*h),
			ghJob(repo, "pr-3", "c3", "CI", "deploy", 1, "skipped", 3*h),
			ghJob(repo, "pr-3", "c3", "", "codecov/patch", 0, "neutral", 3*h),
			ghJob(repo, "pr-3", "c3", "", "cla", 0, "action_required", 3*h),
			// Outside the window: in no count.
			ghJob(repo, "pr-0", "c0", "CI", "test", 1, "failure", -2*h),
		},
	})
	g := rep.GitHubCI
	if g.Source != "github" {
		t.Errorf("Source = %q, want github", g.Source)
	}
	want := metrics.GitHubCIOutcomes{
		Jobs: 9, Passed: 3, Failed: 2, Cancelled: 1, Skipped: 1, Neutral: 1, Other: 1,
		PassRate: 3.0 / 6, FailRate: 2.0 / 6, CancelRate: 1.0 / 6,
	}
	if g.GitHubCIOutcomes != want {
		t.Errorf("outcomes = %+v\nwant       %+v", g.GitHubCIOutcomes, want)
	}
	if len(g.ByJob) != 5 {
		t.Fatalf("ByJob = %+v, want 5 jobs", g.ByJob)
	}
	// Worst first: the job with the failures, then the busiest.
	if first := g.ByJob[0]; first.Job != "CI / test" || first.Failed != 2 || first.Cancelled != 1 || first.Source != "github" {
		t.Errorf("ByJob[0] = %+v, want CI / test with 2 failed and 1 cancelled", first)
	}
	if second := g.ByJob[1]; second.Job != "CI / build" || second.PassRate != 1 {
		t.Errorf("ByJob[1] = %+v, want CI / build passing every time", second)
	}
	for _, r := range g.ByJob {
		if r.Job == "CI / deploy" && (r.Skipped != 1 || r.PassRate != 0) {
			t.Errorf("a skipped job is no pass: %+v", r)
		}
	}
}

func TestGitHubCITimeToGreenAndFlakes(t *testing.T) {
	const repo = "acme/widgets"
	h := time.Hour
	rep := metrics.Compute(metrics.Input{
		Window: win,
		GitHubCIJobs: []model.GitHubCIOutcome{
			// c1: build fails, is re-run and passes -- green 2h after the
			// failure, and a flake.
			ghJob(repo, "pr-1", "c1", "CI", "build", 1, "failure", 1*h),
			ghJob(repo, "pr-1", "c1", "CI", "lint", 1, "success", 1*h),
			ghJob(repo, "pr-1", "c1", "CI", "build", 2, "success", 3*h),
			// c2: fails and is never fixed.
			ghJob(repo, "pr-2", "c2", "CI", "test", 1, "failure", 5*h),
			ghJob(repo, "pr-2", "c2", "CI", "build", 1, "success", 5*h),
			// c3: went red before the window and green inside it, measured
			// from the failure that started it. Lint passing first does
			// not make it green while test is still failed.
			ghJob(repo, "pr-3", "c3", "CI", "test", 1, "failure", -2*h),
			ghJob(repo, "pr-3", "c3", "CI", "lint", 1, "success", 0),
			ghJob(repo, "pr-3", "c3", "CI", "test", 1, "success", 4*h),
			// c4: a cancelled re-run does not turn it green.
			ghJob(repo, "pr-4", "c4", "CI", "e2e", 1, "failure", 10*h),
			ghJob(repo, "pr-4", "c4", "CI", "e2e", 2, "cancelled", 11*h),
		},
	})
	ttg := rep.GitHubCI.TimeToGreen
	if ttg.Recovered != 2 || ttg.StillRed != 2 {
		t.Errorf("recovered %d, still red %d; want 2 and 2", ttg.Recovered, ttg.StillRed)
	}
	if ttg.Duration.N != 2 || ttg.Duration.Max != 6*h {
		t.Errorf("Duration = %+v, want 2 samples, the longest 6h", ttg.Duration)
	}
	if l := ttg.Longest; l == nil || l.Commit != "c3" || l.Ref != "pr-3" || l.Took != 6*h {
		t.Errorf("Longest = %+v, want c3 on pr-3 after 6h", l)
	}
	g := rep.GitHubCI
	if g.FlakyCommits != 2 || len(g.Flaky) != 2 {
		t.Fatalf("flaky = %d commits %+v; want c1's build and c3's test", g.FlakyCommits, g.Flaky)
	}
	for _, f := range g.Flaky {
		if f.Commits != 1 || f.Source != "github" {
			t.Errorf("flake %+v, want one commit each", f)
		}
		if f.Job == "CI / build" && (f.Last != "c1" || !f.LastAt.Equal(since.Add(3*h))) {
			t.Errorf("build flake = %+v, want c1 at 3h", f)
		}
	}
	for _, r := range g.ByJob {
		if r.Job == "CI / build" && r.Flaky != 1 {
			t.Errorf("ByJob build Flaky = %d, want 1", r.Flaky)
		}
	}
}

func TestGitHubCIBaseBranchRedAndGreenTime(t *testing.T) {
	const repo = "acme/widgets"
	h := time.Hour
	rep := metrics.Compute(metrics.Input{
		Window:             win,
		GitHubBaseBranches: []metrics.BranchRef{{Repo: repo, Branch: "main"}, {Repo: repo, Branch: "release"}},
		GitHubCIJobs: []model.GitHubCIOutcome{
			ghJob(repo, "main", "a", "CI", "build", 1, "success", 10*h),
			ghJob(repo, "main", "b", "CI", "build", 1, "failure", 20*h),
			// A late failure for a commit main has moved past says nothing
			// about main.
			ghJob(repo, "main", "a", "CI", "lint", 1, "failure", 25*h),
			ghJob(repo, "main", "b", "CI", "build", 2, "success", 30*h),
			ghJob(repo, "main", "c", "CI", "build", 1, "failure", 160*h),
			// Not a base branch: red here is a pull request's business.
			ghJob(repo, "feature", "f", "CI", "build", 1, "failure", 40*h),
		},
	})
	bases := rep.GitHubCI.BaseBranches
	if len(bases) != 1 {
		t.Fatalf("BaseBranches = %+v, want main alone (release reported nothing)", bases)
	}
	m := bases[0]
	if m.Branch != "main" || m.Source != "github" || m.Jobs != 5 {
		t.Errorf("base = %+v, want main with 5 results", m)
	}
	if m.Red != 18*h || m.Green != 140*h {
		t.Errorf("red/green = %s/%s, want 18h/140h", m.Red, m.Green)
	}
	if m.RedStretches != 2 || m.LongestRed != 10*h || !m.RedNow {
		t.Errorf("stretches %d, longest %s, red now %v; want 2, 10h, true", m.RedStretches, m.LongestRed, m.RedNow)
	}
	if got, want := m.RedShare, 18.0/158; got != want {
		t.Errorf("RedShare = %v, want %v", got, want)
	}
}

func TestGitHubCIIsNarrowedByRepo(t *testing.T) {
	rep := metrics.Compute(metrics.Input{
		Window: win,
		Scope:  metrics.Scope{Repos: []string{"acme/widgets"}},
		GitHubCIJobs: []model.GitHubCIOutcome{
			ghJob("acme/widgets", "pr-1", "c1", "CI", "build", 1, "success", time.Hour),
			ghJob("acme/gadgets", "pr-1", "c9", "CI", "build", 1, "failure", time.Hour),
		},
	})
	if g := rep.GitHubCI; g.Jobs != 1 || g.Failed != 0 {
		t.Errorf("scoped GitHubCI = %+v, want acme/widgets' one pass", g.GitHubCIOutcomes)
	}
}

// The promise this section was added under: grain CI's numbers come
// from ci_run as they always did, so GitHub rows beside them change
// nothing but GitHubCI itself -- byte for byte, over the whole report.
func TestGitHubCILeavesGrainCIAlone(t *testing.T) {
	h := time.Hour
	base := metrics.Input{
		Window: win,
		CIJobs: []model.CIJobTiming{
			job("acme/widgets", "go", 3*h, 18*time.Minute, 6*time.Minute, "slot", "for one of the 3 sandboxes"),
			job("acme/widgets", "lint", 4*h, 0, 6*time.Minute, "", ""),
		},
		MergeBatches: []model.MergeBatchRecord{
			batch(3, 0, 3, model.MergeBatchMerged, 10*h),
		},
		MergeBatchSize: 3,
	}
	with := base
	with.GitHubCIJobs = []model.GitHubCIOutcome{
		ghJob("acme/widgets", "main", "a", "CI", "build", 1, "failure", 2*h),
		ghJob("acme/widgets", "main", "a", "CI", "build", 2, "success", 3*h),
		ghJob("acme/widgets", "pr-1", "c1", "", "codecov", 0, "cancelled", 5*h),
	}
	with.GitHubBaseBranches = []metrics.BranchRef{{Repo: "acme/widgets", Branch: "main"}}

	without, withGitHub := metrics.Compute(base), metrics.Compute(with)
	if withGitHub.GitHubCI.Jobs != 3 {
		t.Fatalf("GitHubCI.Jobs = %d, want the 3 GitHub rows counted", withGitHub.GitHubCI.Jobs)
	}
	for name, sections := range map[string][2]any{
		"ciQueueing": {without.CIQueueing, withGitHub.CIQueueing},
		"checks":     {without.Checks, withGitHub.Checks},
		"merges":     {without.Merges, withGitHub.Merges},
	} {
		a, b := mustJSON(t, sections[0]), mustJSON(t, sections[1])
		if !bytes.Equal(a, b) {
			t.Errorf("%s changed with GitHub rows present:\nwithout %s\nwith    %s", name, a, b)
		}
	}
	withGitHub.GitHubCI, without.GitHubCI = metrics.GitHubCI{}, metrics.GitHubCI{}
	if a, b := mustJSON(t, without), mustJSON(t, withGitHub); !bytes.Equal(a, b) {
		t.Errorf("the report outside GitHubCI changed with GitHub rows present")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
