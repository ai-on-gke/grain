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

// MaxExternalRefLen bounds the byte length of Task.ExternalRef.
const MaxExternalRefLen = 256

// NormalizeExternalRef trims leading and trailing whitespace from an
// external work reference and rejects control characters or values past
// MaxExternalRefLen. An empty string is valid and means "no external ref".
func NormalizeExternalRef(raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if len(ref) > MaxExternalRefLen {
		return "", fmt.Errorf("externalRef exceeds %d bytes", MaxExternalRefLen)
	}
	for i := 0; i < len(ref); i++ {
		if ref[i] < 0x20 || ref[i] == 0x7f {
			return "", errors.New("externalRef must not contain control characters")
		}
	}
	return ref, nil
}

// externalRefLineageKinds are the task_link kinds that point from a spawned
// or derived task back to the parent/source task whose external reference it
// belongs to when the caller did not specify one explicitly.
var externalRefLineageKinds = []LinkKind{
	LinkProposedBy,
	LinkChildOf,
	LinkPortOf,
	LinkPushesReviewedBranch,
	LinkTakeoverReviewOf,
	LinkReverts,
	LinkContinues,
	LinkSupersedes,
}

const externalRefLineageSQL = `'` + string(LinkProposedBy) + `','` +
	string(LinkChildOf) + `','` +
	string(LinkPortOf) + `','` +
	string(LinkPushesReviewedBranch) + `','` +
	string(LinkTakeoverReviewOf) + `','` +
	string(LinkReverts) + `','` +
	string(LinkContinues) + `','` +
	string(LinkSupersedes) + `'`

// externalRefForNewTask resolves the external work reference a newly filed
// task inherits when its caller left ExternalRef empty. It walks upward
// along lineage links and task WorkOrigin pointers in tx until it finds an
// ancestor carrying a non-empty external_ref, guarding against cycles.
func externalRefForNewTask(ctx context.Context, tx *sql.Tx, t Task) (string, error) {
	var queue []string
	seen := map[string]bool{t.ID: true}
	enqueue := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		queue = append(queue, id)
	}
	for _, kind := range externalRefLineageKinds {
		for _, l := range t.Links {
			if l.Kind == kind {
				enqueue(l.Target)
			}
		}
	}
	if origTask := t.WorkOrigin.Task(); origTask != "" {
		enqueue(origTask)
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		var extRef, origRef string
		err := tx.QueryRowContext(ctx,
			"SELECT `external_ref`, `origin_ref` FROM `task` WHERE `id` = ?",
			cur,
		).Scan(&extRef, &origRef)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("reading external_ref of task %s: %w", cur, err)
		}
		if trimmed := strings.TrimSpace(extRef); trimmed != "" {
			return trimmed, nil
		}

		rows, err := tx.QueryContext(ctx,
			"SELECT `target` FROM `task_link` WHERE `task_id` = ? AND `kind` IN ("+externalRefLineageSQL+") ORDER BY `kind`, `target`",
			cur,
		)
		if err != nil {
			return "", fmt.Errorf("reading lineage links of task %s: %w", cur, err)
		}
		var parents []string
		for rows.Next() {
			var target string
			if err := rows.Scan(&target); err != nil {
				rows.Close()
				return "", err
			}
			parents = append(parents, target)
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		for _, p := range parents {
			enqueue(p)
		}
		if origTask := WorkOrigin(origRef).Task(); origTask != "" {
			enqueue(origTask)
		}
	}
	return "", nil
}

// RefSummary is the aggregated status rollup of all readable tasks carrying
// one external work reference (GET /api/refs/{ref}/summary).
type RefSummary struct {
	ExternalRef    string               `json:"externalRef"`
	Total          int                  `json:"total"`
	ByState        map[State]int        `json:"byState"`
	Checks         RefCheckCounts       `json:"checks"`
	OpenQuestions  []RefQuestion        `json:"openQuestions"`
	PullRequests   RefPullRequestCounts `json:"pullRequests"`
	LastActivityAt *time.Time           `json:"lastActivityAt,omitempty"`
}

// RefCheckCounts summarizes CI checks across the tasks in a RefSummary.
type RefCheckCounts struct {
	Passed int `json:"passed"`
	Total  int `json:"total"`
}

// RefQuestion is one open question or decision pending on an awaiting_reply
// task in a RefSummary.
type RefQuestion struct {
	TaskID  string `json:"taskId"`
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary"`
}

// RefPullRequestCounts summarizes pull requests opened and merged across the
// tasks in a RefSummary.
type RefPullRequestCounts struct {
	Opened int `json:"opened"`
	Merged int `json:"merged"`
	Open   int `json:"open"`
}

// RefTaskRollupRow holds the store fields read for one (task, fixes-link)
// pair by Store.RefTaskRollupRows in a single indexed SQL query, without
// reading run transcripts, prompts or CI step logs.
type RefTaskRollupRow struct {
	TaskID      string
	Title       string
	Branch      string
	Target      *RepoRef
	Repo        string
	State       State
	FixesTarget string

	PendingQuestionSet     bool
	PendingQuestionSummary string
	PendingQuestionBody    string
	PendingSecret          string
	LatestCommentSummary   string
	LatestCommentBody      string

	PROpenedAt *time.Time
	PRMergedAt *time.Time
	PRClosedAt *time.Time

	CIRunID      string
	CIConclusion string
	CIJobTotal   int
	CIJobPassed  int

	GitHubCITotal  int
	GitHubCIPassed int

	LastActivityAt *time.Time
}

const refRollupQuery = `
SELECT
  ` + "`t`.`id`" + `,
  ` + "`t`.`title`" + `,
  ` + "`t`.`branch`" + `,
  ` + "`t`.`target_owner`" + `,
  ` + "`t`.`target_name`" + `,
  ` + "`s`.`state`" + `,
  ` + "`fl`.`target`" + `,
  ` + "`o`.`pending_question_comment_id`" + `,
  ` + "`qc`.`summary`" + `,
  SUBSTR(` + "`qc`.`body`" + `, 1, 512),
  ` + "`o`.`pending_secret`" + `,
  (SELECT ` + "`lc`.`summary`" + ` FROM ` + "`task_comment`" + ` AS ` + "`lc`" + ` WHERE ` + "`lc`.`task_id`" + ` = ` + "`t`.`id`" + ` ORDER BY ` + "`lc`.`id`" + ` DESC LIMIT 1),
  (SELECT SUBSTR(` + "`lc`.`body`" + `, 1, 512) FROM ` + "`task_comment`" + ` AS ` + "`lc`" + ` WHERE ` + "`lc`.`task_id`" + ` = ` + "`t`.`id`" + ` ORDER BY ` + "`lc`.`id`" + ` DESC LIMIT 1),
  ` + "`o`.`pr_opened_at`" + `,
  ` + "`o`.`pr_merged_at`" + `,
  ` + "`o`.`pr_closed_at`" + `,
  COALESCE(
    NULLIF(` + "`o`.`ci_run_id`" + `, ''),
    (SELECT ` + "`cr`.`id`" + ` FROM ` + "`ci_run`" + ` AS ` + "`cr`" + `
     WHERE ` + "`t`.`target_owner`" + ` IS NOT NULL
       AND ` + "`cr`.`repo`" + ` = (` + "`t`.`target_owner`" + ` || '/' || ` + "`t`.`target_name`" + `)
       AND ` + "`cr`.`ref`" + ` = (CASE WHEN ` + "`t`.`branch`" + ` <> '' THEN ` + "`t`.`branch`" + ` ELSE 'grain/task-' || ` + "`t`.`id`" + ` END)
     ORDER BY ` + "`cr`.`started_at`" + ` DESC, ` + "`cr`.`id`" + ` DESC LIMIT 1),
    ''
  ),
  COALESCE(` + "`o`.`ci_conclusion`" + `, ''),
  (SELECT COUNT(*) FROM ` + "`ci_job`" + ` AS ` + "`cj`" + `
   WHERE ` + "`cj`.`run_id`" + ` = COALESCE(
     NULLIF(` + "`o`.`ci_run_id`" + `, ''),
     (SELECT ` + "`cr2`.`id`" + ` FROM ` + "`ci_run`" + ` AS ` + "`cr2`" + `
      WHERE ` + "`t`.`target_owner`" + ` IS NOT NULL
        AND ` + "`cr2`.`repo`" + ` = (` + "`t`.`target_owner`" + ` || '/' || ` + "`t`.`target_name`" + `)
        AND ` + "`cr2`.`ref`" + ` = (CASE WHEN ` + "`t`.`branch`" + ` <> '' THEN ` + "`t`.`branch`" + ` ELSE 'grain/task-' || ` + "`t`.`id`" + ` END)
      ORDER BY ` + "`cr2`.`started_at`" + ` DESC, ` + "`cr2`.`id`" + ` DESC LIMIT 1),
     ''
   )),
  (SELECT COUNT(*) FROM ` + "`ci_job`" + ` AS ` + "`cj`" + `
   WHERE ` + "`cj`.`run_id`" + ` = COALESCE(
     NULLIF(` + "`o`.`ci_run_id`" + `, ''),
     (SELECT ` + "`cr3`.`id`" + ` FROM ` + "`ci_run`" + ` AS ` + "`cr3`" + `
      WHERE ` + "`t`.`target_owner`" + ` IS NOT NULL
        AND ` + "`cr3`.`repo`" + ` = (` + "`t`.`target_owner`" + ` || '/' || ` + "`t`.`target_name`" + `)
        AND ` + "`cr3`.`ref`" + ` = (CASE WHEN ` + "`t`.`branch`" + ` <> '' THEN ` + "`t`.`branch`" + ` ELSE 'grain/task-' || ` + "`t`.`id`" + ` END)
      ORDER BY ` + "`cr3`.`started_at`" + ` DESC, ` + "`cr3`.`id`" + ` DESC LIMIT 1),
     ''
   ) AND ` + "`cj`.`conclusion`" + ` IN ('success', 'skipped', 'neutral')),
  (SELECT COUNT(*) FROM ` + "`github_ci_job`" + ` AS ` + "`gj`" + `
   WHERE ` + "`t`.`target_owner`" + ` IS NOT NULL
     AND ` + "`gj`.`repo`" + ` = (` + "`t`.`target_owner`" + ` || '/' || ` + "`t`.`target_name`" + `)
     AND ` + "`gj`.`ref`" + ` = (CASE WHEN ` + "`t`.`branch`" + ` <> '' THEN ` + "`t`.`branch`" + ` ELSE 'grain/task-' || ` + "`t`.`id`" + ` END)
     AND ` + "`gj`.`commit_sha`" + ` = COALESCE((
       SELECT ` + "`gj2`.`commit_sha`" + ` FROM ` + "`github_ci_job`" + ` AS ` + "`gj2`" + `
       WHERE ` + "`gj2`.`repo`" + ` = ` + "`gj`.`repo`" + ` AND ` + "`gj2`.`ref`" + ` = ` + "`gj`.`ref`" + `
       ORDER BY ` + "`gj2`.`recorded_at`" + ` DESC, ` + "`gj2`.`check_run_id`" + ` DESC LIMIT 1
     ), '')),
  (SELECT COUNT(*) FROM ` + "`github_ci_job`" + ` AS ` + "`gj`" + `
   WHERE ` + "`t`.`target_owner`" + ` IS NOT NULL
     AND ` + "`gj`.`repo`" + ` = (` + "`t`.`target_owner`" + ` || '/' || ` + "`t`.`target_name`" + `)
     AND ` + "`gj`.`ref`" + ` = (CASE WHEN ` + "`t`.`branch`" + ` <> '' THEN ` + "`t`.`branch`" + ` ELSE 'grain/task-' || ` + "`t`.`id`" + ` END)
     AND ` + "`gj`.`commit_sha`" + ` = COALESCE((
       SELECT ` + "`gj3`.`commit_sha`" + ` FROM ` + "`github_ci_job`" + ` AS ` + "`gj3`" + `
       WHERE ` + "`gj3`.`repo`" + ` = ` + "`gj`.`repo`" + ` AND ` + "`gj3`.`ref`" + ` = ` + "`gj`.`ref`" + `
       ORDER BY ` + "`gj3`.`recorded_at`" + ` DESC, ` + "`gj3`.`check_run_id`" + ` DESC LIMIT 1
     ), '')
     AND ` + "`gj`.`conclusion`" + ` IN ('success', 'skipped', 'neutral')),
  ` + "`t`.`created_at`" + `,
  ` + "`t`.`approved_at`" + `,
  ` + "`t`.`deferred_at`" + `,
  ` + "`t`.`undeferred_at`" + `,
  ` + "`o`.`observed_at`" + `,
  ` + "`o`.`completed_at`" + `,
  ` + "`o`.`closed_at`" + `,
  ` + "`o`.`ci_started_at`" + `,
  ` + "`o`.`ci_finished_at`" + `,
  ` + "`o`.`last_failed_at`" + `,
  ` + "`o`.`last_succeeded_at`" + `,
  ` + "`o`.`retry_requested_at`" + `,
  ` + "`o`.`run_now_at`" + `,
  (SELECT ` + "`tr1`.`started_at`" + ` FROM ` + "`task_run`" + ` AS ` + "`tr1`" + ` WHERE ` + "`tr1`.`task_id`" + ` = ` + "`t`.`id`" + ` ORDER BY ` + "`tr1`.`started_at`" + ` DESC LIMIT 1),
  (SELECT ` + "`tr2`.`activity_at`" + ` FROM ` + "`task_run`" + ` AS ` + "`tr2`" + ` WHERE ` + "`tr2`.`task_id`" + ` = ` + "`t`.`id`" + ` AND ` + "`tr2`.`activity_at`" + ` IS NOT NULL ORDER BY ` + "`tr2`.`activity_at`" + ` DESC LIMIT 1),
  (SELECT ` + "`tr3`.`finished_at`" + ` FROM ` + "`task_run`" + ` AS ` + "`tr3`" + ` WHERE ` + "`tr3`.`task_id`" + ` = ` + "`t`.`id`" + ` AND ` + "`tr3`.`finished_at`" + ` IS NOT NULL ORDER BY ` + "`tr3`.`finished_at`" + ` DESC LIMIT 1),
  (SELECT ` + "`tc1`.`created_at`" + ` FROM ` + "`task_comment`" + ` AS ` + "`tc1`" + ` WHERE ` + "`tc1`.`task_id`" + ` = ` + "`t`.`id`" + ` ORDER BY ` + "`tc1`.`id`" + ` DESC LIMIT 1),
  (SELECT ` + "`te1`.`at`" + ` FROM ` + "`task_event`" + ` AS ` + "`te1`" + ` WHERE ` + "`te1`.`task_id`" + ` = ` + "`t`.`id`" + ` ORDER BY ` + "`te1`.`at`" + ` DESC LIMIT 1)
FROM ` + "`task`" + ` AS ` + "`t`" + `
JOIN ` + "`task_state`" + ` AS ` + "`s`" + ` ON ` + "`s`.`task_id`" + ` = ` + "`t`.`id`" + `
LEFT JOIN ` + "`task_observation`" + ` AS ` + "`o`" + ` ON ` + "`o`.`task_id`" + ` = ` + "`t`.`id`" + `
LEFT JOIN ` + "`task_comment`" + ` AS ` + "`qc`" + ` ON ` + "`qc`.`id`" + ` = ` + "`o`.`pending_question_comment_id`" + `
LEFT JOIN ` + "`task_link`" + ` AS ` + "`fl`" + ` ON ` + "`fl`.`task_id`" + ` = ` + "`t`.`id`" + ` AND ` + "`fl`.`kind`" + ` = ?
WHERE ` + "`t`.`external_ref`" + ` = ?
ORDER BY ` + "`t`.`order_key`" + ` ASC, ` + "`t`.`id`" + ` ASC, ` + "`fl`.`target`" + ` ASC
`

// RefTaskRollupRows reads all rollup rows for externalRef in a single
// indexed query against task_external_ref, touching zero transcript, prompt
// or CI log columns.
func (s *Store) RefTaskRollupRows(ctx context.Context, externalRef string) ([]RefTaskRollupRow, error) {
	externalRef = strings.TrimSpace(externalRef)
	if externalRef == "" {
		return nil, nil
	}
	var out []RefTaskRollupRow
	err := each(ctx, s.db, refRollupQuery, []any{string(LinkFixes), externalRef}, func(rows *sql.Rows) error {
		var r RefTaskRollupRow
		var state string
		var tOwner, tName, fixesTarget sql.NullString
		var pendingQID sql.NullInt64
		var qSummary, qBody, pendingSecret, latestSummary, latestBody sql.NullString
		var prOpened, prMerged, prClosed sql.NullTime
		var created, approved, deferred, undeferred sql.NullTime
		var observed, completed, closed, ciStarted, ciFinished sql.NullTime
		var lastFailed, lastSucceeded, retryReq, runNow sql.NullTime
		var runStarted, runActivity, runFinished, commentCreated, eventAt any

		if err := rows.Scan(
			&r.TaskID,
			&r.Title,
			&r.Branch,
			&tOwner,
			&tName,
			&state,
			&fixesTarget,
			&pendingQID,
			&qSummary,
			&qBody,
			&pendingSecret,
			&latestSummary,
			&latestBody,
			&prOpened,
			&prMerged,
			&prClosed,
			&r.CIRunID,
			&r.CIConclusion,
			&r.CIJobTotal,
			&r.CIJobPassed,
			&r.GitHubCITotal,
			&r.GitHubCIPassed,
			&created,
			&approved,
			&deferred,
			&undeferred,
			&observed,
			&completed,
			&closed,
			&ciStarted,
			&ciFinished,
			&lastFailed,
			&lastSucceeded,
			&retryReq,
			&runNow,
			&runStarted,
			&runActivity,
			&runFinished,
			&commentCreated,
			&eventAt,
		); err != nil {
			return err
		}
		if r.Branch == "" {
			r.Branch = BranchName(r.TaskID)
		}
		if tOwner.Valid {
			target := RepoFromStored(tOwner.String, tName.String)
			r.Target = &target
			r.Repo = target.String()
		}
		r.State = State(state)
		r.FixesTarget = fixesTarget.String
		r.PendingQuestionSet = pendingQID.Valid
		r.PendingQuestionSummary = qSummary.String
		r.PendingQuestionBody = qBody.String
		r.PendingSecret = pendingSecret.String
		r.LatestCommentSummary = latestSummary.String
		r.LatestCommentBody = latestBody.String
		r.PROpenedAt = timePtr(prOpened)
		r.PRMergedAt = timePtr(prMerged)
		r.PRClosedAt = timePtr(prClosed)
		r.LastActivityAt = latest(
			timePtr(created),
			timePtr(approved),
			timePtr(deferred),
			timePtr(undeferred),
			timePtr(observed),
			timePtr(completed),
			timePtr(closed),
			r.PROpenedAt,
			r.PRMergedAt,
			r.PRClosedAt,
			timePtr(ciStarted),
			timePtr(ciFinished),
			timePtr(lastFailed),
			timePtr(lastSucceeded),
			timePtr(retryReq),
			timePtr(runNow),
			parseScannedTime(runStarted),
			parseScannedTime(runActivity),
			parseScannedTime(runFinished),
			parseScannedTime(commentCreated),
			parseScannedTime(eventAt),
		)
		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// parseScannedTime converts a nullable timestamp returned by a scalar
// subquery on either SQLite or PostgreSQL into a UTC *time.Time.
func parseScannedTime(v any) *time.Time {
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		t := x.UTC()
		return &t
	case []byte:
		return parseTimeText(string(x))
	case string:
		return parseTimeText(x)
	default:
		return nil
	}
}

var scannedTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999 +0000 UTC",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
}

func parseTimeText(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if i := strings.Index(raw, " m="); i >= 0 {
		raw = raw[:i]
	}
	for _, layout := range scannedTimeLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

// BuildRefSummary folds RefTaskRollupRows into a RefSummary, keeping only
// tasks whose repository passes readable (when non-nil).
func BuildRefSummary(externalRef string, rows []RefTaskRollupRow, readable func(repo string) bool) RefSummary {
	sum := RefSummary{
		ExternalRef:   externalRef,
		ByState:       map[State]int{},
		OpenQuestions: []RefQuestion{},
	}
	type prRollup struct {
		merged bool
		closed bool
	}
	seenTasks := map[string]bool{}
	seenCIRuns := map[string]bool{}
	seenGHBranches := map[string]bool{}
	prOrder := []string{}
	prs := map[string]*prRollup{}

	recordPR := func(key string, merged, closed bool) {
		st, ok := prs[key]
		if !ok {
			st = &prRollup{}
			prs[key] = st
			prOrder = append(prOrder, key)
		}
		if merged {
			st.merged = true
		}
		if closed {
			st.closed = true
		}
	}

	for _, r := range rows {
		if readable != nil && !readable(r.Repo) {
			continue
		}
		if !seenTasks[r.TaskID] {
			seenTasks[r.TaskID] = true
			sum.Total++
			sum.ByState[r.State]++
			sum.LastActivityAt = latest(sum.LastActivityAt, r.LastActivityAt)

			// CI checks: Grain CI jobs (deduplicated per ci_run_id) plus
			// GitHub CI check runs on the branch's latest commit
			// (deduplicated per repo+branch).
			if r.CIRunID != "" {
				if !seenCIRuns[r.CIRunID] {
					seenCIRuns[r.CIRunID] = true
					if r.CIJobTotal > 0 {
						sum.Checks.Total += r.CIJobTotal
						sum.Checks.Passed += r.CIJobPassed
					} else if r.CIConclusion != "" {
						sum.Checks.Total++
						if isPassingCIConclusion(r.CIConclusion) {
							sum.Checks.Passed++
						}
					}
				}
			} else if r.CIJobTotal == 0 && r.CIConclusion != "" {
				sum.Checks.Total++
				if isPassingCIConclusion(r.CIConclusion) {
					sum.Checks.Passed++
				}
			}
			if r.Repo != "" && r.GitHubCITotal > 0 {
				ghKey := strings.ToLower(r.Repo) + "\x00" + r.Branch
				if !seenGHBranches[ghKey] {
					seenGHBranches[ghKey] = true
					sum.Checks.Total += r.GitHubCITotal
					sum.Checks.Passed += r.GitHubCIPassed
				}
			}

			// Open questions: awaiting_reply tasks, skipping human-stopped turns.
			if r.State == StateAwaitingReply {
				if summary, ok := refQuestionSummary(r); ok {
					sum.OpenQuestions = append(sum.OpenQuestions, RefQuestion{
						TaskID:  r.TaskID,
						Title:   r.Title,
						Summary: summary,
					})
				}
			}
		}

		// Pull requests: on-target LinkFixes refs, or observation PR timestamps
		// when no LinkFixes row is present.
		if r.FixesTarget != "" {
			probe := Task{ID: r.TaskID, Target: r.Target}
			if prRef, err := ParsePullRequestRef(r.FixesTarget); err == nil && !PullRequestOffTarget(probe, prRef) {
				recordPR("pr:"+strings.ToLower(prRef.String()), r.PRMergedAt != nil, r.PRClosedAt != nil)
			}
		} else if r.PROpenedAt != nil || r.PRMergedAt != nil {
			recordPR("task:"+r.TaskID, r.PRMergedAt != nil, r.PRClosedAt != nil)
		}
	}

	for _, key := range prOrder {
		st := prs[key]
		sum.PullRequests.Opened++
		if st.merged {
			sum.PullRequests.Merged++
		} else if !st.closed {
			sum.PullRequests.Open++
		}
	}
	return sum
}

func isPassingCIConclusion(c string) bool {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "success", "skipped", "neutral":
		return true
	default:
		return false
	}
}

func refQuestionSummary(r RefTaskRollupRow) (string, bool) {
	if r.PendingQuestionSet {
		if s := strings.TrimSpace(r.PendingQuestionSummary); s != "" {
			return s, true
		}
		if sec := strings.TrimSpace(r.PendingSecret); sec != "" && r.PendingQuestionBody == TurnStoppedComment {
			return "set " + sec + " in the encrypted secret input", true
		}
		if r.PendingQuestionBody == TurnStoppedComment {
			return "", false
		}
		if s := DeriveQuestionSummary(r.PendingQuestionBody); s != "" {
			return s, true
		}
	}
	if sec := strings.TrimSpace(r.PendingSecret); sec != "" {
		return "set " + sec + " in the encrypted secret input", true
	}
	if s := strings.TrimSpace(r.LatestCommentSummary); s != "" {
		return s, true
	}
	if r.LatestCommentBody != "" && r.LatestCommentBody != TurnStoppedComment {
		if s := DeriveQuestionSummary(r.LatestCommentBody); s != "" {
			return s, true
		}
	}
	if s := strings.TrimSpace(r.Title); s != "" {
		return s, true
	}
	return "awaiting reply", true
}
