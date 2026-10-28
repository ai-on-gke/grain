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

package metrics

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// GitHubCI is the CI this deployment's repositories run that grain does
// not: GitHub Actions jobs and the check runs other providers post,
// recorded in github_ci_job (docs/github-ci.md). It sits beside grain's
// own CI rather than inside it, and it is computed from those rows
// alone: CIQueueing, Checks and Merges read ci_run exactly as they did
// before this section existed, so a deployment that starts recording
// GitHub CI sees none of its grain-CI numbers move
// (TestGitHubCILeavesGrainCIAlone).
//
// Source labels every number here, and every row, with where the result
// came from -- "github" -- so a reader that lines this section up against
// grain CI's never has to know which table a figure was read from.
//
// # What the rates are over
//
// Passed, Failed and Cancelled are the three outcomes a job's own run
// decides, and the three rates are shares of their sum. Skipped and
// neutral are reported beside them and in no denominator: a job skipped
// by its workflow's own condition did not pass, and counting it as a
// pass would let a workflow that skips most of its jobs look green.
// Other is every remaining word GitHub uses (action_required, stale) --
// counted so the totals add up, and in no rate either.
type GitHubCI struct {
	Source string
	GitHubCIOutcomes
	// ByJob is the same counts per job (model.GitHubCIJob.JobKey),
	// worst first -- most failures, then most runs. At most
	// MaxGitHubCIJobRows rows; MoreJobs is how many were left off.
	ByJob    []GitHubCIJobRates
	MoreJobs int
	// TimeToGreen is how long a commit stayed red: from its first failed
	// job to the moment every job it had reported was passing again.
	TimeToGreen GitHubCITimeToGreen
	// Flaky is every job that failed and then passed on the same commit,
	// by job, most flakes first. FlakyCommits is how many different
	// commits saw at least one.
	Flaky        []GitHubCIFlake
	FlakyCommits int
	// BaseBranches is how long each base branch grain watches spent red
	// and green by its GitHub results over the window.
	BaseBranches []GitHubCIBaseBranch
}

// MaxGitHubCIJobRows bounds GitHubCI.ByJob and GitHubCI.Flaky, so a
// deployment with hundreds of check names still answers a small report.
const MaxGitHubCIJobRows = 50

// GitHubCIOutcomes is how a set of GitHub jobs concluded (GitHubCI's own
// note on which conclusions count where).
type GitHubCIOutcomes struct {
	Jobs      int
	Passed    int
	Failed    int
	Cancelled int
	Skipped   int
	Neutral   int
	Other     int
	// PassRate, FailRate and CancelRate are shares of Passed + Failed +
	// Cancelled, and zero when there were none.
	PassRate   float64
	FailRate   float64
	CancelRate float64
}

// GitHubCIJobRates is one job's outcomes over the window.
type GitHubCIJobRates struct {
	Source string
	Repo   string
	Job    string
	GitHubCIOutcomes
	// Flaky is how many commits this job failed and then passed on.
	Flaky int
}

// GitHubCITimeToGreen is how long commits stayed red.
type GitHubCITimeToGreen struct {
	// Recovered is how many commits went green within the window after a
	// failure, and Duration how long each took, from its first failure.
	Recovered int
	Duration  Distribution
	// StillRed is how many commits went red within the window and had not
	// gone green by its end -- abandoned, or still being worked.
	StillRed int
	// Longest is the commit that took longest to recover, nil with none.
	Longest *GitHubCICommit
}

// GitHubCICommit names one commit a number is about.
type GitHubCICommit struct {
	Repo   string
	Ref    string
	Commit string
	Took   time.Duration
}

// GitHubCIFlake is a job that failed and then passed on the same commit
// -- a re-run (a higher run attempt) or a later run of the same job.
type GitHubCIFlake struct {
	Source  string
	Repo    string
	Job     string
	Commits int
	// Last is the most recent commit it flaked on, and LastAt when the
	// pass that made it a flake came in.
	Last   string
	LastAt time.Time
}

// GitHubCIBaseBranch is one base branch's GitHub verdict over time.
//
// A branch is red from the moment its newest commit has a job whose
// latest result failed, until a newer commit's results (or a re-run on
// the same commit) leave no job failed. Before any result in the window
// the branch is in whatever state the last result before it left it;
// before any result at all it is neither, and that time is in no total.
type GitHubCIBaseBranch struct {
	Source string
	Repo   string
	Branch string
	// Jobs is how many results the branch got within the window.
	Jobs  int
	Red   time.Duration
	Green time.Duration
	// RedShare is Red over Red + Green.
	RedShare float64
	// RedStretches is how many times the branch turned red within the
	// window, and LongestRed the longest stretch of red that overlapped
	// it (clipped to the window).
	RedStretches int
	LongestRed   time.Duration
	// RedNow is whether the branch was red at the window's end.
	RedNow bool
}

// BranchRef is a repository ("owner/name") and a branch of it.
type BranchRef struct {
	Repo   string
	Branch string
}

// add classifies one conclusion into the counts above.
func (o *GitHubCIOutcomes) add(conclusion string) {
	o.Jobs++
	switch c := strings.ToLower(conclusion); {
	case c == "success":
		o.Passed++
	case model.GitHubCIFailed(c):
		o.Failed++
	case c == "cancelled":
		o.Cancelled++
	case c == "skipped":
		o.Skipped++
	case c == "neutral":
		o.Neutral++
	default:
		o.Other++
	}
}

func (o *GitHubCIOutcomes) finish() {
	decided := o.Passed + o.Failed + o.Cancelled
	if decided == 0 {
		return
	}
	o.PassRate = float64(o.Passed) / float64(decided)
	o.FailRate = float64(o.Failed) / float64(decided)
	o.CancelRate = float64(o.Cancelled) / float64(decided)
}

// gitHubCIPassing is whether a conclusion leaves a commit green: a pass,
// or a job that decided not to run. A cancelled job is not passing --
// nothing said the commit is good -- and not failing either.
func gitHubCIPassing(conclusion string) bool {
	switch strings.ToLower(conclusion) {
	case "success", "skipped", "neutral":
		return true
	}
	return false
}

// gitHubCIOf builds the section. jobs is the whole table, oldest first
// (Store.GitHubCIOutcomes); everything is windowed here against each
// job's own conclusion, and a commit or branch that went red before the
// window is followed from its own beginning, so a recovery inside the
// window is measured from the failure that started it.
func gitHubCIOf(w Window, jobs []model.GitHubCIOutcome, bases []BranchRef) GitHubCI {
	out := GitHubCI{Source: model.GitHubCISource}
	// Ties broken the way a re-run orders them: a higher attempt is the
	// later verdict, and the check-run id after that, so the same rows
	// always replay in the same order.
	jobs = slices.Clone(jobs)
	sort.SliceStable(jobs, func(i, j int) bool {
		a, b := jobs[i], jobs[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if a.RunAttempt != b.RunAttempt {
			return a.RunAttempt < b.RunAttempt
		}
		return a.CheckRunID < b.CheckRunID
	})

	byJob := map[[2]string]*GitHubCIJobRates{}
	var jobOrder [][2]string
	for _, j := range jobs {
		at := j.At
		if !w.holds(&at) {
			continue
		}
		out.add(j.Conclusion)
		key := [2]string{j.Repo, j.JobKey()}
		r, ok := byJob[key]
		if !ok {
			r = &GitHubCIJobRates{Source: model.GitHubCISource, Repo: j.Repo, Job: j.JobKey()}
			byJob[key] = r
			jobOrder = append(jobOrder, key)
		}
		r.add(j.Conclusion)
	}
	out.finish()

	// --- per commit: time to green, and flakes ------------------------
	type commitKey struct{ repo, commit string }
	byCommit := map[commitKey][]model.GitHubCIOutcome{}
	var commitOrder []commitKey
	for _, j := range jobs {
		if j.Commit == "" {
			continue
		}
		k := commitKey{j.Repo, j.Commit}
		if _, ok := byCommit[k]; !ok {
			commitOrder = append(commitOrder, k)
		}
		byCommit[k] = append(byCommit[k], j)
	}
	var greens []time.Duration
	flakes := map[[2]string]*GitHubCIFlake{}
	var flakeOrder [][2]string
	for _, k := range commitOrder {
		events := byCommit[k]
		latest := map[string]string{}
		failedOnce := map[string]bool{}
		var redSince *time.Time
		flaked := map[string]bool{}
		for _, e := range events {
			key := e.JobKey()
			latest[key] = e.Conclusion
			at := e.At
			if model.GitHubCIFailed(e.Conclusion) {
				failedOnce[key] = true
				if redSince == nil {
					redSince = &at
				}
				continue
			}
			if strings.EqualFold(e.Conclusion, "success") && failedOnce[key] {
				// Failed, then passed, with nothing between them but
				// time: the job's own verdict on this commit changed.
				failedOnce[key] = false
				if w.holds(&at) && !flaked[key] {
					flaked[key] = true
					fk := [2]string{k.repo, key}
					f, ok := flakes[fk]
					if !ok {
						f = &GitHubCIFlake{Source: model.GitHubCISource, Repo: k.repo, Job: key}
						flakes[fk] = f
						flakeOrder = append(flakeOrder, fk)
					}
					f.Commits++
					f.Last, f.LastAt = k.commit, at
					if r := byJob[fk]; r != nil {
						r.Flaky++
					}
				}
			}
			if redSince == nil || !allPassing(latest) {
				continue
			}
			if w.holds(&at) {
				took := at.Sub(*redSince)
				greens = append(greens, took)
				out.TimeToGreen.Recovered++
				if l := out.TimeToGreen.Longest; l == nil || took > l.Took {
					out.TimeToGreen.Longest = &GitHubCICommit{Repo: k.repo, Ref: e.Ref, Commit: k.commit, Took: took}
				}
			}
			redSince = nil
		}
		if redSince != nil && w.holds(redSince) {
			out.TimeToGreen.StillRed++
		}
		if len(flaked) > 0 {
			out.FlakyCommits++
		}
	}
	out.TimeToGreen.Duration = summarize(greens)

	for _, key := range jobOrder {
		r := byJob[key]
		r.finish()
		out.ByJob = append(out.ByJob, *r)
	}
	sort.SliceStable(out.ByJob, func(i, j int) bool {
		a, b := out.ByJob[i], out.ByJob[j]
		if a.Failed != b.Failed {
			return a.Failed > b.Failed
		}
		if a.Jobs != b.Jobs {
			return a.Jobs > b.Jobs
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Job < b.Job
	})
	if len(out.ByJob) > MaxGitHubCIJobRows {
		out.MoreJobs = len(out.ByJob) - MaxGitHubCIJobRows
		out.ByJob = out.ByJob[:MaxGitHubCIJobRows]
	}
	for _, key := range flakeOrder {
		out.Flaky = append(out.Flaky, *flakes[key])
	}
	sort.SliceStable(out.Flaky, func(i, j int) bool {
		a, b := out.Flaky[i], out.Flaky[j]
		if a.Commits != b.Commits {
			return a.Commits > b.Commits
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Job < b.Job
	})
	if len(out.Flaky) > MaxGitHubCIJobRows {
		out.Flaky = out.Flaky[:MaxGitHubCIJobRows]
	}

	out.BaseBranches = gitHubBaseBranchesOf(w, jobs, bases)
	return out
}

// allPassing is whether every job a commit has reported is, at its
// latest, passing (gitHubCIPassing).
func allPassing(latest map[string]string) bool {
	for _, c := range latest {
		if !gitHubCIPassing(c) {
			return false
		}
	}
	return true
}

// gitHubBaseBranchesOf replays each base branch's results in order and
// measures how long it spent red and green inside the window
// (GitHubCIBaseBranch on what red means). A branch with no results at
// all is left out rather than reported as zero of both.
func gitHubBaseBranchesOf(w Window, jobs []model.GitHubCIOutcome, bases []BranchRef) []GitHubCIBaseBranch {
	var out []GitHubCIBaseBranch
	seen := map[BranchRef]bool{}
	for _, b := range bases {
		if seen[b] || b.Repo == "" || b.Branch == "" {
			continue
		}
		seen[b] = true
		row := GitHubCIBaseBranch{Source: model.GitHubCISource, Repo: b.Repo, Branch: b.Branch}
		// The newest commit to have reported anything decides the
		// branch's state, by the latest result of each of its jobs.
		var head string
		var latest map[string]string
		known, red := false, false
		var since time.Time // when the current state began
		var redStart time.Time
		reported := false
		older := map[string]bool{}
		// span adds the stretch [since, until) in the current state,
		// clipped to the window.
		span := func(until time.Time) {
			if !known {
				return
			}
			from, to := since, until
			if from.Before(w.Since) {
				from = w.Since
			}
			if to.After(w.Until) {
				to = w.Until
			}
			if !to.After(from) {
				return
			}
			if red {
				row.Red += to.Sub(from)
			} else {
				row.Green += to.Sub(from)
			}
		}
		closeRed := func(until time.Time) {
			from, to := redStart, until
			if from.Before(w.Since) {
				from = w.Since
			}
			if to.After(w.Until) {
				to = w.Until
			}
			if d := to.Sub(from); d > row.LongestRed {
				row.LongestRed = d
			}
		}
		for _, j := range jobs {
			if j.Repo != b.Repo || j.Ref != b.Branch || j.At.After(w.Until) {
				continue
			}
			reported = true
			at := j.At
			if w.holds(&at) {
				row.Jobs++
			}
			if j.Commit != head || latest == nil {
				// A result for a commit the branch has already moved past
				// says nothing about its tip. Commits are met in the order
				// they first reported, so any commit met before that is
				// not the head is older than it.
				if older[j.Commit] {
					continue
				}
				if latest != nil {
					older[head] = true
				}
				head, latest = j.Commit, map[string]string{}
			}
			latest[j.JobKey()] = j.Conclusion
			nowRed := false
			for _, c := range latest {
				if model.GitHubCIFailed(c) {
					nowRed = true
					break
				}
			}
			if known && nowRed == red {
				continue
			}
			span(at)
			if red && known {
				closeRed(at)
			}
			if nowRed {
				redStart = at
				if w.holds(&at) {
					row.RedStretches++
				}
			}
			known, red, since = true, nowRed, at
		}
		if !reported {
			continue
		}
		span(w.Until)
		if known && red {
			closeRed(w.Until)
			row.RedNow = true
		}
		if total := row.Red + row.Green; total > 0 {
			row.RedShare = float64(row.Red) / float64(total)
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Red != out[j].Red {
			return out[i].Red > out[j].Red
		}
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Branch < out[j].Branch
	})
	return out
}
