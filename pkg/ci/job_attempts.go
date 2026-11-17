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

// Job-level history read from every attempt of every job of a set of
// GitHub Actions runs (GET .../actions/runs/{id}/jobs?filter=all): how
// often a job passed, passed only on its retry, or failed, per commit.
// It is the verdict pkg/orchestrator/releasestage1.go and
// scripts/candidate-gate.sh check-qualification reach on a release
// candidate's Stage 1 (nightly.yml), so `grain ci test-history <job>
// --workflow nightly.yml --ref 'v*-rc.*'` and `grain ci flake-report`
// count real executions rather than the failing log tails grain keeps.

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
)

// The verdicts of one job on one commit.
const (
	JobVerdictPass        = "pass"
	JobVerdictPassOnRetry = "pass-on-retry"
	JobVerdictFail        = "fail"
	JobVerdictInfra       = "infra"
	JobVerdictPending     = "pending"
)

// FlakeBudgetPercent is the release playbook's flake budget: the share
// of candidates a Stage 1 job may spend its retry on.
const FlakeBudgetPercent = 2.0

// Stage1RollupJob is nightly.yml's roll-up of every other job, which
// carries no verdict of its own (candidate-gate.sh's
// CHECK_QUALIFICATION_IGNORE): job-level reports leave it out.
const Stage1RollupJob = "Stage 1 qualification verdict"

// MaxAttemptRuns bounds the runs a job-level history or flake report
// reads every job attempt of, one forge request (or more) each: a week of
// nightly.yml on every release candidate is about 60.
const MaxAttemptRuns = 150

// JobAttemptRun is one workflow run and every attempt of every job of it.
type JobAttemptRun struct {
	Run  Run
	Jobs []GitHubJob
}

// JobCommitVerdict is one job's attempts on one commit, oldest first
// (pending, success, failure, infra or skipped), and what they add up to.
type JobCommitVerdict struct {
	Job       string     `json:"job"`
	Commit    string     `json:"commit"`
	Ref       string     `json:"ref,omitempty"`
	RunID     string     `json:"runId,omitempty"`
	Attempts  []string   `json:"attempts"`
	Verdict   string     `json:"verdict"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// ConfirmedRed is whether the job failed again on its retry.
func (v JobCommitVerdict) ConfirmedRed() bool {
	n := 0
	for _, a := range v.Attempts {
		if a == "failure" {
			n++
		}
	}
	return n >= 2
}

// JobAttemptSummary is one job's verdicts across the commits it ran on.
type JobAttemptSummary struct {
	Job           string `json:"job"`
	Commits       int    `json:"commits"`
	Passed        int    `json:"passed"`
	PassedOnRetry int    `json:"passedOnRetry"`
	Failed        int    `json:"failed"`
	// ConfirmedRed are the Failed commits whose retry failed too.
	ConfirmedRed int `json:"confirmedRed"`
	Infra        int `json:"infra"`
	Pending      int `json:"pending"`
	// Verdicts are each commit's, newest first.
	Verdicts []JobCommitVerdict `json:"verdicts,omitempty"`
}

// Decisive is the commits the job passed or failed on.
func (s JobAttemptSummary) Decisive() int { return s.Passed + s.PassedOnRetry + s.Failed }

// RetryRate is the share of decisive commits that passed only on a retry,
// in percent.
func (s JobAttemptSummary) RetryRate() float64 {
	if s.Decisive() == 0 {
		return 0
	}
	return float64(s.PassedOnRetry) / float64(s.Decisive()) * 100
}

// AttemptJobName is a job's name without its matrix suffix -- "go,
// shuffled and repeated (3)" is a shard of "go, shuffled and repeated" --
// as candidate-gate.sh judges every shard of a job as one.
func AttemptJobName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, " ("); i > 0 && strings.HasSuffix(name, ")") {
		return name[:i]
	}
	return name
}

// attemptClass is the class of one job attempt's outcome, as
// candidate-gate.sh's JQ_ATTEMPTS has it.
func attemptClass(j GitHubJob) string {
	if j.Status != "" && j.Status != "completed" {
		return "pending"
	}
	c := ""
	if j.Conclusion != nil {
		c = *j.Conclusion
	}
	switch c {
	case "success":
		return "success"
	case "skipped":
		return "skipped"
	case "cancelled", "timed_out", "startup_failure", "stale", "neutral":
		return "infra"
	}
	return "failure"
}

// JobAttemptSummaries judges every job of runs commit by commit, as
// check-qualification does: every shard of one attempt is one verdict,
// attempts across the runs on a commit (a re-dispatched workflow is a
// new run) are read oldest first, and a job that failed then passed has
// passed on its retry. rollup names jobs that only roll the others up
// and carry no verdict of their own (nightly.yml's "Stage 1
// qualification verdict"); they are left out. Summaries are sorted by
// job name.
func JobAttemptSummaries(runs []JobAttemptRun, rollup ...string) []JobAttemptSummary {
	type attemptKey struct {
		run     int64
		attempt int
	}
	type commitKey struct{ job, sha string }
	type commitData struct {
		ref, runID string
		started    *time.Time
		byAttempt  map[attemptKey][]string
	}
	data := map[commitKey]*commitData{}
	for _, r := range runs {
		for _, j := range r.Jobs {
			name := AttemptJobName(j.Name)
			if name == "" || slices.ContainsFunc(rollup, func(s string) bool { return strings.EqualFold(s, name) }) {
				continue
			}
			sha := j.HeadSHA
			if sha == "" {
				sha = r.Run.Commit
			}
			k := commitKey{job: name, sha: strings.ToLower(sha)}
			d := data[k]
			if d == nil {
				d = &commitData{byAttempt: map[attemptKey][]string{}}
				data[k] = d
			}
			if d.ref == "" {
				d.ref = r.Run.Ref
			}
			if at := r.Run.StartedAt; at != nil && (d.started == nil || at.After(*d.started)) {
				d.started, d.runID = at, r.Run.ID
			} else if d.runID == "" {
				d.runID = r.Run.ID
			}
			ak := attemptKey{run: j.RunID, attempt: max(j.RunAttempt, 1)}
			d.byAttempt[ak] = append(d.byAttempt[ak], attemptClass(j))
		}
	}
	byJob := map[string]*JobAttemptSummary{}
	for k, d := range data {
		keys := make([]attemptKey, 0, len(d.byAttempt))
		for ak := range d.byAttempt {
			keys = append(keys, ak)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].run != keys[j].run {
				return keys[i].run < keys[j].run
			}
			return keys[i].attempt < keys[j].attempt
		})
		v := JobCommitVerdict{Job: k.job, Commit: k.sha, Ref: d.ref, RunID: d.runID, StartedAt: d.started}
		last := ""
		for _, ak := range keys {
			c := "skipped"
			for _, rank := range []string{"pending", "failure", "infra", "success"} {
				if slices.Contains(d.byAttempt[ak], rank) {
					c = rank
					break
				}
			}
			// A skipped attempt after a failure or infrastructure one is a
			// dispatched retry the job did not run in: infrastructure, as
			// candidate-gate.sh has it.
			if c == "skipped" && (last == "failure" || last == "infra") {
				c = "infra"
			}
			if c != "skipped" || len(keys) == 1 {
				v.Attempts = append(v.Attempts, c)
				last = c
			}
		}
		if len(v.Attempts) == 0 {
			continue
		}
		v.Verdict = commitVerdict(v.Attempts)
		if v.Verdict == "" {
			continue // skipped: the job never ran on this commit
		}
		s := byJob[k.job]
		if s == nil {
			s = &JobAttemptSummary{Job: k.job}
			byJob[k.job] = s
		}
		s.Commits++
		switch v.Verdict {
		case JobVerdictPass:
			s.Passed++
		case JobVerdictPassOnRetry:
			s.PassedOnRetry++
		case JobVerdictFail:
			s.Failed++
			if v.ConfirmedRed() {
				s.ConfirmedRed++
			}
		case JobVerdictInfra:
			s.Infra++
		case JobVerdictPending:
			s.Pending++
		}
		s.Verdicts = append(s.Verdicts, v)
	}
	out := make([]JobAttemptSummary, 0, len(byJob))
	for _, s := range byJob {
		sort.SliceStable(s.Verdicts, func(i, j int) bool {
			ti, tj := timeValue(s.Verdicts[i].StartedAt), timeValue(s.Verdicts[j].StartedAt)
			if !ti.Equal(tj) {
				return ti.After(tj)
			}
			return s.Verdicts[i].Commit < s.Verdicts[j].Commit
		})
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Job < out[j].Job })
	return out
}

// commitVerdict is what a job's attempts on one commit add up to: two
// failures confirm it red regardless of any later attempt, as
// check-qualification judges Stage 1; otherwise its newest attempt
// decides, and a pass after one failure is a pass on retry. "" for a
// job that was only ever skipped.
func commitVerdict(attempts []string) string {
	fails := 0
	for _, a := range attempts {
		if a == "failure" {
			fails++
		}
	}
	if fails >= 2 {
		return JobVerdictFail
	}
	switch attempts[len(attempts)-1] {
	case "success":
		if fails > 0 {
			return JobVerdictPassOnRetry
		}
		return JobVerdictPass
	case "failure":
		return JobVerdictFail
	case "infra":
		return JobVerdictInfra
	case "pending":
		return JobVerdictPending
	}
	return ""
}

// FindJobSummary is the summary of the job named name (without regard to
// case or a matrix suffix), if any.
func FindJobSummary(summaries []JobAttemptSummary, name string) (JobAttemptSummary, bool) {
	name = AttemptJobName(name)
	for _, s := range summaries {
		if strings.EqualFold(s.Job, name) {
			return s, true
		}
	}
	return JobAttemptSummary{}, false
}

// IsRefGlob is whether a --ref is a glob pattern ("v*-rc.*") rather than
// the substring of a branch every other --ref is.
func IsRefGlob(ref string) bool {
	return strings.ContainsAny(ref, "*?[")
}

// MatchRefGlob is whether ref (a branch or tag) matches the glob pattern,
// path.Match's syntax, without regard to case.
func MatchRefGlob(pattern, ref string) bool {
	ok, err := path.Match(strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(strings.TrimSpace(ref)))
	return err == nil && ok
}

// ApplyJobSummary makes report a job-level report of s: its executions
// are the commits the job ran on, judged from every attempt, so the rates
// are over every commit rather than over the failures grain kept logs of.
func (r *TestHistoryReport) ApplyJobSummary(s JobAttemptSummary) {
	job := s
	r.Job = &job
	r.PassesUncounted = false
	r.Executions = s.Decisive()
	r.Passed, r.Flaked, r.Failed = s.Passed, s.PassedOnRetry, s.Failed
	r.Skipped, r.Quarantined = 0, 0
	r.PassRate, r.FailRate, r.FlakeRate, r.QuarantinedRate = 0, 0, 0, 0
	if d := s.Decisive(); d > 0 {
		r.PassRate = float64(s.Passed) / float64(d) * 100
		r.FailRate = float64(s.Failed) / float64(d) * 100
		r.FlakeRate = float64(s.PassedOnRetry) / float64(d) * 100
	}
	r.RecentRuns = nil
	for _, v := range s.Verdicts {
		status := TestStatusPass
		switch v.Verdict {
		case JobVerdictPassOnRetry:
			status = TestStatusFlake
		case JobVerdictFail:
			status = TestStatusFail
		case JobVerdictInfra, JobVerdictPending:
			continue
		}
		r.RecentRuns = append(r.RecentRuns, TestExecution{
			RunID: v.RunID, Ref: v.Ref, Commit: v.Commit, JobName: s.Job, Provider: ProviderGitHubActions,
			Status: status, StartedAt: v.StartedAt, Message: "attempts: " + strings.Join(v.Attempts, ","),
		})
	}
}

// FlakeReport is `grain ci flake-report`'s answer: every job's verdicts
// across the runs of one workflow.
type FlakeReport struct {
	Repo     string              `json:"repo,omitempty"`
	Workflow string              `json:"workflow,omitempty"`
	Ref      string              `json:"ref,omitempty"`
	Since    *time.Time          `json:"since,omitempty"`
	Runs     int                 `json:"runs"`
	Commits  int                 `json:"commits"`
	Jobs     []JobAttemptSummary `json:"jobs"`
	Notes    []string            `json:"notes,omitempty"`
}

// NewFlakeReport is the flake report of runs.
func NewFlakeReport(runs []JobAttemptRun, rollup ...string) FlakeReport {
	r := FlakeReport{Runs: len(runs), Jobs: JobAttemptSummaries(runs, rollup...)}
	commits := map[string]bool{}
	for _, s := range r.Jobs {
		for _, v := range s.Verdicts {
			commits[v.Commit] = true
		}
	}
	r.Commits = len(commits)
	for i := range r.Jobs {
		r.Jobs[i].Verdicts = nil
	}
	return r
}

// Render is the report as a table: per job, the commits it ran on, how
// many passed first time, passed only on retry, or stayed red, and its
// retry rate against FlakeBudgetPercent.
func (r FlakeReport) Render() string {
	var b strings.Builder
	b.WriteString("Flake report")
	var scope []string
	if r.Repo != "" {
		scope = append(scope, r.Repo)
	}
	if r.Workflow != "" {
		scope = append(scope, "workflow "+r.Workflow)
	}
	if r.Ref != "" {
		scope = append(scope, "ref "+r.Ref)
	}
	if r.Since != nil {
		scope = append(scope, "since "+r.Since.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if len(scope) > 0 {
		b.WriteString(": " + strings.Join(scope, ", "))
	}
	fmt.Fprintf(&b, "\nRuns read: %d | Commits: %d | Flake budget: %.0f%% of commits passing on retry\n", r.Runs, r.Commits, FlakeBudgetPercent)
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "  Note: %s\n", n)
	}
	if len(r.Jobs) == 0 {
		b.WriteString("\nNo job attempts found in the requested runs.")
		return b.String()
	}
	width := len("JOB")
	for _, s := range r.Jobs {
		width = max(width, len(s.Job))
	}
	fmt.Fprintf(&b, "\n%-*s  %7s  %4s  %13s  %4s  %13s  %5s  %10s\n", width, "JOB", "COMMITS", "PASS", "PASS-ON-RETRY", "FAIL", "CONFIRMED-RED", "INFRA", "RETRY-RATE")
	for _, s := range r.Jobs {
		over := ""
		if s.RetryRate() > FlakeBudgetPercent {
			over = fmt.Sprintf("  over budget by %.1f pts", s.RetryRate()-FlakeBudgetPercent)
		}
		fmt.Fprintf(&b, "%-*s  %7d  %4d  %13d  %4d  %13d  %5d  %9.1f%%%s\n", width, s.Job, s.Commits, s.Passed, s.PassedOnRetry,
			s.Failed, s.ConfirmedRed, s.Infra, s.RetryRate(), over)
	}
	return strings.TrimRight(b.String(), "\n")
}
