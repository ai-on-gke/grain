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

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GitHubCIJob is one GitHub Actions job, or one check run some other
// provider posted, that grain saw conclude on a repository it works in.
//
// It is grain's record of *somebody else's* CI, kept beside its own
// (ci_run) rather than in it, for one reason: everything that reads
// ci_run -- the merge gate, verdict reuse, a task's CI verdict, the
// metrics' grain-CI numbers -- means "what grain's own CI said", and a
// GitHub result folded into those tables would change every one of them.
// A sibling table with the same shape lets the metrics and the
// shared-fault correlation read both, each labelled with its Source,
// without either source's numbers moving the other's.
//
// An Actions job *is* a check run on GitHub (its job id and check-run id
// are the same number), so the key is the check run: a workflow_job
// delivery, a check_run delivery and a polled check-run listing for the
// same job all land on one row, and a redelivered webhook rewrites it
// rather than counting it twice.
type GitHubCIJob struct {
	Repo       string `json:"repo"`
	CheckRunID int64  `json:"checkRunId"`
	// Source is GitHubCISource for every row today; it is a column so a
	// reader that merges this table with grain's own runs labels each
	// result without having to know which table it came from.
	Source string `json:"source"`
	// App is the GitHub App that posted the check -- "github-actions" for
	// an Actions job, a provider's slug otherwise. Empty when the read it
	// came from did not say.
	App    string `json:"app,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Workflow is the Actions workflow's name, empty for a check run no
	// workflow posted. With JobName it is the job's identity for the
	// metrics and for correlation (JobKey).
	Workflow   string `json:"workflow,omitempty"`
	RunID      int64  `json:"runId,omitempty"`
	RunAttempt int    `json:"runAttempt,omitempty"`
	JobName    string `json:"jobName"`
	// Conclusion is GitHub's own word: success, failure, cancelled,
	// timed_out, skipped, neutral, action_required, startup_failure.
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	// FailingStep is the first step GitHub concluded did not pass, for an
	// Actions job that reported its steps.
	FailingStep string `json:"failingStep,omitempty"`
	RunURL      string `json:"runUrl,omitempty"`
	JobURL      string `json:"jobUrl,omitempty"`
	// Output is the check run's own output (title, summary, text),
	// bounded -- what a check that is not an Actions job has instead of a
	// log.
	Output string `json:"output,omitempty"`
	// FailureTail is the bounded tail of what a failed job printed: the
	// job log's tail for an Actions job (TailSource "log"), the check
	// run's output for anything else ("output"). Empty for a job that
	// passed, and for a failed one whose log has not been (or could not
	// be) read.
	FailureTail string `json:"failureTail,omitempty"`
	TailSource  string `json:"tailSource,omitempty"`
	// TailFetched records that grain has already asked GitHub for this
	// job's log, whatever it answered, so a log past its retention window
	// is asked for once rather than every cycle.
	TailFetched bool `json:"tailFetched,omitempty"`
	// Via is how grain learned of this result: "webhook" or "poll".
	Via        string    `json:"via,omitempty"`
	RecordedAt time.Time `json:"recordedAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// GitHubCISource is the Source of every GitHubCIJob, and the label the
// metrics and the shared-fault report give GitHub's CI beside grain's.
const GitHubCISource = "github"

// GitHubActionsApp is the App slug GitHub Actions posts its check runs as.
const GitHubActionsApp = "github-actions"

// MaxGitHubCIOutput bounds GitHubCIJob.Output and FailureTail. A check's
// summary is free-form markdown some providers post whole reports into,
// and a job log is megabytes; what correlation and a reader want is the
// end of it.
const MaxGitHubCIOutput = 16 << 10

// JobKey is this job's identity across commits and branches: workflow
// and job name for an Actions job, the check's own name otherwise. GitHub
// exposes no stable job id for either, so the names are it.
func (j GitHubCIJob) JobKey() string {
	if j.Workflow == "" {
		return j.JobName
	}
	return j.Workflow + " / " + j.JobName
}

// IsActions reports whether this check run is a GitHub Actions job, and
// so has a log behind it.
func (j GitHubCIJob) IsActions() bool {
	return j.App == GitHubActionsApp || j.RunID != 0
}

// Failed reports whether GitHub concluded this job did not pass -- the
// conclusions a failure tail is kept for.
func (j GitHubCIJob) Failed() bool { return GitHubCIFailed(j.Conclusion) }

// GitHubCIFailed reports whether a GitHub conclusion is a failure of the
// job itself, as against a cancellation, a skip or a pass.
func GitHubCIFailed(conclusion string) bool {
	switch strings.ToLower(conclusion) {
	case "failure", "timed_out", "startup_failure":
		return true
	}
	return false
}

// PutGitHubCIJob records one concluded GitHub job, merging it into what
// is already known about the same check run.
//
// Merging rather than replacing, because the three ways grain hears of a
// job each know a different part of it: a workflow_job delivery names
// the workflow and the failing step, a check_run delivery names the App,
// and a polled listing knows neither. A field the newer report leaves
// empty keeps what an earlier one said, a failure tail once read is never
// cleared by a later report, and a re-run (a higher RunAttempt) replaces
// the conclusion and times wholesale.
func (s *Store) PutGitHubCIJob(ctx context.Context, j GitHubCIJob) error {
	if j.Repo == "" || j.CheckRunID == 0 {
		return fmt.Errorf("recording a GitHub CI job: repo and check run id are required")
	}
	return s.write(ctx, fmt.Sprintf("record GitHub CI job %s/%d", j.Repo, j.CheckRunID), func(tx *sql.Tx) error {
		prev, err := getGitHubCIJob(ctx, tx, j.Repo, j.CheckRunID)
		if err != nil {
			return err
		}
		merged := mergeGitHubCIJob(prev, j)
		return putGitHubCIJob(ctx, tx, merged)
	})
}

func mergeGitHubCIJob(prev *GitHubCIJob, j GitHubCIJob) GitHubCIJob {
	if j.Source == "" {
		j.Source = GitHubCISource
	}
	j.Output = clampGitHubCIText(j.Output)
	j.FailureTail = clampGitHubCIText(j.FailureTail)
	if j.UpdatedAt.IsZero() {
		j.UpdatedAt = time.Now().UTC()
	}
	if j.RecordedAt.IsZero() {
		j.RecordedAt = j.UpdatedAt
	}
	if prev == nil {
		return j
	}
	out := *prev
	out.UpdatedAt = j.UpdatedAt
	str := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	str(&out.App, j.App)
	str(&out.Ref, j.Ref)
	str(&out.Commit, j.Commit)
	str(&out.Workflow, j.Workflow)
	str(&out.JobName, j.JobName)
	str(&out.RunURL, j.RunURL)
	str(&out.JobURL, j.JobURL)
	str(&out.Output, j.Output)
	str(&out.Via, j.Via)
	if j.RunID != 0 {
		out.RunID = j.RunID
	}
	if j.RunAttempt >= out.RunAttempt {
		if j.RunAttempt > out.RunAttempt {
			// A re-run of the same job: what the last attempt failed with
			// is no longer what this row says happened.
			out.FailingStep, out.FailureTail, out.TailSource, out.TailFetched = "", "", "", false
		}
		out.RunAttempt = j.RunAttempt
		str(&out.Conclusion, j.Conclusion)
		if j.StartedAt != nil {
			out.StartedAt = j.StartedAt
		}
		if j.CompletedAt != nil {
			out.CompletedAt = j.CompletedAt
		}
		str(&out.FailingStep, j.FailingStep)
	}
	if j.FailureTail != "" {
		out.FailureTail, out.TailSource = j.FailureTail, j.TailSource
	}
	if j.TailFetched {
		out.TailFetched = true
	}
	return out
}

func clampGitHubCIText(s string) string {
	if len(s) <= MaxGitHubCIOutput {
		return s
	}
	// The end, at a line boundary where there is one: it is where a
	// failure says what went wrong.
	s = s[len(s)-MaxGitHubCIOutput:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return strings.ToValidUTF8(s, "")
}

const gitHubCIJobColumns = "`repo`,`check_run_id`,`source`,`app`,`ref`,`commit_sha`,`workflow`,`run_id`," +
	"`run_attempt`,`job_name`,`conclusion`,`started_at`,`completed_at`,`failing_step`,`run_url`," +
	"`job_url`,`output`,`failure_tail`,`tail_source`,`tail_fetched`,`via`,`recorded_at`,`updated_at`"

func putGitHubCIJob(ctx context.Context, tx *sql.Tx, j GitHubCIJob) error {
	var started, completed any
	if j.StartedAt != nil {
		started = j.StartedAt.UTC()
	}
	if j.CompletedAt != nil {
		completed = j.CompletedAt.UTC()
	}
	_, err := tx.ExecContext(ctx,
		"REPLACE INTO `github_ci_job` ("+gitHubCIJobColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		j.Repo, j.CheckRunID, j.Source, j.App, j.Ref, j.Commit, j.Workflow, j.RunID,
		j.RunAttempt, j.JobName, j.Conclusion, started, completed, j.FailingStep, j.RunURL,
		j.JobURL, j.Output, j.FailureTail, j.TailSource, j.TailFetched, j.Via,
		j.RecordedAt.UTC(), j.UpdatedAt.UTC())
	return err
}

func scanGitHubCIJob(scan func(...any) error) (GitHubCIJob, error) {
	var j GitHubCIJob
	var started, completed sql.NullTime
	if err := scan(&j.Repo, &j.CheckRunID, &j.Source, &j.App, &j.Ref, &j.Commit, &j.Workflow, &j.RunID,
		&j.RunAttempt, &j.JobName, &j.Conclusion, &started, &completed, &j.FailingStep, &j.RunURL,
		&j.JobURL, &j.Output, &j.FailureTail, &j.TailSource, &j.TailFetched, &j.Via,
		&j.RecordedAt, &j.UpdatedAt); err != nil {
		return GitHubCIJob{}, err
	}
	j.StartedAt = nullableTime(started)
	j.CompletedAt = nullableTime(completed)
	j.RecordedAt = j.RecordedAt.UTC()
	j.UpdatedAt = j.UpdatedAt.UTC()
	return j, nil
}

func getGitHubCIJob(ctx context.Context, q querier, repo string, id int64) (*GitHubCIJob, error) {
	row := q.QueryRowContext(ctx, "SELECT "+gitHubCIJobColumns+" FROM `github_ci_job` WHERE `repo` = ? AND `check_run_id` = ?", repo, id)
	j, err := scanGitHubCIJob(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading GitHub CI job %s/%d: %w", repo, id, err)
	}
	return &j, nil
}

// GetGitHubCIJob reads one recorded job, or nil if grain has none.
func (s *Store) GetGitHubCIJob(ctx context.Context, repo string, checkRunID int64) (*GitHubCIJob, error) {
	return getGitHubCIJob(ctx, s.db, repo, checkRunID)
}

// GitHubCIFilter narrows ListGitHubCIJobs. Every field is optional.
type GitHubCIFilter struct {
	Repo string
	// Since keeps jobs that completed at or after it (by recorded time
	// for a row with no completion time).
	Since time.Time
	// FailedOnly keeps the conclusions GitHubCIFailed counts.
	FailedOnly bool
	// NeedsTail keeps failed Actions jobs whose log nobody has asked for
	// yet -- the failure-tail fetcher's work list.
	NeedsTail bool
	Limit     int
}

// ListGitHubCIJobs returns recorded GitHub CI jobs, most recently
// completed first.
func (s *Store) ListGitHubCIJobs(ctx context.Context, f GitHubCIFilter) ([]GitHubCIJob, error) {
	var where []string
	var args []any
	if f.Repo != "" {
		where = append(where, "`repo` = ?")
		args = append(args, f.Repo)
	}
	if !f.Since.IsZero() {
		where = append(where, "COALESCE(`completed_at`, `recorded_at`) >= ?")
		args = append(args, f.Since.UTC())
	}
	if f.FailedOnly || f.NeedsTail {
		where = append(where, "`conclusion` IN ('failure','timed_out','startup_failure')")
	}
	if f.NeedsTail {
		where = append(where, "`tail_fetched` = 0", "`failure_tail` = ''",
			"(`app` = '"+GitHubActionsApp+"' OR `run_id` <> 0)")
	}
	query := "SELECT " + gitHubCIJobColumns + " FROM `github_ci_job`"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY COALESCE(`completed_at`, `recorded_at`) DESC, `check_run_id` DESC"
	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	var out []GitHubCIJob
	err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		j, err := scanGitHubCIJob(rows.Scan)
		if err != nil {
			return err
		}
		out = append(out, j)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing GitHub CI jobs: %w", err)
	}
	return out, nil
}

// SetGitHubCIFailureTail records what fetching a failed job's log found:
// the tail, or "" for a log GitHub would not serve. Either way the job is
// marked fetched, so it is not asked for again.
func (s *Store) SetGitHubCIFailureTail(ctx context.Context, repo string, checkRunID int64, tail, source string, now time.Time) error {
	return s.write(ctx, fmt.Sprintf("record the failure tail of GitHub CI job %s/%d", repo, checkRunID), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `github_ci_job` SET `failure_tail` = ?, `tail_source` = ?, `tail_fetched` = 1, `updated_at` = ? "+
				"WHERE `repo` = ? AND `check_run_id` = ?",
			clampGitHubCIText(tail), source, now.UTC(), repo, checkRunID)
		return err
	})
}

// PruneGitHubCIJobs forgets jobs recorded before cutoff and reports how
// many it removed. GitHub keeps its own history; grain keeps what its
// metrics and correlation windows read.
func (s *Store) PruneGitHubCIJobs(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := s.write(ctx, "prune GitHub CI jobs", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM `github_ci_job` WHERE `recorded_at` < ?", cutoff.UTC())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}
