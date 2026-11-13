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
	"regexp"
	"strconv"
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
	// FailedTests are the tests FailureTail names as failing (`--- FAIL:`
	// lines, Playwright's ✘ lines), parsed once when the tail arrives
	// (FailedTestNames). They outlive the tail: a pruning compacts a
	// failed job down to these names and its verdict
	// (GitHubCIPrune.Compact), so a test's failures can be counted over
	// weeks of a repository's jobs rather than over the logs grain still
	// holds.
	FailedTests []string `json:"failedTests,omitempty"`
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

// GitHubBaseRepairTag is the tag every repair of a red default branch on
// GitHub Actions carries (the orchestrator's githubbaserepair.go): at most
// one is open per repository.
func GitHubBaseRepairTag(repo RepoRef) string {
	return "github-base-repair:" + strings.ToLower(repo.String())
}

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
		if err := putGitHubCIJob(ctx, tx, merged); err != nil {
			return err
		}
		return recordGitHubCIEventTx(ctx, tx, prev, merged)
	})
}

func mergeGitHubCIJob(prev *GitHubCIJob, j GitHubCIJob) GitHubCIJob {
	fillGitHubCIRunID(&j)
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
		return withFailedTests(j)
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
			out.FailedTests = nil
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
		out.FailedTests = nil
	}
	if len(j.FailedTests) > 0 {
		out.FailedTests = j.FailedTests
	}
	if j.TailFetched {
		out.TailFetched = true
	}
	return withFailedTests(out)
}

// withFailedTests is j with FailedTests parsed out of its failure tail,
// where it has a tail and no names yet.
func withFailedTests(j GitHubCIJob) GitHubCIJob {
	if len(j.FailedTests) == 0 && j.FailureTail != "" {
		j.FailedTests = FailedTestNames(j.FailureTail)
	}
	return j
}

var (
	// failedGoTestLine is a `go test -v` failure: "--- FAIL: TestX/sub (0.1s)".
	failedGoTestLine = regexp.MustCompile(`^\s*--- FAIL:\s+([^\s()]+)`)
	// failedPlaywrightLine is the list reporter's failed test:
	// "  ✘  3 [chromium] › e2e/a.spec.js:12:3 › Suite › does it (1.2s)",
	// the worker number and project optional.
	failedPlaywrightLine = regexp.MustCompile(`^\s*✘\s+(?:\d+\s+)?(?:\[[^\]]+\]\s+›\s+)?(\S+?\.(?:spec|test)\.[cm]?[jt]sx?)(?::\d+(?::\d+)?)?\s+›\s+(.+?)(?:\s+\(retry #\d+\))?(?:\s+\(\d[^)]*\))?\s*$`)
	ansiEscape           = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
)

// FailedTestNames is the tests a job's log tail names as failing, in the
// order they first appear and each once: a Go test by its full name
// ("TestX/sub"), a Playwright test as its spec file and title path
// ("e2e/a.spec.js › Suite › does it") -- without the line and column,
// which move with every edit to the file, and without the duration or
// "(retry #N)". A retried Playwright test prints ✘ once per attempt and
// is named once.
func FailedTestNames(tail string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(ansiEscape.ReplaceAllString(tail, ""), "\n") {
		line = strings.TrimRight(line, "\r")
		// An Actions log line starts with its timestamp.
		if len(line) > 29 && line[4] == '-' && line[10] == 'T' {
			if i := strings.IndexByte(line, ' '); i > 0 && i < 40 {
				line = line[i+1:]
			}
		}
		name := ""
		if m := failedGoTestLine.FindStringSubmatch(line); m != nil {
			name = m[1]
		} else if m := failedPlaywrightLine.FindStringSubmatch(line); m != nil {
			name = m[1] + " › " + strings.TrimSpace(m[2])
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// fillGitHubCIRunID gives a job its workflow run id, and the run's page,
// out of its own page when the report that brought it named neither. A
// polled check run is such a report -- the checks listing carries no run
// id -- and on a host whose webhooks never reach grain it is the only one,
// which left every one of that host's jobs at run 0 and out of everything
// that reads runs by id (ci:read's Actions listing among them).
func fillGitHubCIRunID(j *GitHubCIJob) {
	if j.RunID == 0 {
		j.RunID = ActionsRunIDFromURL(j.JobURL)
		if j.RunID == 0 {
			j.RunID = ActionsRunIDFromURL(j.RunURL)
		}
	}
	if j.RunID != 0 && j.RunURL == "" {
		if i := strings.Index(j.JobURL, "/job/"); i > 0 && ActionsRunIDFromURL(j.JobURL[:i]) == j.RunID {
			j.RunURL = j.JobURL[:i]
		}
	}
}

// ActionsRunIDFromURL is the workflow run id in an Actions run's or job's
// page (https://<host>/<owner>/<name>/actions/runs/<run>[/job/<job>]), on
// any host, or 0 for a URL that is neither.
func ActionsRunIDFromURL(u string) int64 {
	const marker = "/actions/runs/"
	i := strings.LastIndex(u, marker)
	if i < 0 {
		return 0
	}
	rest := u[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 || (end < len(rest) && !strings.ContainsRune("/?#", rune(rest[end]))) {
		return 0
	}
	n, err := strconv.ParseInt(rest[:end], 10, 64)
	if err != nil {
		return 0
	}
	return n
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
	"`job_url`,`output`,`failure_tail`,`tail_source`,`tail_fetched`,`via`,`recorded_at`,`updated_at`,`failed_tests`"

func putGitHubCIJob(ctx context.Context, tx *sql.Tx, j GitHubCIJob) error {
	var started, completed any
	if j.StartedAt != nil {
		started = j.StartedAt.UTC()
	}
	if j.CompletedAt != nil {
		completed = j.CompletedAt.UTC()
	}
	_, err := tx.ExecContext(ctx,
		"REPLACE INTO `github_ci_job` ("+gitHubCIJobColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		j.Repo, j.CheckRunID, j.Source, j.App, j.Ref, j.Commit, j.Workflow, j.RunID,
		j.RunAttempt, j.JobName, j.Conclusion, started, completed, j.FailingStep, j.RunURL,
		j.JobURL, j.Output, j.FailureTail, j.TailSource, j.TailFetched, j.Via,
		j.RecordedAt.UTC(), j.UpdatedAt.UTC(), strings.Join(j.FailedTests, "\n"))
	return err
}

func scanGitHubCIJob(scan func(...any) error) (GitHubCIJob, error) {
	var j GitHubCIJob
	var started, completed sql.NullTime
	var tests string
	if err := scan(&j.Repo, &j.CheckRunID, &j.Source, &j.App, &j.Ref, &j.Commit, &j.Workflow, &j.RunID,
		&j.RunAttempt, &j.JobName, &j.Conclusion, &started, &completed, &j.FailingStep, &j.RunURL,
		&j.JobURL, &j.Output, &j.FailureTail, &j.TailSource, &j.TailFetched, &j.Via,
		&j.RecordedAt, &j.UpdatedAt, &tests); err != nil {
		return GitHubCIJob{}, err
	}
	if tests != "" {
		j.FailedTests = strings.Split(tests, "\n")
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
	Repo  string
	RunID int64
	// Since keeps jobs that completed at or after it (by recorded time
	// for a row with no completion time).
	Since time.Time
	// Until keeps jobs that started before it (by completion, then
	// recorded, time for a row with no start): the other end of Since's
	// window, measured the way ci.RunFilter.Until measures a run, so a
	// run's jobs are all in a window or all out of it.
	Until time.Time
	// FailedOnly keeps the conclusions GitHubCIFailed counts.
	FailedOnly bool
	// NeedsTail keeps failed Actions jobs whose log nobody has asked for
	// yet -- the failure-tail fetcher's work list.
	NeedsTail bool
	// Refs, when set, keeps jobs of runs on one of these refs (a branch
	// name, as recorded).
	Refs []string
	// RefContains keeps jobs whose ref contains it, without regard to
	// case: ci.RunFilter.Ref's substring match, asked of the store so
	// that a listing of one branch is not the newest Limit jobs of every
	// branch narrowed afterwards.
	RefContains string
	// FailedTest keeps failed jobs that named a test containing it among
	// their FailedTests, without regard to case. A containment rather
	// than the caller's own match, which it applies to what comes back.
	FailedTest string
	Limit      int
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
	if f.RunID != 0 {
		where = append(where, "`run_id` = ?")
		args = append(args, f.RunID)
	}
	if !f.Since.IsZero() {
		where = append(where, "COALESCE(`completed_at`, `recorded_at`) >= ?")
		args = append(args, f.Since.UTC())
	}
	if !f.Until.IsZero() {
		where = append(where, "COALESCE(`started_at`, `completed_at`, `recorded_at`) < ?")
		args = append(args, f.Until.UTC())
	}
	if f.RefContains != "" {
		where = append(where, "LOWER(`ref`) LIKE ? ESCAPE '\\'")
		args = append(args, "%"+likeEscape(strings.ToLower(f.RefContains))+"%")
	}
	if f.FailedTest != "" {
		where = append(where, "LOWER(`failed_tests`) LIKE ? ESCAPE '\\'")
		args = append(args, "%"+likeEscape(strings.ToLower(f.FailedTest))+"%")
	}
	if len(f.Refs) > 0 {
		where = append(where, "`ref` IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.Refs)), ",")+")")
		for _, r := range f.Refs {
			args = append(args, r)
		}
	}
	if f.FailedOnly || f.NeedsTail || f.FailedTest != "" {
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
			"UPDATE `github_ci_job` SET `failure_tail` = ?, `tail_source` = ?, `tail_fetched` = 1, `updated_at` = ?, "+
				"`failed_tests` = ? WHERE `repo` = ? AND `check_run_id` = ?",
			clampGitHubCIText(tail), source, now.UTC(), strings.Join(FailedTestNames(tail), "\n"), repo, checkRunID)
		return err
	})
}

// GitHubCIPrune is how long the recorded jobs live. Two ages, because
// the record is asked two questions that cost different amounts to keep
// answering:
//
//   - "what did the default branch and Stage 1 say, and which tests
//     failed" -- over the weeks a time-to-repair or flake baseline is
//     measured against. A verdict and a few test names per job, which
//     is cheap, so it is kept until Before.
//   - everything else: a task branch's passing jobs, and the bounded
//     log tail and check output beside a failure. The bulk of the table,
//     and read only while somebody is still looking at a recent failure,
//     so it goes at Compact.
type GitHubCIPrune struct {
	// Before forgets every job recorded before it.
	Before time.Time
	// Compact, later than Before, forgets the jobs recorded before it
	// that neither failed nor ran on a kept ref, and strips the rest down
	// to their verdict and FailedTests (the failure tail and check output
	// go). Zero compacts nothing and forgets only by Before.
	Compact time.Time
	// KeepRefs are the refs whose every job lives until Before: the
	// repositories' default branches. A release candidate tag
	// (v1.2.3-rc.4, what Stage 1 runs on) is kept whether it is named
	// here or not.
	KeepRefs []string
}

// PruneGitHubCIJobs applies p to the recorded jobs and reports how many
// it removed. GitHub keeps its own history; grain keeps what its
// metrics, correlation windows and verdict history read.
func (s *Store) PruneGitHubCIJobs(ctx context.Context, p GitHubCIPrune) (int64, error) {
	var n int64
	err := s.write(ctx, "prune GitHub CI jobs", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM `github_ci_job` WHERE `recorded_at` < ?", p.Before.UTC())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		if p.Compact.IsZero() || !p.Compact.After(p.Before) {
			return nil
		}
		kept := []string{"`conclusion` IN ('failure','timed_out','startup_failure')",
			"`ref` LIKE 'v%-rc.%'", "`ref` LIKE 'refs/tags/v%-rc.%'"}
		args := []any{p.Compact.UTC()}
		var refs []string
		for _, ref := range p.KeepRefs {
			if ref = strings.TrimSpace(ref); ref != "" {
				refs = append(refs, ref, "refs/heads/"+ref)
			}
		}
		if len(refs) > 0 {
			kept = append(kept, "`ref` IN ("+strings.TrimSuffix(strings.Repeat("?,", len(refs)), ",")+")")
			for _, ref := range refs {
				args = append(args, ref)
			}
		}
		res, err = tx.ExecContext(ctx, "DELETE FROM `github_ci_job` WHERE `recorded_at` < ? AND NOT ("+
			strings.Join(kept, " OR ")+")", args...)
		if err != nil {
			return err
		}
		dropped, _ := res.RowsAffected()
		n += dropped
		_, err = tx.ExecContext(ctx, "UPDATE `github_ci_job` SET `failure_tail` = '', `output` = '' "+
			"WHERE `recorded_at` < ? AND (`failure_tail` <> '' OR `output` <> '')", p.Compact.UTC())
		return err
	})
	return n, err
}

// likeEscape is s with LIKE's wildcards (and the escape character
// itself) escaped, for a pattern that asks `ESCAPE '\'`.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// backfillGitHubCIFailedTests is schema 147's rewrite: every recorded job
// with a failure tail and no failed tests gets the names its tail holds,
// so the jobs failed before the column existed count too once their tails
// are compacted away. Idempotent: a job that has its names is passed over.
func backfillGitHubCIFailedTests(ctx context.Context, tx *sql.Tx) error {
	type key struct {
		repo string
		id   int64
	}
	found := map[key]string{}
	err := each(ctx, tx, "SELECT `repo`,`check_run_id`,`failure_tail` FROM `github_ci_job` "+
		"WHERE `failure_tail` <> '' AND `failed_tests` = ''", nil, func(rows *sql.Rows) error {
		var k key
		var tail string
		if err := rows.Scan(&k.repo, &k.id, &tail); err != nil {
			return err
		}
		if names := FailedTestNames(tail); len(names) > 0 {
			found[k] = strings.Join(names, "\n")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("backfilling failed tests of GitHub CI jobs: %w", err)
	}
	for k, names := range found {
		if _, err := tx.ExecContext(ctx, "UPDATE `github_ci_job` SET `failed_tests` = ? WHERE `repo` = ? AND `check_run_id` = ?",
			names, k.repo, k.id); err != nil {
			return fmt.Errorf("backfilling failed tests of GitHub CI job %s/%d: %w", k.repo, k.id, err)
		}
	}
	return nil
}
