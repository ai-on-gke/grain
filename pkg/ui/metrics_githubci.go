package ui

import (
	"context"
	"time"

	"github.com/bwsalmon/grain/pkg/metrics"
)

// MetricsGitHubCI is the CI beside grain's own over the window: GitHub
// Actions jobs and the check runs other providers post, read from the
// github_ci_job rows alone (metrics.GitHubCI, docs/github-ci.md).
// Nothing in it is computed from ci_run, and nothing computed from
// ci_run -- ciQueueing, checks, merges -- reads it.
//
// source is "github" here and on every row, which is the slice a reader
// lines up against grain CI's sections (source "grain").
//
// passRate, failRate and cancelRate are shares of passed + failed +
// cancelled; skipped, neutral and other are counted beside them and in
// no denominator. failed is GitHub's failure, timed_out and
// startup_failure together.
type MetricsGitHubCI struct {
	Source string `json:"source"`
	MetricsGitHubCIOutcomes
	ByJob    []MetricsGitHubCIJob `json:"byJob"`
	MoreJobs int                  `json:"moreJobs"`
	// TimeToGreen is, per commit, from its first failed job to the moment
	// every job it had reported was passing again.
	TimeToGreen MetricsGitHubCITimeToGreen `json:"timeToGreen"`
	// Flaky is each job that failed and then passed on the same commit
	// (a re-run or a later run), most commits first.
	Flaky        []MetricsGitHubCIFlake `json:"flaky"`
	FlakyCommits int                    `json:"flakyCommits"`
	// BaseBranches is how long each base branch grain watches was red and
	// green by its GitHub results, reddest first.
	BaseBranches []MetricsGitHubCIBaseBranch `json:"baseBranches"`
}

// MetricsGitHubCIOutcomes is how a set of GitHub jobs concluded.
type MetricsGitHubCIOutcomes struct {
	Jobs       int     `json:"jobs"`
	Passed     int     `json:"passed"`
	Failed     int     `json:"failed"`
	Cancelled  int     `json:"cancelled"`
	Skipped    int     `json:"skipped"`
	Neutral    int     `json:"neutral"`
	Other      int     `json:"other"`
	PassRate   float64 `json:"passRate"`
	FailRate   float64 `json:"failRate"`
	CancelRate float64 `json:"cancelRate"`
}

// MetricsGitHubCIJob is one job's outcomes; job is its JobKey
// ("workflow / job", or a check's own name).
type MetricsGitHubCIJob struct {
	Source string `json:"source"`
	Repo   string `json:"repo"`
	Job    string `json:"job"`
	MetricsGitHubCIOutcomes
	Flaky int `json:"flaky"`
}

// MetricsGitHubCITimeToGreen is how long commits stayed red.
type MetricsGitHubCITimeToGreen struct {
	Recovered int                    `json:"recovered"`
	Duration  MetricsDistribution    `json:"duration"`
	StillRed  int                    `json:"stillRed"`
	Longest   *MetricsGitHubCICommit `json:"longest,omitempty"`
}

// MetricsGitHubCICommit is the commit that took longest to go green.
type MetricsGitHubCICommit struct {
	Repo        string  `json:"repo"`
	Ref         string  `json:"ref"`
	Commit      string  `json:"commit"`
	TookSeconds float64 `json:"tookSeconds"`
}

// MetricsGitHubCIFlake is one job that flaked, and on how many commits.
type MetricsGitHubCIFlake struct {
	Source  string    `json:"source"`
	Repo    string    `json:"repo"`
	Job     string    `json:"job"`
	Commits int       `json:"commits"`
	Last    string    `json:"last"`
	LastAt  time.Time `json:"lastAt"`
}

// MetricsGitHubCIBaseBranch is one base branch's red and green time.
type MetricsGitHubCIBaseBranch struct {
	Source            string  `json:"source"`
	Repo              string  `json:"repo"`
	Branch            string  `json:"branch"`
	Jobs              int     `json:"jobs"`
	RedSeconds        float64 `json:"redSeconds"`
	GreenSeconds      float64 `json:"greenSeconds"`
	RedShare          float64 `json:"redShare"`
	RedStretches      int     `json:"redStretches"`
	LongestRedSeconds float64 `json:"longestRedSeconds"`
	RedNow            bool    `json:"redNow"`
}

func gitHubCIOutcomesWire(o metrics.GitHubCIOutcomes) MetricsGitHubCIOutcomes {
	return MetricsGitHubCIOutcomes{
		Jobs: o.Jobs, Passed: o.Passed, Failed: o.Failed, Cancelled: o.Cancelled,
		Skipped: o.Skipped, Neutral: o.Neutral, Other: o.Other,
		PassRate: o.PassRate, FailRate: o.FailRate, CancelRate: o.CancelRate,
	}
}

// gitHubCIWireFrom is the section as /api/metrics serves it. Every list
// is non-nil, so a page reading an empty window iterates nothing rather
// than nothing's absence.
func gitHubCIWireFrom(g metrics.GitHubCI) MetricsGitHubCI {
	out := MetricsGitHubCI{
		Source:                  g.Source,
		MetricsGitHubCIOutcomes: gitHubCIOutcomesWire(g.GitHubCIOutcomes),
		ByJob:                   make([]MetricsGitHubCIJob, 0, len(g.ByJob)),
		MoreJobs:                g.MoreJobs,
		TimeToGreen: MetricsGitHubCITimeToGreen{
			Recovered: g.TimeToGreen.Recovered,
			Duration:  distributionOf(g.TimeToGreen.Duration),
			StillRed:  g.TimeToGreen.StillRed,
		},
		Flaky:        make([]MetricsGitHubCIFlake, 0, len(g.Flaky)),
		FlakyCommits: g.FlakyCommits,
		BaseBranches: make([]MetricsGitHubCIBaseBranch, 0, len(g.BaseBranches)),
	}
	if l := g.TimeToGreen.Longest; l != nil {
		out.TimeToGreen.Longest = &MetricsGitHubCICommit{Repo: l.Repo, Ref: l.Ref, Commit: l.Commit, TookSeconds: l.Took.Seconds()}
	}
	for _, j := range g.ByJob {
		out.ByJob = append(out.ByJob, MetricsGitHubCIJob{
			Source: j.Source, Repo: j.Repo, Job: j.Job,
			MetricsGitHubCIOutcomes: gitHubCIOutcomesWire(j.GitHubCIOutcomes), Flaky: j.Flaky,
		})
	}
	for _, f := range g.Flaky {
		out.Flaky = append(out.Flaky, MetricsGitHubCIFlake(f))
	}
	for _, b := range g.BaseBranches {
		out.BaseBranches = append(out.BaseBranches, MetricsGitHubCIBaseBranch{
			Source: b.Source, Repo: b.Repo, Branch: b.Branch, Jobs: b.Jobs,
			RedSeconds: b.Red.Seconds(), GreenSeconds: b.Green.Seconds(), RedShare: b.RedShare,
			RedStretches: b.RedStretches, LongestRedSeconds: b.LongestRed.Seconds(), RedNow: b.RedNow,
		})
	}
	return out
}

// gitHubBaseBranches is every base branch grain watches
// (Store.ListBaseBranchCI), the branches metrics.GitHubCI measures red
// and green time on.
func (c *Client) gitHubBaseBranches(ctx context.Context) ([]metrics.BranchRef, error) {
	rows, err := c.Store.ListBaseBranchCI(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]metrics.BranchRef, 0, len(rows))
	for _, b := range rows {
		out = append(out, metrics.BranchRef{Repo: b.Repo.String(), Branch: b.Branch})
	}
	return out, nil
}
